"""Real OCI/filesystem boundaries with synthetic scan documents, not image qualification."""
import base64
import copy
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

import image_sbom
import oci_assembly
import qualification
import release
from test_evidence import manifest_fixture
from test_oci_assembly import reproduction


class ImageSBOMTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.manifest = manifest_fixture()
        for name in image_sbom.SOURCE_FILES:
            self.manifest['source_files'][name] = {'mode': '100644', 'sha256': release.sha(release.ROOT / name)}
        self.metadata = self.root / 'RELEASE.json'
        self.metadata.write_bytes(release.encoded(self.manifest))
        self.reports = self.root / 'reports'; self.reports.mkdir()
        for arch in ('amd64', 'arm64'):
            reproduction(self.reports, arch, self.manifest)
        self.assembly = self.root / 'assembly'
        self.subjects = oci_assembly.assemble(self.metadata, self.reports, self.assembly)['subjects']
        self.pin, self.frontend = image_sbom.source_inputs(self.manifest, qualification.Inputs())
        self.out = self.root / 'inventories'; self.out.mkdir()
        (self.out / 'RELEASE.json').write_bytes(self.metadata.read_bytes())
        (self.out / 'dashboard-dependencies.json').write_bytes((release.ROOT / 'LICENSES/dashboard.json').read_bytes())
        (self.out / 'syft-config.yaml').write_bytes(image_sbom.CONFIG)
        for arch in ('amd64', 'arm64'):
            native, spdx = self.documents(arch)
            (self.out / (arch + '.syft.json')).write_bytes(release.encoded(native))
            (self.out / (arch + '.spdx.json')).write_bytes(release.encoded(spdx))
            (self.out / (arch + '.log')).write_bytes(b'fixture scanner output\n')
            (self.out / ('cpra-image-linux-' + arch + '.spdx.json')).write_bytes(
                image_sbom.normalized(self.manifest, arch, spdx, self.frontend))
        self.save_record()

    def documents(self, arch):
        layout = self.assembly / 'layout/blobs/sha256'
        raw = (layout / self.subjects['platforms'][arch][7:]).read_bytes()
        configuration = json.loads(raw)['config']['digest']
        config = (layout / configuration[7:]).read_bytes()
        native = {'descriptor': {'name': 'syft', 'version': self.pin['version']},
                  'artifacts': [{'id': 'fixture-' + arch, 'name': 'package-' + arch, 'version': '1.0.0'}],
                  'source': {'name': self.subjects['image'], 'version': self.manifest['version'], 'type': 'image',
                             'metadata': {'manifestDigest': self.subjects['platforms'][arch], 'imageID': configuration,
                                          'manifest': base64.b64encode(raw).decode(),
                                          'config': base64.b64encode(config).decode(), 'os': '', 'architecture': ''}}}
        spdx = {'spdxVersion': 'SPDX-2.3', 'dataLicense': 'CC0-1.0', 'SPDXID': 'SPDXRef-DOCUMENT',
                'creationInfo': {'created': '2026-10-02T00:00:00Z', 'creators': ['Tool: syft-' + self.pin['version']]},
                'documentNamespace': 'https://example.invalid/random-run', 'files': [],
                'packages': [{'SPDXID': 'SPDXRef-IMAGE', 'name': self.subjects['image'],
                              'versionInfo': self.manifest['version'],
                              'checksums': [{'algorithm': 'SHA256', 'checksumValue': self.subjects['platforms'][arch][7:]}]},
                             {'SPDXID': 'SPDXRef-fixture-' + arch, 'name': 'package-' + arch, 'versionInfo': '1.0.0'}],
                'relationships': [{'spdxElementId': 'SPDXRef-DOCUMENT', 'relationshipType': 'DESCRIBES',
                                   'relatedSpdxElement': 'SPDXRef-IMAGE'},
                                  {'spdxElementId': 'SPDXRef-IMAGE', 'relationshipType': 'CONTAINS',
                                   'relatedSpdxElement': 'SPDXRef-fixture-' + arch}]}
        return native, spdx

    def save_record(self):
        payload = {p.name: {'sha256': release.sha(p), 'size': p.stat().st_size}
                   for p in self.out.iterdir() if p.name != 'image-sbom.json'}
        record = image_sbom.record(self.manifest, self.subjects, self.pin, payload)
        (self.out / 'image-sbom.json').write_bytes(release.encoded(record))

    def verify(self):
        return image_sbom.verify(self.out, self.manifest, qualification.Inputs(),
                                 assembly=self.assembly, reports=self.reports)

    def test_retained_documents_bind_both_distinct_platforms(self):
        result = self.verify()
        self.assertEqual(result['subjects'], self.subjects)
        self.assertNotEqual(result['inventory_sha256']['amd64'], result['inventory_sha256']['arm64'])
        record = image_sbom.read_json(self.out / 'image-sbom.json')
        self.assertFalse(record['release_gate_passed'])
        self.assertTrue(record['publication_locked'])

    def test_relocated_inputs_remain_verifiable_without_producer_paths(self):
        moved = self.root / 'downloaded'; moved.mkdir()
        for name, original in (('inventories', self.out), ('assembly', self.assembly), ('reports', self.reports)):
            shutil.move(original, moved / name)
        self.out, self.assembly, self.reports = moved / 'inventories', moved / 'assembly', moved / 'reports'
        self.verify()

    def test_wrong_architecture_cannot_be_labeled_arm64(self):
        native, spdx = self.documents('amd64')
        for suffix, document in (('syft', native), ('spdx', spdx)):
            (self.out / ('arm64.' + suffix + '.json')).write_bytes(release.encoded(document))
        self.save_record()
        with self.assertRaisesRegex(ValueError, 'another platform'):
            self.verify()

    def test_original_config_and_manifest_bytes_are_required(self):
        for field in ('manifest', 'config'):
            native, spdx = self.documents('amd64')
            native['source']['metadata'][field] = base64.b64encode(b'other bytes').decode()
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'digest differs'):
                image_sbom.scan_documents(self.manifest, self.subjects, self.assembly / 'layout',
                                           'amd64', native, spdx, self.pin)

    def test_bad_metadata_and_conflicting_summaries_are_rejected(self):
        mutations = [lambda n: n['source']['metadata'].update(config='%%invalid'),
                     lambda n: n['source']['metadata'].update(manifest=''),
                     lambda n: n['source']['metadata'].update(imageID='sha256:' + '0' * 64),
                     lambda n: n['source']['metadata'].update(architecture='arm64'),
                     lambda n: n['source']['metadata'].update(os='windows'),
                     lambda n: n['source'].update(version='v99.0.0'),
                     lambda n: n['descriptor'].update(version='0.0.0'),
                     lambda n: n.update(source=None)]
        for mutate in mutations:
            native, spdx = self.documents('amd64'); mutate(native)
            with self.subTest(mutation=mutate), self.assertRaises(ValueError):
                image_sbom.scan_documents(self.manifest, self.subjects, self.assembly / 'layout',
                                           'amd64', native, spdx, self.pin)

    def test_both_formats_must_contain_the_same_package_identities(self):
        native, spdx = self.documents('amd64')
        spdx['packages'][1]['versionInfo'] = '9.0.0'
        with self.assertRaisesRegex(ValueError, 'different package identities'):
            image_sbom.scan_documents(self.manifest, self.subjects, self.assembly / 'layout',
                                       'amd64', native, spdx, self.pin)

    def test_missing_root_checksums_duplicate_ids_and_dangling_relationships_fail(self):
        mutations = [lambda s: s['packages'][0].pop('checksums'),
                     lambda s: s['packages'].append(copy.deepcopy(s['packages'][1])),
                     lambda s: s['relationships'][0].update(relatedSpdxElement='SPDXRef-missing'),
                     lambda s: s.update(packages=[None]), lambda s: s.update(spdxVersion='SPDX-3.0')]
        for mutate in mutations:
            native, spdx = self.documents('amd64'); mutate(spdx)
            with self.subTest(mutation=mutate), self.assertRaises(ValueError):
                image_sbom.scan_documents(self.manifest, self.subjects, self.assembly / 'layout',
                                           'amd64', native, spdx, self.pin)

    def test_changed_final_document_cannot_be_hidden_in_a_new_record(self):
        path = self.out / 'cpra-image-linux-amd64.spdx.json'
        document = image_sbom.read_json(path); document['packages'][0]['name'] = 'replacement'
        path.write_bytes(release.encoded(document)); self.save_record()
        with self.assertRaisesRegex(ValueError, 'original scan'):
            self.verify()

    def test_changed_frontend_and_scanner_config_fail_even_with_refreshed_hashes(self):
        for name in ('dashboard-dependencies.json', 'syft-config.yaml'):
            path = self.out / name; original = path.read_bytes()
            path.write_bytes(original + b'\n'); self.save_record()
            with self.subTest(name=name), self.assertRaises(ValueError): self.verify()
            path.write_bytes(original); self.save_record()

    def test_added_files_directories_and_links_are_rejected(self):
        extra = self.out / 'extra'; extra.mkdir()
        with self.assertRaisesRegex(ValueError, 'unexpected'): self.verify()
        extra.rmdir()
        path = self.out / 'amd64.log'; original = path.read_bytes(); path.unlink()
        external = self.root / 'external.log'; external.write_bytes(original); path.symlink_to(external)
        with self.assertRaisesRegex(ValueError, 'link or alias'): self.verify()

    def test_original_image_corruption_remains_visible(self):
        path = self.reports / 'image-repro-arm64-build-0.oci.tar'
        with path.open('ab') as writer: writer.write(b'changed')
        with self.assertRaises(ValueError): self.verify()

    def test_stale_source_or_missing_inventory_cannot_qualify(self):
        self.manifest['source_files']['LICENSES/dashboard.json']['sha256'] = '0' * 64
        with self.assertRaisesRegex(ValueError, 'retained input changed'): self.verify()
        self.manifest['source_files'].pop('LICENSES/dashboard.json')
        with self.assertRaisesRegex(ValueError, 'frozen source omits'): self.verify()

    def test_json_limits_duplicate_fields_and_nonfinite_numbers_are_explicit(self):
        path = self.root / 'invalid.json'
        for data in (b'{"field":1,"field":2}', b'{"field":NaN}'):
            path.write_bytes(data)
            with self.subTest(data=data), self.assertRaises(ValueError): image_sbom.read_json(path)
        path.write_bytes(b'{"field":123}')
        with patch.object(image_sbom, 'DOCUMENT_LIMIT', 4), self.assertRaisesRegex(ValueError, 'exceeds'):
            image_sbom.read_json(path)

    def test_normalization_removes_clock_namespace_and_array_order_variation(self):
        _, first = self.documents('amd64'); second = copy.deepcopy(first)
        second['creationInfo']['created'] = '2030-01-01T00:00:00Z'
        second['documentNamespace'] = 'https://example.invalid/another-run'
        second['packages'].reverse(); second['relationships'].reverse()
        before = copy.deepcopy(first)
        self.assertEqual(image_sbom.normalized(self.manifest, 'amd64', first, self.frontend),
                         image_sbom.normalized(self.manifest, 'amd64', second, self.frontend))
        self.assertEqual(first, before)

    def test_unreviewed_executable_is_rejected_without_execution_or_output(self):
        executable = self.root / 'unreviewed'; executable.write_bytes(b'not the pinned scanner')
        output = self.root / 'generated'
        with patch.object(image_sbom, 'scan', side_effect=AssertionError('scanner invoked')):
            with self.assertRaisesRegex(ValueError, 'retained input changed'):
                image_sbom.generate(self.metadata, self.assembly, self.reports, output, executable)
        self.assertFalse(output.exists())

    def test_output_permissions_and_exclusive_writes(self):
        path = self.root / 'exclusive'
        image_sbom.write(path, b'original')
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        with self.assertRaises(FileExistsError): image_sbom.write(path, b'replacement')
        self.assertEqual(path.read_bytes(), b'original')

    def fixture_executable(self, code):
        path = self.root / 'scanner-fixture'
        path.write_text('#!/usr/bin/python3\n' + code)
        path.chmod(0o700)
        return path

    def test_child_cannot_inherit_credentials_or_scanner_overrides(self):
        executable = self.fixture_executable('import json,os\nprint(json.dumps(dict(os.environ)))\n')
        directory = self.root / 'scan-output'; directory.mkdir()
        with patch.dict(image_sbom.os.environ, {'CPRA_API_TOKEN': 'fixture-token',
                        'AWS_SECRET_ACCESS_KEY': 'fixture-secret', 'SYFT_PLATFORM': 'windows/amd64'}):
            image_sbom.scan(executable, self.manifest, self.subjects, self.assembly / 'layout',
                            'arm64', directory, image_sbom.DEFAULT_LIMIT)
        environment = json.loads((directory / 'arm64.log').read_text())
        self.assertNotIn('CPRA_API_TOKEN', environment)
        self.assertNotIn('AWS_SECRET_ACCESS_KEY', environment)
        self.assertNotIn('SYFT_PLATFORM', environment)
        self.assertTrue(environment['XDG_CACHE_HOME'].startswith(str(directory)))
        self.assertFalse(any(p.is_dir() for p in directory.iterdir()))

    def test_fast_scanner_cannot_bypass_the_disk_quota(self):
        executable = self.fixture_executable('print("x"*4096)\n')
        directory = self.root / 'scan-output'; directory.mkdir()
        with self.assertRaisesRegex(ValueError, 'byte quota'):
            image_sbom.scan(executable, self.manifest, self.subjects, self.assembly / 'layout',
                            'amd64', directory, 128)
        self.assertFalse(any(p.is_dir() for p in directory.iterdir()))

    def test_deadline_terminates_the_owned_scanner_and_removes_its_cache(self):
        executable = self.fixture_executable('import time\ntime.sleep(60)\n')
        directory = self.root / 'scan-output'; directory.mkdir()
        with patch.object(image_sbom, 'SCAN_TIMEOUT', 0), self.assertRaisesRegex(ValueError, 'deadline'):
            image_sbom.scan(executable, self.manifest, self.subjects, self.assembly / 'layout',
                            'amd64', directory, image_sbom.DEFAULT_LIMIT)
        self.assertFalse(any(p.is_dir() for p in directory.iterdir()))

    def test_failed_generation_removes_only_its_owned_output(self):
        executable = self.fixture_executable('raise SystemExit(1)\n')
        original = image_sbom.source_inputs
        def fixture_pin(manifest, inputs):
            pin, frontend = original(manifest, inputs)
            return dict(pin, executable_sha256=release.sha(executable)), frontend
        output = self.root / 'new-output'
        keep = self.root / 'keep'; keep.write_bytes(b'preexisting')
        # This controlled pin exercises failure cleanup; it is no scanner qualification.
        with patch.object(image_sbom, 'source_inputs', side_effect=fixture_pin):
            with self.assertRaisesRegex(ValueError, 'scanner failed'):
                image_sbom.generate(self.metadata, self.assembly, self.reports, output, executable)
        self.assertFalse(output.exists())
        self.assertEqual(keep.read_bytes(), b'preexisting')


if __name__ == '__main__':
    unittest.main()
