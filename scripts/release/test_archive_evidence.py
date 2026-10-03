"""Small archive fixtures verify the reader, not native executable qualification."""
import hashlib
import io
import json
from pathlib import Path
import stat
import struct
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import warnings
import zipfile

import publish
from test_evidence import manifest_fixture


class ArchiveEvidenceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.out = Path(temporary.name)
        self.manifest = manifest_fixture()

    def entries(self, target):
        suffix = '.exe' if target.startswith('windows_') else ''
        binaries = {name + suffix: (target + '/' + name).encode()
                    for name in ('cpra', 'cpractl')}
        metadata = {
            'RELEASE.json': self.manifest,
            'DEPENDENCIES.json': {
                'target': target,
                'binaries': {name: hashlib.sha256(data).hexdigest()
                             for name, data in binaries.items()},
            },
        }
        return [(name, data, 0o755, 'file') for name, data in binaries.items()] + [
            (name, json.dumps(value).encode(), 0o644, 'file')
            for name, value in metadata.items()]

    def write(self, target, entries=None):
        entries = self.entries(target) if entries is None else entries
        windows = target.startswith('windows_')
        path = self.out / ('cpra-' + self.manifest['version'] + '-'
                           + target.replace('_', '-') + ('.zip' if windows else '.tar.gz'))
        if windows:
            with warnings.catch_warnings():
                warnings.simplefilter('ignore', UserWarning)
                with zipfile.ZipFile(path, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
                    for name, data, mode, kind in entries:
                        info = zipfile.ZipInfo(name)
                        info.create_system = 3
                        info.compress_type = zipfile.ZIP_DEFLATED
                        file_type = {'file': stat.S_IFREG, 'symlink': stat.S_IFLNK,
                                     'directory': stat.S_IFDIR, 'fifo': stat.S_IFIFO}[kind]
                        info.external_attr = (file_type | mode) << 16
                        archive.writestr(info, data)
        else:
            with tarfile.open(path, 'w:gz') as archive:
                for name, data, mode, kind in entries:
                    info = tarfile.TarInfo(name)
                    info.mode = mode
                    info.type = {'file': tarfile.REGTYPE, 'symlink': tarfile.SYMTYPE,
                                 'hardlink': tarfile.LNKTYPE, 'directory': tarfile.DIRTYPE,
                                 'fifo': tarfile.FIFOTYPE}[kind]
                    if kind == 'file':
                        info.size = len(data)
                        archive.addfile(info, io.BytesIO(data))
                    else:
                        info.linkname = 'cpra' if kind in ('symlink', 'hardlink') else ''
                        archive.addfile(info)
        return path

    def replace_metadata(self, entries, name, edit):
        for index, (filename, data, mode, kind) in enumerate(entries):
            if filename == name:
                value = json.loads(data)
                edit(value)
                entries[index] = (filename, json.dumps(value).encode(), mode, kind)
                return
        self.fail('Fixture lacks metadata: ' + name)

    def test_all_six_targets_hash_the_actual_executables(self):
        for target in publish.RECIPE['targets']:
            with self.subTest(target=target):
                self.write(target)
                expected = {name: hashlib.sha256((target + '/' + name).encode()).hexdigest()
                            for name in ('cpra', 'cpractl')}
                self.assertEqual(publish.archive_programs(self.out, self.manifest, target), expected)

    def test_missing_metadata_or_executables_fail(self):
        for target in ('linux_amd64', 'windows_arm64'):
            for absent in [entry[0] for entry in self.entries(target)]:
                with self.subTest(target=target, absent=absent):
                    self.write(target, [entry for entry in self.entries(target) if entry[0] != absent])
                    with self.assertRaisesRegex(ValueError, 'disagrees'):
                        publish.archive_programs(self.out, self.manifest, target)

    def test_inventory_target_must_match_the_archive_target(self):
        for target in ('linux_amd64', 'windows_amd64'):
            for invalid in (None, 'linux_arm64', ''):
                with self.subTest(target=target, inventory_target=invalid):
                    entries = self.entries(target)
                    self.replace_metadata(entries, 'DEPENDENCIES.json', lambda value: value.update(target=invalid))
                    self.write(target, entries)
                    with self.assertRaisesRegex(ValueError, 'disagrees'):
                        publish.archive_programs(self.out, self.manifest, target)

    def test_inventory_has_exactly_the_two_shipped_programs(self):
        for change in (lambda value: value.pop('binaries'),
                       lambda value: value.update(binaries=[]),
                       lambda value: value['binaries'].pop('cpractl'),
                       lambda value: value['binaries'].update(other='a' * 64)):
            entries = self.entries('linux_amd64')
            self.replace_metadata(entries, 'DEPENDENCIES.json', change)
            self.write('linux_amd64', entries)
            with self.assertRaisesRegex(ValueError, 'disagrees'):
                publish.archive_programs(self.out, self.manifest, 'linux_amd64')

    def test_recorded_identity_and_actual_program_bytes_must_match(self):
        for target in ('linux_amd64', 'windows_amd64'):
            entries = self.entries(target)
            self.replace_metadata(entries, 'RELEASE.json', lambda value: value.update(commit='b' * 40))
            self.write(target, entries)
            with self.assertRaisesRegex(ValueError, 'disagrees'):
                publish.archive_programs(self.out, self.manifest, target)
            entries = self.entries(target)
            name, _, mode, kind = entries[0]
            entries[0] = (name, b'different executable', mode, kind)
            self.write(target, entries)
            with self.assertRaisesRegex(ValueError, 'disagrees'):
                publish.archive_programs(self.out, self.manifest, target)

    def test_duplicate_archive_paths_fail_even_when_their_contents_match(self):
        for target in ('linux_amd64', 'windows_amd64'):
            for position in (0, 2, 3):
                with self.subTest(target=target, position=position):
                    entries = self.entries(target)
                    self.write(target, entries + [entries[position]])
                    with self.assertRaisesRegex(ValueError, 'duplicate path'):
                        publish.archive_programs(self.out, self.manifest, target)

    def test_windows_case_aliases_fail(self):
        entries = self.entries('windows_amd64')
        name, data, mode, kind = entries[0]
        self.write('windows_amd64', entries + [(name.upper(), data, mode, kind)])
        with self.assertRaisesRegex(ValueError, 'duplicate path'):
            publish.archive_programs(self.out, self.manifest, 'windows_amd64')

    def test_every_member_path_is_canonical_and_relative(self):
        for target in ('linux_amd64', 'windows_amd64'):
            for name in ('../README.md', '/README.md', './README.md', 'docs//README.md',
                         'docs/../README.md', 'docs\\README.md', 'C:/README.md', 'README:stream', '.'):
                with self.subTest(target=target, name=name):
                    self.write(target, self.entries(target) + [(name, b'not extracted', 0o644, 'file')])
                    with self.assertRaisesRegex(ValueError, 'noncanonical path'):
                        publish.archive_programs(self.out, self.manifest, target)

    def test_links_special_files_and_privileged_modes_fail(self):
        for target in ('linux_amd64', 'windows_amd64'):
            kinds = ('symlink', 'fifo') + (('hardlink',) if target.startswith('linux_') else ())
            for kind in kinds:
                with self.subTest(target=target, kind=kind):
                    self.write(target, self.entries(target) + [('notice', b'cpra', 0o644, kind)])
                    with self.assertRaisesRegex(ValueError, 'link, special file'):
                        publish.archive_programs(self.out, self.manifest, target)
            for mode in (0o4644, 0o2644, 0o1644):
                with self.subTest(target=target, mode=mode):
                    self.write(target, self.entries(target) + [('notice', b'notice', mode, 'file')])
                    with self.assertRaisesRegex(ValueError, 'privileged mode'):
                        publish.archive_programs(self.out, self.manifest, target)

    def test_unix_programs_require_executable_mode(self):
        entries = self.entries('linux_amd64')
        name, data, _, kind = entries[0]
        entries[0] = (name, data, 0o644, kind)
        self.write('linux_amd64', entries)
        with self.assertRaisesRegex(ValueError, 'executable mode'):
            publish.archive_programs(self.out, self.manifest, 'linux_amd64')

    def test_empty_programs_fail_even_with_matching_inventory(self):
        for target in ('linux_amd64', 'windows_amd64'):
            entries = self.entries(target)
            name, _, mode, kind = entries[0]
            entries[0] = (name, b'', mode, kind)
            self.replace_metadata(entries, 'DEPENDENCIES.json',
                                  lambda value: value['binaries'].update({name: hashlib.sha256(b'').hexdigest()}))
            self.write(target, entries)
            with self.assertRaisesRegex(ValueError, 'empty, oversized'):
                publish.archive_programs(self.out, self.manifest, target)

    def test_metadata_rejects_duplicate_fields_and_nonfinite_numbers(self):
        values = (b'{"target":"linux_amd64","target":"linux_amd64"}',
                  b'{"binaries":{"cpra":"a","cpra":"a"}}',
                  b'{"value":NaN}', b'{"value":Infinity}', b'{"value":-Infinity}',
                  b'{"value":1e999}', b'{"value":-1e999}')
        for target in ('linux_amd64', 'windows_amd64'):
            for value in values:
                with self.subTest(target=target, value=value):
                    entries = self.entries(target)
                    entries[-1] = ('DEPENDENCIES.json', value, 0o644, 'file')
                    self.write(target, entries)
                    with self.assertRaisesRegex(ValueError, 'duplicate JSON field|non-finite JSON number'):
                        publish.archive_programs(self.out, self.manifest, target)

    def test_malformed_or_nonobject_metadata_fails(self):
        for value in (b'', b'{', b'[]', b'null', b'"metadata"', b'{} {}', b'\xff'):
            entries = self.entries('linux_amd64')
            entries[-1] = ('DEPENDENCIES.json', value, 0o644, 'file')
            self.write('linux_amd64', entries)
            with self.assertRaises(ValueError):
                publish.archive_programs(self.out, self.manifest, 'linux_amd64')

    def test_metadata_and_executables_have_individual_size_limits(self):
        for target in ('linux_amd64', 'windows_amd64'):
            self.write(target)
            with patch.object(publish, 'ARCHIVE_METADATA_LIMIT', 32):
                with self.assertRaisesRegex(ValueError, 'metadata must be nonempty'):
                    publish.archive_programs(self.out, self.manifest, target)
            with patch.object(publish, 'ARCHIVE_PROGRAM_LIMIT', 4):
                with self.assertRaisesRegex(ValueError, 'empty, oversized'):
                    publish.archive_programs(self.out, self.manifest, target)

    def test_member_count_and_total_uncompressed_bytes_are_bounded(self):
        for target in ('linux_amd64', 'windows_amd64'):
            self.write(target)
            with patch.object(publish, 'ARCHIVE_MEMBER_LIMIT', 3):
                with self.assertRaisesRegex(ValueError, 'more than 4096 members|ZIP directory exceeds'):
                    publish.archive_programs(self.out, self.manifest, target)
            self.write(target, self.entries(target) + [('notice', b'x' * 32768, 0o644, 'file')])
            with patch.object(publish, 'ARCHIVE_LIMIT', 8192):
                with self.assertRaisesRegex(ValueError, 'payload exceeds'):
                    publish.archive_programs(self.out, self.manifest, target)

    def test_corrupt_or_oversized_archive_files_fail(self):
        for target in ('linux_amd64', 'windows_amd64'):
            path = self.write(target)
            with patch.object(publish, 'ARCHIVE_LIMIT', 1):
                with self.assertRaisesRegex(ValueError, 'regular file of at most'):
                    publish.archive_programs(self.out, self.manifest, target)
            path.write_bytes(b'not an archive')
            with self.assertRaisesRegex(ValueError, 'Invalid release archive'):
                publish.archive_programs(self.out, self.manifest, target)

    def test_zip_payload_crc_is_checked(self):
        target = 'windows_amd64'
        path = self.write(target)
        data = bytearray(path.read_bytes())
        central = data.index(b'PK\x01\x02')
        data[14] ^= 0xff
        data[central + 16] ^= 0xff
        path.write_bytes(data)
        with self.assertRaisesRegex(ValueError, 'Invalid release archive'):
            publish.archive_programs(self.out, self.manifest, target)

    def test_damaged_zip_compression_is_reported_as_invalid_archive(self):
        target = 'windows_amd64'
        path = self.write(target)
        with zipfile.ZipFile(path) as archive:
            info = archive.getinfo('cpra.exe')
            offset = info.header_offset + 30 + len(info.filename.encode()) + len(info.extra)
        data = bytearray(path.read_bytes())
        data[offset] ^= 0xff
        path.write_bytes(data)
        with self.assertRaisesRegex(ValueError, 'Invalid release archive'):
            publish.archive_programs(self.out, self.manifest, target)

    def test_zip_directory_is_bounded_before_member_table_allocation(self):
        target = 'windows_amd64'
        path = self.write(target)
        with patch.object(publish, 'ZIP_DIRECTORY_LIMIT', 1):
            with self.assertRaisesRegex(ValueError, 'ZIP directory exceeds'):
                publish.archive_programs(self.out, self.manifest, target)
        for index, value in ((1, 1), (2, 1), (3, 1), (7, 1)):
            with self.subTest(field=index):
                path = self.write(target)
                data = bytearray(path.read_bytes())
                end = list(struct.unpack('<4s4H2LH', data[-22:]))
                end[index] = value
                data[-22:] = struct.pack('<4s4H2LH', *end)
                path.write_bytes(data)
                with self.assertRaisesRegex(ValueError, 'ZIP layout differs'):
                    publish.archive_programs(self.out, self.manifest, target)

    def test_zip_embedded_nul_cannot_change_the_effective_member_name(self):
        target = 'windows_amd64'
        path = self.write(target, self.entries(target) + [('notice.txt', b'notice', 0o644, 'file')])
        data = path.read_bytes().replace(b'notice.txt', b'not\x00ce.txt')
        path.write_bytes(data)
        with self.assertRaisesRegex(ValueError, 'noncanonical path'):
            publish.archive_programs(self.out, self.manifest, target)

    def test_encrypted_zip_flag_is_rejected_before_payload_read(self):
        target = 'windows_amd64'
        path = self.write(target)
        data = bytearray(path.read_bytes())
        central = data.index(b'PK\x01\x02')
        data[6] |= 1
        data[central + 8] |= 1
        path.write_bytes(data)
        with self.assertRaisesRegex(ValueError, 'must not be encrypted'):
            publish.archive_programs(self.out, self.manifest, target)

    def test_artifact_symlinks_and_unsupported_targets_fail(self):
        target = 'linux_amd64'
        path = self.write(target)
        actual = path.with_name('actual.tar.gz')
        path.rename(actual)
        path.symlink_to(actual.name)
        with self.assertRaisesRegex(ValueError, 'regular file of at most'):
            publish.archive_programs(self.out, self.manifest, target)
        with self.assertRaisesRegex(ValueError, 'Unsupported archive target'):
            publish.archive_programs(self.out, self.manifest, 'plan9_amd64')


if __name__ == '__main__':
    unittest.main()
