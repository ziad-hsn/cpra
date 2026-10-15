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
sys.path.insert(0, str(ROOT / 'scripts/release'))
from release import check_manifest, evidence_identity


def source_evidence(manifest, diagnostic=False):
    """Keep pre-freeze executions separate from frozen release qualification."""
    if diagnostic:
        if manifest.get('candidate') is not True or manifest.get('source_files') != {}:
            raise ValueError('diagnostic runs require explicitly prepared candidate metadata')
        check_manifest(manifest)
        return {'evidence_class': 'working_tree_container', 'release_qualified': False,
                'release_gate_passed': False,
                'scope': 'private pre-freeze Compose execution; not release qualification'}
    return {'evidence_class': 'frozen_container', 'release_qualified': False,
            'release_gate_passed': False, 'release_identity': evidence_identity(manifest)}


def read_observation(port, token, path):
    """Read a bounded response from the manifest-mode fixture directly."""
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=2)
    try:
        connection.request('GET', path, headers={'Authorization': 'Bearer ' + token})
        response = connection.getresponse()
        body = response.read((2 << 20) + 1)
        if response.status != 200 or len(body) > 2 << 20:
            raise RuntimeError('fixture observation was unavailable or oversized')
        return json.loads(body)
    except (OSError, http.client.HTTPException, ValueError, KeyError, TypeError, AttributeError):
        raise RuntimeError('authenticated fixture observation read failed') from None
    finally:
        connection.close()


def read_history(port, token):
    page = read_observation(port, token, '/api/v1/history?monitor_id=packaging-fixture&limit=100')
    events = page.get('events') if isinstance(page, dict) else None
    if not isinstance(events, list) or any(not isinstance(event, dict)
            or event.get('monitor_id') != 'packaging-fixture' for event in events):
        raise RuntimeError('fixture history contained an unexpected monitor')
    return events


def healthy_monitor(monitor):
    if not isinstance(monitor, dict) or monitor.get('monitor_id') != 'packaging-fixture' \
            or monitor.get('status') != 'up' or monitor.get('incident') is not False:
        return False
    try:
        success = datetime.fromisoformat(monitor.get('last_success', '').replace('Z', '+00:00'))
        return success.year > 1 and success.utcoffset() is not None
    except (ValueError, TypeError, AttributeError):
        return False


