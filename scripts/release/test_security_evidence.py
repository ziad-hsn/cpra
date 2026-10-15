"""Security-reader regressions. Synthetic campaigns are not release evidence."""
import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

import build_campaign
import build_evidence as build
import release
import security_evidence as security
import test_build_evidence as build_fixtures


def scan_messages(compiler, roots, modules):
    return [{'config': {'protocol_version': 'v1.0.0', 'scanner_name': 'govulncheck',
                        'scanner_version': security.SCANNER_VERSION, 'db': security.DATABASE,
                        'db_last_modified': '2026-09-14T00:00:00Z', 'go_version': compiler,
                        'scan_level': 'symbol', 'scan_mode': 'source'}},
            {'progress': {'message': 'analyzing'}},
            {'SBOM': {'go_version': compiler, 'roots': list(roots), 'modules': [
                {'path': name, 'version': version} for name, version in dict(
                    modules, stdlib='v' + compiler.removeprefix('go')).items()]}}]


def exclusion_report(manifest, compiler, go):
    helper = security.exclusion
    source, binary = '/fixture/source/bin/worker-exclusion-fixture/main.go', '/fixture/source/bin/worker-exclusion-fixture/consumer'
    result = {'status': 'pass', 'evidenceClass': 'local_worker_build_exclusion', 'publicationReady': False,
              'workerExecutionQualified': False, 'workspace': 'off', 'symbolCount': len(helper.CASES),
              'compiler': 'go version ' + compiler + ' linux/amd64', 'checks': [], 'selectedSources': []}
    result['goEnvironment'] = {name: build.ENVIRONMENT.get(name, '') for name in (
        'GOTOOLCHAIN', 'GOENV', 'GOFLAGS', 'GOEXPERIMENT', 'GOWORK', 'GOPROXY', 'GOSUMDB',
        'GOPRIVATE', 'GONOSUMDB', 'GONOPROXY')}
    result['goEnvironment']['GOFLAGS'] = ''

    def row(args, **fields):
        value = dict(command=[go, *args], exitCode=0, seconds=1,
                     timeoutSeconds=helper.command_timeout(args), stdout='', stderr='', **fields)
        result['checks'].append(value)
        return value

    row(['version'])['stdout'] = result['compiler'] + '\n'
    groups = {}
    for package, symbol, declaration in helper.CASES:
        groups.setdefault(package, []).append((symbol, declaration))

    def declaration(package, declarations, tags, symbols=None, absent=False):
        contents = 'package main\nimport excluded "' + helper.MODULE + '/' + package + '"\n' + '\n'.join(declarations) + '\nfunc main() {}\n'
        value = row(['build', '-mod=readonly', '-p=1', *(['-gcflags=-e'] if absent else []), '-tags=' + tags,
                     '-o', binary, source], source=contents, sourceSHA256=hashlib.sha256(contents.encode()).hexdigest())
        if symbols is not None:
            value['expectedUnavailableSymbols' if absent else 'expectedAvailableSymbols'] = symbols
        if absent:
            value['exitCode'] = 1
            value['stderr'] = ''.join('fixture.go:3:16: undefined: excluded.' + symbol + '\n' for symbol in symbols)

    for package, declarations in groups.items():
        declaration(package, [value[1] for value in declarations], 'externaljobs', [value[0] for value in declarations])
    for tags in ('', ' '.join(release.RECIPE['build_tags'])):
        for package, symbol in (('internal/persistence', 'Store'), ('internal/httpauth', 'Authorizer'),
                                 ('internal/localadmin', 'AuthenticationRequest')):
            declaration(package, ['var _ *excluded.' + symbol], tags)
        for package, declarations in groups.items():
            declaration(package, [value[1] for value in declarations], tags, [value[0] for value in declarations], True)
        packages, selected = [], {}
        for package in sorted(set(helper.PACKAGES)):
            sdk = package.startswith('sdk/go')
            module = helper.SDK_MODULE if sdk else helper.MODULE
            filename = 'client.go' if package == 'sdk/go' else 'doc.go' if sdk else 'fixture.go'
            key = package + '/' + filename
            manifest['source_files'].setdefault(key, {'mode': '100644', 'sha256': '1' * 64})
            selected[key] = manifest['source_files'][key]['sha256']
            packages.append({'ImportPath': helper.MODULE + '/' + package, 'Module': {'Path': module}, 'GoFiles': [filename]})
        row(['list', '-mod=readonly', '-tags=' + tags, '-json', *[helper.MODULE + '/' + package for package in helper.PACKAGES]])['stdout'] = '\n'.join(json.dumps(value) for value in packages)
        result['selectedSources'].append({'tags': tags, 'sha256': selected})
        row(['list', '-mod=readonly', '-tags=' + tags, '-deps', '-f', '{{.ImportPath}}', './cmd/cpractl', '.'])['stdout'] = helper.MODULE + '\n'
        row(['test', '-mod=readonly', '-p=1', '-tags=' + tags, '-run', helper.BOUNDARY_TESTS,
             '-count=1', '-timeout=120s', './internal/httpserver', './internal/cpractl/cli'])
    return result


