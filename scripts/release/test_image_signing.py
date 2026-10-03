"""Signing intent, original bundle preservation, discovery and crash contracts."""
import base64
import copy
import hashlib
import http.server
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

import yaml

import image_signing
import oci
import publish
import qualification
import registry
import release
from test_publish import workflow_run
from test_registry import fixture


def provenance(manifest):
    return {'buildDefinition': {'buildType': 'https://actions.github.io/buildtypes/workflow/v1',
            'externalParameters': {'workflow': {'ref': 'refs/heads/main',
                'repository': 'https://github.com/'+publish.REPOSITORY, 'path': '.github/workflows/release.yml'}},
            'resolvedDependencies': [{'uri': 'git+https://github.com/'+publish.REPOSITORY+'@refs/heads/main',
                                      'digest': {'gitCommit': manifest['commit']}}]}, 'runDetails': {}}


def bundle(entry, owner):
    """Synthetic signatures only; separate real-key integration verifies crypto."""
    subject = {'digest': {'sha256': entry['subject']['digest'][7:]}}
    if entry['predicate_type'] == image_signing.SIGNATURE_TYPE:
        subject['annotations'] = {'cpra.io/source-commit': owner['subjects']['commit'],
                                  'cpra.io/release-identity': image_signing.identity_digest(owner)}
        predicate = {}
    else:
        subject['name'] = owner['subjects']['image']
        predicate = provenance({'commit': owner['subjects']['commit']})
    statement = {'_type': 'https://in-toto.io/Statement/v1', 'subject': [subject],
                 'predicateType': entry['predicate_type'], 'predicate': predicate}
    return release.encoded({'mediaType': image_signing.BUNDLE_MEDIA, 'verificationMaterial': {},
        'dsseEnvelope': {'payloadType': 'application/vnd.in-toto+json',
                         'payload': base64.b64encode(release.encoded(statement)).decode(),
                         'signatures': [{'sig': base64.b64encode(b'fixture-signature').decode()}]}})


class Backend:
    def __init__(self):
        self.signed, self.verified, self.remote = [], [], []

    def sign(self, entry, path, owner):
        self.signed.append(entry['role'])
        path.write_bytes(bundle(entry, owner)); path.chmod(0o600)

    def verify_bundle(self, entry, path):
        self.verified.append((entry['role'], str(path)))

    def verify_remote(self, entry):
        self.remote.append(entry['role'])


class Server:
    def __init__(self, native):
        self.native, self.objects, self.requests, self.links = native, {}, [], {}
        owner = self
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_GET(self):
                owner.requests.append(self.path)
                reference = self.path.rsplit('/', 1)[-1]
                headers = {}
                if '/referrers/' in self.path:
                    if owner.native:
                        subject = reference.split('?', 1)[0]
                        rows = []
                        for (kind, _), (row, data) in owner.objects.items():
                            if kind == 'manifest' and row['media_type'] == oci.MANIFEST:
                                value = json.loads(data)
                                if value.get('subject', {}).get('digest') == subject:
                                    rows.append({'mediaType': oci.MANIFEST, 'digest': row['digest'], 'size': row['size'],
                                                 'artifactType': value['artifactType'], 'annotations': value['annotations']})
                        status, body = 200, release.encoded({'schemaVersion': 2, 'mediaType': oci.INDEX, 'manifests': rows})
                        headers['Content-Type'] = oci.INDEX
                        if self.path in owner.links:
                            headers['Link'] = owner.links[self.path]
                    else:
                        status, body = 404, release.encoded({'errors': [{'code': 'UNSUPPORTED'}]})
                else:
                    kind = 'blob' if '/blobs/' in self.path else 'manifest'
                    item = owner.objects.get((kind, reference))
                    if item is None:
                        status, body = 404, release.encoded({'errors': [{'code': 'BLOB_UNKNOWN' if kind == 'blob' else 'MANIFEST_UNKNOWN'}]})
                    else:
                        row, body = item
                        status, headers = 200, {'Content-Type': row['media_type'], 'Docker-Content-Digest': row['digest']}
                self.send_response(status)
                for name, value in headers.items():
                    self.send_header(name, value)
                self.send_header('Content-Length', str(len(body))); self.end_headers()
                try:
                    self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    pass
        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True); self.thread.start()
        self.origin = 'http://127.0.0.1:'+str(self.server.server_port)

    def close(self):
        self.server.shutdown(); self.server.server_close(); self.thread.join(timeout=5)


