"""Real archive/file operations with byte fixtures, not runnable release images."""
import copy
import hashlib
import json
import os
import shutil
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import oci
import oci_assembly
import qualification
import release
from test_evidence import manifest_fixture
from test_oci import image_fixture, write_tar


def retained(path):
    return {'path': path.name, 'sha256': release.sha(path), 'size': path.stat().st_size}


def reproduction(directory, arch, manifest, *, header_mtime=0, **options):
    files, reference = image_fixture(arch=arch, source=manifest, **options)
    name = 'image-repro-' + arch
    path = directory / (name + '-reference.docker.tar')
    write_tar(path, reference)
    config = oci.docker_config_digest(path, 'linux/' + arch)
    builds = []
    for number in range(2):
        path = directory / (name + '-build-' + str(number) + '.oci.tar')
        write_tar(path, files, mtime=header_mtime + number)
        image = oci.read_image(path, 'linux/' + arch)
        builds.append({'archive': retained(path), 'input_mtime': number + 1,
                       'oci_payload': image['oci_payload'], 'config_digests': [config],
                       'manifest_digest': image['manifest_digest'], 'expanded_bytes': image['expanded_bytes'],
                       'payload_sha256': hashlib.sha256(json.dumps(image['oci_payload'], sort_keys=True).encode()).hexdigest()})
    record = {'status': 'pass', 'platform': 'linux/' + arch, 'source': manifest,
              'release_identity': release.evidence_identity(manifest), 'builds': builds,
              'reference_image': {'id': config, 'config_digest': config,
                                  'archive': retained(directory / (name + '-reference.docker.tar'))}}
    (directory / (name + '.json')).write_bytes(release.encoded(record))
    return record


class OCIAssemblyTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.reports = self.root / 'reports'
        self.reports.mkdir()
        self.manifest = manifest_fixture()
        self.release = self.root / 'RELEASE.json'
        self.release.write_bytes(release.encoded(self.manifest))
        self.records = {arch: reproduction(self.reports, arch, self.manifest)
                        for arch in ('amd64', 'arm64')}
        self.out = self.root / 'assembled'

    def assemble(self, **options):
        return oci_assembly.assemble(self.release, self.reports, self.out, **options)

    def edit(self, arch, mutate):
        record = copy.deepcopy(self.records[arch])
        mutate(record)
        (self.reports / ('image-repro-' + arch + '.json')).write_bytes(release.encoded(record))

    def test_actual_index_and_every_blob_match_both_inputs(self):
        record = self.assemble()
        subjects = oci_assembly.verify_assembly(self.out, self.manifest, qualification.Inputs(), reports=self.reports)
        self.assertEqual(subjects, record['subjects'])
        layout = self.out / 'layout'
        root = json.loads((layout / 'index.json').read_bytes())
        descriptor = root['manifests'][0]
        index = (layout / 'blobs/sha256' / subjects['digest'][7:]).read_bytes()
        self.assertEqual(descriptor['size'], len(index))
        self.assertEqual(descriptor['digest'], 'sha256:' + hashlib.sha256(index).hexdigest())
        refs = json.loads(index)['manifests']
        self.assertEqual([ref['platform'] for ref in refs],
                         [{'os': 'linux', 'architecture': arch} for arch in ('amd64', 'arm64')])
        for arch, ref in zip(('amd64', 'arm64'), refs):
            self.assertEqual(ref['digest'], self.records[arch]['builds'][0]['manifest_digest'])
        self.assertFalse(record['release_gate_passed'])
        self.assertTrue(record['publication_locked'])

    def test_independent_assembly_roots_have_identical_payloads(self):
        first = self.assemble()
        self.out = self.root / 'second'
        second = self.assemble()
        self.assertEqual(first['payload'], second['payload'])
        for name in first['payload']:
            self.assertEqual((self.root / 'assembled' / name).read_bytes(), (self.out / name).read_bytes())

    def test_retained_artifacts_can_be_verified_after_relocation(self):
        self.assemble()
        destination = self.root / 'downloaded'
        destination.mkdir()
        shutil.copytree(self.out, destination / 'assembled')
        shutil.copytree(self.reports, destination / 'reports')
        shutil.rmtree(self.out)
        shutil.rmtree(self.reports)
        oci_assembly.verify_assembly(destination / 'assembled', self.manifest,
                                     qualification.Inputs(), reports=destination / 'reports')

    def test_shared_layer_is_copied_once(self):
        record = self.assemble()
        # Two configurations, two manifests, one shared layer, one new index.
        self.assertEqual(len(list((self.out / 'layout/blobs/sha256').iterdir())), 6)
        self.assertEqual(len(record['inputs']), 9)

    def test_input_transport_headers_do_not_change_the_index(self):
        first = self.assemble()
        for arch in ('amd64', 'arm64'):
            self.records[arch] = reproduction(self.reports, arch, self.manifest, header_mtime=999)
        self.out = self.root / 'second'
        second = self.assemble()
        self.assertEqual(first['subjects'], second['subjects'])
        self.assertNotEqual(first['inputs'], second['inputs'])

    def test_output_quota_is_enforced_before_creating_any_directory(self):
        for limit in (1, 0, -1, True):
            with self.subTest(limit=limit), self.assertRaisesRegex(ValueError, 'quota'):
                self.assemble(max_bytes=limit)
            self.assertFalse(self.out.exists())

    def test_existing_output_and_overlapping_inputs_are_preserved(self):
        self.out.mkdir()
        (self.out / 'keep').write_bytes(b'existing file')
        with self.assertRaisesRegex(ValueError, 'already exists'):
            self.assemble()
        self.assertEqual((self.out / 'keep').read_bytes(), b'existing file')
        self.out = self.reports / 'output'
        with self.assertRaisesRegex(ValueError, 'overlaps'):
            self.assemble()

    def test_input_links_and_parent_aliases_are_rejected(self):
        original = self.reports / 'image-repro-amd64-build-0.oci.tar'
        elsewhere = self.root / 'transport.tar'
        original.rename(elsewhere)
        original.symlink_to(elsewhere)
        with self.assertRaisesRegex(ValueError, 'link or alias'):
            self.assemble()
        original.unlink(); elsewhere.rename(original)
        alias = self.root / 'alias'
        alias.symlink_to(self.root, target_is_directory=True)
        self.out = alias / 'output'
        with self.assertRaisesRegex(ValueError, 'link or alias'):
            self.assemble()

    def test_dirty_source_cannot_be_used(self):
        self.manifest['dirty'] = True
        self.release.write_bytes(release.encoded(self.manifest))
        with self.assertRaisesRegex(ValueError, 'clean, frozen'):
            self.assemble()

    def test_missing_platform_and_wrong_source_do_not_produce_output(self):
        path = self.reports / 'image-repro-arm64.json'
        original = path.read_bytes()
        path.unlink()
        with self.assertRaises(OSError):
            self.assemble()
        path.write_bytes(original)
        self.edit('arm64', lambda r: r['source'].update(commit='b' * 40))
        with self.assertRaisesRegex(ValueError, 'frozen release'):
            self.assemble()
        self.assertFalse(self.out.exists())

    def test_bad_reproduction_or_runtime_configuration_is_rejected(self):
        for mutate in (lambda r: r.update(status='fail'),
                       lambda r: r.update(platform='linux/amd64'),
                       lambda r: r['builds'][1].update(input_mtime=1),
                       lambda r: r['reference_image'].update(config_digest='sha256:' + '0' * 64)):
            self.edit('arm64', mutate)
            with self.assertRaises(ValueError):
                self.assemble()
            self.assertFalse(self.out.exists())
        reproduction(self.reports, 'arm64', self.manifest,
                     config_edit=lambda c: c['config'].update(User='0:0'))
        with self.assertRaisesRegex(ValueError, 'runtime configuration'):
            self.assemble()

    def test_corrupt_original_archive_is_rejected(self):
        path = self.reports / 'image-repro-amd64-build-0.oci.tar'
        with path.open('ab') as stream:
            stream.write(b'corruption')
        with self.assertRaisesRegex(ValueError, 'retained transport bytes'):
            self.assemble()

    def test_changed_input_during_copy_removes_owned_output(self):
        original = oci_assembly.write_stream
        changed = False
        def write(path, stream, row):
            nonlocal changed
            original(path, stream, row)
            if not changed:
                changed = True
                with self.release.open('ab') as writer:
                    writer.write(b'\n')
        with patch.object(oci_assembly, 'write_stream', side_effect=write):
            with self.assertRaisesRegex(ValueError, 'input changed'):
                self.assemble()
        self.assertFalse(self.out.exists())

    def test_failed_copy_leaves_no_success_record_or_large_files(self):
        with patch.object(oci_assembly.os, 'fsync', side_effect=OSError('disk write failed')):
            with self.assertRaisesRegex(OSError, 'disk write failed'):
                self.assemble()
        self.assertFalse(self.out.exists())

    def test_verifier_rejects_changed_blob_index_subjects_and_metadata(self):
        record = self.assemble()
        names = ['OCI.json', 'RELEASE.json', 'layout/index.json',
                 next(name for name in record['payload'] if name.startswith('layout/blobs/'))]
        for name in names:
            path = self.out / name
            original = path.read_bytes()
            path.write_bytes(original + b'changed')
            with self.subTest(name=name), self.assertRaises(ValueError):
                oci_assembly.verify_assembly(self.out, self.manifest, qualification.Inputs(), reports=self.reports)
            path.write_bytes(original)

    def test_verifier_rejects_changed_original_even_with_untouched_output(self):
        self.assemble()
        self.edit('arm64', lambda r: r.update(status='fail'))
        with self.assertRaisesRegex(ValueError, 'frozen release'):
            oci_assembly.verify_assembly(self.out, self.manifest, qualification.Inputs(), reports=self.reports)

    def test_verifier_rejects_forged_inventory_and_added_files(self):
        self.assemble()
        path = self.out / 'oci-assembly.json'
        raw = path.read_bytes()
        record = json.loads(raw)
        record['payload']['OCI.json']['sha256'] = '0' * 64
        path.write_bytes(release.encoded(record))
        with self.assertRaisesRegex(ValueError, 'original image transports'):
            oci_assembly.verify_assembly(self.out, self.manifest, qualification.Inputs(), reports=self.reports)
        path.write_bytes(raw)
        (self.out / 'unexpected').write_bytes(b'extra')
        with self.assertRaisesRegex(ValueError, 'payload'):
            oci_assembly.verify_assembly(self.out, self.manifest, qualification.Inputs(), reports=self.reports)

    def test_verifier_rejects_output_symlinks(self):
        self.assemble()
        path = self.out / 'OCI.json'
        target = self.root / 'subjects.json'
        path.rename(target)
        path.symlink_to(target)
        with self.assertRaisesRegex(ValueError, 'link or alias'):
            oci_assembly.verify_assembly(self.out, self.manifest, qualification.Inputs(), reports=self.reports)

    def test_output_permissions_are_restrictive(self):
        self.assemble()
        self.assertEqual(self.out.stat().st_mode & 0o777, 0o700)
        for path in self.out.rglob('*'):
            self.assertEqual(path.stat().st_mode & 0o777, 0o700 if path.is_dir() else 0o600)

    def test_verifier_rejects_unlisted_empty_directories(self):
        self.assemble()
        (self.out / 'unexpected').mkdir()
        with self.assertRaisesRegex(ValueError, 'unexpected output directory'):
            oci_assembly.verify_assembly(self.out, self.manifest, qualification.Inputs(), reports=self.reports)


if __name__ == '__main__':
    unittest.main()