class SecurityEvidenceTests(unittest.TestCase):
    def setUp(self):
        fixture = build_fixtures.BuildEvidenceTests()
        self.addCleanup(fixture.doCleanups)
        fixture.setUp()
        self.fixture, self.root, self.manifest = fixture, fixture.root, fixture.manifest
        self.paths, self.reports = fixture.paths, fixture.reports
        self.exclusions, self.messages = [], {}
        for name, data in fixture.fixture.fixture.files[build.sdk_evidence.CORE].items():
            self.manifest['source_files']['sdk/go/' + name] = {'mode': '100644', 'sha256': hashlib.sha256(data).hexdigest()}
        for index, (path, report) in enumerate(zip(self.paths, self.reports)):
            artifacts = path.with_name(path.stem + '.artifacts')
            section = {'python': '/fixture/bin/python3', 'scans': []}
            report['security'] = section
            self.exclusions.append(exclusion_report(self.manifest, report['go'], report['compiler']))
            row = {'command': [section['python'], '/fixture/source/scripts/release/verify_worker_exclusion.py',
                               '--go', report['compiler'], '--workspace', 'off', '--out', str(artifacts / 'worker-exclusion.json')],
                   'exit_code': 0}
            section['exclusion'] = row
            for channel in ('stdout', 'stderr'):
                (artifacts / ('security-exclusion-' + channel + '.log')).write_bytes(b'')
            for variant, (_, _relative, module, _tags) in zip(report['variants'], build.VARIANTS):
                required = security.required_contracts(variant, module)
                names = {}
                for package, name in sorted(required | {(module, 'TestRequired')}):
                    names.setdefault(package, []).append(name)
                events, packages = [], []
                for package, tests in names.items():
                    packages.append({'ImportPath': package, 'Module': {'Path': module}, 'TestGoFiles': ['fixture_test.go']})
                    events.append(build_fixtures.event(package, 'start'))
                    for name in tests:
                        events.extend([build_fixtures.event(package, 'run', name),
                                       build_fixtures.event(package, 'pass', name, Elapsed=0.1)])
                    events.append(build_fixtures.event(package, 'pass', Elapsed=2))
                (artifacts / (variant['name'] + '-packages-stdout.log')).write_text('\n'.join(json.dumps(value) for value in packages))
                fixture.raw[index, variant['name']] = events
            if index == 1:
                scanner = '/fixture/tools/govulncheck'
                section['scanner'] = scanner
                (artifacts / 'govulncheck').write_bytes(b'synthetic scanner binary')
                (artifacts / 'scanner-build-info-stdout.log').write_text(scanner + ': ' + report['go'] + '\n'
                    '\tpath\tgolang.org/x/vuln/cmd/govulncheck\n\tmod\tgolang.org/x/vuln\tv1.7.0\th1:' + 'a' * 43 + '=\n')
                for variant, (name, relative, module, tags) in zip(report['variants'], build.VARIANTS):
                    packages = build.package_inventory(artifacts / (name + '-packages-stdout.log'), module)
                    role = name.split('-')[0]
                    modules = {value['Path']: value.get('Version', '') for value in build.worker.module_records(
                        build.worker.bounded_text(artifacts / (role + '-modules.jsonl')))}
                    self.messages[name] = scan_messages(report['go'], packages, modules)
                    row = {'name': name, 'directory': relative, 'tags': tags, 'environment': security.SCAN_ENVIRONMENT,
                           'command': security.scan_command(scanner, tags), 'exit_code': 0,
                           'packages': {'command': [report['compiler'], 'list', '-json', *(['-tags=' + tags] if tags else []), './...'],
                                        'exit_code': 0}}
                    section['scans'].append(row)
                    (artifacts / (name + '-scan-packages-stdout.log')).write_bytes((artifacts / (name + '-packages-stdout.log')).read_bytes())
                    for suffix in ('scan-packages-stderr.log', 'vulnerabilities-stderr.log'):
                        (artifacts / (name + '-' + suffix)).write_bytes(b'')
        self.save()

    def save(self):
        for index, (path, report) in enumerate(zip(self.paths, self.reports)):
            artifacts = path.with_name(path.stem + '.artifacts')
            section = report['security']
            for variant in report['variants']:
                variant['logs']['packages']['stdout'] = build_campaign.fingerprint(
                    artifacts / (variant['name'] + '-packages-stdout.log'), self.root)
            if report['checks']:
                commands = build.check_commands(report['compiler'], report['programs'], artifacts,
                                                self.manifest['source_files'])
                for check in report['checks']:
                    check['directory'], check['command'] = commands[check['name']]
            original = artifacts / 'worker-exclusion.json'
            original.write_bytes(release.encoded(self.exclusions[index]))
            section['exclusion']['report'] = build_campaign.fingerprint(original, self.root)
            for channel in ('stdout', 'stderr'):
                section['exclusion'][channel] = build_campaign.fingerprint(artifacts / ('security-exclusion-' + channel + '.log'), self.root)
            if index == 1:
                section['scanner_binary'] = build_campaign.fingerprint(artifacts / 'govulncheck', self.root)
                section['scanner_build_info'] = build_campaign.fingerprint(artifacts / 'scanner-build-info-stdout.log', self.root)
                for row in section['scans']:
                    name = row['name']
                    raw = artifacts / (name + '-vulnerabilities-stdout.log')
                    raw.write_text('\n'.join(json.dumps(value) for value in self.messages[name]))
                    for channel in ('stdout', 'stderr'):
                        row[channel] = build_campaign.fingerprint(artifacts / (name + '-vulnerabilities-' + channel + '.log'), self.root)
                        row['packages'][channel] = build_campaign.fingerprint(artifacts / (name + '-scan-packages-' + channel + '.log'), self.root)
                    role = name.split('-')[0]
                    modules = {value['Path']: value.get('Version', '') for value in build.worker.module_records(
                        build.worker.bounded_text(artifacts / (role + '-modules.jsonl')))}
                    packages = build.package_inventory(artifacts / (name + '-scan-packages-stdout.log'), next(module for variant, _, module, _ in build.VARIANTS if variant == name))
                    row['findings'] = security.scan_results(raw, report['go'], packages, modules)
        self.fixture.save()

    def verify(self):
        return security.verify_security_campaigns(self.manifest, self.paths)

    def test_complete_original_security_inputs_remain_pending_review(self):
        result = self.verify()
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])
        self.assertEqual(len(result['reports'][1]['scans']), 7)
        self.assertEqual(result['reports'][0]['exclusion']['symbols'], len(security.exclusion.CASES))
        self.assertIn(str(self.paths[1].with_name(self.paths[1].stem + '.artifacts') / 'govulncheck'), result['input_sha256'])

    def test_build_only_reports_do_not_satisfy_security(self):
        for report in self.reports:
            report.pop('security')
        self.fixture.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertEqual(len(result['unverified']), 2)

    def test_json_exit_zero_with_reachable_vulnerability_cannot_pass(self):
        messages = self.messages['application-default']
        compiler = self.reports[1]['go']
        messages.extend([{'osv': {'id': 'GO-2026-1234'}}, {'finding': {'osv': 'GO-2026-1234', 'trace': [
            {'module': 'stdlib', 'version': 'v' + compiler.removeprefix('go'), 'package': 'net/http', 'function': 'Serve'}]}}])
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertEqual(result['unverified'][-1]['findings'], ['GO-2026-1234'])
        self.assertFalse(result['release_gate_passed'])

    def test_required_and_imported_findings_stay_visible_for_review(self):
        compiler = self.reports[1]['go']
        self.messages['sdk-default'].extend([{'osv': {'id': 'GO-2026-1234'}},
            {'finding': {'osv': 'GO-2026-1234', 'trace': [{'module': 'stdlib', 'version': 'v' + compiler.removeprefix('go')}]}}])
        self.save()
        result = self.verify()
        self.assertEqual(result['reports'][1]['scans'][4]['required'], ['GO-2026-1234'])
        self.assertEqual(result['reports'][1]['scans'][4]['called'], [])
        self.assertFalse(result['release_gate_passed'])

    def test_missing_scan_changed_tags_and_narrowed_commands_cannot_pass(self):
        report = self.reports[1]['security']
        original = copy.deepcopy(report)
        cases = [lambda r: r['scans'].pop(), lambda r: r['scans'][0]['command'].__setitem__(-1, './internal/httpserver'),
                 lambda r: r['scans'][3].update(tags='externaljobs'),
                 lambda r: r['scans'][0]['environment'].update(GOSUMDB='off'),
                 lambda r: r['scans'][0].update(findings={'called': []})]
        for change in cases:
            self.reports[1]['security'] = copy.deepcopy(original)
            change(self.reports[1]['security'])
            self.fixture.save()
            with self.assertRaises(ValueError): self.verify()
        self.reports[1]['security'] = original

    def test_missing_contract_and_skipped_security_test_are_not_coverage(self):
        events = self.fixture.raw[0, 'application-default']
        before = list(events)
        name = 'TestServerStartFailsClosedWithoutCredentials'
        self.fixture.raw[0, 'application-default'] = [value for value in events if value.get('Test') != name]
        self.fixture.save()
        with self.assertRaisesRegex(ValueError, 'contracts'): self.verify()
        self.fixture.raw[0, 'application-default'] = before
        for event in before:
            if event.get('Test') == name and event['Action'] == 'pass':
                event['Action'] = 'skip'
        self.fixture.save()
        with self.assertRaisesRegex(ValueError, 'contracts'): self.verify()

    def test_exclusion_requires_every_declaration_diagnostic_and_source(self):
        original = copy.deepcopy(self.exclusions[0])
        for change in (lambda r: r.update(workspace='/developer/go.work'),
                       lambda r: r['checks'][1].update(source='package main\nfunc main() {}\n'),
                       lambda r: next(row for row in r['checks'] if 'expectedUnavailableSymbols' in row).update(stderr='missing go.sum entry'),
                       lambda r: r['checks'].pop(),
                       lambda r: r['selectedSources'][0]['sha256'].update({'sdk/go/api/doc.go': '2' * 64})):
            self.exclusions[0] = copy.deepcopy(original)
            change(self.exclusions[0])
            self.save()
            with self.assertRaises((ValueError, RuntimeError)): self.verify()
        self.exclusions[0] = original

    def test_exclusion_cold_build_budget_remains_bounded(self):
        original = copy.deepcopy(self.exclusions[0])
        compiler, go = self.reports[0]['go'], self.reports[0]['compiler']
        original['checks'][1]['seconds'] = 181
        security.verify_exclusion(original, self.manifest, compiler, go)
        for change in (
            lambda rows: rows[1].update(seconds=901),
            lambda rows: rows[1].update(timeoutSeconds=901),
            lambda rows: rows[1].update(timedOut=True),
            lambda rows: rows[0].update(seconds=181),
            lambda rows: rows[0].update(timeoutSeconds=900),
            lambda rows: rows[1].pop('timeoutSeconds'),
        ):
            changed = copy.deepcopy(original)
            change(changed['checks'])
            with self.subTest(change=change), self.assertRaises(ValueError):
                security.verify_exclusion(changed, self.manifest, compiler, go)

    def test_changed_original_bytes_and_links_are_rejected(self):
        path = self.paths[1].with_name(self.paths[1].stem + '.artifacts') / 'application-default-vulnerabilities-stdout.log'
        before = path.read_bytes()
        path.write_bytes(before + b'\n')
        with self.assertRaisesRegex(ValueError, 'changed'): self.verify()
        path.write_bytes(before)
        retained = path.with_name('real.log')
        retained.write_bytes(before)
        path.unlink(); path.symlink_to(retained)
        with self.assertRaisesRegex(ValueError, 'alias|symlink'): self.verify()

    def test_parser_rejects_alternate_scope_database_version_and_unbound_findings(self):
        compiler = self.reports[1]['go']
        roots, modules = ['example.com/fixture'], {'example.com/fixture': ''}
        path = self.root / 'scan.json'
        original = scan_messages(compiler, roots, modules)
        cases = [lambda r: r[0]['config'].update(scan_level='module'),
                 lambda r: r[0]['config'].update(scanner_version='v1.8.0'),
                 lambda r: r[0]['config'].update(db='file:///empty'),
                 lambda r: r[0]['config'].update(db_last_modified='2026-09-14'),
                 lambda r: r[-1]['SBOM'].update(roots=[]),
                 lambda r: r[-1]['SBOM']['modules'][0].update(version='v99.0.0'),
                 lambda r: r.append({'finding': {'osv': 'GO-2026-1234', 'trace': [{'module': 'stdlib', 'version': 'v1.27.1'}]}}),
                 lambda r: r.append({'error': {'message': 'scanner failed'}})]
        for change in cases:
            messages = copy.deepcopy(original); change(messages)
            path.write_text('\n'.join(json.dumps(value) for value in messages))
            with self.subTest(messages=messages), self.assertRaises(ValueError):
                security.scan_results(path, compiler, roots, modules)
        path.write_text('{"config":{},"config":{}}')
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            security.scan_results(path, compiler, roots, modules)

    def recorder(self, compiler, called=False, exclusion_error=None, exclusion_exit=0):
        output = self.root / ('recorded-' + compiler + '.json')
        artifacts = output.with_name(output.stem + '.artifacts')
        artifacts.mkdir()
        source = self.paths[1].with_name(self.paths[1].stem + '.artifacts')
        for name in ('application-modules.jsonl', 'sdk-modules.jsonl', 'worker-modules.jsonl'):
            (artifacts / name).write_bytes((source / name).read_bytes())
        scanner = self.root / 'pinned-scanner'
        scanner.write_bytes(b'synthetic scanner fixture'); scanner.chmod(0o700)
        from argparse import Namespace
        args = Namespace(out=output, govulncheck=str(scanner))
        program = '/fixture/bin/go'
        report, progress = {}, []
        shared_cache = self.root / 'shared-build-cache'
        shared_cache.mkdir(exist_ok=True)
        (shared_cache / 'retained').write_bytes(b'caller-owned cache')
        environment = {'GOCACHE': str(shared_cache)}
        cache_paths = []

        def capture(command, relative, name, environment, timeout):
            stdout, stderr = artifacts / (name + '-stdout.log'), artifacts / (name + '-stderr.log')
            stdout.write_bytes(b''); stderr.write_bytes(b'')
            if name == 'security-exclusion':
                cache = Path(environment['GOCACHE'])
                cache_paths.append(cache)
                self.assertTrue(cache.is_relative_to(artifacts))
                self.assertEqual(cache.stat().st_mode & 0o777, 0o700)
                self.assertEqual(list(cache.iterdir()), [])
                (cache / 'exclusion-object').write_bytes(b'disposable build object')
                (artifacts / 'worker-exclusion.json').write_bytes(release.encoded(
                    exclusion_report(self.manifest, compiler, program)))
                if exclusion_error is not None:
                    raise exclusion_error
            elif name == 'scanner-build-info':
                self.assertEqual(environment['GOCACHE'], str(shared_cache))
                self.assertFalse(cache_paths[0].exists())
                info = (source / 'scanner-build-info-stdout.log').read_text().splitlines()
                stdout.write_text(str(scanner) + ': ' + compiler + '\n' + '\n'.join(info[1:]) + '\n')
            elif name.endswith('-scan-packages'):
                self.assertEqual(environment['CGO_ENABLED'], '0')
                stdout.write_bytes((source / (name + '-stdout.log')).read_bytes())
            else:
                variant = name.removesuffix('-vulnerabilities')
                messages = copy.deepcopy(self.messages[variant])
                if called:
                    messages.extend([{'osv': {'id': 'GO-2026-1234'}}, {'finding': {'osv': 'GO-2026-1234', 'trace': [
                        {'module': 'stdlib', 'version': 'v' + compiler.removeprefix('go'), 'package': 'net/http', 'function': 'Serve'}]}}])
                stdout.write_text('\n'.join(json.dumps(value) for value in messages))
            return {'exit_code': exclusion_exit if name == 'security-exclusion' else 0,
                    'stdout': build_campaign.fingerprint(stdout, self.root),
                    'stderr': build_campaign.fingerprint(stderr, self.root)}
        with patch.object(build_campaign, 'executable', side_effect=lambda value: Path(value) if value != 'python3' else Path('/fixture/bin/python3')):
            security_error = None
            try:
                build_campaign.record_security(args, self.manifest, self.root / 'source', artifacts,
                    compiler, Path(program), environment, capture, report, lambda: progress.append(copy.deepcopy(report)))
            except (ValueError, TimeoutError, KeyboardInterrupt) as error:
                security_error = error
        self.assertEqual(environment, {'GOCACHE': str(shared_cache)})
        self.assertEqual((shared_cache / 'retained').read_bytes(), b'caller-owned cache')
        self.assertEqual(len(cache_paths), 1)
        self.assertNotEqual(cache_paths[0], shared_cache)
        self.assertFalse(cache_paths[0].exists())
        return report, progress, artifacts, security_error

    def test_recorder_retains_all_scanner_outputs_and_minimum_exclusion(self):
        report, progress, artifacts, error = self.recorder(release.RECIPE['go_version'])
        self.assertIsNone(error)
        self.assertEqual(len(report['security']['scans']), 7)
        self.assertGreater(len(progress), 7)
        self.assertTrue((artifacts / 'worker-external-vulnerabilities-stdout.log').is_file())
        self.assertTrue((artifacts / 'govulncheck').is_file())
        report, _, artifacts, error = self.recorder(build.MINIMUM_COMPILER)
        self.assertIsNone(error)
        self.assertEqual(report['security']['scans'], [])
        self.assertTrue((artifacts / 'worker-exclusion.json').is_file())

    def test_recorder_keeps_reachable_findings_and_stops_without_scanning_more(self):
        report, progress, artifacts, error = self.recorder(release.RECIPE['go_version'], called=True)
        self.assertIsNotNone(error)
        self.assertIn('reachable vulnerabilities', str(error))
        self.assertEqual(len(report['security']['scans']), 1)
        self.assertEqual(report['security']['scans'][0]['findings']['called'], ['GO-2026-1234'])
        self.assertTrue((artifacts / 'application-default-vulnerabilities-stdout.log').is_file())
        self.assertEqual(progress[-1]['security']['scans'][0]['findings']['called'], ['GO-2026-1234'])

    def test_exclusion_cache_is_removed_after_timeout_or_interruption(self):
        for compiler, error in ((build.MINIMUM_COMPILER, TimeoutError('fixture timeout')),
                                (release.RECIPE['go_version'], KeyboardInterrupt())):
            with self.subTest(error=type(error).__name__):
                report, _, artifacts, caught = self.recorder(compiler, exclusion_error=error)
                self.assertIs(caught, error)
                self.assertEqual(report['security']['scans'], [])
                self.assertTrue((artifacts / 'worker-exclusion.json').is_file())
                self.assertTrue((artifacts / 'security-exclusion-stdout.log').is_file())

    def test_exclusion_failure_keeps_logs_and_removes_only_its_cache(self):
        report, _, artifacts, error = self.recorder(build.MINIMUM_COMPILER, exclusion_exit=1)
        self.assertIn('worker build exclusion failed', str(error))
        self.assertEqual(report['security']['exclusion']['exit_code'], 1)
        self.assertEqual(report['security']['scans'], [])
        self.assertTrue((artifacts / 'worker-exclusion.json').is_file())
        self.assertTrue((artifacts / 'security-exclusion-stderr.log').is_file())

    def test_scanner_identity_and_repeated_database_records_are_explicit(self):
        original = (self.paths[1].with_name(self.paths[1].stem + '.artifacts') / 'scanner-build-info-stdout.log').read_text()
        for info in ('', original.replace('v1.7.0', 'v1.8.0'), original + '\t=> /local/modified-scanner\n'):
            with self.subTest(info=info), self.assertRaises(ValueError):
                security.verify_scanner_identity(info, '/fixture/tools/govulncheck')
        compiler = self.reports[1]['go']
        roots, modules = ['example.com/fixture'], {'example.com/fixture': ''}
        messages = scan_messages(compiler, roots, modules)
        messages.extend([{'osv': {'id': 'GO-2026-1234'}}, {'osv': {'id': 'GO-2026-1234'}}])
        path = self.root / 'repeated-osv.json'
        path.write_text('\n'.join(json.dumps(value) for value in messages))
        self.assertEqual(security.scan_results(path, compiler, roots, modules)['called'], [])
        messages[-1]['osv']['summary'] = 'contradictory record'
        path.write_text('\n'.join(json.dumps(value) for value in messages))
        with self.assertRaisesRegex(ValueError, 'contradictory'):
            security.scan_results(path, compiler, roots, modules)

    def test_cli_reports_incomplete_security_as_a_nonzero_result(self):
        for status, code in (('incomplete', 2), ('consistent_pending_review', 0)):
            result = {'status': status, 'release_gate_passed': False}
            output = io.StringIO()
            with patch.object(sys, 'argv',
                    ['security_evidence.py', '--release', '/fixture/RELEASE.json', '--campaign', '/fixture/build.json']), \
                    patch.object(security, 'read_report', return_value={}), \
                    patch.object(security, 'verify_security_campaigns', return_value=result), \
                    patch('sys.stdout', output):
                self.assertEqual(security.main(), code)
            self.assertEqual(json.loads(output.getvalue()), result)


if __name__ == '__main__':
    unittest.main()
