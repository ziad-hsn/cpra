"""Small recorded-report fixtures; these do not execute or qualify releases."""
import copy
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
import zipfile

import publish
import release
from oci import read_image, docker_config_digest
from evidence import read_report
from test_evidence import manifest_fixture
from test_oci import image_fixture, write_tar


def sha(data):
    return hashlib.sha256(data).hexdigest()


def scenarios(names):
    return [{'name': name, 'status': 'pass'} for name in names]


def scenario(report, name, field='checks'):
    return next(item for item in report[field] if item['name'] == name)


def unix_permissions(scope):
    owner, executable_gid = (0, 0) if scope == 'system' else (1001, 1001)
    modes = {
        'binary_directory': (owner, executable_gid, 0o755 if scope == 'system' else 0o700, 'directory'),
        'binary': (owner, executable_gid, 0o755 if scope == 'system' else 0o700, 'regular'),
        'config_directory': (owner, 1001, 0o750 if scope == 'system' else 0o700, 'directory'),
        'monitors': (owner, 1001, 0o640 if scope == 'system' else 0o600, 'regular'),
        'runtime': (owner, 1001, 0o640 if scope == 'system' else 0o600, 'regular'),
        'api_token': (owner, 1001, 0o640 if scope == 'system' else 0o600, 'regular'),
        'state_directory': (1001, 1001, 0o700, 'directory'),
        'service_definition': (owner, executable_gid, 0o644 if scope == 'system' else 0o600, 'regular'),
    }
    return {name: dict(uid=uid, gid=gid, mode=mode, type=kind)
            for name, (uid, gid, mode, kind) in modes.items()}


class PackagingEvidenceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.out = Path(temporary.name)
        self.manifest = manifest_fixture()
        self.manifest['source_files']['scripts/release/recipe.json'] = {
            'mode': '100644', 'sha256': sha(b'recipe fixture')}
        self.programs = {}
        self.payloads = {}
        self.reports = {}
        self.base = {
            'status': 'pass', 'version': self.manifest['version'], 'commit': self.manifest['commit'],
            'release_identity': release.evidence_identity(self.manifest), 'source': self.manifest,
            'source_candidate': False, 'source_dirty': False,
        }
        self.archives()
        self.native_reports()
        self.package_reports()
        self.deployment_reports()
        self.reports['reproducibility.json'] = dict(
            self.base, packages_compared=True, excluded_tools=['git', 'node', 'pnpm'],
            source_sha256=self.file('source.tar.gz', b'source archive fixture'),
            recipe_sha256=sha(b'recipe fixture'), unsigned_payloads=[self.payloads.copy(), self.payloads.copy()])
        self.reports['documentation.json'] = dict(self.base, docs_commit='d' * 40)
        self.save()

    def file(self, suffix, data):
        path = self.out / ('cpra-' + self.manifest['version'] + '-' + suffix)
        path.write_bytes(data)
        return release.sha(path)

    def archives(self):
        for target in release.RECIPE['targets']:
            extension = '.exe' if target.startswith('windows_') else ''
            files = {program + extension: ('fixture ' + target + '/' + program).encode()
                     for program in ('cpra', 'cpractl')}
            self.programs[target] = {program.removesuffix(extension) if extension else program: sha(data)
                                     for program, data in files.items()}
            for program, data in files.items():
                self.payloads[target + '/' + program] = sha(data)
            files['DEPENDENCIES.json'] = json.dumps({'target': target, 'binaries': {name: sha(data) for name, data in files.items()}}).encode()
            files['RELEASE.json'] = json.dumps(self.manifest).encode()
            suffix = target.replace('_', '-') + ('.zip' if extension else '.tar.gz')
            path = self.out / ('cpra-' + self.manifest['version'] + '-' + suffix)
            if extension:
                with zipfile.ZipFile(path, 'w') as archive:
                    for name, data in files.items():
                        archive.writestr(name, data)
            else:
                with tarfile.open(path, 'w:gz') as archive:
                    for name, data in files.items():
                        member = tarfile.TarInfo(name)
                        member.size = len(data)
                        member.mode = 0o755 if name in ('cpra', 'cpractl') else 0o644
                        archive.addfile(member, io.BytesIO(data))
            self.payloads[path.name] = release.sha(path)
        for arch in ('amd64', 'arm64'):
            for format in ('deb', 'rpm'):
                suffix = 'linux-' + arch + '.' + format
                self.payloads['cpra-' + self.manifest['version'] + '-' + suffix] = self.file(suffix, suffix.encode())
        suffix = 'compose.tar.gz'
        self.payloads['cpra-' + self.manifest['version'] + '-' + suffix] = self.file(suffix, b'compose fixture')

    def native_reports(self):
        for target in release.RECIPE['targets']:
            goos, arch = target.split('_')
            programs = self.programs[target]
            native = dict(self.base, os={'linux': 'Linux', 'darwin': 'Darwin', 'windows': 'Windows'}[goos],
                          architecture={'amd64': 'x86_64', 'arm64': 'arm64'}[arch],
                          execution_environment='native-host', binary_sha256=programs['cpra'],
                          cli_sha256=programs['cpractl'])
            storage = {'mode': 'raft', 'ready': True, 'single_node': True,
                       'node_id': 'node-fixture', 'committed_index': 0}
            self.reports['native-' + target + '.json'] = dict(
                native, restart_states=[{'storage': storage.copy()}, {'storage': storage.copy()}])
            installations = [
                {'command': module + '@' + self.manifest['version'], 'tags': tags,
                 'identity': 'cpra ' + self.manifest['version'], 'sha256': programs[program]}
                for module, program in ((release.RECIPE['module'], 'cpra'), (release.RECIPE['module'] + '/cmd/cpractl', 'cpractl'))
                for tags in ('', ' '.join(release.RECIPE['build_tags']))]
            self.reports['go-install-' + target + '.json'] = dict(native, installations=installations)
            steps = scenarios([
                'clean supervisor environment and source identity',
                'fresh installation remains stopped',
                'native supervisor start and authenticated readiness',
                'running update preserves configuration, state and startup mode',
                'deliberate stop remains stopped',
                'stopped update preserves disabled state and edited configuration',
                'uninstall removes owned integration and preserves complete state/configuration',
                'reinstall and stopped backup restore recover under the service identity',
            ])
            steps[2]['node_id'] = steps[7]['node_id'] = 'node-fixture'
            steps[4]['observed_seconds'] = 21
            acl = {'service_sid': 'S-1-5-80-123', **{
                path: {'owner': 'BUILTIN\\Administrators', 'protected': True,
                       'rules': [{'sid': 'S-1-5-80-123', 'allow': True, 'rights': 2 if path == 'state' else 1}]}
                for path in ('binary', 'config', 'state')}}
            permission = {'name': 'service ACLs and LocalService identity', 'status': 'pass', 'acl': acl} \
                if goos == 'windows' else {'name': 'native process identity and filesystem ownership', 'status': 'pass',
                                         'service_uid': 1001, 'service_gid': 1001, 'binary_uid': 0,
                                         'filesystem': unix_permissions('system')}
            steps.extend([dict(permission, phase=phase) for phase in ('initial_startup', 'running_update', 'restored_startup')])
            for scope in (('system',) if goos == 'windows' else ('system', 'user')):
                name = ('service-user-' if scope == 'user' else 'service-') + target + '.json'
                self.reports[name] = dict(native, scope='native ' + scope + ' service lifecycle',
                                          steps=copy.deepcopy(steps), provider_operations=0, manifest_monitor_count=0,
                                          filesystem='fixture-filesystem', supervisor_version='fixture-supervisor',
                                          encryption={'key_id': 'local-sha256:' + 'b' * 64, 'inspection_matches': True},
                                          authentication={
                                              'initial_epoch': 'initial-epoch', 'initial_revision': 'initial-revision',
                                              'reset_epoch': 'restored-epoch', 'reset_revision': 'reset-revision',
                                              'restored_epoch': 'restored-epoch', 'restored_revision': 'restored-revision',
                                              'initial_principal': 'native/initial', 'restored_principal': 'native/restored',
                                              'observed_principal': 'native/restored', 'reset_required': True,
                                              'tls_verified': True, 'certificate_sha256': 'a' * 64,
                                              'old_named_status': 401, 'startup_token_status': 401})
                if scope == 'user':
                    for step in self.reports[name]['steps']:
                        if 'binary_uid' in step:
                            step['binary_uid'] = step['service_uid']
                            step['filesystem'] = unix_permissions('user')

    def package_reports(self):
        for arch in ('amd64', 'arm64'):
            for format in ('deb', 'rpm'):
                programs = self.programs['linux_' + arch]
                self.reports['package-' + format + '-' + arch + '.json'] = dict(
                    self.base, format=format, arch=arch,
                    package_sha256=self.payloads['cpra-' + self.manifest['version'] + '-linux-' + arch + '.' + format],
                    binary_sha256=programs['cpra'], cli_sha256=programs['cpractl'],
                    execution_environment='linux-systemd-container', filesystem='fixture-filesystem',
                    os='Linux', architecture='x86_64' if arch == 'amd64' else 'aarch64',
                    manifest_monitor_count=0, provider_operations=0,
                    observations=self.package_observations(programs),
                    systemd='fixture-systemd', package_manager='fixture-package-manager', kernel='fixture-kernel',
                    os_release='fixture-os', supervisor_pacing_seconds=61,
                    scenarios=['fresh-stopped', 'edited-config', 'start-ready', 'running-revision-upgrade',
                               'different-version-stopped-upgrade', 'semver-a.1-to-a-1',
                               'prerelease-to-final-running-upgrade', 'disabled-stopped-revision-upgrade',
                               'remove', 'reinstall', 'purge-preserved-config' if format == 'deb' else 'erase-rpmsave'],
                    fixture_versions=[
                        {'name': name, 'version': release.package_version(version), 'revision': revision}
                        for name, version, revision in (
                            ('revision', self.manifest['version'], '2'),
                            ('early', 'v0.1.1-a.1', '1'), ('late', 'v0.1.1-a-1', '1'),
                            ('final', 'v0.1.1', '1'), ('final-revision', 'v0.1.1', '2'))])

    def package_observations(self, programs):
        permissions = unix_permissions('system')
        permissions['cli'] = dict(uid=0, gid=0, mode=0o755, type='regular')
        rows = []
        for index, phase in enumerate(('fresh_install', 'initial_startup', 'running_revision_upgrade',
                                       'prerelease_final_upgrade', 'reinstall')):
            row = dict(phase=phase, service_uid=1001, service_gid=1001, filesystem=copy.deepcopy(permissions),
                       binary_sha256=programs['cpra'], cli_sha256=programs['cpractl'])
            if phase not in ('fresh_install', 'reinstall'):
                row.update(process=dict(pid=100 + index, start_ticks=1000 + index, uid=1001, gid=1001,
                                        binary_sha256=programs['cpra']),
                           state={'storage': dict(mode='raft', ready=True, single_node=True,
                                                  node_id='package-node', committed_index=7 + index)},
                           monitor_count=0,
                           pool_tasks={name: dict(tasks_submitted=0, tasks_completed=0)
                                       for name in ('pulse', 'intervention', 'code')})
            rows.append(row)
        return rows

    def transport_record(self, path):
        return {'path': path.name, 'sha256': release.sha(path), 'size': path.stat().st_size}

    def image_report(self, arch, image_id, **options):
        files, reference = image_fixture(arch=arch, source=self.manifest, **options)
        stem = 'image-repro-' + arch
        reference_path = self.out / (stem + '-reference.docker.tar')
        write_tar(reference_path, reference)
        config = docker_config_digest(reference_path, 'linux/' + arch)
        builds = []
        for number, mtime in enumerate((0, 86523)):
            path = self.out / (stem + f'-build-{number}.oci.tar')
            write_tar(path, files, mtime=mtime)
            image = read_image(path, 'linux/' + arch)
            payload = image['oci_payload']
            builds.append({'archive': self.transport_record(path), 'oci_payload': payload,
                           'input_mtime': mtime, 'config_digests': [image['config_digest']],
                           'manifest_digest': image['manifest_digest'],
                           'expanded_bytes': image['expanded_bytes'],
                           'payload_sha256': sha(json.dumps(payload, sort_keys=True).encode())})
        return dict(self.base, platform='linux/' + arch,
                    reference_image={'id': image_id, 'config_digest': config,
                                     'archive': self.transport_record(reference_path)}, builds=builds)

    def deployment_reports(self):
        for arch in ('amd64', 'arm64'):
            image_id = 'sha256:' + sha((arch + 'image').encode())
            checks = scenarios([
                'exact_release_binary_hashes_and_hardened_runtime',
                'real_http_outage_records_incident_without_liveness_failure',
                'second_owner_rejected', 'sigterm_drains_within_supervisor_budget',
                'restart_recovers_identity_and_history', 'versioned_token_file_rotation',
                'down_up_retains_named_volume',
            ])
            checks[0].update(binary_sha256=self.programs['linux_' + arch], image_id=image_id)
            checks[1]['event_ids'] = ['11:0', '12:0']
            checks[5].update(committed_revision='fixture-rotation-revision', old_token_status=401)
            self.reports['container-' + arch + '.json'] = dict(
                self.base, checks=checks, platform='linux/' + arch, cleanup='pass',
                state_filesystem='fixture-filesystem', kernel='fixture-kernel',
                compose_version='fixture-compose', docker_server_version='fixture-docker')
            self.reports['image-repro-' + arch + '.json'] = self.image_report(arch, image_id)
        for variant in ('helm3-to-4', 'helm4'):
            checks = scenarios([
                'install_readiness_nonroot_readonly', 'delayed_manifest_mount_starts_without_restart_churn',
                'rwop_blocks_contending_owner', 'probes_and_helm_test_cause_zero_provider_calls',
                'target_outage_retains_events_and_liveness', 'restart_restores_node_identity_and_history',
                'immutable_secret_rotation_rollout', 'failed_upgrade_then_explicit_recovery_preserves_state',
                'large_file_backed_manifest_survives_rollout', 'offline_whole_state_copy_includes_history',
                'uninstall_retains_original_pvc', 'restore_existing_claim_recovers_history_and_identity',
                'headless_mode_has_no_api_service_or_probes', 'uninstall_preserves_restored_and_original_claims',
            ])
            checks[1]['delay_seconds'] = 5
            checks[2]['scheduler_reason'] = 'ReadWriteOncePod contention fixture'
            checks[3].update(before=1, after=1)
            checks[4]['event_ids'] = ['11:0', '12:0']
            checks[6].update(committed_revision='fixture-rotation-revision', stopped_owner=True, old_token_status=401)
            checks[8].update(file_bytes=2**20 + 1, configured_monitors=10001, active_monitors=1,
                             source='separate read-only PVC', sha256=sha(b'large manifest fixture'))
            report = dict(self.base, checks=checks, access_mode='ReadWriteOncePod', csi_rwop_fencing='pass',
                          cleanup='pass', state_volume={'spec': {'csi': {'driver': 'fixture.csi.invalid'}}},
                          state_filesystem='fixture-filesystem', helm='v4.0.0+fixture',
                          image_id=self.reports['image-repro-amd64.json']['reference_image']['id'],
                          binary_sha256='\n'.join(value + '  /usr/local/bin/' + name
                                                 for name, value in self.programs['linux_amd64'].items()))
            # Reader contract fixture, not actual cluster execution evidence.
            report.update(management_enabled=True, namespace='cpra-fixture')
            managed = scenarios([
                'managed_secret_sources_and_verified_tls', 'managed_conditional_writes_and_redaction',
                'managed_https_probes_have_zero_provider_calls',
                'managed_restart_preserves_configuration_and_authority',
                'managed_access_rotation_requires_stopped_owner', 'managed_rwop_fences_second_owner',
                'managed_stopped_copy_restore_preserves_encrypted_history',
            ])
            managed[0].update(uid=1001, directory_mode='0700',
                              file_modes={file: '0600' for file in ('read-token', 'policy.yaml', 'wrapping.key', 'tls.crt', 'tls.key', 'ca.crt')},
                              input_mount_absent=True, init_state_mount_absent=True, protected_mount_read_only=True,
                              binary_sha256=report['binary_sha256'], image_id=report['image_id'],
                              service_dns='cpra-managed.cpra-fixture.svc', tls_verified=True)
            managed[1].update(operation_ids=['credential-created', 'monitor-created', 'monitor-patched'],
                              monitor_uid='fixture-monitor-incarnation', credential_uid='fixture-credential-incarnation',
                              credential_value_available=False, read_token_write_status=403, reader_write_status=403,
                              wrong_token_status=401, stale_version_status=412)
            managed[2].update(before=3, after=3, service_dns='cpra-managed.cpra-fixture.svc', tls_verified=True)
            managed[3].update(previous_pod_uid='fixture-old-pod', current_pod_uid='fixture-new-pod',
                              node_identity_sha256=sha(b'managed fixture identity'),
                              monitor_uid='fixture-monitor-incarnation', credential_uid='fixture-credential-incarnation',
                              replacement_policy_token_status=401, original_operator_status=200)
            managed[4].update(stopped_owner=True, administrative_pod_removed=True,
                              committed_revisions=['read-rotation', 'operator-rotation'], old_read_token_status=401,
                              old_operator_status=401, new_read_token_status=200, new_operator_status=200)
            managed[5].update(scheduler_reason='ReadWriteOncePod contention fixture', csi_driver='fixture.csi.invalid',
                              state_filesystem='fixture-filesystem', claim='state-cpra-managed-0')
            managed[6].update(stopped_owner=True, backup_method='trusted stopped complete-directory clone',
                              original_claim='state-cpra-managed-0', restored_claim='cpra-managed-restored',
                              inventory_sha256=sha(b'stopped state fixture inventory'), inventory_files=5,
                              credential_plaintext_absent=True, event_ids=['31:0'], restored_event_ids=['31:0'],
                              node_identity_sha256=sha(b'managed fixture identity'),
                              monitor_uid='fixture-monitor-incarnation', credential_uid='fixture-credential-incarnation',
                              target_before=4, target_after=4)
            checks.extend(managed)
            if variant == 'helm3-to-4':
                report.update(helm='v3.19.0+fixture', upgrade_helm='v4.0.0+fixture')
                checks.append({'name': 'in_place_upgrade_between_helm_versions', 'status': 'pass',
                               'previous': report['helm'], 'current': report['upgrade_helm']})
                checks.append({'name': 'managed_upgrade_between_helm_versions', 'status': 'pass',
                               'previous': report['helm'], 'current': report['upgrade_helm'],
                               'monitor_uid': 'fixture-monitor-incarnation', 'credential_uid': 'fixture-credential-incarnation'})
            self.reports['chart-' + variant + '.json'] = report

    def save(self):
        (self.out / 'RELEASE.json').write_bytes(release.encoded(self.manifest))
        for name, report in self.reports.items():
            (self.out / name).write_text(json.dumps(report))

    def reject(self, name, mutate):
        original = copy.deepcopy(self.reports[name])
        try:
            mutate(self.reports[name])
            self.save()
            with self.assertRaisesRegex(ValueError, name.replace('.', r'\.')):
                publish.evidence(self.out, self.manifest)
        finally:
            self.reports[name] = original
            self.save()

    def test_complete_recorded_matrix_and_real_archive_bytes_are_accepted(self):
        self.assertEqual(len(self.reports), 34)
        self.assertEqual(len(self.payloads), 23)
        publish.evidence(self.out, self.manifest)

    def test_native_pass_without_restart_observations_is_rejected(self):
        for target in release.RECIPE['targets']:
            self.reject('native-' + target + '.json', lambda report: report.pop('restart_states'))

    def test_restart_requires_same_usable_raft_identity_and_monotonic_commit(self):
        changes = ({'node_id': 'replacement-node'}, {'mode': 'memory'}, {'ready': False}, {'single_node': False},
                   {'committed_index': -1}, {'committed_index': True}, {'error': 'storage unavailable'})
        for change in changes:
            with self.subTest(change=change):
                self.reject('native-linux_amd64.json', lambda report: report['restart_states'][1]['storage'].update(change))
        self.reports['native-linux_amd64.json']['restart_states'][0]['storage']['committed_index'] = 7
        self.reject('native-linux_amd64.json', lambda report: report['restart_states'][1]['storage'].update(committed_index=6))

    def test_each_recorded_service_and_deployment_case_is_required(self):
        for name, report in list(self.reports.items()):
            field = 'steps' if name.startswith('service-') else 'checks'
            if field not in report:
                continue
            for case in {item['name'] for item in report[field]}:
                with self.subTest(report=name, case=case):
                    self.reject(name, lambda report: report.update({field: [item for item in report[field] if item['name'] != case]}))

    def test_failed_duplicate_or_malformed_scenarios_cannot_hide_in_a_pass(self):
        name = 'container-amd64.json'
        for value in ('fail', 'not-configured', 'skipped', True, None):
            with self.subTest(status=value):
                self.reject(name, lambda report: report['checks'][0].update(status=value))
        self.reject(name, lambda report: report['checks'].append(report['checks'][0].copy()))
        self.reject(name, lambda report: report['checks'].append('pass'))

    def test_pending_errors_and_cleanup_failures_reject_all_report_types(self):
        for name in self.reports:
            with self.subTest(report=name):
                self.reject(name, lambda report: report.update(pending_verification='not executed'))
                self.reject(name, lambda report: report.update(errors=['failed observation']))
                self.reject(name, lambda report: report.update(cleanup_errors=['owner still running']))

    def test_service_scope_restore_identity_and_stop_observation_are_required(self):
        name = 'service-user-linux_amd64.json'
        self.reject(name, lambda report: report.update(scope='native system service lifecycle'))
        self.reject(name, lambda report: report.update(provider_operations=True))
        self.reject(name, lambda report: report.update(manifest_monitor_count=1))
        self.reject(name, lambda report: report.update(filesystem=' unknown '))
        self.reject(name, lambda report: scenario(report, 'deliberate stop remains stopped', 'steps').update(observed_seconds=1))
        self.reject(name, lambda report: scenario(report, 'reinstall and stopped backup restore recover under the service identity', 'steps').update(node_id='new-node'))

    def test_repeated_service_permission_observations_cannot_omit_or_contradict_ownership(self):
        for target in ('linux_amd64', 'darwin_arm64'):
            name = 'service-' + target + '.json'
            self.reject(name, lambda report: report['steps'][-2].pop('service_uid'))
            self.reject(name, lambda report: report['steps'][-1].update(binary_uid=1001))
            self.reject(name, lambda report: report['steps'][-1].update(service_uid=True))
        name = 'service-windows_amd64.json'
        self.reject(name, lambda report: report['steps'][-2].pop('acl'))
        self.reject(name, lambda report: report['steps'][-1]['acl']['state'].update(protected=False))
        self.reject(name, lambda report: report['steps'][-1]['acl']['binary'].update(rules=[]))
        self.reject(name, lambda report: report['steps'][-1]['acl']['binary']['rules'][0].update(rights=True))

    def test_service_requires_observed_local_key_initialization(self):
        for name in ('service-user-linux_amd64.json', 'service-darwin_arm64.json', 'service-windows_amd64.json'):
            with self.subTest(name=name):
                self.reject(name, lambda report: report.pop('encryption'))
                for invalid in (None, {}, {'key_id': 'b' * 64, 'inspection_matches': True},
                                {'key_id': 'local-sha256:' + 'b' * 63, 'inspection_matches': True},
                                {'key_id': 'local-sha256:' + 'b' * 64},
                                {'key_id': 'local-sha256:' + 'b' * 64, 'inspection_matches': False},
                                {'key_id': 'local-sha256:' + 'b' * 64, 'inspection_matches': 'true'}):
                    self.reject(name, lambda report: report.update(encryption=invalid))

    def test_service_permissions_require_private_configuration_and_protected_executable_parents(self):
        for target in ('linux_amd64', 'darwin_arm64'):
            for scope in ('system', 'user'):
                name = ('service-' if scope == 'system' else 'service-user-') + target + '.json'
                with self.subTest(target=target, scope=scope):
                    self.reject(name, lambda report: report['steps'][-2].pop('filesystem'))
                    self.reject(name, lambda report: report['steps'][-1].pop('service_gid'))
                    self.reject(name, lambda report: report['steps'][-1].update(service_gid=True))
                for path in unix_permissions(scope):
                    with self.subTest(target=target, scope=scope, path=path):
                        self.reject(name, lambda report: report['steps'][-2]['filesystem'].pop(path))
                        for field, value in (('mode', 0o777), ('mode', True), ('uid', 1002),
                                             ('gid', 1002), ('type', 'symlink')):
                            with self.subTest(field=field, value=value):
                                self.reject(name, lambda report: report['steps'][-1]['filesystem'][path].update({field: value}))

    def test_permission_observations_cannot_substitute_startup_for_upgrade_or_restore(self):
        for name in ('service-user-linux_amd64.json', 'service-darwin_arm64.json', 'service-windows_amd64.json'):
            with self.subTest(report=name):
                self.reject(name, lambda report: report['steps'].pop())
                self.reject(name, lambda report: report['steps'][-1].pop('phase'))
                for phase in ('initial_startup', True, ['restored_startup'], None):
                    with self.subTest(phase=phase):
                        self.reject(name, lambda report: report['steps'][-1].update(phase=phase))

    def test_service_restore_requires_new_authority_and_observed_credential_rejection(self):
        name = 'service-linux_amd64.json'
        self.reject(name, lambda report: report.pop('authentication'))
        for field in list(self.reports[name]['authentication']):
            with self.subTest(field=field):
                self.reject(name, lambda report: report['authentication'].pop(field))
        for change in ({'reset_epoch': 'initial-epoch', 'restored_epoch': 'initial-epoch'},
                       {'restored_epoch': 'unrelated-epoch'}, {'reset_required': False},
                       {'restored_revision': 'initial-revision'}, {'restored_revision': 'reset-revision'},
                       {'observed_principal': 'native/initial'}, {'tls_verified': False},
                       {'certificate_sha256': 'unverified'}, {'old_named_status': 200},
                       {'startup_token_status': 503}, {'old_named_status': '401'}):
            with self.subTest(change=change):
                self.reject(name, lambda report: report['authentication'].update(change))

    def test_package_removal_variants_and_ordering_fixtures_are_required(self):
        for format in ('deb', 'rpm'):
            name = 'package-' + format + '-amd64.json'
            for case in self.reports[name]['scenarios'].copy():
                with self.subTest(format=format, case=case):
                    self.reject(name, lambda report: report['scenarios'].remove(case))
            self.reject(name, lambda report: report.update(fixture_versions=[]))
            self.reject(name, lambda report: report['fixture_versions'][-1].update(name='early'))
            self.reject(name, lambda report: report['fixture_versions'][-1].update(revision='1'))
            self.reject(name, lambda report: report.update(binary_sha256='0' * 64))
            self.reject(name, lambda report: report.update(execution_environment='emulator'))

    def test_package_requires_native_host_architecture_and_installed_observations(self):
        for format in ('deb', 'rpm'):
            name = 'package-' + format + '-amd64.json'
            for change in ({'architecture': 'aarch64'}, {'observations': []}, {'manifest_monitor_count': 1}):
                with self.subTest(format=format, change=change):
                    self.reject(name, lambda report: report.update(change))

    def test_package_observations_cannot_omit_upgrade_phases_or_change_installed_bytes(self):
        name = 'package-deb-amd64.json'
        for index in range(5):
            with self.subTest(phase=index):
                self.reject(name, lambda report: report['observations'].pop(index))
                self.reject(name, lambda report: report['observations'][index].update(binary_sha256='0' * 64))
                self.reject(name, lambda report: report['observations'][index].update(cli_sha256='0' * 64))
        self.reject(name, lambda report: report['observations'][2].update(phase='initial_startup'))
        self.reject(name, lambda report: report['observations'].reverse())

    def test_package_permissions_and_service_identity_must_survive_each_phase(self):
        name = 'package-rpm-arm64.json'
        for index in range(5):
            for field, change in (('binary', {'uid': 1001}), ('cli', {'mode': 0o777}),
                                  ('api_token', {'mode': 0o666}), ('state_directory', {'uid': 0}),
                                  ('runtime', {'type': 'symlink'}), ('service_definition', {'gid': True})):
                with self.subTest(phase=index, field=field):
                    self.reject(name, lambda report: report['observations'][index]['filesystem'][field].update(change))
            self.reject(name, lambda report: report['observations'][index].update(service_uid=0))
        self.reject(name, lambda report: report['observations'][2].update(service_gid=1002))
        self.reject(name, lambda report: report['observations'][0].update(process={'pid': 42}))

    def test_package_running_owner_and_raft_recovery_cannot_be_claimed_from_staging(self):
        name = 'package-deb-amd64.json'
        for index in (1, 2, 3):
            for change in ({'pid': True}, {'start_ticks': 0}, {'uid': 0}, {'gid': 1001.0},
                           {'binary_sha256': '0' * 64}):
                with self.subTest(phase=index, process=change):
                    self.reject(name, lambda report: report['observations'][index]['process'].update(change))
            self.reject(name, lambda report: report['observations'][index].pop('state'))
            self.reject(name, lambda report: report['observations'][index]['state']['storage'].update(ready=False))
        self.reject(name, lambda report: report['observations'][2].update(process=report['observations'][1]['process']))
        self.reject(name, lambda report: report['observations'][2]['state']['storage'].update(node_id='different-node'))
        self.reject(name, lambda report: report['observations'][2]['state']['storage'].update(committed_index=0))
        # Reused PID numbers are valid when the kernel start identity changes.
        for row in self.reports[name]['observations'][1:4]: row['process']['pid'] = 101
        self.save(); publish.evidence(self.out, self.manifest)

    def test_package_empty_fixture_requires_observed_zero_provider_counters(self):
        name = 'package-rpm-amd64.json'
        for index in (1, 2, 3):
            self.reject(name, lambda report: report['observations'][index].update(monitor_count=False))
            self.reject(name, lambda report: report['observations'][index].update(monitor_count=1))
            self.reject(name, lambda report: report['observations'][index].pop('pool_tasks'))
            for value in (1, False, 0.0):
                for field in ('tasks_submitted', 'tasks_completed'):
                    with self.subTest(phase=index, field=field, value=value):
                        self.reject(name, lambda report: report['observations'][index]['pool_tasks']['pulse'].update({field: value}))
        self.reject(name, lambda report: report.update(provider_operations=False))

    def test_source_install_matrix_platform_version_and_executable_are_required(self):
        name = 'go-install-windows_arm64.json'
        self.reject(name, lambda report: report.update(installations=[]))
        self.reject(name, lambda report: report['installations'].__setitem__(3, report['installations'][0]))
        self.reject(name, lambda report: report.update(os='Linux'))
        self.reject(name, lambda report: report.update(architecture='x86_64'))
        self.reject(name, lambda report: report['installations'][0].update(identity='cpra dev'))
        self.reject(name, lambda report: report['installations'][0].update(identity='cpra v0.1.0-rc.10'))
        self.reject(name, lambda report: report['installations'][0].update(sha256='truncated'))

    def test_compose_platform_hashes_and_retained_events_are_required(self):
        name = 'container-arm64.json'
        self.reject(name, lambda report: report.update(platform='linux/amd64'))
        self.reject(name, lambda report: report['checks'][0].update(binary_sha256=self.programs['linux_amd64']))
        self.reject(name, lambda report: report['checks'][1].update(event_ids=[]))
        self.reject(name, lambda report: report['checks'][1].update(event_ids=['11:0', '11:0']))
        self.reject(name, lambda report: report.update(cleanup='incomplete'))
        for change in ({'committed_revision': ''}, {'old_token_status': 200}, {'old_token_status': '401'}):
            self.reject(name, lambda report: scenario(report, 'versioned_token_file_rotation').update(change))

    def test_helm_requires_observed_storage_probe_and_large_file_outcomes(self):
        name = 'chart-helm4.json'
        self.reject(name, lambda report: report.update(access_mode='ReadWriteOnce'))
        for volume in ({}, {'spec': []}, {'spec': {'csi': None}}):
            self.reject(name, lambda report: report.update(state_volume=volume))
        self.reject(name, lambda report: report.update(helm='v3.19.0'))
        self.reject(name, lambda report: report.update(binary_sha256=''))
        for change in ({'committed_revision': ''}, {'stopped_owner': False}, {'old_token_status': 200}, {'old_token_status': '401'}):
            self.reject(name, lambda report: scenario(report, 'immutable_secret_rotation_rollout').update(change))
        self.reject(name, lambda report: scenario(report, 'rwop_blocks_contending_owner').pop('scheduler_reason'))
        self.reject(name, lambda report: scenario(report, 'delayed_manifest_mount_starts_without_restart_churn').update(delay_seconds=0))
        for before, after in ((0, 0), (True, True), (1, 2)):
            self.reject(name, lambda report: scenario(report, 'probes_and_helm_test_cause_zero_provider_calls').update(before=before, after=after))
        for change in ({'file_bytes': 1024}, {'configured_monitors': 10001.0}, {'active_monitors': True}, {'source': 'ConfigMap'}):
            self.reject(name, lambda report: scenario(report, 'large_file_backed_manifest_survives_rollout').update(change))
        self.reject('chart-helm3-to-4.json', lambda report: report.update(upgrade_helm='v3.19.1'))
        self.reject('chart-helm3-to-4.json', lambda report: scenario(report, 'in_place_upgrade_between_helm_versions').update(current='v4.0.1'))

    def test_read_only_helm_evidence_cannot_qualify_management(self):
        for name in ('chart-helm4.json', 'chart-helm3-to-4.json'):
            with self.subTest(report=name):
                self.reject(name, lambda report: report.update(management_enabled=False))

    def test_helm_management_requires_real_protected_mounts_and_matching_artifacts(self):
        name = 'chart-helm4.json'
        for change in ({'uid': 0}, {'uid': True}, {'directory_mode': '0750'}, {'file_modes': {}},
                       {'input_mount_absent': False}, {'init_state_mount_absent': False}, {'protected_mount_read_only': False},
                       {'binary_sha256': ''}, {'image_id': 'different'}, {'tls_verified': False}, {'service_dns': 'localhost'}):
            with self.subTest(change=change):
                self.reject(name, lambda report: scenario(report, 'managed_secret_sources_and_verified_tls').update(change))
        self.reject(name, lambda report: report.update(namespace='another-project'))
        for change in ({'before': 0}, {'before': True}, {'after': 4}, {'after': 3.0}, {'tls_verified': False}):
            self.reject(name, lambda report: scenario(report, 'managed_https_probes_have_zero_provider_calls').update(change))

    def test_helm_management_requires_conditional_commits_and_credential_redaction(self):
        name = 'chart-helm4.json'
        for change in ({'operation_ids': []}, {'operation_ids': ['a', 'b', 'a']}, {'monitor_uid': ''}, {'credential_uid': None},
                       {'credential_value_available': True}, {'read_token_write_status': 200}, {'reader_write_status': 200},
                       {'wrong_token_status': '401'}, {'stale_version_status': 200}):
            self.reject(name, lambda report: scenario(report, 'managed_conditional_writes_and_redaction').update(change))

    def test_helm_management_requires_authoritative_restart_and_stopped_rotation(self):
        name = 'chart-helm4.json'
        for change in ({'current_pod_uid': 'fixture-old-pod'}, {'monitor_uid': 'changed'}, {'credential_uid': 'changed'},
                       {'node_identity_sha256': ''}, {'replacement_policy_token_status': 200}, {'original_operator_status': 401}):
            self.reject(name, lambda report: scenario(report, 'managed_restart_preserves_configuration_and_authority').update(change))
        for change in ({'stopped_owner': False}, {'administrative_pod_removed': False}, {'committed_revisions': ['a', 'a']},
                       {'committed_revisions': []}, {'old_read_token_status': 200}, {'old_operator_status': 200},
                       {'new_read_token_status': '200'}, {'new_operator_status': 401}):
            self.reject(name, lambda report: scenario(report, 'managed_access_rotation_requires_stopped_owner').update(change))

    def test_helm_management_requires_fencing_and_complete_encrypted_history_restore(self):
        name = 'chart-helm4.json'
        for change in ({'scheduler_reason': 'generic failure'}, {'csi_driver': ''}, {'state_filesystem': ''}, {'claim': 'other'}):
            self.reject(name, lambda report: scenario(report, 'managed_rwop_fences_second_owner').update(change))
        for change in ({'stopped_owner': False}, {'backup_method': 'live copy'}, {'original_claim': 'other'},
                       {'restored_claim': 'state-cpra-managed-0'}, {'inventory_files': True}, {'inventory_files': 4097},
                       {'inventory_sha256': ''}, {'credential_plaintext_absent': False}, {'event_ids': []},
                       {'restored_event_ids': ['lost']}, {'restored_event_ids': ['31:0', '31:0']},
                       {'node_identity_sha256': sha(b'new identity')}, {'monitor_uid': 'new incarnation'},
                       {'target_before': 0}, {'target_after': 5}):
            self.reject(name, lambda report: scenario(report, 'managed_stopped_copy_restore_preserves_encrypted_history').update(change))
        self.reject('chart-helm3-to-4.json', lambda report: scenario(report, 'managed_upgrade_between_helm_versions').update(previous='v4.0.0'))
        self.reject('chart-helm3-to-4.json', lambda report: scenario(report, 'managed_upgrade_between_helm_versions').update(credential_uid='changed'))

    def test_image_rebuild_requires_complete_identical_inventories_and_reference_config(self):
        name = 'image-repro-amd64.json'
        self.reject(name, lambda report: report.update(builds=[{}, {}]))
        self.reject(name, lambda report: report['reference_image'].update(config_digest='sha256:' + '0' * 64))
        self.reject(name, lambda report: report['builds'][1].update(oci_payload={}))
        self.reject(name, lambda report: report['builds'][1].update(payload_sha256='0' * 64))
        self.reject(name, lambda report: report['builds'][1].update(input_mtime=report['builds'][0]['input_mtime']))
        self.reject(name, lambda report: report['builds'][1]['oci_payload'].update({'../blob': {'sha256': '0' * 64, 'size': 1}}))

    def test_image_transport_records_require_unique_exact_names_sizes_and_hashes(self):
        name = 'image-repro-amd64.json'
        for field in ('path', 'sha256', 'size'):
            self.reject(name, lambda report: report['reference_image']['archive'].pop(field))
            self.reject(name, lambda report: report['builds'][0]['archive'].pop(field))
        for value in ('../outside.tar', 'image-repro-arm64-build-0.oci.tar',
                      'image-repro-amd64-build-1.oci.tar'):
            self.reject(name, lambda report: report['builds'][0]['archive'].update(path=value))
        for value in (True, 0, 1.0, 2 ** 31 + 1):
            self.reject(name, lambda report: report['builds'][0]['archive'].update(size=value))
        self.reject(name, lambda report: report['reference_image']['archive'].update(sha256='0' * 64))
        self.reject(name, lambda report: report['builds'][1].update(archive=report['builds'][0]['archive']))

    def test_every_image_transport_is_required_and_cannot_change_after_verification(self):
        name = 'image-repro-amd64.json'
        records = [self.reports[name]['reference_image']['archive'],
                   *[build['archive'] for build in self.reports[name]['builds']]]
        for record in records:
            path = self.out / record['path']
            original = path.read_bytes()
            for changed in (None, b'changed archive bytes', original + b'changed transport header'):
                try:
                    if changed is None:
                        path.unlink()
                    else:
                        path.write_bytes(changed)
                    with self.assertRaisesRegex(ValueError, 'image-repro-amd64.json'):
                        publish.evidence(self.out, self.manifest)
                finally:
                    path.write_bytes(original)
        path = self.out / records[-1]['path']
        original = path.read_bytes()
        path.unlink()
        try:
            path.symlink_to(records[1]['path'])
            with self.assertRaisesRegex(ValueError, 'image-repro-amd64.json'):
                publish.evidence(self.out, self.manifest)
        finally:
            path.unlink()
            path.write_bytes(original)

    def test_reported_graph_digests_and_expansion_must_match_actual_image(self):
        name = 'image-repro-amd64.json'
        self.reject(name, lambda report: report['builds'][0].update(manifest_digest='sha256:' + '0' * 64))
        for value in (None, True, 0, 31.0, 999):
            self.reject(name, lambda report: report['builds'][0].update(expanded_bytes=value))

        def alter_inventory(report):
            payload = report['builds'][0]['oci_payload']
            payload['index.json']['size'] += 1
            report['builds'][0]['payload_sha256'] = sha(json.dumps(payload, sort_keys=True).encode())

        self.reject(name, alter_inventory)

    def test_image_runtime_and_source_labels_are_checked_from_the_saved_config(self):
        name = 'image-repro-amd64.json'
        image_id = self.reports[name]['reference_image']['id']
        edits = [lambda c: c['config'].update(User='0:0'),
                 lambda c: c['config'].update(WorkingDir='/tmp'),
                 lambda c: c['config'].update(Entrypoint=['/bin/sh']),
                 lambda c: c['config'].pop('Labels'),
                 lambda c: c['config']['Labels'].update({'org.opencontainers.image.version': 'v0.0.0'}),
                 lambda c: c['config']['Labels'].update({'org.opencontainers.image.revision': '0' * 40}),
                 lambda c: c['config']['Labels'].update({'org.opencontainers.image.source': 'https://example.invalid'})]
        for edit in edits:
            try:
                self.reports[name] = self.image_report('amd64', image_id, config_edit=edit)
                self.save()
                with self.assertRaisesRegex(ValueError, 'image-repro-amd64.json.*runtime configuration|image-repro-amd64.json.*labels'):
                    publish.evidence(self.out, self.manifest)
            finally:
                self.reports[name] = self.image_report('amd64', image_id)
                self.save()

    def test_saved_runtime_image_cannot_be_replaced_with_the_other_architecture(self):
        name = 'image-repro-amd64.json'
        record = self.reports[name]['reference_image']['archive']
        path = self.out / record['path']
        original = path.read_bytes()
        try:
            other = self.reports['image-repro-arm64.json']['reference_image']['archive']
            path.write_bytes((self.out / other['path']).read_bytes())
            record.update(self.transport_record(path))
            self.save()
            with self.assertRaisesRegex(ValueError, 'image-repro-amd64.json.*platform mismatch'):
                publish.evidence(self.out, self.manifest)
        finally:
            path.write_bytes(original)
            record.update(self.transport_record(path))
            self.save()

    def test_source_rebuild_requires_complete_inventories_matching_final_bytes(self):
        name = 'reproducibility.json'
        self.reject(name, lambda report: report.update(unsigned_payloads=[{}, {}]))
        for payload in self.payloads:
            with self.subTest(payload=payload):
                self.reject(name, lambda report: report['unsigned_payloads'][1].pop(payload))
                self.reject(name, lambda report: report['unsigned_payloads'][0].update({payload: '0' * 64}))
        self.reject(name, lambda report: report.update(source_sha256='0' * 64))
        self.reject(name, lambda report: report.update(recipe_sha256='0' * 64))
        self.reject(name, lambda report: report.update(packages_compared=1))
        self.reject(name, lambda report: report.update(excluded_tools=[]))

    def test_changed_final_distribution_cannot_reuse_matching_reports(self):
        path = self.out / ('cpra-' + self.manifest['version'] + '-compose.tar.gz')
        path.write_bytes(b'changed after qualification')
        with self.assertRaisesRegex(ValueError, 'reproducibility.json'):
            publish.evidence(self.out, self.manifest)

    def test_documentation_requires_its_own_source_commit(self):
        for value in ('', 'a' * 7, None):
            self.reject('documentation.json', lambda report: report.update(docs_commit=value))

    def test_matching_identity_cannot_conceal_contradictory_source_or_version(self):
        for name in self.reports:
            self.reject(name, lambda report: report.update(version='v0.1.1'))
            if name.startswith(('image-repro-', 'container-', 'chart-')):
                changed = copy.deepcopy(self.manifest)
                changed['dashboard_sha256'] = '0' * 64
                self.reject(name, lambda report: report.update(source=changed))

    def test_matching_reports_cannot_select_another_recipe(self):
        changed = copy.deepcopy(self.manifest)
        changed['recipe']['build_flags'].append('-race')
        with self.assertRaisesRegex(ValueError, 'different release recipe'):
            publish.evidence(self.out, changed)

    def test_ambiguous_malformed_and_unbounded_reports_fail_before_outcome_validation(self):
        path = self.out / 'reproducibility.json'
        valid = path.read_text()
        cases = ('[]', 'null', '{', b'\xff', '{"status":"fail",' + valid[1:],
                 '{"duration":NaN,' + valid[1:], '{"duration":Infinity,' + valid[1:],
                 '{"duration":1e999,' + valid[1:], '{"duration":-1e999,' + valid[1:])
        for data in cases:
            with self.subTest(data=str(data)[:40]):
                path.write_bytes(data.encode() if isinstance(data, str) else data)
                with self.assertRaisesRegex(ValueError, 'reproducibility.json'):
                    publish.evidence(self.out, self.manifest)
        path.write_bytes(b' ' * (16 * 1024 * 1024 + 1))
        with self.assertRaisesRegex(ValueError, 'exceeds 16 MiB'):
            read_report(path)


if __name__ == '__main__':
    unittest.main()
