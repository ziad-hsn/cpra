"""Source-bundle checks; no actual Go install or module compiler is invoked."""
import copy
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

import go_install
from test_evidence import manifest_fixture


class SourceInstallTests(unittest.TestCase):
    def bundle(self, path, *, change=None):
        manifest = manifest_fixture()
        files = {name: ('source fixture ' + name).encode() for name in manifest['source_files']}
        manifest['source_files'] = {name: {'mode': '100644', 'sha256': hashlib.sha256(data).hexdigest()}
                                    for name, data in files.items()}
        if change in ('executable', 'executable-lost'):
            manifest['source_files']['main.go']['mode'] = '100755'
        expected = copy.deepcopy(manifest)
        if change == 'changed':
            files['sdk/go/client.go'] = b'changed source bytes'
        elif change == 'missing':
            del files['sdk/go/worker/go.mod']
        elif change == 'extra':
            files['unexpected.go'] = b'unrecorded source'
        elif change == 'escape':
            files['../outside.go'] = b'escaping source'
        with tarfile.open(path, 'w') as archive:
            for name, content in {'RELEASE.json': json.dumps(manifest).encode(), **files}.items():
                member = tarfile.TarInfo(name); member.size = len(content); member.mode = 0o644
                if name == 'main.go' and change in ('executable', 'executable-added'):
                    member.mode = 0o755
                archive.addfile(member, io.BytesIO(content))
                if change == 'duplicate' and name == 'RELEASE.json':
                    archive.addfile(member, io.BytesIO(content))
        return expected

    def test_verified_source_omits_generated_metadata_but_keeps_nested_sources(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); bundle = root / 'source.tar'
            expected = self.bundle(bundle)
            source = root / 'source'
            manifest, identity = go_install.extract_release_source(bundle, source)
            self.assertEqual(manifest, expected)
            self.assertEqual(identity['commit'], expected['commit'])
            self.assertFalse((source / 'RELEASE.json').exists())
            self.assertTrue((source / 'sdk/go/go.mod').is_file())
            self.assertTrue((source / 'sdk/go/worker/go.mod').is_file())
            for name, record in expected['source_files'].items():
                self.assertEqual(hashlib.sha256((source / name).read_bytes()).hexdigest(), record['sha256'])

    def test_changed_missing_unrecorded_duplicate_and_escaping_files_are_rejected(self):
        for change in ('changed', 'missing', 'extra', 'duplicate', 'escape', 'executable-lost', 'executable-added'):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary); bundle = root / 'source.tar'
                self.bundle(bundle, change=change)
                with self.assertRaises(ValueError):
                    go_install.extract_release_source(bundle, root / 'source')
                self.assertFalse((root / 'outside.go').exists())

    def test_recorded_executable_mode_is_accepted(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); bundle = root / 'source.tar'
            expected = self.bundle(bundle, change='executable')
            manifest, _ = go_install.extract_release_source(bundle, root / 'source')
            self.assertEqual(manifest, expected)


if __name__ == '__main__':
    unittest.main()
