"""Management-reader regressions. Synthetic executions are not release evidence."""
import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
import zipfile

import build_campaign
import build_evidence as build
import management_evidence as management
import release
import test_build_evidence as build_fixtures


class ManagementEvidenceTests(unittest.TestCase):
    def setUp(self):
        fixture = build_fixtures.BuildEvidenceTests()
        self.addCleanup(fixture.doCleanups)
        fixture.setUp()
        self.fixture, self.root, self.manifest = fixture, fixture.root, fixture.manifest
        self.paths, self.reports = fixture.paths, fixture.reports
        self.payloads = {name: (release.ROOT / name).read_bytes() for name in (
            *management.CONTRACT_FILES, *('sdk/go/' + item for item in management.SDK_CONTRACT_FILES))}
        self.inventory = management.contract_inventory(self.payloads)
        self.sync_contracts()
        for index, (path, report) in enumerate(zip(self.paths, self.reports)):
            artifacts = path.with_name(path.stem + '.artifacts')
            for row, (_, _relative, module, _tags) in zip(report['variants'], build.VARIANTS):
                if row['name'].startswith('worker-'):
                    continue
                required = management.required_contracts(row, module, self.inventory) | {(module, 'TestRequired')}
                groups = {}
                for package, name in sorted(required):
                    groups.setdefault(package, set()).add(name)
                events, packages = [], []
                for package, tests in groups.items():
                    packages.append({'ImportPath': package, 'Module': {'Path': module}, 'TestGoFiles': ['fixture_test.go']})
                    events.append(build_fixtures.event(package, 'start'))
                    for name in sorted(item for item in tests if '/' not in item):
                        events.append(build_fixtures.event(package, 'run', name))
                        for child in sorted(item for item in tests if item.startswith(name + '/')):
                            events.extend([build_fixtures.event(package, 'run', child),
                                           build_fixtures.event(package, 'pass', child, Elapsed=0.01)])
                        if package == release.RECIPE['module'] and name in management.CLI_TESTS:
                            events.append(build_fixtures.event(package, 'output', name,
                                Output='Native CLI helper uses ' + report['go'] + '; build tags: ' + json.dumps(row['tags']) + '\n'))
                        events.append(build_fixtures.event(package, 'pass', name, Elapsed=0.1))
                    events.append(build_fixtures.event(package, 'pass', Elapsed=2))
                (artifacts / (row['name'] + '-packages-stdout.log')).write_text('\n'.join(json.dumps(item) for item in packages))
                fixture.raw[index, row['name']] = events
        self.save()

    def sync_contracts(self):
        files = self.fixture.fixture.fixture.files[build.sdk_evidence.CORE]
        for name, data in self.payloads.items():
            self.manifest['source_files'][name] = {'mode': '100644', 'sha256': hashlib.sha256(data).hexdigest()}
            if name.startswith('sdk/go/'):
                files[name.removeprefix('sdk/go/')] = data
        prefix = self.fixture.fixture.sdk['Path'] + '@' + self.fixture.fixture.sdk['Version'] + '/'
        archive = self.root / 'management-synthetic-sdk.zip'
        with zipfile.ZipFile(archive, 'w') as stream:
            for name, data in files.items():
                stream.writestr(prefix + name, data)
        self.fixture.fixture.sdk['ArchiveSHA256'] = release.sha(archive)
        for path, report in zip(self.paths, self.reports):
            artifacts = path.with_name(path.stem + '.artifacts')
            (artifacts / 'sdk.zip').write_bytes(archive.read_bytes())
            report['sdk_module'] = dict(self.fixture.fixture.sdk)
            report['module_inputs']['sdk.zip'] = build_campaign.fingerprint(artifacts / 'sdk.zip')
            directory = artifacts / 'contracts'
            directory.mkdir(exist_ok=True)
            report['contract_inputs'] = {}
            for name in management.CONTRACT_FILES:
                target = directory / Path(name).name
                target.write_bytes(self.payloads[name])
                report['contract_inputs'][name] = build_campaign.fingerprint(target, self.root)
            if report.get('check_results'):
                generation = artifacts / 'generation.json'
                content = json.loads(generation.read_bytes())
                content['outputs'] = {name: self.manifest['source_files'][name]['sha256'] for name in build.SDK_OUTPUTS}
                generation.write_bytes(release.encoded(content))
                report['check_results']['generation.json'] = build_campaign.fingerprint(generation)

    def save(self):
        for path, report in zip(self.paths, self.reports):
            artifacts = path.with_name(path.stem + '.artifacts')
            for row in report['variants']:
                for action in ('packages', 'vet'):
                    for channel in ('stdout', 'stderr'):
                        row['logs'][action][channel] = build_campaign.fingerprint(
                            artifacts / (row['name'] + '-' + action + '-' + channel + '.log'), self.root)
        self.fixture.save()

    def verify(self):
        return management.verify_management_campaigns(self.manifest, self.paths)

    def replace_json(self, name, change):
        value = json.loads(self.payloads[name])
        change(value)
        self.payloads[name] = release.encoded(value)

    def test_complete_original_inputs_keep_execution_classes_and_publication_separate(self):
        with patch('subprocess.Popen', side_effect=AssertionError('reader must not run code')):
            result = self.verify()
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertEqual(result['unverified'], [])
        self.assertFalse(result['release_gate_passed'])
        for report in result['reports']:
            self.assertEqual(report['operations'], {'base': 76, 'external_enabled': 87})
            self.assertEqual(len(report['variants']), 6)
            for variant in report['variants'][:4]:
                self.assertEqual(set(variant['native_cli_helpers']), set(management.CLI_TESTS))
        self.assertIn('accessibility', result['companion_requirements'])
        self.assertTrue(any('transport coverage' in item for item in result['remaining']))
        for path in self.paths:
            self.assertIn(str(path.with_name(path.stem + '.artifacts') / 'contracts/operations.json'), result['input_sha256'])

    def test_parent_inventory_pass_cannot_replace_missing_operation_subtest(self):
        key = (1, 'sdk-external')
        name = 'TestExternalOperationInventory/' + next(row['operationID'] for row in self.inventory if row['buildTag'])
        self.fixture.raw[key] = [event for event in self.fixture.raw[key] if event.get('Test') != name]
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertTrue(any(row.get('test') == name for row in result['unverified']))

    def test_sdk_transport_coverage_cannot_replace_actual_server_lifecycle(self):
        key = (0, 'application-default')
        name = 'TestManagementIncidentControlsRealSDK'
        self.fixture.raw[key] = [event for event in self.fixture.raw[key] if event.get('Test') != name]
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertTrue(any(row.get('test') == name for row in result['unverified']))

    def test_skipped_contract_is_incomplete_even_when_package_passes(self):
        for event in self.fixture.raw[0, 'application-default']:
            if event.get('Test') == 'TestManagementHistoryByteBoundPreservesEveryEvent' and event['Action'] == 'pass':
                event['Action'] = 'skip'
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertFalse(result['release_gate_passed'])

    def test_old_build_record_without_original_contracts_is_incomplete(self):
        for report in self.reports:
            report.pop('contract_inputs')
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertEqual(sum(row.get('requirement') == 'original canonical API inputs' for row in result['unverified']), 2)

    def test_actual_cli_helper_identity_and_tags_must_match_each_variant(self):
        key = (0, 'application-builtins-external')
        events = self.fixture.raw[key]
        row = next(event for event in events if event['Action'] == 'output')
        original = row['Output']
        for replacement in ('', original.replace(self.reports[0]['go'], 'go1.99.0'),
                            original.replace('externaljobs', 'unexpected'), original + original):
            with self.subTest(replacement=replacement):
                row['Output'] = replacement
                self.save()
                with self.assertRaisesRegex(ValueError, 'CLI compiler or tags'):
                    self.verify()
        row['Output'] = original
        self.save()

    def test_contract_bytes_cannot_be_replaced_by_recorded_summary(self):
        original = self.paths[0].with_name(self.paths[0].stem + '.artifacts') / 'contracts/models.base.json'
        original.write_bytes(b'{"status":"pass"}')
        with self.assertRaisesRegex(ValueError, 'contract identity'):
            self.verify()

    def test_contract_path_alias_link_and_missing_payload_are_rejected(self):
        row = self.reports[0]['contract_inputs'][management.CONTRACT_FILES[0]]
        original = row['file']
        row['file'] = original.replace('/contracts/', '/contracts/../contracts/')
        self.save()
        with self.assertRaises(ValueError): self.verify()
        row['file'] = original
        self.save()
        path = self.root / original
        real = path.with_name('original-inventory.json')
        path.rename(real); path.symlink_to(real)
        with self.assertRaises(ValueError): self.verify()
        path.unlink()
        with self.assertRaises(ValueError): self.verify()

    def test_source_consistent_server_inventory_drift_from_sdk_is_rejected(self):
        self.replace_json(management.CONTRACT_FILES[0], lambda rows: rows[0].update(sdkMethod='MonitorsService.Unreviewed'))
        self.sync_contracts(); self.save()
        with self.assertRaisesRegex(ValueError, 'server and SDK inventories differ'):
            self.verify()

    def test_schema_change_unknown_metadata_and_duplicate_identity_are_rejected(self):
        original = dict(self.payloads)
        scenarios = (
            (lambda: self.replace_json('sdk/go/api/schema_base.json', lambda value: value['paths'].pop('/api/v2/monitors')), 'generated SDK schema'),
            (lambda: self.replace_json(management.CONTRACT_FILES[0], lambda rows: rows.append(copy.deepcopy(rows[0]))), 'duplicate'),
            (lambda: self.replace_json(management.CONTRACT_FILES[0], lambda rows: rows[0].update(contractTest='TestNotExecuted')), 'inventory test'),
            (lambda: self.replace_json(management.CONTRACT_FILES[0], lambda rows: rows[0].update(availability='planned')), 'inventory fields'),
        )
        for change, message in scenarios:
            with self.subTest(message=message):
                self.payloads = dict(original)
                change()
                self.payloads['sdk/go/testdata/operations.json'] = self.payloads[management.CONTRACT_FILES[0]]
                self.sync_contracts(); self.save()
                with self.assertRaisesRegex(ValueError, message): self.verify()
        self.payloads = original
        self.sync_contracts(); self.save()

    def test_duplicate_json_fields_and_oversized_contracts_are_rejected(self):
        data = self.payloads[management.CONTRACT_FILES[1]]
        self.payloads[management.CONTRACT_FILES[1]] = b'{"openapi":"3.1.2",' + data.lstrip()[1:]
        with self.assertRaisesRegex(ValueError, 'duplicate JSON field'):
            management.contract_inventory(self.payloads)
        self.payloads[management.CONTRACT_FILES[1]] = data
        path = self.paths[0].with_name(self.paths[0].stem + '.artifacts') / 'contracts/operations.json'
        path.write_bytes(b' ' * (management.MAX_CONTRACT_BYTES + 1))
        with self.assertRaises(ValueError): self.verify()

    def test_model_names_and_media_types_cannot_drift_from_selected_wire_contract(self):
        original = dict(self.payloads)
        for field, value, message in (('schema', 'JobType', 'response model'),
                                      ('request', 'Credential', 'request model')):
            with self.subTest(field=field):
                self.payloads = dict(original)
                self.replace_json(management.CONTRACT_FILES[0], lambda rows: rows[1].update({field: value}))
                self.payloads['sdk/go/testdata/operations.json'] = self.payloads[management.CONTRACT_FILES[0]]
                with self.assertRaisesRegex(ValueError, message): management.contract_inventory(self.payloads)
        self.payloads = original

    def test_split_original_go_output_still_requires_one_cli_helper(self):
        key = (0, 'application-default')
        events = self.fixture.raw[key]
        index = next(i for i, event in enumerate(events) if event['Action'] == 'output')
        original = events[index]
        data = original['Output']
        events[index:index + 1] = [dict(original, Output=data[:24]), dict(original, Output=data[24:])]
        self.save()
        self.assertEqual(self.verify()['status'], 'consistent_pending_review')

    def test_capture_keeps_original_bytes_and_restrictive_files_without_execution(self):
        source, artifacts = self.root / 'capture-source', self.root / 'capture-artifacts'
        source.mkdir(); artifacts.mkdir()
        for name in management.CONTRACT_FILES:
            path = source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(self.payloads[name])
        with patch('subprocess.Popen', side_effect=AssertionError('capture must not run code')):
            records = management.capture_contracts(source, self.manifest, artifacts, self.root)
        self.assertEqual((artifacts / 'contracts').stat().st_mode & 0o777, 0o700)
        for name, record in records.items():
            path = self.root / record['file']
            self.assertEqual(path.read_bytes(), self.payloads[name])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(record['sha256'], self.manifest['source_files'][name]['sha256'])

    def test_cli_returns_nonzero_for_missing_management_evidence(self):
        self.reports[0].pop('contract_inputs'); self.save()
        manifest = self.root / 'RELEASE.json'
        manifest.write_bytes(release.encoded(self.manifest))
        arguments = ['management_evidence.py', '--release', str(manifest),
                     '--campaign', str(self.paths[0]), '--campaign', str(self.paths[1])]
        with patch.object(sys, 'argv', arguments), patch('sys.stdout', new_callable=io.StringIO) as output:
            status = management.main()
        self.assertEqual(status, 2)
        self.assertEqual(json.loads(output.getvalue())['status'], 'incomplete')


if __name__ == '__main__':
    unittest.main()
