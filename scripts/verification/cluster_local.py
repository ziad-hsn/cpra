#!/usr/bin/env python3
"""Actual Kubernetes, systemd and kernel ICMP tests in a disposable kind node.

The caller creates an isolated cpra-prefixed kind cluster and loads ubuntu:latest
into its nodes first; python3 and dbus must be installed in the node image. No host services or host sysctls are modified. The candidate
and observers execute inside the node against its local Kubernetes API, system
bus and kernel counters. Each run removes only its own generated resources.
"""
import argparse
import hashlib
import json
import pathlib
import re
import subprocess
import uuid


def validate_node(name, row):
    cluster = row.get('Config', {}).get('Labels', {}).get('io.x-k8s.kind.cluster', '')
    if not re.fullmatch(r'cpra-[a-z0-9-]+', name) or not cluster.startswith('cpra-'):
        raise ValueError('requires a dedicated cpra-prefixed kind node and cluster')
    if row.get('Name') != '/' + name or not row.get('State', {}).get('Running'):
        raise ValueError('designated kind node is not running')


def build_config(prefix, directory):
    monitors, cases = [], []
    unit = prefix + '.service'
    for kind, driver in [('pulse', 'icmp'), ('intervention', 'kubernetes'), ('intervention', 'systemd')]:
        identity = kind + '-' + driver
        monitor = {'id': identity, 'name': identity, 'enabled': True,
                   'pulse_check': {'type': 'icmp', 'interval': '60s', 'timeout': '5s',
                                   'config': {'host': '127.0.0.1', 'count': 1}}}
        if driver == 'kubernetes':
            monitor['intervention'] = {'action': driver, 'max_failures': 1,
                'target': {'type': driver, 'kubeconfig_path': '/etc/kubernetes/admin.conf',
                           'namespace': prefix, 'kind': 'deployment', 'name': prefix, 'timeout': '30s'}}
            observer = ['python3', directory + '/observe_effect.py', driver, 'deployment/' + prefix, '--namespace', prefix]
        elif driver == 'systemd':
            monitor['intervention'] = {'action': driver, 'max_failures': 1,
                'target': {'type': driver, 'unit': unit, 'mode': 'replace', 'timeout': '30s'}}
            observer = ['python3', directory + '/observe_effect.py', driver, unit]
        else:
            observer = ['python3', directory + '/observe_icmp.py']
        monitors.append(monitor)
        cases.append({'kind': kind, 'driver': driver, 'configured': True, 'monitor_id': identity,
                      'evidence_type': 'local_integration', 'observation_boundary': 'effect',
                      'timeout': '120s', 'observer': observer})
    return {'manifest': {'monitors': monitors}, 'cases': cases}