class Remote(registry.Registry):
    """Upload double, with actual HTTP readback and referrer pagination."""
    def __init__(self, server):
        super().__init__('ziad-hsn/cpra', origin=server.origin, allow_loopback_http=True)
        self.server, self.uploads = server, []
        self.fail_after = False

    def upload(self, oras, row, path, work, *, referrer=False, referrer_subject=None):
        self.uploads.append((row['digest'], referrer, referrer_subject))
        reference = registry.referrer_tag(row, referrer_subject, self.repository) if referrer_subject else row['digest']
        self.server.objects[(row['kind'], reference)] = (copy.deepcopy(row), path.read_bytes())
        if self.fail_after:
            self.fail_after = False
            raise RuntimeError('fixture lost registry reply')


def setup(root, backend=None):
    manifest, reports, assembly, subjects = fixture(root)
    descriptors = {row['digest']: {'mediaType': row['media_type'], 'digest': row['digest'], 'size': row['size']}
                   for row, _ in publish.image_objects(assembly, subjects) if row['kind'] == 'manifest'}
    progress = root/'progress'
    publish.prepare_progress(manifest, workflow_run(manifest), progress)
    return image_signing.Bundles(manifest, subjects, descriptors, progress, backend or Backend()), assembly


class LocalKeyCosign(image_signing.Cosign):
    """Explicit local integration identity; never selected by the publisher."""
    def __init__(self, executable, remote, work, key):
        if not remote.fixture:
            raise ValueError('Fixture identities cannot access production registries')
        super().__init__(executable, remote, work)
        self.key = Path(key)

    def trust(self):
        return ['--key', str(self.key.with_suffix('.pub')), '--insecure-ignore-tlog=true']

    def environment(self, directory):
        return dict(super().environment(directory), COSIGN_PASSWORD='')

    def command(self, arguments):
        arguments = list(arguments)
        if arguments[0] in ('sign', 'sign-blob'):
            arguments += ['--key', str(self.key.with_suffix('.key')), '--use-signing-config=false', '--tlog-upload=false']
        if arguments[0] in ('sign', 'verify', 'verify-attestation'):
            arguments += ['--allow-http-registry=true']
        super().command(arguments)

    def provenance(self, bundles, path):
        # Sign DSSE PAE using the actual cosign executable. attest-blob uses an
        # older in-toto statement/name convention; the GitHub action emits v1.
        entry = bundles.entries['provenance-index']
        self.dsse(bundle(entry, bundles.owner), path)

    def dsse(self, data, path):
        envelope = json.loads(data)['dsseEnvelope']
        payload = base64.b64decode(envelope['payload']); kind = envelope['payloadType'].encode()
        pae = b'DSSEv1 '+str(len(kind)).encode()+b' '+kind+b' '+str(len(payload)).encode()+b' '+payload
        source, signed = self.work/'fixture-pae', self.work/'fixture-message.json'
        source.write_bytes(pae)
        self.command(['sign-blob', '--yes', '--bundle', str(signed), str(source)])
        material = json.loads(signed.read_bytes())
        envelope['signatures'] = [{'sig': material['messageSignature']['signature']}]
        path.write_bytes(release.encoded({'mediaType': image_signing.BUNDLE_MEDIA,
            'verificationMaterial': material['verificationMaterial'], 'dsseEnvelope': envelope}))
        source.unlink(); signed.unlink()


