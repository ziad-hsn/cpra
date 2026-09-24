#!/usr/bin/env python3
"""Run the actual Compose definition using only disposable cpra-* resources.

Requires a local candidate image, Docker Compose, and staged exact release binaries.
A real HTTP target generates incident events. The suite checks image binary hashes,
non-root/read-only operation, graceful exit, restart recovery, directory locking,
API-token rotation, and named-volume survival across down/up. It deletes only its
own fixture resources. Evidence files cannot overwrite previous attempts.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import http.client
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import time

import yaml

ROOT = Path(__file__).resolve().parents[2]


def read_history(port, token):
    """Read this manifest-mode fixture directly; no CLI compatibility client."""
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=2)
    try:
        connection.request('GET', '/api/v1/history?monitor_id=packaging-fixture&limit=100',
                           headers={'Authorization': 'Bearer ' + token})
        response = connection.getresponse()
        body = response.read((2 << 20) + 1)
        if response.status != 200 or len(body) > 2 << 20:
            raise RuntimeError('fixture history response was unavailable or oversized')
        events = json.loads(body)['events']
        if not isinstance(events, list) or any(event.get('monitor_id') != 'packaging-fixture' for event in events):
            raise RuntimeError('fixture history contained an unexpected monitor')
        return events
    except (OSError, http.client.HTTPException, ValueError, KeyError, TypeError, AttributeError):
        raise RuntimeError('authenticated fixture history read failed') from None
    finally:
        connection.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--target-image', default='busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0')
    parser.add_argument('--release-dir', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    a = parser.parse_args()
    if a.out.exists():
        parser.error('evidence exists; use a new path to preserve failed attempts')
    a.out.parent.mkdir(parents=True, exist_ok=True)
    name = 'cpra-compose-' + secrets.token_hex(4)
    volume = name + '-data'
    token_values = [secrets.token_urlsafe(32), secrets.token_urlsafe(32)]
    report = {'started': datetime.now(timezone.utc).isoformat(), 'image': a.image,
              'target_image': a.target_image, 'project': name, 'status': 'incomplete', 'checks': [],
              'source': json.loads((a.release_dir/'RELEASE.json').read_text())}

    def run(cmd, check=True, timeout=120, env=None):
        r = subprocess.run(cmd, text=True, capture_output=True, timeout=timeout, env=env)
        if check and r.returncode:
            raise RuntimeError(f'{cmd[0]} {cmd[1]} failed ({r.returncode}): {(r.stdout+r.stderr)[-5000:]}')
        return r

    def record(step, **details):
        report['checks'].append({'name': step, 'status': 'pass', **details})
        a.out.write_text(json.dumps(report, indent=2) + '\n')
        print(step + ': pass', flush=True)

    def until(fn, description, seconds=90):
        end = time.monotonic() + seconds
        last = None
        while time.monotonic() < end:
            try:
                value = fn()
                if value:
                    return value
            except (ValueError, KeyError, RuntimeError) as exc:
                last = str(exc)
            time.sleep(1)
        raise RuntimeError(f'timeout waiting for {description}: {last}')

    with tempfile.TemporaryDirectory(prefix=name) as tmp:
        tmp = Path(tmp)
        manifest = tmp/'monitors.yaml'
        manifest.write_text(yaml.safe_dump({'monitors': [{'id': 'packaging-fixture', 'name': 'packaging-fixture', 'enabled': True,
            'pulse_check': {'type': 'http', 'interval': '1s', 'timeout': '1s', 'unhealthy_threshold': 1, 'healthy_threshold': 1,
                            'config': {'url': 'http://target:8080/'}},
            'codes': {'red': {'dispatch': True, 'notify': 'log', 'config': {'file': '/var/lib/cpra/events.jsonl'}},
                      'green': {'dispatch': True, 'notify': 'log', 'config': {'file': '/var/lib/cpra/events.jsonl'}}}}]}))
        manifest.chmod(0o444)
        token_files = [tmp/'token-v1', tmp/'token-v2']
        for path, token in zip(token_files, token_values):
            path.write_text(token)
            # Disposable random fixture credentials; parent directory remains 0700.
            path.chmod(0o444)
        override = tmp/'override.yaml'
        override.write_text(yaml.safe_dump({'services': {
            'cpra': {'healthcheck': {'interval': '2s', 'start_period': '60s'}},
            'target': {'image': a.target_image, 'user': '1001:1001', 'entrypoint': ['/bin/sh', '-c'],
                       'command': ['mkdir -p /tmp/www; echo healthy > /tmp/www/index.html; exec httpd -f -p 8080 -h /tmp/www'],
                       'read_only': True, 'tmpfs': ['/tmp:rw,size=16m,uid=1001,gid=1001'],
                       'cap_drop': ['ALL'], 'security_opt': ['no-new-privileges:true']}}}))
        env = {**os.environ, 'CPRA_IMAGE': a.image, 'CPRA_MANIFEST_FILE': str(manifest),
               'CPRA_TOKEN_FILE': str(token_files[0]), 'CPRA_DATA_VOLUME': volume, 'CPRA_PORT': '0'}
        base = ['docker', 'compose', '-p', name, '-f', str(ROOT/'docker/docker-compose.yml'), '-f', str(override)]

        def compose(*args, **kw):
            return run(base + list(args), env=env, **kw)

        def cid():
            return compose('ps', '-q', 'cpra').stdout.strip()

        def ctl(*args, check=True):
            return run(['docker', 'exec', cid(), '/usr/local/bin/cpractl', *args, '--server', 'http://127.0.0.1:8060', '--allow-insecure-http',
                        '--token-file', '/run/secrets/cpra_api_token', '--request-timeout', '2s'], check=check, timeout=15)

        def history():
            bindings = json.loads(run(['docker', 'inspect', cid()], timeout=15).stdout)[0]['NetworkSettings']['Ports']['8060/tcp']
            if len(bindings) != 1 or bindings[0]['HostIp'] != '127.0.0.1':
                raise RuntimeError('fixture API must have one loopback port binding')
            port = int(bindings[0]['HostPort'])
            if not 0 < port < 65536:
                raise RuntimeError('fixture API port is invalid')
            token = Path(env['CPRA_TOKEN_FILE']).read_text().strip()
            return read_history(port, token)

        try:
            report['docker_server_version'] = run(['docker', 'version', '--format', '{{.Server.Version}}']).stdout.strip()
            report['compose_version'] = run(['docker', 'compose', 'version', '--short']).stdout.strip()
            image_details = json.loads(run(['docker', 'image', 'inspect', a.image]).stdout)[0]
            report['platform'] = image_details['Os'] + '/' + image_details['Architecture']
            compose('up', '-d', '--wait', '--wait-timeout', '120', timeout=150)
            inspect = json.loads(run(['docker', 'inspect', cid()]).stdout)[0]
            assert inspect['Config']['User'] == '1001:1001'
            assert inspect['HostConfig']['ReadonlyRootfs']
            assert inspect['HostConfig']['CapDrop'] == ['ALL']
            assert inspect['State']['Health']['Status'] == 'healthy'
            report['state_filesystem'] = run(['docker', 'exec', cid(), 'stat', '-f', '-c', '%T', '/var/lib/cpra']).stdout.strip()
            report['kernel'] = run(['docker', 'exec', cid(), 'uname', '-srvm']).stdout.strip()
            source = json.loads(run(['docker', 'exec', cid(), 'cat', '/usr/share/cpra/RELEASE.json']).stdout)
            assert source == report['source'], 'image source identity differs from staged release metadata'
            hashes = {}
            for binary in ('cpra', 'cpractl'):
                image_hash = run(['docker', 'exec', cid(), 'sha256sum', '/usr/local/bin/'+binary]).stdout.split()[0]
                assert image_hash == hashlib.sha256((a.release_dir/binary).read_bytes()).hexdigest()
                hashes[binary] = image_hash
            record('exact_release_binary_hashes_and_hardened_runtime', image_id=inspect['Image'], binary_sha256=hashes)
            target = compose('ps', '-q', 'target').stdout.strip()
            run(['docker', 'exec', target, 'rm', '/tmp/www/index.html'])
            events = until(lambda: history(), 'incident events')
            ids = {event['id'] for event in events}
            ctl('health')
            record('real_http_outage_records_incident_without_liveness_failure', event_ids=sorted(ids))
            identity = run(['docker', 'exec', cid(), 'cat', '/var/lib/cpra/identity.json']).stdout
            duplicate = compose('run', '--name', name+'-duplicate', '--no-deps', '--rm', 'cpra', check=False, timeout=20)
            assert duplicate.returncode != 0 and any(x in (duplicate.stdout+duplicate.stderr).lower() for x in ('locked', 'lock', 'timeout'))
            record('second_owner_rejected')
            old = cid()
            compose('stop', 'cpra', timeout=75)
            stopped = json.loads(run(['docker', 'inspect', old]).stdout)[0]['State']
            assert stopped['ExitCode'] == 0 and not stopped['OOMKilled'], stopped
            record('sigterm_drains_within_supervisor_budget')
            compose('up', '-d', '--wait', '--wait-timeout', '120', timeout=150)
            assert ids <= {event['id'] for event in history()}
            assert run(['docker', 'exec', cid(), 'cat', '/var/lib/cpra/identity.json']).stdout == identity
            record('restart_recovers_identity_and_history')
            env['CPRA_TOKEN_FILE'] = str(token_files[1])
            compose('up', '-d', '--force-recreate', '--wait', '--wait-timeout', '120', 'cpra', timeout=150)
            ctl('ready')
            assert ids <= {event['id'] for event in history()}
            record('versioned_token_file_rotation')
            compose('down', timeout=90)
            run(['docker', 'volume', 'inspect', volume])
            compose('up', '-d', '--wait', '--wait-timeout', '120', timeout=150)
            assert ids <= {event['id'] for event in history()}
            assert run(['docker', 'exec', cid(), 'cat', '/var/lib/cpra/identity.json']).stdout == identity
            record('down_up_retains_named_volume')
            report['status'] = 'pass'
        except Exception as exc:
            report['status'] = 'fail'
            report['error'] = str(exc)
            report['logs'] = compose('logs', '--no-color', '--tail', '100', check=False).stdout
        finally:
            run(['docker', 'rm', '-f', name+'-duplicate'], check=False)
            cleanup = compose('down', '--volumes', '--remove-orphans', check=False, timeout=90)
            report['cleanup'] = 'pass' if cleanup.returncode == 0 else cleanup.stderr
            if cleanup.returncode:
                report['status'] = 'fail'
    report['finished'] = datetime.now(timezone.utc).isoformat()
    rendered = json.dumps(report, indent=2)
    for token in token_values:
        rendered = rendered.replace(token, '[REDACTED]')
    a.out.write_text(rendered+'\n')
    print(str(a.out)+': '+report['status'])
    return 0 if report['status'] == 'pass' else 1


if __name__ == '__main__':
    sys.exit(main())
