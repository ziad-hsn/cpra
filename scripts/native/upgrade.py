#!/usr/bin/env python3
"""Exercise two native CPRa versions against real committed state and stopped backups.

The caller supplies separately built executables and a new private work directory.
Only local HTTP fixtures receive effects. This does not install services, publish
artifacts, or qualify other operating systems or storage implementations.
"""
import argparse
from collections import Counter
from datetime import datetime
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import platform
import secrets
import signal
import socket
import ssl
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(chunk)
    return value.hexdigest()


def write(path, value):
    with path.open('xb') as stream:
        os.chmod(path, 0o600)
        stream.write(value.encode() if isinstance(value, str) else value)


def json_file(path, value):
    write(path, json.dumps(value, indent=2) + '\n')


def environment():
    return {'PATH': '/usr/bin:/bin', 'HOME': str(Path.home()), 'TMPDIR': '/tmp',
            'CPRA_ENV': 'test', 'GOTOOLCHAIN': 'local', 'GOWORK': 'off'}


def command(argv, log, success=True):
    with log.open('xb') as output:
        os.chmod(log, 0o600)
        result = subprocess.run([str(item) for item in argv], stdout=output,
                                stderr=subprocess.STDOUT, env=environment(), timeout=20)
    require((result.returncode == 0) == success, 'Unexpected command result: ' + log.name)
    return result.returncode


def eventually(predicate, message, timeout=20):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(.1)
    raise RuntimeError(message)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Client:
    def __init__(self, origin, certificate, token):
        self.origin, self.token = origin, token
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), NoRedirect(),
            urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(certificate))))

    def request(self, path, method='GET', body=None, headers=None, expected=None):
        headers = dict(headers or {})
        headers.update({'Authorization': 'Bearer ' + self.token, 'Accept': 'application/json'})
        if body is not None:
            headers.setdefault('Content-Type', 'application/json')
        req = urllib.request.Request(self.origin + path,
                                     data=None if body is None else json.dumps(body).encode(),
                                     headers=headers, method=method)
        try:
            response = self.opener.open(req, timeout=3)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(1024 * 1024 + 1)
            require(len(raw) <= 1024 * 1024, 'Response exceeds fixture limit')
            status = response.status
            require(status == expected if expected is not None else 200 <= status < 300,
                    'Unexpected HTTP status ' + str(status) + ' for ' + path.split('?')[0])
            return (json.loads(raw) if raw else {}), response.headers

    def mutation(self, path, body, headers=None, method='POST'):
        value, response_headers = self.request(path, method, body, headers)
        operation = response_headers.get('X-Operation-ID')
        require(operation, 'Mutation has no committed operation identity')

        def completed():
            progress, _ = self.request('/api/v2/operations/' + operation)
            require(progress['state'] not in ('failed', 'partial', 'cancelled'),
                    'Mutation did not apply completely')
            return progress['state'] == 'completed' and progress.get('applied') == 1

        eventually(completed, 'Committed operation did not complete')
        return value

    def create(self, collection, kind, identity, spec):
        return self.mutation('/api/v2/' + collection,
                             {'apiVersion': 'cpra.io/v2', 'kind': kind,
                              'metadata': {'id': identity}, 'spec': spec}, {'If-None-Match': '*'})

    def page(self, collection):
        value, _ = self.request('/api/v2/' + collection + '?monitorID=upgrade-monitor&limit=100')
        require(not value.get('nextCursor'), 'Fixture unexpectedly exceeds one page')
        return value['items']


class Provider:
    def __init__(self):
        self.lock, self.release = threading.Lock(), threading.Event()
        self.counts = Counter()
        self.failures_remaining = 0
        self.last_failure_at = 0
        provider = self

        class Handler(BaseHTTPRequestHandler):
            def handle_request(self):
                length = int(self.headers.get('Content-Length', 0))
                require(0 <= length <= 1024 * 1024, 'Fixture request exceeds limit')
                self.rfile.read(length)
                path = self.path.split('?')[0]
                with provider.lock:
                    provider.counts[path] += 1
                    failed = path == '/health' and provider.failures_remaining > 0
                    if failed:
                        provider.failures_remaining -= 1
                        provider.counts['failed_health_checks'] += 1
                        provider.last_failure_at = time.time()
                if path in ('/recover', '/notify-held'):
                    provider.release.wait(30)
                try:
                    self.send_response(503 if path == '/health' and failed else 200)
                    self.send_header('Content-Length', '0')
                    self.end_headers()
                except (BrokenPipeError, ConnectionResetError):
                    pass

            do_GET = do_POST = handle_request

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.origin = 'http://127.0.0.1:' + str(self.server.server_port)

    def observed(self):
        with self.lock:
            return dict(self.counts)

    def fail_checks(self, count):
        with self.lock:
            require(self.failures_remaining == 0, 'Previous fault has not drained')
            self.failures_remaining = count

    def fault_finished_at(self):
        with self.lock:
            require(self.failures_remaining == 0, 'Check fault has not drained')
            return self.last_failure_at

    def close(self):
        self.release.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


