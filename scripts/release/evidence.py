"""Validate recorded packaging outcomes against the candidate's actual payloads."""
import hashlib
import json
import math
import re
import tarfile

from release import RECIPE, package_version, sha


SERVICE_STEPS = {
    'clean supervisor environment and source identity',
    'fresh installation remains stopped',
    'native supervisor start and authenticated readiness',
    'running update preserves configuration, state and startup mode',
    'deliberate stop remains stopped',
    'stopped update preserves disabled state and edited configuration',
    'uninstall removes owned integration and preserves complete state/configuration',
    'reinstall and stopped backup restore recover under the service identity',
}
PACKAGE_SCENARIOS = {
    'fresh-stopped', 'edited-config', 'start-ready', 'running-revision-upgrade',
    'different-version-stopped-upgrade', 'semver-a.1-to-a-1',
    'prerelease-to-final-running-upgrade', 'disabled-stopped-revision-upgrade',
    'remove', 'reinstall',
}
PACKAGE_PHASES = ('fresh_install', 'initial_startup', 'running_revision_upgrade',
                  'prerelease_final_upgrade', 'reinstall')
COMPOSE_CHECKS = {
    'exact_release_binary_hashes_and_hardened_runtime',
    'real_http_outage_records_incident_without_liveness_failure',
    'second_owner_rejected', 'sigterm_drains_within_supervisor_budget',
    'restart_recovers_identity_and_history', 'versioned_token_file_rotation',
    'down_up_retains_named_volume',
}
HELM_CHECKS = {
    'install_readiness_nonroot_readonly',
    'delayed_manifest_mount_starts_without_restart_churn',
    'rwop_blocks_contending_owner', 'probes_and_helm_test_cause_zero_provider_calls',
    'target_outage_retains_events_and_liveness',
    'restart_restores_node_identity_and_history', 'immutable_secret_rotation_rollout',
    'failed_upgrade_then_explicit_recovery_preserves_state',
    'large_file_backed_manifest_survives_rollout', 'offline_whole_state_copy_includes_history',
    'uninstall_retains_original_pvc', 'restore_existing_claim_recovers_history_and_identity',
    'headless_mode_has_no_api_service_or_probes',
    'uninstall_preserves_restored_and_original_claims',
}
HELM_MANAGEMENT_CHECKS = {
    'managed_secret_sources_and_verified_tls', 'managed_conditional_writes_and_redaction',
    'managed_https_probes_have_zero_provider_calls',
    'managed_restart_preserves_configuration_and_authority',
    'managed_access_rotation_requires_stopped_owner', 'managed_rwop_fences_second_owner',
    'managed_stopped_copy_restore_preserves_encrypted_history',
}


