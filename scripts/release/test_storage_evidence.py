"""Synthetic reader fixtures and real log-file boundaries, not native qualification."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import release
import storage_evidence as storage
from test_evidence import manifest_fixture


class StorageEvidenceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.distribution = self.root / 'candidate'; self.distribution.mkdir()
        self.previous_distribution = self.root / 'previous'; self.previous_distribution.mkdir()
        self.manifest = manifest_fixture()
        self.manifest['source_files'][storage.SOURCE] = {'mode': '100755', 'sha256': release.sha(release.ROOT / storage.SOURCE)}
        self.previous = copy.deepcopy(self.manifest)
        self.previous.update(version='v0.0.9', commit='c' * 40, storage_format_version=35)
        self.previous['recipe']['storage_format_version'] = 35
        self.previous_release = self.previous_distribution / 'RELEASE.json'
        self.previous_release.write_bytes(release.encoded(self.previous))
        self.paths, self.reports = [], []
        self.add_campaign('linux_amd64', 'x86_64')

    def add_campaign(self, target, architecture):
        path = self.root / ('upgrade-' + target + '.json')
        executables = {}
        for label, manifest, directory in [('candidate', self.manifest, self.distribution),
                                            ('previous', self.previous, self.previous_distribution)]:
            programs = {name: ('synthetic executable fixture: ' + label + '/' + target + '/' + name).encode()
                        for name in ('cpra', 'cpractl')}
            inventory = {'target': target, 'binaries': {name: hashlib.sha256(data).hexdigest() for name, data in programs.items()}}
            entries = {name: (data, True) for name, data in programs.items()}
            entries.update({'RELEASE.json': (release.encoded(manifest), False),
                            'DEPENDENCIES.json': (release.encoded(inventory), False)})
            release.package.archive(directory / storage.archive_name(manifest, target), entries, manifest['source_date_epoch'])
            for name, role in [('cpra', 'binary'), ('cpractl', 'cli')]:
                executables[label + '_' + role] = {'sha256': inventory['binaries'][name],
                                                  'version': name + ' ' + manifest['version'] + ' (commit ' + manifest['commit'] + ')'}
            stage = self.root / (label + '-stage-' + target); stage.mkdir()
            (stage / 'RELEASE.json').write_bytes(release.encoded(manifest))
            for name, data in programs.items():
                (stage / name).write_bytes(data); (stage / name).chmod(0o755)
        report = {'schema_version': 2, 'status': 'passed', 'scope': 'native two-binary storage upgrade fixture',
                  'platform': 'Linux-synthetic-fixture', 'architecture': architecture,
                  'release_gate_passed': False, 'endurance_performed': False,
                  'source_files': {storage.SOURCE: self.manifest['source_files'][storage.SOURCE]['sha256']},
                  'source_changed': [], 'executables': executables, 'cases': [], 'logs': {}}
        log_root = path.with_name(path.name + '.inputs'); log_root.mkdir()
        for case_name in ('log', 'snapshot'):
            actions = [{'id': format(index + 1, '064x'), 'executionID': format(index + 1, '064x'),
                        'executionRevision': 'e' * 64, 'incarnationUID': 'synthetic-incarnation',
                        'incidentID': 'd' * 64, 'monitorID': 'upgrade-monitor',
                        'kind': 'intervention' if index == 0 else 'code',
                        'state': 'succeeded' if index == 1 else 'started', 'held': False}
                       for index in range(3)]
            monitor = {'metadata': {'uid': 'synthetic-incarnation', 'id': 'upgrade-monitor'}, 'status': {'incidentID': 'd' * 64}}
            incident = {'id': 'd' * 64, 'acknowledgedBy': 'upgrade/operator'}
            history = [{'id': 'synthetic-event', 'monitorID': 'upgrade-monitor', 'kind': 'incident.opened'}]
            counts = {path: 1 for path in storage.EFFECTS}
            case = {'case': case_name, 'status': 'passed', 'phase': 'candidate_recovered',
                    'snapshot_interval': '1s' if case_name == 'snapshot' else '1h',
                    'snapshot_count': 3 if case_name == 'snapshot' else 0,
                    'backup_formats': {'previous': None, 'candidate': self.manifest['storage_format_version']},
                    'older_restore': {'original_backup_accepted': True, 'candidate_backup_rejected': True},
                    'older_owner_rejected_upgraded_state': True, 'older_owner_exit_code': 1,
                    'restore_rejected_before_reprovision': True, 'old_token_status': 401,
                    'before': {'node_id': 'synthetic-node', 'monitor': monitor, 'incident': incident, 'history': history},
                    'observed_actions': copy.deepcopy(actions), 'interrupted_actions': copy.deepcopy(actions),
                    'provider_counts': dict(counts, failed_health_checks=1, **{'/health': 5})}
            for phase, failures in [('upgrade', 9), ('restore', 17)]:
                final = copy.deepcopy(actions)
                for action in final:
                    if action['state'] == 'started':
                        action.update(state='unknown', held=True)
                case[phase] = {'node_id': 'synthetic-node', 'monitor': copy.deepcopy(monitor),
                               'incident': copy.deepcopy(incident), 'history': copy.deepcopy(history),
                               'actions': final, 'provider_counts': dict(counts, failed_health_checks=failures, **{'/health': failures + 5})}
            report['cases'].append(case)
            directory = log_root / case_name; directory.mkdir()
            for name in storage.LOGS:
                log = directory / name
                log.write_bytes(b'Synthetic process log: no native operation was executed.\n')
                report['logs'][case_name + '/' + name] = {'sha256': release.sha(log), 'bytes': log.stat().st_size}
        self.paths.append(path); self.reports.append(report)
        self.save()

    def save(self):
        for path, report in zip(self.paths, self.reports):
            path.write_bytes(release.encoded(report))

    def verify(self):
        return storage.verify_upgrades(self.manifest, self.previous_release, self.distribution,
                                       self.previous_distribution, self.paths)

    def test_archive_binding_preserves_missing_native_platforms_and_publication_lock(self):
        self.add_campaign('linux_arm64', 'aarch64')
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertEqual(result['remaining_targets'], ['darwin_amd64', 'darwin_arm64', 'windows_amd64', 'windows_arm64'])
        self.assertEqual({row['target'] for row in result['targets']}, {'linux_amd64', 'linux_arm64'})
        self.assertFalse(result['release_gate_passed'])
        self.assertFalse(result['endurance_required'])
        self.assertFalse(result['same_format_upgrade_qualified'])
        self.assertIn(str(self.previous_release), result['input_sha256'])
        self.assertIn(str(self.distribution / storage.archive_name(self.manifest, 'linux_amd64')), result['input_sha256'])
        self.assertIn(str(self.paths[0].with_name(self.paths[0].name + '.inputs') / 'snapshot/restored.log'), result['input_sha256'])

    def test_stale_diagnostic_or_summary_reports_cannot_qualify(self):
        original = copy.deepcopy(self.reports[0])
        mutations = [lambda row: row.update(schema_version=1), lambda row: row.update(status='incomplete'),
                     lambda row: row.update(source_changed=[storage.SOURCE]),
                     lambda row: row.update(source_files={storage.SOURCE: 'f' * 64}),
                     lambda row: row.update(release_gate_passed=True),
                     lambda row: row.update(cases=[]), lambda row: row.update(logs={})]
        for index, mutation in enumerate(mutations):
            with self.subTest(index=index):
                self.reports[0] = copy.deepcopy(original); mutation(self.reports[0]); self.save()
                with self.assertRaises(ValueError): self.verify()

    def test_other_binary_bytes_or_source_versions_cannot_reuse_an_upgrade(self):
        original = copy.deepcopy(self.reports[0])
        for role in original['executables']:
            for field, value in [('sha256', 'f' * 64), ('version', 'dev-dirty unknown source')]:
                with self.subTest(role=role, field=field):
                    self.reports[0] = copy.deepcopy(original)
                    self.reports[0]['executables'][role][field] = value; self.save()
                    with self.assertRaisesRegex(ValueError, 'tested executable differs'): self.verify()

    def test_repeated_effects_lost_history_and_changed_ownership_are_rejected(self):
        original = copy.deepcopy(self.reports[0])
        mutations = [lambda case: case['upgrade']['provider_counts'].update({'/notify-delivered': 2}),
                     lambda case: case['restore']['provider_counts'].update(failed_health_checks=16),
                     lambda case: case['restore'].update(history=[]),
                     lambda case: case['restore']['history'][0].update(kind='changed-event'),
                     lambda case: case['upgrade']['history'].append(copy.deepcopy(case['upgrade']['history'][0])),
                     lambda case: case['upgrade']['incident'].update(acknowledgedBy='someone-else'),
                     lambda case: case['restore'].update(node_id='another-node'),
                     lambda case: case['restore']['monitor']['metadata'].update(uid='new-incarnation'),
                     lambda case: case['restore']['actions'][0].update(executionRevision='f' * 64),
                     lambda case: case['restore']['actions'][0].update(held=False),
                     lambda case: case['restore']['actions'][1].update(state='unknown', held=True),
                     lambda case: case.update(older_owner_exit_code=0),
                     lambda case: case.update(old_token_status=200),
                     lambda case: case.update(snapshot_count=1)]
        for index, mutation in enumerate(mutations):
            with self.subTest(index=index):
                self.reports[0] = copy.deepcopy(original); mutation(self.reports[0]['cases'][0]); self.save()
                with self.assertRaises(ValueError): self.verify()

    def test_original_logs_cannot_be_removed_changed_or_linked(self):
        log = self.paths[0].with_name(self.paths[0].name + '.inputs') / 'log/older-owner.log'
        data = log.read_bytes()
        for action in ('change', 'remove', 'link'):
            with self.subTest(action=action):
                if log.exists() or log.is_symlink(): log.unlink()
                if action == 'change': log.write_bytes(b'changed original log')
                elif action == 'link':
                    external = self.root / 'outside.log'; external.write_bytes(data); log.symlink_to(external)
                with self.assertRaises((ValueError, FileNotFoundError)): self.verify()

    def test_identical_unfrozen_worker_or_nonadvancing_source_is_rejected(self):
        original = copy.deepcopy(self.previous)
        mutations = [lambda row: row.update(commit=self.manifest['commit']),
                     lambda row: row.update(version=self.manifest['version']),
                     lambda row: row.update(candidate=True),
                     lambda row: row.update(storage_format_version=self.manifest['storage_format_version']),
                     lambda row: row['recipe']['build_tags'].append('externaljobs')]
        for index, mutation in enumerate(mutations):
            with self.subTest(index=index):
                previous = copy.deepcopy(original); mutation(previous)
                self.previous_release.write_bytes(release.encoded(previous))
                with self.assertRaises(ValueError): self.verify()

    def test_duplicate_missing_or_mislabeled_platform_is_rejected(self):
        with self.assertRaises(ValueError):
            storage.verify_upgrades(self.manifest, self.previous_release, self.distribution,
                                     self.previous_distribution, [self.paths[0], self.paths[0]])
        self.reports[0]['platform'] = 'Darwin-synthetic-fixture'; self.save()
        with self.assertRaisesRegex(ValueError, 'supported native Linux'): self.verify()

    def check_inputs(self):
        with patch.object(storage.release, 'check_manifest') as source_check, \
                patch.object(storage.platform, 'system', return_value='Linux'), \
                patch.object(storage.platform, 'machine', return_value='x86_64'):
            result = storage.check_executables(self.manifest, self.previous_release, self.distribution,
                                               self.previous_distribution, self.root / 'candidate-stage-linux_amd64',
                                               self.root / 'previous-stage-linux_amd64')
        source_check.assert_called_once_with(self.manifest)
        return result

    def test_input_validation_binds_the_exact_stages_without_executing_them(self):
        with patch.object(storage.publish, 'run', side_effect=AssertionError('executed an artifact')):
            result = self.check_inputs()
        self.assertEqual(result['target'], 'linux_amd64')
        self.assertFalse(result['release_gate_passed'])
        binary = self.root / 'previous-stage-linux_amd64/cpra'; binary.write_bytes(b'another build')
        with self.assertRaisesRegex(ValueError, 'stage differs'): self.check_inputs()

    def test_bad_source_prevents_artifact_inspection_and_linked_stages_are_rejected(self):
        with patch.object(storage.release, 'check_manifest', side_effect=ValueError('unfrozen source')), \
                patch.object(storage.publish, 'archive_programs', side_effect=AssertionError('archive inspected')):
            with self.assertRaisesRegex(ValueError, 'unfrozen source'):
                storage.check_executables(self.manifest, self.previous_release, self.distribution,
                                           self.previous_distribution, self.root / 'candidate-stage-linux_amd64',
                                           self.root / 'previous-stage-linux_amd64')
        stage = self.root / 'candidate-stage-linux_amd64'
        actual = self.root / 'actual-stage'; stage.rename(actual); stage.symlink_to(actual)
        with self.assertRaisesRegex(ValueError, 'stage contains a link'): self.check_inputs()


class PreviousRunTests(unittest.TestCase):
    def setUp(self):
        self.manifest = manifest_fixture()
        self.repository = 'fixture/private-cpra'
        self.run = {'id': 123, 'path': '.github/workflows/release.yml', 'event': 'workflow_dispatch',
                    'status': 'completed', 'conclusion': 'failure', 'head_sha': self.manifest['commit'],
                    'repository': {'full_name': self.repository, 'private': True},
                    'head_repository': {'full_name': self.repository, 'private': True}}
        self.jobs = {'total_count': 1, 'jobs': [{'name': 'build', 'run_id': 123, 'status': 'completed',
                                               'conclusion': 'success', 'head_sha': self.manifest['commit']}]}

    def verify(self):
        storage.validate_previous_run(self.manifest, self.run, self.jobs, self.repository, '123')

    def test_successful_build_is_distinct_from_a_qualified_previous_release(self):
        self.verify()
        self.assertEqual(self.run['conclusion'], 'failure')

    def test_wrong_private_repository_workflow_event_source_or_run_is_rejected(self):
        original = copy.deepcopy(self.run)
        mutations = [lambda run: run.update(id=124), lambda run: run.update(status='in_progress'),
                     lambda run: run.update(event='pull_request'), lambda run: run.update(path='.github/workflows/ci.yml'),
                     lambda run: run.update(head_sha='f' * 40),
                     lambda run: run['repository'].update(private=False),
                     lambda run: run['head_repository'].update(full_name='someone/fork')]
        for index, mutation in enumerate(mutations):
            with self.subTest(index=index):
                self.run = copy.deepcopy(original); mutation(self.run)
                with self.assertRaises(ValueError): self.verify()

    def test_missing_failed_duplicated_or_truncated_build_jobs_are_rejected(self):
        original = copy.deepcopy(self.jobs)
        mutations = [lambda jobs: jobs.update(total_count=2), lambda jobs: jobs.update(jobs=[]),
                     lambda jobs: jobs['jobs'][0].update(conclusion='failure'),
                     lambda jobs: jobs['jobs'][0].update(run_id=124),
                     lambda jobs: jobs['jobs'][0].update(head_sha='f' * 40),
                     lambda jobs: jobs.update(total_count=2, jobs=jobs['jobs'] * 2)]
        for index, mutation in enumerate(mutations):
            with self.subTest(index=index):
                self.jobs = copy.deepcopy(original); mutation(self.jobs)
                with self.assertRaises(ValueError): self.verify()


class UpgradeLogTests(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location('native_upgrade', release.ROOT / storage.SOURCE)
        self.upgrade = importlib.util.module_from_spec(spec); spec.loader.exec_module(self.upgrade)
        temporary = tempfile.TemporaryDirectory(); self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name); self.work = self.root / 'work'
        self.private = self.work / 'log/private'; self.private.mkdir(parents=True)
        self.out = self.root / 'result.json'

    def test_retention_copies_only_logs_and_preserves_their_original_bytes(self):
        (self.private / 'original.token').write_bytes(b'private-token-fixture')
        (self.private / 'wrapping.key').write_bytes(b'private-key-fixture')
        (self.private / 'previous.log').write_bytes(b'original native fixture output\n')
        records = self.upgrade.retain_logs(self.work, self.out)
        copied = self.out.with_name('result.json.inputs') / 'log/previous.log'
        self.assertEqual(copied.read_bytes(), b'original native fixture output\n')
        self.assertEqual(records['log/previous.log']['sha256'], release.sha(copied))
        self.assertEqual(copied.stat().st_mode & 0o777, 0o600)
        self.assertEqual(set(records), {'log/previous.log'})
        self.assertEqual(list(copied.parent.iterdir()), [copied])

    def test_tokens_and_private_pem_never_enter_retained_logs(self):
        for index, content in enumerate((b'leaked bearer: private-token-fixture', b'-----BEGIN PRIVATE KEY-----')):
            with self.subTest(index=index):
                (self.private / 'original.token').write_bytes(b'private-token-fixture')
                (self.private / 'previous.log').write_bytes(content)
                out = self.root / ('bad-' + str(index) + '.json')
                with self.assertRaisesRegex(RuntimeError, 'authentication material'):
                    self.upgrade.retain_logs(self.work, out)
                self.assertFalse((out.with_name(out.name + '.inputs') / 'log/previous.log').exists())

    def test_linked_or_oversized_log_is_rejected(self):
        log = self.private / 'previous.log'
        external = self.root / 'outside'; external.write_text('outside')
        log.symlink_to(external)
        with self.assertRaisesRegex(RuntimeError, 'linked or exceeds'):
            self.upgrade.retain_logs(self.work, self.root / 'linked.json')
        log.unlink()
        with log.open('wb') as stream: stream.truncate(8 * 1024 ** 2 + 1)
        with self.assertRaisesRegex(RuntimeError, 'linked or exceeds'):
            self.upgrade.retain_logs(self.work, self.root / 'oversized.json')


if __name__ == '__main__':
    unittest.main()
