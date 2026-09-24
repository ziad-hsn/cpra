import json
import pathlib
import tempfile
import unittest
from unittest import mock
from types import SimpleNamespace

import record_fixture


class FixtureRecordingTests(unittest.TestCase):
    def test_declared_binding_is_unique_and_matches_child_command(self):
        binary, report = pathlib.Path('/tmp/verifier'), pathlib.Path('/tmp/report.json')
        command = ['python3', 'fixture.py', '--binary', str(binary), '--out', str(report)]
        record_fixture.validate_command(command, binary, report)
        record_fixture.validate_command(['python3', 'fixture.py', '--binary=' + str(binary), '--out=' + str(report)], binary, report)
        for extra in (['--binary', '/tmp/other'], ['--out', '/tmp/other.json'],
                      ['--binary=/tmp/other'], ['--out=/tmp/other.json'], ['--bin=/tmp/other'], ['--o=/tmp/other.json']):
            with self.subTest(extra=extra), self.assertRaises(ValueError):
                record_fixture.validate_command(command + extra, binary, report)

    def test_missing_uninvoked_or_failed_checks_cannot_create_passing_sidecar(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'report.json'
            for records in ([], [{'configured': False}],
                            [{'configured': True, 'status': 'fail'}],
                            [{'configured': True, 'status': 'pass', 'accepted': True, 'observed': True}]):
                path.write_text(json.dumps({'records': records}))
                self.assertFalse(record_fixture.passing_report(path))

    def test_only_invoked_accepted_observed_checks_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'report.json'
            row = {'configured': True, 'status': 'pass', 'operation_invoked': True, 'accepted': True, 'observed': True}
            path.write_text(json.dumps({'records': [row, {'configured': False}]}))
            self.assertTrue(record_fixture.passing_report(path))
            for field in ('operation_invoked', 'accepted', 'observed'):
                path.write_text(json.dumps({'records': [{**row, field: False}]}))
                self.assertFalse(record_fixture.passing_report(path))

    def test_binary_changed_during_fixture_execution_prevents_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            binary, output = pathlib.Path(directory) / 'verifier', pathlib.Path(directory) / 'report.json'
            binary.write_bytes(b'original binary')
            def changed_fixture(_command, **_kwargs):
                binary.write_bytes(b'different binary')
                output.write_text(json.dumps({'records': [{'configured': True, 'status': 'pass',
                    'operation_invoked': True, 'accepted': True, 'observed': True}]}))
                return SimpleNamespace(returncode=0)
            arguments = ['record_fixture.py', '--binary', str(binary), '--out', str(output), '--',
                         'python3', 'fixture.py', '--binary', str(binary), '--out', str(output)]
            with mock.patch('sys.argv', arguments), mock.patch.object(record_fixture.subprocess, 'run', side_effect=changed_fixture):
                self.assertEqual(record_fixture.main(), 1)
            sidecar = json.loads(pathlib.Path(str(output) + '.fixture.json').read_text())
            self.assertEqual(sidecar['status'], 'fail')
            self.assertNotEqual(sidecar['binary_sha256'], sidecar['binary_sha256_after'])


if __name__ == '__main__':
    unittest.main()
