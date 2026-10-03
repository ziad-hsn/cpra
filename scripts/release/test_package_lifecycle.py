"""Local harness contracts, not native package-manager qualification."""
import argparse
import http.server
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
from unittest.mock import patch

import package_lifecycle as lifecycle


class PackagePlatformTests(unittest.TestCase):
    def test_other_architecture_is_rejected_before_supervisor_or_package_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = argparse.Namespace(out=root, evidence=root / 'report.json', format='deb', arch='arm64')
            with patch.object(lifecycle.os, 'geteuid', return_value=0), \
                    patch.object(lifecycle.platform, 'system', return_value='Linux'), \
                    patch.object(lifecycle.platform, 'machine', return_value='x86_64'), \
                    patch.object(lifecycle.Path, 'exists', return_value=False), \
                    patch.object(lifecycle.Path, 'is_dir', return_value=False), \
                    patch.object(lifecycle, 'call') as command:
                with self.assertRaisesRegex(RuntimeError, 'architecture'):
                    lifecycle.exercise(args)
                command.assert_not_called()
                self.assertFalse((root / 'report.json').is_file())

    def test_platform_selection_accepts_only_the_matching_native_linux_target(self):
        for system, architecture, target, accepted in (
                ('Linux', 'x86_64', 'amd64', True), ('Linux', 'aarch64', 'arm64', True),
                ('Linux', 'x86_64', 'arm64', False), ('Linux', 'aarch64', 'amd64', False),
                ('Linux', 'ppc64le', 'amd64', False), ('Darwin', 'arm64', 'arm64', False)):
            with self.subTest(system=system, architecture=architecture, target=target), \
                    patch.object(lifecycle.platform, 'system', return_value=system), \
                    patch.object(lifecycle.platform, 'machine', return_value=architecture):
                if accepted: self.assertEqual(lifecycle.validate_platform(target), architecture)
                else:
                    with self.assertRaisesRegex(RuntimeError, 'architecture'): lifecycle.validate_platform(target)

    def test_existing_or_dangling_installation_is_rejected_before_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = argparse.Namespace(out=root, evidence=root / 'report.json', format='deb', arch='amd64')
            for exists, linked in ((True, False), (False, True)):
                with self.subTest(exists=exists, linked=linked), \
                        patch.object(lifecycle.os, 'geteuid', return_value=0), \
                        patch.object(lifecycle, 'validate_platform', return_value='x86_64'), \
                        patch.object(lifecycle.Path, 'exists', return_value=exists), \
                        patch.object(lifecycle.Path, 'is_symlink', return_value=linked), \
                        patch.object(lifecycle, 'call') as command, \
                        self.assertRaisesRegex(RuntimeError, 'existing CPRa'):
                    lifecycle.exercise(args)
                command.assert_not_called()

    def test_existing_or_linked_recovery_backup_is_rejected_before_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = argparse.Namespace(out=root, evidence=root / 'report.json', format='deb', arch='amd64')
            backup = Path('/var/backups/cpra')
            for exists, linked in ((True, False), (False, True)):
                with self.subTest(exists=exists, linked=linked), \
                        patch.object(lifecycle.os, 'geteuid', return_value=0), \
                        patch.object(lifecycle, 'validate_platform', return_value='x86_64'), \
                        patch.object(lifecycle.Path, 'exists', autospec=True, side_effect=lambda path: path == backup and exists), \
                        patch.object(lifecycle.Path, 'is_symlink', autospec=True, side_effect=lambda path: path == backup and linked), \
                        patch.object(lifecycle.Path, 'is_dir', return_value=False), \
                        patch.object(lifecycle, 'call') as command, \
                        self.assertRaisesRegex(RuntimeError, 'existing CPRa'):
                    lifecycle.exercise(args)
                command.assert_not_called()


class InstalledFileTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='cpra-installed-file-contract-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.account = argparse.Namespace(pw_uid=os.getuid(), pw_gid=os.getgid())
        self.paths = {}
        templates = lifecycle.installed_files(self.account)
        for name, (_, _, _, mode, kind) in templates.items():
            path = self.root / name
            if kind == 'directory': path.mkdir()
            else: path.write_bytes(('installed fixture ' + name).encode())
            path.chmod(mode)
            self.paths[name] = (path, os.getuid(), os.getgid(), mode, kind)
        self.expected = {'binary_sha256': lifecycle.sha(self.paths['binary'][0]),
                         'cli_sha256': lifecycle.sha(self.paths['cli'][0])}

    def observe(self):
        with patch.object(lifecycle, 'installed_files', return_value=self.paths), \
                patch.object(lifecycle, 'call', side_effect=AssertionError('supervisor must not be invoked')), \
                patch.object(lifecycle, 'request', side_effect=AssertionError('API must not be invoked')):
            return lifecycle.observe_installation('fresh_install', self.expected, self.account)

    def test_actual_installed_files_are_hashed_without_running_or_mutating_them(self):
        before = {name: path.read_bytes() for name, (path, _, _, _, kind) in self.paths.items() if kind == 'regular'}
        observed = self.observe()
        self.assertEqual(observed['binary_sha256'], self.expected['binary_sha256'])
        self.assertEqual(observed['cli_sha256'], self.expected['cli_sha256'])
        self.assertNotIn('process', observed)
        for name, data in before.items(): self.assertEqual(self.paths[name][0].read_bytes(), data)

    def test_changed_installed_program_cannot_reuse_the_staged_hash(self):
        for name in ('binary', 'cli'):
            with self.subTest(program=name):
                path = self.paths[name][0]; original = path.read_bytes()
                try:
                    path.write_bytes(b'changed after package installation')
                    with self.assertRaisesRegex(RuntimeError, 'installed package executable differs'): self.observe()
                finally: path.write_bytes(original)

    def test_linked_special_wrong_owner_and_open_modes_fail_before_hashing(self):
        path, uid, gid, mode, kind = self.paths['api_token']
        before = path.read_bytes(); outside = self.root / 'outside'; outside.write_bytes(b'not a managed token')
        path.unlink(); path.symlink_to(outside)
        with self.assertRaisesRegex(RuntimeError, 'permissions differ'): self.observe()
        self.assertEqual(outside.read_bytes(), b'not a managed token')
        path.unlink(); os.mkfifo(path)
        with self.assertRaisesRegex(RuntimeError, 'permissions differ'): self.observe()
        path.unlink(); path.write_bytes(before); path.chmod(0o666)
        with self.assertRaisesRegex(RuntimeError, 'permissions differ'): self.observe()
        path.chmod(mode); self.paths['api_token'] = (path, uid + 1, gid, mode, kind)
        with self.assertRaisesRegex(RuntimeError, 'permissions differ'): self.observe()