def integration(tools, origin, root, environment):
    """Real cosign keys, exact ORAS uploads, SIGKILL and registry discovery."""
    tools, root = Path(tools).absolute(), Path(root).absolute()
    root.mkdir(mode=0o700)
    bundles, assembly = setup(root)
    key = root/'fixture-key'
    remote = registry.Registry('ziad-hsn/cpra', origin=origin, allow_loopback_http=True)
    backend = LocalKeyCosign(tools/'cosign', remote, root, key)
    env = dict(environment, HOME=str(root), COSIGN_PASSWORD='')
    generated = subprocess.run([str(tools/'cosign'), 'generate-key-pair', '--output-key-prefix', str(key)],
                               env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15)
    if generated.returncode != 0:
        raise ValueError('Fixture key generation failed')
    for row, path in publish.image_objects(assembly, bundles.owner['subjects']):
        if not remote.verify(row):
            remote.upload(tools/'oras', row, path, root)
    bundles.backend = backend
    child = """
import json,os,signal,sys
from pathlib import Path
import image_signing,registry,test_image_signing as test
root,tools=Path(sys.argv[1]),Path(sys.argv[2]);origin,boundary=sys.argv[3:5]
manifest=json.loads((root/'RELEASE.json').read_bytes())
owner=json.loads((root/'progress/image-signing/identity.json').read_bytes()) if (root/'progress/image-signing/identity.json').exists() else None
if owner is None:
 import publish
 subjects=json.loads((root/'assembly/OCI.json').read_bytes())
 descriptors={row['digest']:{'mediaType':row['media_type'],'digest':row['digest'],'size':row['size']} for row,_ in publish.image_objects(root/'assembly',subjects) if row['kind']=='manifest'}
else:
 subjects=owner['subjects'];descriptors={entry['subject']['digest']:entry['subject'] for entry in owner['entries']}
class Interrupted(registry.Registry):
 def upload(self,oras,row,path,work,**kwargs):
  super().upload(oras,row,path,work,**kwargs)
  if ((boundary=='bundle' and row['media_type']==image_signing.BUNDLE_MEDIA) or (boundary=='manifest' and kwargs.get('referrer')) or (boundary=='fallback' and kwargs.get('referrer_subject'))): os.kill(os.getpid(),signal.SIGKILL)
remote=Interrupted('ziad-hsn/cpra',origin=origin,allow_loopback_http=True)
backend=test.LocalKeyCosign(tools/'cosign',remote,root,root/'fixture-key')
bundles=image_signing.Bundles(manifest,subjects,descriptors,root/'progress',backend)
if boundary=='signature':
 original=backend.sign
 def sign(*args):
  original(*args);os.kill(os.getpid(),signal.SIGKILL)
 backend.sign=sign;bundles.signatures()
else:image_signing.publish_bundles(bundles,remote,tools/'oras',root)
"""
    env = dict(environment, PYTHONPATH=str(Path(__file__).parent), PYTHONDONTWRITEBYTECODE='1')
    killed = subprocess.run([sys.executable, '-B', '-c', child, str(root), str(tools), origin, 'signature'],
                            env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90)
    if killed.returncode != -signal.SIGKILL:
        raise ValueError('Real signature crash boundary failed: '+killed.stderr.decode(errors='replace')[-3000:])
    first_path, _ = bundles.paths('signature-amd64'); original = first_path.read_bytes()
    bundles.signatures()
    if first_path.read_bytes() != original:
        raise ValueError('Signing continuation changed the original bundle')
    bundles.prepare_provenance(); proof = root/'fixture-provenance.sigstore.json'
    backend.provenance(bundles, proof); bundles.capture_provenance(proof)
    original_bundles = {role: release.sha(bundles.paths(role)[0]) for role in image_signing.ROLES}
    for boundary in ('bundle', 'manifest', 'fallback'):
        killed = subprocess.run([sys.executable, '-B', '-c', child, str(root), str(tools), origin, boundary],
                                env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120)
        if killed.returncode != -signal.SIGKILL:
            raise ValueError('Real referrer crash boundary failed '+boundary+': '+killed.stderr.decode(errors='replace')[-3000:])
    result = image_signing.publish_bundles(bundles, remote, tools/'oras', root)
    with patch.object(remote, 'upload', wraps=remote.upload) as uploads:
        again = image_signing.publish_bundles(bundles, remote, tools/'oras', root, verify_only=True)
        if uploads.call_count:
            raise ValueError('Release promotion attempted another registry upload')
    if (result['bundle_sha256'] != original_bundles or again['bundle_sha256'] != original_bundles
            or again['uploaded'] != 0 or result['native_referrers'] is not False):
        raise ValueError('Real signing continuation changed or duplicated original material')
    from test_release_publication import asset_integration
    assets = asset_integration(backend, root/'asset-signing', environment)
    return {'status': 'pass', 'checks': ['sigkill_after_signature_output', 'original_bundle_reused',
        'sigkill_after_bundle_accept', 'sigkill_after_referrer_manifest_accept', 'sigkill_after_fallback_index_accept',
        'cosign_verifies_all_three_signatures_and_provenance', 'second_resume_has_zero_uploads',
        'promotion_rechecks_original_bundles_without_upload'],
        'bundle_sha256': original_bundles, 'continuation': result, 'second_continuation': again, 'asset_signing': assets,
        'fixture': 'ephemeral local keys and synthetic source; not GitHub identity, transparency inclusion or production signing',
        'public_objects_created': False, 'release_gate_passed': False}


class GenerationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(); self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.bundles, self.assembly = setup(self.root)

    def complete(self):
        self.bundles.signatures(); self.bundles.prepare_provenance()
        path = self.root/'provenance.json'
        path.write_bytes(bundle(self.bundles.entries['provenance-index'], self.bundles.owner))
        self.bundles.capture_provenance(path)

    def test_live_fixture_checks_cosign_before_generating_any_keys(self):
        tools = self.root/'tools'; tools.mkdir()
        (tools/'cosign').write_bytes(b'not the reviewed executable')
        with patch.object(subprocess, 'run') as command, self.assertRaisesRegex(ValueError, 'reviewed checksum'):
            integration(tools, 'http://127.0.0.1:12345', self.root/'live', {})
        command.assert_not_called()

    def test_ci_explicitly_installs_cosign_and_executes_signing_recovery(self):
        workflow = yaml.safe_load((release.ROOT/'.github/workflows/ci.yml').read_text())
        runs = [step['run'] for step in workflow['jobs']['container-chart-contracts']['steps'] if 'run' in step]
        installer = next(line for run in runs for line in run.splitlines() if 'scripts/release/install_tools.py' in line)
        campaign = next(line for run in runs for line in run.splitlines() if 'scripts/release/test_registry.py' in line)
        self.assertIn('cosign', installer.split())
        self.assertIn('--integration', campaign.split())
        self.assertIn('--image-signatures', campaign.split())

    def test_every_generation_is_intended_before_execution_and_reused_after_sealing(self):
        original = self.bundles.backend.sign
        def observe(entry, path, owner):
            lines = [json.loads(line) for line in (self.root/'progress/image-signing.jsonl').read_bytes().splitlines()]
            self.assertEqual(lines[-1]['kind'], entry['role']); self.assertEqual(lines[-1]['state'], 'started')
            original(entry, path, owner)
        with patch.object(self.bundles.backend, 'sign', side_effect=observe):
            self.bundles.signatures(); self.bundles.signatures()
        self.assertEqual(self.bundles.backend.signed, list(image_signing.ROLES[:3]))
        for path in self.bundles.directory.iterdir():
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_lost_sign_reply_with_original_bundle_is_recovered_without_resigning(self):
        original = self.bundles.backend.sign
        def lost(*args):
            original(*args); raise RuntimeError('fixture lost signing reply')
        with patch.object(self.bundles.backend, 'sign', side_effect=lost), self.assertRaises(RuntimeError):
            self.bundles.signatures()
        self.bundles.signatures()
        self.assertEqual(self.bundles.backend.signed, list(image_signing.ROLES[:3]))

    def test_signing_without_recoverable_bundle_is_held(self):
        with patch.object(self.bundles.backend, 'sign', side_effect=RuntimeError('fixture unknown')), self.assertRaises(RuntimeError):
            self.bundles.signatures()
        with self.assertRaisesRegex(ValueError, 'without recoverable material'):
            self.bundles.signatures()
        self.assertEqual(self.bundles.backend.signed, [])

    def test_provenance_unknown_generation_is_held_and_sealed_result_is_reused(self):
        self.bundles.signatures(); self.assertTrue(self.bundles.prepare_provenance()['generate'])
        with self.assertRaisesRegex(ValueError, 'without recoverable material'):
            self.bundles.prepare_provenance()
        path = self.root/'provenance.json'; path.write_bytes(bundle(self.bundles.entries['provenance-index'], self.bundles.owner))
        self.bundles.capture_provenance(path)
        self.assertFalse(self.bundles.prepare_provenance()['generate']); self.bundles.ready()

    def test_changed_or_missing_sealed_bundles_never_trigger_new_signing(self):
        self.complete(); path, _ = self.bundles.paths('signature-index'); original = path.read_bytes()
        changed = json.loads(original); changed['verificationMaterial']['timestampVerificationData'] = {}
        path.write_bytes(release.encoded(changed))
        with self.assertRaisesRegex(ValueError, 'original bytes'):
            self.bundles.signatures()
        path.write_bytes(original); path.unlink()
        with self.assertRaises((ValueError, FileNotFoundError)):
            self.bundles.signatures()
        self.assertEqual(len(self.bundles.backend.signed), 3)

    def test_wrong_source_subject_predicate_and_provenance_are_rejected_before_crypto(self):
        self.bundles.signatures(); self.bundles.prepare_provenance()
        entry = self.bundles.entries['provenance-index']; original = json.loads(bundle(entry, self.bundles.owner))
        statement = json.loads(base64.b64decode(original['dsseEnvelope']['payload']))
        mutations = (
            lambda s: s['subject'][0]['digest'].update(sha256='f'*64),
            lambda s: s.update(predicateType=image_signing.SIGNATURE_TYPE),
            lambda s: s['predicate']['buildDefinition']['resolvedDependencies'][0]['digest'].update(gitCommit='f'*40),
            lambda s: s['predicate']['buildDefinition']['externalParameters']['workflow'].update(ref='refs/heads/other'),
            lambda s: s['predicate']['buildDefinition']['externalParameters']['workflow'].update(repository='https://github.com/other/repo'),
            lambda s: s['predicate']['buildDefinition']['externalParameters']['workflow'].update(path='untrusted.yml'),
            lambda s: s['subject'][0].update(name='unrelated/image'),
        )
        before = len(self.bundles.backend.verified)
        path = self.root/'wrong.json'
        for change in mutations:
            value, signed = copy.deepcopy(original), copy.deepcopy(statement); change(signed)
            value['dsseEnvelope']['payload'] = base64.b64encode(release.encoded(signed)).decode(); path.write_bytes(release.encoded(value))
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.bundles.capture_provenance(path)
        self.assertEqual(before, len(self.bundles.backend.verified))

    def test_unexpected_files_links_hardlinks_and_size_are_rejected(self):
        self.bundles.signatures(); cache = self.bundles.directory
        path = cache/'credentials.json'; path.write_bytes(b'{}')
        with self.assertRaisesRegex(ValueError, 'unexpected'):
            self.bundles.ready()
        path.unlink(); raw, _ = self.bundles.paths('signature-index'); original = raw.read_bytes(); raw.unlink()
        source = self.root/'linked.json'; source.write_bytes(original); raw.symlink_to(source)
        with self.assertRaisesRegex(ValueError, 'link or alias'):
            self.bundles.signatures()
        raw.unlink(); os.link(source, raw)
        with self.assertRaisesRegex(ValueError, 'aliased'):
            self.bundles.signatures()
        raw.unlink(); raw.write_bytes(b'x'*(image_signing.BUNDLE_LIMIT+1))
        with self.assertRaisesRegex(ValueError, 'oversized'):
            self.bundles.signatures()

    def test_cache_restore_keeps_original_bundles_and_authority(self):
        self.complete(); prior = workflow_run(self.bundles.manifest)
        current = workflow_run(self.bundles.manifest, 43)
        # Artifact download recreates readable directory modes; the restored
        # active cache must still be private.
        self.bundles.directory.chmod(0o755)
        destination = self.root/'restored'
        publish.prepare_progress(self.bundles.manifest, current, destination, incoming=self.root/'progress', previous_run=prior)
        restored = image_signing.Bundles(self.bundles.manifest, self.bundles.owner['subjects'],
            {entry['subject']['digest']: entry['subject'] for entry in self.bundles.owner['entries']}, destination, Backend())
        restored.signatures(); restored.ready(); self.assertEqual(restored.backend.signed, [])
        self.assertEqual({p.name:p.read_bytes() for p in restored.directory.iterdir()},
                         {p.name:p.read_bytes() for p in self.bundles.directory.iterdir()})
        self.assertEqual(restored.directory.stat().st_mode & 0o777, 0o700)

    def test_restore_rejects_a_file_added_after_cache_inspection(self):
        self.complete()
        original = image_signing.inspect_cache
        def changed(*args, **kwargs):
            owner = original(*args, **kwargs)
            (self.bundles.directory/'added.json').write_bytes(b'{}')
            return owner
        destination = self.root/'restored'
        with patch.object(image_signing, 'inspect_cache', side_effect=changed), \
                self.assertRaisesRegex(ValueError, 'changed during restoration'):
            publish.prepare_progress(self.bundles.manifest, workflow_run(self.bundles.manifest, 43), destination,
                                     incoming=self.root/'progress', previous_run=workflow_run(self.bundles.manifest))
        self.assertFalse(destination.exists())

    def test_restore_bounds_a_bundle_that_grows_after_inspection(self):
        self.complete()
        original = image_signing.inspect_cache
        def changed(*args, **kwargs):
            owner = original(*args, **kwargs)
            self.bundles.paths('signature-index')[0].write_bytes(b'x'*(image_signing.BUNDLE_LIMIT+1))
            return owner
        destination = self.root/'restored'
        with patch.object(image_signing, 'inspect_cache', side_effect=changed), \
                self.assertRaisesRegex(ValueError, 'oversized'):
            publish.prepare_progress(self.bundles.manifest, workflow_run(self.bundles.manifest, 43), destination,
                                     incoming=self.root/'progress', previous_run=workflow_run(self.bundles.manifest))
        self.assertFalse(destination.exists())

    def test_wrong_release_and_original_graph_fail_before_generation(self):
        self.complete()
        changed = copy.deepcopy(self.bundles.manifest); changed['version'] = 'v0.2.0'
        with self.assertRaises(ValueError):
            image_signing.inspect_cache(self.bundles.directory, changed, qualification.Inputs())
        self.bundles.owner['source_date'] = '2000-01-01T00:00:00Z'
        with self.assertRaises(ValueError):
            self.bundles.signatures()

    def test_two_signing_owners_are_excluded(self):
        with self.bundles.session():
            with self.assertRaisesRegex(ValueError, 'locked by another owner'):
                self.bundles.signatures()

    def test_old_asset_signing_progress_does_not_authorize_fresh_image_signing(self):
        (self.root/'progress/signed').mkdir(mode=0o700)
        with self.assertRaisesRegex(ValueError, 'older signed progress'):
            self.bundles.signatures()
        self.assertEqual(self.bundles.backend.signed, [])

    def test_unstarted_or_out_of_order_generation_receipts_are_rejected(self):
        self.complete()
        path = self.root/'progress/image-signing.jsonl'
        rows = path.read_bytes().splitlines(keepends=True)
        path.write_bytes(b''.join([rows[0], *rows[2:]]))
        with self.assertRaisesRegex(ValueError, 'out of order'):
            self.bundles.signatures()
        self.assertEqual(len(self.bundles.backend.signed), 3)

    def test_duplicate_fields_and_wrong_inventory_shape_are_explicit_errors(self):
        self.bundles.signatures(); path = self.bundles.directory/'identity.json'; data = path.read_bytes()
        values = [b'{"schema_version":1,"schema_version":1}',
                  release.encoded(dict(self.bundles.owner, entries=[{}, {}, {}, {}])),
                  release.encoded(dict(self.bundles.owner, entries=[dict(self.bundles.owner['entries'][0], subject={'digest': []})]*4))]
        for value in values:
            path.write_bytes(value)
            with self.subTest(value=value[:100]), self.assertRaises(ValueError):
                image_signing.inspect_cache(self.bundles.directory, self.bundles.manifest, qualification.Inputs())
        path.write_bytes(data)

    def test_real_process_death_after_output_or_seal_never_reinvokes_same_signature(self):
        for boundary in ('output', 'seal'):
            with self.subTest(boundary=boundary), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                child = """
import os,signal,sys
from pathlib import Path
import publish,test_image_signing as test
root=Path(sys.argv[1]); boundary=sys.argv[2]
bundles,_=test.setup(root)
original=bundles.backend.sign
def sign(entry,path,owner):
 original(entry,path,owner)
 if boundary=='output': os.kill(os.getpid(),signal.SIGKILL)
bundles.backend.sign=sign
record=publish.TransferProgress.record
def receipt(self,row,state,**kwargs):
 if boundary=='seal' and state=='verified': os.kill(os.getpid(),signal.SIGKILL)
 return record(self,row,state,**kwargs)
publish.TransferProgress.record=receipt
bundles.signatures()
"""
                env = dict(os.environ, PYTHONPATH=str(Path(__file__).parent), PYTHONDONTWRITEBYTECODE='1')
                result = subprocess.run([sys.executable, '-B', '-c', child, str(root), boundary], env=env,
                                        capture_output=True, timeout=10)
                self.assertEqual(result.returncode, -signal.SIGKILL, result.stderr)
                owner = json.loads((root/'progress/image-signing/identity.json').read_bytes())
                manifest = json.loads((root/'RELEASE.json').read_bytes())
                backend = Backend()
                restored = image_signing.Bundles(manifest, owner['subjects'],
                    {entry['subject']['digest']:entry['subject'] for entry in owner['entries']}, root/'progress', backend)
                restored.signatures(); self.assertEqual(backend.signed, list(image_signing.ROLES[1:3]))