def new_incident(events, baseline):
    """An earlier incident or unrelated action cannot prove the induced outage."""
    event_ids = {event['id'] for event in baseline}
    incident_ids = {event.get('incident_id') for event in baseline}
    return next((event for event in events if event.get('monitor_id') == 'packaging-fixture'
                 and event.get('type') == 'incident_opened'
                 and isinstance(event.get('id'), str) and event['id'].strip()
                 and isinstance(event.get('incident_id'), str) and event['incident_id'].strip()
                 and event['id'] not in event_ids and event['incident_id'] not in incident_ids), None)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--target-image', default='busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0')
    parser.add_argument('--release-dir', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--diagnostic', action='store_true',
                        help='execute private candidate artifacts without qualifying a frozen release')
    a = parser.parse_args()
    if a.out.exists():
        parser.error('evidence exists; use a new path to preserve failed attempts')
    a.out.parent.mkdir(parents=True, exist_ok=True)
    name = 'cpra-compose-' + secrets.token_hex(4)
    volume = name + '-data'
    rotation_volume = name + '-tokens'
    rotation_created = False
    token_values = [secrets.token_urlsafe(32), secrets.token_urlsafe(32)]
    report = {'started': datetime.now(timezone.utc).isoformat(), 'image': a.image,
              'target_image': a.target_image, 'project': name, 'status': 'incomplete', 'checks': [],
              'source': json.loads((a.release_dir/'RELEASE.json').read_text())}
    report.update(source_evidence(report['source'], a.diagnostic))

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
        runtime = tmp/'runtime.yaml'
        runtime.write_bytes((ROOT/'docker/runtime.yaml').read_bytes())
        runtime.chmod(0o444)
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
               'CPRA_RUNTIME_FILE': str(runtime),
               'CPRA_TOKEN_FILE': str(token_files[0]), 'CPRA_DATA_VOLUME': volume, 'CPRA_PORT': '0'}
        base = ['docker', 'compose', '-p', name, '-f', str(ROOT/'docker/docker-compose.yml'), '-f', str(override)]

        def compose(*args, **kw):
            return run(base + list(args), env=env, **kw)

        def cid():
            return compose('ps', '-q', 'cpra').stdout.strip()

        def ctl(*args, check=True):
            return run(['docker', 'exec', cid(), '/usr/local/bin/cpractl', *args, '--server', 'http://127.0.0.1:8060', '--allow-insecure-http',
                        '--token-file', '/run/secrets/cpra_api_token', '--request-timeout', '2s'], check=check, timeout=15)

        def api_port():
            bindings = json.loads(run(['docker', 'inspect', cid()], timeout=15).stdout)[0]['NetworkSettings']['Ports']['8060/tcp']
            if len(bindings) != 1 or bindings[0]['HostIp'] != '127.0.0.1':
                raise RuntimeError('fixture API must have one loopback port binding')
            port = int(bindings[0]['HostPort'])
            if not 0 < port < 65536:
                raise RuntimeError('fixture API port is invalid')
            return port

        def history():
            token = Path(env['CPRA_TOKEN_FILE']).read_text().strip()
            return read_history(api_port(), token)

        def healthy():
            token = Path(env['CPRA_TOKEN_FILE']).read_text().strip()
            page = read_observation(api_port(), token, '/api/v1/monitors?size=1')
            monitors = page.get('monitors') if isinstance(page, dict) else None
            if not isinstance(monitors, list) or len(monitors) != 1 or page.get('total') != 1:
                return False
            return healthy_monitor(monitors[0])

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
            until(healthy, 'a completed healthy check before the induced outage')
            baseline = history()
            run(['docker', 'exec', target, 'rm', '/tmp/www/index.html'])
            opened = until(lambda: new_incident(history(), baseline), 'a new incident for the induced outage')
            ids = {opened['id']}
            ctl('health')
            record('real_http_outage_records_incident_without_liveness_failure', event_ids=sorted(ids),
                   healthy_before_outage=True, baseline_event_ids=sorted(event['id'] for event in baseline),
                   baseline_incident_ids=sorted({event['incident_id'] for event in baseline if event.get('incident_id')}),
                   opened_incident={key: opened[key] for key in ('id', 'incident_id', 'monitor_id', 'type')})
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
            # Startup files cannot overwrite committed access. Rotate using the
            # actual stopped-store CLI and a separate private token volume.
            compose('stop', 'cpra', timeout=75)
            run(['docker', 'volume', 'create', rotation_volume])
            rotation_created = True
            run(['docker', 'run', '--rm', '--network', 'none', '--user', '0:0',
                 '--read-only', '--volume', rotation_volume+':/out', '--entrypoint', '/bin/sh',
                 a.image, '-c', 'mkdir -m 700 /out/tokens && chown 1001:1001 /out/tokens'])
            rotated = json.loads(run(['docker', 'run', '--rm', '--network', 'none', '--user', '1001:1001',
                '--read-only', '--tmpfs', '/tmp:rw,size=64m,uid=1001,gid=1001,mode=0700',
                '--volume', volume+':/var/lib/cpra', '--volume', rotation_volume+':/out',
                '--entrypoint', '/usr/local/bin/cpractl', a.image,
                'local', 'auth', 'rotate-read-token', '--data-dir', '/var/lib/cpra',
                '--token-output', '/out/tokens/api.token']).stdout)
            if rotated.get('outcome') != 'committed' or not rotated.get('revision') \
                    or rotated['revision'] != rotated.get('intendedRevision'):
                raise RuntimeError('token rotation was not confirmed; retain the original attempt')
            token = run(['docker', 'run', '--rm', '--network', 'none', '--user', '1001:1001',
                         '--read-only', '--volume', rotation_volume+':/out:ro',
                         '--entrypoint', '/bin/cat', a.image, '/out/tokens/api.token']).stdout.strip()
            if not token or token in token_values:
                raise RuntimeError('rotation did not generate a fresh token')
            token_values.append(token)
            token_files[1].chmod(0o600)
            token_files[1].write_text(token)
            token_files[1].chmod(0o444)
            env['CPRA_TOKEN_FILE'] = str(token_files[1])
            compose('up', '-d', '--force-recreate', '--wait', '--wait-timeout', '120', 'cpra', timeout=150)
            ctl('ready')
            assert ids <= {event['id'] for event in history()}
            connection = http.client.HTTPConnection('127.0.0.1', api_port(), timeout=2)
            try:
                connection.request('GET', '/api/v2/readyz', headers={'Authorization': 'Bearer '+token_values[0]})
                response = connection.getresponse()
                if response.status != 401:
                    raise RuntimeError('rotated service accepted the old token')
                response.read(1 << 20)
            finally:
                connection.close()
            record('versioned_token_file_rotation', committed_revision=rotated['revision'], old_token_status=401)
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
            if rotation_created:
                token_cleanup = run(['docker', 'volume', 'rm', rotation_volume], check=False)
                if token_cleanup.returncode:
                    report['status'] = 'fail'
                    report['rotation_cleanup'] = token_cleanup.stderr
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
