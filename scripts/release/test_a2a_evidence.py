"""Synthetic A2A recordings test reader boundaries, never agent qualification."""
import argparse
import copy
import io
import json
from pathlib import Path
import re
import sys
import unittest
from unittest.mock import patch

import a2a_evidence as a2a
import build_campaign
import build_evidence as build
import release
import test_build_evidence as build_fixtures
import test_provider_evidence as provider_fixtures


class A2AEvidenceTests(unittest.TestCase):
    def setUp(self):
        fixture = build_fixtures.BuildEvidenceTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        self.fixture, self.root, self.manifest = fixture, fixture.root, fixture.manifest
        self.paths, self.reports = fixture.paths, fixture.reports
        # These are real frozen input bytes, while all commands/events/executables
        # below are explicitly synthetic. They must never be release evidence.
        self.sources = {name: (release.ROOT / name).read_bytes() for name in a2a.REFERENCE_FILES}
        for name, data in self.sources.items():
            self.manifest['source_files'][name] = {'mode': '100644', 'sha256': build_fixtures.worker_fixtures.sdk_fixtures.sha(data)}
        self.sums = {tuple(line.split()[:2]): line.split()[2]
                     for line in self.sources[a2a.REFERENCE_DIRECTORY + '/go.sum'].decode().splitlines()}
        self.reference_logs = {}
        self.interop = {}
        module = release.RECIPE['module']
        contracts = a2a.required_contracts()
        for index, (path, report) in enumerate(zip(self.paths, self.reports)):
            artifacts = path.with_name(path.stem + '.artifacts')
            for row in report['variants'][:4]:
                packages, events = [], []
                selected = {module, *(package for package, _ in contracts)}
                for package in sorted(selected):
                    packages.append({'ImportPath': package, 'Module': {'Path': module}, 'TestGoFiles': ['required_test.go']})
                    events.append(build_fixtures.event(package, 'start'))
                    names = {name for owner, name in contracts if owner == package}
                    if package == module:
                        names.add('TestRequired')
                    for name in sorted(names):
                        events.extend([build_fixtures.event(package, 'run', name),
                                       build_fixtures.event(package, 'pass', name, Elapsed=1)])
                    events.append(build_fixtures.event(package, 'pass', Elapsed=2))
                self.fixture.raw[(index, row['name'])] = events
                package_log = artifacts / (row['name'] + '-packages-stdout.log')
                package_log.write_text('\n'.join(json.dumps(package) for package in packages))
                row['logs']['packages']['stdout'] = build_campaign.fingerprint(package_log, self.root)
            directory = artifacts / 'a2a-source'
            directory.mkdir()
            sources = {}
            for name, data in self.sources.items():
                payload = directory / Path(name).name
                payload.write_bytes(data)
                sources[name] = build_campaign.fingerprint(payload, self.root)
            binary = artifacts / 'a2a-agent'
            binary.write_bytes(b'synthetic A2A executable: not a native execution')
            section = {'compiler': '/fixture/reference/bin/go', 'directory': a2a.REFERENCE_DIRECTORY,
                       'binary_path': str(binary), 'environment': dict(a2a.REFERENCE_ENVIRONMENT),
                       'source_files': sources, 'source_changed': [], 'commands': [],
                       'binary': build_campaign.fingerprint(binary, self.root)}
            report['a2a_reference'] = section
            commands = a2a.reference_commands(section['compiler'])
            commands['build'][-2] = str(binary)
            commands['build-info'][-1] = str(binary)
            graph = [{'Path': a2a.REFERENCE_MODULE, 'Main': True},
                     {'Path': a2a.REFERENCE_SDK, 'Version': a2a.REFERENCE_VERSION,
                      'Sum': self.sums[(a2a.REFERENCE_SDK, a2a.REFERENCE_VERSION)],
                      'GoModSum': self.sums[(a2a.REFERENCE_SDK, a2a.REFERENCE_VERSION + '/go.mod')]}]
            settings = {'GOOS': 'linux', 'GOARCH': 'amd64', 'CGO_ENABLED': '0', 'GOAMD64': 'v1',
                        '-trimpath': 'true', '-buildmode': 'exe', '-compiler': 'gc'}
            info = str(binary) + ': ' + release.RECIPE['go_version'] + '\n\tpath\t' + a2a.REFERENCE_MODULE + '\n'
            info += '\tdep\t' + a2a.REFERENCE_SDK + '\t' + a2a.REFERENCE_VERSION + '\t' + graph[1]['Sum'] + '\n'
            info += ''.join('\tbuild\t' + key + '=' + value + '\n' for key, value in settings.items())
            outputs = {
                'environment': json.dumps({'GOVERSION': release.RECIPE['go_version'], 'GOHOSTOS': 'linux',
                                           'GOHOSTARCH': 'amd64', 'GOOS': 'linux', 'GOARCH': 'amd64', 'GOWORK': 'off'}),
                'verify': 'all modules verified\n',
                'modules': '\n'.join(json.dumps(value) for value in graph),
                'packages': json.dumps({'ImportPath': a2a.REFERENCE_MODULE, 'Module': {'Path': a2a.REFERENCE_MODULE},
                                       'Name': 'main', 'GoFiles': ['main.go']}),
                'build-info': info,
            }
            for name, command in commands.items():
                self.reference_logs[(index, name)] = outputs.get(name, '')
                section['commands'].append({'name': name, 'command': command, 'exit_code': 0})
            section['tests'] = {'environment': dict(a2a.REFERENCE_TEST_ENVIRONMENT, CPRA_A2A_AGENT=str(binary))}
            for name, command in a2a.reference_test_commands(report['compiler']).items():
                section['tests'][name] = {'command': command, 'exit_code': 0}
            package = module + '/internal/jobs'
            events = [build_fixtures.event(package, 'start')]
            for name in sorted({a2a.REFERENCE_TEST, *(a2a.REFERENCE_TEST + '/' + path + '/' + scenario
                                                   for path in a2a.PATHS for scenario in a2a.SCENARIOS)}):
                events.extend([build_fixtures.event(package, 'run', name),
                               build_fixtures.event(package, 'pass', name, Elapsed=1)])
            events.append(build_fixtures.event(package, 'pass', Elapsed=2))
            self.interop[index] = events
        self.save()

    def save(self):
        for index, (path, report) in enumerate(zip(self.paths, self.reports)):
            if report.get('checks'):
                commands = build.check_commands(report['compiler'], report['programs'],
                                                path.with_name(path.stem + '.artifacts'), self.manifest['source_files'])
                for row in report['checks']:
                    row['directory'], row['command'] = commands[row['name']]
            section = report.get('a2a_reference')
            if not isinstance(section, dict):
                continue
            artifacts = path.with_name(path.stem + '.artifacts')
            for row in section['commands']:
                for channel in ('stdout', 'stderr'):
                    output = artifacts / ('a2a-reference-' + row['name'] + '-' + channel + '.log')
                    output.write_text(self.reference_logs[(index, row['name'])] if channel == 'stdout' else '')
                    row[channel] = build_campaign.fingerprint(output, self.root)
            for name in ('packages', 'tests'):
                for channel in ('stdout', 'stderr'):
                    output = artifacts / ('a2a-interop-' + name + '-' + channel + '.log')
                    output.write_bytes(b'')
                    if channel == 'stdout' and name == 'packages':
                        output.write_text(json.dumps({'ImportPath': release.RECIPE['module'] + '/internal/jobs',
                                                     'Module': {'Path': release.RECIPE['module']},
                                                     'TestGoFiles': ['a2a_interop_test.go', 'a2a_test.go']}))
                    elif channel == 'stdout':
                        output.write_bytes(b''.join((json.dumps(event) + '\n').encode() for event in self.interop[index]))
                    section['tests'][name][channel] = build_campaign.fingerprint(output, self.root)
        self.fixture.save()

    def provider(self, kinds=('intervention', 'code'), category='live_account', boundary='effect'):
        fixture = provider_fixtures.ProviderEvidenceTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        for name in a2a.provider.SOURCE_REQUIREMENTS:
            self.manifest['source_files'].setdefault(name, {'mode': '100644', 'sha256': 'c' * 64})
        fixture.manifest = self.manifest
        fixture.build['release_identity'] = release.evidence_identity(self.manifest)
        fixture.build['source_files'] = copy.deepcopy(self.manifest['source_files'])
        fixture.save_build()
        fixture.write_campaign([(kind, 'a2a') for kind in kinds], category=category, boundary=boundary)
        self.save()
        return fixture

    def verify(self, provider=None):
        return a2a.verify_a2a_campaigns(self.manifest, self.paths,
                                      provider.build_directory if provider else None, [provider.path] if provider else ())

    def test_protocol_and_lifecycle_do_not_establish_designated_agent_effects(self):
        with patch('subprocess.Popen', side_effect=AssertionError('reader must not execute work')):
            result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertFalse(result['release_gate_passed'])
        self.assertFalse(result['formal_vendor_certification'])
        self.assertEqual(result['unverified'], [{'requirement': 'configured agent effect', 'kind': kind, 'driver': 'a2a'}
                                               for kind in ('intervention', 'code')])
        for report in result['reports']:
            self.assertEqual(report['reference']['scenarios'], 21)
            self.assertEqual(report['reference']['evidence_class'], 'upstream_protocol_fixture')
            self.assertEqual(report['reference']['coverage']['passed'], 22)
            self.assertTrue(all(row['passed'] == row['required'] for row in report['lifecycle_contracts']))
        self.assertIn(str(self.paths[0].with_name(self.paths[0].stem + '.artifacts') / 'a2a-agent'), result['input_sha256'])

    def test_required_named_contracts_exist_in_the_actual_application_sources(self):
        # Catch stale/misspelled evidence requirements before a real campaign.
        # This validates selection only, never substitutes for execution.
        for package, names in a2a.CONTRACTS.items():
            sources = '\n'.join(path.read_text() for path in (release.ROOT / package).glob('*_test.go'))
            for name in names:
                with self.subTest(package=package, test=name):
                    self.assertRegex(sources, r'(?m)^func ' + re.escape(name) + r'\(t \*testing\.T\)')

    def test_both_designated_effects_are_consistent_pending_independent_review(self):
        provider = self.provider()
        result = self.verify(provider)
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])
        self.assertFalse(result['formal_vendor_certification'])
        self.assertEqual(result['unverified'], [])
        self.assertEqual({row['kind'] for row in result['configured_effects']}, {'code', 'intervention'})
        # The A2A component does not claim that the other 33 providers passed.
        self.assertFalse(provider.verify()['providers']['all_providers_verified'])

    def test_local_sandbox_acceptance_and_missing_role_remain_unverified(self):
        for kinds, category, boundary in (
            (('intervention', 'code'), 'local_integration', 'effect'),
            (('intervention', 'code'), 'provider_sandbox', 'api_acceptance'),
            (('intervention',), 'live_account', 'effect'),
        ):
            with self.subTest(category=category, kinds=kinds):
                fixture = self.provider(kinds, category, boundary)
                if boundary == 'api_acceptance':
                    with self.assertRaisesRegex(ValueError, 'API acceptance is restricted'):
                        self.verify(fixture)
                    continue
                result = self.verify(fixture)
                self.assertEqual(result['status'], 'incomplete')
                self.assertTrue(any(row['requirement'] == 'configured agent effect' for row in result['unverified']))

    def test_missing_reference_and_missing_lifecycle_case_are_visible(self):
        self.reports[0].pop('a2a_reference')
        key = (1, 'application-builtins-external')
        package, name = sorted(a2a.required_contracts())[0]
        self.fixture.raw[key] = [event for event in self.fixture.raw[key] if (event['Package'], event.get('Test')) != (package, name)]
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertIn({'compiler': build.MINIMUM_COMPILER, 'requirement': 'original upstream reference build and interoperability execution'},
                      result['unverified'])
        self.assertTrue(any(row.get('test') == name and row.get('variant') == key[1] for row in result['unverified']))

    def test_skipped_upstream_child_cannot_be_hidden_by_a_passing_parent(self):
        case = a2a.REFERENCE_TEST + '/intervention/completed'
        next(event for event in self.interop[0] if event.get('Test') == case and event['Action'] == 'pass')['Action'] = 'skip'
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertTrue(any(row.get('test') == case for row in result['unverified']))

    def test_reference_graph_checksums_package_identity_and_build_settings_are_bound(self):
        originals = dict(self.reference_logs)
        changes = (
            ('modules', lambda text: text.replace('v2.6.0', 'v2.5.0'), 'another SDK'),
            ('modules', lambda text: text.replace(self.sums[(a2a.REFERENCE_SDK, a2a.REFERENCE_VERSION)], 'h1:' + 'a' * 43 + '='), 'checksums'),
            ('packages', lambda text: text.replace('main.go', 'substitute.go'), 'sources'),
            ('packages', lambda text: text.replace(a2a.REFERENCE_MODULE, 'other/module'), 'module boundary'),
            ('build-info', lambda text: text + '\tbuild\t-race=true\n', 'settings'),
            ('build-info', lambda text: text.replace('v2.6.0', 'v2.5.0'), 'dependency'),
            ('build-info', lambda text: text.replace(self.sums[(a2a.REFERENCE_SDK, a2a.REFERENCE_VERSION)], 'h1:' + 'a' * 43 + '='), 'dependency'),
            ('verify', lambda text: '', 'not verified'),
            ('vet', lambda text: 'unreviewed diagnostic', 'diagnostics'),
            ('environment', lambda text: text.replace('amd64', 'arm64'), 'native host'),
        )
        for name, change, message in changes:
            with self.subTest(name=name, message=message):
                self.reference_logs = dict(originals)
                self.reference_logs[(0, name)] = change(self.reference_logs[(0, name)])
                self.save()
                with self.assertRaisesRegex(ValueError, message): self.verify()
        self.reference_logs = originals
        self.save()

    def test_reference_commands_environment_failure_and_foreign_source_are_rejected(self):
        original = copy.deepcopy(self.reports[0]['a2a_reference'])
        changes = (
            lambda section: section['commands'].pop(2),
            lambda section: section['commands'][0].update(exit_code=False),
            lambda section: section['commands'][0]['command'].append('GOPRIVATE'),
            lambda section: section['tests']['tests']['command'].extend(['-run', 'Small']),
            lambda section: section['tests']['tests'].update(exit_code=1),
            lambda section: section['environment'].update(GOTOOLCHAIN='auto'),
            lambda section: section['tests']['environment'].update(CPRA_A2A_AGENT='/foreign/agent'),
            lambda section: section.update(source_changed=['main.go']),
            lambda section: section['source_files'].pop(a2a.REFERENCE_FILES[0]),
        )
        for change in changes:
            with self.subTest(change=change):
                self.reports[0]['a2a_reference'] = copy.deepcopy(original)
                change(self.reports[0]['a2a_reference'])
                self.save()
                with self.assertRaises(ValueError): self.verify()
        self.reports[0]['a2a_reference'] = original
        self.save()

    def test_reference_module_requires_the_exact_recipe_minimum(self):
        name = a2a.REFERENCE_DIRECTORY + '/go.mod'
        original = self.sources[name]
        minimum = release.RECIPE['minimum_go_version'].removeprefix('go')
        for directive in ('1.26.0', '1.27.0', minimum.replace('.', 'x'), ''):
            with self.subTest(directive=directive):
                replacement = ('go ' + directive).encode() if directive else b''
                changed = re.sub(rb'(?m)^go[^\S\n]+[^\n]+$', replacement, original)
                self.assertNotEqual(changed, original)
                checksum = build_fixtures.worker_fixtures.sdk_fixtures.sha(changed)
                self.manifest['source_files'][name]['sha256'] = checksum
                for path, report in zip(self.paths, self.reports):
                    payload = path.with_name(path.stem + '.artifacts') / 'a2a-source/go.mod'
                    payload.write_bytes(changed)
                    record = build_campaign.fingerprint(payload, self.root)
                    self.assertEqual(record['sha256'], checksum)
                    report['a2a_reference']['source_files'][name] = record
                self.save()
                with self.assertRaisesRegex(ValueError, 'upstream SDK or module requirement differs'):
                    self.verify()

    def test_changed_binary_source_payload_alias_and_missing_output_are_rejected(self):
        artifacts = self.paths[0].with_name(self.paths[0].stem + '.artifacts')
        for path in (artifacts / 'a2a-agent', artifacts / 'a2a-source/main.go', artifacts / 'a2a-reference-modules-stdout.log'):
            with self.subTest(path=path.name):
                original = path.read_bytes()
                path.write_bytes(original + b'\n')
                with self.assertRaises(ValueError): self.verify()
                path.write_bytes(original)
        record = self.reports[0]['a2a_reference']['binary']
        original = record['file']
        record['file'] = original.replace('/a2a-agent', '/../' + artifacts.name + '/a2a-agent')
        self.save()
        with self.assertRaises(ValueError): self.verify()
        record['file'] = original
        self.save()
        output = artifacts / 'a2a-reference-modules-stdout.log'
        retained = output.with_name('original-modules.log')
        output.rename(retained); output.symlink_to(retained)
        with self.assertRaises(ValueError): self.verify()
        output.unlink()
        with self.assertRaises(ValueError): self.verify()

    def test_upstream_package_exclusion_incomplete_execution_and_byte_bounds_are_rejected(self):
        artifacts = self.paths[0].with_name(self.paths[0].stem + '.artifacts')
        package = artifacts / 'a2a-interop-packages-stdout.log'
        data = json.loads(package.read_bytes())
        data['TestGoFiles'] = ['a2a_test.go']
        package.write_bytes(release.encoded(data))
        self.reports[0]['a2a_reference']['tests']['packages']['stdout'] = build_campaign.fingerprint(package, self.root)
        self.fixture.save()
        with self.assertRaisesRegex(ValueError, 'source was excluded'): self.verify()
        self.save()
        self.interop[0].pop()
        self.save()
        with self.assertRaisesRegex(ValueError, 'did not all complete'): self.verify()
        self.interop[0].append(build_fixtures.event(release.RECIPE['module'] + '/internal/jobs', 'pass', Elapsed=2))
        self.save()
        with patch.object(a2a.provider, 'MAX_BINARY_BYTES', 4), self.assertRaisesRegex(ValueError, 'byte limit'):
            self.verify()

    def test_effect_build_and_reports_must_be_supplied_together_and_match_source(self):
        with self.assertRaisesRegex(ValueError, 'supplied together'):
            a2a.verify_a2a_campaigns(self.manifest, self.paths, self.root)
        provider = self.provider()
        provider.build['release_identity']['commit'] = 'b' * 40
        provider.save_build()
        with self.assertRaisesRegex(ValueError, 'differs'): self.verify(provider)

    def test_cli_reports_missing_effects_without_claiming_approval(self):
        manifest = self.root / 'RELEASE.json'
        manifest.write_bytes(release.encoded(self.manifest))
        arguments = ['a2a_evidence.py', '--release', str(manifest)]
        for path in self.paths:
            arguments.extend(['--campaign', str(path)])
        with patch.object(sys, 'argv', arguments), patch('sys.stdout', new_callable=io.StringIO) as output:
            status = a2a.main()
        self.assertEqual(status, 2)
        self.assertEqual(json.loads(output.getvalue())['status'], 'incomplete')

    def test_recorder_preserves_original_reference_files_and_failure_without_invoking_code(self):
        source = self.root / 'reference-source'
        output = self.root / 'capture/result.json'
        artifacts = output.with_name(output.stem + '.artifacts')
        artifacts.mkdir(parents=True)
        go = self.root / 'reference-go'
        go.write_bytes(b'fixture compiler, never executed'); go.chmod(0o700)
        for name, data in self.sources.items():
            path = source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        report = {'compiler': '/fixture/selected/go'}
        calls = []
        def capture(command, relative, name, environment, timeout):
            calls.append((command, relative, name, environment, timeout))
            self.assertEqual(environment['GOWORK'], 'off')
            self.assertEqual(environment['GOTOOLCHAIN'], 'local')
            value = self.reference_logs[(0, name.removeprefix('a2a-reference-'))]
            record = {'exit_code': 1 if name == 'a2a-reference-verify' else 0}
            for channel in ('stdout', 'stderr'):
                path = artifacts / (name + '-' + channel + '.log')
                path.write_text(value if channel == 'stdout' else '')
                record[channel] = build_campaign.fingerprint(path, output.parent)
            return record
        args = argparse.Namespace(a2a_reference_go=str(go), out=output)
        progress = []
        with patch('subprocess.Popen', side_effect=AssertionError('recorder fixture cannot execute code')):
            with self.assertRaisesRegex(ValueError, 'reference verify failed'):
                build_campaign.record_a2a(args, self.manifest, source, artifacts, {'PATH': '/fixture/bin'},
                                          capture, report, lambda: progress.append(copy.deepcopy(report)))
        self.assertEqual([row[2] for row in calls], ['a2a-reference-environment', 'a2a-reference-download', 'a2a-reference-verify'])
        self.assertEqual(report['a2a_reference']['commands'][-1]['exit_code'], 1)
        self.assertEqual((artifacts / 'a2a-source').stat().st_mode & 0o777, 0o700)
        for name, record in report['a2a_reference']['source_files'].items():
            path = output.parent / record['file']
            self.assertEqual(path.read_bytes(), self.sources[name])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertTrue(progress)

    def test_recorder_uses_separate_compilers_and_keeps_reader_compatible_original_bytes(self):
        source = self.root / 'successful-reference-source'
        for name, data in self.sources.items():
            path = source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        go = self.root / 'successful-reference-go'
        go.write_bytes(b'synthetic compiler, not executed'); go.chmod(0o700)
        for index, compiler in enumerate((build.MINIMUM_COMPILER, release.RECIPE['go_version'])):
            with self.subTest(compiler=compiler):
                output = self.root / ('capture-' + compiler) / 'result.json'
                artifacts = output.with_name(output.stem + '.artifacts')
                artifacts.mkdir(parents=True)
                binary = artifacts / 'a2a-agent'
                report = {'compiler': '/fixture/' + compiler + '/go', 'go': compiler}
                calls = []
                original = self.paths[index].with_name(self.paths[index].stem + '.artifacts')
                def capture(command, relative, name, environment, timeout):
                    calls.append(name)
                    self.assertLessEqual(timeout, 600)
                    if name.startswith('a2a-reference-'):
                        self.assertEqual(command[0], str(go))
                        self.assertEqual(relative, a2a.REFERENCE_DIRECTORY)
                        self.assertEqual(environment['CGO_ENABLED'], '0')
                        data = self.reference_logs[(index, name.removeprefix('a2a-reference-'))]
                        if name == 'a2a-reference-build-info':
                            data = data.replace(str(original / 'a2a-agent'), str(binary))
                        if name == 'a2a-reference-build':
                            binary.write_bytes(b'synthetic reference executable, not release proof')
                    else:
                        self.assertEqual(command[0], report['compiler'])
                        self.assertEqual(relative, '.')
                        self.assertEqual(environment['CGO_ENABLED'], '1')
                        self.assertEqual(environment['CPRA_A2A_AGENT'], str(binary))
                        data = (original / (name + '-stdout.log')).read_text()
                    row = {'exit_code': 0}
                    for channel in ('stdout', 'stderr'):
                        target = artifacts / (name + '-' + channel + '.log')
                        target.write_text(data if channel == 'stdout' else '')
                        row[channel] = build_campaign.fingerprint(target, output.parent)
                    return row
                args = argparse.Namespace(a2a_reference_go=str(go), out=output)
                with patch('subprocess.Popen', side_effect=AssertionError('fixture must not execute code')), \
                        patch.object(build, 'source_snapshot', return_value=self.manifest['source_files']):
                    build_campaign.record_a2a(args, self.manifest, source, artifacts, {'PATH': '/fixture/bin', 'CGO_ENABLED': '1'},
                                              capture, report, lambda: None)
                    inputs = {}
                    def remember(record, expected, source=None, limit=build.MAX_LOG_BYTES):
                        self.assertEqual(output.parent / record['file'], expected)
                        value, size = build.browser_evidence.hash_file(expected, limit)
                        self.assertEqual((record['sha256'], record['bytes']), (value, size))
                        if source is not None:
                            self.assertEqual(value, source)
                        inputs[str(expected)] = value
                    result = a2a.reference_results(self.manifest, report, artifacts, remember)
                self.assertEqual(result['coverage']['passed'], 22)
                self.assertEqual(report['a2a_reference']['source_changed'], [])
                self.assertEqual(len(calls), 10)
                self.assertIn(str(binary), inputs)


if __name__ == '__main__':
    unittest.main()
