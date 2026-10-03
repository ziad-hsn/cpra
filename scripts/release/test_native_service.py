"""Native fixture contracts; supervisor execution remains a separate release gate."""
import argparse
import http.server
import json
import os
from pathlib import Path
import shutil
import ssl
import sys
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch
import urllib.error
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'native'))
import service_lifecycle as service


def access(epoch='initial', revision='revision', principal='native/initial', outcome='observed', output=None):
    report = dict(epoch=epoch, revision=revision, intendedRevision=revision, outcome=outcome,
                  bootstrapConsumed=True, resetRequired=False, anonymousLoopback=False, legacyReadEnabled=False,
                  principals=[dict(id=principal, role='operator', revoked=False)])
    if output is not None:
        report['tokenOutput'] = str(output)
    return report


def response(value):
    return argparse.Namespace(stdout=json.dumps(value))


class ServiceAuthenticationTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix='cpra-service-contract-')
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        args = argparse.Namespace(stage=self.root, scope='user', out=self.root / 'report.json',
                                  openssl=None, allow_system_changes=False, isolated_container=False)
        self.h = service.Harness(args)
        self.h.os = 'Linux'
        self.h.config, self.h.state, self.h.fixture = (self.root / name for name in ('config', 'state', 'fixture'))
        for path in (self.h.config, self.h.state, self.h.fixture):
            path.mkdir(mode=0o700)
        self.output = self.h.fixture / 'new.token'

    def test_local_verification_refuses_other_scopes_platforms_and_root_before_commands(self):
        self.h.args.local_user = True
        for system, scope, uid in (('Darwin', 'user', 1000), ('Windows', 'system', 1000),
                                   ('Linux', 'system', 1000), ('Linux', 'user', 0)):
            with self.subTest(system=system, scope=scope, uid=uid):
                self.h.os, self.h.scope = system, scope
                with patch.object(service.os, 'geteuid', return_value=uid), \
                        patch.object(self.h, 'run') as execute, \
                        self.assertRaisesRegex(RuntimeError, 'non-root Linux user session'):
                    self.h.guard()
                execute.assert_not_called()
                self.assertFalse(self.h.owned)

    def test_local_verification_does_not_create_user_sessions_or_enable_lingering(self):
        self.h.args.local_user = True
        self.h.run = Mock(return_value=argparse.Namespace(returncode=3))
        with self.assertRaisesRegex(RuntimeError, 'existing user manager'):
            self.h.prepare_linux_user_manager()
        self.h.run.assert_called_once()
        self.assertEqual(self.h.run.call_args.args[0],
                         ['systemctl', 'is-active', f'user@{os.getuid()}.service'])
        self.assertFalse(self.h.enabled_linger)
        self.assertFalse(self.h.user_manager_started)

    def test_local_verification_observes_existing_manager_without_exposing_environment(self):
        self.h.args.local_user = True
        self.h.run = Mock(return_value=argparse.Namespace(returncode=0))
        self.h.systemctl = Mock()
        self.h.prepare_linux_user_manager()
        self.h.run.assert_called_once()
        self.h.systemctl.assert_called_once_with('is-system-running', acceptable=(0, 1))
        self.assertFalse(self.h.enabled_linger)
        self.assertFalse(self.h.user_manager_started)

    def test_native_ci_does_not_accept_unfrozen_local_candidate(self):
        self.h.args.allow_system_changes = True
        metadata = {'schema_version': 1, 'version': 'v0.1.0-rc.1', 'commit': 'a' * 40,
                    'candidate': True, 'dirty': True}
        (self.h.stage / 'RELEASE.json').write_text(json.dumps(metadata))
        self.h.run = Mock(return_value=argparse.Namespace(stdout='OpenSSL fixture'))
        self.h.ctl = Mock(side_effect=AssertionError('candidate was executed'))
        with patch.dict(os.environ, {'CI': 'true', 'GITHUB_ACTIONS': 'true'}), \
                patch.object(service.os, 'geteuid', return_value=1000), \
                patch.object(service.shutil, 'which', return_value=sys.executable), \
                self.assertRaisesRegex(ValueError, 'clean, frozen source'):
            self.h.guard()
        self.h.ctl.assert_not_called()
        self.assertFalse(self.h.owned)

    def test_linux_crash_requires_an_active_owner_before_signalling(self):
        self.h.status = Mock(return_value={'running': False, 'pid': 0})
        self.h.systemctl = Mock()
        with self.assertRaisesRegex(RuntimeError, 'installed service owner'):
            self.h.verify_linux_crash_recovery('node')
        self.h.systemctl.assert_not_called()

    def test_cleanup_observes_registration_before_stopping_its_owned_user_manager(self):
        self.h.owned = True
        self.h.user_manager_started = True
        self.h.roots = [self.h.config, self.h.state]
        self.h.stop = Mock()
        self.h.systemctl = Mock()
        calls = []
        self.h.run = Mock(side_effect=lambda command: calls.append(command))
        def registration():
            self.assertEqual(calls, [], 'user manager was stopped before registration was observed')
            return {'registered': False}
        self.h.status = Mock(side_effect=registration)
        self.h.cleanup()
        self.h.status.assert_called_once()
        self.assertEqual(calls, [['sudo', '-n', 'systemctl', 'stop', f'user@{os.getuid()}.service']])
        self.assertEqual(self.h.record['cleanup'],
                         {'status': 'pass', 'remaining_owned_paths': [], 'supervisor_registered': False})

    def test_previous_report_is_preserved_without_constructing_or_running_harness(self):
        report = self.root / 'report.json'
        report.write_text('original retained evidence')
        args = ['service_lifecycle.py', '--stage', str(self.root), '--out', str(report)]
        with patch.object(sys, 'argv', args), patch.object(service, 'Harness') as constructor, \
                self.assertRaises(SystemExit) as failure:
            service.main()
        self.assertEqual(failure.exception.code, 2)
        constructor.assert_not_called()
        self.assertEqual(report.read_text(), 'original retained evidence')

    def test_uncertain_or_mismatched_commit_is_not_retried(self):
        for change in ({'outcome': 'unknown'}, {'revision': 'wrong'}, {'tokenOutput': '/wrong'}, {'epoch': ''}):
            with self.subTest(change=change):
                report = access(outcome='committed', output=self.output)
                report.update(change)
                self.h.ctl = Mock(return_value=response(report))
                with self.assertRaisesRegex(RuntimeError, 'not confirmed'):
                    self.h.provision_access('bootstrap', 'native/initial', self.output)
                self.assertEqual(self.h.ctl.call_count, 1)
                self.assertFalse(hasattr(self.h, 'token_file'))

    def test_readback_must_match_exact_commit_and_only_the_requested_grant(self):
        changes = ({'epoch': 'another'}, {'revision': 'another'}, {'resetRequired': True},
                   {'legacyReadEnabled': True}, {'anonymousLoopback': True},
                   {'principals': []}, {'principals': ['malformed']},
                   {'principals': [dict(id='other', role='operator', revoked=False)]},
                   {'principals': [dict(id='native/initial', role='reader', revoked=False)]},
                   {'principals': [dict(id='native/initial', role='operator', revoked=True)]})
        for change in changes:
            with self.subTest(change=change):
                current = access(); current.update(change)
                self.h.ctl = Mock(side_effect=[response(access(outcome='committed', output=self.output)), response(current)])
                with self.assertRaises(RuntimeError):
                    self.h.provision_access('bootstrap', 'native/initial', self.output)
                self.assertEqual(self.h.ctl.call_count, 2)

    def test_missing_restore_reset_or_reused_epoch_prevents_mutation(self):
        self.h.initial_access = access()
        for change in ({'resetRequired': False}, {'resetRequired': True, 'epoch': 'initial'},
                       {'resetRequired': True, 'epoch': ''}, {'resetRequired': True, 'outcome': 'unknown'}):
            with self.subTest(change=change):
                current = access(epoch='reset'); current.update(change)
                self.h.ctl = Mock(return_value=response(current))
                with self.assertRaisesRegex(RuntimeError, 'new epoch'):
                    self.h.reprovision_restored_access()
                self.assertEqual(self.h.ctl.call_count, 1)

    def test_restore_commits_once_and_keeps_the_reset_epoch(self):
        self.h.initial_access = access()
        self.h.certificate = self.root / 'certificate.pem'
        self.h.certificate.write_bytes(b'public test certificate')
        output = self.h.fixture / 'access/restored.token'
        restored = access(epoch='reset', revision='reset-revision'); restored['resetRequired'] = True
        current = access(epoch='reset', revision='new-revision', principal='native/restored')
        committed = dict(current, outcome='committed', tokenOutput=str(output))
        self.h.ctl = Mock(side_effect=[response(restored), response(committed), response(current)])
        self.h.reprovision_restored_access()
        self.assertEqual(self.h.ctl.call_count, 3)
        self.assertEqual(self.h.ctl.call_args_list[1].args[:3], ('auth', 'reprovision', 'native/restored'))
        self.assertEqual(self.h.record['authentication']['reset_epoch'], 'reset')
        self.assertEqual(self.h.record['authentication']['restored_epoch'], 'reset')
        self.assertEqual(self.h.token_file, output)

    def test_reprovision_cannot_select_a_different_epoch(self):
        self.h.ctl = Mock(side_effect=[response(access(outcome='committed', output=self.output)), response(access())])
        with self.assertRaisesRegex(RuntimeError, 'restore epoch'):
            self.h.provision_access('reprovision', 'native/initial', self.output, expected_epoch='restored')
        self.assertFalse(hasattr(self.h, 'token_file'))

    def prepare_tls(self):
        self.h.openssl = shutil.which('openssl')
        self.assertIsNotNone(self.h.openssl, 'OpenSSL is required for the TLS fixture')
        (self.h.config / 'runtime.yaml').write_text('{}')
        (self.h.config / 'auth.token').write_text('obsolete-startup-token')
        def ctl(*args):
            if args[:2] == ('encryption', 'init'):
                output = Path(args[3])
                output.write_bytes(os.urandom(32)); output.chmod(0o600)
                return response({'key_id': 'fixture-key-identity'})
            if args[:2] == ('encryption', 'inspect'):
                return response({'key_id': 'fixture-key-identity'})
            if args[:2] == ('auth', 'bootstrap'):
                output = Path(args[-1]); output.write_text('initial-test-token'); output.chmod(0o600)
                return response(access(outcome='committed', output=output))
            if args[:2] == ('auth', 'list'):
                return response(access())
            if args == ('init',):
                return response({})
            raise AssertionError('unexpected fixture administration')
        with patch.object(self.h, 'ctl', side_effect=ctl):
            self.h.prepare_management()
        return self.h

    def test_key_initialization_requires_a_matching_safe_inspection(self):
        key = self.h.config / 'wrapping.key'
        key.write_bytes(os.urandom(32))
        created = {'key_id': 'key-identity'}
        for invalid in (None, {}, {'key_id': ''}, {'key_id': 42}, {'key_id': 'x' * 129},
                        {'key_id': 'key-identity', 'raw_key': 'must-not-be-accepted'}):
            with self.subTest(created=invalid):
                self.h.ctl = Mock(return_value=response(invalid))
                with self.assertRaisesRegex(RuntimeError, 'safe identity'):
                    self.h.prepare_encryption(key)
                self.assertEqual(self.h.ctl.call_count, 1)
                self.assertNotIn('encryption', self.h.record)
        for observed in (None, {}, {'key_id': 'different'}, {'key_id': 'key-identity', 'raw_key': 'extra'}):
            with self.subTest(observed=observed):
                self.h.ctl = Mock(side_effect=[response(created), response(observed)])
                with self.assertRaisesRegex(RuntimeError, 'did not confirm'):
                    self.h.prepare_encryption(key)
                self.assertEqual(self.h.ctl.call_count, 2)
                self.assertNotIn('encryption', self.h.record)

    def test_key_initialization_preserves_declared_reader_and_does_not_repair_a_failure(self):
        key = self.h.config / 'wrapping.key'
        key.write_bytes(os.urandom(32))
        for policy, expected in (({}, ()), ({'reader_group_id': 0}, ('--reader-group-id', '0')),
                                 ({'reader_sid': 'service-sid'}, ('--reader-sid', 'service-sid'))):
            with self.subTest(policy=policy):
                self.h.reader_policy = policy
                self.h.ctl = Mock(return_value=response({'key_id': 'key-identity'}))
                self.h.prepare_encryption(key)
                args = ('--key-file', key, '--data-dir', self.h.state, *expected)
                self.h.ctl.assert_any_call('encryption', 'init', *args)
                self.h.ctl.assert_any_call('encryption', 'inspect', *args)
                self.assertEqual(self.h.ctl.call_count, 2)
                self.assertTrue(self.h.record['encryption']['inspection_matches'])
        before = key.read_bytes()
        self.h.ctl = Mock(side_effect=RuntimeError('initialization outcome uncertain'))
        with self.assertRaisesRegex(RuntimeError, 'uncertain'):
            self.h.prepare_encryption(key)
        self.assertEqual(self.h.ctl.call_count, 1)
        self.assertEqual(key.read_bytes(), before)

    def test_management_sources_are_protected_and_outside_the_store(self):
        h = self.prepare_tls()
        settings = json.loads((h.config / 'runtime.yaml').read_text())
        self.assertTrue(settings['management']['enabled'])
        for name in ('wrapping.key', 'server.key', 'server.pem'):
            source = h.config / 'private' / name
            self.assertFalse(source.is_relative_to(h.state))
            self.assertEqual(source.stat().st_mode & 0o777, 0o600)
        self.assertEqual((h.config / 'private/wrapping.key').stat().st_size, 32)
        self.assertFalse(h.token_file.is_relative_to(h.state))
        self.assertFalse(h.token_file.is_relative_to(h.config))

    def test_tls_requests_use_selected_access_refuse_redirects_and_bound_responses(self):
        h = self.prepare_tls()
        calls = []
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                calls.append((self.path, self.headers.get('Authorization')))
                if self.path == '/redirect':
                    self.send_response(302); self.send_header('Location', '/unexpected'); self.end_headers(); return
                if self.headers.get('Authorization') != 'Bearer initial-test-token':
                    self.send_response(401); self.end_headers(); return
                self.send_response(200); self.end_headers()
                self.wfile.write(b' ' * 65537 if self.path == '/large' else b'{"available":true}')
            def log_message(self, *args):
                pass
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(h.certificate, h.tls_key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        self.addCleanup(lambda: (server.shutdown(), server.server_close(), thread.join(5)))
        h.api_origin = 'https://127.0.0.1:' + str(server.server_port)
        with patch.dict(os.environ, {'https_proxy': 'http://127.0.0.1:1', 'HTTPS_PROXY': 'http://127.0.0.1:1', 'no_proxy': '', 'NO_PROXY': ''}):
            self.assertTrue(h.request('/ready')['available'])
        with self.assertRaises(urllib.error.HTTPError) as redirect:
            h.request('/redirect')
        redirect.exception.close()
        self.assertEqual(redirect.exception.code, 302)
        self.assertNotIn('/unexpected', [path for path, _ in calls])
        with self.assertRaisesRegex(RuntimeError, 'response limit'):
            h.request('/large')
        with self.assertRaises(urllib.error.HTTPError) as unauthorized:
            h.request('/ready', token_file=h.config / 'auth.token')
        unauthorized.exception.close()
        self.assertEqual(unauthorized.exception.code, 401)
        h.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
                                               urllib.request.HTTPSHandler(context=ssl.create_default_context()))
        with self.assertRaises(urllib.error.URLError):
            h.request('/ready')

    def test_restore_proof_requires_new_operator_and_exact_401_rejections(self):
        self.h.principal = 'native/restored'; self.h.initial_token = self.root / 'initial.token'
        for status in (200, 403, 503):
            with self.subTest(status=status):
                self.h.record['authentication'] = {}
                effect = [dict(principalId='native/restored', role='operator')]
                effect.append({} if status == 200 else urllib.error.HTTPError('https://localhost', status, '', {}, None))
                self.h.request = Mock(side_effect=effect)
                with self.assertRaises(RuntimeError):
                    self.h.verify_restored_access()
        self.h.request = Mock(side_effect=[dict(principalId='native/restored', role='operator'),
            urllib.error.HTTPError('https://localhost', 401, '', {}, None),
            urllib.error.HTTPError('https://localhost', 401, '', {}, None)])
        self.h.verify_restored_access()
        self.assertEqual(self.h.record['authentication']['old_named_status'], 401)
        self.assertEqual(self.h.record['authentication']['startup_token_status'], 401)


class ServiceFilesystemTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix='cpra-service-permissions-')
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        args = argparse.Namespace(stage=self.root, scope='user', out=self.root / 'report.json',
                                  openssl=None, allow_system_changes=False, isolated_container=False)
        self.h = service.Harness(args)
        self.h.os = 'Linux'
        self.h.config, self.h.state = self.root / 'config', self.root / 'state'
        self.h.installed, self.h.unit = self.root / 'bin/cpra', self.root / 'cpra.service'
        for path in (self.h.config, self.h.state, self.h.installed.parent):
            path.mkdir(mode=0o700)
        for path in (self.h.config / 'monitors.yaml', self.h.config / 'runtime.yaml',
                     self.h.config / 'auth.token', self.h.unit):
            path.write_bytes(b'unchanged fixture configuration')
            path.chmod(0o600)
        self.h.installed.write_bytes(b'exact staged executable fixture')
        self.h.installed.chmod(0o700)
        self.h.record['binary_sha256'] = service.digest(self.h.installed)
        self.h.status = Mock(return_value={'pid': 'fixture-pid'})
        self.h.run = Mock(return_value=argparse.Namespace(stdout=str(os.geteuid())))

    def test_private_user_objects_are_observed_from_filesystem(self):
        self.h.verify_permissions('initial_startup')
        step = self.h.record['steps'][-1]
        self.assertEqual(step['phase'], 'initial_startup')
        self.assertEqual(step['service_gid'], os.getegid())
        self.assertEqual(step['filesystem']['binary_directory']['mode'], 0o700)
        self.assertEqual(step['filesystem']['api_token']['mode'], 0o600)
        self.assertEqual(step['filesystem']['service_definition']['type'], 'regular')
        self.h.run.assert_called_once_with(['ps', '-o', 'uid=', '-p', 'fixture-pid'])

    def test_writable_binary_parent_or_public_token_is_rejected(self):
        for path in (self.h.installed.parent, self.h.config / 'auth.token', self.h.config, self.h.unit):
            with self.subTest(path=path.name):
                mode = path.stat().st_mode & 0o777
                path.chmod(0o777 if path.is_dir() else 0o666)
                try:
                    with self.assertRaisesRegex(RuntimeError, 'native service contract'):
                        self.h.verify_permissions('running_update')
                    self.assertEqual(self.h.record['steps'], [])
                finally:
                    path.chmod(mode)

    def test_linked_configuration_does_not_count_as_protected_regular_file(self):
        token = self.h.config / 'auth.token'
        outside = self.root / 'outside.token'
        token.rename(outside)
        token.symlink_to(outside)
        with self.assertRaisesRegex(RuntimeError, 'native service contract'):
            self.h.verify_permissions('restored_startup')
        self.assertEqual(self.h.record['steps'], [])

    def test_process_identity_must_match_observed_user_ownership(self):
        self.h.run.return_value.stdout = str(os.geteuid() + 1)
        with self.assertRaisesRegex(RuntimeError, 'wrong user'):
            self.h.verify_permissions('initial_startup')
        self.assertEqual(self.h.record['steps'], [])


if __name__ == '__main__':
    unittest.main()