class Owner:
    def __init__(self, binary, private, state, label):
        self.process = None
        self.log = (private / (label + '.log')).open('xb')
        os.chmod(self.log.name, 0o600)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        self.origin = 'https://127.0.0.1:' + str(port)
        self.process = subprocess.Popen(
            [str(binary), '-runtime-config', str(private / 'runtime.yaml'),
             '-yaml', str(private / 'monitors.yaml'), '-data-dir', str(state),
             '-allow-empty', '-web.addr', '127.0.0.1:' + str(port), '-shutdown-timeout', '5s'],
            stdout=self.log, stderr=subprocess.STDOUT, env=environment(), start_new_session=True)

    def ready(self, client):
        def initialized():
            require(self.process.poll() is None, 'Owner exited before readiness')
            try:
                client.request('/api/v2/readyz')
                return True
            except (RuntimeError, urllib.error.URLError, TimeoutError):
                return False
        eventually(initialized, 'Owner did not become ready')

    def stop(self, force=False):
        if self.process is None:
            return
        try:
            if self.process.poll() is None:
                self.process.send_signal(signal.SIGKILL if force else signal.SIGTERM)
                try:
                    self.process.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait(timeout=5)
                    raise RuntimeError('Owner exceeded shutdown deadline')
            if not force:
                require(self.process.returncode == 0, 'Owner failed graceful shutdown')
        finally:
            self.log.close()


def state_identity(state):
    return json.loads((state / 'identity.json').read_text())['id']


