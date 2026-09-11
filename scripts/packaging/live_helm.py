#!/usr/bin/env python3
"""Exercise a candidate chart only in a designated cpra-* test context/namespace.

Requires a pre-created isolated cluster, a locally available candidate image, Helm,
kubectl and PyYAML. --access-mode explicitly records the provisioner's fence.
The suite deliberately creates local HTTP incident activity, then verifies restart,
versioned token rotation, offline whole-directory copy/restore and PVC retention.
It removes its newly created namespace on completion; never supply an existing one.
"""
import argparse
import base64
import copy
import gzip
import hashlib
from datetime import datetime, timezone
import json
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import time

import yaml

ROOT = Path(__file__).resolve().parents[2]


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--image', required=True)
    p.add_argument('--target-image', default='busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0')
    p.add_argument('--helm', default='helm')
    p.add_argument('--upgrade-helm', help='A different Helm binary to exercise an in-place Helm 3 to 4 upgrade')
    p.add_argument('--kubeconfig', required=True)
    p.add_argument('--context', required=True)
    p.add_argument('--namespace', required=True)
    p.add_argument('--access-mode', choices=['ReadWriteOncePod', 'ReadWriteOnce'], default='ReadWriteOncePod')
    p.add_argument('--storage-class')
    p.add_argument('--out', required=True)
    a = p.parse_args()
    if not a.namespace.startswith('cpra-') or not (a.context.startswith('kind-cpra-') or a.context.startswith('cpra-')):
        p.error('context and namespace must identify a dedicated cpra- test environment')
    out = Path(a.out)
    if out.exists():
        p.error('evidence output already exists; preserve previous attempts with a new path')
    out.parent.mkdir(parents=True, exist_ok=True)
    tokens = [secrets.token_urlsafe(32), secrets.token_urlsafe(32)]
    report = {'started': datetime.now(timezone.utc).isoformat(), 'image': a.image,
              'target_image': a.target_image, 'context': a.context, 'namespace': a.namespace, 'access_mode': a.access_mode,
              'status': 'incomplete', 'checks': [],
              'csi_rwop_fencing': 'not_verified' if a.access_mode == 'ReadWriteOnce' else 'not_exercised'}
    created = False
    k = ['kubectl', '--kubeconfig', a.kubeconfig, '--context', a.context, '-n', a.namespace]
    h = [a.helm, '--kubeconfig', a.kubeconfig, '--kube-context', a.context, '-n', a.namespace]

    def redact(text):
        for token in tokens:
            text = text.replace(token, '[REDACTED]').replace(base64.b64encode(token.encode()).decode(), '[REDACTED]')
        return text

    def run(cmd, data=None, check=True, timeout=240):
        r = subprocess.run(cmd, input=data, text=True, capture_output=True, timeout=timeout)
        if check and r.returncode:
            raise RuntimeError(f'{cmd[0]} {cmd[1]} failed ({r.returncode}): {redact(r.stderr + r.stdout)[-5000:]}')
        return r

    def apply(obj):
        return run(k + ['apply', '-f', '-'], json.dumps(obj))

    def record(name, **details):
        report['checks'].append({'name': name, 'status': 'pass', **details})
        out.write_text(json.dumps(report, indent=2) + '\n')
        print(name + ': pass', flush=True)

    def until(fn, description, seconds=120):
        deadline = time.monotonic() + seconds
        last = None
        while time.monotonic() < deadline:
            try:
                result = fn()
                if result:
                    return result
            except (RuntimeError, ValueError, KeyError) as e:
                last = str(e)
            time.sleep(1)
        raise RuntimeError(f'timeout waiting for {description}: {last}')

    def ctl(*args, check=True):
        return run(k + ['exec', 'cpra-fixture-0', '--', '/usr/local/bin/cpractl', *args,
                        '--server', 'http://127.0.0.1:8060', '--token-file', '/run/secrets/cpra/token',
                        '--request-timeout', '2s'], check=check, timeout=10)

    def history():
        return json.loads(ctl('get', 'history', 'packaging-fixture', '-o', 'json').stdout)['events']

    def wait_ready():
        run(k + ['rollout', 'status', 'statefulset/cpra-fixture', '--timeout=180s'])
        until(lambda: ctl('ready', check=False).returncode == 0, 'authenticated readiness')

    def target_count():
        return int(run(k + ['exec', 'cpra-target', '--', 'sh', '-c', 'wc -l < /tmp/checks']).stdout)

    try:
        if run(k + ['get', 'namespace', a.namespace], check=False).returncode == 0:
            raise RuntimeError('refusing to mutate an existing namespace')
        run(k + ['create', 'namespace', a.namespace])
        created = True
        report['kubernetes'] = json.loads(run(k + ['version', '-o', 'json']).stdout)
        report['helm'] = run([a.helm, 'version', '--short']).stdout.strip()
        report['storage_classes'] = json.loads(run(k + ['get', 'storageclasses', '-o', 'json']).stdout)
        for i, token in enumerate(tokens, 1):
            apply({'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': f'cpra-api-v{i}'},
                   'immutable': True, 'type': 'Opaque', 'stringData': {'token': token}})
        manifest = {'monitors': [{'id': 'packaging-fixture', 'name': 'packaging-fixture', 'enabled': True,
                    'pulse_check': {'type': 'http', 'interval': '1h', 'timeout': '1s',
                                    'unhealthy_threshold': 1, 'healthy_threshold': 1,
                                    'config': {'url': 'http://cpra-target:8080/cgi-bin/check'}},
                    'codes': {'red': {'dispatch': True, 'notify': 'log', 'config': {'file': '/var/lib/cpra/events.jsonl'}},
                              'green': {'dispatch': True, 'notify': 'log', 'config': {'file': '/var/lib/cpra/events.jsonl'}}}}]}
        monitor_config = {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'cpra-monitors'},
                          'immutable': True, 'data': {'monitors.yaml': yaml.safe_dump(manifest)}}
        target_script = """set -eu
mkdir -p /tmp/www/cgi-bin
: > /tmp/checks
cat > /tmp/www/cgi-bin/check <<'CGI'
#!/bin/sh
printf 'check\\n' >> /tmp/checks
if test -f /tmp/fail; then printf 'Status: 503 Unavailable\\r\\n'; fi
printf 'Content-Type: text/plain\\r\\n\\r\\nfixture\\n'
CGI
chmod 755 /tmp/www/cgi-bin/check
exec httpd -f -p 8080 -h /tmp/www
"""
        apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'cpra-target', 'labels': {'fixture': 'cpra-target'}},
               'spec': {'automountServiceAccountToken': False, 'containers': [{'name': 'target', 'image': a.target_image,
                        'imagePullPolicy': 'IfNotPresent', 'command': ['/bin/sh', '-c', target_script],
                        'securityContext': {'runAsUser': 1001, 'readOnlyRootFilesystem': True, 'allowPrivilegeEscalation': False, 'capabilities': {'drop': ['ALL']}},
                        'volumeMounts': [{'name': 'tmp', 'mountPath': '/tmp'}]}], 'volumes': [{'name': 'tmp', 'emptyDir': {'sizeLimit': '16Mi'}}]}})
        apply({'apiVersion': 'v1', 'kind': 'Service', 'metadata': {'name': 'cpra-target'},
               'spec': {'selector': {'fixture': 'cpra-target'}, 'ports': [{'port': 8080, 'targetPort': 8080}]}})
        run(k + ['wait', '--for=condition=Ready', 'pod/cpra-target', '--timeout=120s'])
        if '@sha256:' in a.image:
            repo, digest = a.image.rsplit('@', 1)
            image_values = {'repository': repo, 'digest': digest, 'pullPolicy': 'Never'}
        else:
            repo, tag = a.image.rsplit(':', 1)
            image_values = {'repository': repo, 'tag': tag, 'pullPolicy': 'Never'}
        values = {'fullnameOverride': 'cpra-fixture', 'image': image_values,
                  'auth': {'existingSecret': 'cpra-api-v1'}, 'manifest': {'existingConfigMap': 'cpra-monitors'},
                  'persistence': {'accessMode': a.access_mode, 'acknowledgeReadWriteOnce': a.access_mode == 'ReadWriteOnce', 'size': '256Mi'}}
        if a.storage_class is not None:
            values['persistence']['storageClass'] = a.storage_class
        with tempfile.TemporaryDirectory(prefix='cpra-live-helm-') as tmp:
            vf = Path(tmp) / 'values.yaml'

            def deploy(install=False, wait=True, timeout='4m', check=True):
                vf.write_text(yaml.safe_dump(values))
                command = ['install', 'cpra', str(ROOT/'charts/cpra')] if install else ['upgrade', 'cpra', str(ROOT/'charts/cpra')]
                return run(h + command + ['-f', str(vf), '--timeout', timeout] + (['--wait'] if wait else []), timeout=270, check=check)

            # Delay a required mount after admission; startup must wait without restart churn.
            deploy(install=True, wait=False)
            until(lambda: run(k + ['get', 'pod', 'cpra-fixture-0'], check=False).returncode == 0, 'created pod')
            time.sleep(5)
            waiting = json.loads(run(k + ['get', 'pod', 'cpra-fixture-0', '-o', 'json']).stdout)
            assert not any(s.get('restartCount', 0) for s in waiting['status'].get('containerStatuses', []))
            apply(monitor_config)
            wait_ready()
            current = json.loads(run(k + ['get', 'pod', 'cpra-fixture-0', '-o', 'json']).stdout)
            report['image_id'] = current['status']['containerStatuses'][0]['imageID']
            report['source'] = json.loads(run(k + ['exec', 'cpra-fixture-0', '--', 'cat', '/usr/share/cpra/RELEASE.json']).stdout)
            report['cpra_version'] = run(k + ['exec', 'cpra-fixture-0', '--', '/usr/local/bin/cpra', '-version']).stdout.strip()
            report['binary_sha256'] = run(k + ['exec', 'cpra-fixture-0', '--', 'sha256sum', '/usr/local/bin/cpra', '/usr/local/bin/cpractl']).stdout.strip()
            record('install_readiness_nonroot_readonly')
            record('delayed_manifest_mount_starts_without_restart_churn', delay_seconds=5)
            state_claim = json.loads(run(k + ['get', 'pvc', 'state-cpra-fixture-0', '-o', 'json']).stdout)
            report['state_volume'] = json.loads(run(k + ['get', 'pv', state_claim['spec']['volumeName'], '-o', 'json']).stdout)
            report['state_filesystem'] = run(k + ['exec', 'cpra-fixture-0', '--', 'stat', '-f', '-c', '%T', '/var/lib/cpra']).stdout.strip()
            if a.access_mode == 'ReadWriteOncePod':
                assert state_claim['spec']['accessModes'] == ['ReadWriteOncePod']
                apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'cpra-contending-owner'},
                       'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False,
                                'securityContext': {'runAsUser': 1001, 'runAsGroup': 1001, 'fsGroup': 1001},
                                'containers': [{'name': 'contender', 'image': a.image, 'imagePullPolicy': 'Never',
                                                'command': ['/bin/sh', '-c', 'echo unexpected-volume-owner; exit 1'],
                                                'securityContext': {'readOnlyRootFilesystem': True, 'allowPrivilegeEscalation': False, 'capabilities': {'drop': ['ALL']}},
                                                'volumeMounts': [{'name': 'state', 'mountPath': '/var/lib/cpra'}]}],
                                'volumes': [{'name': 'state', 'persistentVolumeClaim': {'claimName': 'state-cpra-fixture-0'}}]}})

                def fenced():
                    pod = json.loads(run(k + ['get', 'pod', 'cpra-contending-owner', '-o', 'json']).stdout)
                    if pod['status']['phase'] != 'Pending':
                        raise ValueError('contending owner passed scheduling despite RWOP')
                    return next((c.get('message', '') for c in pod['status'].get('conditions', [])
                                 if c['type'] == 'PodScheduled' and c['status'] == 'False'
                                 and 'readwriteoncepod' in c.get('message', '').lower()), None)

                reason = until(fenced, 'RWOP rejection of a second owner', seconds=60)
                ctl('ready')
                run(k + ['delete', 'pod', 'cpra-contending-owner', '--wait=true'])
                report['csi_rwop_fencing'] = 'pass'
                record('rwop_blocks_contending_owner', scheduler_reason=reason)
            # Calibrate the independent receipt counter, then keep normal scheduled
            # work an hour apart while checking for calls induced by client probes.
            previous = target_count()
            run(k + ['exec', 'cpra-target', '--', 'wget', '-qO-', 'http://127.0.0.1:8080/cgi-bin/check'])
            assert target_count() > previous
            count = target_count()
            for _ in range(5):
                ctl('health')
                ctl('ready')
            run(h + ['test', 'cpra', '--timeout', '60s'])
            assert target_count() == count, 'probes or Helm test generated an unscheduled provider call'
            record('probes_and_helm_test_cause_zero_provider_calls', before=count, after=target_count())
            if a.upgrade_helm:
                h[0] = a.upgrade_helm
                report['upgrade_helm'] = run([a.upgrade_helm, 'version', '--short']).stdout.strip()
                deploy()
                wait_ready()
                record('in_place_upgrade_between_helm_versions', previous=report['helm'], current=report['upgrade_helm'])
            manifest['monitors'][0]['pulse_check']['interval'] = '1s'
            monitor_config['metadata']['name'] = 'cpra-monitors-active'
            monitor_config['data']['monitors.yaml'] = yaml.safe_dump(manifest)
            apply(monitor_config)
            values['manifest']['existingConfigMap'] = 'cpra-monitors-active'
            deploy()
            wait_ready()
            run(k + ['exec', 'cpra-target', '--', 'touch', '/tmp/fail'])
            events = until(lambda: history(), 'durable incident event')
            ids = {event['id'] for event in events}
            assert all(event['monitor_id'] == 'packaging-fixture' for event in events)
            ctl('health')
            record('target_outage_retains_events_and_liveness', event_ids=sorted(ids))
            identity = run(k + ['exec', 'cpra-fixture-0', '--', 'cat', '/var/lib/cpra/identity.json']).stdout
            old_uid = json.loads(run(k + ['get', 'pod', 'cpra-fixture-0', '-o', 'json']).stdout)['metadata']['uid']
            run(k + ['delete', 'pod', 'cpra-fixture-0', '--wait=true', '--timeout=90s'])
            wait_ready()
            new_uid = json.loads(run(k + ['get', 'pod', 'cpra-fixture-0', '-o', 'json']).stdout)['metadata']['uid']
            assert new_uid != old_uid
            assert ids <= {event['id'] for event in history()}
            assert run(k + ['exec', 'cpra-fixture-0', '--', 'cat', '/var/lib/cpra/identity.json']).stdout == identity
            record('restart_restores_node_identity_and_history')
            values['auth']['existingSecret'] = 'cpra-api-v2'
            deploy()
            wait_ready()
            assert ids <= {event['id'] for event in history()}
            record('immutable_secret_rotation_rollout')
            values['auth']['existingSecret'] = 'cpra-api-intentionally-missing'
            failed = deploy(timeout='20s', check=False)
            assert failed.returncode != 0, 'missing token unexpectedly passed upgrade readiness'
            values['auth']['existingSecret'] = 'cpra-api-v2'
            deploy(wait=False)
            # StatefulSet can retain the failed revision's pod after a reverted
            # template. Delete normally only after restoring the valid template.
            run(k + ['delete', 'pod', 'cpra-fixture-0', '--wait=true', '--timeout=90s'])
            wait_ready()
            assert ids <= {event['id'] for event in history()}
            assert run(k + ['exec', 'cpra-fixture-0', '--', 'cat', '/var/lib/cpra/identity.json']).stdout == identity
            record('failed_upgrade_then_explicit_recovery_preserves_state')

            # Place a genuinely large manifest outside Kubernetes objects and Helm
            # release values. Disabled fixture monitors avoid extra provider load.
            large = copy.deepcopy(manifest)
            for index in range(10000):
                monitor = copy.deepcopy(manifest['monitors'][0])
                monitor.update(id=f'large-{index}', name=f'large-{index}', enabled=False)
                large['monitors'].append(monitor)
            large_data = yaml.safe_dump(large, sort_keys=False)
            assert len(large_data.encode()) > 1024 * 1024
            config_claim = {'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim', 'metadata': {'name': 'cpra-large-manifest'},
                            'spec': {'accessModes': [a.access_mode], 'resources': {'requests': {'storage': '64Mi'}}}}
            if a.storage_class is not None:
                config_claim['spec']['storageClassName'] = a.storage_class
            apply(config_claim)
            apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'cpra-stage-config'},
                   'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False,
                            'securityContext': {'runAsUser': 1001, 'runAsGroup': 1001, 'fsGroup': 1001},
                            'containers': [{'name': 'stage', 'image': a.target_image, 'command': ['sleep', '300'],
                                            'securityContext': {'readOnlyRootFilesystem': True, 'allowPrivilegeEscalation': False, 'capabilities': {'drop': ['ALL']}},
                                            'volumeMounts': [{'name': 'config', 'mountPath': '/config'}]}],
                            'volumes': [{'name': 'config', 'persistentVolumeClaim': {'claimName': 'cpra-large-manifest'}}]}})
            run(k + ['wait', '--for=condition=Ready', 'pod/cpra-stage-config', '--timeout=120s'])
            # Some kubectl/server streaming combinations close large stdin copies
            # early without a failing exit status. Chunk compressed bytes and verify
            # the actual mounted file before allowing the controller to read it.
            payload = gzip.compress(large_data.encode(), mtime=0)
            run(k + ['exec', 'cpra-stage-config', '--', 'sh', '-c', 'umask 027; : > /config/large.yaml.gz'])
            for offset in range(0, len(payload), 16 * 1024):
                chunk = base64.b64encode(payload[offset:offset + 16 * 1024]).decode()
                run(k + ['exec', '-i', 'cpra-stage-config', '--', 'sh', '-c', 'base64 -d >> /config/large.yaml.gz'], data=chunk)
            run(k + ['exec', 'cpra-stage-config', '--', 'sh', '-c', 'umask 027; gzip -dc /config/large.yaml.gz > /config/large.yaml; rm /config/large.yaml.gz'])
            expected_hash = hashlib.sha256(large_data.encode()).hexdigest()
            actual_hash = run(k + ['exec', 'cpra-stage-config', '--', 'sha256sum', '/config/large.yaml']).stdout.split()[0]
            assert actual_hash == expected_hash, 'mounted manifest differs from the generated fixture; refuse rollout'
            run(k + ['delete', 'pod', 'cpra-stage-config', '--wait=true'])
            values['manifest'] = {'existingClaim': 'cpra-large-manifest', 'file': 'large.yaml', 'revision': 'large-v1'}
            deploy()
            wait_ready()
            assert ids <= {event['id'] for event in history()}
            record('large_file_backed_manifest_survives_rollout', file_bytes=len(large_data.encode()), configured_monitors=10001,
                   active_monitors=1, source='separate read-only PVC', sha256=actual_hash)
            values['maintenance'] = True
            deploy()
            run(k + ['wait', '--for=delete', 'pod/cpra-fixture-0', '--timeout=90s'])
            source = 'state-cpra-fixture-0'
            claim = {'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim', 'metadata': {'name': 'cpra-restored'},
                     'spec': {'accessModes': [a.access_mode], 'resources': {'requests': {'storage': '256Mi'}}}}
            if a.storage_class is not None:
                claim['spec']['storageClassName'] = a.storage_class
            apply(claim)
            apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'cpra-offline-copy'},
                   'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False,
                            'securityContext': {'runAsUser': 1001, 'runAsGroup': 1001, 'fsGroup': 1001},
                            'containers': [{'name': 'copy', 'image': a.image, 'imagePullPolicy': 'Never',
                                            'command': ['/bin/sh', '-c', 'set -eu; test -f /source/identity.json; test -f /source/history/catalog.json; cp -a /source/. /restore/; cmp /source/identity.json /restore/identity.json; cmp /source/history/catalog.json /restore/history/catalog.json'],
                                            'securityContext': {'readOnlyRootFilesystem': True, 'allowPrivilegeEscalation': False, 'capabilities': {'drop': ['ALL']}},
                                            'volumeMounts': [{'name': 'source', 'mountPath': '/source', 'readOnly': True}, {'name': 'restore', 'mountPath': '/restore'}]}],
                            'volumes': [{'name': 'source', 'persistentVolumeClaim': {'claimName': source, 'readOnly': True}},
                                        {'name': 'restore', 'persistentVolumeClaim': {'claimName': 'cpra-restored'}}]}})
            until(lambda: json.loads(run(k + ['get', 'pod', 'cpra-offline-copy', '-o', 'json']).stdout)['status']['phase'] == 'Succeeded', 'offline state copy')
            run(k + ['delete', 'pod', 'cpra-offline-copy', '--wait=true'])
            record('offline_whole_state_copy_includes_history')
            run(h + ['uninstall', 'cpra', '--wait', '--timeout', '90s'])
            run(k + ['get', 'pvc', source])
            record('uninstall_retains_original_pvc')
            values['maintenance'] = False
            values['persistence']['existingClaim'] = 'cpra-restored'
            deploy(install=True)
            wait_ready()
            assert ids <= {event['id'] for event in history()}
            assert run(k + ['exec', 'cpra-fixture-0', '--', 'cat', '/var/lib/cpra/identity.json']).stdout == identity
            record('restore_existing_claim_recovers_history_and_identity')
            values['api'] = {'enabled': False}
            values['auth'] = {'existingSecret': ''}
            deploy()
            pod = json.loads(run(k + ['get', 'pod', 'cpra-fixture-0', '-o', 'json']).stdout)
            assert '-web=false' in pod['spec']['containers'][0]['args']
            assert not any('Probe' in key for key in pod['spec']['containers'][0])
            assert run(k + ['get', 'service', 'cpra-fixture'], check=False).returncode != 0
            record('headless_mode_has_no_api_service_or_probes')
            run(h + ['uninstall', 'cpra', '--wait', '--timeout', '90s'])
            run(k + ['get', 'pvc', source, 'cpra-restored'])
            record('uninstall_preserves_restored_and_original_claims')
        report['status'] = 'pass'
    except Exception as exc:
        report['status'] = 'fail'
        report['error'] = redact(str(exc))
        if created:
            report['pods'] = redact(run(k + ['get', 'pods', '-o', 'wide'], check=False).stdout)
            report['controller_logs'] = redact(run(k + ['logs', 'cpra-fixture-0', '--tail=100'], check=False).stdout)
            report['copy_logs'] = redact(run(k + ['logs', 'cpra-offline-copy', '--tail=30'], check=False).stdout)
            report['events'] = redact(run(k + ['get', 'events', '--sort-by=.metadata.creationTimestamp'], check=False).stdout[-12000:])
    finally:
        if created:
            cleanup = run(k + ['delete', 'namespace', a.namespace, '--wait=true', '--timeout=120s'], check=False, timeout=150)
            report['cleanup'] = 'pass' if cleanup.returncode == 0 else redact(cleanup.stderr)
            if cleanup.returncode:
                report['status'] = 'fail'
        report['finished'] = datetime.now(timezone.utc).isoformat()
        out.write_text(json.dumps(report, indent=2) + '\n')
    print(str(out) + ': ' + report['status'])
    return 0 if report['status'] == 'pass' else 1


if __name__ == '__main__':
    sys.exit(main())
