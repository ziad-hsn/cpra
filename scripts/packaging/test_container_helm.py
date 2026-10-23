#!/usr/bin/env python3
"""Render the actual chart and validate deployment contracts; requires Helm + PyYAML."""
import copy
import json
import os
from pathlib import Path
import shlex
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts/packaging'))
import live_compose
import live_helm
CHART = ROOT / 'charts/cpra'
HELM = os.environ.get('HELM_BIN', 'helm')
BASE = {'auth': {'existingSecret': 'cpra-api-v1'},
        'manifest': {'existingConfigMap': 'cpra-monitors-v1'}}
MANAGEMENT = {'enabled': True,
              'policy': {'existingSecret': 'cpra-policy-v1'},
              'encryption': {'existingSecret': 'cpra-key-v1'},
              'tls': {'existingSecret': 'cpra-tls-v1'}}


class ComposeEvidenceContract(unittest.TestCase):
    def test_outage_requires_a_new_incident_for_the_designated_monitor(self):
        previous = {'id': '10:0', 'monitor_id': 'packaging-fixture',
                    'type': 'incident_opened', 'incident_id': 'old-incident'}
        current = {**previous, 'id': '20:0', 'incident_id': 'new-incident'}
        baseline = [previous]
        for events in ([previous], [{**current, 'type': 'action_succeeded'}],
                       [{**current, 'incident_id': 'old-incident'}],
                       [{**current, 'monitor_id': 'unrelated-monitor'}],
                       [{**current, 'incident_id': ''}], [{**current, 'id': ''}]):
            with self.subTest(events=events):
                self.assertIsNone(live_compose.new_incident(events, baseline))
        self.assertEqual(live_compose.new_incident([previous, current], baseline), current)
        self.assertEqual(live_compose.new_incident([current], []), current)

    def test_healthy_baseline_requires_a_completed_successful_check(self):
        monitor = {'monitor_id': 'packaging-fixture', 'status': 'up', 'incident': False,
                   'last_success': '2026-10-15T03:00:00Z'}
        self.assertTrue(live_compose.healthy_monitor(monitor))
        for change in ({'monitor_id': 'unrelated-monitor'}, {'status': 'unknown'},
                       {'incident': True}, {'last_success': '0001-01-01T00:00:00Z'},
                       {'last_success': ''}, {'last_success': 'not-a-time'},
                       {'last_success': '2026-10-15T03:00:00'}):
            with self.subTest(change=change):
                self.assertFalse(live_compose.healthy_monitor({**monitor, **change}))

    def test_pre_freeze_execution_cannot_become_frozen_release_evidence(self):
        import release
        with tempfile.TemporaryDirectory(prefix='cpra-compose-source-') as tmp:
            manifest = release.prepare('v0.1.0-rc.1', Path(tmp), candidate=True)
        for reader, evidence_class in ((live_compose.source_evidence, 'working_tree_container'),
                                       (live_helm.chart_source_evidence, 'working_tree_chart')):
            with self.subTest(evidence_class=evidence_class):
                with self.assertRaisesRegex(ValueError, 'clean, frozen'):
                    reader(manifest)
                report = reader(manifest, diagnostic=True)
                self.assertEqual(report['evidence_class'], evidence_class)
                self.assertIs(report['release_qualified'], False)
                self.assertIs(report['release_gate_passed'], False)
                self.assertNotIn('release_identity', report)
                for change in ({'candidate': False}, {'source_files': {'unapproved': {}}}):
                    with self.subTest(change=change), self.assertRaisesRegex(ValueError, 'candidate metadata'):
                        reader({**manifest, **change}, diagnostic=True)
                with self.assertRaisesRegex(ValueError, 'recipe'):
                    reader({**manifest, 'recipe': {}}, diagnostic=True)


