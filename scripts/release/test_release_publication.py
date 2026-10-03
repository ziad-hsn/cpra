"""Real loopback HTTP and process-crash contracts, not GitHub release evidence."""
import base64
import copy
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import unittest
import urllib.parse
import urllib.error
from unittest.mock import patch

import publish
import qualification
import release
import release_publication as publication
import test_publication_evidence
import test_publish


class ReleaseFixture:
    def __init__(self, commit):
        self.commit = commit
        self.refs, self.releases, self.contents = {}, {}, {}
        self.events, self.requests = [], []
        self.next_id, self.lost, self.failure, self.after = 1, None, None, None
        owner = self
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def respond(self, code, value):
                data = value if isinstance(value, bytes) else release.encoded(value)
                self.send_response(code)
                self.send_header('Content-Length', str(len(data)))
                self.send_header('Content-Type', 'application/json')
                self.end_headers()
                try:
                    self.wfile.write(data)
                except (BrokenPipeError, ConnectionResetError):
                    pass

            def mutation(self, kind, value, code=200):
                owner.events.append(kind)
                if owner.after is not None:
                    owner.after(kind)
                if owner.lost == kind:
                    owner.lost = None
                    self.close_connection = True
                else:
                    self.respond(code, value)

            def do_GET(self):
                owner.requests.append(('GET', self.path))
                if owner.failure:
                    self.respond(*owner.failure); return
                route = urllib.parse.urlsplit(self.path)
                path = route.path.removeprefix('/repos/ziad-hsn/cpra')
                query = urllib.parse.parse_qs(route.query)
                if path == '':
                    self.respond(200, {'full_name': 'ziad-hsn/cpra', 'private': False, 'permissions': {'push': True}})
                elif path == '/git/commits/'+owner.commit:
                    self.respond(200, {'sha': owner.commit})
                elif path.startswith('/git/ref/tags/'):
                    tag = path.removeprefix('/git/ref/tags/')
                    value = owner.refs.get(tag)
                    self.respond(200, {'ref': 'refs/tags/'+tag, 'object': {'type': 'commit', 'sha': value}}) if value else self.respond(404, {'message': 'Not Found'})
                elif path == '/releases':
                    size, page = int(query['per_page'][0]), int(query['page'][0])
                    rows = list(owner.releases.values())
                    self.respond(200, rows[(page-1)*size:page*size])
                elif path.startswith('/releases/assets/'):
                    self.respond(200, owner.contents[int(path.rsplit('/', 1)[1])])
                elif path.endswith('/assets'):
                    value = owner.releases.get(int(path.split('/')[2]))
                    size, page = int(query['per_page'][0]), int(query['page'][0])
                    rows = value['assets'] if value else []
                    self.respond(200, rows[(page-1)*size:page*size])
                elif path.startswith('/releases/'):
                    value = owner.releases.get(int(path.rsplit('/', 1)[1]))
                    self.respond(200, value) if value else self.respond(404, {'message': 'Not Found'})
                else:
                    self.respond(404, {'message': 'Not Found'})

            def do_POST(self):
                route = urllib.parse.urlsplit(self.path)
                path = route.path.removeprefix('/repos/ziad-hsn/cpra')
                raw = self.rfile.read(int(self.headers['Content-Length']))
                owner.requests.append(('POST', self.path))
                if owner.failure:
                    self.respond(*owner.failure); return
                if path == '/git/refs':
                    value = json.loads(raw); tag = value['ref'].removeprefix('refs/tags/')
                    if tag in owner.refs:
                        self.respond(422, {'message': 'Reference already exists'}); return
                    owner.refs[tag] = value['sha']
                    self.mutation('tag:'+tag, {'ref': value['ref'], 'object': {'type': 'commit', 'sha': value['sha']}}, 201)
                elif path == '/releases':
                    value = json.loads(raw)
                    assert value.pop('make_latest') == 'false'
                    assert value.pop('generate_release_notes') is False
                    value.update(id=owner.allocate(), immutable=False, assets=[])
                    owner.releases[value['id']] = value
                    self.mutation('create', value, 201)
                elif path.endswith('/assets'):
                    query = urllib.parse.parse_qs(route.query)
                    value = {'id': owner.allocate(), 'name': query['name'][0], 'label': query['label'][0],
                             'size': len(raw), 'digest': 'sha256:'+hashlib.sha256(raw).hexdigest(), 'state': 'uploaded'}
                    destination = owner.releases[int(path.split('/')[2])]
                    assert destination['draft'] is True
                    assert all(asset['name'] != value['name'] for asset in destination['assets'])
                    destination['assets'].append(value); owner.contents[value['id']] = raw
                    self.mutation('upload', value, 201)
                else:
                    self.respond(404, {'message': 'Not Found'})

            def do_PATCH(self):
                value = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                owner.requests.append(('PATCH', self.path))
                assert value == {'draft': False, 'prerelease': True, 'make_latest': 'false'}
                item = owner.releases[int(self.path.rsplit('/', 1)[1])]
                item.update(draft=False, immutable=True)
                self.mutation('publish', item)

            def do_DELETE(self):
                owner.requests.append(('DELETE', self.path))
                item = owner.releases[int(self.path.rsplit('/', 1)[1])]
                assert item['draft'] is True
                del owner.releases[item['id']]
                self.mutation('cleanup', b'', 204)

        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.server.daemon_threads = True
        self.origin = 'http://127.0.0.1:'+str(self.server.server_port)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def allocate(self):
        value = self.next_id; self.next_id += 1
        return value

    def client(self):
        return publication.Releases(origin=self.origin, allow_loopback_http=True, timeout=2)

    def close(self):
        self.server.shutdown(); self.server.server_close(); self.thread.join(timeout=2)


