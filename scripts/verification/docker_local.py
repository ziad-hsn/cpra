#!/usr/bin/env python3
"""Verify production Docker check/restart against one uniquely owned container.

Requires an already installed image. Never pulls or modifies existing services.
"""
import argparse
import json
import pathlib
import subprocess
import sys
import tempfile
import uuid

import fixture_preflight


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--binary', required=True)
    p.add_argument('--out', required=True)
    p.add_argument('--image', default='ubuntu:latest')
    args = p.parse_args()
    output = fixture_preflight.output_path(args.out)
    preflight = fixture_preflight.inspect('docker')
    if not preflight['ready']:
        fixture_preflight.write_new(output, {'status': 'not_verified', 'preflight': preflight})
        return 2
    image = json.loads(subprocess.check_output(['docker', 'image', 'inspect', args.image], timeout=15))[0]
    name = 'cpra-verification-' + uuid.uuid4().hex[:12]
    environment = {'driver': 'docker', 'preflight': preflight, 'container_name': name,
                   'image_id': image['Id'], 'image_digests': image.get('RepoDigests', []),
                   'cleanup_confirmed': False}
    try:
        subprocess.run(['docker', 'run', '-d', '--init', '--pull=never', '--name', name,
                        '--label', 'cpra.verification=disposable', '--read-only',
                        '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m', '--memory', '128m',
                        '--memory-swap', '128m', '--cpus', '0.5', '--pids-limit', '64',
                        '--log-driver', 'json-file', '--log-opt', 'max-size=1m',
                        '--log-opt', 'max-file=2', image['Id'], 'sleep', '600'],
                       check=True, stdout=subprocess.DEVNULL, timeout=30)
        container = json.loads(subprocess.check_output(['docker', 'inspect', name], timeout=15))[0]
        settings = container['HostConfig']
        environment['container_id'] = container['Id']
        environment['runtime_limits'] = {key: settings[key] for key in
                                        ('Memory', 'MemorySwap', 'NanoCpus', 'PidsLimit', 'ReadonlyRootfs', 'Tmpfs', 'LogConfig')}
        with tempfile.TemporaryDirectory(prefix='cpra-docker-live-') as directory:
            observer = str(pathlib.Path(__file__).with_name('observe_effect.py').resolve())
            monitor = {'id': 'docker-fixture', 'name': 'docker-fixture', 'pulse_check': {'type': 'docker', 'interval': '60s', 'timeout': '5s', 'config': {'container': name}},
                       'intervention': {'action': 'docker', 'target': {'type': 'docker', 'container': name}}}
            # A separate daemon inspector checks health, and a changed StartedAt
            # plus running state confirms the actual recovery effect.
            health = str(pathlib.Path(__file__).with_name('observe_docker_health.py').resolve())
            cases = [{'kind': kind, 'driver': 'docker', 'configured': True, 'evidence_type': 'local_integration', 'monitor_id': monitor['id'],
                      'observer': [sys.executable, health, name] if kind == 'pulse' else [sys.executable, observer, 'docker', name]}
                     for kind in ['pulse', 'intervention']]
            config = pathlib.Path(directory) / 'config.yaml'
            config.write_text(json.dumps({'manifest': {'monitors': [monitor]}, 'cases': cases}))
            result = subprocess.run([str(pathlib.Path(args.binary).resolve()), '-config', str(config), '-live', '-out', str(output)], timeout=180)
            if result.returncode not in (0, 2):
                return result.returncode
            report = json.loads(pathlib.Path(args.out).read_text())
            configured = [r for r in report['records'] if r['status'] != 'not_configured']
            return 0 if len(configured) == 2 and all(r['status'] == 'pass' for r in configured) else 1
    finally:
        try:
            subprocess.run(['docker', 'rm', '-f', name], check=True, stdout=subprocess.DEVNULL, timeout=30)
            environment['cleanup_confirmed'] = True
        finally:
            fixture_preflight.write_new(str(output) + '.environment.json', environment)


if __name__ == '__main__':
    raise SystemExit(main())