def require(condition, name, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent packaging evidence: ' + name + ': ' + detail)


def text(value):
    return isinstance(value, str) and bool(value.strip()) and value.strip().lower() not in ('unknown', 'not_verified')


def digest(value):
    return isinstance(value, str) and re.fullmatch('[a-f0-9]{64}', value) is not None


def integer(value, minimum=0):
    return type(value) is int and value >= minimum


def read_report(path):
    limit = 16 * 1024 * 1024
    with path.open('rb') as stream:
        data = stream.read(limit + 1)
    require(len(data) <= limit, path.name, 'report exceeds 16 MiB')
    return decode_report(data, path.name)


def decode_report(data, name):
    """Decode one unambiguous JSON object, including embedded release metadata."""
    report = decode_json(data, name)
    require(isinstance(report, dict), name, 'report must be an object')
    return report


def decode_json(data, name):
    """Decode bounded callers' JSON with unique fields and finite numbers."""

    def unique_fields(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, name, 'duplicate JSON field')
            result[key] = value
        return result

    def invalid_constant(_):
        require(False, name, 'non-finite JSON number')

    def finite_float(value):
        number = float(value)
        require(math.isfinite(number), name, 'non-finite JSON number')
        return number

    try:
        report = json.loads(data, object_pairs_hook=unique_fields,
                            parse_constant=invalid_constant, parse_float=finite_float)
    except (UnicodeError, json.JSONDecodeError) as error:
        raise ValueError('Invalid packaging evidence JSON: ' + name) from error
    return report


def outcomes(report, field, required, name, repeated=()):
    values = report.get(field)
    require(isinstance(values, list) and 0 < len(values) <= 128, name, 'missing ' + field)
    cases = {}
    for item in values:
        require(isinstance(item, dict) and text(item.get('name')), name, 'invalid scenario record')
        case = item['name']
        require(item.get('status') == 'pass', name, 'scenario did not pass')
        require(case not in cases or case in repeated, name, 'duplicate scenario')
        cases[case] = item
    require(required <= cases.keys(), name, 'missing required scenario outcomes')
    return cases


def event_ids(value, name):
    require(isinstance(value, list) and 0 < len(value) <= 10000
            and all(text(item) for item in value) and len(set(value)) == len(value),
            name, 'missing or duplicate retained event identities')


def restart_states(report, name):
    states = report.get('restart_states')
    require(isinstance(states, list) and len(states) == 2, name, 'two restart observations are required')
    identities = []
    for state in states:
        storage = state.get('storage') if isinstance(state, dict) else None
        require(isinstance(storage, dict), name, 'restart observation has no storage state')
        require(storage.get('mode') == 'raft' and storage.get('ready') is True
                and storage.get('single_node') is True and not storage.get('error'),
                name, 'restart did not recover usable single-node Raft storage')
        require(text(storage.get('node_id')) and integer(storage.get('committed_index')),
                name, 'restart lacks node identity or commit position')
        identities.append(storage['node_id'])
    require(identities[0] == identities[1], name, 'restart changed node identity')
    require(states[1]['storage']['committed_index'] >= states[0]['storage']['committed_index'],
            name, 'restart lost committed state')


def native_service(report, name):
    permission = ('service ACLs and LocalService identity' if report['os'] == 'Windows'
                  else 'native process identity and filesystem ownership')
    steps = outcomes(report, 'steps', SERVICE_STEPS | {permission}, name, repeated={permission})
    scope = 'user' if name.startswith('service-user-') else 'system'
    require(report.get('scope') == 'native ' + scope + ' service lifecycle', name, 'wrong service scope')
    permission_steps = [step for step in report['steps'] if step['name'] == permission]
    require(len(permission_steps) == 3 and all(text(step.get('phase')) for step in permission_steps)
            and {step['phase'] for step in permission_steps} == {'initial_startup', 'running_update', 'restored_startup'},
            name, 'permissions must be observed at startup, running upgrade and restoration')
    require(text(report.get('filesystem')) and text(report.get('supervisor_version')),
            name, 'missing filesystem or supervisor identity')
    encryption = report.get('encryption')
    require(isinstance(encryption, dict), name, 'missing local key initialization and inspection')
    key_id = encryption.get('key_id')
    require(isinstance(key_id, str) and key_id.startswith('local-sha256:')
            and digest(key_id.removeprefix('local-sha256:'))
            and encryption.get('inspection_matches') is True,
            name, 'initialized local key identity was not confirmed by inspection')
    require(type(report.get('provider_operations')) is int and report['provider_operations'] == 0
            and type(report.get('manifest_monitor_count')) is int and report['manifest_monitor_count'] == 0,
            name, 'empty service fixture or provider-operation boundary is missing')
    initial = steps['native supervisor start and authenticated readiness'].get('node_id')
    restored = steps['reinstall and stopped backup restore recover under the service identity'].get('node_id')
    require(text(initial) and initial == restored, name, 'restored node identity is missing or different')
    observed = steps['deliberate stop remains stopped'].get('observed_seconds')
    require(integer(observed, 21), name, 'deliberate stop observation is incomplete')
    access = report.get('authentication')
    require(isinstance(access, dict), name, 'missing restored authentication observations')
    for field in ('initial_epoch', 'initial_revision', 'reset_epoch', 'reset_revision',
                  'restored_epoch', 'restored_revision', 'initial_principal', 'restored_principal'):
        require(text(access.get(field)), name, 'missing authentication identity: ' + field)
    require(access['initial_epoch'] != access['reset_epoch'] == access['restored_epoch']
            and len({access['initial_revision'], access['reset_revision'], access['restored_revision']}) == 3,
            name, 'restore did not replace authentication authority')
    require(access.get('reset_required') is True and access.get('tls_verified') is True
            and digest(access.get('certificate_sha256')), name, 'restore reset or TLS verification is missing')
    require(access['initial_principal'] != access['restored_principal'] == access.get('observed_principal'),
            name, 'fresh operator was not observed after restoration')
    for field in ('old_named_status', 'startup_token_status'):
        require(type(access.get(field)) is int and access[field] == 401,
                name, 'obsolete credentials were not rejected after restoration')
    for step in report['steps']:
        if step['name'] != permission:
            continue
        if report['os'] == 'Windows':
            acl = step.get('acl')
            require(isinstance(acl, dict) and text(acl.get('service_sid')), name, 'missing service ACL observation')
            for path in ('binary', 'config', 'state'):
                entry = acl.get(path)
                require(isinstance(entry, dict) and text(entry.get('owner')) and entry.get('protected') is True
                        and isinstance(entry.get('rules'), list) and len(entry['rules']) > 0,
                        name, 'missing protected service ACL')
                for rule in entry['rules']:
                    require(isinstance(rule, dict) and text(rule.get('sid')) and integer(rule.get('rights'))
                            and type(rule.get('allow')) is bool, name, 'invalid service ACL rule')
        else:
            uid, gid, owner = step.get('service_uid'), step.get('service_gid'), step.get('binary_uid')
            require(integer(uid) and integer(gid) and integer(owner), name, 'missing process or executable owner')
            require((scope == 'system' and uid != 0 and owner == 0) or (scope == 'user' and uid == owner),
                    name, 'service and executable ownership disagree with scope')
            system = scope == 'system'
            executable_gid = 0 if system else gid
            expected = {
                'binary_directory': (owner, executable_gid, 0o755 if system else 0o700, 'directory'),
                'binary': (owner, executable_gid, 0o755 if system else 0o700, 'regular'),
                'config_directory': (owner, gid, 0o750 if system else 0o700, 'directory'),
                'monitors': (owner, gid, 0o640 if system else 0o600, 'regular'),
                'runtime': (owner, gid, 0o640 if system else 0o600, 'regular'),
                'api_token': (owner, gid, 0o640 if system else 0o600, 'regular'),
                'state_directory': (uid, gid, 0o700, 'directory'),
                'service_definition': (owner, executable_gid, 0o644 if system else 0o600, 'regular'),
            }
            filesystem = step.get('filesystem')
            require(isinstance(filesystem, dict) and filesystem.keys() == expected.keys(),
                    name, 'missing managed filesystem permission observations')
            for path, (file_uid, file_gid, mode, kind) in expected.items():
                observed = filesystem[path]
                require(isinstance(observed, dict)
                        and all(integer(observed.get(field)) for field in ('uid', 'gid', 'mode'))
                        and observed == {'uid': file_uid, 'gid': file_gid, 'mode': mode, 'type': kind},
                        name, 'managed filesystem permissions differ from the native service contract: ' + path)


def package_lifecycle(report, name, manifest, programs):
    require(report.get('os') == 'Linux'
            and report.get('architecture') in {'amd64': ('x86_64', 'AMD64'),
                                               'arm64': ('aarch64', 'arm64', 'ARM64')}[report['arch']],
            name, 'package execution lacks the matching native platform')
    final = 'purge-preserved-config' if report['format'] == 'deb' else 'erase-rpmsave'
    scenarios = report.get('scenarios')
    require(isinstance(scenarios, list) and all(text(item) for item in scenarios)
            and len(set(scenarios)) == len(scenarios)
            and set(scenarios) == PACKAGE_SCENARIOS | {final}, name, 'package lifecycle scenarios are incomplete')
    versions = report.get('fixture_versions')
    require(isinstance(versions, list) and len(versions) == 5, name, 'package ordering fixtures are incomplete')
    names = []
    for fixture in versions:
        require(isinstance(fixture, dict) and text(fixture.get('name')) and text(fixture.get('version'))
                and isinstance(fixture.get('revision'), str) and fixture['revision'].isdigit(),
                name, 'invalid package version fixture')
        names.append(fixture['name'])
    require(set(names) == {'revision', 'early', 'late', 'final', 'final-revision'}, name, 'wrong version fixtures')
    major, minor, patch = manifest['version'].removeprefix('v').split('-', 1)[0].split('.')
    next_version = 'v' + major + '.' + minor + '.' + str(int(patch) + 1)
    expected_versions = {
        'revision': (package_version(manifest['version']), str(int(manifest['package_revision']) + 1)),
        'early': (package_version(next_version + '-a.1'), '1'),
        'late': (package_version(next_version + '-a-1'), '1'),
        'final': (package_version(next_version), '1'),
        'final-revision': (package_version(next_version), '2'),
    }
    require({fixture['name']: (fixture['version'], fixture['revision']) for fixture in versions} == expected_versions,
            name, 'package version ordering fixtures differ from the candidate')
    require(report.get('execution_environment') in ('native-host', 'linux-systemd-container'),
            name, 'package manager was not exercised in a supported environment')
    require(all(text(report.get(field)) for field in ('filesystem', 'systemd', 'package_manager', 'kernel', 'os_release')),
            name, 'missing package execution environment')
    expected = programs['linux_' + report['arch']]
    require(report.get('binary_sha256') == expected['cpra'] and report.get('cli_sha256') == expected['cpractl'],
            name, 'installed executables differ from the native archive')
    require(type(report.get('manifest_monitor_count')) is int and report['manifest_monitor_count'] == 0
            and type(report.get('provider_operations')) is int and report['provider_operations'] == 0,
            name, 'empty package fixture or provider-operation boundary is missing')
    observations = report.get('observations')
    require(isinstance(observations, list) and len(observations) == len(PACKAGE_PHASES)
            and all(isinstance(step, dict) for step in observations)
            and [step.get('phase') for step in observations] == list(PACKAGE_PHASES),
            name, 'installed package observations are incomplete or out of order')
    identities = []
    states = []
    service_identity = None
    for step in observations:
        uid, gid = step.get('service_uid'), step.get('service_gid')
        require(integer(uid, 1) and integer(gid, 1), name, 'package service identity must be non-root')
        if service_identity is None:
            service_identity = (uid, gid)
        require((uid, gid) == service_identity, name, 'package upgrade changed the service identity')
        require(step.get('binary_sha256') == expected['cpra'] and step.get('cli_sha256') == expected['cpractl'],
                name, 'observed installed executables differ from the candidate')
        permissions = {
            'binary_directory': (0, 0, 0o755, 'directory'),
            'binary': (0, 0, 0o755, 'regular'),
            'cli': (0, 0, 0o755, 'regular'),
            'config_directory': (0, gid, 0o750, 'directory'),
            'monitors': (0, gid, 0o640, 'regular'),
            'runtime': (0, gid, 0o640, 'regular'),
            'api_token': (0, gid, 0o640, 'regular'),
            'state_directory': (uid, gid, 0o700, 'directory'),
            'service_definition': (0, 0, 0o644, 'regular'),
        }
        filesystem = step.get('filesystem')
        require(isinstance(filesystem, dict) and filesystem.keys() == permissions.keys(),
                name, 'package filesystem observations are missing')
        for path, (owner, group, mode, kind) in permissions.items():
            entry = filesystem[path]
            require(isinstance(entry, dict)
                    and all(integer(entry.get(field)) for field in ('uid', 'gid', 'mode'))
                    and entry == {'uid': owner, 'gid': group, 'mode': mode, 'type': kind},
                    name, 'installed package permissions differ: ' + path)
        if step['phase'] in ('fresh_install', 'reinstall'):
            require('process' not in step and 'state' not in step,
                    name, 'stopped package phase records a running owner')
            continue
        process = step.get('process')
        require(isinstance(process, dict) and integer(process.get('pid'), 1)
                and integer(process.get('start_ticks'), 1)
                and type(process.get('uid')) is int and process['uid'] == uid
                and type(process.get('gid')) is int and process['gid'] == gid
                and process.get('binary_sha256') == expected['cpra'],
                name, 'running package process identity is missing or different')
        identities.append((process['pid'], process['start_ticks']))
        state = step.get('state')
        require(isinstance(state, dict), name, 'package Raft recovery observation is missing')
        states.append(state)
        require(type(step.get('monitor_count')) is int and step['monitor_count'] == 0,
                name, 'running package fixture admitted monitors')
        pools = step.get('pool_tasks')
        require(isinstance(pools, dict) and pools.keys() == {'pulse', 'intervention', 'code'},
                name, 'package fixture lacks worker-pool counters')
        for pool in pools.values():
            require(isinstance(pool, dict) and pool.keys() == {'tasks_submitted', 'tasks_completed'}
                    and all(type(value) is int and value == 0 for value in pool.values()),
                    name, 'package probes or empty fixture dispatched provider work')
    require(len(set(identities)) == 3, name, 'running upgrades did not replace the original process')
    for before, after in zip(states, states[1:]):
        restart_states({'restart_states': [before, after]}, name)


def source_installs(report, name, manifest):
    target = name.removeprefix('go-install-').removesuffix('.json')
    goos, arch = target.split('_')
    require(report.get('os') == {'linux': 'Linux', 'darwin': 'Darwin', 'windows': 'Windows'}[goos]
            and report.get('architecture') in {'amd64': ('x86_64', 'AMD64'), 'arm64': ('aarch64', 'arm64', 'ARM64')}[arch],
            name, 'source installations lack the matching native platform')
    installations = report.get('installations')
    require(isinstance(installations, list) and len(installations) == 4, name, 'all source installations are required')
    expected = {(package + '@' + manifest['version'], tags)
                for package in (RECIPE['module'], RECIPE['module'] + '/cmd/cpractl')
                for tags in ('', ' '.join(RECIPE['build_tags']))}
    observed = set()
    for installation in installations:
        require(isinstance(installation, dict) and isinstance(installation.get('command'), str)
                and isinstance(installation.get('tags'), str) and text(installation.get('identity'))
                and re.search(r'(?<![A-Za-z0-9.+-])' + re.escape(manifest['version']) + r'(?![A-Za-z0-9.+-])', installation['identity'])
                and digest(installation.get('sha256')),
                name, 'source installation lacks executable/version evidence')
        observed.add((installation['command'], installation['tags']))
    require(observed == expected, name, 'source installation matrix is incomplete or duplicated')


def compose(report, name, programs):
    arch = name.removeprefix('container-').removesuffix('.json')
    require(report.get('platform') == 'linux/' + arch and report.get('cleanup') == 'pass',
            name, 'wrong image platform or incomplete cleanup')
    require(all(text(report.get(field)) for field in ('state_filesystem', 'kernel', 'compose_version', 'docker_server_version')),
            name, 'missing container execution environment')
    checks = outcomes(report, 'checks', COMPOSE_CHECKS, name)
    image = checks['exact_release_binary_hashes_and_hardened_runtime']
    require(image.get('binary_sha256') == programs['linux_' + arch] and text(image.get('image_id')),
            name, 'container executables differ from the native archive')
    event_ids(checks['real_http_outage_records_incident_without_liveness_failure'].get('event_ids'), name)
    rotation = checks['versioned_token_file_rotation']
    require(text(rotation.get('committed_revision')) and type(rotation.get('old_token_status')) is int
            and rotation['old_token_status'] == 401, name, 'committed token rotation or old-token rejection is missing')


def helm(report, name, programs):
    require(report.get('management_enabled') is True, name, 'managed Kubernetes execution is required')
    required = HELM_CHECKS | HELM_MANAGEMENT_CHECKS
    if name == 'chart-helm3-to-4.json':
        required.add('in_place_upgrade_between_helm_versions')
        required.add('managed_upgrade_between_helm_versions')
    checks = outcomes(report, 'checks', required, name)
    rotation = checks['immutable_secret_rotation_rollout']
    require(text(rotation.get('committed_revision')) and rotation.get('stopped_owner') is True
            and type(rotation.get('old_token_status')) is int and rotation['old_token_status'] == 401,
            name, 'stopped token rotation or old-token rejection is missing')
    require(report.get('access_mode') == 'ReadWriteOncePod' and report.get('csi_rwop_fencing') == 'pass'
            and report.get('cleanup') == 'pass', name, 'RWOP or cleanup outcome is incomplete')
    volume = report.get('state_volume')
    spec = volume.get('spec') if isinstance(volume, dict) else None
    csi = spec.get('csi') if isinstance(spec, dict) else None
    driver = csi.get('driver') if isinstance(csi, dict) else None
    require(text(driver) and text(report.get('state_filesystem')), name, 'missing CSI driver or filesystem')
    require(text(checks['rwop_blocks_contending_owner'].get('scheduler_reason')), name, 'no observed RWOP rejection')
    require(integer(checks['delayed_manifest_mount_starts_without_restart_churn'].get('delay_seconds'), 5),
            name, 'startup delay was not exercised')
    version = report.get('helm')
    major = '3' if name == 'chart-helm3-to-4.json' else '4'
    require(isinstance(version, str) and re.match(r'^v' + major + r'\.[0-9]+\.[0-9]+(?:[+\-]|$)', version),
            name, 'wrong Helm version')
    if major == '3':
        upgrade = report.get('upgrade_helm')
        require(isinstance(upgrade, str) and re.match(r'^v4\.[0-9]+\.[0-9]+(?:[+\-]|$)', upgrade),
                name, 'Helm 4 upgrade was not recorded')
        step = checks['in_place_upgrade_between_helm_versions']
        require(step.get('previous') == version and step.get('current') == upgrade,
                name, 'Helm upgrade observations disagree')
    probes = checks['probes_and_helm_test_cause_zero_provider_calls']
    require(integer(probes.get('before'), 1) and type(probes.get('after')) is int
            and probes['before'] == probes['after'], name, 'probe effect counts are missing or changed')
    large = checks['large_file_backed_manifest_survives_rollout']
    require(integer(large.get('file_bytes'), 1024 * 1024 + 1)
            and type(large.get('configured_monitors')) is int and large['configured_monitors'] == 10001
            and type(large.get('active_monitors')) is int and large['active_monitors'] == 1
            and large.get('source') == 'separate read-only PVC' and digest(large.get('sha256')),
            name, 'large file-backed manifest was not exercised')
    event_ids(checks['target_outage_retains_events_and_liveness'].get('event_ids'), name)
    # The existing chart matrix imports the tested amd64 image. Other platforms
    # require their own deployment campaign instead of relabeling this report.
    hashes = report.get('binary_sha256')
    require(isinstance(hashes, str), name, 'missing deployed executable hashes')
    found = re.findall(r'^([a-f0-9]{64})  /usr/local/bin/(cpra|cpractl)$', hashes, re.M)
    require(len(found) == 2 and len({program for _, program in found}) == 2
            and {program: value for value, program in found} == programs['linux_amd64'],
            name, 'chart executables differ from the tested amd64 archive')
    helm_management(report, checks, name)


def helm_management(report, checks, name):
    """Require observed managed behavior; read-only results cannot fill these gates."""
    mounted = checks['managed_secret_sources_and_verified_tls']
    files = ('read-token', 'policy.yaml', 'wrapping.key', 'tls.crt', 'tls.key', 'ca.crt')
    require(type(mounted.get('uid')) is int and mounted['uid'] == 1001
            and mounted.get('directory_mode') == '0700'
            and mounted.get('file_modes') == {file: '0600' for file in files}
            and all(mounted.get(field) is True for field in ('input_mount_absent', 'init_state_mount_absent', 'protected_mount_read_only')),
            name, 'managed Secret projection did not preserve the protected-file boundary')
    require(mounted.get('binary_sha256') == report['binary_sha256'] and text(mounted.get('image_id'))
            and mounted['image_id'] == report.get('image_id'), name, 'managed deployment used different artifacts')
    namespace = report.get('namespace')
    require(text(namespace) and namespace.startswith('cpra-'), name, 'managed namespace is not recorded')
    probes = checks['managed_https_probes_have_zero_provider_calls']
    for scenario in (mounted, probes):
        require(scenario.get('tls_verified') is True
                and scenario.get('service_dns') == 'cpra-managed.' + namespace + '.svc',
                name, 'verified managed Service TLS was not exercised')
    require(integer(probes.get('before'), 1) and type(probes.get('after')) is int
            and probes['before'] == probes['after'], name, 'managed probes produced effects or lack calibrated counts')
    writes = checks['managed_conditional_writes_and_redaction']
    operations = writes.get('operation_ids')
    require(isinstance(operations, list) and len(operations) == 3 and all(text(item) for item in operations)
            and len(set(operations)) == 3 and text(writes.get('monitor_uid')) and text(writes.get('credential_uid'))
            and writes.get('credential_value_available') is False, name, 'managed committed writes or credential redaction are missing')
    expected = {'read_token_write_status': 403, 'reader_write_status': 403, 'wrong_token_status': 401, 'stale_version_status': 412}
    require(all(type(writes.get(field)) is int and writes[field] == code for field, code in expected.items()),
            name, 'managed authorization or conditional-write outcomes disagree')
    restart = checks['managed_restart_preserves_configuration_and_authority']
    require(text(restart.get('previous_pod_uid')) and text(restart.get('current_pod_uid'))
            and restart['previous_pod_uid'] != restart['current_pod_uid'] and digest(restart.get('node_identity_sha256'))
            and type(restart.get('replacement_policy_token_status')) is int and restart['replacement_policy_token_status'] == 401
            and type(restart.get('original_operator_status')) is int and restart['original_operator_status'] == 200,
            name, 'managed restart or authoritative access retention was not observed')
    restored = checks['managed_stopped_copy_restore_preserves_encrypted_history']
    for scenario in (restart, restored):
        require(scenario.get('monitor_uid') == writes['monitor_uid'] and scenario.get('credential_uid') == writes['credential_uid'],
                name, 'managed resource incarnation changed during recovery')
    rotation = checks['managed_access_rotation_requires_stopped_owner']
    revisions = rotation.get('committed_revisions')
    require(rotation.get('stopped_owner') is True and rotation.get('administrative_pod_removed') is True
            and isinstance(revisions, list) and len(revisions) == 2 and all(text(item) for item in revisions)
            and len(set(revisions)) == 2, name, 'managed access rotation lacks stopped ownership and confirmed revisions')
    expected = {'old_read_token_status': 401, 'old_operator_status': 401, 'new_read_token_status': 200, 'new_operator_status': 200}
    require(all(type(rotation.get(field)) is int and rotation[field] == code for field, code in expected.items()),
            name, 'managed rotation did not replace both read and write access')
    fencing = checks['managed_rwop_fences_second_owner']
    require(text(fencing.get('scheduler_reason')) and 'readwriteoncepod' in fencing['scheduler_reason'].lower()
            and text(fencing.get('csi_driver')) and text(fencing.get('state_filesystem'))
            and fencing.get('claim') == 'state-cpra-managed-0', name, 'managed RWOP fencing is missing')
    require(restored.get('stopped_owner') is True
            and restored.get('backup_method') == 'trusted stopped complete-directory clone'
            and restored.get('original_claim') == 'state-cpra-managed-0' and restored.get('restored_claim') == 'cpra-managed-restored'
            and digest(restored.get('inventory_sha256')) and integer(restored.get('inventory_files'), 4)
            and restored['inventory_files'] <= 4096 and restored.get('credential_plaintext_absent') is True
            and restored.get('node_identity_sha256') == restart['node_identity_sha256'],
            name, 'managed stopped encrypted-state copy is incomplete')
    event_ids(restored.get('event_ids'), name)
    event_ids(restored.get('restored_event_ids'), name)
    require(set(restored['event_ids']) <= set(restored['restored_event_ids'])
            and integer(restored.get('target_before'), 1) and type(restored.get('target_after')) is int
            and restored['target_before'] == restored['target_after'], name, 'managed restored history or disabled execution disagrees')
    if name == 'chart-helm3-to-4.json':
        upgrade = checks['managed_upgrade_between_helm_versions']
        require(upgrade.get('previous') == report['helm'] and upgrade.get('current') == report['upgrade_helm']
                and upgrade.get('monitor_uid') == writes['monitor_uid'] and upgrade.get('credential_uid') == writes['credential_uid'],
                name, 'managed Helm client upgrade was not observed against the same state')


def image_reproducibility(out, report, name):
    from oci import docker_config_digest, read_image

    arch = name.removeprefix('image-repro-').removesuffix('.json')
    require(report.get('platform') == 'linux/' + arch, name, 'wrong reproduced image platform')
    reference = report.get('reference_image')
    require(isinstance(reference, dict) and text(reference.get('id'))
            and isinstance(reference.get('config_digest'), str)
            and re.fullmatch(r'sha256:[a-f0-9]{64}', reference['config_digest']), name, 'missing reference image identity')

    def retained(record, filename):
        require(isinstance(record, dict) and record.get('path') == filename
                and digest(record.get('sha256')) and integer(record.get('size'), 1),
                name, 'missing or mismatched retained transport')
        path = out / filename
        require(not path.is_symlink() and path.is_file() and path.stat().st_size == record['size']
                and path.stat().st_size <= 2 * 1024 ** 3 and sha(path) == record['sha256'],
                name, 'retained transport bytes differ from the recorded payload')
        return path

    def inspect(reader, path):
        try:
            return reader(path, report['platform'])
        except (OSError, ValueError, tarfile.TarError, EOFError) as error:
            raise ValueError('Incomplete or inconsistent packaging evidence: ' + name
                             + ': retained transport validation failed: ' + str(error)) from error

    source = report.get('source')
    require(isinstance(source, dict), name, 'missing image source metadata')
    reference_path = retained(reference.get('archive'), name.removesuffix('.json') + '-reference.docker.tar')
    require(inspect(docker_config_digest, reference_path) == reference['config_digest'],
            name, 'reference config differs from the saved runtime-test image')
    builds = report.get('builds')
    require(isinstance(builds, list) and len(builds) == 2 and all(isinstance(build, dict) for build in builds),
            name, 'two independent image builds are required')
    for number, build in enumerate(builds):
        payload = build.get('oci_payload')
        require(isinstance(payload, dict) and {'index.json', 'oci-layout'} <= payload.keys()
                and len(payload) >= 4, name, 'missing OCI payload inventory')
        for path, entry in payload.items():
            require(isinstance(path, str) and isinstance(entry, dict) and digest(entry.get('sha256'))
                    and integer(entry.get('size'), 1), name, 'invalid OCI payload entry')
            require(path in ('index.json', 'oci-layout') or path == 'blobs/sha256/' + entry['sha256'],
                    name, 'OCI blob path does not identify its contents')
        config = reference['config_digest']
        require('blobs/sha256/' + config.removeprefix('sha256:') in payload
                and build.get('config_digests') == [config], name, 'reproduced image does not match the runtime reference')
        expected = hashlib.sha256(json.dumps(payload, sort_keys=True).encode()).hexdigest()
        require(build.get('payload_sha256') == expected and integer(build.get('input_mtime')),
                name, 'invalid image inventory digest or input timestamp')
        path = retained(build.get('archive'), name.removesuffix('.json') + f'-build-{number}.oci.tar')
        image = inspect(read_image, path)
        require(image['oci_payload'] == payload and image['config_digest'] == config
                and image['manifest_digest'] == build.get('manifest_digest')
                and type(build.get('expanded_bytes')) is int
                and image['expanded_bytes'] == build['expanded_bytes'],
                name, 'image graph or payload differs from the retained transport')
        runtime = image['configuration'].get('config')
        require(isinstance(runtime, dict) and runtime.get('User') == '1001:1001'
                and runtime.get('Entrypoint') == ['/usr/local/bin/cpra']
                and runtime.get('WorkingDir') == '/var/lib/cpra', name, 'wrong image runtime configuration')
        labels = runtime.get('Labels')
        require(isinstance(labels, dict)
                and labels.get('org.opencontainers.image.version') == source.get('version')
                and labels.get('org.opencontainers.image.revision') == source.get('commit')
                and labels.get('org.opencontainers.image.source') == 'https://github.com/ziad-hsn/cpra',
                name, 'image labels disagree with the qualified source')
    require(builds[0]['oci_payload'] == builds[1]['oci_payload']
            and builds[0]['input_mtime'] != builds[1]['input_mtime'], name, 'independent image payloads differ or inputs were identical')


def reproducibility(out, report, name, manifest, programs):
    require(report.get('packages_compared') is True, name, 'native packages were not reproduced')
    require(report.get('excluded_tools') == ['git', 'node', 'pnpm'], name, 'source rebuild tool isolation is missing')
    source = out / ('cpra-' + manifest['version'] + '-source.tar.gz')
    require(report.get('source_sha256') == sha(source), name, 'source archive differs from the reproduced input')
    recipe = manifest['source_files'].get('scripts/release/recipe.json', {}).get('sha256')
    require(digest(recipe) and report.get('recipe_sha256') == recipe, name, 'rebuild recipe differs from the recorded source')
    expected = {}
    for target in RECIPE['targets']:
        suffix = '.exe' if target.startswith('windows_') else ''
        for program, value in programs[target].items():
            expected[target + '/' + program + suffix] = value
        archive = 'cpra-' + manifest['version'] + '-' + target.replace('_', '-') + ('.zip' if suffix else '.tar.gz')
        expected[archive] = sha(out / archive)
    for arch in ('amd64', 'arm64'):
        for extension in ('deb', 'rpm'):
            path = 'cpra-' + manifest['version'] + '-linux-' + arch + '.' + extension
            expected[path] = sha(out / path)
    compose_archive = 'cpra-' + manifest['version'] + '-compose.tar.gz'
    expected[compose_archive] = sha(out / compose_archive)
    attempts = report.get('unsigned_payloads')
    require(isinstance(attempts, list) and len(attempts) == 2
            and all(attempt == expected for attempt in attempts), name, 'rebuild inventory is incomplete or differs from final payloads')


def verify_packaging_outcomes(out, manifest, reports, programs):
    for name, report in reports.items():
        require(not any(report.get(key) for key in ('error', 'errors', 'cleanup_errors'))
                and not any(key.startswith('pending_') for key in report), name, 'report retains a failure or pending requirement')
        if name.startswith('native-'):
            restart_states(report, name)
        elif name.startswith('service-'):
            native_service(report, name)
        elif name.startswith('package-'):
            package_lifecycle(report, name, manifest, programs)
        elif name.startswith('go-install-'):
            source_installs(report, name, manifest)
        elif name.startswith('container-'):
            compose(report, name, programs)
        elif name.startswith('chart-'):
            helm(report, name, programs)
        elif name.startswith('image-repro-'):
            image_reproducibility(out, report, name)
        elif name == 'reproducibility.json':
            reproducibility(out, report, name, manifest, programs)
        elif name == 'documentation.json':
            require(isinstance(report.get('docs_commit'), str) and re.fullmatch('[a-f0-9]{40}', report['docs_commit']),
                    name, 'documentation source identity is missing')
        else:
            raise ValueError('Unknown packaging evidence: ' + name)
