import contextlib
import hashlib
import io
import json
import os
import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch
import zipfile

import verify
from verify import (CORE, WORKER, VERSION,
                    retain_module, verify_archive_identity, verify_documentation_archive)


class ArchiveContractTests(unittest.TestCase):
    def test_prepared_archive_content_is_stable_across_compiler_zip_encodings(self):
        record = {'Path': verify.CORE, 'Version': 'v0.1.0-rc.1', 'Sum': 'content',
                  'GoModSum': 'module', 'Requires': {}, 'ArchiveSHA256': 'encoding-one'}
        preparation = {'compiler': {'sha256': 'compiler-one'},
                       'module_archives': {verify.CORE: record}}
        different_zip = dict(record, ArchiveSHA256='encoding-two')
        verify.verify_prepared_archives({verify.CORE: different_zip}, preparation, 'compiler-two')
        with self.assertRaisesRegex(ValueError, 'same compiler'):
            verify.verify_prepared_archives({verify.CORE: different_zip}, preparation, 'compiler-one')
        with self.assertRaisesRegex(ValueError, 'content differs'):
            verify.verify_prepared_archives({verify.CORE: dict(different_zip, Sum='changed-content')},
                                            preparation, 'compiler-two')

    def test_downloaded_content_must_match_every_candidate_file(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            proxy = root / "proxy"
            candidate = proxy / CORE / "@v" / (VERSION + ".zip")
            candidate.parent.mkdir(parents=True)
            with zipfile.ZipFile(candidate, 'w') as archive:
                archive.writestr(CORE + '@' + VERSION + '/go.mod', 'module ' + CORE + '\n')
                archive.writestr(CORE + '@' + VERSION + '/client.go', 'package cpra\n')
            verify_archive_identity(proxy, CORE, str(candidate))
            for changed in (False, True):
                downloaded = root / "downloaded.zip"
                with zipfile.ZipFile(candidate) as original, zipfile.ZipFile(downloaded, "w") as archive:
                    for name in original.namelist():
                        data = original.read(name)
                        if changed and name.endswith("client.go"):
                            data += b"// changed\n"
                        archive.writestr(name, data)
                    if not changed:
                        archive.writestr(CORE + "@" + VERSION + "/unexpected.txt", "extra")
                with self.assertRaisesRegex(RuntimeError, "differs"):
                    verify_archive_identity(proxy, CORE, str(downloaded))

    def test_documentation_must_be_in_the_downloaded_archive(self):
        with tempfile.TemporaryDirectory() as temp:
            archive_path = Path(temp) / "module.zip"
            for module in (CORE, WORKER):
                names = {"README.md", "LICENSE", "go.mod", "go.sum", "doc.go", "example_test.go"}
                if module == CORE:
                    names.update(f"{package}/{name}" for package in ("api", "collection")
                                 for name in ("doc.go", "example_test.go"))
                for missing in (None, "README.md", "LICENSE", "example_test.go"):
                    with zipfile.ZipFile(archive_path, "w") as archive:
                        for name in names - {missing}:
                            archive.writestr(module + "@" + VERSION + "/" + name, "fixture\n")
                    if missing is None:
                        self.assertEqual(verify_documentation_archive(module, archive_path), sorted(names))
                    else:
                        with self.assertRaisesRegex(ValueError, "missing documentation input"):
                            verify_documentation_archive(module, archive_path)

    def test_empty_package_documentation_fails(self):
        with tempfile.TemporaryDirectory() as temp:
            archive_path = Path(temp) / "module.zip"
            with zipfile.ZipFile(archive_path, "w") as archive:
                for name in ("README.md", "LICENSE", "go.mod", "go.sum", "doc.go", "example_test.go"):
                    archive.writestr(WORKER + "@" + VERSION + "/" + name, "\n" if name == "doc.go" else "fixture\n")
            with self.assertRaisesRegex(ValueError, "empty documentation input: doc.go"):
                verify_documentation_archive(WORKER, archive_path)


class RetainedEvidenceTests(unittest.TestCase):
    def inputs(self, root, module=CORE):
        proxy = root / 'proxy'
        source = proxy / module / '@v'
        source.mkdir(parents=True)
        downloaded = {}
        for suffix, field in (('.zip', 'Zip'), ('.mod', 'GoMod'), ('.info', 'Info')):
            (source / (VERSION + suffix)).write_bytes(b'source ' + suffix.encode())
            path = root / ('downloaded' + suffix)
            path.write_bytes(b'downloaded ' + suffix.encode())
            downloaded[field] = str(path)
        directory = root / 'retained'
        directory.mkdir(mode=0o700)
        return proxy, downloaded, directory

    def test_source_and_downloaded_bytes_survive_temporary_input_removal(self):
        for module, role in ((CORE, 'sdk'), (WORKER, 'worker')):
            with self.subTest(module=module), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                proxy, downloaded, directory = self.inputs(root, module)
                records = retain_module(directory, module, VERSION, proxy, downloaded)
                for origin in ('source', 'downloaded'):
                    for suffix, field in (('.zip', 'Zip'), ('.mod', 'GoMod'), ('.info', 'Info')):
                        record = records[origin][field]
                        self.assertEqual(record['file'], role + '-' + origin + suffix)
                        expected = origin.encode() + b' ' + suffix.encode()
                        self.assertEqual(record['bytes'], len(expected))
                        self.assertEqual(record['sha256'], hashlib.sha256(expected).hexdigest())
                        path = directory / record['file']
                        self.assertEqual(path.read_bytes(), expected)
                        if os.name != 'nt':
                            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                for path in proxy.rglob('*'):
                    if path.is_file():
                        path.unlink()
                for path in downloaded.values():
                    Path(path).unlink()
                self.assertEqual(len(list(directory.iterdir())), 6)
                self.assertEqual((directory / (role + '-downloaded.zip')).read_bytes(), b'downloaded .zip')

    def test_archive_quota_and_symlink_inputs_fail_explicitly(self):
        for failure in ('quota', 'symlink'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                proxy, downloaded, directory = self.inputs(root)
                source = proxy / CORE / '@v' / (VERSION + '.zip')
                if failure == 'symlink':
                    source.unlink()
                    source.symlink_to(downloaded['Zip'])
                with patch.object(verify, 'MAX_ARCHIVE_BYTES', 1 if failure == 'quota' else 500 * 1024 ** 2):
                    with self.assertRaisesRegex(ValueError, 'byte limit|regular file'):
                        retain_module(directory, CORE, VERSION, proxy, downloaded)
                self.assertEqual(list(directory.iterdir()), [])

    def test_existing_payloads_are_never_overwritten(self):
        with tempfile.TemporaryDirectory() as temporary:
            proxy, downloaded, directory = self.inputs(Path(temporary))
            original = directory / 'sdk-source.zip'
            original.write_bytes(b'previous campaign')
            with self.assertRaises(FileExistsError):
                retain_module(directory, CORE, VERSION, proxy, downloaded)
            self.assertEqual(original.read_bytes(), b'previous campaign')

    def test_report_reserves_private_payload_directory_and_cannot_be_reused(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'consumer.json'
            report = {'status': 'pass', 'publicationReady': False}
            def consumers(go, artifacts, **kwargs):
                self.assertEqual(go, '/compiler/go')
                self.assertEqual(artifacts, output.with_name('consumer.json.modules'))
                self.assertTrue(output.exists())
                if os.name != 'nt':
                    self.assertEqual(artifacts.stat().st_mode & 0o777, 0o700)
                (artifacts / 'payload').write_bytes(b'retained input')
                return report
            with patch.object(verify, 'verify_consumers', side_effect=consumers), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(verify.verify('/compiler/go', output), report)
            before = output.read_bytes()
            self.assertEqual(json.loads(before), report)
            if os.name != 'nt':
                self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            with patch.object(verify, 'verify_consumers') as consumer:
                with self.assertRaises(FileExistsError):
                    verify.verify('/compiler/go', output)
                consumer.assert_not_called()
            self.assertEqual(output.read_bytes(), before)
            self.assertEqual((output.with_name('consumer.json.modules') / 'payload').read_bytes(), b'retained input')

    def test_failed_consumer_keeps_failure_report_and_already_retained_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'consumer.json'
            def consumers(go, artifacts, **kwargs):
                (artifacts / 'sdk-source.zip').write_bytes(b'archive from the failed campaign')
                raise RuntimeError('tagged example failed')
            with patch.object(verify, 'verify_consumers', side_effect=consumers):
                with self.assertRaisesRegex(RuntimeError, 'tagged example failed'):
                    verify.verify('/compiler/go', output, sdk_version='v0.2.0', worker_version='v0.3.0')
            report = json.loads(output.read_text())
            self.assertEqual(report['status'], 'failed')
            self.assertFalse(report['publicationReady'])
            self.assertEqual(report['versions'], {CORE: 'v0.2.0', WORKER: 'v0.3.0'})
            self.assertEqual((output.with_name('consumer.json.modules') / 'sdk-source.zip').read_bytes(),
                             b'archive from the failed campaign')

    def test_existing_payload_directory_cannot_be_adopted_as_a_new_campaign(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'consumer.json'
            artifacts = output.with_name('consumer.json.modules')
            artifacts.mkdir()
            (artifacts / 'earlier.zip').write_bytes(b'earlier payload')
            with patch.object(verify, 'verify_consumers') as consumer:
                with self.assertRaises(FileExistsError):
                    verify.verify('/compiler/go', output)
                consumer.assert_not_called()
            self.assertEqual(json.loads(output.read_text())['status'], 'failed')
            self.assertEqual((artifacts / 'earlier.zip').read_bytes(), b'earlier payload')


if __name__ == "__main__":
    unittest.main()
