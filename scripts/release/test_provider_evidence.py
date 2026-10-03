"""Small evidence fixtures test rejection rules, not provider certification."""
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import provider_evidence as provider
import release
from test_evidence import manifest_fixture


class ProviderEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.build_directory = self.root / 'build'
        self.build_directory.mkdir()
        self.manifest = manifest_fixture()
        for name in provider.SOURCE_REQUIREMENTS:
            self.manifest['source_files'][name] = {'mode': '100644', 'sha256': 'c' * 64}
        (self.build_directory / provider.PROGRAM).write_bytes(b'fixture executable, not a native qualification')
        self.binary = provider.fingerprint(self.build_directory / provider.PROGRAM, provider.MAX_BINARY_BYTES)
        command = ['/fixture/go', 'build', *release.RECIPE['build_flags'], '-tags',
                   ' '.join(release.RECIPE['build_tags']), '-ldflags', release.ldflags(self.manifest),
                   '-o', str(self.build_directory / provider.PROGRAM), provider.ENTRYPOINT]
        metadata = {'GOVERSION': release.RECIPE['go_version'], 'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64',
                    'GOOS': 'linux', 'GOARCH': 'amd64', 'GOWORK': 'off'}
        settings = {'GOOS': 'linux', 'GOARCH': 'amd64', 'CGO_ENABLED': '0', '-trimpath': 'true',
                    '-buildmode': 'exe', '-compiler': 'gc', 'GOAMD64': 'v1',
                    '-tags': ','.join(release.RECIPE['build_tags'])}
        info = str(self.build_directory / provider.PROGRAM) + ': ' + release.RECIPE['go_version'] + '\n'
        info += '\tpath\t' + release.RECIPE['module'] + '/cmd/cpra-verify\n'
        info += ''.join('\tbuild\t' + key + '=' + value + '\n' for key, value in settings.items())
        commands = []
        for name, arguments, stdout in (
            ('go-env', ['env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH', 'GOOS', 'GOARCH', 'GOWORK'], json.dumps(metadata)),
            ('go-download', ['mod', 'download'], ''), ('go-verify', ['mod', 'verify'], 'all modules verified\n'),
            ('go-build', command[1:], ''), ('build-info', ['version', '-m', command[-2]], info),
        ):
            record = {'command': ['/fixture/go', *arguments], 'status': 'pass', 'exit_code': 0}
            for field, text in (('stdout', stdout), ('stderr', '')):
                path = self.build_directory / (name + '.' + field)
                path.write_text(text)
                record[field] = path.name
                record[field + '_sha256'] = release.sha(path)
            commands.append(record)
        self.build = {'schema_version': 1, 'status': 'built', 'evidence_class': 'frozen_provider_build',
                      'source_unchanged': True, 'release_qualified': False, 'public_download_qualified': False,
                      'release_identity': release.evidence_identity(self.manifest),
                      'source_files': copy.deepcopy(self.manifest['source_files']), 'recipe': copy.deepcopy(release.RECIPE),
                      'environment': copy.deepcopy(release.RECIPE['environment']),
                      'go_version': release.RECIPE['go_version'], 'target': 'linux_amd64',
                      'version': provider.version_text(self.manifest), 'compiler_sha256': 'd' * 64,
                      'executable': {'path': provider.PROGRAM, **self.binary}, 'commands': commands}
        self.save_build()
        self.path = self.root / 'provider.json'
        self.write_campaign([])

    def save_build(self):
        (self.build_directory / 'report.json').write_bytes(release.encoded(self.build))

    def write_campaign(self, passed, category='live_account', boundary='effect'):
        directory = Path(str(self.path) + '.evidence')
        directory.mkdir(exist_ok=True)
        records = []
        for kind, driver in provider.summary.INVENTORY:
            row = {'kind': kind, 'driver': driver, 'status': 'not_configured', 'configured': False,
                   'operation_invoked': False, 'accepted': False, 'observed': False, 'duration_ms': 0}
            if (kind, driver) in passed:
                evidence = {'kind': kind, 'driver': driver, 'run_id': 'run-one', 'evidence_type': category,
                            'observation_boundary': boundary, 'accepted': True, 'observed': boundary == 'effect'}
                if boundary == 'effect':
                    evidence.update(before={'count': 0}, after={'count': 1, 'observed': True})
                if driver == 'a2a':
                    fields = dict(monitor_id='agent-monitor', action_id='a' * 64, action_state='succeeded')
                    evidence.update(fields)
                    row.update(fields)
                path = directory / (kind + '-' + driver + '.json')
                path.write_bytes(release.encoded(evidence))
                row.update(status='pass', configured=True, operation_invoked=True, accepted=True,
                           observed=boundary == 'effect', evidence_type=category, observation_boundary=boundary,
                           evidence_ref=path.name, evidence_sha256=release.sha(path), duration_ms=1)
            records.append(row)
        self.campaign = {'run_id': 'run-one', 'binary_sha256': self.binary['sha256'],
                         'version': self.build['version'], 'go_version': self.build['go_version'], 'records': records}
        self.save_campaign()

    def save_campaign(self):
        self.path.write_bytes(release.encoded(self.campaign))

    def verify(self, paths=None):
        return provider.verify_provider_reports(self.manifest, self.build_directory, paths or [self.path])

    def test_missing_configuration_stays_incomplete_with_zero_verified_providers(self):
        report = self.verify()
        self.assertEqual(report['status'], 'incomplete')
        self.assertEqual(report['providers']['covered_driver_count'], 0)
        self.assertFalse(report['providers']['all_providers_verified'])
        self.assertEqual(len(report['providers']['records']), 35)
        self.assertFalse(report['release_gate_passed'])

    def test_every_live_effect_is_still_pending_review_and_never_publication_approval(self):
        self.write_campaign(provider.summary.INVENTORY)
        report = self.verify()
        self.assertEqual(report['status'], 'consistent_pending_review')
        self.assertEqual(report['providers']['strongest_evidence_counts']['live_account'], 35)
        self.assertFalse(report['formal_vendor_certification'])
        self.assertFalse(report['release_gate_passed'])
        self.assertEqual(len(report['input_sha256']), 48)

    def test_local_effect_and_sandbox_acceptance_do_not_satisfy_live_account_gate(self):
        self.write_campaign(provider.summary.INVENTORY, category='local_integration')
        self.assertEqual(self.verify()['status'], 'incomplete')
        self.write_campaign([('code', 'twilio')], category='provider_sandbox', boundary='api_acceptance')
        report = self.verify()
        self.assertEqual(report['providers']['covered_driver_count'], 1)
        self.assertFalse(report['providers']['all_providers_verified'])

    def test_working_tree_stale_source_and_unapproved_builds_are_rejected(self):
        changes = [lambda: self.build.update(evidence_class='working_tree_provider_build'),
                   lambda: self.build.update(source_unchanged=False),
                   lambda: self.build['source_files']['go.mod'].update(sha256='e' * 64),
                   lambda: self.build['release_identity'].update(commit='b' * 40),
                   lambda: self.build['environment'].update(GOWORK='/private/go.work'),
                   lambda: self.build.update(go_version='go1.25.0'),
                   lambda: self.build.update(target='windows_amd64'),
                   lambda: self.build['commands'][3]['command'].insert(2, '-race'),
                   lambda: self.build['commands'][0].update(command=[]),
                   lambda: self.build['commands'][0].update(exit_code=False)]
        original = copy.deepcopy(self.build)
        for change in changes:
            with self.subTest(change=changes.index(change)):
                change()
                self.save_build()
                with self.assertRaises(ValueError): self.verify()
                self.build = copy.deepcopy(original)

    def test_changed_executable_or_retained_build_log_is_rejected(self):
        executable = self.build_directory / provider.PROGRAM
        executable.write_bytes(b'different executable')
        with self.assertRaisesRegex(ValueError, 'executable changed'): self.verify()
        executable.write_bytes(b'fixture executable, not a native qualification')
        (self.build_directory / 'go-build.stderr').write_text('changed')
        with self.assertRaisesRegex(ValueError, 'output changed'): self.verify()

    def test_actual_report_fingerprint_cannot_be_replaced_by_adjacent_claims(self):
        self.campaign.pop('binary_sha256')
        self.save_campaign()
        self.path.with_suffix('.suite.json').write_bytes(release.encoded({'passed': True, 'binary_sha256': self.binary['sha256']}))
        with self.assertRaisesRegex(ValueError, 'omits its hash'): self.verify()

    def test_report_version_binary_and_duplicate_run_id_are_rejected(self):
        for key, value in (('version', 'old-version'), ('go_version', 'go1.25.0'), ('binary_sha256', 'e' * 64)):
            original = self.campaign[key]
            self.campaign[key] = value
            self.save_campaign()
            with self.subTest(key=key), self.assertRaises(ValueError): self.verify()
            self.campaign[key] = original
        self.save_campaign()
        other = self.root / 'duplicate.json'
        other.write_bytes(self.path.read_bytes())
        with self.assertRaisesRegex(ValueError, 'duplicate provider run'): self.verify([self.path, other])

    def test_links_path_traversal_and_oversized_evidence_fail_before_decoding(self):
        self.write_campaign([('pulse', 'http')])
        row = next(row for row in self.campaign['records'] if row['status'] == 'pass')
        for reference in ('../outside', 'C:\\outside', '.', '..'):
            row['evidence_ref'] = reference
            self.save_campaign()
            with self.subTest(reference=reference), self.assertRaises(ValueError): self.verify()
        self.write_campaign([('pulse', 'http')])
        row = next(row for row in self.campaign['records'] if row['status'] == 'pass')
        path = Path(str(self.path) + '.evidence') / row['evidence_ref']
        original = path.read_bytes()
        other = self.root / 'outside.json'
        other.write_bytes(original)
        path.unlink()
        path.symlink_to(other)
        with self.assertRaisesRegex(ValueError, 'regular file'): self.verify()
        path.unlink()
        with path.open('wb') as stream:
            stream.truncate(provider.summary.MAX_JSON_BYTES + 1)
        with self.assertRaisesRegex(ValueError, 'exceeds 16 MiB'): self.verify()

    def test_binary_metadata_cannot_hide_instrumentation_or_wrong_entrypoint(self):
        path = self.build_directory / 'build-info.stdout'
        original = path.read_text()
        for text in (original + '\tbuild\t-race=true\n',
                     original.replace('/cmd/cpra-verify', '/cmd/other'),
                     original.replace('CGO_ENABLED=0', 'CGO_ENABLED=1'),
                     original + '\tbuild\tGOOS=linux\n'):
            with self.subTest(text=text[-40:]):
                path.write_text(text)
                self.build['commands'][-1]['stdout_sha256'] = release.sha(path)
                self.save_build()
                with self.assertRaises(ValueError): self.verify()

    def test_build_cannot_use_working_tree_cache_as_frozen_provenance(self):
        for options in ({'dependencies': self.root / 'dependencies'},
                        {'manifest': self.manifest, 'cache': self.root / 'cache'}):
            with self.subTest(options=list(options)):
                destination = self.root / 'invalid-build'
                with self.assertRaises(ValueError):
                    provider.build_verifier(Path(__file__), destination, **options)
                self.assertFalse(destination.exists())

    def test_frozen_inventory_must_include_the_verifier_and_its_evidence_readers(self):
        self.manifest['source_files'].pop('internal/drivertest/runner.go')
        with self.assertRaisesRegex(ValueError, 'omits verifier inputs'): self.verify()

    def test_duplicate_json_fields_and_nonfinite_numbers_are_rejected(self):
        for raw in (b'{"run_id":"one","run_id":"two"}', b'{"duration_ms":NaN}', b'{"duration_ms":1e400}'):
            self.path.write_bytes(raw)
            with self.subTest(raw=raw), self.assertRaises(ValueError): self.verify()

    def test_huge_integer_duration_is_an_evidence_error(self):
        self.write_campaign([('pulse', 'http')])
        row = next(row for row in self.campaign['records'] if row['status'] == 'pass')
        row['duration_ms'] = 10 ** 400
        self.save_campaign()
        with self.assertRaisesRegex(ValueError, 'invalid duration'): self.verify()

    def test_dirty_release_identity_and_bad_report_counts_are_rejected(self):
        self.manifest['dirty'] = True
        with self.assertRaisesRegex(ValueError, 'clean, frozen'): self.verify()
        self.manifest['dirty'] = False
        for paths in ([], [self.path] * (provider.MAX_REPORTS + 1)):
            with self.subTest(count=len(paths)), self.assertRaises(ValueError):
                provider.verify_provider_reports(self.manifest, self.build_directory, paths)


if __name__ == '__main__':
    unittest.main()