@unittest.skipUnless(sys.platform == 'linux', 'requires Linux /proc process observations')
class ProcessObservationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='cpra-process-observation-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name); ready = self.root / 'ready'
        script = ('import ctypes,pathlib,time; '
                  'ctypes.CDLL(None).prctl(15,b"cpra ) test",0,0,0); '
                  'pathlib.Path(' + repr(str(ready)) + ').write_text("ready"); time.sleep(60)')
        self.binary = Path(sys.executable).resolve()
        self.process = subprocess.Popen([str(self.binary), '-c', script], stdout=subprocess.DEVNULL,
                                        stderr=subprocess.DEVNULL, env={'PATH': '/usr/bin:/bin', 'LANG': 'C'})
        self.addCleanup(self.stop)
        deadline = time.monotonic() + 5
        while not ready.exists():
            if self.process.poll() is not None or time.monotonic() > deadline:
                self.fail('owned process fixture did not initialize')
            time.sleep(0.01)
        self.account = argparse.Namespace(pw_uid=os.getuid(), pw_gid=os.getgid())
        self.expected = lifecycle.sha(self.binary)

    def stop(self):
        if self.process.poll() is None:
            self.process.terminate()
            try: self.process.wait(timeout=3)
            except subprocess.TimeoutExpired: self.process.kill(); self.process.wait(timeout=3)

    def test_actual_proc_executable_user_group_and_start_identity_are_observed(self):
        row = lifecycle.observe_process(self.process.pid, self.binary, self.expected, self.account)
        self.assertEqual(row['pid'], self.process.pid)
        self.assertEqual(row['uid'], os.getuid()); self.assertEqual(row['gid'], os.getgid())
        self.assertEqual(row['binary_sha256'], self.expected)
        self.assertGreater(row['start_ticks'], 0)

    def test_wrong_identity_program_or_digest_cannot_qualify_a_live_process(self):
        account = argparse.Namespace(pw_uid=os.getuid() + 1, pw_gid=os.getgid())
        with self.assertRaisesRegex(RuntimeError, 'wrong identity'):
            lifecycle.observe_process(self.process.pid, self.binary, self.expected, account)
        with self.assertRaisesRegex(RuntimeError, 'executable differs'):
            lifecycle.observe_process(self.process.pid, self.root / 'other-binary', self.expected, self.account)
        with self.assertRaisesRegex(RuntimeError, 'executable differs'):
            lifecycle.observe_process(self.process.pid, self.binary, '0' * 64, self.account)

    def test_replaced_or_dead_process_does_not_return_an_observation(self):
        started = lifecycle.process_identity(self.process.pid)
        with patch.object(lifecycle, 'process_identity', side_effect=[started, started + 1]), \
                self.assertRaisesRegex(RuntimeError, 'changed during observation'):
            lifecycle.observe_process(self.process.pid, self.binary, self.expected, self.account)
        self.stop()
        with self.assertRaises((OSError, RuntimeError)):
            lifecycle.observe_process(self.process.pid, self.binary, self.expected, self.account)
        for pid in (False, 0, -1):
            with self.assertRaisesRegex(RuntimeError, 'missing package service process'):
                lifecycle.process_identity(pid)

    def running_paths(self):
        paths = {}
        for name, (_, _, _, mode, kind) in lifecycle.installed_files(self.account).items():
            if name == 'binary': path = self.binary
            elif name == 'binary_directory': path = self.binary.parent
            else:
                path = self.root / name
                if kind == 'directory': path.mkdir()
                else: path.write_bytes(('observation fixture ' + name).encode())
                path.chmod(mode)
            info = path.stat()
            paths[name] = (path, info.st_uid, info.st_gid, info.st_mode & 0o777, kind)
        return paths

    def responses(self):
        return [{'storage': {'mode': 'raft', 'ready': True, 'single_node': True,
                             'node_id': 'fixture-node', 'committed_index': 7}},
                {'total': 0}, {name: {'tasks_submitted': 0, 'tasks_completed': 0}
                               for name in ('pulse', 'intervention', 'code')}]

    def test_running_observation_joins_actual_process_with_read_only_state_and_counters(self):
        paths = self.running_paths(); responses = self.responses()
        expected = dict(binary_sha256=self.expected, cli_sha256=lifecycle.sha(paths['cli'][0]))
        with patch.object(lifecycle, 'installed_files', return_value=paths), \
                patch.object(lifecycle, 'call', return_value=argparse.Namespace(stdout=str(self.process.pid))) as commands, \
                patch.object(lifecycle, 'request', side_effect=responses) as requests:
            row = lifecycle.observe_installation('initial_startup', expected, self.account, running=True)
        self.assertEqual(row['process']['pid'], self.process.pid)
        self.assertEqual(row['process']['binary_sha256'], self.expected)
        self.assertEqual(row['state'], responses[0]); self.assertEqual(row['monitor_count'], 0)
        self.assertEqual([call.args for call in requests.call_args_list],
                         [('/api/v1/state',), ('/api/v1/overview',), ('/api/v1/pools',)])
        self.assertEqual([call.args for call in commands.call_args_list],
                         [('systemctl', 'show', '--property=MainPID', '--value', 'cpra.service')] * 2)

    def test_provider_work_or_process_replacement_during_observation_fails(self):
        paths = self.running_paths()
        expected = dict(binary_sha256=self.expected, cli_sha256=lifecycle.sha(paths['cli'][0]))
        for counter in (1, False):
            responses = self.responses(); responses[2]['pulse']['tasks_submitted'] = counter
            with self.subTest(counter=counter), patch.object(lifecycle, 'installed_files', return_value=paths), \
                    patch.object(lifecycle, 'call', return_value=argparse.Namespace(stdout=str(self.process.pid))), \
                    patch.object(lifecycle, 'request', side_effect=responses), \
                    self.assertRaisesRegex(RuntimeError, 'dispatched provider work'):
                lifecycle.observe_installation('initial_startup', expected, self.account, running=True)
        with patch.object(lifecycle, 'installed_files', return_value=paths), \
                patch.object(lifecycle, 'call', side_effect=[argparse.Namespace(stdout=str(self.process.pid)),
                                                           argparse.Namespace(stdout=str(self.process.pid + 1))]), \
                patch.object(lifecycle, 'request', side_effect=self.responses()), \
                self.assertRaisesRegex(RuntimeError, 'changed during API observation'):
            lifecycle.observe_installation('initial_startup', expected, self.account, running=True)


class ObservationTransportTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='cpra-package-http-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name); self.token = self.root / 'auth.token'; self.token.write_text('fixture-token')
        self.requests = []
        self.body = b'{"status":"ready"}'
        self.redirect = False
        test = self
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                test.requests.append((self.path, self.headers.get('Authorization')))
                if test.redirect and self.path != '/receiver':
                    self.send_response(302); self.send_header('Location', test.origin + '/receiver'); self.end_headers()
                else:
                    self.send_response(200); self.send_header('Content-Length', str(len(test.body))); self.end_headers()
                    try: self.wfile.write(test.body)
                    except (BrokenPipeError, ConnectionResetError): pass
            def log_message(self, *args): pass
        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.origin = 'http://127.0.0.1:' + str(self.server.server_port)
        thread = threading.Thread(target=self.server.serve_forever, daemon=True); thread.start()
        self.addCleanup(lambda: (self.server.shutdown(), self.server.server_close(), thread.join(timeout=3)))
        for name, value in (('API_ORIGIN', self.origin), ('AUTH_TOKEN_FILE', self.token)):
            replacement = patch.object(lifecycle, name, value); replacement.start(); self.addCleanup(replacement.stop)

    def test_actual_loopback_json_does_not_use_ambient_proxy_settings(self):
        with patch.dict(os.environ, {'HTTP_PROXY': 'http://127.0.0.1:1', 'NO_PROXY': ''}):
            self.assertEqual(lifecycle.request('/api/v1/readyz'), {'status': 'ready'})
        self.assertEqual(self.requests, [('/api/v1/readyz', 'Bearer fixture-token')])

    def test_redirect_is_rejected_without_forwarding_the_token(self):
        self.redirect = True
        with self.assertRaises(urllib.error.HTTPError): lifecycle.request('/api/v1/readyz')
        self.assertEqual(self.requests, [('/api/v1/readyz', 'Bearer fixture-token')])

    def test_successful_http_status_cannot_hide_unavailable_readiness(self):
        self.body = b'{"status":"not ready"}'
        with self.assertRaisesRegex(RuntimeError, 'readiness response is inconsistent'):
            lifecycle.ready()

    def test_overflow_duplicate_fields_and_nonobject_responses_are_explicit_errors(self):
        for body in (b' ' * (1024 * 1024 + 1), b'{"ready":true,"ready":false}', b'[]'):
            with self.subTest(body_bytes=len(body)):
                self.body = body
                with self.assertRaises((RuntimeError, ValueError)): lifecycle.request('/api/v1/readyz')


if __name__ == '__main__':
    unittest.main()
