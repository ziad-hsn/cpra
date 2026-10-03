"""Managed Helm runtime scenarios, invoked only inside live_helm's fresh namespace.

Uses the production image, actual Kubernetes mounts and the public API. Contract
tests may exercise builders and transport; only live execution supplies evidence.
"""
import base64
from contextlib import contextmanager
import copy
import hashlib
import http.client
import json
from pathlib import Path
import re
import secrets
import ssl
import subprocess
import tempfile
import time

import yaml

ROOT = Path(__file__).resolve().parents[2]
PROTECTED_FILES = ('read-token', 'policy.yaml', 'wrapping.key', 'tls.crt', 'tls.key', 'ca.crt')


def response(connection, method, path, token, data=None, headers=None, expected=200):
    """One bounded request; http.client neither follows redirects nor retries writes."""
    body = None if data is None else json.dumps(data, separators=(',', ':')).encode()
    connection.request(method, path, body=body,
                       headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json', **(headers or {})})
    reply = connection.getresponse()
    raw = reply.read((2 << 20) + 1)
    if len(raw) > 2 << 20 or reply.status != expected:
        raise RuntimeError('managed API returned an unexpected status or oversized response')
    try:
        value = json.loads(raw)
    except (ValueError, UnicodeError):
        raise RuntimeError('managed API returned an invalid JSON response') from None
    return value, {name.lower(): value for name, value in reply.getheaders()}


@contextmanager
def tunnel(kubectl, pod, log):
    """Own one loopback-only tunnel and always reap it before owner replacement."""
    with log.open('w+b') as output:
        forward = subprocess.Popen(kubectl + ['port-forward', '--address', '127.0.0.1', 'pod/' + pod, ':8060'],
                                   stdout=output, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 8
            while True:
                raw = log.read_bytes()
                if len(raw) > 4096 or forward.poll() is not None:
                    raise RuntimeError('managed API tunnel failed')
                match = re.search(rb'Forwarding from 127\.0\.0\.1:([0-9]+) -> 8060', raw)
                if match:
                    port = int(match.group(1))
                    if not 0 < port < 65536:
                        raise RuntimeError('managed API tunnel returned an invalid port')
                    yield port
                    return
                if time.monotonic() >= deadline:
                    raise RuntimeError('managed API tunnel startup timed out')
                time.sleep(.05)
        finally:
            if forward.poll() is None:
                forward.terminate()
            try:
                forward.wait(timeout=2)
            except subprocess.TimeoutExpired:
                forward.kill(); forward.wait(timeout=2)


def admin_pod(image, claim):
    """Stopped authentication edits do not load a backend, controller or provider."""
    return {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'cpra-managed-admin'},
            'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False,
                     'securityContext': {'runAsNonRoot': True, 'runAsUser': 1001, 'runAsGroup': 1001,
                                         'fsGroup': 1001, 'seccompProfile': {'type': 'RuntimeDefault'}},
                     'containers': [{'name': 'admin', 'image': image, 'imagePullPolicy': 'Never',
                                     'command': ['/bin/sh', '-ec', 'umask 077; mkdir /tmp/cpra-tokens; exec sleep 3600'],
                                     'securityContext': {'readOnlyRootFilesystem': True, 'allowPrivilegeEscalation': False,
                                                         'capabilities': {'drop': ['ALL']}},
                                     'volumeMounts': [{'name': 'state', 'mountPath': '/var/lib/cpra'},
                                                      {'name': 'tmp', 'mountPath': '/tmp'}]}],
                     'volumes': [{'name': 'state', 'persistentVolumeClaim': {'claimName': claim}},
                                 {'name': 'tmp', 'emptyDir': {'medium': 'Memory', 'sizeLimit': '16Mi'}}]}}


