#!/usr/bin/env python3
"""Exercise the production EC2 driver against Moto over real HTTP.

Run with the interpreter from requirements-moto.txt's isolated environment.
The proxy observes exact reboot requests and Moto responses; it never invents
a rebooted guest. Reports are explicitly mock-contract evidence.
"""
import argparse
import hashlib
import http.server
import importlib.metadata
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET


def response_kind(status, body):
    """Recognize Moto's EC2 response envelope, never a guest reboot effect."""
    try:
        root = ET.fromstring(body)
    except ET.ParseError:
        return 'invalid'
    name = root.tag.rsplit('}', 1)[-1]
    values = {e.tag.rsplit('}', 1)[-1]: e.text for e in root.iter()}
    # Moto 5.2.3 omits the optional return element in this response.
    request_id = (values.get('requestId') or '').strip()
    return_ok = 'return' not in values or (values['return'] or '').strip() == 'true'
    if status == 200 and name == 'RebootInstancesResponse' and request_id and return_ok:
        return 'count'
    if status == 400 and name == 'Response' and values.get('Code') == 'InvalidInstanceID.NotFound':
        return 'rejected'
    return 'invalid'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    try:
        import boto3
        from moto.server import ThreadedMotoServer
    except ImportError:
        parser.error('Install requirements-moto.txt in an isolated Python environment first')
    binary = pathlib.Path(args.binary).resolve()
    output = pathlib.Path(args.out).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    moto = ThreadedMotoServer(ip_address='127.0.0.1', port=0, verbose=False)
    moto.start()
    _, port = moto.get_host_and_port()
    upstream = f'http://127.0.0.1:{port}'
    ec2 = boto3.client('ec2', region_name='us-east-1', endpoint_url=upstream,
                       aws_access_key_id='cpra-fixture', aws_secret_access_key='cpra-fixture')
    lock = threading.Lock()
    counts = {'count': 0, 'rejected': 0, 'invalid': 0}
    instance = ec2.run_instances(ImageId='ami-12345678', MinCount=1, MaxCount=1)['Instances'][0]['InstanceId']

    class Target(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path != '/audit':
                self.send_error(404)
                return
            with lock:
                data = json.dumps(counts).encode()
            self.send_response(200)
            self.end_headers()
            self.wfile.write(data)

        def do_POST(self):
            length = int(self.headers.get('Content-Length', '0'))
            if length <= 0 or length > 65536:
                self.send_error(400)
                return
            data = self.rfile.read(length)
            form = urllib.parse.parse_qs(data.decode())
            valid = (self.path == '/' and form.get('Action') == ['RebootInstances']
                     and form.get('Version') == ['2016-11-15']
                     and form.get('InstanceId.1') in ([instance], ['i-00000000000000000'])
                     and 'Credential=cpra-fixture/' in self.headers.get('Authorization', ''))
            if not valid:
                with lock:
                    counts['invalid'] += 1
                self.send_error(400)
                return
            request = urllib.request.Request(upstream, data=data, headers={
                'Content-Type': self.headers['Content-Type'],
                'Authorization': self.headers['Authorization'],
                'X-Amz-Date': self.headers.get('X-Amz-Date', '')})
            try:
                response = urllib.request.urlopen(request, timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                body, status = response.read(), response.code
            with lock:
                counts[response_kind(status, body)] += 1
            self.send_response(status)
            self.send_header('Content-Type', 'text/xml')
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Target)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    endpoint = f'http://127.0.0.1:{server.server_port}'
    try:
        with tempfile.TemporaryDirectory(prefix='cpra-moto-') as directory:
            observer = pathlib.Path(directory) / 'observer.py'
            observer.write_text('''import json, os, sys, urllib.request
with urllib.request.urlopen(sys.argv[1] + '/audit', timeout=5) as r: row=json.load(r)
if os.environ['CPRA_VERIFY_PHASE']=='after':
    before=json.load(sys.stdin)
    row['observed']=row['count']==before['count']+1 and row['invalid']==before['invalid']
print(json.dumps(row))
''')
            env = dict(os.environ, AWS_ENDPOINT_URL_EC2=endpoint,
                       AWS_ACCESS_KEY_ID='cpra-fixture', AWS_SECRET_ACCESS_KEY='cpra-fixture',
                       AWS_EC2_METADATA_DISABLED='true', AWS_SHARED_CREDENTIALS_FILE='/dev/null',
                       AWS_CONFIG_FILE='/dev/null', AWS_IGNORE_CONFIGURED_ENDPOINT_URLS='false')
            for key in ('AWS_SESSION_TOKEN', 'AWS_PROFILE', 'AWS_WEB_IDENTITY_TOKEN_FILE',
                        'AWS_ROLE_ARN', 'AWS_CONTAINER_CREDENTIALS_FULL_URI', 'AWS_CONTAINER_CREDENTIALS_RELATIVE_URI'):
                env.pop(key, None)
            scenarios = []
            for name, target in [('success', instance), ('invalid-instance', 'i-00000000000000000')]:
                monitor = {'id': 'moto', 'name': 'moto', 'pulse_check': {'type': 'http', 'interval': '60s',
                           'timeout': '5s', 'config': {'url': endpoint}}, 'intervention': {'action': 'aws',
                           'target': {'type': 'aws', 'region': 'us-east-1', 'operation': 'reboot-instance',
                                      'instance_id': target, 'timeout': '5s'}}}
                case = {'kind': 'intervention', 'driver': 'aws', 'monitor_id': 'moto', 'configured': True,
                        'evidence_type': 'mock_contract', 'observer': [sys.executable, str(observer), endpoint]}
                config = pathlib.Path(directory) / 'config.json'
                config.write_text(json.dumps({'manifest': {'monitors': [monitor]}, 'cases': [case]}))
                report_path = output if name == 'success' else output.with_name(output.stem + '-invalid-instance.json')
                before = dict(counts)
                completed = subprocess.run([str(binary), '-live', '-config', str(config), '-out', str(report_path)],
                                           env=env, timeout=30, stdout=subprocess.DEVNULL, check=False)
                if completed.returncode not in (0, 2) or not report_path.exists():
                    raise RuntimeError('verification invocation failed')
                report = json.loads(report_path.read_text())
                row = next(r for r in report['records'] if r['kind'] == 'intervention' and r['driver'] == 'aws')
                if name == 'success':
                    passed = row['status'] == 'pass' and counts['count'] == before['count'] + 1
                else:
                    passed = (row['status'] == 'fail' and not row['accepted']
                              and counts['rejected'] == before['rejected'] + 1
                              and counts['invalid'] == before['invalid'])
                passed = (passed and row['operation_invoked'] and row['evidence_type'] == 'mock_contract'
                          and not report['all_providers_verified'])
                scenarios.append({'scenario': name, 'passed': passed, 'evidence_type': 'mock_contract',
                                  'report': report_path.name})
            summary = {'status': 'pass' if all(s['passed'] for s in scenarios) else 'fail',
                       'moto_version': importlib.metadata.version('moto'), 'scenarios': scenarios,
                       'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
                       'real_guest_reboot_verified': False}
            output.with_suffix('.suite.json').write_text(json.dumps(summary, indent=2) + '\n')
            return 0 if summary['status'] == 'pass' else 1
    finally:
        server.shutdown()
        server.server_close()
        thread.join()
        moto.stop()


if __name__ == '__main__':
    raise SystemExit(main())