def render(values=None, kube='1.35.0', succeeds=True):
    with tempfile.TemporaryDirectory(prefix='cpra-chart-') as tmp:
        path = Path(tmp) / 'values.yaml'
        path.write_text(yaml.safe_dump(BASE if values is None else values))
        result = subprocess.run([HELM, 'template', 'contract', str(CHART),
                                 '-f', str(path), '--kube-version', kube],
                                text=True, capture_output=True, timeout=30)
    if succeeds and result.returncode:
        raise AssertionError(result.stderr)
    if not succeeds:
        if not result.returncode:
            raise AssertionError('invalid values unexpectedly rendered')
        return result.stderr
    return [x for x in yaml.safe_load_all(result.stdout) if x]


def kind(objects, name):
    return next(x for x in objects if x['kind'] == name)


class ChartContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not shutil.which(HELM):
            raise RuntimeError('Helm is required; chart checks cannot be reported as passing without it')

    def test_notification_log_volume_is_separate_and_private(self):
        for claim in ('', 'cpra-notification-output'):
            objects = render({**BASE, 'notificationLog': {'enabled': True, 'existingClaim': claim}})
            pod = kind(objects, 'StatefulSet')['spec']['template']['spec']
            init = next(x for x in pod['initContainers'] if x['name'] == 'notification-log-directory')
            owner = pod['containers'][0]
            self.assertEqual(init['image'], owner['image'])
            self.assertEqual([m['name'] for m in init['volumeMounts']], ['notification-log'])
            self.assertTrue(init['securityContext']['readOnlyRootFilesystem'])
            mount = next(m for m in owner['volumeMounts'] if m['name'] == 'notification-log')
            self.assertEqual(mount['subPath'], 'notifications')
            self.assertEqual(mount['mountPath'], '/var/log/cpra-notifications')
            volume = next(v for v in pod['volumes'] if v['name'] == 'notification-log')
            self.assertEqual(volume.get('persistentVolumeClaim') if claim else volume.get('emptyDir'),
                             {'claimName': claim} if claim else {'sizeLimit': '64Mi'})
            runtime = yaml.safe_load(kind(objects, 'ConfigMap')['data']['runtime.yaml'])
            self.assertEqual(runtime['notifications']['log_directory'], mount['mountPath'])
            self.assertNotIn('chmod', init['args'][0])
        for claim, changes in (('state-contract-cpra-0', {}), ('state-existing', {'persistence': {'existingClaim': 'state-existing'}}),
                               ('manifest-existing', {'manifest': {'existingConfigMap': '', 'existingClaim': 'manifest-existing'}})):
            values = {**BASE, **changes, 'notificationLog': {'enabled': True, 'existingClaim': claim}}
            self.assertIn('separate', render(values, succeeds=False))
        disabled = kind(render(), 'StatefulSet')['spec']['template']['spec']
        self.assertNotIn('notification-log', [v['name'] for v in disabled['volumes']])

    @unittest.skipUnless(sys.platform.startswith('linux'), 'Linux shell permission fixture; native container evidence is separate')
    def test_notification_directory_initializer_refuses_existing_unsafe_entries(self):
        pod = kind(render({**BASE, 'notificationLog': {'enabled': True}}), 'StatefulSet')['spec']['template']['spec']
        script = next(x for x in pod['initContainers'] if x['name'] == 'notification-log-directory')['args'][0]
        for existing in ('absent', 'private', 'broad', 'symlink', 'file'):
            with self.subTest(existing=existing), tempfile.TemporaryDirectory(prefix='cpra-log-init-') as directory:
                root = Path(directory)
                child, target = root/'notifications', root/'canary'
                target.mkdir(mode=0o700)
                if existing in ('private', 'broad'):
                    child.mkdir(mode=0o700)
                    child.chmod(0o700 if existing == 'private' else 0o770)
                elif existing == 'symlink':
                    child.symlink_to(target, target_is_directory=True)
                elif existing == 'file':
                    child.write_text('unchanged')
                # This verifies the real script at the test user's UID. It is
                # explicitly not evidence of execution as container UID 1001.
                source = script.replace('/work', shlex.quote(directory)).replace('1001:700', f'{os.getuid()}:700')
                result = subprocess.run(['/bin/sh', '-ec', source], capture_output=True, timeout=5)
                self.assertEqual(result.returncode == 0, existing in ('absent', 'private'))
                self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o700)
                if existing == 'broad':
                    self.assertEqual(stat.S_IMODE(child.stat().st_mode), 0o770)
                if existing == 'file':
                    self.assertEqual(child.read_text(), 'unchanged')

    def test_declared_api_versions_and_unsupported_versions(self):
        for version in ('1.32.0', '1.33.0', '1.34.0', '1.35.0', '1.36.0'):
            with self.subTest(version=version):
                self.assertEqual(kind(render(kube=version), 'StatefulSet')['apiVersion'], 'apps/v1')
        for version in ('1.31.0', '1.37.0'):
            with self.subTest(version=version):
                self.assertIn('kubeVersion', render(kube=version, succeeds=False))

    def test_management_bootstrap_keeps_secrets_out_of_runtime_and_raft(self):
        objects = render({**BASE, 'management': MANAGEMENT})
        pod = kind(objects, 'StatefulSet')['spec']['template']['spec']
        init = pod['initContainers'][0]
        owner = pod['containers'][0]
        self.assertEqual(init['image'], owner['image'])
        self.assertEqual(pod['securityContext']['runAsUser'], 1001)
        self.assertFalse(init['securityContext']['allowPrivilegeEscalation'])
        self.assertTrue(init['securityContext']['readOnlyRootFilesystem'])
        self.assertEqual(init['securityContext']['capabilities']['drop'], ['ALL'])
        self.assertNotIn('state', [mount['name'] for mount in init['volumeMounts']])
        self.assertNotIn('management-inputs', [mount['name'] for mount in owner['volumeMounts']])
        mount = next(mount for mount in owner['volumeMounts'] if mount['name'] == 'management-bootstrap')
        self.assertTrue(mount['readOnly'])
        self.assertEqual(mount['subPath'], 'protected')
        self.assertEqual(mount['mountPath'], '/etc/cpra/management')
        volume = next(volume for volume in pod['volumes'] if volume['name'] == 'management-bootstrap')
        self.assertEqual(volume['emptyDir'], {'medium': 'Memory', 'sizeLimit': '16Mi'})
        projection = next(volume for volume in pod['volumes'] if volume['name'] == 'management-inputs')['projected']
        self.assertEqual(projection['defaultMode'], 0o440)
        self.assertEqual({source['secret']['name'] for source in projection['sources']},
                         {'cpra-api-v1', 'cpra-policy-v1', 'cpra-key-v1', 'cpra-tls-v1'})
        runtime = yaml.safe_load(kind(objects, 'ConfigMap')['data']['runtime.yaml'])
        self.assertTrue(runtime['management']['enabled'])
        self.assertEqual(runtime['management']['policy_file'], '/etc/cpra/management/policy.yaml')
        self.assertEqual(runtime['management']['encryption']['local']['active_key_file'],
                         '/etc/cpra/management/wrapping.key')
        self.assertNotIn('reader_group_id', runtime['management']['tls'])
        self.assertNotIn('policy_reader_group_id', runtime['management'])
        self.assertFalse(any(object['kind'] == 'Secret' for object in objects))

    def test_management_probes_and_test_job_use_verified_https(self):
        objects = render({**BASE, 'management': MANAGEMENT})
        owner = kind(objects, 'StatefulSet')['spec']['template']['spec']['containers'][0]
        for field in ('startupProbe', 'readinessProbe', 'livenessProbe'):
            command = owner[field]['exec']['command']
            self.assertEqual(command[:1], ['/usr/local/bin/cpractl'])
            self.assertEqual(command[command.index('--server') + 1], 'https://127.0.0.1:8060')
            self.assertEqual(command[command.index('--ca-file') + 1], '/etc/cpra/management/ca.crt')
            self.assertEqual(command[command.index('--token-file') + 1], '/etc/cpra/management/read-token')
            self.assertFalse(any(argument.startswith('--token-reader-group') for argument in command))
            self.assertNotIn('--allow-insecure-http', command)
        services = [object for object in objects if object['kind'] == 'Service']
        self.assertTrue(all(service['spec']['ports'][0]['name'] == 'https' for service in services))
        job = kind(objects, 'Job')['spec']['template']['spec']
        args = job['containers'][0]['args']
        self.assertEqual(args[0], 'ready')
        self.assertEqual(args[args.index('--server') + 1], 'https://contract-cpra.default.svc:8060')
        self.assertIn('--ca-file', args)
        self.assertNotIn('--allow-insecure-http', args)
        self.assertEqual({volume['name'] for volume in job['volumes']}, {'auth', 'ca'})
        self.assertIn('--token-root=/run/secrets/cpra', args)
        self.assertIn('--token-reader-group=1001', args)
        ca = next(volume for volume in job['volumes'] if volume['name'] == 'ca')['secret']
        self.assertEqual(ca['items'], [{'key': 'ca.crt', 'path': 'ca.crt'}])

    def test_management_requires_complete_references_and_durable_api(self):
        invalid = [
            {**BASE, 'management': {'enabled': True}},
            {**BASE, 'api': {'enabled': False}, 'management': MANAGEMENT},
            {**BASE, 'storage': {'mode': 'memory'}, 'persistence': {'enabled': False}, 'management': MANAGEMENT},
            {**BASE, 'management': {**MANAGEMENT, 'enabled': False}},
            {**BASE, 'management': {**MANAGEMENT, 'token': 'literal-credentials-are-forbidden'}},
            {**BASE, 'management': {**MANAGEMENT, 'tls': {'existingSecret': 'cpra-tls-v1', 'keys': {'privateKey': '../escape'}}}},
            {**BASE, 'management': MANAGEMENT, 'ingress': {'enabled': True, 'host': 'cpra.example', 'tlsSecret': 'ingress-v1'}},
        ]
        for values in invalid:
            with self.subTest(values=values):
                render(values, succeeds=False)

    def test_management_previous_keys_are_bounded_and_keep_original_sources(self):
        previous = [{'existingSecret': 'cpra-key-v0', 'key': 'wrapping.key'}]
        management = {**MANAGEMENT, 'encryption': {**MANAGEMENT['encryption'], 'previous': previous}}
        objects = render({**BASE, 'management': management})
        runtime = yaml.safe_load(kind(objects, 'ConfigMap')['data']['runtime.yaml'])
        self.assertEqual(runtime['management']['encryption']['local']['previous_key_files'],
                         ['/etc/cpra/management/previous/0.key'])
        pod = kind(objects, 'StatefulSet')['spec']['template']['spec']
        projection = next(volume for volume in pod['volumes'] if volume['name'] == 'management-inputs')['projected']
        self.assertIn({'secret': {'name': 'cpra-key-v0', 'items': [{'key': 'wrapping.key', 'path': 'previous/0.key'}]}},
                      projection['sources'])
        for keys in (previous * 2, [{'existingSecret': 'cpra-key-v1', 'key': 'wrapping.key'}],
                     [{'existingSecret': 'cpra-key-' + str(n), 'key': 'wrapping.key'} for n in range(33)]):
            with self.subTest(previous=keys):
                render({**BASE, 'management': {**MANAGEMENT, 'encryption': {**MANAGEMENT['encryption'], 'previous': keys}}}, succeeds=False)

    def test_management_init_copies_projected_sources_privately_and_retries(self):
        objects = render({**BASE, 'management': MANAGEMENT})
        init = kind(objects, 'StatefulSet')['spec']['template']['spec']['initContainers'][0]
        self.assertEqual(init['command'], ['/bin/sh', '-ec'])
        with tempfile.TemporaryDirectory(prefix='cpra management files ') as temporary:
            root = Path(temporary)
            inputs, work = root / 'input', root / 'work'
            inputs.mkdir(); work.mkdir()
            payloads = {'read-token': b'read-only-fixture-token', 'policy.yaml': b'principals: []\n',
                        'wrapping.key': bytes(range(32)), 'tls.crt': b'certificate fixture',
                        'tls.key': b'private-key-fixture', 'ca.crt': b'CA fixture'}
            projected = inputs / '..payload'; projected.mkdir()
            for name, data in payloads.items():
                (projected / name).write_bytes(data)
                (projected / name).chmod(0o440)
                (inputs / name).symlink_to(Path('..payload') / name)
            script = init['args'][0].replace('/input/', shlex.quote(str(inputs)) + '/').replace('/work/', shlex.quote(str(work)) + '/')
            for attempt in range(2):
                if attempt:
                    (work / 'protected').chmod(0o770)
                    for name in payloads:
                        (work / 'protected' / name).chmod(0o660)
                result = subprocess.run(['/bin/sh', '-ec', script], text=True, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertFalse(result.stdout)
                self.assertEqual(stat.S_IMODE((work / 'protected').stat().st_mode), 0o700)
                for name, data in payloads.items():
                    target = work / 'protected' / name
                    self.assertFalse(target.is_symlink())
                    self.assertEqual(target.read_bytes(), data)
                    self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o600)

    def test_single_owner_and_retention(self):
        objs = render()
        spec = kind(objs, 'StatefulSet')['spec']
        self.assertEqual(spec['replicas'], 1)
        self.assertEqual(spec['persistentVolumeClaimRetentionPolicy'], {'whenDeleted': 'Retain', 'whenScaled': 'Retain'})
        self.assertEqual(spec['volumeClaimTemplates'][0]['spec']['accessModes'], ['ReadWriteOncePod'])
        self.assertNotIn('storageClassName', spec['volumeClaimTemplates'][0]['spec'])
        self.assertFalse(any(x['kind'] in ('HorizontalPodAutoscaler', 'PodDisruptionBudget') for x in objs))
        values = copy.deepcopy(BASE)
        values['replicas'] = 2
        render(values, succeeds=False)

    def test_maintenance_stops_owner_and_omits_tests(self):
        values = {**BASE, 'maintenance': True}
        objs = render(values)
        self.assertEqual(kind(objs, 'StatefulSet')['spec']['replicas'], 0)
        self.assertFalse(any(x['kind'] == 'Job' for x in objs))

    def test_probes_are_pure_authenticated_client_commands(self):
        spec = kind(render(), 'StatefulSet')['spec']['template']['spec']
        c = spec['containers'][0]
        for field, subcommand in [('startupProbe', 'health'), ('livenessProbe', 'health'), ('readinessProbe', 'ready')]:
            command = c[field]['exec']['command']
            self.assertEqual(command[:2], ['/usr/local/bin/cpractl', subcommand])
            self.assertIn('--token-file', command)
            self.assertIn('--token-root=/run/secrets/cpra', command)
            self.assertIn('--token-reader-group=1001', command)
            self.assertIn('--allow-insecure-http', command)
            self.assertEqual(command[command.index('--server') + 1], 'http://127.0.0.1:8060')
            self.assertNotIn('httpGet', c[field])
        self.assertIn('-web.auth-root=/run/secrets/cpra', c['args'])
        self.assertIn('-web.auth-reader-group=1001', c['args'])
        self.assertEqual(spec['terminationGracePeriodSeconds'], 60)
        job = kind(render(), 'Job')['spec']['template']['spec']
        self.assertEqual(job['containers'][0]['command'], ['/usr/local/bin/cpractl'])
        args = job['containers'][0]['args']
        self.assertIn('--allow-insecure-http', args)
        self.assertEqual(args[args.index('--server') + 1], 'http://contract-cpra:8060')
        self.assertEqual([x['name'] for x in job['volumes']], ['auth'])
        self.assertIn('--token-root=/run/secrets/cpra', args)
        self.assertIn('--token-reader-group=1001', args)
        self.assertFalse(job['automountServiceAccountToken'])

    def test_security_and_privileges_are_opt_in(self):
        objs = render()
        spec = kind(objs, 'StatefulSet')['spec']['template']['spec']
        self.assertFalse(spec['automountServiceAccountToken'])
        self.assertEqual(spec['securityContext']['runAsUser'], 1001)
        self.assertEqual(spec['securityContext']['fsGroupChangePolicy'], 'OnRootMismatch')
        self.assertTrue(spec['containers'][0]['securityContext']['readOnlyRootFilesystem'])
        self.assertEqual(spec['containers'][0]['securityContext']['capabilities']['drop'], ['ALL'])
        self.assertFalse(any(x['kind'] in ('Role', 'ClusterRole', 'ClusterRoleBinding') for x in objs))
        values = {**BASE, 'kubernetesRecovery': {'enabled': True, 'restartDeployments': ['designated']}}
        objs = render(values)
        rules = kind(objs, 'Role')['rules']
        self.assertEqual(rules, [{'apiGroups': ['apps'], 'resources': ['deployments'],
                                 'resourceNames': ['designated'], 'verbs': ['patch']}])

    def test_config_sources_and_large_manifest_do_not_embed_contents(self):
        for source in ('existingSecret', 'existingConfigMap', 'existingClaim'):
            values = {**BASE, 'manifest': {source: 'designated-input'}}
            objs = render(values)
            c = kind(objs, 'StatefulSet')['spec']['template']['spec']
            volume = next(x for x in c['volumes'] if x['name'] == 'monitors')
            if source == 'existingClaim':
                self.assertTrue(volume['persistentVolumeClaim']['readOnly'])
            self.assertFalse(any(x['kind'] == 'Secret' for x in objs))
        for manifest in ({}, {'existingSecret': 'one', 'existingConfigMap': 'two'},
                         {'existingClaim': 'one', 'file': '../escape'}):
            with self.subTest(manifest=manifest):
                render({**BASE, 'manifest': manifest}, succeeds=False)

    def test_existing_claim_memory_and_weaker_fence_are_explicit(self):
        values = {**BASE, 'persistence': {'existingClaim': 'saved-state'}}
        spec = kind(render(values), 'StatefulSet')['spec']
        self.assertNotIn('volumeClaimTemplates', spec)
        self.assertTrue(any(x.get('persistentVolumeClaim', {}).get('claimName') == 'saved-state'
                            for x in spec['template']['spec']['volumes']))
        render({**BASE, 'storage': {'mode': 'memory'}}, succeeds=False)
        spec = kind(render({**BASE, 'storage': {'mode': 'memory'}, 'persistence': {'enabled': False}}), 'StatefulSet')['spec']
        self.assertNotIn('volumeClaimTemplates', spec)
        render({**BASE, 'persistence': {'accessMode': 'ReadWriteOnce'}}, succeeds=False)
        render({**BASE, 'persistence': {'accessMode': 'ReadWriteOnce', 'acknowledgeReadWriteOnce': True}})

    def test_auth_ingress_and_digest_validation(self):
        render({'manifest': BASE['manifest']}, succeeds=False)
        render({**BASE, 'ingress': {'enabled': True, 'host': 'monitor.example'}}, succeeds=False)
        render({**BASE, 'ingress': {'path': '/cpra'}}, succeeds=False)
        digest = 'sha256:' + 'a' * 64
        objects = render({**BASE, 'image': {'digest': digest},
                          'ingress': {'enabled': True, 'host': 'monitor.example', 'tlsSecret': 'tls'}})
        self.assertTrue(kind(objects, 'StatefulSet')['spec']['template']['spec']['containers'][0]['image'].endswith('@'+digest))
        self.assertEqual(kind(objects, 'Ingress')['spec']['rules'][0]['http']['paths'][0]['path'], '/')

    def test_provider_credentials_are_secret_references(self):
        env = {'name': 'AWS_SHARED_CREDENTIALS_FILE', 'secret': 'provider-v1', 'key': 'path'}
        values = {**BASE, 'providerFiles': {'existingSecret': 'provider-v1'}, 'providerEnv': [env]}
        pod = kind(render(values), 'StatefulSet')['spec']['template']['spec']
        self.assertEqual(pod['containers'][0]['env'][1], {'name': env['name'],
                         'valueFrom': {'secretKeyRef': {'name': 'provider-v1', 'key': 'path'}}})
        volume = next(x for x in pod['volumes'] if x['name'] == 'provider')
        self.assertEqual(volume['secret']['defaultMode'], 0o440)
        render({**BASE, 'providerEnv': [env, env]}, succeeds=False)
        render({**BASE, 'providerEnv': [{**env, 'value': 'forbidden-literal-secret'}]}, succeeds=False)
        render({**BASE, 'providerEnv': [{**env, 'name': 'GOMEMLIMIT'}]}, succeeds=False)

    def test_api_disabled_omits_network_and_probe_resources(self):
        values = {'api': {'enabled': False}, 'manifest': BASE['manifest']}
        objects = render(values)
        self.assertFalse(any(x['kind'] in ('Service', 'Ingress', 'Job') for x in objects))
        pod = kind(objects, 'StatefulSet')['spec']['template']['spec']
        container = pod['containers'][0]
        self.assertIn('-web=false', container['args'])
        self.assertFalse(any('Probe' in key for key in container))
        self.assertNotIn('ports', container)
        self.assertNotIn('auth', [volume['name'] for volume in pod['volumes']])
        render({**values, 'ingress': {'enabled': True, 'host': 'cpra.example', 'tlsSecret': 'tls'}}, succeeds=False)
        render({**BASE, 'fullnameOverride': 'a'*49}, succeeds=False)
        render({**BASE, 'fullnameOverride': 'invalid.service.name'}, succeeds=False)


