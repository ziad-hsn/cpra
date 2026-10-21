"""Build reader/recorder regressions; fixtures do not qualify a release."""
import argparse
import copy
import itertools
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import build_campaign
import management_evidence as management
import build_evidence as build
import release
import test_worker_evidence as worker_fixtures


OUTPUTS = (
    'api/generated_base.go', 'api/generated_external.go',
    'internal/transport/generated_base.go', 'internal/transport/generated_external.go',
    'api/schema_base.json', 'api/schema_external.json', 'api/schema_base.go', 'api/schema_external.go',
    'testdata/operations.json', 'generation.json',
)


def event(package, action, name=None, **fields):
    return dict(Package=package, Action=action, **({'Test': name} if name else {}), **fields)


def passed_events(package):
    return [event(package, 'start'), event(package, 'run', 'TestRequired'),
            event(package, 'pass', 'TestRequired', Elapsed=1), event(package, 'pass', Elapsed=2),
            event(package + '/empty', 'start'), event(package + '/empty', 'skip')]


def dashboard_results():
    return {'success': True, 'numTotalTests': 2, 'numPassedTests': 2, 'numFailedTests': 0,
            'numPendingTests': 0, 'numTodoTests': 0,
            'testResults': [{'name': '/fixture/Monitor.test.tsx', 'status': 'passed', 'assertionResults': [
                {'fullName': 'shows state', 'status': 'passed'}, {'fullName': 'rejects stale write', 'status': 'passed'}]}]}


