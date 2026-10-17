"""OCI readback, interruption and ownership tests; live suite uses real tools."""
import argparse
import copy
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
from unittest.mock import patch

import oci
import oci_assembly
import publish
import registry
import release
from test_evidence import manifest_fixture
from test_oci_assembly import reproduction


def fixture(root):
    reports = root / 'reports'
    reports.mkdir()
    manifest = manifest_fixture()
    source = root / 'RELEASE.json'
    source.write_bytes(release.encoded(manifest))
    for arch in ('amd64', 'arm64'):
        reproduction(reports, arch, manifest, layer_data=('layer-' + arch).encode())
    assembly = root / 'assembly'
    subjects = oci_assembly.assemble(source, reports, assembly)['subjects']
    return manifest, reports, assembly, subjects


class ReadbackFixture:
    def __init__(self):
        self.objects, self.requests, self.failure = {}, [], None
        owner = self
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_GET(self):
                owner.requests.append(self.path)
                key = ('blob' if '/blobs/' in self.path else 'manifest', self.path.rsplit('/', 1)[-1])
                if owner.failure:
                    status, body, headers = owner.failure
                elif key in owner.objects:
                    row, body = owner.objects[key]
                    status, headers = 200, {'Content-Type': row['media_type'], 'Docker-Content-Digest': row['digest']}
                else:
                    code = 'BLOB_UNKNOWN' if key[0] == 'blob' else 'MANIFEST_UNKNOWN'
                    status, body, headers = 404, release.encoded({'errors': [{'code': code}]}), {}
                self.send_response(status)
                for name, value in headers.items():
                    self.send_header(name, value)
                if 'Content-Length' not in headers:
                    self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                try:
                    self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    pass
        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.origin = 'http://127.0.0.1:' + str(self.server.server_port)

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


class UploadFixture(registry.Registry):
    """Unit-test upload double; GET byte verification uses actual HTTP."""
    def __init__(self, server, repository='ziad-hsn/cpra'):
        super().__init__(repository, origin=server.origin, allow_loopback_http=True)
        self.server, self.uploads = server, []
        self.fail_before, self.fail_after = False, False

    def upload(self, oras, row, path, work, *, tag=None):
        if self.fail_before:
            self.fail_before = False
            raise RuntimeError('fixture interrupted before upload')
        self.uploads.append(copy.deepcopy(row))
        self.server.objects[(row['kind'], row['digest'] if tag is None else tag)] = (copy.deepcopy(row), path.read_bytes())
        if self.fail_after:
            self.fail_after = False
            raise RuntimeError('fixture lost upload reply')


class VerifiedCopyTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source, self.destination = self.root / 'source', self.root / 'copy'
        self.data = b'approved bytes' * 100000
        self.source.write_bytes(self.data)
        self.digest = hashlib.sha256(self.data).hexdigest()

    def copy(self, **kwargs):
        registry.verified_copy(self.source, self.destination, len(self.data), self.digest,
                               deadline=time.monotonic() + 2, **kwargs)

    def test_large_input_is_copied_exactly_with_read_only_permissions(self):
        self.copy()
        self.assertEqual(self.destination.read_bytes(), self.data)
        self.assertEqual(stat.S_IMODE(self.destination.stat().st_mode), 0o400)
        self.assertNotEqual(self.source.stat().st_ino, self.destination.stat().st_ino)

    def test_symlink_hardlink_and_fifo_are_rejected_without_opening(self):
        original = self.root / 'original'
        self.source.rename(original)
        for kind in ('symlink', 'hardlink', 'fifo'):
            if kind == 'symlink':
                self.source.symlink_to(original)
            elif kind == 'hardlink':
                os.link(original, self.source)
            else:
                os.mkfifo(self.source)
            with self.subTest(kind=kind), patch.object(registry.os, 'open') as opened, self.assertRaises(ValueError):
                self.copy()
            opened.assert_not_called()
            self.source.unlink()
        self.assertFalse(self.destination.exists())

    def test_file_swap_to_fifo_during_open_fails_without_blocking(self):
        opened = os.open
        def replace(path, flags, *args):
            if Path(path) == self.source:
                self.source.unlink()
                os.mkfifo(self.source)
            return opened(path, flags, *args)
        started = time.monotonic()
        with patch.object(registry.os, 'open', side_effect=replace), self.assertRaisesRegex(ValueError, 'changed'):
            self.copy()
        self.assertLess(time.monotonic() - started, 1)
        self.assertFalse(self.destination.exists())

    def test_content_inode_and_link_changes_during_copy_remove_partial_output(self):
        opened = os.open
        for kind in ('content', 'inode', 'link'):
            self.source.write_bytes(self.data)
            def mutate(path, flags, *args):
                result = opened(path, flags, *args)
                if Path(path) == self.destination:
                    if kind == 'content':
                        self.source.write_bytes(b'x' * len(self.data))
                    elif kind == 'inode':
                        self.source.unlink()
                        self.source.write_bytes(self.data)
                    else:
                        os.link(self.source, self.root / 'alias')
                return result
            with self.subTest(kind=kind), patch.object(registry.os, 'open', side_effect=mutate), self.assertRaises(ValueError):
                self.copy()
            self.assertFalse(self.destination.exists())
            (self.root / 'alias').unlink(missing_ok=True)

    def test_existing_destination_is_never_overwritten(self):
        self.destination.write_bytes(b'original destination')
        with self.assertRaises(ValueError):
            self.copy()
        self.assertEqual(self.destination.read_bytes(), b'original destination')

    def test_overflow_and_deadline_remove_partial_output(self):
        with self.assertRaises(ValueError):
            self.copy(maximum=1)
        with patch.object(registry.time, 'monotonic', side_effect=(0, 0, 3)), self.assertRaisesRegex(ValueError, 'deadline'):
            registry.verified_copy(self.source, self.destination, len(self.data), self.digest, deadline=2)
        self.assertFalse(self.destination.exists())

    def test_invalid_descriptor_and_expired_deadline_fail_before_copy(self):
        for size, digest, deadline in ((True, self.digest, 10), (-1, self.digest, 10),
                                      (len(self.data), 'not-a-digest', 10),
                                      (len(self.data), self.digest, float('inf')),
                                      (len(self.data), self.digest, -1)):
            with self.subTest(size=size, digest=digest, deadline=deadline), self.assertRaises(ValueError):
                registry.verified_copy(self.source, self.destination, size, digest, deadline=deadline)
        self.assertFalse(self.destination.exists())


class RegistryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.server = ReadbackFixture()
        self.addCleanup(self.server.close)
        self.remote = UploadFixture(self.server)
        self.data = b'original object bytes'
        self.row = {'kind': 'blob', 'digest': 'sha256:' + hashlib.sha256(self.data).hexdigest(),
                    'size': len(self.data), 'media_type': oci.CONFIG}

    def install(self):
        self.server.objects[('blob', self.row['digest'])] = (self.row, self.data)

    def test_expected_bytes_are_read_not_just_headers_or_head(self):
        self.install()
        self.assertTrue(self.remote.verify(self.row))
        self.assertEqual(len(self.server.requests), 1)
        self.server.objects[('blob', self.row['digest'])] = (self.row, b'x' * len(self.data))
        with self.assertRaisesRegex(ValueError, 'bytes differ'):
            self.remote.verify(self.row)

    def test_only_recognized_404_means_missing(self):
        self.assertFalse(self.remote.verify(self.row))
        for code, errors in ((403, {}), (500, {}), (404, {}), (404, {'errors': []}),
                             (404, {'errors': [{'code': 'DENIED'}]}),
                             (404, {'errors': [{'code': 'MANIFEST_UNKNOWN'}]})):
            self.server.failure = (code, release.encoded(errors), {})
            with self.subTest(code=code, errors=errors), self.assertRaises(ValueError):
                self.remote.verify(self.row)

    def test_wrong_lengths_digests_and_overflow_are_explicit(self):
        for body, headers in ((self.data[:-1], {}), (self.data + b'overflow', {}),
                              (self.data, {'Docker-Content-Digest': 'sha256:' + '0' * 64})):
            self.server.failure = (200, body, headers)
            with self.subTest(headers=headers, size=len(body)), self.assertRaises(ValueError):
                self.remote.verify(self.row)
        with self.assertRaisesRegex(ValueError, 'byte limit'):
            list(registry.body_chunks(io.BytesIO(b'12345'), 4, time.monotonic() + 1))

    def test_missing_optional_digest_header_is_accepted(self):
        self.server.failure = (200, self.data, {})
        self.assertTrue(self.remote.verify(self.row))

    def test_manifest_media_type_is_verified(self):
        row = dict(self.row, kind='manifest', media_type=oci.MANIFEST)
        self.server.failure = (200, self.data, {'Content-Type': oci.INDEX})
        with self.assertRaisesRegex(ValueError, 'media type'):
            self.remote.verify(row)

    def test_network_failure_is_not_absence(self):
        with patch.object(self.remote.opener, 'open', side_effect=urllib.error.URLError('fixture-secret')):
            with self.assertRaises(ValueError) as stopped:
                self.remote.verify(self.row)
            self.assertNotIn('fixture-secret', str(stopped.exception))

    def test_unsafe_origins_and_descriptors_fail_before_io(self):
        for origin in ('http://ghcr.io', 'https://evil.invalid', 'http://localhost:5000',
                       'http://127.0.0.1:5000/path', 'http://user:secret@127.0.0.1:5000'):
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                registry.Registry('ziad-hsn/cpra', origin=origin, allow_loopback_http=True)
        for row in (dict(self.row, digest='../../other'), dict(self.row, size=True),
                    dict(self.row, size=-1), dict(self.row, kind='tag'),
                    dict(self.row, media_type='text/plain\r\nAuthorization: secret')):
            with self.subTest(row=row), self.assertRaises(ValueError):
                self.remote.verify(row)
        self.assertEqual(self.server.requests, [])
        with self.assertRaises(ValueError):
            registry.Registry('ziad-hsn/cpra', origin=self.server.origin, allow_loopback_http=True,
                              username='operator', token='fixture-secret')

    def test_redirect_does_not_forward_credentials(self):
        destination = ReadbackFixture()
        self.addCleanup(destination.close)
        self.server.failure = (307, b'', {'Location': destination.origin + '/untrusted'})
        opener = registry.urllib.request.build_opener(registry.NoRedirect())
        request = registry.urllib.request.Request(self.server.origin + '/first',
                                                 headers={'Authorization': 'Bearer fixture-secret'})
        with self.assertRaises(urllib.error.HTTPError):
            opener.open(request, timeout=1)
        self.assertEqual(destination.requests, [])

    def test_storage_redirect_has_no_registry_credentials_and_is_bounded(self):
        client = registry.Registry('ziad-hsn/cpra', username='fixture', token='fixture-secret')
        client.bearer = 'fixture-bearer'
        seen = []
        def open_response(request, **_):
            seen.append(request)
            if len(seen) == 1:
                raise urllib.error.HTTPError(request.full_url, 307, 'redirect',
                                             {'Location': 'https://pkg-containers.githubusercontent.com/blob?sig=fixture'}, io.BytesIO())
            return io.BytesIO(self.data)
        with patch.object(client.opener, 'open', side_effect=open_response):
            self.assertTrue(client.verify(self.row))
        self.assertIn('Authorization', seen[0].headers)
        self.assertNotIn('Authorization', seen[1].headers)
        for target in ('http://pkg-containers.githubusercontent.com/blob', 'https://evil.invalid/blob',
                       'https://pkg-containers.githubusercontent.com:443/blob',
                       'https://secret@pkg-containers.githubusercontent.com/blob'):
            with self.subTest(target=target), self.assertRaises(ValueError):
                client.storage_response(target)

    def test_metadata_is_bounded_strict_and_closed(self):
        client = registry.Registry('ziad-hsn/cpra', username='fixture', token='fixture-secret')
        for body in (b'{"token":"a","token":"b"}', b'{"token":false}', b'x' * (registry.METADATA_LIMIT + 1)):
            response = io.BytesIO(body)
            with patch.object(client.opener, 'open', return_value=response), self.assertRaises(ValueError):
                client.authorization()
            self.assertTrue(response.closed)

    def test_stream_deadline_is_explicit(self):
        with self.assertRaisesRegex(ValueError, 'deadline'):
            list(registry.body_chunks(io.BytesIO(self.data), 100, time.monotonic() - 1))

    def test_object_reference_validation_precedes_authentication(self):
        for kind, reference in (('blobs', 'latest'), ('tags', '../other'),
                                ('manifests', 'bad\r\nAuthorization: secret'), ('other', 'list')):
            with self.subTest(kind=kind, reference=reference), self.assertRaises(ValueError):
                self.remote.response(kind, reference)
        self.assertEqual(self.server.requests, [])

    def test_upload_rejects_unpinned_tools_before_starting_a_process(self):
        tool, payload = self.root / 'oras', self.root / 'object'
        tool.write_bytes(b'not the pinned executable')
        payload.write_bytes(self.data)
        with patch.object(registry.subprocess, 'Popen') as process, self.assertRaisesRegex(ValueError, 'reviewed checksum'):
            self.remote.upload = registry.Registry.upload.__get__(self.remote)
            self.remote.upload(tool, self.row, payload, self.root)
        process.assert_not_called()

    def runner(self, body):
        tool, payload = self.root / 'test-client', self.root / 'object'
        tool.write_text('#!/usr/bin/python3\n' + body)
        tool.chmod(0o700)
        payload.write_bytes(self.data)
        original = registry.decode_report
        pin = {'oras': {'executable_sha256': hashlib.sha256(tool.read_bytes()).hexdigest()}}
        replacement = lambda data, name: pin if name == 'tool pins' else original(data, name)
        return tool, payload, patch.object(registry, 'decode_report', side_effect=replacement)

    def test_process_environment_auth_permissions_and_cleanup(self):
        output = self.root / 'observed.json'
        tool, payload, pinned_fixture = self.runner(
            "import json,os,stat,sys\nfrom pathlib import Path\n"
            "config=Path(sys.argv[sys.argv.index('--registry-config')+1])\n"
            "assert stat.S_IMODE(config.stat().st_mode)==0o600\n"
            "assert json.loads(config.read_text())['auths']\n"
            "assert not any(k in os.environ for k in ('HOME','GH_TOKEN','DOCKER_CONFIG','LD_PRELOAD','ORAS_AUTH'))\n"
            "assert all('@sha256:' in a for a in sys.argv if a.startswith('ghcr.io/'))\n"
            "Path(" + repr(str(output)) + ").write_text('true')\n")
        client = registry.Registry('ziad-hsn/cpra', username='fixture', token='fixture-secret')
        with pinned_fixture, patch.dict(os.environ, {'GH_TOKEN': 'ambient-secret', 'ORAS_AUTH': 'ambient-secret'}):
            client.upload(tool, self.row, payload, self.root)
        self.assertEqual(output.read_text(), 'true')
        self.assertEqual(list(self.root.glob('oras-*')), [])

    def test_source_change_at_process_start_cannot_change_uploaded_payload(self):
        observed = self.root / 'uploaded-sha256'
        tool, payload, pinned_fixture = self.runner(
            'import hashlib,sys\nfrom pathlib import Path\n'
            'Path(' + repr(str(observed)) + ').write_text(hashlib.sha256(Path(sys.argv[-1]).read_bytes()).hexdigest())\n')
        start = subprocess.Popen

        def mutate_before_start(*args, **kwargs):
            payload.write_bytes(b'x' * len(self.data))
            return start(*args, **kwargs)

        with pinned_fixture, patch.object(registry.subprocess, 'Popen', side_effect=mutate_before_start):
            registry.Registry.upload(self.remote, tool, self.row, payload, self.root)
        self.assertEqual(observed.read_text(), self.row['digest'].removeprefix('sha256:'))
        self.assertEqual(list(self.root.glob('oras-*')), [])

    def test_source_tool_change_at_process_start_cannot_run_an_unreviewed_executable(self):
        observed = self.root / 'approved-executable'
        tool, payload, pinned_fixture = self.runner(
            'from pathlib import Path\nPath(' + repr(str(observed)) + ').write_text("approved")\n')
        start = subprocess.Popen
        def mutate_before_start(*args, **kwargs):
            tool.write_text('#!/usr/bin/python3\nraise SystemExit(99)\n')
            return start(*args, **kwargs)
        with pinned_fixture, patch.object(registry.subprocess, 'Popen', side_effect=mutate_before_start):
            registry.Registry.upload(self.remote, tool, self.row, payload, self.root)
        self.assertEqual(observed.read_text(), 'approved')
        self.assertEqual(list(self.root.glob('oras-*')), [])

    def test_bad_payload_and_quota_exhaustion_cannot_start_a_process(self):
        tool, payload, pinned_fixture = self.runner('raise SystemExit(99)\n')
        payload.write_bytes(b'x' * len(self.data))
        with pinned_fixture, patch.object(registry.subprocess, 'Popen') as started, self.assertRaisesRegex(ValueError, 'checksum'):
            registry.Registry.upload(self.remote, tool, self.row, payload, self.root)
        started.assert_not_called()
        payload.write_bytes(self.data)
        with pinned_fixture, patch.object(registry, 'WORK_LIMIT', 1), patch.object(registry.subprocess, 'Popen') as started:
            with self.assertRaisesRegex(ValueError, 'quota'):
                registry.Registry.upload(self.remote, tool, self.row, payload, self.root)
        started.assert_not_called()
        self.assertEqual(list(self.root.glob('oras-*')), [])

    def test_copy_that_exhausts_deadline_cannot_start_a_process(self):
        tool, payload, pinned_fixture = self.runner('raise SystemExit(99)\n')
        clock, copy = time.monotonic, registry.verified_copy
        offset = 0
        def finish_copy(*args, **kwargs):
            nonlocal offset
            copy(*args, **kwargs)
            if Path(args[0]) == payload:
                offset = 10
        with pinned_fixture, patch.object(registry, 'verified_copy', side_effect=finish_copy), \
                patch.object(registry.time, 'monotonic', side_effect=lambda: clock() + offset), \
                patch.object(registry.subprocess, 'Popen') as started, self.assertRaisesRegex(ValueError, 'deadline'):
            registry.Registry.upload(self.remote, tool, self.row, payload, self.root, timeout=1)
        started.assert_not_called()
        self.assertEqual(list(self.root.glob('oras-*')), [])

    def test_failed_and_uncooperative_processes_are_stopped_and_cleaned(self):
        for body in ('raise SystemExit(1)\n', 'import time\ntime.sleep(20)\n',
                     "import os,time\nos.write(1,b'x'*65536)\ntime.sleep(20)\n"):
            tool, payload, pinned_fixture = self.runner(body)
            with pinned_fixture, patch.object(registry, 'PROCESS_LOG_LIMIT', 16384), self.assertRaises(ValueError):
                registry.Registry.upload(self.remote, tool, self.row, payload, self.root, timeout=0.2)
            self.assertEqual(list(self.root.glob('oras-*')), [])


class TransferTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.manifest, self.reports, self.assembly, self.subjects = fixture(self.root)
        self.server = ReadbackFixture()
        self.addCleanup(self.server.close)
        self.remote = UploadFixture(self.server)
        self.progress = self.root / 'progress.jsonl'

    def transfer(self):
        return publish.transfer_images(self.assembly, self.reports, self.manifest,
                                       self.remote, '/unused/oras', self.progress, self.root)

    def test_complete_graph_and_resume_recheck_every_object_without_new_uploads(self):
        first = self.transfer()
        self.assertEqual(first['uploaded'], 7)
        self.assertEqual(self.remote.uploads[-1]['digest'], self.subjects['digest'])
        reads = len(self.server.requests)
        self.remote.uploads.clear()
        second = self.transfer()
        self.assertEqual(second['uploaded'], 0)
        self.assertEqual(second['reused'], 7)
        self.assertEqual(len(self.server.requests) - reads, 7)
        self.assertEqual(self.remote.uploads, [])

    def test_lost_reply_is_reconciled_from_remote_bytes(self):
        self.remote.fail_after = True
        with self.assertRaisesRegex(RuntimeError, 'lost upload reply'):
            self.transfer()
        first = self.remote.uploads[0]
        self.assertIn('started', self.progress.read_text())
        result = self.transfer()
        self.assertEqual(result['reused'], 1)
        self.assertEqual(self.remote.uploads.count(first), 1)

    def test_interruption_before_external_write_can_resume(self):
        self.remote.fail_before = True
        with self.assertRaises(RuntimeError):
            self.transfer()
        self.assertEqual(self.remote.uploads, [])
        self.assertEqual(self.transfer()['uploaded'], 7)

    def test_verified_marker_never_authorizes_replacement_or_corrupt_readback(self):
        self.transfer()
        self.remote.uploads.clear()
        key = next(iter(self.server.objects))
        row, data = self.server.objects[key]
        self.server.objects[key] = (row, b'x' * len(data))
        with self.assertRaises(ValueError):
            self.transfer()
        self.server.objects.pop(key)
        with self.assertRaisesRegex(ValueError, 'replacement is forbidden'):
            self.transfer()
        self.assertEqual(self.remote.uploads, [])

    def test_changed_graph_or_source_and_private_ownership_are_rejected(self):
        self.transfer()
        requests = len(self.server.requests)
        original = self.progress.read_bytes()
        header, *rest = original.splitlines()
        value = json.loads(header)
        value['identity']['release_identity'] = 'different source'
        self.progress.write_bytes(publish.release_bytes(value) + b'\n'.join(rest) + b'\n')
        with self.assertRaisesRegex(ValueError, 'different source'):
            self.transfer()
        self.assertEqual(len(self.server.requests), requests)
        self.progress.write_bytes(original)
        self.progress.chmod(0o644)
        with self.assertRaisesRegex(ValueError, 'private'):
            self.transfer()

    def test_locked_alias_and_corrupt_progress_fail_before_upload(self):
        self.transfer()
        header = json.loads(self.progress.read_bytes().splitlines()[0])['identity']
        objects = header['objects']
        with publish.TransferProgress(self.progress, header, objects):
            with self.assertRaisesRegex(ValueError, 'locked'):
                self.transfer()
        alias = self.root / 'alias'
        alias.symlink_to(self.progress)
        self.progress = alias
        with self.assertRaises(OSError):
            self.transfer()
        self.progress = self.root / 'corrupt'
        self.progress.write_text('not a valid header\n')
        self.progress.chmod(0o600)
        with self.assertRaises(ValueError):
            self.transfer()

    def test_interrupted_last_record_is_repaired_after_identity_validation(self):
        self.transfer()
        original = self.progress.read_bytes()
        with self.progress.open('ab') as stream:
            stream.write(b'{"kind":"blob","digest":')
        self.assertEqual(self.transfer()['reused'], 7)
        self.assertEqual(self.progress.read_bytes(), original)

    def test_malformed_progress_identity_fails_with_a_controlled_error(self):
        self.transfer()
        original = self.progress.read_bytes()
        for invalid in ({'kind': [], 'digest': {}, 'state': 'verified'},
                        {'kind': 'blob', 'digest': 'unknown', 'state': 'verified'}):
            self.progress.write_bytes(original + publish.release_bytes(invalid))
            with self.subTest(invalid=invalid), self.assertRaisesRegex(ValueError, 'Invalid publication progress'):
                self.transfer()
            self.assertEqual(self.progress.read_bytes(), original + publish.release_bytes(invalid))

    def test_progress_capacity_is_reserved_before_external_write(self):
        self.remote.fail_before = True
        with self.assertRaises(RuntimeError):
            self.transfer()
        with patch.object(publish, 'PROGRESS_LIMIT', self.progress.stat().st_size + 1):
            with self.assertRaisesRegex(ValueError, 'no space'):
                self.transfer()
        self.assertEqual(self.remote.uploads, [])

    def test_changed_local_bytes_fail_before_io_and_admission(self):
        (self.assembly / 'RELEASE.json').write_bytes(b'changed')
        with self.assertRaises(ValueError):
            self.transfer()
        self.assertEqual(self.server.requests, [])
        self.assertFalse(self.progress.exists())

    def test_input_change_during_remote_read_stops_before_upload(self):
        verify = self.remote.verify
        def change(row):
            present = verify(row)
            with (self.assembly / 'RELEASE.json').open('ab') as stream:
                stream.write(b'changed')
            return present
        with patch.object(self.remote, 'verify', side_effect=change), self.assertRaises(ValueError):
            self.transfer()
        self.assertEqual(self.remote.uploads, [])

    def test_progress_cannot_modify_retained_inputs_and_dirty_source_is_rejected(self):
        self.progress = self.assembly / 'progress'
        with self.assertRaisesRegex(ValueError, 'overlap'):
            self.transfer()
        self.progress = self.root / 'progress'
        self.manifest['dirty'] = True
        with self.assertRaisesRegex(ValueError, 'clean frozen'):
            self.transfer()
        self.assertEqual(self.server.requests, [])