def deployment(prefix):
    return {'apiVersion': 'apps/v1', 'kind': 'Deployment',
            'metadata': {'name': prefix, 'namespace': prefix},
            'spec': {'replicas': 1, 'selector': {'matchLabels': {'app': prefix}},
                     'template': {'metadata': {'labels': {'app': prefix}},
                                  'spec': {'terminationGracePeriodSeconds': 1,
                                           'containers': [{'name': 'fixture', 'image': 'ubuntu:latest',
                                                           'imagePullPolicy': 'Never', 'command': ['sleep', '3600']}]}}}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--out', required=True)
    parser.add_argument('--node', required=True, help='Existing disposable cpra-prefixed kind node; does not use the host kubectl context')
    args = parser.parse_args()
    out = pathlib.Path(args.out).resolve()
    out.parent.mkdir(parents=True, exist_ok=True)
    progress = pathlib.Path(str(out) + '.fixture.json')
    if out.exists() or progress.exists():
        parser.error('output already exists; select a fresh path to preserve previous evidence')
    binary = pathlib.Path(args.binary).resolve(strict=True)
    root = pathlib.Path(__file__).resolve().parent
    prefix = 'cpra-local-' + uuid.uuid4().hex[:12]
    # kind's systemd can mount a private /tmp invisible to Docker's copy API.
    directory = '/root/' + prefix
    unit = prefix + '.service'
    unit_path = '/run/systemd/system/' + unit
    report = directory + '/report.json'
    with binary.open('rb') as source:
        binary_digest = hashlib.file_digest(source, 'sha256').hexdigest()
    state = {'binary_sha256': binary_digest, 'status': 'running', 'node': args.node, 'resource_prefix': prefix,
             'boundary': 'actual local Kubernetes API, systemd D-Bus and kernel ICMP', 'steps': [], 'cleanup_failures': []}

    def save():
        temporary = progress.with_suffix(progress.suffix + '.tmp')
        temporary.write_text(json.dumps(state, indent=2) + '\n')
        temporary.replace(progress)

    def command(argv, timeout=60, check=True, input=None):
        return subprocess.run(argv, input=input, text=True, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, timeout=timeout, check=check)

    def node(argv, **kw):
        return command(['docker', 'exec', '-i', '-e', 'KUBECONFIG=/etc/kubernetes/admin.conf', args.node] + argv, **kw)

    def write(path, text):
        node(['python3', '-c', 'import pathlib,sys; pathlib.Path(sys.argv[1]).write_text(sys.stdin.read())', path], input=text)

    def step(name):
        state['steps'].append(name)
        save()

    verified_node = False
    previous_ping = None
    unit_created = False
    bus_started = False
    bus_socket_active = False
    namespace_created = False
    try:
        save()
        row = json.loads(command(['docker', 'inspect', args.node]).stdout)[0]
        validate_node(args.node, row)
        verified_node = True
        state['node_image'] = row['Config']['Image']
        state['kubernetes_version'] = json.loads(node(['kubectl', 'version', '-o', 'json']).stdout)['serverVersion']['gitVersion']
        state['systemd_version'] = node(['systemctl', '--version']).stdout.splitlines()[0]
        images = json.loads(node(['crictl', 'images', '--output', 'json']).stdout)['images']
        state['fixture_images'] = [{'id': image['id'], 'tags': image.get('repoTags', [])}
                                  for image in images if 'docker.io/library/ubuntu:latest' in image.get('repoTags', [])]
        if not state['fixture_images']:
            raise RuntimeError('ubuntu:latest must be preloaded into the designated kind node')
        node(['mkdir', directory])
        for source, destination in [(binary, 'cpra-verify'), (root / 'observe_effect.py', 'observe_effect.py'),
                                    (root / 'observe_icmp.py', 'observe_icmp.py')]:
            command(['docker', 'cp', str(source), args.node + ':' + directory + '/' + destination])
        node(['chmod', '700', directory + '/cpra-verify'])
        step('copied_candidate_and_observers')
        previous_ping = node(['cat', '/proc/sys/net/ipv4/ping_group_range']).stdout.strip()
        write('/proc/sys/net/ipv4/ping_group_range', '0 2147483647\n')
        step('enabled_unprivileged_icmp_in_node')
        bus_socket_active = node(['systemctl', 'is-active', '--quiet', 'dbus.socket'], check=False).returncode == 0
        if node(['systemctl', 'is-active', '--quiet', 'dbus.service'], check=False).returncode != 0:
            bus_started = True
            node(['systemctl', 'start', 'dbus.service'])
        step('system_bus_ready')
        write(unit_path, '[Unit]\nDescription=CPRa disposable verification unit\n[Service]\nExecStart=/usr/bin/sleep 3600\nTimeoutStopSec=2\n')
        unit_created = True
        node(['systemctl', 'daemon-reload'])
        node(['systemctl', 'start', unit])
        node(['systemctl', 'is-active', unit])
        step('started_dedicated_unit')
        namespace_created = True
        node(['kubectl', 'create', 'namespace', prefix])
        node(['kubectl', 'apply', '-f', '-'], input=json.dumps(deployment(prefix)))
        node(['kubectl', '-n', prefix, 'rollout', 'status', 'deployment/' + prefix, '--timeout=120s'], timeout=130)
        step('deployment_ready')
        write(directory + '/config.yaml', json.dumps(build_config(prefix, directory)))
        run = node([directory + '/cpra-verify', '-live', '-require-configured-pass', '-config',
                    directory + '/config.yaml', '-out', report], timeout=400, check=False)
        state['runner_exit_code'] = run.returncode
        copied = command(['docker', 'cp', args.node + ':' + report, str(out)], check=False)
        if copied.returncode != 0:
            raise RuntimeError('verification did not produce a report')
        evidence = command(['docker', 'cp', args.node + ':' + report + '.evidence', str(out) + '.evidence'], check=False)
        state['evidence_copied'] = evidence.returncode == 0
        rows = json.loads(out.read_text())['records']
        wanted = {('pulse', 'icmp'), ('intervention', 'kubernetes'), ('intervention', 'systemd')}
        results = [r for r in rows if (r['kind'], r['driver']) in wanted]
        passed = len(results) == 3 and all(r['status'] == 'pass' and r['accepted'] and r['observed']
                                          and r['evidence_type'] == 'local_integration' for r in results)
        state['status'] = 'pass' if run.returncode == 0 and passed and evidence.returncode == 0 else 'fail'
        step('verification_completed')
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as exc:
        state['status'] = 'fail'
        state['reason'] = type(exc).__name__ + ': fixture setup or execution failed'
        if isinstance(exc, subprocess.CalledProcessError):
            state['failed_command'] = exc.cmd
            state['command_exit_code'] = exc.returncode
            state['command_error'] = (exc.stderr or '')[-4000:]
        save()
    finally:
        if verified_node:
            cleanup = []
            if namespace_created:
                cleanup.append(['kubectl', 'delete', 'namespace', prefix, '--ignore-not-found=true', '--wait=true', '--timeout=90s'])
            if unit_created:
                cleanup.extend([['systemctl', 'stop', unit], ['rm', '-f', unit_path], ['systemctl', 'daemon-reload']])
            if previous_ping is not None:
                try:
                    write('/proc/sys/net/ipv4/ping_group_range', previous_ping + '\n')
                except (OSError, subprocess.SubprocessError):
                    state['cleanup_failures'].append('restore_node_ping_group_range')
            if bus_started:
                cleanup.append(['systemctl', 'stop', 'dbus.service'])
                if not bus_socket_active:
                    cleanup.append(['systemctl', 'stop', 'dbus.socket'])
            cleanup.append(['rm', '-rf', directory])
            for action in cleanup:
                try:
                    result = node(action, timeout=100, check=False)
                    if result.returncode:
                        state['cleanup_failures'].append(action)
                except (OSError, subprocess.SubprocessError):
                    state['cleanup_failures'].append(action)
        if state['cleanup_failures']:
            state['status'] = 'fail'
        save()
    print(json.dumps({'status': state['status'], 'report': str(out), 'fixture': str(progress)}))
    return 0 if state['status'] == 'pass' else 1


if __name__ == '__main__':
    raise SystemExit(main())