class ReleasePublicationTests(unittest.TestCase):
    def setUp(self):
        self.fixture = test_publication_evidence.PublicationEvidenceTests('test_bound_payload_graph_keeps_actual_publication_and_downloads_unqualified')
        self.fixture.setUp(); self.addCleanup(self.fixture.doCleanups)
        self.root, self.manifest, self.out = self.fixture.root, self.fixture.manifest, self.fixture.out
        # Arbitrary bundles below exercise byte identity only. The real publisher
        # must verify Sigstore signatures/provenance with its production policy.
        for name in ('SHA256SUMS.sigstore.json', 'provenance.sigstore.json'):
            (self.out/name).write_bytes(b'fixture-only-'+name.encode())
        self.work = self.root/'work'; self.work.mkdir(mode=0o700)
        self.progress = self.root/'release.jsonl'
        self.remote = ReleaseFixture(self.manifest['commit']); self.addCleanup(self.remote.close)
        self.verifications = []

    def modules(self):
        client = self.remote.client(); client.prepare(self.manifest['commit'])
        for tag in ('sdk/go/v0.2.0-rc.1', 'sdk/go/worker/v0.3.0-rc.1', self.manifest['version']):
            current = client.reference(tag)
            if current is None:
                client.create(tag, self.manifest['commit'])
            else:
                self.assertEqual(current, self.manifest['commit'])

    def verify_assets(self, directory):
        expected = {p.name: p.read_bytes() for p in self.out.iterdir()}
        self.assertEqual({p.name: p.read_bytes() for p in directory.iterdir()}, expected)
        self.verifications.append('verified')

    def run_publication(self, *, modules=None, verify=None, client=None):
        return publication.publish_release(self.manifest, self.out, client or self.remote.client(),
                                           modules or self.modules, verify or self.verify_assets,
                                           self.progress, self.work)

    def interrupt(self, kind):
        self.remote.lost = kind
        with self.assertRaisesRegex(ValueError, 'could not be confirmed'):
            self.run_publication()

    def first_release(self):
        return next(iter(self.remote.releases.values()))

    def test_signed_readback_precedes_tags_and_complete_resume_makes_no_mutations(self):
        def modules():
            self.assertEqual(len(self.verifications), 1)
            self.modules()
        result = self.run_publication(modules=modules)
        self.assertFalse(result['release_gate_passed'])
        self.assertEqual(result['assets_verified'], len(list(self.out.iterdir())))
        self.assertEqual(self.remote.events.count('create'), 2)
        self.assertEqual(self.remote.events.count('upload'), 2*result['assets_verified'])
        self.assertEqual(len(self.remote.releases), 1)
        self.assertEqual(self.remote.events[-2:], ['publish', 'cleanup'])
        events = list(self.remote.events)
        self.run_publication()
        self.assertEqual(self.remote.events, events)
        self.assertFalse(any(self.work.iterdir()))

    def test_lost_creation_reply_reuses_original_release(self):
        self.interrupt('create'); original = self.first_release()['id']
        self.run_publication()
        receipts = [json.loads(row)['receipt']['id'] for row in self.progress.read_text().splitlines()[1:]
                    if json.loads(row)['kind'] == 'release-create' and json.loads(row)['state'] == 'verified']
        self.assertEqual(receipts[0], original)
        self.assertEqual(self.remote.events.count('create'), 2)

    def test_lost_upload_reply_reuses_original_asset(self):
        self.interrupt('upload'); original = self.first_release()['assets'][0]['id']
        result = self.run_publication()
        receipts = [json.loads(row).get('receipt', {}).get('id') for row in self.progress.read_text().splitlines()[1:]]
        self.assertIn(original, receipts)
        self.assertEqual(self.remote.events.count('upload'), 2*result['assets_verified'])

    def test_lost_publish_reply_reads_immutable_release_without_republishing(self):
        self.interrupt('publish'); self.run_publication()
        self.assertEqual(self.remote.events.count('publish'), 1)

    def test_lost_cleanup_reply_does_not_recreate_or_delete_other_releases(self):
        self.interrupt('cleanup'); self.run_publication()
        self.assertEqual(self.remote.events.count('create'), 2)
        self.assertEqual(self.remote.events.count('cleanup'), 1)

    def test_verification_failure_prevents_every_module_and_application_tag(self):
        def fail(_): raise ValueError('signature policy rejected fixture')
        with self.assertRaisesRegex(ValueError, 'signature policy'):
            self.run_publication(verify=fail)
        self.assertEqual(self.remote.refs, {})
        self.assertNotIn('publish', self.remote.events)

    def test_failed_module_verification_preserves_staging_and_prevents_application_release(self):
        def fail(): raise ValueError('module download failed')
        with self.assertRaisesRegex(ValueError, 'module download'):
            self.run_publication(modules=fail)
        self.assertEqual(self.remote.events.count('create'), 1)
        self.assertNotIn('publish', self.remote.events)

    def test_preexisting_release_without_ownership_is_never_modified(self):
        client = self.remote.client(); client.prepare(self.manifest['commit'])
        assets = publication.inventory(self.out, self.manifest, qualification.Inputs())
        expected = publication.expected_release(self.manifest, 'application', assets)
        client.create_release(expected)
        events = list(self.remote.events)
        with self.assertRaisesRegex(ValueError, 'without original progress'):
            self.run_publication()
        self.assertEqual(events, self.remote.events)

    def test_changed_signature_bundle_cannot_resume_original_publication(self):
        self.interrupt('upload'); events = list(self.remote.events)
        (self.out/'SHA256SUMS.sigstore.json').write_bytes(b'different signing attempt')
        with self.assertRaisesRegex(ValueError, 'different source, graph or registry'):
            self.run_publication()
        self.assertEqual(events, self.remote.events)

    def test_partial_provider_asset_is_held_without_replacement(self):
        self.interrupt('upload'); self.first_release()['assets'][0]['state'] = 'starter'
        events = list(self.remote.events)
        with self.assertRaisesRegex(ValueError, 'incomplete'):
            self.run_publication()
        self.assertEqual(events, self.remote.events)

    def test_corrupt_remote_bytes_prevent_module_tags(self):
        self.interrupt('upload'); asset = self.first_release()['assets'][0]
        self.remote.contents[asset['id']] = b'x'*asset['size']
        with self.assertRaisesRegex(ValueError, 'readback bytes differ'):
            self.run_publication()
        self.assertEqual(self.remote.refs, {})

    def test_replaced_release_id_cannot_resume_original_operation(self):
        self.interrupt('upload'); value = self.first_release(); old_id = value['id']
        value['id'] = self.remote.allocate(); self.remote.releases[value['id']] = value
        del self.remote.releases[old_id]
        with self.assertRaisesRegex(ValueError, 'release object was replaced'):
            self.run_publication()

    def test_verified_asset_disappearance_cannot_be_reuploaded(self):
        def fail(_): raise ValueError('stop after readback')
        with self.assertRaisesRegex(ValueError, 'stop after readback'):
            self.run_publication(verify=fail)
        self.first_release()['assets'].pop()
        events = list(self.remote.events)
        with self.assertRaisesRegex(ValueError, 'verified asset is missing'):
            self.run_publication()
        self.assertEqual(events, self.remote.events)

    def test_completed_publish_receipt_cannot_name_another_release(self):
        self.run_publication(); rows = [json.loads(row) for row in self.progress.read_text().splitlines()]
        next(row for row in rows if row.get('kind') == 'release-publish' and row['state'] == 'verified')['receipt']['id'] += 1000
        self.progress.write_bytes(b''.join(publish.release_bytes(row) for row in rows))
        with self.assertRaisesRegex(ValueError, 'receipts contradict'):
            self.run_publication()

    def test_unexpected_asset_after_upload_is_rejected_before_tags(self):
        def after(kind):
            if kind == 'upload':
                self.first_release()['assets'].append({'id': self.remote.allocate(), 'name': 'unexpected.bin'})
                self.remote.after = None
        self.remote.after = after
        with self.assertRaisesRegex(ValueError, 'unexpected assets'):
            self.run_publication()
        self.assertEqual(self.remote.refs, {})

    def test_failed_api_listing_is_not_release_absence(self):
        self.remote.failure = (503, {'message': 'temporary outage'})
        with self.assertRaisesRegex(ValueError, 'could not be confirmed'):
            self.run_publication()
        self.assertEqual(self.remote.events, [])

    def test_readback_storage_quota_fails_before_mutations(self):
        with patch.object(publication, 'WORK_LIMIT', 1):
            with self.assertRaisesRegex(ValueError, 'storage quota'):
                self.run_publication()
        self.assertEqual(self.remote.events, [])

    def test_receipts_reject_booleans_zero_overflow_extra_fields_and_started_markers(self):
        for receipt in ({'id': True}, {'id': 0}, {'id': 2**64}, {'id': 1, 'tag': 'x'}):
            with self.subTest(receipt=receipt), self.assertRaisesRegex(ValueError, 'object receipt'):
                publish.TransferProgress.validate_receipt('verified', receipt)
        with self.assertRaisesRegex(ValueError, 'object receipt'):
            publish.TransferProgress.validate_receipt('started', {'id': 1})

    def test_published_readback_is_read_only_and_rechecks_original_files(self):
        self.run_publication(); events = list(self.remote.events)
        result = publication.read_published(self.manifest, self.out, self.remote.client(), self.verify_assets, self.work)
        self.assertEqual(result['assets_verified'], len(list(self.out.iterdir())))
        self.assertEqual(events, self.remote.events)
        value = self.first_release(); self.remote.contents[value['assets'][0]['id']] = b'corrupt'
        with self.assertRaisesRegex(ValueError, 'size differs|bytes differ'):
            publication.read_published(self.manifest, self.out, self.remote.client(), self.verify_assets, self.work)
        self.assertEqual(events, self.remote.events)

    def test_published_readback_rejects_mutable_release_and_moved_tag(self):
        self.run_publication(); value = self.first_release()
        value['immutable'] = False
        with self.assertRaisesRegex(ValueError, 'not immutable'):
            publication.read_published(self.manifest, self.out, self.remote.client(), self.verify_assets, self.work)
        value['immutable'] = True; self.remote.refs[self.manifest['version']] = 'b'*40
        with self.assertRaisesRegex(ValueError, 'tag differs'):
            publication.read_published(self.manifest, self.out, self.remote.client(), self.verify_assets, self.work)

    def kill_and_resume(self, kind):
        source = """
import sys
from pathlib import Path
import release, release_publication as publication
manifest = publication.decode_report(Path(sys.argv[1]).read_bytes(), 'fixture source')
client = publication.Releases(origin=sys.argv[2], allow_loopback_http=True, timeout=2)
def modules():
    client.prepare(manifest['commit'])
    for tag in ('sdk/go/v0.2.0-rc.1', 'sdk/go/worker/v0.3.0-rc.1', manifest['version']):
        if client.reference(tag) is None: client.create(tag, manifest['commit'])
def verify(path):
    assert {p.name: p.read_bytes() for p in path.iterdir()} == {p.name: p.read_bytes() for p in Path(sys.argv[3]).iterdir()}
publication.publish_release(manifest, Path(sys.argv[3]), client, modules, verify, Path(sys.argv[4]), Path(sys.argv[5]))
"""
        source_path = self.root/'manifest.json'; source_path.write_bytes(release.encoded(self.manifest))
        holder, accepted = {}, threading.Event()
        def after(event):
            if event == kind:
                os.kill(holder['child'].pid, signal.SIGKILL); accepted.set()
        self.remote.after = after
        child = subprocess.Popen([sys.executable, '-B', '-c', source, str(source_path), self.remote.origin,
                                  str(self.out), str(self.progress), str(self.work)],
                                 cwd=Path(__file__).parent, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        holder['child'] = child
        try:
            stdout, stderr = child.communicate(timeout=15)
            self.assertTrue(accepted.is_set(), (stdout, stderr))
            self.assertEqual(child.returncode, -signal.SIGKILL)
        finally:
            self.remote.after = None
            if child.poll() is None:
                child.kill(); child.wait(timeout=3)
        result = self.run_publication()
        self.assertEqual(self.remote.events.count('publish'), 1)
        self.assertTrue(result['immutable'])
        events = list(self.remote.events); self.run_publication()
        self.assertEqual(events, self.remote.events)

    def test_process_kill_after_remote_publish_then_resume_reads_original_bytes(self):
        self.kill_and_resume('publish')

    def test_process_kill_after_remote_upload_does_not_duplicate_asset(self):
        self.kill_and_resume('upload')
        self.assertEqual(self.remote.events.count('upload'), 2*len(list(self.out.iterdir())))


class SigningCacheTests(unittest.TestCase):
    setUp = ReleasePublicationTests.setUp

    def prepare(self):
        self.journals = self.root/'progress'
        publish.prepare_progress(self.manifest, test_publish.workflow_run(self.manifest), self.journals)

    def fresh_distribution(self):
        target = self.root/'fresh'; target.mkdir(mode=0o700)
        for path in self.out.iterdir():
            if path.name not in publication.SIGNED_FILES:
                (target/path.name).write_bytes(path.read_bytes())
        release.checksums(target)
        return target

    def capture(self):
        self.prepare()
        return publication.capture_signing(self.manifest, self.out, self.journals)

    def test_cache_preserves_exact_original_bundles_and_reuses_only_matching_inputs(self):
        self.assertFalse(self.capture()['reused'])
        self.assertTrue(publication.capture_signing(self.manifest, self.out, self.journals)['reused'])
        cache = self.journals/'signed'
        self.assertEqual(cache.stat().st_mode & 0o777, 0o700)
        for name in publication.SIGNED_FILES:
            self.assertEqual((cache/name).read_bytes(), (self.out/name).read_bytes())
            self.assertEqual((cache/name).stat().st_mode & 0o777, 0o600)
        (self.out/'provenance.sigstore.json').write_bytes(b'resigned')
        with self.assertRaisesRegex(ValueError, 'cannot be replaced'):
            publication.capture_signing(self.manifest, self.out, self.journals)

    def test_restore_replaces_only_derived_unsigned_checksums_and_is_repeatable(self):
        self.capture(); fresh = self.fresh_distribution()
        self.assertTrue(publication.restore_signing(self.manifest, fresh, self.journals)['reused'])
        self.assertEqual({p.name: p.read_bytes() for p in fresh.iterdir()},
                         {p.name: p.read_bytes() for p in self.out.iterdir()})
        self.assertTrue(publication.restore_signing(self.manifest, fresh, self.journals)['reused'])

    def test_missing_cache_requires_new_signing_without_modifying_distribution(self):
        self.prepare(); fresh = self.fresh_distribution()
        before = {p.name: p.read_bytes() for p in fresh.iterdir()}
        self.assertFalse(publication.restore_signing(self.manifest, fresh, self.journals)['reused'])
        self.assertEqual(before, {p.name: p.read_bytes() for p in fresh.iterdir()})

    def test_corrupt_cache_changed_unsigned_and_conflicting_bundle_fail_before_copy(self):
        self.capture(); fresh = self.fresh_distribution()
        path = fresh/'cpra-linux_amd64.spdx.json'; original = path.read_bytes(); path.write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError, 'unsigned distribution differs'):
            publication.restore_signing(self.manifest, fresh, self.journals)
        path.write_bytes(original)
        (fresh/'provenance.sigstore.json').write_bytes(b'unrelated bundle')
        with self.assertRaisesRegex(ValueError, 'existing signing material differs'):
            publication.restore_signing(self.manifest, fresh, self.journals)
        (fresh/'provenance.sigstore.json').unlink()
        (self.journals/'signed/trusted_root.jsonl').write_bytes(b'corrupt root')
        with self.assertRaises(ValueError):
            publication.restore_signing(self.manifest, fresh, self.journals)
        self.assertFalse((fresh/'trusted_root.jsonl').exists())

    def test_extra_files_links_and_quotas_are_rejected(self):
        self.capture(); cache = self.journals/'signed'
        extra = cache/'credentials'; extra.write_bytes(b'must never enter progress')
        with self.assertRaisesRegex(ValueError, 'unexpected'):
            publication.signing_cache(cache, self.manifest, qualification.Inputs())
        extra.unlink(); original = (cache/'trusted_root.jsonl').read_bytes()
        (cache/'trusted_root.jsonl').unlink(); outside = self.root/'root-copy'; outside.write_bytes(original)
        (cache/'trusted_root.jsonl').symlink_to(outside)
        with self.assertRaises(ValueError):
            publication.signing_cache(cache, self.manifest, qualification.Inputs())
        (cache/'trusted_root.jsonl').unlink(); (cache/'trusted_root.jsonl').write_bytes(original)
        with patch.object(publication, 'SIGNING_LIMIT', 1), self.assertRaisesRegex(ValueError, 'quota'):
            publication.signing_cache(cache, self.manifest, qualification.Inputs())

    def test_workflow_import_preserves_bundles_and_receipt_journals_without_credentials(self):
        self.capture()
        header = {'schema_version': 1, 'identity': {'release_identity': release.evidence_identity(self.manifest)}}
        (self.journals/'releases.jsonl').write_bytes(publish.release_bytes(header))
        destination = self.root/'next-progress'
        publish.prepare_progress(self.manifest, test_publish.workflow_run(self.manifest, 43), destination,
                                 incoming=self.journals, previous_run=test_publish.workflow_run(self.manifest))
        self.assertEqual((destination/'releases.jsonl').read_bytes(), (self.journals/'releases.jsonl').read_bytes())
        for name in (*publication.SIGNED_FILES, publication.SIGNING_LEDGER):
            self.assertEqual((destination/'signed'/name).read_bytes(), (self.journals/'signed'/name).read_bytes())
            self.assertEqual((destination/'signed'/name).stat().st_mode & 0o777, 0o600)
        self.assertTrue(publication.restore_signing(self.manifest, self.fresh_distribution(), destination)['reused'])

    def test_local_capture_and_restore_commands_require_no_public_credentials(self):
        self.prepare()
        environment = {key: value for key, value in os.environ.items() if key not in ('GH_TOKEN', 'GITHUB_TOKEN')}
        base = [sys.executable, '-B', str(release.ROOT/'scripts/release/publish.py'), '--progress-dir', str(self.journals)]
        result = subprocess.run([*base, '--phase', 'capture-signing', '--out', str(self.out)],
                                env=environment, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(json.loads(result.stdout)['reused'])
        result = subprocess.run([*base, '--phase', 'restore-signing', '--out', str(self.fresh_distribution())],
                                env=environment, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads(result.stdout)['reused'])

    def test_unsigned_checksum_override_and_extra_distribution_signatures_are_rejected(self):
        self.capture(); fresh = self.fresh_distribution()
        (fresh/'SHA256SUMS').write_bytes(b'forged manifest')
        with self.assertRaisesRegex(ValueError, 'existing signing material differs'):
            publication.restore_signing(self.manifest, fresh, self.journals)
        (self.out/'unlisted.sigstore.json').write_bytes(b'unlisted')
        with self.assertRaisesRegex(ValueError, 'unexpected files'):
            publication.inventory(self.out, self.manifest, qualification.Inputs())

    def test_workflow_import_bounds_files_that_grow_after_inspection(self):
        self.capture()
        inspect = publication.signing_cache
        path = self.journals/'signed/trusted_root.jsonl'
        original_read = Path.read_bytes
        def inspected(*args):
            result = inspect(*args)
            with path.open('r+b') as stream:
                stream.truncate(publication.SIGNING_LIMIT + 1)
            return result
        def guarded_read(file):
            if file == path:
                raise AssertionError('unbounded signing-cache read after inspection')
            return original_read(file)
        destination = self.root/'next-progress'
        with patch.object(publication, 'signing_cache', side_effect=inspected), patch.object(Path, 'read_bytes', guarded_read):
            with self.assertRaisesRegex(ValueError, 'quota'):
                publish.prepare_progress(self.manifest, test_publish.workflow_run(self.manifest, 43), destination,
                                         incoming=self.journals, previous_run=test_publish.workflow_run(self.manifest))
        self.assertFalse(destination.exists())


def asset_provenance(owner):
    """Synthetic signature for binding contracts; not cryptographic evidence."""
    graph = {**owner['unsigned'], 'trusted_root.jsonl': owner['inputs']['trusted_root.jsonl']}
    statement = {'_type': 'https://in-toto.io/Statement/v1', 'predicateType': 'https://slsa.dev/provenance/v1',
                 'subject': [{'name': name, 'digest': {'sha256': row['sha256']}} for name, row in sorted(graph.items())],
                 'predicate': {'buildDefinition': {'buildType': 'https://actions.github.io/buildtypes/workflow/v1',
                     'externalParameters': {'workflow': {'ref': 'refs/heads/main',
                         'repository': 'https://github.com/'+publish.REPOSITORY, 'path': '.github/workflows/release.yml'}},
                     'resolvedDependencies': [{'uri': 'git+https://github.com/'+publish.REPOSITORY+'@refs/heads/main',
                         'digest': {'gitCommit': owner['release_identity']['commit']}}]}}}
    return release.encoded({'mediaType': 'application/vnd.dev.sigstore.bundle.v0.3+json', 'verificationMaterial': {},
        'dsseEnvelope': {'payloadType': 'application/vnd.in-toto+json',
                         'payload': base64.b64encode(release.encoded(statement)).decode(),
                         'signatures': [{'sig': base64.b64encode(b'contract fixture').decode()}]}})


def asset_signature(owner):
    return release.encoded({'mediaType': 'application/vnd.dev.sigstore.bundle.v0.3+json', 'verificationMaterial': {},
        'messageSignature': {'messageDigest': {'algorithm': 'SHA2_256', 'digest': base64.b64encode(
            bytes.fromhex(owner['inputs']['SHA256SUMS']['sha256'])).decode()},
            'signature': base64.b64encode(b'contract fixture').decode()}})


class AssetSigningBackend:
    """Recording command double; real-key execution is a separate integration."""
    def __init__(self, owner):
        self.owner, self.calls, self.after_sign, self.fail_verify, self.fail_provenance = owner, [], None, False, False

    def trust(self):
        return ['--certificate-identity', publish.IDENTITY, '--certificate-oidc-issuer', publish.ISSUER]

    def command(self, arguments):
        self.calls.append(arguments)
        if arguments[0] == 'sign-blob':
            Path(arguments[arguments.index('--bundle')+1]).write_bytes(asset_signature(self.owner))
            if self.after_sign:
                self.after_sign()
        elif self.fail_verify and arguments[0] == 'verify-blob' or self.fail_provenance and arguments[0] == 'verify-blob-attestation':
            raise ValueError('fixture cryptographic verification rejected')


class AssetSigningTests(unittest.TestCase):
    prepare = SigningCacheTests.prepare
    fresh_distribution = SigningCacheTests.fresh_distribution

    def setUp(self):
        ReleasePublicationTests.setUp(self)
        for name in publication.ASSET_SIGNING_ROLES:
            (self.out/name).unlink()
        self.prepare()
        self.signing = publication.AssetSigning(self.manifest, self.out, self.journals)
        self.backend = AssetSigningBackend(self.signing.owner)
        self.provenance = self.root/'original-provenance.json'
        self.provenance.write_bytes(asset_provenance(self.signing.owner))

    def accepted_provenance(self):
        self.assertTrue(self.signing.prepare_provenance()['generate'])
        self.signing.capture_provenance(self.provenance)

    def test_partial_provenance_resume_reuses_original_inputs_and_each_bundle(self):
        self.accepted_provenance()
        original = self.provenance.read_bytes()
        self.provenance.unlink()
        fresh = self.fresh_distribution()
        result = publication.restore_signing(self.manifest, fresh, self.journals)
        self.assertFalse(result['reused']); self.assertTrue(result['inputs_ready'])
        restored = publication.AssetSigning(self.manifest, fresh, self.journals)
        self.assertFalse(restored.prepare_provenance()['generate'])
        restored.sign_checksum(self.backend)
        self.assertEqual((fresh/'provenance.sigstore.json').read_bytes(), original)
        original_files = {name: (fresh/name).read_bytes() for name in publication.SIGNED_FILES}
        restored.sign_checksum(self.backend)
        self.assertEqual(sum(call[0] == 'sign-blob' for call in self.backend.calls), 1)
        self.assertEqual(original_files, {name: (fresh/name).read_bytes() for name in publication.SIGNED_FILES})
        publication.capture_signing(self.manifest, fresh, self.journals)

    def test_started_provenance_without_output_stops_before_any_second_generation(self):
        self.assertTrue(self.signing.prepare_provenance()['generate'])
        for operation in (self.signing.prepare_provenance,
                          lambda: publication.restore_signing(self.manifest, self.fresh_distribution(), self.journals)):
            with self.assertRaisesRegex(ValueError, 'without recoverable material'):
                operation()
        self.assertFalse(self.backend.calls)

    def test_output_saved_before_receipt_recovers_without_generating_again(self):
        self.signing.prepare_provenance()
        raw = self.signing.directory/publication.ASSET_SIGNING_ROLES[0]
        raw.write_bytes(self.provenance.read_bytes())
        self.assertFalse(self.signing.prepare_provenance()['generate'])
        self.assertTrue((self.signing.directory/(raw.name+'.record.json')).exists())

    def test_lost_sign_reply_reuses_original_output_and_verifies_it(self):
        self.accepted_provenance()
        def failure():
            raise RuntimeError('fixture lost signing reply')
        self.backend.after_sign = failure
        with self.assertRaisesRegex(RuntimeError, 'lost signing reply'):
            self.signing.sign_checksum(self.backend)
        original = (self.signing.directory/publication.ASSET_SIGNING_ROLES[1]).read_bytes()
        self.backend.after_sign = None
        self.signing.sign_checksum(self.backend)
        self.assertEqual(sum(call[0] == 'sign-blob' for call in self.backend.calls), 1)
        self.assertEqual((self.out/publication.ASSET_SIGNING_ROLES[1]).read_bytes(), original)

    def test_verification_failure_holds_original_signature_without_resigning(self):
        self.accepted_provenance(); self.backend.fail_verify = True
        with self.assertRaisesRegex(ValueError, 'verification rejected'):
            self.signing.sign_checksum(self.backend)
        self.backend.fail_verify = False; self.signing.sign_checksum(self.backend)
        self.assertEqual(sum(call[0] == 'sign-blob' for call in self.backend.calls), 1)

    def test_untrusted_provenance_cannot_start_checksum_signing(self):
        self.accepted_provenance(); self.backend.fail_provenance = True
        with self.assertRaisesRegex(ValueError, 'verification rejected'):
            self.signing.sign_checksum(self.backend)
        self.assertEqual([call[0] for call in self.backend.calls], ['verify-blob-attestation'])
        self.assertFalse((self.signing.directory/'SHA256SUMS.sigstore.json').exists())
        with self.signing.session() as journal:
            self.assertEqual(len(journal.records), 2)

    def test_actual_process_kill_after_output_recovers_and_before_output_holds(self):
        self.accepted_provenance()
        for output in (False, True):
            with self.subTest(output=output):
                progress = self.root/('process-'+str(output)); progress.mkdir(mode=0o700)
                for path in self.journals.iterdir():
                    if path.is_dir():
                        import shutil
                        shutil.copytree(path, progress/path.name)
                    else:
                        (progress/path.name).write_bytes(path.read_bytes())
                        (progress/path.name).chmod(0o600)
                script = '''import json,os,signal,sys
from pathlib import Path
import release_publication as p
from test_release_publication import AssetSigningBackend,asset_signature
root,progress,output=sys.argv[1:]
s=p.AssetSigning(json.loads((Path(root)/'RELEASE.json').read_text()),Path(root),Path(progress))
class Backend(AssetSigningBackend):
 def command(self, args):
  if args[0]=='sign-blob':
   if output=='True': Path(args[args.index('--bundle')+1]).write_bytes(asset_signature(s.owner))
   os.kill(os.getpid(),signal.SIGKILL)
  super().command(args)
s.sign_checksum(Backend(s.owner))
'''
                result = subprocess.run([sys.executable, '-B', '-c', script, str(self.out), str(progress), str(output)],
                                        cwd=release.ROOT/'scripts/release', capture_output=True, timeout=10)
                self.assertEqual(result.returncode, -signal.SIGKILL, result.stderr)
                resumed = publication.AssetSigning(self.manifest, self.out, progress)
                backend = AssetSigningBackend(resumed.owner)
                if output:
                    resumed.sign_checksum(backend)
                    self.assertEqual([call[0] for call in backend.calls], ['verify-blob-attestation', 'verify-blob'])
                else:
                    with self.assertRaisesRegex(ValueError, 'without recoverable material'):
                        resumed.sign_checksum(backend)
                    self.assertFalse(backend.calls)

    def test_changed_inputs_subjects_source_and_completed_output_are_rejected(self):
        self.signing.prepare_provenance()
        bad = json.loads(self.provenance.read_bytes())
        statement = json.loads(base64.b64decode(bad['dsseEnvelope']['payload']))
        statement['subject'][0]['digest']['sha256'] = 'b'*64
        bad['dsseEnvelope']['payload'] = base64.b64encode(release.encoded(statement)).decode()
        self.provenance.write_bytes(release.encoded(bad))
        with self.assertRaisesRegex(ValueError, 'subjects differ'):
            self.signing.capture_provenance(self.provenance)
        self.provenance.write_bytes(asset_provenance(self.signing.owner)); self.signing.capture_provenance(self.provenance)
        raw = self.signing.directory/'provenance.sigstore.json'; raw.write_bytes(raw.read_bytes()+b' ')
        with self.assertRaisesRegex(ValueError, 'original bytes'):
            self.signing.prepare_provenance()

    def test_original_partial_cache_and_journal_survive_workflow_import(self):
        self.accepted_provenance(); destination = self.root/'next-progress'
        publish.prepare_progress(self.manifest, test_publish.workflow_run(self.manifest, 43), destination,
                                 incoming=self.journals, previous_run=test_publish.workflow_run(self.manifest))
        self.assertEqual((destination/publication.ASSET_SIGNING_JOURNAL).read_bytes(),
                         (self.journals/publication.ASSET_SIGNING_JOURNAL).read_bytes())
        for path in self.signing.directory.iterdir():
            imported = destination/publication.ASSET_SIGNING_CACHE/path.name
            self.assertEqual(imported.read_bytes(), path.read_bytes())
            self.assertEqual(imported.stat().st_mode & 0o777, 0o600)
        fresh = self.fresh_distribution()
        self.assertTrue(publication.restore_signing(self.manifest, fresh, destination)['inputs_ready'])
        self.assertFalse(publication.AssetSigning(self.manifest, fresh, destination).prepare_provenance()['generate'])

    def test_one_owner_out_of_order_and_unsigned_changes_stop(self):
        with self.signing.session(), self.assertRaisesRegex(ValueError, 'locked by another owner'):
            self.signing.prepare_provenance()
        with self.assertRaisesRegex(ValueError, 'provenance must be preserved'):
            self.signing.sign_checksum(self.backend)
        (self.out/'OCI.json').write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError, 'unsigned distribution differs'):
            publication.AssetSigning(self.manifest, self.out, self.journals)

    def test_extra_linked_and_oversized_partial_material_is_rejected(self):
        self.accepted_provenance()
        extra = self.signing.directory/'credentials'; extra.write_bytes(b'fixture')
        with self.assertRaisesRegex(ValueError, 'unexpected files'):
            self.signing.prepare_provenance()
        extra.unlink()
        raw = self.signing.directory/'provenance.sigstore.json'; original = raw.read_bytes(); raw.unlink()
        raw.symlink_to(self.provenance)
        with self.assertRaisesRegex(ValueError, 'link or alias'):
            self.signing.prepare_provenance()
        raw.unlink(); raw.write_bytes(original)
        with patch.object(publication, 'SIGNING_LIMIT', 1), self.assertRaisesRegex(ValueError, 'quota'):
            self.signing.prepare_provenance()


