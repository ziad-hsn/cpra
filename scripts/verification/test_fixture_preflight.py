"""Local fixture admission must not relax the large-campaign prerequisite."""
import json
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import database_local
import fixture_preflight


class FixturePreflightTests(unittest.TestCase):
    def inspect(self, *, free=7 * fixture_preflight.GIB, memory=4 * fixture_preflight.GIB, base='C:\\Ubuntu'):
        output = SimpleNamespace(stdout=json.dumps({'free': free, 'base': base}))
        with patch.object(Path, 'exists', return_value=True), \
                patch.object(Path, 'read_text', return_value='MemAvailable: '+str(memory // 1024)+' kB\n'), \
                patch.object(fixture_preflight.shutil, 'disk_usage', return_value=SimpleNamespace(free=100 * fixture_preflight.GIB)), \
                patch.object(fixture_preflight.subprocess, 'run', return_value=output):
            return fixture_preflight.inspect('postgres')

    def test_short_cached_fixture_does_not_claim_large_campaign_admission(self):
        result = self.inspect(free=6 * fixture_preflight.GIB)
        self.assertTrue(result['ready'])
        self.assertFalse(result['large_campaign_qualified'])
        self.assertEqual(result['purpose'], 'bounded_local_fixture')

    def test_insufficient_physical_space_memory_or_wrong_backing_drive_refuses_admission(self):
        for changes in ({'free': 5 * fixture_preflight.GIB - 1}, {'memory': fixture_preflight.GIB}, {'base': 'D:\\Ubuntu'}):
            with self.subTest(changes=changes):
                result = self.inspect(**changes)
                self.assertFalse(result['ready'])
                self.assertIn('reason', result)

    def test_unknown_driver_does_not_inherit_a_budget(self):
        with self.assertRaises(ValueError):
            fixture_preflight.inspect('unknown')

    def test_existing_output_and_environment_are_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'report.json'
            fixture_preflight.write_new(path, {'original': True})
            with self.assertRaises(ValueError):
                fixture_preflight.output_path(path)
            path.unlink()
            fixture_preflight.write_new(str(path) + '.environment.json', {'original': True})
            with self.assertRaises(ValueError):
                fixture_preflight.output_path(path)


class DatabaseReadinessTests(unittest.TestCase):
    def test_native_client_success_not_just_socket_acceptance(self):
        for driver, output in (('redis', 'PONG\n'), ('postgres', '1|cpra\n'), ('mongo', '1\n'),
                               ('rabbitmq', ''), ('kafka', '')):
            with self.subTest(driver=driver), patch.object(database_local.subprocess, 'run',
                return_value=SimpleNamespace(returncode=0, stdout=output)) as command:
                self.assertTrue(database_local.database_ready(driver, ['docker', 'compose']))
                self.assertEqual(command.call_args.kwargs['timeout'], 20 if driver == 'kafka' else 10)
                arguments = command.call_args.args[0]
                self.assertIn('timeout', arguments)
                self.assertIn('--kill-after=1s', arguments)

    def test_wrong_account_database_reply_and_native_client_failure_do_not_pass(self):
        for driver, output, code in (('redis', 'NOAUTH', 0), ('postgres', '1|wrong', 0),
                                      ('mongo', '0', 0), ('kafka', '', 1), ('rabbitmq', '', 1)):
            with self.subTest(driver=driver), patch.object(database_local.subprocess, 'run',
                return_value=SimpleNamespace(returncode=code, stdout=output)):
                self.assertFalse(database_local.database_ready(driver, ['docker', 'compose']))

    def test_slow_native_client_cannot_hang_readiness(self):
        with patch.object(database_local.subprocess, 'run', side_effect=subprocess.TimeoutExpired('fixture', 5)):
            self.assertFalse(database_local.database_ready('postgres', ['docker', 'compose']))

    def test_successful_readiness_does_not_retain_an_earlier_error_as_its_current_result(self):
        diagnostics = {'detail': 'earlier failure', 'timed_out': True}
        with patch.object(database_local.subprocess, 'run',
                          return_value=SimpleNamespace(returncode=0, stdout='', stderr='')):
            self.assertTrue(database_local.database_ready('kafka', ['docker', 'compose'], diagnostics))
        self.assertNotIn('detail', diagnostics)
        self.assertEqual(diagnostics['exit_code'], 0)
        self.assertFalse(diagnostics['timed_out'])

    def test_postgres_uses_authenticated_tcp_query_and_no_user_startup_file(self):
        with patch.object(database_local.subprocess, 'run',
                          return_value=SimpleNamespace(returncode=0, stdout='1|cpra')) as command:
            database_local.database_ready('postgres', ['docker', 'compose'])
            arguments = command.call_args.args[0]
            self.assertIn('PGPASSWORD=cpra-disposable', arguments)
            self.assertIn('--host=127.0.0.1', arguments)
            self.assertIn('-X', arguments)
            self.assertIn('--command=SELECT 1, current_database();', arguments)


if __name__ == '__main__':
    unittest.main()