class ManagedChart:
    def __init__(self, args, kubectl, runner, recorder, target_count, redactions, image_values, binary_sha256):
        self.args, self.k, self.run, self.record = args, kubectl, runner, recorder
        self.target_count, self.redactions = target_count, redactions
        self.expected_binaries = binary_sha256
        self.values = {'fullnameOverride': 'cpra-managed', 'image': copy.deepcopy(image_values), 'allowEmpty': True,
                       'auth': {'existingSecret': 'cpra-managed-read-v1'},
                       'manifest': {'existingConfigMap': 'cpra-managed-manifest'},
                       'management': {'enabled': True, 'policy': {'existingSecret': 'cpra-managed-policy-v1'},
                                      'encryption': {'existingSecret': 'cpra-managed-key-v1'},
                                      'tls': {'existingSecret': 'cpra-managed-tls-v1'}},
                       'persistence': {'accessMode': args.access_mode, 'acknowledgeReadWriteOnce': args.access_mode == 'ReadWriteOnce',
                                       'size': '256Mi'}}
        if args.storage_class is not None:
            self.values['persistence']['storageClass'] = args.storage_class
        self.h = [args.helm, '--kubeconfig', args.kubeconfig, '--kube-context', args.context, '-n', args.namespace]
        self.read_token, self.operator, self.reader = [secrets.token_urlsafe(40) for _ in range(3)]
        self.credential, self.wrapping_key = secrets.token_urlsafe(48), secrets.token_bytes(32)
        self.redactions += [self.read_token, self.operator, self.reader, self.credential, base64.b64encode(self.wrapping_key).decode()]

    def apply(self, resource):
        self.run(self.k + ['apply', '-f', '-'], json.dumps(resource))

    def secret(self, name, contents):
        self.apply({'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': name}, 'immutable': True,
                    'type': 'Opaque', 'data': {key: base64.b64encode(value).decode() for key, value in contents.items()}})

    def until(self, action, description, seconds=120):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            try:
                result = action()
                if result:
                    return result
            except (RuntimeError, ValueError, KeyError, OSError, http.client.HTTPException):
                pass
            time.sleep(.2)
        raise RuntimeError('timeout waiting for managed chart ' + description)

    def ctl(self, *args, check=True):
        return self.run(self.k + ['exec', 'cpra-managed-0', '--', '/usr/local/bin/cpractl', *args,
                                  '--server', 'https://127.0.0.1:8060', '--ca-file', '/etc/cpra/management/ca.crt',
                                  '--token-file', '/etc/cpra/management/read-token', '--request-timeout', '2s'], check=check, timeout=10)

    def deploy(self, install=False):
        self.value_file.write_text(yaml.safe_dump(self.values))
        self.run(self.h + ['install' if install else 'upgrade', 'cpra-management', str(ROOT / 'charts/cpra'),
                          '-f', str(self.value_file), '--wait', '--timeout', '4m'], timeout=270)
        self.run(self.k + ['rollout', 'status', 'statefulset/cpra-managed', '--timeout=180s'])
        self.until(lambda: self.ctl('ready', check=False).returncode == 0, 'authenticated readiness')

    def maintenance(self):
        self.values['maintenance'] = True
        self.value_file.write_text(yaml.safe_dump(self.values))
        self.run(self.h + ['upgrade', 'cpra-management', str(ROOT / 'charts/cpra'), '-f', str(self.value_file), '--wait', '--timeout', '4m'], timeout=270)
        self.run(self.k + ['wait', '--for=delete', 'pod/cpra-managed-0', '--timeout=90s'])

    def pod(self):
        return json.loads(self.run(self.k + ['get', 'pod', 'cpra-managed-0', '-o', 'json']).stdout)

    def identity(self):
        return json.loads(self.run(self.k + ['exec', 'cpra-managed-0', '--', 'cat', '/var/lib/cpra/identity.json']).stdout)

    def api(self, method, path, token=None, data=None, headers=None, expected=200):
        with tunnel(self.k, 'cpra-managed-0', self.work / 'forward.log') as port:
            connection = http.client.HTTPSConnection('127.0.0.1', port, timeout=2,
                                                     context=ssl.create_default_context(cafile=str(self.work / 'ca.crt')))
            try:
                return response(connection, method, path, token or self.operator, data, headers, expected)
            finally:
                connection.close()

    def wait_operation(self, headers):
        operation = headers.get('x-operation-id')
        if not operation or headers.get('x-cpra-admission') != 'committed':
            raise RuntimeError('managed mutation lacks a committed operation receipt')
        result = self.until(lambda: (lambda item: item if item.get('state') == 'completed' else None)
                            (self.api('GET', '/api/v2/operations/' + operation)[0]), 'operation activation')
        if result.get('applied') != 1:
            raise RuntimeError('managed mutation did not apply exactly once')
        return operation

    def patch_monitor(self, resource, patch):
        result, headers = self.api('PATCH', '/api/v2/monitors/managed-check', data=patch,
                                   headers={'Content-Type': 'application/merge-patch+json',
                                            'If-Match': '"' + resource['metadata']['resourceVersion'] + '"'})
        self.wait_operation(headers)
        return result

    def history(self):
        events = self.api('GET', '/api/v2/history?monitorID=managed-check&limit=100')[0]['items']
        if not isinstance(events, list) or any(item.get('monitorID') != 'managed-check' for item in events):
            raise RuntimeError('managed history returned an unexpected monitor')
        return events

    def resources(self):
        monitor = self.api('GET', '/api/v2/monitors/managed-check')[0]
        credential = self.api('GET', '/api/v2/credentials/shared-secret')[0]
        if credential['spec'].get('value') is not None:
            raise RuntimeError('managed API returned a plaintext credential')
        return monitor, credential

    def policy(self, operator):
        return yaml.safe_dump({'principals': [
            {'id': 'team/oncall', 'role': 'operator', 'token_sha256': hashlib.sha256(operator.encode()).hexdigest()},
            {'id': 'team/observer', 'role': 'reader', 'token_sha256': hashlib.sha256(self.reader.encode()).hexdigest()},
        ]}).encode()

    def provision(self):
        """Fresh fixture secrets only; never generate a key during pod restart."""
        ca, key = self.work / 'ca.crt', self.work / 'ca.key'
        cert, private = self.work / 'server.crt', self.work / 'server.key'
        csr, extensions = self.work / 'server.csr', self.work / 'extensions'
        extensions.write_text('basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\n'
                              'extendedKeyUsage=serverAuth\n'
                              'subjectAltName=IP:127.0.0.1,DNS:cpra-managed.' + self.args.namespace + '.svc\n')
        self.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
                  '-subj', '/CN=CPRa chart fixture CA', '-keyout', str(key), '-out', str(ca)], timeout=30)
        self.run(['openssl', 'req', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=CPRa chart fixture',
                  '-keyout', str(private), '-out', str(csr)], timeout=30)
        self.run(['openssl', 'x509', '-req', '-in', str(csr), '-CA', str(ca), '-CAkey', str(key),
                  '-CAcreateserial', '-days', '2', '-extfile', str(extensions), '-out', str(cert)], timeout=30)
        for path in self.work.iterdir():
            if path.is_file():
                path.chmod(0o600)
        self.redactions.extend((key.read_text(), private.read_text()))
        self.secret('cpra-managed-read-v1', {'token': self.read_token.encode()})
        self.secret('cpra-managed-policy-v1', {'policy.yaml': self.policy(self.operator)})
        self.secret('cpra-managed-key-v1', {'wrapping.key': self.wrapping_key})
        self.secret('cpra-managed-tls-v1', {'tls.crt': cert.read_bytes(), 'tls.key': private.read_bytes(), 'ca.crt': ca.read_bytes()})
        self.apply({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'cpra-managed-manifest'},
                    'immutable': True, 'data': {'monitors.yaml': 'monitors: []\n'}})

    def verify_mounts(self):
        pod = self.pod()
        main = next(container for container in pod['spec']['containers'] if container['name'] == 'cpra')
        init = next(container for container in pod['spec']['initContainers'] if container['name'] == 'management-bootstrap')
        mount = next(item for item in main['volumeMounts'] if item['name'] == 'management-bootstrap')
        if mount.get('subPath') != 'protected' or mount.get('readOnly') is not True \
                or any(item['name'] == 'management-inputs' for item in main['volumeMounts']) \
                or any(item['name'] == 'state' for item in init['volumeMounts']):
            raise RuntimeError('managed pod does not isolate bootstrap inputs from state and the main process')
        statuses = pod['status'].get('initContainerStatuses', [])
        if not any(item['name'] == 'management-bootstrap'
                   and item.get('state', {}).get('terminated', {}).get('exitCode') == 0 for item in statuses):
            raise RuntimeError('managed Secret bootstrap did not complete')
        uid = self.run(self.k + ['exec', 'cpra-managed-0', '--', 'id', '-u']).stdout.strip()
        paths = ['/etc/cpra/management'] + ['/etc/cpra/management/' + name for name in PROTECTED_FILES]
        stat = self.run(self.k + ['exec', 'cpra-managed-0', '--', 'stat', '-c', '%u %g %a', *paths]).stdout.splitlines()
        expected = ['1001 1001 700'] + ['1001 1001 600'] * len(PROTECTED_FILES)
        if uid != '1001' or stat != expected:
            raise RuntimeError('managed files do not have protected ownership and permissions')
        touch = self.run(self.k + ['exec', 'cpra-managed-0', '--', 'touch',
                                 '/etc/cpra/management/fixture-write-test'], check=False)
        if touch.returncode == 0:
            raise RuntimeError('managed files are writable from the main process')
        hashes = self.run(self.k + ['exec', 'cpra-managed-0', '--', 'sha256sum',
                                   '/usr/local/bin/cpra', '/usr/local/bin/cpractl']).stdout.strip()
        if hashes != self.expected_binaries:
            raise RuntimeError('managed executables differ from the read-only deployment')
        return {'uid': 1001, 'directory_mode': '0700', 'file_modes': {name: '0600' for name in PROTECTED_FILES},
                'input_mount_absent': True, 'init_state_mount_absent': True, 'protected_mount_read_only': True,
                'binary_sha256': hashes,
                'image_id': next(item['imageID'] for item in pod['status']['containerStatuses'] if item['name'] == 'cpra')}

    def writes(self):
        credential, header = self.api('POST', '/api/v2/credentials', data={
            'apiVersion': 'cpra.io/v2', 'kind': 'Credential', 'metadata': {'id': 'shared-secret'},
            'spec': {'value': self.credential}}, headers={'If-None-Match': '*'})
        operations = [self.wait_operation(header)]
        monitor, header = self.api('POST', '/api/v2/monitors', data={
            'apiVersion': 'cpra.io/v2', 'kind': 'Monitor', 'metadata': {'id': 'managed-check', 'name': 'Original'},
            'spec': {'enabled': False, 'check': {'driver': {'type': 'http', 'config': {
                'url': 'http://cpra-bench-target:8080/cgi-bin/check'}}, 'interval': '1s', 'timeout': '1s',
                'healthyThreshold': 1, 'unhealthyThreshold': 1}}}, headers={'If-None-Match': '*'})
        operations.append(self.wait_operation(header))
        self.monitor_uid, self.credential_uid = monitor['metadata']['uid'], credential['metadata']['uid']
        old_version = monitor['metadata']['resourceVersion']
        monitor, header = self.api('PATCH', '/api/v2/monitors/managed-check', data={'metadata': {'name': 'Edited'}},
                                   headers={'Content-Type': 'application/merge-patch+json', 'If-Match': '"' + old_version + '"'})
        operations.append(self.wait_operation(header))
        if not self.monitor_uid or not self.credential_uid or len(set(operations)) != 3 \
                or monitor['metadata']['uid'] != self.monitor_uid \
                or monitor['metadata']['resourceVersion'] == old_version \
                or credential['spec'].get('value') is not None:
            raise RuntimeError('managed writes lost identity, versioning or redaction')
        for token, status in ((self.read_token, 403), (self.reader, 403), ('invalid-fixture-token', 401)):
            self.api('POST', '/api/v2/credentials', token=token, data={},
                     headers={'If-None-Match': '*'}, expected=status)
        # A stale conditional write must fail, rather than silently fetching a new version.
        self.api('PATCH', '/api/v2/monitors/managed-check', data={'metadata': {'name': 'Stale'}},
                 headers={'Content-Type': 'application/merge-patch+json', 'If-Match': '"' + old_version + '"'}, expected=412)
        observed, credential = self.resources()
        if observed['metadata']['name'] != 'Edited' or observed['spec']['enabled'] is not False:
            raise RuntimeError('managed configuration activation differs from the committed write')
        self.record('managed_conditional_writes_and_redaction', operation_ids=operations,
                    monitor_uid=self.monitor_uid, credential_uid=self.credential_uid,
                    credential_value_available=False, read_token_write_status=403, reader_write_status=403,
                    wrong_token_status=401, stale_version_status=412)

    def probes(self):
        # The preceding read-only outage deliberately leaves this fixture unhealthy.
        self.run(self.k + ['exec', 'cpra-bench-target', '--', 'rm', '-f', '/tmp/fail'])
        previous = self.target_count()
        self.run(self.k + ['exec', 'cpra-bench-target', '--', 'wget', '-qO-', 'http://127.0.0.1:8080/cgi-bin/check'])
        count = self.target_count()
        if count <= previous:
            raise RuntimeError('managed target receipt counter failed calibration')
        for _ in range(5):
            self.ctl('health'); self.ctl('ready')
        self.run(self.h + ['test', 'cpra-management', '--timeout', '60s'])
        after = self.target_count()
        if after != count:
            raise RuntimeError('managed probes or Helm test invoked a provider')
        # The Helm test uses the Service DNS SAN, independently of the loopback tunnel.
        self.record('managed_https_probes_have_zero_provider_calls', before=count, after=after,
                    service_dns='cpra-managed.' + self.args.namespace + '.svc', tls_verified=True)

    def restart(self):
        identity, uid = self.identity(), self.pod()['metadata']['uid']
        self.run(self.k + ['delete', 'pod', 'cpra-managed-0', '--wait=true', '--timeout=90s'])
        self.run(self.k + ['rollout', 'status', 'statefulset/cpra-managed', '--timeout=180s'])
        self.until(lambda: self.ctl('ready', check=False).returncode == 0, 'restarted readiness')
        restarted_uid = self.pod()['metadata']['uid']
        if restarted_uid == uid or self.identity() != identity:
            raise RuntimeError('managed restart did not replace the pod and retain node identity')
        replacement = secrets.token_urlsafe(40)
        self.redactions.append(replacement)
        self.secret('cpra-managed-policy-v2', {'policy.yaml': self.policy(replacement)})
        self.values['management']['policy']['existingSecret'] = 'cpra-managed-policy-v2'
        self.deploy()
        self.api('GET', '/api/v2/monitors/managed-check', token=replacement, expected=401)
        monitor, credential = self.resources()
        if monitor['metadata']['uid'] != self.monitor_uid or credential['metadata']['uid'] != self.credential_uid \
                or monitor['metadata']['name'] != 'Edited' or monitor['spec']['enabled'] is not False \
                or self.identity() != identity:
            raise RuntimeError('managed restart changed committed configuration or bootstrap authority')
        self.record('managed_restart_preserves_configuration_and_authority', previous_pod_uid=uid,
                    current_pod_uid=restarted_uid, node_identity_sha256=hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest(),
                    monitor_uid=monitor['metadata']['uid'], credential_uid=credential['metadata']['uid'],
                    replacement_policy_token_status=401, original_operator_status=200)

    def rotate(self):
        self.maintenance()
        claim = self.values['persistence'].get('existingClaim', 'state-cpra-managed-0')
        self.apply(admin_pod(self.args.image, claim))
        self.run(self.k + ['wait', '--for=condition=Ready', 'pod/cpra-managed-admin', '--timeout=120s'])
        revisions, fresh = [], []
        for command, output in ((['rotate-read-token'], 'read'), (['rotate', 'team/oncall'], 'operator')):
            # Mutations run exactly once; an uncertain result ends this campaign.
            result = self.run(self.k + ['exec', 'cpra-managed-admin', '--', '/usr/local/bin/cpractl',
                                      'local', 'auth', *command, '--data-dir', '/var/lib/cpra',
                                      '--token-output', '/tmp/cpra-tokens/' + output], check=False)
            if result.returncode:
                raise RuntimeError('stopped managed rotation did not complete; retain the original attempt')
            receipt = json.loads(result.stdout)
            if receipt.get('outcome') != 'committed' or not receipt.get('revision') \
                    or receipt['revision'] != receipt.get('intendedRevision'):
                raise RuntimeError('stopped managed rotation lacks an authoritative receipt')
            token = self.run(self.k + ['exec', 'cpra-managed-admin', '--', 'cat',
                                     '/tmp/cpra-tokens/' + output], check=False)
            if token.returncode or not token.stdout.strip():
                raise RuntimeError('stopped managed rotation token was not readable')
            fresh.append(token.stdout.strip())
            self.redactions.append(fresh[-1])
            revisions.append(receipt['revision'])
        if len(set(fresh + [self.read_token, self.operator])) != 4 or len(set(revisions)) != 2:
            raise RuntimeError('managed rotation did not generate distinct access revisions and tokens')
        self.secret('cpra-managed-read-v2', {'token': fresh[0].encode()})
        self.run(self.k + ['delete', 'pod', 'cpra-managed-admin', '--wait=true', '--timeout=90s'])
        self.values['auth']['existingSecret'] = 'cpra-managed-read-v2'
        self.values['maintenance'] = False
        self.deploy()
        self.api('GET', '/api/v2/monitors/managed-check', token=self.read_token, expected=401)
        self.api('GET', '/api/v2/monitors/managed-check', token=self.operator, expected=401)
        self.read_token, self.operator = fresh
        self.api('GET', '/api/v2/monitors/managed-check', token=self.read_token)
        monitor, credential = self.resources()
        if monitor['metadata']['uid'] != self.monitor_uid or credential['metadata']['uid'] != self.credential_uid:
            raise RuntimeError('managed rotation changed catalog identities')
        self.record('managed_access_rotation_requires_stopped_owner', stopped_owner=True, administrative_pod_removed=True,
                    committed_revisions=revisions, old_read_token_status=401, old_operator_status=401,
                    new_read_token_status=200, new_operator_status=200)

    def fence(self):
        if self.args.access_mode != 'ReadWriteOncePod':
            return  # RWO reports remain diagnostic; the release reader requires this scenario.
        claim = 'state-cpra-managed-0'
        contender = admin_pod(self.args.image, claim)
        contender['metadata']['name'] = 'cpra-managed-contender'
        self.apply(contender)

        def rejected():
            pod = json.loads(self.run(self.k + ['get', 'pod', 'cpra-managed-contender', '-o', 'json']).stdout)
            if pod['status']['phase'] != 'Pending':
                raise RuntimeError('managed contender acquired the live RWOP claim')
            return next((item.get('message', '') for item in pod['status'].get('conditions', [])
                         if item['type'] == 'PodScheduled' and item['status'] == 'False'
                         and 'readwriteoncepod' in item.get('message', '').lower()), None)

        reason = self.until(rejected, 'RWOP fencing', seconds=60)
        self.ctl('ready')
        self.run(self.k + ['delete', 'pod', 'cpra-managed-contender', '--wait=true', '--timeout=90s'])
        pvc = json.loads(self.run(self.k + ['get', 'pvc', claim, '-o', 'json']).stdout)
        volume = json.loads(self.run(self.k + ['get', 'pv', pvc['spec']['volumeName'], '-o', 'json']).stdout)
        driver = volume['spec']['csi']['driver']
        filesystem = self.run(self.k + ['exec', 'cpra-managed-0', '--', 'stat', '-f', '-c', '%T', '/var/lib/cpra']).stdout.strip()
        if pvc['spec']['accessModes'] != ['ReadWriteOncePod'] or not driver or not filesystem:
            raise RuntimeError('managed RWOP volume lacks actual CSI/filesystem evidence')
        self.record('managed_rwop_fences_second_owner', scheduler_reason=reason, csi_driver=driver,
                    state_filesystem=filesystem, claim=claim)

    def upgrade(self):
        if not self.args.upgrade_helm:
            return
        identity = self.identity()
        previous = self.run([self.h[0], 'version', '--short']).stdout.strip()
        self.h[0] = self.args.upgrade_helm
        self.deploy()
        current = self.run([self.h[0], 'version', '--short']).stdout.strip()
        monitor, credential = self.resources()
        if self.identity() != identity or monitor['metadata']['uid'] != self.monitor_uid \
                or credential['metadata']['uid'] != self.credential_uid:
            raise RuntimeError('managed Helm client upgrade changed authoritative state')
        self.record('managed_upgrade_between_helm_versions', previous=previous, current=current,
                    monitor_uid=monitor['metadata']['uid'], credential_uid=credential['metadata']['uid'])

    def restore(self):
        self.run(self.k + ['exec', 'cpra-bench-target', '--', 'touch', '/tmp/fail'])
        monitor, _ = self.resources()
        monitor = self.patch_monitor(monitor, {'spec': {'enabled': True}})
        events = self.until(lambda: self.history(), 'committed incident history')
        event_ids = sorted({item['id'] for item in events})
        if not event_ids or len(event_ids) != len(events):
            raise RuntimeError('managed incident history is absent or duplicated')
        self.patch_monitor(monitor, {'spec': {'enabled': False}})
        identity = self.identity()
        self.maintenance()
        source, restored = 'state-cpra-managed-0', 'cpra-managed-restored'
        pvc = {'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim', 'metadata': {'name': restored},
               'spec': {'accessModes': [self.args.access_mode], 'resources': {'requests': {'storage': '256Mi'}}}}
        if self.args.storage_class is not None:
            pvc['spec']['storageClassName'] = self.args.storage_class
        self.apply(pvc)
        copy_pod = admin_pod(self.args.image, restored)
        copy_pod['metadata']['name'] = 'cpra-managed-copy'
        copy_pod['spec']['volumes'].append({'name': 'source', 'persistentVolumeClaim': {'claimName': source, 'readOnly': True}})
        copy_pod['spec']['containers'][0]['volumeMounts'].append({'name': 'source', 'mountPath': '/source', 'readOnly': True})
        self.apply(copy_pod)
        self.run(self.k + ['wait', '--for=condition=Ready', 'pod/cpra-managed-copy', '--timeout=120s'])
        self.run(self.k + ['exec', 'cpra-managed-copy', '--', 'sh', '-ec',
                          'test -f /source/identity.json; test -f /source/raft.db; '
                          'test -f /source/history/catalog.json; test -d /source/snapshots; '
                          'test -d /source/history; cp -a /source/. /var/lib/cpra/'])
        inventory = []
        for path in ('/source', '/var/lib/cpra'):
            lines = self.run(self.k + ['exec', 'cpra-managed-copy', '--', 'sh', '-ec',
                                      'cd "$1"; find . -type f > /tmp/cpra-inventory.paths; '
                                      'while IFS= read -r file; do sha256sum "$file"; done '
                                      '< /tmp/cpra-inventory.paths > /tmp/cpra-inventory.hashes; '
                                      'LC_ALL=C sort /tmp/cpra-inventory.hashes', 'sh', path]).stdout
            if not lines or len(lines.splitlines()) > 4096:
                raise RuntimeError('stopped managed copy inventory is absent or unbounded')
            inventory.append(lines)
        if inventory[0] != inventory[1]:
            raise RuntimeError('stopped managed copy omitted or changed stored bytes')
        self.run(self.k + ['exec', '-i', 'cpra-managed-copy', '--', 'sh', '-ec',
                          'umask 077; cat > /tmp/credential-pattern'], data=self.credential)
        scan = self.run(self.k + ['exec', 'cpra-managed-copy', '--', 'sh', '-c',
                                 'grep -r -F -q -f /tmp/credential-pattern /source'], check=False)
        if scan.returncode != 1:
            raise RuntimeError('managed state contains plaintext or could not be scanned')
        self.run(self.k + ['delete', 'pod', 'cpra-managed-copy', '--wait=true', '--timeout=90s'])
        self.run(self.h + ['uninstall', 'cpra-management', '--wait', '--timeout', '90s'])
        self.run(self.k + ['get', 'pvc', source, restored])
        self.values['persistence']['existingClaim'] = restored
        self.values['maintenance'] = False
        before = self.target_count()
        self.deploy(install=True)
        monitor, credential = self.resources()
        history = self.history()
        if self.identity() != identity or monitor['metadata']['uid'] != self.monitor_uid \
                or credential['metadata']['uid'] != self.credential_uid or monitor['spec']['enabled'] is not False \
                or monitor['metadata']['name'] != 'Edited' or not set(event_ids) <= {item['id'] for item in history} \
                or len({item['id'] for item in history}) != len(history) or self.target_count() != before:
            raise RuntimeError('managed restore changed configuration, history, identity or disabled execution')
        self.record('managed_stopped_copy_restore_preserves_encrypted_history', stopped_owner=True,
                    backup_method='trusted stopped complete-directory clone', original_claim=source, restored_claim=restored,
                    inventory_sha256=hashlib.sha256(inventory[0].encode()).hexdigest(), inventory_files=len(inventory[0].splitlines()),
                    credential_plaintext_absent=True, event_ids=event_ids, restored_event_ids=sorted({item['id'] for item in history}),
                    node_identity_sha256=hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest(),
                    monitor_uid=monitor['metadata']['uid'], credential_uid=credential['metadata']['uid'],
                    target_before=before, target_after=self.target_count())
        self.run(self.h + ['uninstall', 'cpra-management', '--wait', '--timeout', '90s'])
        self.run(self.k + ['get', 'pvc', source, restored])

    def execute(self):
        with tempfile.TemporaryDirectory(prefix='cpra-managed-helm-') as work:
            self.work = Path(work)
            self.value_file = self.work / 'values.yaml'
            self.provision()
            self.deploy(install=True)
            mounts = self.verify_mounts()
            self.run(self.h + ['test', 'cpra-management', '--timeout', '60s'])
            self.record('managed_secret_sources_and_verified_tls', **mounts,
                        service_dns='cpra-managed.' + self.args.namespace + '.svc', tls_verified=True)
            self.writes()
            self.probes()
            self.restart()
            self.rotate()
            self.fence()
            self.upgrade()
            self.restore()
