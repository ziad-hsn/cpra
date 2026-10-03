"""Real archive retention with controlled Docker replies; no image execution."""
import contextlib
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import image_repro
from test_evidence import manifest_fixture
from test_oci import image_fixture, write_tar


class ImageRetentionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = manifest_fixture()
        self.build_calls = []

    def invoke(self, arch='amd64', second_failure=False, wrong_config=False, malformed=False):
        stage = self.root / 'release' / ('linux_' + arch)
        stage.mkdir(parents=True)
        (stage / 'RELEASE.json').write_text(json.dumps(self.source))
        (stage / 'cpra').write_bytes(b'fixture binary; never executed')
        report = self.root / 'evidence' / ('image-repro-' + arch + '.json')
        files, reference = image_fixture(arch=arch, source=self.source)

        def run(command, **options):
            if command[:3] == ['docker', 'image', 'save']:
                write_tar(Path(command[command.index('-o') + 1]), reference)
            elif command[:3] == ['docker', 'buildx', 'build']:
                number = len(self.build_calls)
                self.build_calls.append(command)
                if number == 1 and second_failure:
                    return subprocess.CompletedProcess(command, 1, stdout='', stderr='controlled second build failure')
                fields = command[command.index('--output') + 1].split(',')
                output = Path(next(value.removeprefix('dest=') for value in fields if value.startswith('dest=')))
                payload = files
                if wrong_config and number == 1:
                    payload, _ = image_fixture(arch=arch, source=self.source,
                                              config_edit=lambda c: c.update(created='changed config'))
                if malformed:
                    payload = {key: data for key, data in files.items() if key != 'oci-layout'}
                write_tar(output, payload)
            else:
                raise AssertionError('Unexpected command: ' + repr(command))
            return subprocess.CompletedProcess(command, 0, stdout='', stderr='')

        argv = ['image_repro.py', '--release-dir', str(stage.parent), '--arch', arch,
                '--reference-image', 'fixture:' + arch, '--out', str(report)]
        with patch.object(sys, 'argv', argv), patch.object(image_repro.subprocess, 'run', side_effect=run), \
                patch.object(image_repro.subprocess, 'check_output', return_value=json.dumps([{'Id': 'fixture image identity'}]).encode()), \
                contextlib.redirect_stdout(io.StringIO()):
            code = image_repro.main()
        return code, report, json.loads(report.read_text())

    def assert_retained(self, parent, record):
        path = parent / record['path']
        self.assertTrue(path.is_file())
        self.assertEqual(record['size'], path.stat().st_size)
        self.assertEqual(record['sha256'], hashlib.sha256(path.read_bytes()).hexdigest())
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_both_architectures_keep_reference_and_independent_builds(self):
        for arch in ('amd64', 'arm64'):
            with self.subTest(arch=arch):
                self.build_calls.clear()
                code, path, report = self.invoke(arch=arch)
                self.assertEqual(code, 0)
                self.assertEqual(report['status'], 'pass')
                self.assertEqual(report['platform'], 'linux/' + arch)
                self.assertEqual(len(report['builds']), 2)
                self.assert_retained(path.parent, report['reference_image']['archive'])
                self.assertEqual(report['reference_image']['archive']['path'], path.stem + '-reference.docker.tar')
                for number, build in enumerate(report['builds']):
                    self.assert_retained(path.parent, build['archive'])
                    self.assertEqual(build['archive']['path'], path.stem + f'-build-{number}.oci.tar')
                    self.assertEqual(build['config_digests'], [report['reference_image']['config_digest']])
                self.assertNotEqual(report['builds'][0]['input_mtime'], report['builds'][1]['input_mtime'])
                self.assertEqual(report['builds'][0]['oci_payload'], report['builds'][1]['oci_payload'])
                for command in self.build_calls:
                    self.assertIn('--no-cache', command)
                    self.assertIn('--provenance=false', command)
                    self.assertIn('linux/' + arch, command)

    def test_failed_second_build_keeps_reference_first_build_and_failure_report(self):
        code, path, report = self.invoke(second_failure=True)
        self.assertEqual(code, 1)
        self.assertEqual(report['status'], 'fail')
        self.assertIn('controlled second build failure', report['error'])
        self.assertEqual(len(report['builds']), 1)
        self.assert_retained(path.parent, report['reference_image']['archive'])
        self.assert_retained(path.parent, report['builds'][0]['archive'])

    def test_wrong_second_config_keeps_both_archives_without_a_passing_result(self):
        code, path, report = self.invoke(wrong_config=True)
        self.assertEqual(code, 1)
        self.assertEqual(report['status'], 'fail')
        self.assertIn('does not match', report['error'])
        self.assertEqual(len(report['builds']), 2)
        for build in report['builds']:
            self.assert_retained(path.parent, build['archive'])
        self.assertNotIn('oci_payload', report['builds'][1])

    def test_malformed_output_is_retained_before_validation(self):
        code, path, report = self.invoke(malformed=True)
        self.assertEqual(code, 1)
        self.assertEqual(report['status'], 'fail')
        self.assertEqual(len(report['builds']), 1)
        self.assert_retained(path.parent, report['builds'][0]['archive'])
        self.assertNotIn('oci_payload', report['builds'][0])

    def test_existing_report_is_not_replaced_and_causes_no_docker_calls(self):
        _, path, _ = self.invoke()
        original = path.read_bytes()
        with patch.object(sys, 'argv', ['image_repro.py', '--release-dir', str(self.root / 'release'),
                                       '--arch', 'amd64', '--reference-image', 'fixture:amd64', '--out', str(path)]), \
                patch.object(image_repro.subprocess, 'check_output') as calls, \
                contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                image_repro.main()
        self.assertEqual(error.exception.code, 2)
        calls.assert_not_called()
        self.assertEqual(path.read_bytes(), original)


if __name__ == '__main__':
    unittest.main()