class BuildEvidenceTests(unittest.TestCase):
    def setUp(self):
        fixture = worker_fixtures.WorkerEvidenceTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        self.fixture, self.root, self.manifest = fixture, fixture.root, fixture.manifest
        required = {'Makefile', 'tools/sdkgen/go.mod', 'tools/sdkgen/go.sum',
                    'scripts/release/build_campaign.py', 'scripts/release/build_evidence.py',
                    'dashboard/package.json', 'dashboard/pnpm-lock.yaml', 'dashboard/build-manifest.json'}
        for name in required:
            self.manifest['source_files'][name] = {'mode': '100644', 'sha256': '1' * 64}
        self.contract_payloads = {name: b'[]' if name.endswith('/operations.json') else b'{}'
                                  for name in management.CONTRACT_FILES}
        for name, data in self.contract_payloads.items():
            self.manifest['source_files'][name] = {'mode': '100644', 'sha256': worker_fixtures.sdk_fixtures.sha(data)}
        # Make every generated payload a real member of the synthetic SDK ZIP.
        # Fixture h1 values remain explicitly synthetic, never Go download proof.
        sdk_files = fixture.fixture.files[build.sdk_evidence.CORE]
        for name in OUTPUTS:
            sdk_files[name] = ('generated fixture ' + name).encode()
            self.manifest['source_files']['sdk/go/' + name] = {'mode': '100644', 'sha256': worker_fixtures.sdk_fixtures.sha(sdk_files[name])}
        archive = fixture.fixture.cache / 'modules/cache/download' / fixture.sdk['Path'] / '@v' / (fixture.sdk['Version'] + '.zip')
        with zipfile.ZipFile(archive, 'w') as stream:
            for name, data in sdk_files.items():
                stream.writestr(fixture.sdk['Path'] + '@' + fixture.sdk['Version'] + '/' + name, data)
        fixture.sdk['ArchiveSHA256'] = release.sha(archive)
        self.paths, self.reports, self.raw = [], [], {}
        for index, compiler in enumerate((build.MINIMUM_COMPILER, release.RECIPE['go_version'])):
            path = self.root / ('build-' + compiler + '.json')
            directory = path.with_name(path.stem + '.artifacts')
            directory.mkdir()
            original = fixture.paths[index].with_name(fixture.paths[index].stem + '.artifacts')
            for name in fixture.reports[index]['module_inputs']:
                shutil.copyfile(archive if name == 'sdk.zip' else original / name, directory / name)
            (directory / 'sdk-modules.jsonl').write_text(json.dumps({'Path': build.sdk_evidence.CORE, 'Main': True}))
            (directory / 'sdk-verify.log').write_text('all modules verified\n')
            report = copy.deepcopy(fixture.reports[index])
            report.update(compiler='/fixture/bin/go', race=True, environment=dict(build.ENVIRONMENT),
                          release_changed=False, variants=[], checks=[])
            report['sdk_module'] = dict(fixture.sdk)
            report['module_inputs'] = {name: build_campaign.fingerprint(directory / name)
                                      for name in (*fixture.reports[index]['module_inputs'], 'sdk-modules.jsonl', 'sdk-verify.log')}
            for name, relative, module, tags in build.VARIANTS:
                packages = [{'ImportPath': module, 'Module': {'Path': module}, 'TestGoFiles': ['required_test.go']},
                            {'ImportPath': module + '/empty', 'Module': {'Path': module}, 'GoFiles': ['empty.go']}]
                row = {'name': name, 'directory': relative, 'tags': tags,
                       'commands': build.commands(report['compiler'], tags),
                       'environment': dict(build.ENVIRONMENT, CPRA_CLI_BUILD_TAGS=tags), 'source_changed': [], 'logs': {}}
                report['variants'].append(row)
                for action in ('packages', 'vet', 'tests'):
                    stdout = directory / (name + '-' + action + '-stdout.log')
                    stderr = directory / (name + '-' + action + '-stderr.log')
                    stdout.write_bytes(b''); stderr.write_bytes(b'')
                    if action == 'packages':
                        stdout.write_text('\n'.join(json.dumps(value) for value in packages))
                    if action == 'tests':
                        self.raw[(index, name)] = passed_events(module)
                    row['logs'][action] = {'exit_code': 0, 'stdout': build_campaign.fingerprint(stdout, self.root),
                                          'stderr': build_campaign.fingerprint(stderr, self.root)}
            if index == 1:
                report.update(programs={'python': '/fixture/bin/python3', 'node': '/fixture/bin/node', 'pnpm': '/fixture/bin/pnpm'},
                              frontend={'node': release.RECIPE['frontend']['node_version'], 'pnpm': release.RECIPE['frontend']['pnpm_version']})
                commands = build.check_commands(report['compiler'], report['programs'], directory, self.manifest['source_files'])
                for name in build.GENERATION_CHECKS:
                    relative, command = commands[name]
                    row = {'name': name, 'directory': relative, 'command': command,
                           'environment': build.check_environment(), 'source_changed': [], 'exit_code': 0}
                    for channel in ('stdout', 'stderr'):
                        output = directory / (name + '-' + channel + '.log')
                        output.write_bytes(b'')
                        if name == 'dashboard-audit' and channel == 'stdout':
                            output.write_bytes(release.encoded({'advisories': {}, 'metadata': {
                                'dependencies': 3, 'devDependencies': 7, 'optionalDependencies': 2,
                                'totalDependencies': 10, 'vulnerabilities': {level: 0 for level in ('info', 'low', 'moderate', 'high', 'critical')}}}))
                        row[channel] = build_campaign.fingerprint(output, self.root)
                    report['checks'].append(row)
                (directory / 'generation.json').write_bytes(release.encoded({
                    'status': 'pass', 'compiler': 'go version ' + compiler + ' linux/amd64', 'sourceRoots': 2,
                    'comparedToCheckedInOutputs': True, 'outputs': {'sdk/go/' + name:
                        self.manifest['source_files']['sdk/go/' + name]['sha256'] for name in OUTPUTS}}))
                (directory / 'dashboard-tests.json').write_bytes(release.encoded(dashboard_results()))
                report['dashboard_tests'] = {'tests': 2, 'files': 1}
                report['check_results'] = {name: build_campaign.fingerprint(directory / name)
                                          for name in ('generation.json', 'dashboard-tests.json')}
            self.paths.append(path)
            self.reports.append(report)
        self.save()

    def save(self, summaries=True):
        for index, (path, report) in enumerate(zip(self.paths, self.reports)):
            directory = path.with_name(path.stem + '.artifacts')
            source = directory / 'source.json'
            source.write_bytes(release.encoded(build.source_inputs(self.manifest)))
            report.update(source_inventory=source.relative_to(self.root).as_posix(), source_inventory_sha256=release.sha(source),
                          release_identity=release.evidence_identity(self.manifest))
            (directory / 'RELEASE.json').write_bytes(release.encoded(self.manifest))
            report['release_metadata_sha256'] = release.sha(directory / 'RELEASE.json')
            for row, (_, _, module, tags) in zip(report['variants'], build.VARIANTS):
                output = directory / (row['name'] + '-tests-stdout.log')
                output.write_bytes(b''.join((json.dumps(value) + '\n').encode() for value in self.raw[(index, row['name'])]))
                row['logs']['tests']['stdout'] = build_campaign.fingerprint(output, self.root)
                if summaries:
                    row['tests'] = build.test_results(output, build.package_inventory(
                        directory / (row['name'] + '-packages-stdout.log'), module), tags)
            path.write_bytes(release.encoded(report))

    def verify(self):
        return build.verify_build_campaigns(self.manifest, self.paths)

    def test_complete_matrix_checks_original_inputs_and_keeps_publication_locked(self):
        with patch('subprocess.Popen', side_effect=AssertionError('reader must not run work')):
            result = self.verify()
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])
        self.assertEqual(result['unverified'], [])
        self.assertEqual(len(result['reports']), 2)
        for report in result['reports']:
            self.assertEqual(len(report['variants']), 7)
            self.assertTrue(all(row['race'] for row in report['variants']))
        self.assertIn(str(self.paths[1].with_name(self.paths[1].stem + '.artifacts') / 'dashboard-tests.json'), result['input_sha256'])

    def test_go_only_report_cannot_satisfy_generation_or_frontend_requirements(self):
        self.reports[1]['checks'] = []
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertIn({'compiler': release.RECIPE['go_version'], 'requirement': 'generation and dashboard checks'}, result['unverified'])

    def test_unexpected_skip_is_visible_instead_of_a_green_matrix(self):
        self.raw[(0, build.VARIANTS[0][0])][2]['Action'] = 'skip'
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertEqual(result['unverified'][0]['test'], 'TestRequired')
        self.assertFalse(result['release_gate_passed'])

    def test_known_subprocess_helper_and_separately_required_browser_are_explicit(self):
        name = build.VARIANTS[0][0]
        package = release.RECIPE['module']
        events = self.raw[(0, name)]
        for test in ('TestMainWorkerServerProcessHelper', 'TestMainManagementControlsBrowser'):
            events[1:1] = [event(package, 'run', test), event(package, 'skip', test)]
        self.save()
        result = self.verify()
        skipped = result['reports'][0]['variants'][0]['skipped']
        self.assertEqual({row['covered_by'] for row in skipped}, {'subprocess_helper', 'browser'})
        self.assertIn('browser', result['companion_requirements'])

    def test_missing_combined_variant_or_narrowed_commands_cannot_pass(self):
        for change in (lambda r: r['variants'].pop(3),
                       lambda r: r['variants'][3]['commands']['tests'].extend(['-run', 'TestSmall']),
                       lambda r: r['variants'][6]['commands']['tests'].remove('-race'),
                       lambda r: r['variants'][3].update(tags='externaljobs')):
            with self.subTest(change=change):
                original = copy.deepcopy(self.reports[0])
                change(self.reports[0]); self.save(summaries=False)
                with self.assertRaises(ValueError): self.verify()
                self.reports[0] = original
        self.save()

    def test_failures_missing_package_completion_and_duplicate_completions_are_rejected(self):
        key = (0, build.VARIANTS[0][0])
        for change in (lambda events: events[2].update(Action='fail'),
                       lambda events: events.pop(3),
                       lambda events: events.insert(3, copy.deepcopy(events[2])),
                       lambda events: events[2].update(Elapsed=-1),
                       lambda events: events[2].update(Package='outside/module')):
            with self.subTest(change=change):
                original = copy.deepcopy(self.raw[key])
                change(self.raw[key]); self.save(summaries=False)
                with self.assertRaises(ValueError): self.verify()
                self.raw[key] = original
        self.save()

    def test_foreign_module_packages_and_replaced_dependencies_are_rejected(self):
        directory = self.paths[0].with_name(self.paths[0].stem + '.artifacts')
        graph = directory / 'sdk-modules.jsonl'
        graph.write_text(json.dumps({'Path': build.sdk_evidence.CORE, 'Main': True, 'Replace': {'Path': '/tmp/private'}}))
        self.reports[0]['module_inputs'][graph.name] = build_campaign.fingerprint(graph)
        self.save()
        with self.assertRaisesRegex(ValueError, 'replacement'): self.verify()
        packages = directory / 'packages.log'
        packages.write_text(json.dumps({'ImportPath': 'foreign/module', 'Module': {'Path': 'foreign/module'}}))
        with self.assertRaisesRegex(ValueError, 'module boundary'): build.package_inventory(packages, release.RECIPE['module'])

    def test_source_compiler_workspace_and_checksum_overrides_cannot_pass(self):
        for change in (lambda r: r.update(source_changed=['main.go']),
                       lambda r: r.update(go='go1.26.1'),
                       lambda r: r.update(workspace='/private/go.work'),
                       lambda r: r['environment'].update(GONOSUMDB=build.sdk_evidence.CORE),
                       lambda r: r['variants'][0]['environment'].update(GOPROXY='https://private.invalid'),
                       lambda r: r.update(source_inventory_sha256=None),
                       lambda r: r.update(release_changed=True),
                       lambda r: r.update(seconds=1),
                       lambda r: r.update(release_qualified=True)):
            with self.subTest(change=change):
                original = copy.deepcopy(self.reports[0])
                change(self.reports[0]); self.paths[0].write_bytes(release.encoded(self.reports[0]))
                with self.assertRaises(ValueError): self.verify()
                self.reports[0] = original
                self.save()

    def test_recorded_duration_stays_within_the_campaign_deadline(self):
        self.reports[0]['seconds'] = build.MAX_CAMPAIGN_SECONDS
        self.save()
        self.verify()
        for seconds in (build.MAX_CAMPAIGN_SECONDS + 0.1, float('inf'), float('nan')):
            with self.subTest(seconds=seconds):
                self.reports[0]['seconds'] = seconds
                self.save()
                with self.assertRaises(ValueError):
                    self.verify()

    def recorder_fixture(self, compiler, output):
        root = self.root / 'recorded-source'
        (root / 'sdk/go/worker').mkdir(parents=True, exist_ok=True)
        for name, data in self.contract_payloads.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        go = self.root / 'recorded-go'
        go.write_bytes(b'fixture compiler, never executed by this test')
        go.chmod(0o700)
        manifest = self.root / 'recorded-RELEASE.json'
        manifest.write_bytes(release.encoded(self.manifest))
        source_directory = self.paths[1].with_name(self.paths[1].stem + '.artifacts')
        cache = self.fixture.fixture.cache
        dependency = self.fixture.fixture.dependency
        dependency['release_identity'] = release.evidence_identity(self.manifest)
        self.fixture.fixture.save()
        args = argparse.Namespace(source_dir=root, release=manifest, dependencies=cache, go=str(go), pnpm='pnpm', out=output)

        def observe(command, **_):
            if command[1] == 'env':
                return json.dumps({'GOVERSION': compiler, 'GOOS': 'linux', 'GOARCH': 'amd64'})
            return release.RECIPE['frontend']['node_version' if command[0].endswith('node') else 'pnpm_version'] + '\n'

        def capture(command, cwd, environment, stdout, stderr, timeout):
            self.assertGreater(timeout, 0)
            self.assertEqual(environment['GOWORK'], 'off')
            stderr.write_bytes(b'')
            module = (build.sdk_evidence.WORKER if cwd.name == 'worker' else
                      build.sdk_evidence.CORE if cwd.name == 'go' else release.RECIPE['module'])
            if command[1:3] == ['list', '-m']:
                role = 'worker' if cwd.name == 'worker' else 'sdk' if cwd.name == 'go' else 'application'
                stdout.write_bytes((source_directory / (role + '-modules.jsonl')).read_bytes())
            elif command[1] == 'mod':
                stdout.write_bytes(b'all modules verified\n')
            elif command[1] == 'list':
                stdout.write_text('\n'.join(json.dumps(row) for row in (
                    {'ImportPath': module, 'Module': {'Path': module}, 'TestGoFiles': ['required_test.go']},
                    {'ImportPath': module + '/empty', 'Module': {'Path': module}, 'GoFiles': ['empty.go']})))
            elif command[1] == 'test':
                stdout.write_bytes(b''.join((json.dumps(row) + '\n').encode() for row in passed_events(module)))
            elif command[1] == 'audit':
                self.assertEqual(environment['NODE_TLS_REJECT_UNAUTHORIZED'], '1')
                self.assertEqual(environment['npm_config_strict_ssl'], 'true')
                for option in ('--only=', '--prod=false', '--dev=false', '--optional=true',
                               '--ignore-registry-errors=false', '--ignore-unfixable=false'):
                    self.assertIn(option, command)
                stdout.write_bytes((source_directory / 'dashboard-audit-stdout.log').read_bytes())
            else:
                stdout.write_bytes(b'')
                if 'scripts/sdk/regenerate_check.py' in command:
                    shutil.copyfile(source_directory / 'generation.json', Path(command[-1]))
                if 'vitest' in command:
                    target = Path(next(value.split('=', 1)[1] for value in command if value.startswith('--outputFile=')))
                    shutil.copyfile(source_directory / 'dashboard-tests.json', target)
            return 0

        return args, observe, capture

    def test_recorder_round_trip_retains_reader_compatible_original_results(self):
        outputs = []
        for compiler in (build.MINIMUM_COMPILER, release.RECIPE['go_version']):
            output = self.root / 'recorded' / ('build-' + compiler + '.json')
            args, observe, capture = self.recorder_fixture(compiler, output)
            native_executable = build_campaign.executable
            def executable(name):
                return Path('/fixture/bin/' + name) if name in ('node', 'pnpm') else native_executable(name)
            with patch.object(build, 'source_snapshot', return_value=build.source_inputs(self.manifest)), \
                    patch.object(build_campaign.dependencies, 'cache_environment', return_value={
                        'GOMODCACHE': str(self.fixture.fixture.cache / 'modules'), 'GOPATH': str(self.fixture.fixture.cache / 'go-path')}), \
                    patch.object(build_campaign.subprocess, 'check_output', side_effect=observe), \
                    patch.object(build_campaign.worker_campaign, 'capture', side_effect=capture), \
                    patch.object(build_campaign, 'executable', side_effect=executable), \
                    patch.object(build_campaign.time, 'monotonic', side_effect=itertools.count(0, 20)):
                result = build_campaign.campaign(args)
            self.assertEqual(result['result'], 'passed')
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            self.assertEqual(set(result['contract_inputs']), set(management.CONTRACT_FILES))
            for name, record in result['contract_inputs'].items():
                path = output.parent / record['file']
                self.assertEqual(path.read_bytes(), self.contract_payloads[name])
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            outputs.append(output)
        result = build.verify_build_campaigns(self.manifest, outputs)
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])

    def test_recorder_rejects_exit_zero_with_invalid_or_filtered_audit_output(self):
        source = self.paths[1].with_name(self.paths[1].stem + '.artifacts') / 'dashboard-audit-stdout.log'
        filtered = json.loads(source.read_bytes())
        filtered['metadata']['vulnerabilities']['high'] = 1
        subset = json.loads(source.read_bytes())
        subset['metadata']['devDependencies'] = 0
        for index, payload in enumerate((b'ignored registry error', release.encoded(filtered), release.encoded(subset))):
            with self.subTest(payload=payload):
                output = self.root / ('invalid-audit-' + str(index) + '.json')
                args, observe, capture = self.recorder_fixture(release.RECIPE['go_version'], output)
                native_executable = build_campaign.executable
                def executable(name):
                    return Path('/fixture/bin/' + name) if name in ('node', 'pnpm') else native_executable(name)

                def invalid_audit(command, cwd, environment, stdout, stderr, timeout):
                    status = capture(command, cwd, environment, stdout, stderr, timeout)
                    if command[1] == 'audit':
                        stdout.write_bytes(payload)
                    return status

                with patch.object(build, 'source_snapshot', return_value=build.source_inputs(self.manifest)), \
                        patch.object(build_campaign.dependencies, 'cache_environment', return_value={
                            'GOMODCACHE': str(self.fixture.fixture.cache / 'modules'), 'GOPATH': str(self.fixture.fixture.cache / 'go-path')}), \
                        patch.object(build_campaign.subprocess, 'check_output', side_effect=observe), \
                        patch.object(build_campaign.worker_campaign, 'capture', side_effect=invalid_audit), \
                        patch.object(build_campaign, 'executable', side_effect=executable), \
                        patch.object(build_campaign.time, 'monotonic', side_effect=itertools.count(0, 20)):
                    result = build_campaign.campaign(args)
                self.assertEqual(result['result'], 'failed')
                self.assertEqual(result['checks'][-1]['name'], 'dashboard-audit')
                self.assertEqual(result['checks'][-1]['exit_code'], 0)
                retained = output.with_name(output.stem + '.artifacts') / 'dashboard-audit-stdout.log'
                self.assertEqual(retained.read_bytes(), payload)
                self.assertEqual(result['checks'][-1]['stdout'], build_campaign.fingerprint(retained, output.parent))
                self.assertEqual(json.loads(output.read_bytes())['result'], 'failed')

    def test_security_rejection_stops_before_test_matrix(self):
        output = self.root / 'security-failed.json'
        args, observe, capture = self.recorder_fixture(release.RECIPE['go_version'], output)
        args.security = True
        commands = []

        def record_capture(command, *arguments):
            commands.append(command)
            return capture(command, *arguments)

        with patch.object(build, 'source_snapshot', return_value=build.source_inputs(self.manifest)), \
                patch.object(build_campaign.dependencies, 'cache_environment', return_value={
                    'GOMODCACHE': str(self.fixture.fixture.cache / 'modules'), 'GOPATH': str(self.fixture.fixture.cache / 'go-path')}), \
                patch.object(build_campaign.subprocess, 'check_output', side_effect=observe), \
                patch.object(build_campaign.worker_campaign, 'capture', side_effect=record_capture), \
                patch.object(build_campaign, 'record_security', side_effect=ValueError('reachable vulnerabilities were found')):
            result = build_campaign.campaign(args)
        self.assertEqual(result['result'], 'failed')
        self.assertEqual(result['error'], 'reachable vulnerabilities were found')
        self.assertEqual(result['variants'], [])
        self.assertEqual(len([command for command in commands if command[1:3] == ['mod', 'verify']]), 3)
        self.assertFalse(any(command[1] in ('vet', 'test') for command in commands))
        self.assertEqual(json.loads(output.read_bytes())['result'], 'failed')

    def test_recorder_allows_package_queueing_without_changing_package_timeout(self):
        output = self.root / 'queued-packages.json'
        args, observe, capture = self.recorder_fixture(build.MINIMUM_COMPILER, output)
        clock, application_runs = [0], []

        def queued_capture(command, cwd, environment, stdout, stderr, timeout):
            if command[1] == 'test':
                self.assertIn('-timeout=45m', command)
                self.assertEqual(timeout, build_campaign.TEST_COMMAND_SECONDS)
                if cwd == args.source_dir:
                    application_runs.append(command)
                    clock[0] += 50 * 60
            return capture(command, cwd, environment, stdout, stderr, timeout)

        with patch.object(build, 'source_snapshot', return_value=build.source_inputs(self.manifest)), \
                patch.object(build_campaign.dependencies, 'cache_environment', return_value={
                    'GOMODCACHE': str(self.fixture.fixture.cache / 'modules'), 'GOPATH': str(self.fixture.fixture.cache / 'go-path')}), \
                patch.object(build_campaign.subprocess, 'check_output', side_effect=observe), \
                patch.object(build_campaign.worker_campaign, 'capture', side_effect=queued_capture), \
                patch.object(build_campaign.time, 'monotonic', side_effect=lambda: clock[0]):
            result = build_campaign.campaign(args)
        self.assertEqual(result['result'], 'passed')
        self.assertEqual(len(application_runs), 4)
        self.assertEqual(result['seconds'], 200 * 60)

    def test_recorder_caps_commands_at_remaining_time_and_preserves_timeout_failure(self):
        output = self.root / 'deadline-expired.json'
        args, observe, capture = self.recorder_fixture(build.MINIMUM_COMPILER, output)
        clock, limits = [0], []

        def timed_capture(command, cwd, environment, stdout, stderr, timeout):
            limits.append(timeout)
            clock[0] = build.MAX_CAMPAIGN_SECONDS - 10 if len(limits) == 1 else build.MAX_CAMPAIGN_SECONDS + 1
            return capture(command, cwd, environment, stdout, stderr, timeout)

        with patch.object(build, 'source_snapshot', return_value=build.source_inputs(self.manifest)), \
                patch.object(build_campaign.dependencies, 'cache_environment', return_value={
                    'GOMODCACHE': str(self.fixture.fixture.cache / 'modules'), 'GOPATH': str(self.fixture.fixture.cache / 'go-path')}), \
                patch.object(build_campaign.subprocess, 'check_output', side_effect=observe), \
                patch.object(build_campaign.worker_campaign, 'capture', side_effect=timed_capture), \
                patch.object(build_campaign.time, 'monotonic', side_effect=lambda: clock[0]):
            result = build_campaign.campaign(args)
        self.assertEqual(limits, [120, 10])
        self.assertEqual(result['result'], 'failed')
        self.assertEqual(result['error'], 'Incomplete or inconsistent build matrix evidence: matrix exceeded its five-hour process deadline')
        self.assertEqual(json.loads(output.read_bytes())['result'], 'failed')
        self.assertEqual(result['variants'], [])
        self.assertTrue((output.with_name(output.stem + '.artifacts') / 'application-verify-stdout.log').is_file())

    def test_recorder_preserves_failed_final_source_inspection(self):
        output = self.root / 'source-failed.json'
        args, observe, capture = self.recorder_fixture(build.MINIMUM_COMPILER, output)
        with patch.object(build, 'source_snapshot', side_effect=[build.source_inputs(self.manifest),
                    ValueError('source changed to symlink'), ValueError('source remains unreadable')]), \
                patch.object(build_campaign.dependencies, 'cache_environment', return_value={
                    'GOMODCACHE': str(self.fixture.fixture.cache / 'modules'), 'GOPATH': str(self.fixture.fixture.cache / 'go-path')}), \
                patch.object(build_campaign.subprocess, 'check_output', side_effect=observe), \
                patch.object(build_campaign.worker_campaign, 'capture', side_effect=capture), \
                patch.object(build_campaign.time, 'monotonic', side_effect=itertools.count(0, 20)):
            result = build_campaign.campaign(args)
        self.assertEqual(result['result'], 'failed')
        self.assertEqual(result['source_snapshot_error'], 'source remains unreadable')
        self.assertEqual(json.loads(output.read_bytes())['result'], 'failed')
        self.assertTrue((output.with_name(output.stem + '.artifacts') / 'application-default-tests-stdout.log').is_file())

    def test_changed_raw_bytes_summary_and_oversized_output_cannot_pass(self):
        path = self.paths[0].with_name(self.paths[0].stem + '.artifacts') / 'application-default-tests-stdout.log'
        before = path.read_bytes()
        path.write_bytes(before + b'\n')
        with self.assertRaisesRegex(ValueError, 'changed'): self.verify()
        path.write_bytes(before)
        self.reports[0]['variants'][0]['tests']['passed'] = 100
        self.save(summaries=False)
        with self.assertRaisesRegex(ValueError, 'contradicts'): self.verify()
        self.save()
        with patch.object(build, 'MAX_LOG_BYTES', 4), self.assertRaisesRegex(ValueError, 'byte limit'): self.verify()

    def test_dashboard_skip_generation_subset_or_narrowed_frontend_command_cannot_pass(self):
        directory = self.paths[1].with_name(self.paths[1].stem + '.artifacts')
        for filename, change in (
            ('dashboard-tests.json', lambda r: r.update(numPendingTests=1)),
            ('dashboard-tests.json', lambda r: r['testResults'][0]['assertionResults'][0].update(status='skipped')),
            ('generation.json', lambda r: r.update(sourceRoots=1)),
            ('generation.json', lambda r: r['outputs'].pop('sdk/go/api/generated_external.go'))):
            path = directory / filename
            before = path.read_bytes()
            data = json.loads(before); change(data); path.write_bytes(release.encoded(data))
            self.reports[1]['check_results'][filename] = build_campaign.fingerprint(path)
            self.save()
            with self.subTest(filename=filename), self.assertRaises(ValueError): self.verify()
            path.write_bytes(before)
            self.reports[1]['check_results'][filename] = build_campaign.fingerprint(path)
        self.reports[1]['checks'][-2]['command'].append('Monitor.test.tsx')
        self.save()
        with self.assertRaisesRegex(ValueError, 'command'): self.verify()

    def test_frontend_audit_must_be_present_and_report_zero_advisories(self):
        directory = self.paths[1].with_name(self.paths[1].stem + '.artifacts')
        row = next(row for row in self.reports[1]['checks'] if row['name'] == 'dashboard-audit')
        output = directory / 'dashboard-audit-stdout.log'
        original = output.read_bytes()
        for change in (
                lambda data: data['metadata']['vulnerabilities'].update(high=1),
                lambda data: data['metadata']['vulnerabilities'].update(low=False),
                lambda data: data['metadata']['vulnerabilities'].pop('critical'),
                lambda data: data['metadata'].update(totalDependencies=0),
                lambda data: data['metadata'].update(dependencies=0),
                lambda data: data['metadata'].update(devDependencies=0),
                lambda data: data['metadata'].update(optionalDependencies=0),
                lambda data: data['metadata'].update(devDependencies=True),
                lambda data: data['metadata'].update(devDependencies=11),
                lambda data: data['metadata'].pop('devDependencies'),
                lambda data: data.update(advisories={'id': {'severity': 'high'}}),
                lambda data: data.pop('metadata')):
            data = json.loads(original); change(data)
            output.write_bytes(release.encoded(data))
            row['stdout'] = build_campaign.fingerprint(output, self.root)
            self.save()
            with self.assertRaisesRegex(ValueError, 'frontend dependency audit'): self.verify()
        output.write_bytes(original); row['stdout'] = build_campaign.fingerprint(output, self.root)
        self.reports[1]['checks'].remove(row)
        self.save()
        with self.assertRaisesRegex(ValueError, 'coverage'): self.verify()

    def test_symlink_raw_input_and_duplicate_json_fields_fail(self):
        directory = self.paths[0].with_name(self.paths[0].stem + '.artifacts')
        path = directory / 'application-default-vet-stdout.log'
        target = directory / 'real.log'; target.write_bytes(path.read_bytes()); path.unlink(); path.symlink_to(target)
        with self.assertRaisesRegex(ValueError, 'symlink|alias'): self.verify()
        path.unlink(); path.write_bytes(b'')
        raw = directory / 'duplicate.log'
        raw.write_bytes(b'{"Action":"pass","Action":"skip","Package":"github.com/ziad-hsn/cpra"}\n')
        with self.assertRaisesRegex(ValueError, 'duplicate'): build.test_results(raw, {release.RECIPE['module']: False}, '')

    def test_snapshot_includes_metadata_and_new_inputs_without_following_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'RELEASE.json').write_bytes(b'{}')
            (root / 'main.go').write_bytes(b'package main')
            original = build.source_snapshot(root)
            self.assertIn('RELEASE.json', original)
            (root / 'new.go').write_bytes(b'package main')
            self.assertNotEqual(build.source_snapshot(root), original)
            (root / 'node_modules').mkdir(); (root / 'node_modules/ignored').write_bytes(b'generated dependency')
            self.assertNotIn('node_modules/ignored', build.source_snapshot(root))
            (root / 'linked.go').symlink_to(root / 'main.go')
            with self.assertRaisesRegex(ValueError, 'regular'): build.source_snapshot(root)

    def test_recorder_refuses_existing_output_or_unfrozen_source_before_running_commands(self):
        out = self.root / 'existing.json'; out.write_bytes(b'original evidence')
        args = argparse.Namespace(source_dir=self.root / 'source', dependencies=self.root / 'cache',
                                  release=self.root / 'RELEASE.json', out=out, go='/fixture/go', pnpm='pnpm')
        with patch.object(build_campaign, 'read_report', side_effect=AssertionError('input consumed')):
            with self.assertRaisesRegex(ValueError, 'output exists'): build_campaign.campaign(args)
        self.assertEqual(out.read_bytes(), b'original evidence')
        args.out = self.root / 'new.json'; args.release.write_bytes(release.encoded(self.manifest))
        with patch.object(build, 'source_snapshot', return_value={}), \
                patch.object(build_campaign.subprocess, 'check_output', side_effect=AssertionError('compiler invoked')):
            with self.assertRaisesRegex(ValueError, 'source differs'): build_campaign.campaign(args)

    def test_real_go_parallel_events_and_package_without_tests_are_understood(self):
        go = shutil.which(os.environ.get('GO', 'go'))
        if go is None:
            self.fail('Go must be available for the native event-format regression')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'go.mod').write_text('module github.com/ziad-hsn/cpra\n\ngo 1.26.10\n')
            (root / 'fixture_test.go').write_text('''package fixture
import "testing"
func TestA(t *testing.T) { t.Parallel(); t.Run("sub",func(t *testing.T) {}) }
func TestB(t *testing.T) { t.Parallel(); t.Run("sub",func(t *testing.T) {}) }
func TestUnavailable(t *testing.T) { t.Skip("deliberate fixture coverage gap") }
''')
            (root / 'empty').mkdir(); (root / 'empty/empty.go').write_text('package empty\n')
            environment = {name: os.environ[name] for name in ('PATH', 'HOME', 'GOCACHE', 'GOMODCACHE', 'GOPATH', 'TMPDIR')
                           if name in os.environ}
            environment.update(GOENV='off', GOWORK='off', GOTOOLCHAIN='local', GOFLAGS='-mod=readonly',
                               GOPROXY='off', GOSUMDB='sum.golang.org', GONOSUMDB='', GONOPROXY='', GOPRIVATE='')
            inventory = subprocess.check_output([go, 'list', '-json', './...'], cwd=root, env=environment, timeout=60)
            packages = root / 'packages.log'; packages.write_bytes(inventory)
            original = subprocess.check_output([go, 'test', '-json', '-count=1', './...'], cwd=root, env=environment, timeout=90)
            log = root / 'tests.log'; log.write_bytes(original)
            result = build.test_results(log, build.package_inventory(packages, release.RECIPE['module']), '')
            self.assertEqual(result['packages'], 2)
            self.assertEqual(result['passed'], 4)
            self.assertEqual(result['unverified_skips'][0]['test'], 'TestUnavailable')


if __name__ == '__main__':
    unittest.main()
