"""Harness contracts; these tests never qualify Kubernetes or provider execution."""
import http.client
from http.server import BaseHTTPRequestHandler, HTTPServer
from contextlib import nullcontext
import json
from pathlib import Path
import subprocess
import ssl
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import yaml

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts/packaging'))
import helm_management as managed


def chart_fixture(runner=None):
    args = SimpleNamespace(namespace='cpra-fixture', image='cpra:test', access_mode='ReadWriteOncePod',
                           storage_class='fixture-only', helm='helm3', upgrade_helm='helm4',
                           kubeconfig='/private/test-config', context='kind-cpra-fixture')
    return managed.ManagedChart(args, ['kubectl'], runner or Mock(), Mock(), Mock(), [],
                                {'repository': 'cpra', 'tag': 'test', 'pullPolicy': 'Never'}, 'fixture-hashes')


class ManagementTransportTests(unittest.TestCase):
    def setUp(self):
        self.requests = []
        self.status, self.body = 200, b'{"items":[]}'
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.respond()

            def do_POST(self):
                self.respond()

            def respond(self):
                fixture.requests.append((self.command, self.path, dict(self.headers),
                                         self.rfile.read(int(self.headers.get('Content-Length', '0')))))
                self.send_response(fixture.status)
                self.send_header('Content-Length', str(len(fixture.body)))
                self.send_header('Location', '/never-follow-this')
                self.end_headers()
                self.wfile.write(fixture.body)

            def log_message(self, *_):
                pass

        self.server = HTTPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()
        self.client = http.client.HTTPConnection('127.0.0.1', self.server.server_port, timeout=2)

    def tearDown(self):
        self.client.close()
        self.server.shutdown()
        self.thread.join(timeout=2)
        self.server.server_close()
        self.assertFalse(self.thread.is_alive())

    def test_one_request_preserves_conditional_headers_and_json(self):
        value, headers = managed.response(self.client, 'POST', '/api/v2/monitors', 'private-bearer',
                                           {'spec': {'enabled': False}}, {'If-None-Match': '*'})
        self.assertEqual(value, {'items': []})
        self.assertEqual(headers['location'], '/never-follow-this')
        self.assertEqual(len(self.requests), 1)
        method, path, sent, body = self.requests[0]
        self.assertEqual((method, path), ('POST', '/api/v2/monitors'))
        self.assertEqual(sent['If-None-Match'], '*')
        self.assertEqual(sent['Authorization'], 'Bearer private-bearer')
        self.assertEqual(json.loads(body), {'spec': {'enabled': False}})

    def test_redirects_and_ambiguous_mutations_are_not_retried_or_logged(self):
        for status in (301, 307, 500, 503):
            with self.subTest(status=status):
                self.status, self.body = status, b'{"private":"do-not-echo"}'
                before = len(self.requests)
                with self.assertRaises(RuntimeError) as error:
                    managed.response(self.client, 'POST', '/api/v2/credentials', 'do-not-echo', data={'value': 'do-not-echo'})
                self.assertNotIn('do-not-echo', str(error.exception))
                self.assertEqual(len(self.requests), before + 1)
                self.assertNotEqual(self.requests[-1][1], '/never-follow-this')

    def test_invalid_and_oversized_bodies_fail_explicitly(self):
        for body in (b'private-response-is-not-json', b'x' * ((2 << 20) + 1)):
            with self.subTest(bytes=len(body)):
                self.body = body
                with self.assertRaises(RuntimeError) as error:
                    managed.response(self.client, 'GET', '/api/v2/history', 'private-bearer')
                self.assertNotIn('private-response', str(error.exception))