class ComposeContract(unittest.TestCase):
    def test_actual_compose_interpolation_and_runtime_contract(self):
        if not shutil.which('docker'):
            raise RuntimeError('Docker Compose is required for this check')
        with tempfile.TemporaryDirectory(prefix='cpra-compose-') as tmp:
            token = Path(tmp) / 'api-token'
            token.write_text('fixture-secret-must-not-appear-in-config')
            monitors = Path(tmp) / 'monitors.yaml'
            monitors.write_text('monitors: []\n')
            runtime = Path(tmp) / 'runtime.yaml'
            runtime.write_text('storage: {mode: raft, directory: /var/lib/cpra}\n')
            env = {**os.environ, 'CPRA_IMAGE': 'cpra:fixture', 'CPRA_MANIFEST_FILE': str(monitors),
                   'CPRA_TOKEN_FILE': str(token), 'CPRA_RUNTIME_FILE': str(runtime),
                   'CPRA_DATA_VOLUME': 'cpra-contract-data'}
            result = subprocess.run(['docker', 'compose', '-f', str(ROOT/'docker/docker-compose.yml'),
                                     'config', '--format', 'json'], env=env, text=True,
                                    capture_output=True, check=True, timeout=30)
            self.assertNotIn(token.read_text(), result.stdout)
            config = json.loads(result.stdout)
            service = config['services']['cpra']
            self.assertTrue(service['read_only'])
            self.assertEqual(service['ports'][0]['host_ip'], '127.0.0.1')
            self.assertEqual(config['volumes']['cpra-data']['name'], 'cpra-contract-data')
            mount = next(x for x in service['volumes'] if x['target'] == '/etc/cpra/runtime.yaml')
            self.assertEqual(mount['source'], str(runtime))
            self.assertTrue(mount['read_only'])
            self.assertFalse(mount['bind'].get('create_host_path', False))
            self.assertEqual(service['logging']['options']['max-file'], '3')
            self.assertIn('ready', service['healthcheck']['test'])
            self.assertIn('--allow-insecure-http', service['healthcheck']['test'])


if __name__ == '__main__':
    unittest.main()
