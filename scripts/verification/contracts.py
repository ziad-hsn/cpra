#!/usr/bin/env python3
"""Exercise twelve production HTTP notification drivers against strict mocks.

Uses a separately invoked cpra-verify binary and actual loopback HTTP sockets.
No Go transport replacement, provider account or real destination is involved.
The primary report contains success cases. The adjacent .suite.json also proves
rejection of bad status lines, disconnects, timeouts and HTTP 400/429/500.
"""
import argparse
import base64
import hashlib
import http.server
import json
import pathlib
import re
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse

DRIVERS = ('slack', 'pagerduty', 'webhook', 'telegram', 'discord', 'opsgenie',
           'mattermost', 'victorops', 'pushover', 'datadog', 'teams', 'twilio')
MODES = ('success', '400', '429', '500', 'malformed_http', 'disconnect', 'timeout')
PATHS = {'slack': '/services/TLOCAL/BLOCAL/local-hook', 'pagerduty': '/v2/enqueue',
         'webhook': '/notifications', 'telegram': '/bot123456:local-token/sendMessage',
         'discord': '/api/webhooks/123456/local-token', 'opsgenie': '/v2/alerts',
         'mattermost': '/hooks/local-hook', 'victorops': '/integrations/generic/20131114/alert/local-endpoint/local-route',
         'pushover': '/1/messages.json', 'datadog': '/api/v1/events',
         'teams': '/workflows/local-hook', 'twilio': '/2010-04-01/Accounts/ACLOCAL/Messages.json'}
RUN_PATTERN = re.compile(r'\[CPRa verification ([0-9a-f-]{36})\]')


def notification_config(driver, url):
    return {
        'slack': {'hook': url},
        'pagerduty': {'url': url, 'routing_key': 'local-routing-key'},
        'webhook': {'url': url, 'method': 'POST', 'headers': {'X-CPRa-Fixture': 'local-header'}},
        'telegram': {'url': url, 'bot_token': '123456:local-token', 'chat_id': '424242'},
        'discord': {'webhook_url': url},
        'opsgenie': {'url': url, 'api_key': 'local-api-key'},
        'mattermost': {'webhook_url': url, 'channel': 'cpra-fixture', 'username': 'cpra-verifier'},
        'victorops': {'url': url, 'rest_endpoint_key': 'local-endpoint', 'routing_key': 'local-route',
                      'message_type': 'CRITICAL', 'entity_id': 'local-entity'},
        'pushover': {'url': url, 'app_token': 'local-app-token', 'user_key': 'local-user',
                     'title': 'CPRa fixture', 'priority': 2, 'retry': 60, 'expire': 1800, 'sound': 'pushover'},
        'datadog': {'url': url, 'api_key': 'local-api-key', 'app_key': 'local-app-key', 'tags': ['fixture:cpra']},
        'teams': {'webhook_url': url},
        'twilio': {'url': url, 'account_sid': 'ACLOCAL', 'auth_token': 'local-auth-token',
                   'from': '+15005550006', 'to': '+15005550009'},
    }[driver]


def success_response(driver):
    """Representative documented success envelopes; these are mock receipts."""
    responses = {
        'slack': (200, 'text/plain', b'ok'),
        'pagerduty': (202, 'application/json', {'status': 'success', 'message': 'Event processed', 'dedup_key': 'cpra-fixture'}),
        'webhook': (204, 'application/json', b''),
        'telegram': (200, 'application/json', {'ok': True, 'result': {'message_id': 1, 'date': 1700000000,
                                                                  'chat': {'id': 424242, 'type': 'private'}, 'text': 'fixture'}}),
        'discord': (204, 'application/json', b''),
        'opsgenie': (202, 'application/json', {'result': 'Request will be processed', 'took': 0.001, 'requestId': 'local-fixture-request'}),
        'mattermost': (200, 'text/plain', b'ok'),
        'victorops': (200, 'application/json', {'result': 'success', 'entity_id': 'local-entity'}),
        'pushover': (200, 'application/json', {'status': 1, 'request': 'local-fixture-request', 'receipt': 'local-fixture-receipt'}),
        'datadog': (202, 'application/json', {'status': 'ok', 'event': {'id': 1, 'title': 'CPRa fixture', 'text': 'fixture'}}),
        'teams': (202, 'application/json', b''),
        'twilio': (201, 'application/json', {'sid': 'SM' + '0' * 32, 'account_sid': 'ACLOCAL', 'status': 'queued',
                                          'from': '+15005550006', 'to': '+15005550009', 'body': 'fixture'}),
    }
    status, content_type, body = responses[driver]
    return status, content_type, body if isinstance(body, bytes) else json.dumps(body).encode()


