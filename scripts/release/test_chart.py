"""Chart input and archive boundaries; mocked Helm is not cluster evidence."""
import copy
import gzip
import io
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import yaml

import chart
import release
from test_evidence import manifest_fixture


class ChartPackageTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = self.root / 'charts/cpra'
        self.source.mkdir(parents=True)
        (self.source / 'Chart.yaml').write_text('apiVersion: v2\nname: cpra\nversion: 0.0.0\nappVersion: dev\n')
        (self.source / 'values.yaml').write_text('image:\n  repository: ghcr.io/ziad-hsn/cpra\n  tag: dev\n  digest: ""\n')
        (self.source / 'values.schema.json').write_text('{"type":"object"}\n')
        (self.source / 'README.md').write_text('Fixture chart\n')
        self.manifest = manifest_fixture()
        for path in self.source.iterdir():
            self.manifest['source_files'][path.relative_to(self.root).as_posix()] = {'mode': '100644', 'sha256': release.sha(path)}
        self.output = self.root / 'output'
        self.output.mkdir()
        self.digest = 'sha256:' + 'a' * 64
        self.patch = patch.object(chart, 'ROOT', self.root)
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def helm(self, args, **options):
        if args[1] == 'package':
            source = Path(args[2])
            destination = Path(args[4])
            with tarfile.open(destination / 'cpra-0.1.0.tgz', 'w:gz') as archive:
                for path in source.iterdir():
                    archive.add(path, arcname='cpra/' + path.name, recursive=False)
        return subprocess.CompletedProcess(args, 0)

    def package(self, *, runner=None):
        with patch.object(chart.subprocess, 'run', side_effect=runner or self.helm):
            return chart.package_chart(self.manifest, self.output, 'fixture-helm', self.digest)

    def test_package_records_source_digest_and_deterministic_archive(self):
        first = self.package()
        self.assertFalse(first['release_gate_passed'])
        self.assertTrue(first['publication_locked'])
        self.assertEqual(first['image_digest'], self.digest)
        with tarfile.open(self.output / first['archive']) as archive:
            values = yaml.safe_load(archive.extractfile('cpra/values.yaml').read())
            metadata = yaml.safe_load(archive.extractfile('cpra/Chart.yaml').read())
            for member in archive:
                self.assertEqual(member.mtime, self.manifest['source_date_epoch'])
        self.assertEqual(values['image']['digest'], self.digest)
        self.assertEqual(values['image']['tag'], '')
        self.assertEqual(metadata['annotations']['cpra.io/source-commit'], self.manifest['commit'])
        self.output = self.root / 'second'
        self.output.mkdir()
        self.assertEqual(self.package()['sha256'], first['sha256'])

    def test_bad_digest_version_epoch_or_dirty_source_never_invokes_helm(self):
        for mutate in (lambda: setattr(self, 'digest', 'latest'),
                       lambda: self.manifest.update(chart_version='../escape'),
                       lambda: self.manifest.update(chart_version=None),
                       lambda: self.manifest.update(source_date_epoch=True),
                       lambda: self.manifest.update(dirty=True)):
            original, digest = copy.deepcopy(self.manifest), self.digest
            mutate()
            with patch.object(chart.subprocess, 'run') as runner, self.assertRaises(ValueError):
                chart.package_chart(self.manifest, self.output, 'fixture-helm', self.digest)
            runner.assert_not_called()
            self.manifest, self.digest = original, digest

    def test_changed_missing_added_and_linked_sources_fail_before_execution(self):
        path = self.source / 'README.md'
        raw = path.read_bytes()
        path.write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError, 'frozen inventory'):
            self.package()
        path.write_bytes(raw)
        path.unlink()
        with self.assertRaisesRegex(ValueError, 'incomplete'):
            self.package()
        path.write_bytes(raw)
        (self.source / 'extra').write_bytes(b'extra')
        with self.assertRaisesRegex(ValueError, 'frozen inventory'):
            self.package()
        (self.source / 'extra').unlink()
        path.unlink(); path.symlink_to(self.source / 'values.yaml')
        with self.assertRaisesRegex(ValueError, 'link'):
            self.package()

    def test_existing_package_is_never_overwritten(self):
        self.package()
        path = self.output / 'cpra-chart-0.1.0.tgz'
        original = path.read_bytes()
        with self.assertRaisesRegex(ValueError, 'already exists'):
            self.package()
        self.assertEqual(path.read_bytes(), original)

    def test_output_aliases_are_rejected(self):
        alias = self.root / 'alias'
        alias.symlink_to(self.output, target_is_directory=True)
        self.output = alias
        with self.assertRaisesRegex(ValueError, 'link or alias'):
            self.package()

    def test_helm_failure_has_no_package(self):
        def failed(args, **options):
            raise subprocess.CalledProcessError(1, args)
        with self.assertRaises(subprocess.CalledProcessError):
            self.package(runner=failed)
        self.assertFalse(list(self.output.iterdir()))

    def test_compressed_archive_expansion_is_bounded_before_header_decoding(self):
        def bomb(args, **options):
            if args[1] == 'package':
                path = Path(args[4]) / 'cpra-0.1.0.tgz'
                with gzip.open(path, 'wb') as writer:
                    for _ in range(33):
                        writer.write(b'\x00' * (1024 ** 2))
            return subprocess.CompletedProcess(args, 0)
        with self.assertRaisesRegex(ValueError, 'expansion limit'):
            self.package(runner=bomb)
        self.assertFalse(list(self.output.iterdir()))

    def test_source_change_during_packaging_is_rejected(self):
        def changed(args, **options):
            result = self.helm(args, **options)
            if args[1] == 'package':
                (self.source / 'README.md').write_bytes(b'changed')
            return result
        with self.assertRaisesRegex(ValueError, 'frozen inventory'):
            self.package(runner=changed)
        self.assertFalse(list(self.output.iterdir()))

    def test_link_traversal_duplicate_and_changed_packaged_values_fail(self):
        for kind in ('link', 'traversal', 'duplicate', 'changed-values'):
            def corrupted(args, **options):
                result = self.helm(args, **options)
                if args[1] != 'package':
                    return result
                path = Path(args[4]) / 'cpra-0.1.0.tgz'
                with tarfile.open(path) as source:
                    entries = [(member, source.extractfile(member).read()) for member in source]
                with tarfile.open(path, 'w:gz') as archive:
                    for member, data in entries:
                        if kind == 'changed-values' and member.name == 'cpra/values.yaml':
                            data = b'image: {repository: other, tag: dev, digest: changed}\n'
                            member.size = len(data)
                        archive.addfile(member, io.BytesIO(data))
                    if kind == 'link':
                        member = tarfile.TarInfo('cpra/link'); member.type = tarfile.SYMTYPE; member.linkname = '/tmp'
                        archive.addfile(member)
                    elif kind == 'traversal':
                        archive.addfile(tarfile.TarInfo('cpra/../escape'))
                    elif kind == 'duplicate':
                        member, data = entries[0]; archive.addfile(member, io.BytesIO(data))
                return result
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                self.package(runner=corrupted)
            self.assertFalse(list(self.output.iterdir()))

    def test_failed_flush_removes_only_new_incomplete_output(self):
        with patch('oci_assembly.os.fsync', side_effect=OSError('failed flush')):
            with self.assertRaisesRegex(OSError, 'failed flush'):
                self.package()
        self.assertFalse(list(self.output.iterdir()))

    def test_concurrent_package_creation_is_preserved(self):
        def competed(args, **options):
            result = self.helm(args, **options)
            if args[1] == 'package':
                (self.output / 'cpra-chart-0.1.0.tgz').write_bytes(b'other owner')
            return result
        with self.assertRaises(FileExistsError):
            self.package(runner=competed)
        self.assertEqual((self.output / 'cpra-chart-0.1.0.tgz').read_bytes(), b'other owner')


if __name__ == '__main__':
    unittest.main()
