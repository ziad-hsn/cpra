#!/usr/bin/env python3
"""Verify an actual local Mattermost channel receipt using a disposable server."""
import argparse
import hashlib
import json
import os
import pathlib
import secrets
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--out', required=True)
    parser.add_argument('--image', default='mattermost/mattermost-preview:latest')
    args = parser.parse_args()
    binary, output = pathlib.Path(args.binary).resolve(), pathlib.Path(args.out).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    image = json.loads(subprocess.check_output(['docker', 'image', 'inspect', args.image]))[0]
    name = 'cpra-mattermost-' + uuid.uuid4().hex[:12]
    subprocess.run(['docker', 'run', '-d', '--pull=never', '--name', name,
                    '--label', 'cpra.verification=disposable', '-p', '127.0.0.1::8065',
                    '-e', 'MM_EMAILSETTINGS_SENDEMAILNOTIFICATIONS=false',
                    '-e', 'MM_SERVICESETTINGS_ENABLEINCOMINGWEBHOOKS=true', args.image],
                   check=True, stdout=subprocess.DEVNULL)
    try:
        binding = json.loads(subprocess.check_output(['docker', 'inspect', name]))[0]['NetworkSettings']['Ports']['8065/tcp'][0]
        base = 'http://127.0.0.1:' + binding['HostPort']
        token = ''

        def api(path, body=None):
            request = urllib.request.Request(base + '/api/v4' + path,
                      data=None if body is None else json.dumps(body).encode(),
                      headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
            with urllib.request.urlopen(request, timeout=10) as response:
                return json.load(response), response.headers

        deadline = time.monotonic() + 180
        while True:
            try:
                status, _ = api('/system/ping')
                if status.get('status') == 'OK':
                    break
            except (OSError, urllib.error.HTTPError):
                pass
            if time.monotonic() >= deadline:
                raise RuntimeError('local Mattermost did not become ready in 180 seconds')
            time.sleep(1)
        password = 'CPRa-fixture-' + secrets.token_hex(16)
        api('/users', {'email': 'cpra@example.test', 'username': 'cpra', 'password': password})
        _, headers = api('/users/login', {'login_id': 'cpra', 'password': password})
        token = headers['Token']
        team, _ = api('/teams', {'name': 'cpra', 'display_name': 'CPRa fixture', 'type': 'O'})
        channel, _ = api('/channels', {'team_id': team['id'], 'name': 'verification',
                                     'display_name': 'Verification', 'type': 'O'})
        hook, _ = api('/hooks/incoming', {'channel_id': channel['id'], 'display_name': 'CPRa verification'})
        with tempfile.TemporaryDirectory(prefix='cpra-mattermost-') as directory:
            private = pathlib.Path(directory)
            credentials = private / 'observer-credentials.json'
            credentials.write_text(json.dumps({'base': base, 'token': token, 'channel': channel['id']}))
            credentials.chmod(0o600)
            observer = private / 'observer.py'
            observer.write_text('''import json, os, sys, urllib.request
with open(sys.argv[1]) as f: cfg=json.load(f)
req=urllib.request.Request(cfg['base']+'/api/v4/channels/'+cfg['channel']+'/posts?per_page=100',headers={'Authorization':'Bearer '+cfg['token']})
with urllib.request.urlopen(req,timeout=5) as r: posts=json.load(r)['posts']
count=sum(os.environ['CPRA_VERIFY_RUN_ID'] in p['message'] for p in posts.values())
row={'count':count}
if os.environ['CPRA_VERIFY_PHASE']=='after':
    before=json.load(sys.stdin)
    row['observed']=count==before['count']+1
print(json.dumps(row))
''')
            scenarios = []
            for scenario, hook_id in [('success', hook['id']), ('invalid-webhook', 'invalid-' + uuid.uuid4().hex)]:
                monitor = {'id': 'mattermost', 'name': 'mattermost', 'pulse_check': {'type': 'http',
                           'interval': '60s', 'timeout': '5s', 'config': {'url': base}},
                           'codes': {'red': {'dispatch': True, 'notify': 'mattermost',
                           'config': {'webhook_url': base + '/hooks/' + hook_id}}}}
                case = {'kind': 'code', 'driver': 'mattermost', 'configured': True, 'monitor_id': 'mattermost',
                        'color': 'red', 'evidence_type': 'local_integration',
                        'observer': [sys.executable, str(observer), str(credentials)]}
                config = private / 'config.json'
                config.write_text(json.dumps({'manifest': {'monitors': [monitor]}, 'cases': [case]}))
                config.chmod(0o600)
                path = output if scenario == 'success' else output.with_name(output.stem + '-invalid-webhook.json')
                result = subprocess.run([str(binary), '-live', '-config', str(config), '-out', str(path)],
                                        timeout=30, stdout=subprocess.DEVNULL, check=False)
                if result.returncode not in (0, 2):
                    raise RuntimeError('verification invocation failed')
                report = json.loads(path.read_text())
                row = next(r for r in report['records'] if r['kind'] == 'code' and r['driver'] == 'mattermost')
                expected = 'pass' if scenario == 'success' else 'fail'
                passed = (row['status'] == expected and row['accepted'] == (scenario == 'success')
                          and row['operation_invoked'] and row['evidence_type'] == 'local_integration'
                          and not report['all_providers_verified'])
                if scenario == 'invalid-webhook':
                    passed = passed and row.get('http_status') in (400, 404)
                scenarios.append({'scenario': scenario, 'passed': passed, 'report': path.name})
            summary = {'status': 'pass' if all(s['passed'] for s in scenarios) else 'fail',
                       'evidence_type': 'local_integration', 'scenarios': scenarios,
                       'image_id': image['Id'], 'image_digests': image.get('RepoDigests', []),
                       'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest()}
            output.with_suffix('.suite.json').write_text(json.dumps(summary, indent=2) + '\n')
            return 0 if summary['status'] == 'pass' else 1
    finally:
        subprocess.run(['docker', 'rm', '-f', '-v', name], check=True, stdout=subprocess.DEVNULL)


if __name__ == '__main__':
    raise SystemExit(main())