class ManagementHarnessTests(unittest.TestCase):
    def test_authentication_admin_mounts_no_provider_or_encryption_secrets(self):
        pod = managed.admin_pod('cpra:test', 'state-owner')
        self.assertFalse(pod['spec']['automountServiceAccountToken'])
        self.assertEqual(pod['spec']['securityContext']['runAsUser'], 1001)
        self.assertEqual({item['name'] for item in pod['spec']['volumes']}, {'state', 'tmp'})
        container = pod['spec']['containers'][0]
        self.assertTrue(container['securityContext']['readOnlyRootFilesystem'])
        self.assertEqual(container['securityContext']['capabilities']['drop'], ['ALL'])
        self.assertEqual([item['mountPath'] for item in container['volumeMounts']], ['/var/lib/cpra', '/tmp'])
        self.assertFalse(any(item.get('secret') or item.get('projected') for item in pod['spec']['volumes']))
        self.assertNotIn('cpra -', ' '.join(container['command']))

    def test_values_and_policy_contain_references_and_verifiers_only(self):
        chart = chart_fixture()
        encoded = yaml.safe_dump(chart.values)
        policy = yaml.safe_load(chart.policy(chart.operator))
        for private in (chart.read_token, chart.operator, chart.reader, chart.credential):
            self.assertNotIn(private, encoded)
            self.assertNotIn(private, json.dumps(policy))
        self.assertEqual([item['id'] for item in policy['principals']], ['team/oncall', 'team/observer'])
        self.assertNotEqual(policy['principals'][0]['token_sha256'], policy['principals'][1]['token_sha256'])
        self.assertTrue(chart.values['management']['enabled'])
        self.assertEqual(chart.values['persistence']['accessMode'], 'ReadWriteOncePod')

    def test_maintenance_waits_for_owner_deletion_without_querying_its_api(self):
        chart = chart_fixture()
        with tempfile.TemporaryDirectory() as work:
            chart.value_file = Path(work) / 'values.yaml'
            chart.maintenance()
            self.assertTrue(yaml.safe_load(chart.value_file.read_text())['maintenance'])
        commands = [call.args[0] for call in chart.run.call_args_list]
        self.assertEqual(len(commands), 2)
        self.assertIn('upgrade', commands[0])
        self.assertIn('--for=delete', commands[1])
        self.assertFalse(any('exec' in command or 'rollout' in command for command in commands))

    def test_uncertain_rotation_never_retries_or_resumes_the_owner(self):
        calls = []

        def run(command, *_, **__):
            calls.append(command)
            return subprocess.CompletedProcess(command, 0, json.dumps({'outcome': 'unconfirmed', 'revision': ''}), '')

        chart = chart_fixture(run)
        chart.maintenance, chart.apply, chart.deploy = Mock(), Mock(), Mock()
        with self.assertRaisesRegex(RuntimeError, 'authoritative receipt'):
            chart.rotate()
        mutations = [call for call in calls if 'rotate-read-token' in call or 'rotate' in call]
        self.assertEqual(len(mutations), 1)
        self.assertIn('rotate-read-token', mutations[0])
        chart.deploy.assert_not_called()
        self.assertFalse(any('delete' in call for call in calls))

    def test_interrupted_tunnel_is_reaped_and_bound_to_loopback(self):
        processes, commands = [], []
        spawn = subprocess.Popen

        def fixture(command, stdout, stderr):
            commands.append(command)
            process = spawn([sys.executable, '-B', '-c',
                             'import time; print("Forwarding from 127.0.0.1:32101 -> 8060", flush=True); time.sleep(30)'],
                            stdout=stdout, stderr=stderr)
            processes.append(process)
            return process

        with tempfile.TemporaryDirectory() as work, patch.object(managed.subprocess, 'Popen', fixture):
            with self.assertRaisesRegex(RuntimeError, 'fixture interruption'):
                with managed.tunnel(['kubectl'], 'cpra-managed-0', Path(work) / 'forward.log') as port:
                    self.assertEqual(port, 32101)
                    raise RuntimeError('fixture interruption')
        self.assertEqual(commands[0][commands[0].index('--address') + 1], '127.0.0.1')
        self.assertTrue(all(process.poll() is not None for process in processes))

    def test_failed_tunnel_does_not_start_requests_or_leave_a_process(self):
        processes = []
        spawn = subprocess.Popen

        def fixture(command, stdout, stderr):
            process = spawn([sys.executable, '-B', '-c', 'raise SystemExit(1)'], stdout=stdout, stderr=stderr)
            processes.append(process)
            return process

        with tempfile.TemporaryDirectory() as work, patch.object(managed.subprocess, 'Popen', fixture):
            with self.assertRaisesRegex(RuntimeError, 'tunnel failed'):
                with managed.tunnel(['kubectl'], 'cpra-managed-0', Path(work) / 'forward.log'):
                    self.fail('failed tunnel yielded an API connection')
        self.assertTrue(all(process.poll() is not None for process in processes))


class ManagementTLSBoundaryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix='cpra-harness-tls-')
        cls.root = Path(cls.directory.name)
        try:
            for name, san in (('valid', 'IP:127.0.0.1'), ('wrong-host', 'DNS:unrelated.example.invalid')):
                subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                                '-subj', '/CN=CPRa harness contract', '-addext', 'subjectAltName=' + san,
                                '-keyout', str(cls.root / (name + '.key')), '-out', str(cls.root / (name + '.crt'))],
                               capture_output=True, check=True, timeout=15)
        except BaseException:
            cls.directory.cleanup()
            raise

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def exercise(self, server_name, trusted_name):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                requests.append(self.headers.get('Authorization'))
                self.send_response(200)
                self.send_header('Content-Length', '2')
                self.end_headers()
                self.wfile.write(b'{}')

            def log_message(self, *_):
                pass

        server = HTTPServer(('127.0.0.1', 0), Handler)
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(self.root / (server_name + '.crt'), self.root / (server_name + '.key'))
        server.socket = tls.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        chart = chart_fixture()
        try:
            with tempfile.TemporaryDirectory() as directory:
                chart.work = Path(directory)
                (chart.work / 'ca.crt').write_bytes((self.root / (trusted_name + '.crt')).read_bytes())
                with patch.object(managed, 'tunnel', return_value=nullcontext(server.server_port)):
                    if server_name == trusted_name == 'valid':
                        self.assertEqual(chart.api('GET', '/api/v2/state')[0], {})
                        self.assertEqual(requests, ['Bearer ' + chart.operator])
                    else:
                        with self.assertRaises(ssl.SSLCertVerificationError):
                            chart.api('GET', '/api/v2/state')
                        self.assertEqual(requests, [], 'bearer reached a server with an invalid TLS identity')
        finally:
            server.shutdown()
            thread.join(timeout=2)
            server.server_close()
            self.assertFalse(thread.is_alive())

    def test_verified_tls_can_make_an_authenticated_read(self):
        self.exercise('valid', 'valid')

    def test_wrong_host_or_untrusted_certificate_receives_no_bearer(self):
        for server, trust in (('wrong-host', 'wrong-host'), ('valid', 'wrong-host')):
            with self.subTest(server=server, trust=trust):
                self.exercise(server, trust)


if __name__ == '__main__':
    unittest.main()