def scenario(args, name, snapshot_interval, result):
    root = args.work_dir / name
    root.mkdir(mode=0o700)
    private = root / 'private'
    private.mkdir(mode=0o700)
    state = root / 'state'
    token = secrets.token_urlsafe(40)
    write(private / 'original.token', token)
    wrapping, backup_key = private / 'wrapping.key', private / 'backup.key'
    write(wrapping, secrets.token_bytes(32))
    write(backup_key, secrets.token_bytes(32))
    cert, key = private / 'server.pem', private / 'server.key'
    command(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt',
             'ec_paramgen_curve:prime256v1', '-nodes', '-days', '2', '-subj', '/CN=127.0.0.1',
             '-addext', 'subjectAltName=IP:127.0.0.1', '-out', cert, '-keyout', key],
            private / 'certificate.log')
    os.chmod(key, 0o600)
    os.chmod(cert, 0o600)
    json_file(private / 'principals.yaml', {'principals': [{
        'id': 'upgrade/operator', 'role': 'operator', 'token_sha256': hashlib.sha256(token.encode()).hexdigest()}]})
    json_file(private / 'runtime.yaml', {
        'storage': {'mode': 'raft', 'directory': str(state), 'snapshot_interval': snapshot_interval},
        'management': {'enabled': True, 'policy_file': str(private / 'principals.yaml'),
                       'tls': {'cert_file': str(cert), 'key_file': str(key)},
                       'encryption': {'local': {'active_key_file': str(wrapping)}}}})
    write(private / 'monitors.yaml', 'monitors: []\n')
    provider, owner = Provider(), None
    result.update(snapshot_interval=snapshot_interval, phase='starting_previous')
    try:
        owner = Owner(args.previous_binary, private, state, 'previous')
        client = Client(owner.origin, cert, token)
        owner.ready(client)
        result['phase'] = 'previous_ready'
        node = state_identity(state)
        for identity, path in [('recovery-url', '/recover'), ('delivered-url', '/notify-delivered'),
                               ('held-url', '/notify-held')]:
            created = client.create('credentials', 'Credential', identity, {'value': provider.origin + path})
            require('value' not in created['spec'], 'Credential read exposed a value')
        for identity, credential in [('delivered', 'delivered-url'), ('held', 'held-url')]:
            client.create('notification-endpoints', 'NotificationEndpoint', identity,
                          {'type': 'webhook', 'config': {'method': 'POST'},
                           'credentialRefs': {'url': credential}})
        monitor = client.create('monitors', 'Monitor', 'upgrade-monitor', {
            'enabled': True,
            'check': {'interval': '250ms', 'timeout': '1s', 'retries': 0, 'healthyThreshold': 1,
                      'unhealthyThreshold': 1, 'driver': {'type': 'http', 'config': {'url': provider.origin + '/health'}}},
            'recovery': {'maxFailures': 1, 'maxAttempts': 3, 'cooldown': '250ms',
                         'driver': {'type': 'webhook', 'config': {'method': 'POST', 'timeout': '20s'},
                                    'credentialRefs': {'url': 'recovery-url'}}},
            'notifications': {'yellow': {'dispatch': True, 'endpointRefs': ['delivered', 'held']}}})
        eventually(lambda: provider.observed().get('/health', 0) >= 3, 'Healthy checks did not execute')
        require(not client.page('actions'), 'Healthy monitor produced an action')
        command([args.candidate_cli, 'local', 'backup', '--data-dir', state,
                 '--output', root / 'live-backup', '--backup-auth-key', backup_key],
                private / 'live-backup.log', success=False)
        require(not (root / 'live-backup').exists(), 'Live backup published a destination')
        if name == 'snapshot':
            eventually(lambda: list((state / 'snapshots').glob('*/state.bin')),
                       'Previous owner did not create a snapshot')
        provider.fail_checks(1)

        def started():
            actions = client.page('actions')
            result['observed_actions'] = actions
            result['provider_counts'] = provider.observed()
            return (len(actions) == 3 and sorted(a['state'] for a in actions) == ['started', 'started', 'succeeded']
                    and provider.observed().get('/recover') == 1
                    and provider.observed().get('/notify-held') == 1
                    and provider.observed().get('/notify-delivered') == 1) and actions

        actions = eventually(started, 'Expected committed actions and independently observed effects did not occur')
        result['phase'] = 'actions_started'
        current, _ = client.request('/api/v2/monitors/upgrade-monitor')
        incident_id = current['status']['incidentID']
        incident, _ = client.request('/api/v2/incidents/' + incident_id)
        client.mutation('/api/v2/incidents/' + incident_id + '/acknowledge',
                        {'revision': incident['revision'], 'note': 'Investigating during the upgrade fixture'},
                        {'If-Match': '"' + incident['revision'] + '"'})
        before = client.page('history')
        require(before and len({e['id'] for e in before}) == len(before), 'Original history is missing or duplicated')
        interrupted = client.page('actions')
        result['interrupted_actions'] = interrupted
        require({a['id'] for a in interrupted} == {a['id'] for a in actions}
                and sorted(a['state'] for a in interrupted) == ['started', 'started', 'succeeded'],
                'Actions were no longer at the intended started crash boundary')
        acknowledged, _ = client.request('/api/v2/incidents/' + incident_id)
        result['before'] = {'node_id': node, 'monitor': current, 'incident': acknowledged,
                            'history': before}
        owner.stop(force=True)
        owner = None
        result['phase'] = 'previous_terminated'
        command([args.previous_cli, 'local', 'backup', '--data-dir', state,
                 '--output', root / 'previous-backup', '--backup-auth-key', backup_key],
                private / 'previous-backup.log')
        original_inventory = json.loads((root / 'previous-backup/BACKUP.json').read_text())
        require(original_inventory['version'] == 2 and 'state_format' not in original_inventory,
                'Previous executable did not produce the expected original inventory')

        def recovered(client, expected_actor):
            recovered_monitor, _ = client.request('/api/v2/monitors/upgrade-monitor')
            require(recovered_monitor['metadata']['uid'] == monitor['metadata']['uid'], 'Upgrade changed monitor incarnation')
            require(recovered_monitor['status']['incidentID'] == incident_id, 'Upgrade changed incident identity')
            observed_actions = client.page('actions')
            require({a['id'] for a in observed_actions} == {a['id'] for a in actions}, 'Upgrade replaced or added action identities')
            require(sorted(a['state'] for a in observed_actions) == ['succeeded', 'unknown', 'unknown'],
                    'Interrupted actions were repeated, lost, or resolved without evidence')
            require(all(a['held'] for a in observed_actions if a['state'] == 'unknown'), 'Unknown outcome is not held')
            recovered_incident, _ = client.request('/api/v2/incidents/' + incident_id)
            require(recovered_incident['acknowledgedBy'] == 'upgrade/operator', 'Incident ownership did not survive')
            history = client.page('history')
            require(len({e['id'] for e in history}) == len(history), 'Replayed history has duplicate events')
            require({e['id'] for e in before} <= {e['id'] for e in history}, 'Upgrade lost retained history')
            access, _ = client.request('/api/v2/self')
            require(access['principalId'] == expected_actor, 'Recovered access belongs to the wrong principal')
            prior_checks = provider.observed().get('/health', 0)
            provider.fail_checks(8)
            eventually(lambda: provider.observed().get('/health', 0) >= prior_checks + 10,
                       'Health checks did not resume through repeated failures')
            def committed():
                current, _ = client.request('/api/v2/monitors/upgrade-monitor')
                checked_at = current['status'].get('lastCheckedAt')
                return checked_at and datetime.fromisoformat(checked_at.replace('Z', '+00:00')).timestamp() > provider.fault_finished_at()

            eventually(committed, 'Resumed check results did not commit')
            final_actions = client.page('actions')
            require({a['id'] for a in final_actions} == {a['id'] for a in actions}
                    and sorted(a['state'] for a in final_actions) == ['succeeded', 'unknown', 'unknown'],
                    'Repeated failures replaced a delivered or interrupted action')
            require(all(provider.observed().get(path) == 1 for path in ['/recover', '/notify-held', '/notify-delivered']),
                    'Upgrade repeated an interrupted action or confirmed delivery')
            for identity in ['recovery-url', 'delivered-url', 'held-url']:
                credential, _ = client.request('/api/v2/credentials/' + identity)
                require('value' not in credential['spec'], 'Recovered credential returned its secret')
            return {'actions': final_actions, 'history': history, 'monitor': recovered_monitor,
                    'incident': recovered_incident, 'provider_counts': provider.observed()}

        owner = Owner(args.candidate_binary, private, state, 'candidate')
        client = Client(owner.origin, cert, token)
        owner.ready(client)
        require(state_identity(state) == node, 'Upgrade replaced durable node identity')
        result['upgrade'] = recovered(client, 'upgrade/operator')
        result['upgrade']['node_id'] = state_identity(state)
        result['phase'] = 'candidate_recovered'
        owner.stop()
        owner = None
        command([args.candidate_cli, 'local', 'backup', '--data-dir', state,
                 '--output', root / 'candidate-backup', '--backup-auth-key', backup_key],
                private / 'candidate-backup.log')
        candidate_inventory = json.loads((root / 'candidate-backup/BACKUP.json').read_text())
        require(candidate_inventory.get('state_format', 0) > 1, 'Candidate backup lacks committed-state format')
        result['backup_formats'] = {'previous': original_inventory.get('state_format'),
                                   'candidate': candidate_inventory['state_format']}
        command([args.previous_cli, 'local', 'restore', '--backup', root / 'previous-backup',
                 '--data-dir', root / 'older-compatible-restore', '--backup-auth-key', backup_key],
                private / 'older-compatible-restore.log')
        command([args.previous_cli, 'local', 'restore', '--backup', root / 'candidate-backup',
                 '--data-dir', root / 'older-restore', '--backup-auth-key', backup_key],
                private / 'older-restore.log', success=False)
        require(not (root / 'older-restore').exists(), 'Older tool published an unsupported inventory')
        result['older_restore'] = {'original_backup_accepted': True, 'candidate_backup_rejected': True}
        counts_before_old_owner = provider.observed()
        owner = Owner(args.previous_binary, private, state, 'older-owner')
        eventually(lambda: owner.process.poll() is not None, 'Older owner accepted the upgraded storage format')
        require(owner.process.returncode != 0, 'Older owner exited successfully against upgraded state')
        result['older_owner_exit_code'] = owner.process.returncode
        owner.stop(force=True)
        owner = None
        require(provider.observed() == counts_before_old_owner, 'Older owner invoked providers against unsupported state')
        require(state_identity(state) == node, 'Older owner changed the node identity')
        result['older_owner_rejected_upgraded_state'] = True
        restored = root / 'restored'
        command([args.candidate_cli, 'local', 'restore', '--backup', root / 'previous-backup',
                 '--data-dir', restored, '--backup-auth-key', backup_key], private / 'restore.log')
        counts_before_fence = provider.observed()
        owner = Owner(args.candidate_binary, private, restored, 'fenced-restore')
        eventually(lambda: owner.process.poll() is not None, 'Restored owner did not require authentication reprovision')
        require(owner.process.returncode != 0, 'Restored owner exited successfully without reprovision')
        owner.stop(force=True)
        owner = None
        require(provider.observed() == counts_before_fence, 'Fenced restore invoked a provider')
        result['restore_rejected_before_reprovision'] = True
        command([args.candidate_cli, 'local', 'auth', 'reprovision', 'upgrade/restored',
                 '--data-dir', restored, '--role', 'operator', '--token-output', private / 'restored.token'],
                private / 'reprovision.log')
        restored_token = (private / 'restored.token').read_text().strip()
        owner = Owner(args.candidate_binary, private, restored, 'restored')
        restored_client = Client(owner.origin, cert, restored_token)
        owner.ready(restored_client)
        Client(owner.origin, cert, token).request('/api/v2/state', expected=401)
        require(state_identity(restored) == node, 'Stopped restore replaced node identity')
        result['restore'] = recovered(restored_client, 'upgrade/restored')
        result['restore']['node_id'] = state_identity(restored)
        result['old_token_status'] = 401
        owner.stop()
        owner = None
        result['snapshot_count'] = len(list((root / 'previous-backup/data/snapshots').glob('*/state.bin')))
        require((result['snapshot_count'] > 0) == (name == 'snapshot'), 'Unexpected snapshot fixture boundary')
        result['status'] = 'passed'
        return result
    finally:
        if owner is not None:
            owner.stop(force=True)
        provider.close()