def integration(tools, work, *, image_signatures=False):
    """Explicit CI/local suite: real ORAS, real registry, real SIGKILL restart."""
    tools, work = Path(tools).absolute(), Path(work).absolute()
    work.mkdir(mode=0o700, parents=True, exist_ok=True)
    pins = json.loads(Path(__file__).with_name('tools.json').read_text())
    names = ('oras', 'distribution', 'cosign') if image_signatures else ('oras', 'distribution')
    for name in names:
        path = tools / name
        expected = pins[name].get('executable_sha256', pins[name]['sha256'])
        if path.is_symlink() or not path.is_file() or release.sha(path) != expected:
            raise ValueError('Live registry test requires checksum-pinned ' + name)
    with tempfile.TemporaryDirectory(prefix='registry-', dir=work) as temporary:
        root = Path(temporary)
        manifest, reports, assembly, subjects = fixture(root)
        config = root / 'registry.yaml'
        config.write_text('version: 0.1\nlog:\n  level: info\nstorage:\n  filesystem:\n'
                          '    rootdirectory: ' + str(root / 'storage') + '\nhttp:\n'
                          '  addr: 127.0.0.1:0\n  secret: isolated-registry-fixture\n')
        config.chmod(0o600)
        environment = {'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8', 'TZ': 'UTC',
                       'TMPDIR': str(root), 'XDG_CACHE_HOME': str(root),
                       'OTEL_SDK_DISABLED': 'true', 'OTEL_TRACES_EXPORTER': 'none'}
        log_path = root / 'registry.log'
        with log_path.open('wb') as log:
            process = subprocess.Popen([str(tools / 'distribution'), 'serve', str(config)],
                                       env=environment, cwd=root, stdout=log, stderr=log,
                                       stdin=subprocess.DEVNULL, start_new_session=True)
            try:
                deadline, address = time.monotonic() + 10, None
                while process.poll() is None and time.monotonic() < deadline:
                    text = log_path.read_text()
                    match = re.search(r'listening on (127\.0\.0\.1:\d+)', text)
                    if match:
                        address = match.group(1)
                        break
                    if log_path.stat().st_size > registry.PROCESS_LOG_LIMIT:
                        raise ValueError('Local registry log quota exceeded')
                    time.sleep(0.05)
                if address is None:
                    raise ValueError('Local registry did not report its bound address')
                origin = 'http://' + address
                progress = root / 'progress.jsonl'
                child_code = (
                    "import json,os,signal,sys\nfrom pathlib import Path\nimport publish,registry\n"
                    "root=Path(sys.argv[1]);tools=Path(sys.argv[2])\n"
                    "class Interrupted(registry.Registry):\n"
                    " def upload(self,*args,**kwargs):\n"
                    "  super().upload(*args,**kwargs)\n"
                    "  os.kill(os.getpid(),signal.SIGKILL)\n"
                    "remote=Interrupted('ziad-hsn/cpra',origin=sys.argv[3],allow_loopback_http=True)\n"
                    "publish.transfer_images(root/'assembly',root/'reports',json.loads((root/'RELEASE.json').read_text()),"
                    "remote,tools/'oras',root/'progress.jsonl',root)\n")
                child_environment = dict(environment, PYTHONPATH=str(Path(__file__).parent), PYTHONDONTWRITEBYTECODE='1')
                killed = subprocess.run([sys.executable, '-B', '-c', child_code, str(root), str(tools), origin],
                                        env=child_environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
                if killed.returncode != -signal.SIGKILL:
                    raise ValueError('Registry process-crash scenario did not reach its verified upload boundary')
                rows = [json.loads(line) for line in progress.read_bytes().splitlines()]
                if len(rows) != 2 or rows[1]['state'] != 'started':
                    raise ValueError('Crash did not retain exactly one committed started marker')
                remote = registry.Registry('ziad-hsn/cpra', origin=origin, allow_loopback_http=True)
                result = publish.transfer_images(assembly, reports, manifest, remote, tools / 'oras', progress, root)
                if (result['reused'] != 1 or result['uploaded'] != 6 or result['objects_verified'] != 7
                        or result['subjects'] != subjects):
                    raise ValueError('Actual registry crash/resume graph does not reconcile')
                again = publish.transfer_images(assembly, reports, manifest, remote, tools / 'oras', progress, root)
                if again['uploaded'] != 0 or again['reused'] != 7:
                    raise ValueError('Actual registry readback repeated an upload')
                response = remote.response('tags', 'list')
                if response is not None:
                    with response:
                        if registry.metadata(response, 3).get('tags'):
                            raise ValueError('Digest transfer unexpectedly created a tag')
                tags = root / 'storage/docker/registry/v2/repositories/ziad-hsn/cpra/_manifests/tags'
                if tags.exists() and list(tags.iterdir()):
                    raise ValueError('Registry storage contains an unexpected tag')
                tag_code = child_code.replace('publish.transfer_images(', 'publish.tag_images(').replace(
                    "root/'progress.jsonl'", "root/'image-tag.jsonl'")
                tagged = subprocess.run([sys.executable, '-B', '-c', tag_code, str(root), str(tools), origin],
                                        env=child_environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
                if tagged.returncode != -signal.SIGKILL:
                    raise ValueError('Exact-index tag crash did not reach the accepted write boundary')
                rows = [json.loads(line) for line in (root/'image-tag.jsonl').read_bytes().splitlines()]
                if len(rows) != 2 or rows[1]['state'] != 'started':
                    raise ValueError('Tag crash did not retain exactly one started marker')
                tagged_result = publish.tag_images(assembly, reports, manifest, remote, tools/'oras',
                                                   root/'image-tag.jsonl', root)
                tagged_again = publish.tag_images(assembly, reports, manifest, remote, tools/'oras',
                                                  root/'image-tag.jsonl', root)
                if tagged_result['reused'] is not True or tagged_again['reused'] is not True:
                    raise ValueError('Actual registry tag continuation repeated a version write')
                with remote.response('tags', 'list') as response:
                    if registry.metadata(response, 3).get('tags') != [manifest['version']]:
                        raise ValueError('Registry contains a version other than the exact original index')
                from test_chart_publication import integration as chart_integration
                chart_result = chart_integration(tools, origin, root, environment)
                signing_result = None
                if image_signatures:
                    from test_image_signing import integration as signing_integration
                    signing_result = signing_integration(tools, origin, root/'signing', environment)
                return {'status': 'pass', 'tool_versions': {name: pins[name]['version'] for name in names},
                        'tool_sha256': {name: pins[name].get('executable_sha256', pins[name]['sha256']) for name in names},
                        'checks': ['exact_seven_object_graph', 'sigkill_after_remote_accept_before_result',
                                   'resume_reuses_original_object', 'second_resume_rechecks_every_byte',
                                   'digest_transfer_creates_no_tags', 'sigkill_after_exact_index_tag_accept',
                                   'tag_resume_reuses_and_hashes_original_index', 'only_the_requested_version_exists'],
                        'subjects': subjects, 'chart': chart_result, 'image_signing': signing_result,
                        'fixture': 'local OCI byte fixture; not runnable CPRa or GHCR qualification',
                        'release_gate_passed': False, 'public_objects_created': False}
            except BaseException:
                with tempfile.NamedTemporaryFile(prefix='registry-failure-', suffix='.log', dir=work, delete=False) as failure:
                    failure.write(log_path.read_bytes()[:registry.PROCESS_LOG_LIMIT])
                raise
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGTERM)
                    try:
                        process.wait(timeout=3)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait(timeout=3)


if __name__ == '__main__':
    if '--integration' in sys.argv:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument('--integration', action='store_true')
        parser.add_argument('--image-signatures', action='store_true',
                            help='also exercise original image bundles with pinned Cosign and ephemeral fixture keys')
        parser.add_argument('--tools', required=True, type=Path)
        parser.add_argument('--work', required=True, type=Path)
        parser.add_argument('--out', type=Path)
        args = parser.parse_args()
        result = integration(args.tools, args.work, image_signatures=args.image_signatures)
        if args.out:
            with args.out.open('xb') as writer:
                writer.write(release.encoded(result))
        print(json.dumps(result, sort_keys=True))
    else:
        unittest.main()
