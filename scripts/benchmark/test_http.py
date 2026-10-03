"""Real loopback HTTP tests for the harness; no CPRa capacity claim."""
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import gzip
import io
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import campaign


@contextmanager
def server(respond):
    requests = []

    class Handler(BaseHTTPRequestHandler):
        def handle_request(self):
            requests.append({'method': self.command, 'path': self.path,
                             'authorization': self.headers.get('Authorization')})
            status, headers, body = respond(requests[-1])
            self.send_response(status)
            for name, value in headers.items():
                self.send_header(name, value)
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        do_GET = handle_request
        do_POST = handle_request

        def log_message(self, *_):
            pass

    httpd = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=lambda: httpd.serve_forever(poll_interval=.01))
    thread.start()
    try:
        yield 'http://127.0.0.1:' + str(httpd.server_port), requests
    finally:
        httpd.shutdown()
        httpd.server_close()
        thread.join(timeout=2)
        if thread.is_alive():
            raise AssertionError('HTTP fixture failed to stop')


class RunningProcess:
    def poll(self):
        return None


class BenchmarkHTTPTests(unittest.TestCase):
    def test_api_token_stays_in_header_and_target_requests_have_no_token(self):
        token = 'fixture-benchmark-token'

        def authenticated(request):
            if request['authorization'] != 'Bearer ' + token:
                return 401, {}, b'{}'
            return 200, {}, b'{"total":10}'

        with server(authenticated) as (origin, calls), \
                server(lambda _: (200, {}, b'{"received":10}')) as (target, target_calls):
            api = campaign.HTTPClient(origin, token=token)
            target_http = campaign.HTTPClient(target)
            self.assertEqual(api.get('/api/v1/overview'), {'total': 10})
            self.assertEqual(target_http.get('/audit'), {'received': 10})
            target_http.post('/fault?outage=false')
            self.assertEqual(calls, [{'method': 'GET', 'path': '/api/v1/overview',
                                     'authorization': 'Bearer ' + token}])
            self.assertEqual([row['authorization'] for row in target_calls], [None, None])
            self.assertNotIn(token, ''.join(row['path'] for row in calls + target_calls))

    def test_redirects_never_contact_another_origin_or_repeat_a_request(self):
        for status in (301, 302, 303, 307, 308):
            for method in ('get', 'post'):
                with self.subTest(status=status, method=method), \
                        server(lambda _: (200, {}, b'{}')) as (destination, downstream), \
                        server(lambda _: (status, {'Location': destination + '/stolen'}, b'')) as (origin, original):
                    client = campaign.HTTPClient(origin, token='fixture-token')
                    with self.assertRaises(campaign.RequestError) as failure:
                        getattr(client, method)('/api/v1/overview')
                    self.assertEqual(failure.exception.status, status)
                    self.assertFalse(failure.exception.retryable)
                    self.assertEqual(len(original), 1)
                    self.assertEqual(downstream, [])

    def test_same_origin_redirect_is_also_rejected(self):
        with server(lambda _: (302, {'Location': '/elsewhere'}, b'')) as (origin, calls):
            with self.assertRaises(campaign.RequestError):
                campaign.HTTPClient(origin).get('/audit')
            self.assertEqual(len(calls), 1)

    def test_environment_proxy_cannot_receive_the_api_token(self):
        with server(lambda _: (200, {}, b'{}')) as (origin, calls), \
                server(lambda _: (200, {}, b'{}')) as (proxy, proxy_calls), \
                patch.dict(os.environ, {'http_proxy': proxy, 'HTTP_PROXY': proxy,
                                        'all_proxy': proxy, 'ALL_PROXY': proxy,
                                        'no_proxy': '', 'NO_PROXY': ''}):
            campaign.HTTPClient(origin, token='fixture-token').get('/api/v1/overview')
            self.assertEqual(len(calls), 1)
            self.assertEqual(proxy_calls, [])

    def test_oversized_and_invalid_json_fail_explicitly_without_echoing_body(self):
        for body, limit in ((b'{"large":"' + b'x' * 200 + b'"}', 32),
                            (b'fixture-token-not-json', 1024)):
            with self.subTest(limit=limit), server(lambda _: (200, {}, body)) as (origin, calls):
                with self.assertRaises(campaign.RequestError) as failure:
                    campaign.HTTPClient(origin, token='fixture-token', response_limit=limit).get('/api/v1/state')
                self.assertFalse(failure.exception.retryable)
                self.assertNotIn('fixture-token', str(failure.exception))
                self.assertEqual(len(calls), 1)

    def test_only_numeric_loopback_origins_and_relative_paths_are_accepted(self):
        for origin in ('https://example.com', 'http://localhost:80', 'http://127.0.0.1:80@evil.invalid',
                       'http://user:secret@127.0.0.1:80', 'http://127.0.0.1:80/?token=secret',
                       'http://127.0.0.1:80/#fragment', 'http://127.0.0.1:0'):
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                campaign.HTTPClient(origin)
        with server(lambda _: (200, {}, b'{}')) as (origin, calls):
            client = campaign.HTTPClient(origin, token='fixture-token')
            for path in ('http://evil.invalid', '//evil.invalid/path', 'relative', '/audit#fragment'):
                with self.subTest(path=path), self.assertRaises(ValueError):
                    client.get(path)
            self.assertEqual(calls, [])

    def test_startup_fails_immediately_on_authentication_or_protocol_errors(self):
        for status, body in ((401, b'{}'), (403, b'{}'), (404, b'{}'), (200, b'not-json')):
            with self.subTest(status=status), server(lambda _: (status, {}, body)) as (origin, calls):
                with self.assertRaises(campaign.RequestError):
                    campaign.wait_for_fleet(campaign.HTTPClient(origin), 10, RunningProcess(), timeout=.5, interval=.001)
                self.assertEqual(len(calls), 1)

    def test_startup_retries_unavailable_then_requires_the_complete_fleet(self):
        replies = iter(((503, {}, b'{}'), (200, {}, b'{"total":9}'), (200, {}, b'{"total":10}')))
        with server(lambda _: next(replies)) as (origin, calls):
            self.assertEqual(campaign.wait_for_fleet(campaign.HTTPClient(origin), 10,
                                                    RunningProcess(), timeout=1, interval=.001), {'total': 10})
            self.assertEqual(len(calls), 3)

    def test_history_reconciliation_authenticates_every_page(self):
        def history(request):
            if request['authorization'] != 'Bearer fixture-token':
                return 401, {}, b'{}'
            if 'cursor=second' in request['path']:
                return 200, {}, b'{"events":[{"id":"event-1","monitor_id":"benchmark-7-0","action_id":"action-1","kind":"intervention","type":"action_succeeded"}],"next_cursor":""}'
            return 200, {}, b'{"events":[],"next_cursor":"second"}'

        with server(history) as (origin, calls):
            report = campaign.reconcile(campaign.HTTPClient(origin, token='fixture-token'),
                                        {'actions': 1, 'duplicate_actions': 0, 'cohort_actions': [1]}, 1, 7)
            self.assertTrue(report['reconciled'])
            self.assertEqual(len(calls), 2)
            self.assertTrue(all(row['authorization'] == 'Bearer fixture-token' for row in calls))

    def test_matching_totals_cannot_hide_an_effect_recorded_for_another_monitor(self):
        def history(request):
            events = []
            if 'monitor_id=benchmark-7-0' in request['path']:
                events = [{'id': 'event-1', 'monitor_id': 'benchmark-7-0', 'action_id': 'action-1',
                           'kind': 'intervention', 'type': 'action_succeeded'}]
            return 200, {}, json.dumps({'events': events, 'next_cursor': ''}).encode()

        with server(history) as (origin, _):
            result = campaign.reconcile(campaign.HTTPClient(origin),
                                        {'actions': 1, 'duplicate_actions': 0, 'cohort_actions': [0, 1]}, 2, 7)
        self.assertEqual(result['target_accepted'], result['committed_successes'])
        self.assertFalse(result['reconciled'])
        self.assertEqual([row['target_accepted'] for row in result['monitors']], [0, 1])
        self.assertEqual([row['committed_successes'] for row in result['monitors']], [1, 0])

    def test_duplicate_history_pages_and_wrong_monitor_events_fail_the_audit(self):
        event = {'id': 'event-1', 'monitor_id': 'benchmark-7-0', 'action_id': 'action-1',
                 'kind': 'intervention', 'type': 'action_succeeded'}
        for page in ({'events': [], 'next_cursor': 'repeated'},
                     {'events': [event, event], 'next_cursor': ''},
                     {'events': [{**event, 'monitor_id': 'another-monitor'}], 'next_cursor': ''},
                     {'events': [event, {**event, 'id': 'another-event'}], 'next_cursor': ''}):
            with self.subTest(page=page), server(lambda _: (200, {}, json.dumps(page).encode())) as (origin, calls):
                with self.assertRaises(RuntimeError):
                    campaign.reconcile(campaign.HTTPClient(origin),
                                       {'actions': 1, 'duplicate_actions': 0, 'cohort_actions': [1]}, 1, 7)
                self.assertLessEqual(len(calls), 2)

    def test_missing_independent_per_monitor_counts_cannot_be_reconciled(self):
        with server(lambda _: (200, {}, b'{"events":[],"next_cursor":""}')) as (origin, calls):
            for observed in (None, [], [True], [-1], [2]):
                with self.subTest(observed=observed), self.assertRaisesRegex(RuntimeError, 'per-monitor'):
                    campaign.reconcile(campaign.HTTPClient(origin),
                                       {'actions': 1, 'duplicate_actions': 0, 'cohort_actions': observed}, 1, 7)
            self.assertEqual(calls, [])

    def test_token_file_is_private_exclusive_and_never_used_as_an_argument_value(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'auth.token'
            previous = os.umask(0)
            try:
                token = campaign.create_api_token(path)
            finally:
                os.umask(previous)
            self.assertGreaterEqual(len(token), 32)
            self.assertEqual(path.read_text().strip(), token)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            with self.assertRaises(FileExistsError):
                campaign.create_api_token(path)
            self.assertEqual(path.read_text().strip(), token)
            for runtime in (None, Path(directory) / 'runtime.yaml'):
                command = campaign.controller_command('/cpra', Path(directory) / 'monitors.json', 18060, path, runtime)
                self.assertNotIn(token, ' '.join(command))
                self.assertEqual(command[command.index('-web.auth-file') + 1], str(path))
                self.assertEqual('-runtime-config' in command, runtime is not None)

    def test_run_uses_one_token_for_each_controller_and_removes_it_on_exit(self):
        for upgraded, reject, cleanup_error in ((False, False, False), (True, False, False),
                                                (True, True, False), (True, False, True)):
            with self.subTest(upgraded=upgraded, reject=reject, cleanup_error=cleanup_error), tempfile.TemporaryDirectory() as directory:
                count = 2
                authorization = []
                controller_calls = []

                def respond(request):
                    if reject or not authorization or request['authorization'] != 'Bearer ' + authorization[0]:
                        return 401, {}, b'{}'
                    path = request['path']
                    if path.startswith('/api/v1/overview'):
                        value = {'total': count}
                    elif path.startswith('/api/v1/monitors'):
                        value = {'total': count, 'monitors': []}
                    elif path.startswith('/api/v1/state'):
                        value = {'storage': {'ready': True}, 'storage_usage': {}, 'process': {}}
                    elif path.startswith('/api/v1/history'):
                        value = {'events': [], 'next_cursor': ''}
                    else:
                        value = {}
                    return 200, {}, json.dumps(value).encode()

                class Process(RunningProcess):
                    pid = os.getpid()

                    def __init__(self, output, fail_wait=False):
                        self.stdout = output
                        self.returncode = None
                        self.fail_wait = fail_wait

                    def poll(self):
                        return self.returncode

                    def send_signal(self, _):
                        self.returncode = 0

                    def wait(self, timeout):
                        if self.fail_wait:
                            raise OSError('fixture wait failure')
                        return self.returncode

                with server(respond) as (origin, api_calls), \
                        server(lambda _: (200, {}, b'{"received":0,"actions":0,"duplicate_actions":0,"cohort_actions":[0,0]}')) as (target, target_calls):
                    def launch(argv, **kwargs):
                        if '-monitors' in argv:
                            return Process(io.StringIO(target + '\n'))
                        controller_calls.append((argv, kwargs))
                        authorization.append(Path(argv[argv.index('-web.auth-file') + 1]).read_text().strip())
                        return Process(io.BytesIO(), fail_wait=cleanup_error)

                    binary = Path(directory) / 'fixture-controller'
                    binary.write_bytes(b'process fixture; not a CPRa executable')
                    run_dir = Path(directory) / 'run'
                    args = SimpleNamespace(target='fixture-target', seed=7, mode='smoke',
                                           api_port=int(origin.rsplit(':', 1)[1]))
                    with patch.object(campaign.subprocess, 'Popen', side_effect=launch), \
                            patch.object(campaign, 'target_address', side_effect=lambda process: process.stdout.readline().strip()), \
                            patch.dict(os.environ, CPRA_AUTH_TOKEN='ambient-secret', CPRA_AUTH_TOKEN_FILE='/unrelated/token'):
                        result = campaign.run_one(args, run_dir, str(binary), count, 0, .1, upgraded)
                    self.assertEqual(result['status'], 'incomplete' if reject or cleanup_error else 'harness_smoke_pass', result)
                    self.assertEqual(len(controller_calls), 1)
                    command, options = controller_calls[0]
                    self.assertNotIn('CPRA_AUTH_TOKEN', options['env'])
                    self.assertNotIn('CPRA_AUTH_TOKEN_FILE', options['env'])
                    self.assertEqual('-runtime-config' in command, upgraded)
                    self.assertEqual(stat.S_IMODE(run_dir.stat().st_mode), 0o700)
                    self.assertFalse((run_dir / 'api.token').exists())
                    self.assertTrue(api_calls)
                    if not reject:
                        required = {'/api/v1/overview', '/api/v1/queues', '/api/v1/pools', '/api/v1/monitors'}
                        if upgraded:
                            required |= {'/api/v1/slo', '/api/v1/state', '/api/v1/history'}
                        self.assertTrue(required <= {row['path'].split('?', 1)[0] for row in api_calls})
                    self.assertTrue(all(call['authorization'] == 'Bearer ' + authorization[0] for call in api_calls))
                    self.assertTrue(all(call['authorization'] is None for call in target_calls))
                    self.assertTrue((run_dir / 'result.json').is_file())
                    for path in run_dir.iterdir():
                        if path.is_file():
                            data = gzip.decompress(path.read_bytes()) if path.suffix == '.gz' else path.read_bytes()
                            self.assertNotIn(authorization[0].encode(), data, path.name)

    def test_target_address_wait_is_bounded_and_requires_a_complete_line(self):
        programs = (
            ("print('http://127.0.0.1:12345', flush=True)", 'http://127.0.0.1:12345'),
            ("print('http://127.0.0.1:12345', end='', flush=True)", None),
            ("print('x' * 4097, end='', flush=True)", None),
        )
        for program, expected in programs:
            with self.subTest(expected=expected):
                process = subprocess.Popen([sys.executable, '-B', '-c', program + '; import time; time.sleep(10)'],
                                           stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
                try:
                    started = time.monotonic()
                    if expected:
                        self.assertEqual(campaign.target_address(process, timeout=2), expected)
                    else:
                        with self.assertRaises(RuntimeError):
                            campaign.target_address(process, timeout=.1)
                        self.assertLess(time.monotonic() - started, 2)
                finally:
                    if process.poll() is None:
                        process.kill()
                    process.wait(timeout=2)
                    process.stdout.close()


if __name__ == '__main__':
    unittest.main()