def asset_integration(backend, directory, environment):
    """Actual local-key crypto and killed processes over synthetic release bytes."""
    directory = Path(directory); directory.mkdir(mode=0o700)
    fixture = AssetSigningTests('test_partial_provenance_resume_reuses_original_inputs_and_each_bundle')
    fixture.setUp()
    try:
        signing = fixture.signing
        signing.prepare_provenance()
        proof = fixture.root/'crypto-provenance.json'
        backend.dsse(asset_provenance(signing.owner), proof)
        subject = next(iter(signing.owner['unsigned'].values()))['sha256']
        backend.command(['verify-blob-attestation', '--bundle', str(proof), '--digestAlg', 'sha256',
                         '--digest', subject, '--type', 'https://slsa.dev/provenance/v1', *backend.trust()])
        signing.capture_provenance(proof)
        original_provenance = release.sha(proof)
        child = '''import json,os,signal,sys
from pathlib import Path
import registry,release_publication as p
from test_image_signing import LocalKeyCosign
out,progress,executable,key,work,origin,boundary,marker=sys.argv[1:]
remote=registry.Registry('ziad-hsn/cpra',origin=origin,allow_loopback_http=True)
class Interrupted(LocalKeyCosign):
 def command(self,args):
  if args[0]=='sign-blob':
   with open(marker,'a') as log: log.write('invocation\\n');log.flush();os.fsync(log.fileno())
   if boundary=='before-output':os.kill(os.getpid(),signal.SIGKILL)
  super().command(args)
  if args[0]=='sign-blob':os.kill(os.getpid(),signal.SIGKILL)
backend=Interrupted(Path(executable),remote,Path(work),Path(key))
p.AssetSigning(json.loads((Path(out)/'RELEASE.json').read_text()),Path(out),Path(progress)).sign_checksum(backend)
'''
        observations = {}
        for boundary in ('before-output', 'after-output'):
            progress = fixture.root/boundary
            publish.prepare_progress(fixture.manifest, test_publish.workflow_run(fixture.manifest, 43), progress,
                                     incoming=fixture.journals, previous_run=test_publish.workflow_run(fixture.manifest))
            marker = directory/(boundary+'.invocations')
            env = dict(environment, PYTHONPATH=str(Path(__file__).parent), PYTHONDONTWRITEBYTECODE='1')
            killed = subprocess.run([sys.executable, '-B', '-c', child, str(fixture.out), str(progress),
                                     str(backend.executable), str(backend.key), str(backend.work), backend.remote.origin,
                                     boundary, str(marker)], env=env, capture_output=True, timeout=90)
            if killed.returncode != -signal.SIGKILL:
                raise ValueError('Actual asset signing crash boundary failed: '+killed.stderr.decode(errors='replace')[-2000:])
            resumed = publication.AssetSigning(fixture.manifest, fixture.out, progress)
            calls = []
            command = backend.command
            def recorded(arguments):
                calls.append(arguments[0]); command(arguments)
            if boundary == 'before-output':
                with patch.object(backend, 'command', side_effect=recorded):
                    try:
                        resumed.sign_checksum(backend)
                    except ValueError as error:
                        if 'without recoverable material' not in str(error):
                            raise
                    else:
                        raise ValueError('Uncertain asset signing was repeated')
                if calls:
                    raise ValueError('Held signing invoked an external command')
                observations[boundary] = {'sigkill': True, 'held': True, 'resume_commands': []}
            else:
                raw = resumed.directory/'SHA256SUMS.sigstore.json'; original = release.sha(raw)
                with patch.object(backend, 'command', side_effect=recorded):
                    resumed.sign_checksum(backend); resumed.sign_checksum(backend)
                if calls != ['verify-blob-attestation', 'verify-blob']*2 or release.sha(raw) != original:
                    raise ValueError('Signing continuation changed or regenerated the original output')
                publication.capture_signing(fixture.manifest, fixture.out, progress)
                fresh = fixture.fresh_distribution()
                publication.restore_signing(fixture.manifest, fresh, progress)
                if release.sha(fresh/'provenance.sigstore.json') != original_provenance:
                    raise ValueError('Signing continuation replaced the original provenance')
                for name in publication.SIGNED_FILES:
                    (directory/name).write_bytes((fresh/name).read_bytes())
                for name in ('identity.json', 'provenance.sigstore.json.record.json', 'SHA256SUMS.sigstore.json.record.json'):
                    (directory/name).write_bytes((resumed.directory/name).read_bytes())
                (directory/publication.ASSET_SIGNING_JOURNAL).write_bytes((progress/publication.ASSET_SIGNING_JOURNAL).read_bytes())
                (directory/'signing.json').write_bytes((progress/'signed/signing.json').read_bytes())
                observations[boundary] = {'sigkill': True, 'original_signature_sha256': original,
                                          'resume_commands': calls, 'signer_invocations': marker.read_text().splitlines()}
        (directory/'fixture-key.pub').write_bytes(backend.key.with_suffix('.pub').read_bytes())
        return {'status': 'pass', 'boundaries': observations, 'original_provenance_sha256': original_provenance,
                'bundle_sha256': {name: release.sha(directory/name) for name in publication.SIGNED_FILES},
                'fixture': 'ephemeral local keys and synthetic release assets; no GitHub identity or transparency proof',
                'public_objects_created': False, 'release_gate_passed': False}
    finally:
        fixture.doCleanups()


class ReleaseHTTPBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.remote = ReleaseFixture('a'*40); self.addCleanup(self.remote.close)
        self.client = self.remote.client()

    def test_unprepared_paths_upload_origins_and_remote_identifiers_are_rejected(self):
        with self.assertRaisesRegex(ValueError, 'access must be established'):
            self.client.open_release('GET', '/repos/ziad-hsn/cpra/releases/1')
        self.client.prepare('a'*40)
        for method, path in [('GET', '/repos/other/cpra/releases/1'), ('DELETE', '/repos/ziad-hsn/cpra/releases/assets/1'),
                             ('POST', '/repos/ziad-hsn/cpra/releases/1/assets?name=one&label=two')]:
            with self.subTest(method=method, path=path), self.assertRaisesRegex(ValueError, 'unexpected'):
                self.client.open_release(method, path)
        for value in (True, 0, -1, 2**64, '1'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.client.get(value)

    def test_source_change_after_preflight_cannot_change_uploaded_bytes(self):
        import tempfile
        self.client.prepare('a'*40)
        self.remote.releases[1] = {'id': 1, 'tag_name': 'fixture-upload', 'draft': True, 'assets': []}
        with tempfile.TemporaryDirectory(prefix='cpra-upload-fixture-') as temporary:
            path = Path(temporary) / 'payload.bin'
            original = b'approved-artifact-bytes' * 4096
            altered = b'x' * len(original)
            path.write_bytes(original)
            record = {'size': len(original), 'sha256': hashlib.sha256(original).hexdigest()}
            opener = self.client.opener.open

            def mutate_before_send(request, **kwargs):
                if request.get_method() == 'POST':
                    path.write_bytes(altered)
                return opener(request, **kwargs)

            with patch.object(self.client.opener, 'open', side_effect=mutate_before_send):
                value = self.client.upload(1, path, record, 'fixture-upload', work=Path(temporary))
            self.assertEqual(hashlib.sha256(self.remote.contents[value['id']]).hexdigest(), record['sha256'])
            self.assertEqual(value['digest'], 'sha256:' + record['sha256'])
            self.assertEqual(self.remote.events, ['upload'])
            self.assertEqual(list(Path(temporary).glob('asset-upload-*')), [])

    def test_unsafe_inputs_and_exhausted_quota_send_no_upload_request(self):
        import tempfile
        self.client.prepare('a'*40)
        with tempfile.TemporaryDirectory(prefix='cpra-upload-fixture-') as temporary:
            work = Path(temporary)
            original, path = work / 'original', work / 'payload.bin'
            original.write_bytes(b'approved')
            record = {'size': 8, 'sha256': hashlib.sha256(b'approved').hexdigest()}
            for kind in ('symlink', 'hardlink', 'fifo', 'changed'):
                if kind == 'symlink':
                    path.symlink_to(original)
                elif kind == 'hardlink':
                    os.link(original, path)
                elif kind == 'fifo':
                    os.mkfifo(path)
                else:
                    path.write_bytes(b'altered!')
                with self.subTest(kind=kind), self.assertRaises(ValueError):
                    self.client.upload(1, path, record, 'fixture-upload', work=work)
                path.unlink()
            path.write_bytes(b'approved')
            with patch.object(publication, 'WORK_LIMIT', 1), self.assertRaisesRegex(ValueError, 'quota'):
                self.client.upload(1, path, record, 'fixture-upload', work=work)
            self.assertEqual(list(work.glob('asset-upload-*')), [])
        self.assertFalse(any(method == 'POST' for method, _ in self.remote.requests))

    def test_lost_upload_response_preserves_one_remote_effect_and_cleans_staging(self):
        import tempfile
        self.client.prepare('a'*40)
        self.remote.releases[1] = {'id': 1, 'tag_name': 'fixture-upload', 'draft': True, 'assets': []}
        self.remote.lost = 'upload'
        with tempfile.TemporaryDirectory(prefix='cpra-upload-fixture-') as temporary:
            work = Path(temporary)
            path = work / 'payload.bin'
            path.write_bytes(b'approved')
            record = {'size': 8, 'sha256': hashlib.sha256(b'approved').hexdigest()}
            with self.assertRaisesRegex(ValueError, 'could not be confirmed'):
                self.client.upload(1, path, record, 'fixture-upload', work=work)
            self.assertEqual(list(work.glob('asset-upload-*')), [])
        self.assertEqual(self.remote.events, ['upload'])
        self.assertEqual(list(self.remote.contents.values()), [b'approved'])

    def test_page_limit_never_establishes_absence_or_creates_a_release(self):
        self.client.prepare('a'*40)
        for number in range(10):
            self.remote.releases[number+1] = {'id': number+1, 'tag_name': 'other-'+str(number)}
        with patch.object(publication, 'PAGE_LIMIT', 1), self.assertRaisesRegex(ValueError, 'no absence is established'):
            self.client.find('v0.1.0-rc.1')
        self.assertEqual(self.remote.events, [])

    def test_untrusted_redirects_never_receive_api_credentials(self):
        client = publication.Releases('fixture-token-never-used-for-network')
        for url in ('https://example.com/file', 'http://release-assets.githubusercontent.com/file',
                    'https://release-assets.githubusercontent.com@evil.example/file',
                    'https://release-assets.githubusercontent.com:443/file',
                    'https://release-assets.githubusercontent.com/file#fragment',
                    'https://release-assets.githubusercontent.com/file\r\nAuthorization: secret'):
            with self.subTest(url=url), patch.object(client.opener, 'open') as opener, self.assertRaises(ValueError):
                client.storage_response(url)
            opener.assert_not_called()

    def test_supported_asset_redirect_is_anonymous_and_cannot_redirect_again(self):
        client = publication.Releases('fixture-token-never-used-for-network')
        response = io.BytesIO(b'asset'); response.status = 200
        with patch.object(client.opener, 'open', return_value=response) as opener:
            self.assertIs(client.storage_response('https://release-assets.githubusercontent.com/asset?sig=public'), response)
            request = opener.call_args.args[0]
            self.assertNotIn('Authorization', request.headers)
            self.assertEqual(request.full_url, 'https://release-assets.githubusercontent.com/asset?sig=public')
        error = urllib.error.HTTPError('https://release-assets.githubusercontent.com/asset', 302, 'Found',
                                       {'Location': 'https://example.com/file'}, io.BytesIO())
        with patch.object(client.opener, 'open', side_effect=error) as opener, self.assertRaisesRegex(ValueError, 'could not be confirmed'):
            client.storage_response('https://release-assets.githubusercontent.com/asset')
        self.assertEqual(opener.call_count, 1)

    def test_response_metadata_and_file_counts_are_bounded(self):
        self.client.prepare('a'*40)
        self.remote.releases[1] = {'id': 1, 'tag_name': 'v0.1.0-rc.1', 'assets': [{'id': 2}, {'id': 3}]}
        with patch.object(publication, 'FILE_LIMIT', 1), self.assertRaisesRegex(ValueError, 'file limit'):
            self.client.assets(1)
        with patch.object(publication, 'JSON_LIMIT', 1), self.assertRaisesRegex(ValueError, 'limit|quota|oversized'):
            self.client.get(1)


if __name__ == '__main__':
    unittest.main()