def validate_request(driver, method, path, headers, raw, expected_path, monitor):
    """Return correlated run ID or raise on a real request-contract violation."""
    if method != 'POST' or path != expected_path:
        raise ValueError('method or route mismatch')
    headers = {key.lower(): value for key, value in headers.items()}
    form = driver in ('twilio', 'pushover')
    content_type = 'application/x-www-form-urlencoded' if form else 'application/json'
    if headers.get('content-type', '').split(';')[0] != content_type:
        raise ValueError('content type mismatch')
    if form:
        values = urllib.parse.parse_qs(raw.decode(), strict_parsing=True)
        if any(len(items) != 1 for items in values.values()):
            raise ValueError('duplicate form field')
        body = {key: items[0] for key, items in values.items()}
    else:
        body = json.loads(raw)
    expected_auth = {'opsgenie': 'GenieKey local-api-key',
                     'twilio': 'Basic ' + base64.b64encode(b'ACLOCAL:local-auth-token').decode()}
    if driver in expected_auth and headers.get('authorization') != expected_auth[driver]:
        raise ValueError('authorization mismatch')
    if driver == 'datadog' and (headers.get('dd-api-key') != 'local-api-key' or
                               headers.get('dd-application-key') != 'local-app-key'):
        raise ValueError('Datadog credentials mismatch')
    if driver == 'webhook' and headers.get('x-cpra-fixture') != 'local-header':
        raise ValueError('webhook custom header mismatch')
    message = None
    if driver in ('slack', 'telegram', 'mattermost', 'datadog'):
        message = body['text']
    elif driver == 'pagerduty':
        message = body['payload']['summary']
    elif driver in ('webhook', 'pushover'):
        message = body['message']
    elif driver == 'discord':
        message = body['content']
    elif driver == 'opsgenie':
        message = body['description']
    elif driver == 'victorops':
        message = body['state_message']
    elif driver == 'twilio':
        message = body['Body']
    elif driver == 'teams':
        attachment, = body['attachments']
        content = attachment['content']
        if (body['type'] != 'message' or attachment['contentType'] != 'application/vnd.microsoft.card.adaptive'
                or content['type'] != 'AdaptiveCard' or content['version'] != '1.2'):
            raise ValueError('Teams card mismatch')
        block, = content['body']
        if block['type'] != 'TextBlock' or block['wrap'] is not True:
            raise ValueError('Teams text block mismatch')
        message = block['text']
    match = RUN_PATTERN.search(message)
    if not match:
        raise ValueError('missing operation correlation')
    run_id = match.group(1)
    monitor_name = monitor + ' [CPRa verification ' + run_id + ']'
    if 'Monitor: ' + monitor_name not in message or '\nStatus: ' not in message:
        raise ValueError('notification message mismatch')
    fields = {
        'telegram': {'chat_id': '424242'},
        'webhook': {'monitor': monitor_name, 'color': 'red'},
        'mattermost': {'channel': 'cpra-fixture', 'username': 'cpra-verifier'},
        'victorops': {'message_type': 'CRITICAL', 'entity_id': 'local-entity'},
        'pushover': {'token': 'local-app-token', 'user': 'local-user', 'title': 'CPRa fixture',
                     'priority': '2', 'retry': '60', 'expire': '1800', 'sound': 'pushover'},
        'twilio': {'From': '+15005550006', 'To': '+15005550009'},
        'datadog': {'title': 'CPRA alert: ' + monitor_name, 'alert_type': 'error', 'tags': ['fixture:cpra']},
        'pagerduty': {'routing_key': 'local-routing-key', 'event_action': 'trigger', 'dedup_key': 'cpra:' + monitor_name},
    }
    if any(body.get(key) != value for key, value in fields.get(driver, {}).items()):
        raise ValueError('provider body fields mismatch')
    if driver == 'pagerduty' and (body['payload']['source'] != monitor_name or body['payload']['severity'] != 'error'):
        raise ValueError('PagerDuty payload mismatch')
    if driver == 'opsgenie' and (len(body['message']) > 130 or not body['message'] or
                                not re.fullmatch(r'[0-9a-f-]{36}', body['alias'])):
        raise ValueError('Opsgenie limits or identity mismatch')
    return run_id


class ContractServer(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self):
        self.lock = threading.Lock()
        self.states = {}
        super().__init__(('127.0.0.1', 0), ContractHandler)

    def register(self, driver, mode):
        key = driver + '-' + mode
        path = '/' + key + PATHS[driver]
        with self.lock:
            self.states[key] = {'count': 0, 'invalid': 0, 'run_id': '', 'digest': '',
                                'driver': driver, 'mode': mode, 'path': path}
        return key, 'http://127.0.0.1:' + str(self.server_port) + path

    def audit(self, key):
        with self.lock:
            return dict(self.states[key])


class ContractHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        if not self.path.startswith('/audit/'):
            self.send_error(404)
            return
        try:
            data = json.dumps(self.server.audit(self.path.removeprefix('/audit/'))).encode()
        except KeyError:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        key = self.path.split('/')[1]
        try:
            state = self.server.audit(key)
        except KeyError:
            self.send_error(404)
            return
        try:
            length = int(self.headers.get('Content-Length', '0'))
            if length < 1 or length > 65536:
                raise ValueError('invalid request size')
            raw = self.rfile.read(length)
            run_id = validate_request(state['driver'], 'POST', self.path, self.headers, raw, state['path'], key)
        except (ValueError, KeyError, TypeError, UnicodeError):
            with self.server.lock:
                self.server.states[key]['invalid'] += 1
            self.send_error(422)
            return
        with self.server.lock:
            self.server.states[key]['count'] += 1
            self.server.states[key].update(run_id=run_id, digest=hashlib.sha256(raw).hexdigest())
        mode = state['mode']
        if mode == 'disconnect':
            self.connection.shutdown(socket.SHUT_RDWR)
            self.close_connection = True
            return
        if mode == 'malformed_http':
            self.connection.sendall(b'THIS IS NOT HTTP\r\n\r\n')
            self.close_connection = True
            return
        if mode == 'timeout':
            time.sleep(1.5)
            self.close_connection = True
            return
        status, content_type, response = success_response(state['driver'])
        if mode.isdigit():
            status, content_type, response = int(mode), 'application/json', b'{"error":"fixture rejection"}'
        self.send_response(status)
        if mode == '429':
            self.send_header('Retry-After', '1')
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(response)))
        self.end_headers()
        try:
            self.wfile.write(response)
        except (BrokenPipeError, ConnectionResetError):
            pass


def run(binary, out):
    out = pathlib.Path(out).resolve()
    out.parent.mkdir(parents=True, exist_ok=True)
    observer = pathlib.Path(__file__).with_name('observe_fixture.py').resolve()
    server = ContractServer()
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    suite = {'evidence_type': 'mock_contract', 'provider_certification': False,
             'binary_sha256': hashlib.sha256(pathlib.Path(binary).read_bytes()).hexdigest(),
             'scenarios': [], 'limitations': ['Mocks check request contracts and HTTP failure handling; they do not prove provider delivery.',
                                              'Malformed HTTP framing is tested; successful response bodies are not parsed by current notification drivers.']}
    try:
        with tempfile.TemporaryDirectory(prefix='cpra-contract-') as directory:
            for mode in MODES:
                monitors, cases = [], []
                for driver in DRIVERS:
                    key, url = server.register(driver, mode)
                    monitors.append({'id': key, 'name': key,
                                     'pulse_check': {'type': 'http', 'interval': '60s', 'timeout': '1s', 'config': {'url': url}},
                                     'codes': {'red': {'dispatch': True, 'notify': driver, 'config': notification_config(driver, url)}}})
                    cases.append({'kind': 'code', 'driver': driver, 'configured': True, 'monitor_id': key,
                                  'color': 'red', 'evidence_type': 'mock_contract', 'timeout': '1s',
                                  'observer': [sys.executable, str(observer), f'http://127.0.0.1:{server.server_port}/audit/{key}']})
                config = pathlib.Path(directory) / (mode + '.yaml')
                config.write_text(json.dumps({'manifest': {'monitors': monitors}, 'cases': cases}))
                report_path = out if mode == 'success' else out.with_name(out.stem + '-' + mode + '.json')
                result = subprocess.run([str(pathlib.Path(binary).resolve()), '-live', '-config', str(config), '-out', str(report_path)],
                                        capture_output=True, text=True, timeout=45)
                if result.returncode not in (0, 2):
                    raise RuntimeError(f'cpra-verify {mode} exited {result.returncode}: {result.stderr[:1000]}')
                report = json.loads(report_path.read_text())
                for driver in DRIVERS:
                    record = next(item for item in report['records'] if item['kind'] == 'code' and item['driver'] == driver)
                    state = server.audit(driver + '-' + mode)
                    passed = (record.get('operation_invoked') is True
                              and state['count'] == 1 and state['invalid'] == 0 and state['run_id'] == report['run_id']
                              and record['accepted'] == (mode == 'success') and record['status'] == ('pass' if mode == 'success' else 'fail')
                              and record['duration_ms'] < 3000)
                    suite['scenarios'].append({'driver': driver, 'mode': mode, 'passed': passed,
                                               'requests': state['count'], 'invalid_requests': state['invalid'],
                                               'request_sha256': state['digest'], 'run_id': state['run_id'],
                                               'driver_accepted': record['accepted'], 'duration_ms': record['duration_ms'],
                                               'driver_status': record['status'], 'reason': record.get('reason', ''),
                                               'operation_invoked': record.get('operation_invoked', False),
                                               'report': report_path.name})
                print(f'{mode}: {sum(s["passed"] for s in suite["scenarios"] if s["mode"] == mode)}/{len(DRIVERS)} checks passed')
    finally:
        server.shutdown()
        server.server_close()
        thread.join()
        suite['passed'] = len(suite['scenarios']) == len(DRIVERS) * len(MODES) and all(s['passed'] for s in suite['scenarios'])
        out.with_suffix('.suite.json').write_text(json.dumps(suite, indent=2) + '\n')
    return 0 if suite['passed'] else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    return run(args.binary, args.out)


if __name__ == '__main__':
    raise SystemExit(main())
