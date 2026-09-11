#!/usr/bin/env python3
"""Render the actual chart and validate deployment contracts; requires Helm + PyYAML."""
import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / 'charts/cpra'
HELM = os.environ.get('HELM_BIN', 'helm')
BASE = {'auth': {'existingSecret': 'cpra-api-v1'},
        'manifest': {'existingConfigMap': 'cpra-monitors-v1'}}


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

    def test_declared_api_versions_and_unsupported_versions(self):
        for version in ('1.32.0', '1.33.0', '1.34.0', '1.35.0', '1.36.0'):
            with self.subTest(version=version):
                self.assertEqual(kind(render(kube=version), 'StatefulSet')['apiVersion'], 'apps/v1')
        for version in ('1.31.0', '1.37.0'):
            with self.subTest(version=version):
                self.assertIn('kubeVersion', render(kube=version, succeeds=False))

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
            self.assertNotIn('httpGet', c[field])
        self.assertEqual(spec['terminationGracePeriodSeconds'], 60)
        job = kind(render(), 'Job')['spec']['template']['spec']
        self.assertEqual(job['containers'][0]['command'], ['/usr/local/bin/cpractl'])
        self.assertEqual([x['name'] for x in job['volumes']], ['auth'])
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
            env = {**os.environ, 'CPRA_IMAGE': 'cpra:fixture', 'CPRA_MANIFEST_FILE': str(monitors),
                   'CPRA_TOKEN_FILE': str(token), 'CPRA_DATA_VOLUME': 'cpra-contract-data'}
            result = subprocess.run(['docker', 'compose', '-f', str(ROOT/'docker/docker-compose.yml'),
                                     'config', '--format', 'json'], env=env, text=True,
                                    capture_output=True, check=True, timeout=30)
            self.assertNotIn(token.read_text(), result.stdout)
            config = json.loads(result.stdout)
            service = config['services']['cpra']
            self.assertTrue(service['read_only'])
            self.assertEqual(service['ports'][0]['host_ip'], '127.0.0.1')
            self.assertEqual(config['volumes']['cpra-data']['name'], 'cpra-contract-data')
            self.assertEqual(service['logging']['options']['max-file'], '3')
            self.assertIn('ready', service['healthcheck']['test'])


if __name__ == '__main__':
    unittest.main()