class PublicationTests(unittest.TestCase):
    def setUp(self):
        GenerationTests.setUp(self); GenerationTests.complete(self)

    def remote(self, native):
        server = Server(native); self.addCleanup(server.close)
        return Remote(server)

    def run_publication(self, remote):
        return image_signing.publish_bundles(self.bundles, remote, '/unused/oras', self.root)

    def test_native_and_fallback_publish_identical_original_graph_and_repeat_only_reads(self):
        for native in (True, False):
            with self.subTest(native=native):
                remote = self.remote(native)
                journal = self.root/'progress/image-referrers.jsonl'; journal.unlink(missing_ok=True)
                first = self.run_publication(remote); count = len(remote.uploads)
                before = {key:value[1] for key,value in remote.server.objects.items()}
                again = self.run_publication(remote)
                self.assertEqual(first['native_referrers'], native); self.assertFalse(first['release_gate_passed'])
                self.assertEqual(again['uploaded'], 0); self.assertEqual(len(remote.uploads), count)
                self.assertEqual(before, {key:value[1] for key,value in remote.server.objects.items()})
                self.assertEqual(first['bundle_sha256'], again['bundle_sha256'])
                self.assertEqual(len(self.bundles.backend.signed), 3)

    def test_accepted_blob_with_lost_reply_is_reused(self):
        remote = self.remote(True); remote.fail_after = True
        with self.assertRaisesRegex(RuntimeError, 'lost registry reply'):
            self.run_publication(remote)
        digest = remote.uploads[0][0]
        self.run_publication(remote)
        self.assertEqual(sum(row[0] == digest for row in remote.uploads), 1)

    def test_accepted_fallback_index_with_lost_reply_is_reused(self):
        remote = self.remote(False); original = remote.upload
        def lost(*args, **kwargs):
            if kwargs.get('referrer_subject'):
                remote.fail_after = True
            return original(*args, **kwargs)
        with patch.object(remote, 'upload', side_effect=lost), self.assertRaisesRegex(RuntimeError, 'lost registry reply'):
            self.run_publication(remote)
        first = remote.uploads[-1]
        self.run_publication(remote)
        self.assertEqual(remote.uploads.count(first), 1)

    def test_missing_completed_graph_or_index_is_not_replaced(self):
        for native in (True, False):
            remote = self.remote(native); (self.root/'progress/image-referrers.jsonl').unlink(missing_ok=True)
            self.run_publication(remote); count = len(remote.uploads)
            key = next(key for key in remote.server.objects if key[0] == 'blob')
            saved = remote.server.objects.pop(key)
            with self.assertRaisesRegex(ValueError, 'missing; replacement'):
                self.run_publication(remote)
            remote.server.objects[key] = saved
            if not native:
                remote.server.objects.pop(('manifest', self.bundles.owner['subjects']['digest'].replace(':','-')))
                with self.assertRaisesRegex(ValueError, 'missing; replacement'):
                    self.run_publication(remote)
            self.assertEqual(len(remote.uploads), count)

    def test_unrelated_fallback_index_is_held_before_any_upload(self):
        remote = self.remote(False)
        with tempfile.TemporaryDirectory(dir=self.root) as temporary:
            _, indexes, _ = image_signing.objects(self.bundles, Path(temporary), qualification.Inputs())
            subject, (row,path) = next(iter(indexes.items()))
            remote.server.objects[('manifest', subject.replace(':','-'))] = (row,path.read_bytes())
        with self.assertRaisesRegex(ValueError, 'without original intent'):
            self.run_publication(remote)
        self.assertEqual(remote.uploads, [])

    def test_native_discovery_must_include_each_original_bundle(self):
        remote = self.remote(True); original = remote.referrers
        def missing(subject):
            return [row for row in original(subject) if row.get('annotations',{}).get('dev.sigstore.bundle.predicateType')
                    != image_signing.PROVENANCE_TYPE]
        with patch.object(remote, 'referrers', side_effect=missing), self.assertRaisesRegex(ValueError, 'missing or duplicated'):
            self.run_publication(remote)

    def test_promotion_requires_completed_progress_and_is_read_only(self):
        remote = self.remote(False)
        with self.assertRaisesRegex(ValueError, 'completed original'):
            image_signing.publish_bundles(self.bundles, remote, '/unused/oras', self.root, verify_only=True)
        self.assertEqual(remote.uploads, [])
        self.run_publication(remote); count = len(remote.uploads)
        result = image_signing.publish_bundles(self.bundles, remote, '/unused/oras', self.root, verify_only=True)
        self.assertEqual(result['uploaded'], 0); self.assertEqual(len(remote.uploads), count)
        key = next(key for key in remote.server.objects if key[0] == 'blob'); remote.server.objects.pop(key)
        with self.assertRaisesRegex(ValueError, 'missing; replacement'):
            image_signing.publish_bundles(self.bundles, remote, '/unused/oras', self.root, verify_only=True)
        self.assertEqual(len(remote.uploads), count)

    def test_discovery_can_omit_optional_annotations_without_weakening_digest_checks(self):
        remote = self.remote(True); original = remote.referrers
        def optional(subject):
            return [{key:value for key,value in row.items() if key != 'annotations'} for row in original(subject)]
        with patch.object(remote, 'referrers', side_effect=optional):
            self.run_publication(remote)

    def test_referrer_pagination_stays_on_original_origin_and_has_bounds(self):
        remote = self.remote(True); subject = self.bundles.owner['subjects']['digest']
        path = '/v2/'+remote.repository+'/referrers/'+subject
        remote.server.links[path] = '<'+path+'?last=next&n=1>; rel="next"'
        self.assertEqual(remote.referrers(subject), [])
        for link in ('<https://evil.invalid'+path+'?last=next>; rel="next"',
                     '<'+path+'?last=next&last=other>; rel="next"',
                     '<'+path+'?n=0>; rel="next"', '<'+path+'?access_token=secret>; rel="next"',
                     '<'+path+'>; rel="next"'):
            remote.server.links[path] = link
            with self.subTest(link=link), self.assertRaises(ValueError):
                remote.referrers(subject)
        remote.server.links[path] = '<'+path+'?last=next>; rel="next"'
        remote.server.links[path+'?last=next'] = '<'+path+'?last=next>; rel="next"'
        with self.assertRaisesRegex(ValueError, 'repeats'):
            remote.referrers(subject)


if __name__ == '__main__':
    unittest.main()