def retain_logs(work_dir, out):
    """Copy only completed process logs, excluding fixture keys and state."""
    destination = out.with_name(out.name + '.inputs')
    destination.mkdir(mode=0o700, exist_ok=False)
    records = {}
    for case in ('log', 'snapshot'):
        private = work_dir / case / 'private'
        if not private.is_dir():
            continue
        forbidden = [path.read_bytes().strip() for path in private.glob('*.token')]
        target = destination / case
        target.mkdir(mode=0o700)
        for path in sorted(private.glob('*.log')):
            require(not path.is_symlink() and path.stat().st_size <= 8 * 1024 ** 2,
                    'Fixture log is linked or exceeds 8 MiB')
            data = path.read_bytes()
            require(not any(value and value in data for value in forbidden)
                    and b'-----BEGIN PRIVATE KEY-----' not in data,
                    'Fixture log contains private authentication material')
            copied = target / path.name
            write(copied, data)
            records[case + '/' + path.name] = {'sha256': digest(copied), 'bytes': len(data)}
    return records


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['previous-binary', 'previous-cli', 'candidate-binary', 'candidate-cli', 'work-dir', 'out']:
        parser.add_argument('--' + name, required=True, type=Path)
    args = parser.parse_args()
    require(platform.system() == 'Linux', 'This fixture currently requires native Linux')
    args.work_dir = args.work_dir.absolute()
    require(not args.work_dir.exists(), 'Work directory must be new')
    require(not args.out.exists(), 'Report must be new')
    require(not args.out.with_name(args.out.name + '.inputs').exists(), 'Retained log directory must be new')
    args.work_dir.mkdir(mode=0o700)
    source = Path(__file__).absolute()
    report = {'schema_version': 2, 'status': 'incomplete', 'scope': 'native two-binary storage upgrade fixture',
              'platform': platform.platform(), 'architecture': platform.machine(),
              'release_gate_passed': False, 'endurance_performed': False, 'executables': {}, 'cases': [],
              'source_files': {'scripts/native/upgrade.py': digest(source)}, 'source_changed': []}
    try:
        for name in ['previous_binary', 'previous_cli', 'candidate_binary', 'candidate_cli']:
            path = getattr(args, name).absolute()
            setattr(args, name, path)
            flag = '--version' if name.endswith('cli') else '-version'
            identity = subprocess.check_output([str(path), flag], env=environment(), text=True, timeout=5).strip()
            report['executables'][name] = {'sha256': digest(path), 'version': identity}
        require(report['executables']['previous_binary']['sha256'] != report['executables']['candidate_binary']['sha256'],
                'Upgrade requires two different executables')
        for name, interval in [('log', '1h'), ('snapshot', '1s')]:
            case = {'case': name, 'status': 'incomplete'}
            report['cases'].append(case)
            scenario(args, name, interval, case)
        report['status'] = 'passed'
    except Exception as error:
        report['error'] = str(error)[:512]
        raise
    finally:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        try:
            unchanged = digest(source) == report['source_files']['scripts/native/upgrade.py']
        except OSError:
            unchanged = False
        report['source_changed'] = [] if unchanged else ['scripts/native/upgrade.py']
        if not unchanged:
            report['status'] = 'incomplete'
        try:
            report['logs'] = retain_logs(args.work_dir, args.out)
        except (OSError, RuntimeError) as error:
            report['status'] = 'incomplete'
            report['log_error'] = str(error)[:512]
            json_file(args.out, report)
            raise
        json_file(args.out, report)


if __name__ == '__main__':
    main()
