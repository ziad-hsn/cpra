"""Small OCI byte fixtures exercise verification, not runnable image qualification."""
import copy
import gzip
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import oci
from test_evidence import manifest_fixture


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def sha(data):
    return hashlib.sha256(data).hexdigest()


def write_tar(path, files, mtime=0, extras=()):
    with tarfile.open(path, 'w') as archive:
        for name, data in files.items():
            info = tarfile.TarInfo(name)
            info.size, info.mtime, info.mode = len(data), mtime, 0o644
            archive.addfile(info, io.BytesIO(data))
        for info, data in extras:
            archive.addfile(info, io.BytesIO(data) if data is not None else None)


def image_fixture(arch='amd64', source=None, compressed=True, config_edit=None,
                  manifest_edit=None, index_edit=None, layer_data=None):
    source = manifest_fixture() if source is None else source
    raw = b'bounded fixture filesystem bytes' if layer_data is None else layer_data
    layer = gzip.compress(raw, mtime=0) if compressed else raw
    config = {'architecture': arch, 'os': 'linux',
              'rootfs': {'type': 'layers', 'diff_ids': ['sha256:' + sha(raw)]},
              'config': {'User': '1001:1001', 'WorkingDir': '/var/lib/cpra',
                         'Entrypoint': ['/usr/local/bin/cpra'], 'Labels': {
                             'org.opencontainers.image.version': source['version'],
                             'org.opencontainers.image.revision': source['commit'],
                             'org.opencontainers.image.source': 'https://github.com/ziad-hsn/cpra'}}}
    if config_edit:
        config_edit(config)
    config_bytes = encoded(config)
    descriptor = lambda data, kind: {'digest': 'sha256:' + sha(data), 'size': len(data), 'mediaType': kind}
    manifest = {'schemaVersion': 2, 'mediaType': oci.MANIFEST,
                'config': descriptor(config_bytes, oci.CONFIG),
                'layers': [descriptor(layer, 'application/vnd.oci.image.layer.v1.tar'
                                     + ('+gzip' if compressed else ''))]}
    if manifest_edit:
        manifest_edit(manifest)
    manifest_bytes = encoded(manifest)
    image = descriptor(manifest_bytes, oci.MANIFEST)
    image['platform'] = {'os': 'linux', 'architecture': arch}
    index = {'schemaVersion': 2, 'mediaType': oci.INDEX, 'manifests': [image]}
    if index_edit:
        index_edit(index)
    files = {'oci-layout': encoded({'imageLayoutVersion': '1.0.0'}), 'index.json': encoded(index),
             'blobs/sha256/' + sha(config_bytes): config_bytes,
             'blobs/sha256/' + sha(manifest_bytes): manifest_bytes,
             'blobs/sha256/' + sha(layer): layer}
    reference = {'manifest.json': encoded([{'Config': sha(config_bytes) + '.json', 'Layers': ['layer.tar']}]),
                 sha(config_bytes) + '.json': config_bytes, 'layer.tar': raw}
    return files, reference


class OCITransportTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.path = self.root / 'image.oci.tar'

    def image(self, **options):
        files, reference = image_fixture(**options)
        write_tar(self.path, files)
        return files, reference

    def test_both_platforms_read_actual_blobs_and_layer_contents(self):
        for arch in ('amd64', 'arm64'):
            for compressed in (True, False):
                with self.subTest(arch=arch, compressed=compressed):
                    files, reference = self.image(arch=arch, compressed=compressed)
                    observed = oci.read_image(self.path, 'linux/' + arch)
                    self.assertEqual(observed['oci_payload'],
                                     {name: {'sha256': sha(data), 'size': len(data)} for name, data in files.items()})
                    path = self.root / 'reference.tar'
                    write_tar(path, reference)
                    self.assertEqual(oci.docker_config_digest(path, 'linux/' + arch), observed['config_digest'])
                    self.assertEqual(observed['expanded_bytes'], len(b'bounded fixture filesystem bytes'))

    def test_transport_headers_do_not_change_the_compared_payload(self):
        files, _ = self.image()
        first = oci.read_image(self.path, 'linux/amd64')
        original = self.path.read_bytes()
        write_tar(self.path, files, mtime=123)
        self.assertNotEqual(self.path.read_bytes(), original)
        self.assertEqual(oci.read_image(self.path, 'linux/amd64'), first)

    def test_missing_or_wrong_layout_metadata_fails(self):
        for absent in ('index.json', 'oci-layout'):
            files, _ = image_fixture();del files[absent];write_tar(self.path, files)
            with self.assertRaisesRegex(ValueError, 'metadata'):
                oci.read_image(self.path, 'linux/amd64')
        files, _ = image_fixture();files['oci-layout'] = encoded({'imageLayoutVersion': '9.0'})
        write_tar(self.path, files)
        with self.assertRaisesRegex(ValueError, 'layout version'):
            oci.read_image(self.path, 'linux/amd64')

    def test_blob_paths_bind_actual_content(self):
        files, _ = image_fixture()
        name = next(name for name in files if name.startswith('blobs/'))
        files[name] += b'changed'
        write_tar(self.path, files)
        with self.assertRaisesRegex(ValueError, 'blob path'):
            oci.read_image(self.path, 'linux/amd64')

    def test_descriptors_require_existing_correctly_sized_blobs(self):
        edits = (lambda m: m['config'].update(size=1),
                 lambda m: m['config'].update(size=True),
                 lambda m: m['config'].update(digest='sha256:' + '0' * 64),
                 lambda m: m['config'].update(digest='sha512:' + '0' * 64),
                 lambda m: m['layers'][0].update(size=0))
        for edit in edits:
            self.image(manifest_edit=edit)
            with self.assertRaisesRegex(ValueError, 'descriptor|missing blob'):
                oci.read_image(self.path, 'linux/amd64')

    def test_layers_must_match_the_uncompressed_config_digests(self):
        for compressed in (True, False):
            self.image(compressed=compressed,
                       config_edit=lambda c: c['rootfs'].update(diff_ids=['sha256:' + '0' * 64]))
            with self.assertRaisesRegex(ValueError, 'uncompressed layer digest'):
                oci.read_image(self.path, 'linux/amd64')

    def test_rootfs_layer_counts_and_digest_types_are_required(self):
        for edit in (lambda c: c.pop('rootfs'),
                     lambda c: c['rootfs'].update(type='other'),
                     lambda c: c['rootfs'].update(diff_ids=[]),
                     lambda c: c['rootfs'].update(diff_ids=[None]),
                     lambda c: c['rootfs'].update(diff_ids=['sha512:' + 'a' * 64])):
            self.image(config_edit=edit)
            with self.assertRaisesRegex(ValueError, 'root filesystem|uncompressed layer digests'):
                oci.read_image(self.path, 'linux/amd64')

    def test_config_and_descriptor_platforms_cannot_be_relabelled(self):
        for edit in (lambda c: c.update(os='windows'), lambda c: c.update(architecture='arm64')):
            self.image(config_edit=edit)
            with self.assertRaisesRegex(ValueError, 'config platform'):
                oci.read_image(self.path, 'linux/amd64')
        for value in ({}, {'os': 'linux', 'architecture': 'arm64'}, None, 'linux/amd64'):
            self.image(index_edit=lambda i: i['manifests'][0].update(platform=value))
            if value is None:
                oci.read_image(self.path, 'linux/amd64')
            else:
                with self.assertRaisesRegex(ValueError, 'descriptor platform'):
                    oci.read_image(self.path, 'linux/amd64')

    def test_multiple_images_unsupported_media_and_inline_data_fail(self):
        for edit in (lambda i: i.update(manifests=[]),
                     lambda i: i['manifests'].append(copy.deepcopy(i['manifests'][0])),
                     lambda i: i['manifests'][0].update(mediaType='application/unknown'),
                     lambda i: i['manifests'][0].update(data='unreviewed-inline-data')):
            self.image(index_edit=edit)
            with self.assertRaisesRegex(ValueError, 'platform image|media type|inline descriptor'):
                oci.read_image(self.path, 'linux/amd64')

    def test_declared_document_media_types_must_agree(self):
        for options in ({'index_edit': lambda i: i.update(mediaType=oci.MANIFEST)},
                        {'manifest_edit': lambda m: m.update(mediaType=oci.INDEX)}):
            self.image(**options)
            with self.assertRaisesRegex(ValueError, 'media type'):
                oci.read_image(self.path, 'linux/amd64')
        self.image(index_edit=lambda i: i.pop('mediaType'),
                   manifest_edit=lambda m: m.pop('mediaType'))
        oci.read_image(self.path, 'linux/amd64')

    def test_corrupt_or_truncated_gzip_layers_fail_explicitly(self):
        for body in (b'invalid gzip bytes', gzip.compress(b'layer', mtime=0)[:-4]):
            files, _ = image_fixture()
            index = json.loads(files['index.json'])
            name = 'blobs/sha256/' + index['manifests'][0]['digest'].removeprefix('sha256:')
            manifest = json.loads(files.pop(name))
            previous = 'blobs/sha256/' + manifest['layers'][0]['digest'].removeprefix('sha256:')
            del files[previous]
            manifest['layers'][0].update(digest='sha256:' + sha(body), size=len(body))
            data = encoded(manifest)
            files['blobs/sha256/' + sha(data)] = data
            files['blobs/sha256/' + sha(body)] = body
            index['manifests'][0].update(digest='sha256:' + sha(data), size=len(data))
            files['index.json'] = encoded(index)
            write_tar(self.path, files)
            with self.assertRaisesRegex(ValueError, 'corrupt compressed layer'):
                oci.read_image(self.path, 'linux/amd64')

    def test_nested_single_platform_indexes_resolve_with_bounded_depth(self):
        for depth in (1, 8, 9):
            files, _ = image_fixture();index = json.loads(files['index.json'])
            for _ in range(depth):
                data = encoded(index);name = 'blobs/sha256/' + sha(data);files[name] = data
                index = {'schemaVersion': 2, 'mediaType': oci.INDEX,
                         'manifests': [{'mediaType': oci.INDEX, 'size': len(data), 'digest': 'sha256:' + sha(data)}]}
            files['index.json'] = encoded(index);write_tar(self.path, files)
            if depth <= 8:
                oci.read_image(self.path, 'linux/amd64')
            else:
                with self.assertRaisesRegex(ValueError, 'nesting'):
                    oci.read_image(self.path, 'linux/amd64')

    def test_metadata_duplicate_fields_and_nonfinite_numbers_fail(self):
        files, _ = image_fixture();original = files['index.json']
        for data in (b'{"schemaVersion":2,' + original[1:], b'{"value":1e999}', b'{"value":NaN}', b'[]'):
            files['index.json'] = data;write_tar(self.path, files)
            with self.assertRaisesRegex(ValueError, 'duplicate JSON|non-finite|must be an object'):
                oci.read_image(self.path, 'linux/amd64')

    def test_outer_transport_paths_links_modes_and_duplicates_fail(self):
        files, _ = image_fixture()
        for name, kind, mode in (('../outside', tarfile.REGTYPE, 0o644),
                                 ('/outside', tarfile.REGTYPE, 0o644),
                                 ('blobs/../outside', tarfile.REGTYPE, 0o644),
                                 ('notice', tarfile.SYMTYPE, 0o644),
                                 ('notice', tarfile.LNKTYPE, 0o644),
                                 ('notice', tarfile.FIFOTYPE, 0o644),
                                 ('notice', tarfile.REGTYPE, 0o4644),
                                 ('directory', tarfile.DIRTYPE, 0o4755),
                                 ('index.json', tarfile.REGTYPE, 0o644)):
            info = tarfile.TarInfo(name);info.type = kind;info.mode = mode;info.size = 0
            write_tar(self.path, files, extras=[(info, None)])
            with self.assertRaisesRegex(ValueError, 'path|link, special file|privileged member'):
                oci.read_image(self.path, 'linux/amd64')

    def test_metadata_members_and_expansion_have_explicit_limits(self):
        self.image(layer_data=b'x' * 10000)
        for name, limit, message in (('METADATA_LIMIT', 1, 'metadata'),
                                     ('MEMBER_LIMIT', 1, 'members'),
                                     ('EXPANDED_LIMIT', 100, 'expanded layers'),
                                     ('ARCHIVE_LIMIT', 1, 'transport exceeds')):
            with patch.object(oci, name, limit):
                with self.assertRaisesRegex(ValueError, message):
                    oci.read_image(self.path, 'linux/amd64')

    def test_tar_extension_sizes_are_rejected_before_library_decoding(self):
        info = tarfile.TarInfo('extended metadata')
        info.type = tarfile.XHDTYPE
        info.size = oci.METADATA_LIMIT + 1
        with self.path.open('wb') as stream:
            stream.write(info.tobuf(format=tarfile.USTAR_FORMAT))
            stream.seek(info.size + 512)
            stream.write(bytes(1024))
        with patch.object(oci.tarfile, 'open') as opened:
            with self.assertRaisesRegex(ValueError, 'extended tar metadata'):
                oci.read_image(self.path, 'linux/amd64')
            opened.assert_not_called()

    def test_global_metadata_cannot_be_copied_into_every_member(self):
        with tarfile.open(self.path, 'w', format=tarfile.PAX_FORMAT, pax_headers={'operator': 'fixture'}) as archive:
            archive.addfile(tarfile.TarInfo('fixture'))
        with patch.object(oci.tarfile, 'open') as opened:
            with self.assertRaisesRegex(ValueError, 'global tar metadata'):
                oci.read_image(self.path, 'linux/amd64')
            opened.assert_not_called()

    def test_physical_extension_count_and_total_bytes_are_bounded(self):
        header = tarfile.TarInfo('metadata')
        header.type = tarfile.GNUTYPE_LONGNAME
        header.size = 2
        data = header.tobuf(format=tarfile.USTAR_FORMAT) + b'a\x00' + bytes(510)
        self.path.write_bytes(data * 3 + bytes(1024))
        for limit, value, message in (('MEMBER_LIMIT', 2, 'physical headers'),
                                       ('METADATA_LIMIT', 5, 'extended tar metadata')):
            with patch.object(oci, limit, value), patch.object(oci.tarfile, 'open') as opened:
                with self.assertRaisesRegex(ValueError, message):
                    oci.read_image(self.path, 'linux/amd64')
                opened.assert_not_called()

    def test_sparse_pax_and_truncated_payloads_fail_before_library_decoding(self):
        info = tarfile.TarInfo('sparse fixture')
        info.pax_headers = {'GNU.sparse.major': '1', 'GNU.sparse.minor': '0',
                            'GNU.sparse.name': 'sparse file', 'GNU.sparse.realsize': '100000000'}
        self.path.write_bytes(info.tobuf(format=tarfile.PAX_FORMAT) + bytes(1024))
        with patch.object(oci.tarfile, 'open') as opened:
            with self.assertRaisesRegex(ValueError, 'sparse outer metadata'):
                oci.read_image(self.path, 'linux/amd64')
            opened.assert_not_called()
        info = tarfile.TarInfo('truncated file')
        info.size = 4096
        self.path.write_bytes(info.tobuf(format=tarfile.USTAR_FORMAT) + bytes(1024))
        with patch.object(oci.tarfile, 'open') as opened:
            with self.assertRaisesRegex(ValueError, 'truncated declared tar payload'):
                oci.read_image(self.path, 'linux/amd64')
            opened.assert_not_called()

    def test_reference_config_requires_one_correct_platform_and_safe_path(self):
        _, reference = image_fixture();path = self.root / 'reference.tar'
        for value in ([], [{}], [{'Config': '../key'}], [{'Config': 'absent'}]):
            files = {**reference, 'manifest.json': encoded(value)};write_tar(path, files)
            with self.assertRaisesRegex(ValueError, 'reference|one image'):
                oci.docker_config_digest(path, 'linux/amd64')
        write_tar(path, reference)
        with self.assertRaisesRegex(ValueError, 'reference config platform'):
            oci.docker_config_digest(path, 'linux/arm64')

    def test_retention_is_exclusive_private_and_records_actual_bytes(self):
        self.image();destination = self.root / 'retained.tar'
        record = oci.copy_transport(self.path, destination)
        self.assertEqual(record, {'path': destination.name, 'sha256': sha(self.path.read_bytes()),
                                  'size': self.path.stat().st_size})
        self.assertEqual(destination.read_bytes(), self.path.read_bytes())
        self.assertEqual(destination.stat().st_mode & 0o777, 0o600)
        with self.assertRaises(FileExistsError):oci.copy_transport(self.path, destination)

    def test_transport_symlinks_missing_files_and_unknown_platform_fail(self):
        self.image();original = self.root / 'original.tar';self.path.rename(original)
        self.path.symlink_to(original.name)
        with self.assertRaisesRegex(ValueError, 'regular file'):
            oci.read_image(self.path, 'linux/amd64')
        with self.assertRaisesRegex(ValueError, 'unsupported release platform'):
            oci.read_image(original, 'darwin/arm64')
        with self.assertRaisesRegex(ValueError, 'regular file'):
            oci.read_image(self.root / 'absent.tar', 'linux/amd64')


if __name__ == '__main__':
    unittest.main()
