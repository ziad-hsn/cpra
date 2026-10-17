"""Preserve original image bundles before publishing their OCI referrers.

The CLI remains locked. Local fixture keys do not qualify GitHub identity,
transparency-log inclusion, or account-backed publication.
"""
import base64
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import tempfile
import time

from evidence import decode_report
import module_publication
import oci
import publish
import qualification
import registry
import release
import release_publication

BUNDLE_MEDIA = 'application/vnd.dev.sigstore.bundle.v0.3+json'
SIGNATURE_TYPE = 'https://sigstore.dev/cosign/sign/v1'
PROVENANCE_TYPE = 'https://slsa.dev/provenance/v1'
BUNDLE_LIMIT = 4 * 1024**2
CACHE_NAME = 'image-signing'
GENERATION_JOURNAL = 'image-signing.jsonl'
TRANSFER_JOURNAL = 'image-referrers.jsonl'
ROLES = ('signature-amd64', 'signature-arm64', 'signature-index', 'provenance-index')


def require(condition, detail):
    if not condition:
        raise ValueError('Image signing stopped: ' + detail)


def bounded_file(path, limit, inputs=None):
    path = Path(path).absolute()
    require(path.resolve(strict=True) == path, 'file contains a link or alias')
    mode = path.stat()
    require(stat.S_ISREG(mode.st_mode) and mode.st_nlink == 1
            and 0 < mode.st_size <= limit, 'file is nonregular, aliased, empty or oversized')
    if inputs is not None:
        inputs.remember(path)
    with path.open('rb') as stream:
        data = stream.read(limit + 1)
    require(len(data) == mode.st_size, 'file changed while reading')
    return data


def json_value(data):
    try:
        return decode_report(data, 'image signing')
    except RecursionError:
        raise ValueError('Image signing JSON is too deeply nested') from None


def identity_digest(owner):
    return hashlib.sha256(release.encoded(owner['release_identity'])).hexdigest()


def binding(manifest, subjects, descriptors):
    identity = release.evidence_identity(manifest)
    require(isinstance(subjects, dict), 'image subjects must be an object')
    require(subjects == {'image': 'ghcr.io/ziad-hsn/cpra', 'digest': subjects.get('digest'),
                        'platforms': subjects.get('platforms'), 'version': manifest['version'],
                        'commit': manifest['commit']}
            and isinstance(subjects['platforms'], dict)
            and set(subjects['platforms']) == {'amd64', 'arm64'}, 'image subjects differ from the release')
    entries = []
    for role in ROLES:
        digest = subjects['platforms'][role[10:]] if role in ROLES[:2] else subjects['digest']
        require(isinstance(digest, str) and re.fullmatch(r'sha256:[a-f0-9]{64}', digest), 'invalid subject digest')
        descriptor = descriptors.get(digest)
        require(isinstance(descriptor, dict) and set(descriptor) == {'mediaType', 'digest', 'size'}
                and descriptor['digest'] == digest and type(descriptor['size']) is int
                and 0 < descriptor['size'] <= 16 * 1024**2
                and descriptor['mediaType'] == (oci.MANIFEST if role in ROLES[:2] else oci.INDEX),
                'subject descriptor differs from its original image')
        entries.append({'role': role, 'subject': descriptor,
                        'predicate_type': PROVENANCE_TYPE if role == ROLES[-1] else SIGNATURE_TYPE})
    return {'schema_version': 1, 'release_identity': identity, 'subjects': subjects,
            'source_date': manifest['source_date'], 'entries': entries,
            'certificate_identity': publish.IDENTITY, 'oidc_issuer': publish.ISSUER}


def bundle_statement(data, entry, owner):
    bundle = json_value(data)
    require(set(bundle) == {'mediaType', 'verificationMaterial', 'dsseEnvelope'}
            and bundle['mediaType'] == BUNDLE_MEDIA and isinstance(bundle['verificationMaterial'], dict)
            and set(bundle['verificationMaterial']) <= {'certificate', 'publicKey', 'x509CertificateChain',
                                                       'tlogEntries', 'timestampVerificationData'},
            'expected one Sigstore v0.3 DSSE bundle')
    envelope = bundle['dsseEnvelope']
    require(isinstance(envelope, dict) and set(envelope) == {'payload', 'payloadType', 'signatures'}
            and envelope['payloadType'] == 'application/vnd.in-toto+json'
            and isinstance(envelope['signatures'], list) and len(envelope['signatures']) == 1,
            'invalid DSSE envelope')
    try:
        payload = base64.b64decode(envelope['payload'], validate=True)
        signature = base64.b64decode(envelope['signatures'][0]['sig'], validate=True)
    except (ValueError, TypeError, KeyError):
        raise ValueError('Image signing bundle contains invalid base64') from None
    require(0 < len(payload) <= BUNDLE_LIMIT and 0 < len(signature) <= 4096, 'invalid payload or signature size')
    statement = json_value(payload)
    require(statement.get('_type') == 'https://in-toto.io/Statement/v1'
            and statement.get('predicateType') == entry['predicate_type']
            and isinstance(statement.get('subject'), list) and len(statement['subject']) == 1
            and isinstance(statement['subject'][0], dict)
            and statement['subject'][0].get('digest') == {'sha256': entry['subject']['digest'][7:]},
            'signed predicate or subject differs from the original operation')
    if entry['predicate_type'] == SIGNATURE_TYPE:
        require(statement['subject'][0].get('annotations') == {
                    'cpra.io/source-commit': owner['subjects']['commit'],
                    'cpra.io/release-identity': identity_digest(owner)},
                'signature does not bind the frozen source and release identity')
    else:
        predicate = statement.get('predicate')
        definition = predicate.get('buildDefinition') if isinstance(predicate, dict) else None
        require(isinstance(definition, dict)
                and definition.get('buildType') == 'https://actions.github.io/buildtypes/workflow/v1'
                and definition.get('externalParameters') == {'workflow': {
                    'ref': 'refs/heads/main', 'repository': 'https://github.com/'+publish.REPOSITORY,
                    'path': '.github/workflows/release.yml'}}
                and definition.get('resolvedDependencies') == [{
                    'uri': 'git+https://github.com/'+publish.REPOSITORY+'@refs/heads/main',
                    'digest': {'gitCommit': owner['subjects']['commit']}}]
                and statement['subject'][0].get('name') == owner['subjects']['image'],
                'provenance does not bind the approved repository, workflow, ref and commit')
    return statement


def inspect_cache(directory, manifest, inputs):
    """Bound imported public material; this is not cryptographic verification."""
    # GitHub artifact extraction does not preserve private directory modes.
    # Imported material is public; active session storage is private below.
    directory = qualification.regular(Path(directory).absolute(), directory=True)
    owner = json_value(bounded_file(directory/'identity.json', 64 * 1024, inputs))
    require(isinstance(owner.get('entries'), list) and len(owner['entries']) == len(ROLES), 'invalid signing inventory')
    for role, entry in zip(ROLES, owner['entries']):
        require(isinstance(entry, dict) and set(entry) == {'role', 'subject', 'predicate_type'}
                and entry['role'] == role and isinstance(entry['subject'], dict)
                and isinstance(entry['subject'].get('digest'), str), 'invalid signing role or subject')
    descriptors = {entry['subject']['digest']: entry['subject'] for entry in owner['entries']
                   if isinstance(entry, dict) and isinstance(entry.get('subject'), dict)}
    require(owner == binding(manifest, owner.get('subjects', {}), descriptors), 'signing cache belongs to different inputs')
    allowed = {'identity.json'} | {role+suffix for role in ROLES for suffix in ('.sigstore.json', '.record.json')}
    require({p.name for p in directory.iterdir()} <= allowed, 'cache contains unexpected files')
    for entry in owner['entries']:
        path, record = directory/(entry['role']+'.sigstore.json'), directory/(entry['role']+'.record.json')
        if path.exists() or path.is_symlink():
            bundle_statement(bounded_file(path, BUNDLE_LIMIT, inputs), entry, owner)
        if record.exists() or record.is_symlink():
            require(path.is_file() and not path.is_symlink(), 'sealed bundle is missing its original material')
            value = json_value(bounded_file(record, 4096, inputs))
            require(value == {'role': entry['role'], 'sha256': release.sha(path), 'size': path.stat().st_size},
                    'sealed bundle differs from its original bytes')
    inputs.unchanged()
    return owner


def save_original(path, data):
    """A completed rename is durable before its journal receipt is committed."""
    if path.exists() or path.is_symlink():
        require(bounded_file(path, max(len(data), 1)) == data, 'original material cannot be replaced')
        return
    with tempfile.TemporaryDirectory(prefix='image-bundle-', dir=path.parent.parent.parent) as temporary:
        staged = Path(temporary)/'material'
        with staged.open('xb') as writer:
            os.chmod(staged, 0o600)
            writer.write(data); writer.flush(); os.fsync(writer.fileno())
        require(not path.exists() and not path.is_symlink(), 'original material appeared concurrently')
        os.rename(staged, path); release_publication.sync_directory(path.parent)


class Bundles:
    """One locked source-bound generation session; uncertain signing is held."""
    def __init__(self, manifest, subjects, descriptors, progress, backend):
        self.progress = release_publication.signing_owner(progress, manifest)
        self.manifest, self.owner = manifest, binding(manifest, subjects, descriptors)
        self.directory, self.backend = self.progress/CACHE_NAME, backend
        self.entries = {entry['role']: entry for entry in self.owner['entries']}
        self.rows = [module_publication.marker(entry['role'], {'owner': self.owner, 'entry': entry})
                     for entry in self.owner['entries']]

    @contextmanager
    def session(self):
        require(not (self.progress/'signed').exists() or (self.progress/GENERATION_JOURNAL).exists(),
                'older signed progress lacks original image-generation records; inspect the original run')
        with publish.TransferProgress(self.progress/GENERATION_JOURNAL, self.owner, self.rows) as journal:
            expected = [{'kind': row['kind'], 'digest': row['digest'], 'state': state}
                        for row in self.rows for state in ('started', 'verified')]
            require(journal.records == expected[:len(journal.records)], 'generation progress is duplicated or out of order')
            if not self.directory.exists() and not self.directory.is_symlink():
                self.directory.mkdir(mode=0o700)
                save_original(self.directory/'identity.json', release.encoded(self.owner))
                release_publication.sync_directory(self.progress)
            module_publication.private_directory(self.directory)
            require(inspect_cache(self.directory, self.manifest, qualification.Inputs()) == self.owner,
                    'signing session belongs to another image graph')
            yield journal

    def paths(self, role):
        return self.directory/(role+'.sigstore.json'), self.directory/(role+'.record.json')

    def state(self, journal, role):
        row = self.rows[ROLES.index(role)]
        return row, journal.states.get((row['kind'], row['digest']))

    def finish(self, journal, role, source=None):
        row, state = self.state(journal, role)
        require(state is not None, 'bundle has no committed generation intent')
        raw, record = self.paths(role)
        inputs = qualification.Inputs()
        source = raw if source is None else Path(source).absolute()
        data = bounded_file(source, BUNDLE_LIMIT, inputs)
        bundle_statement(data, self.entries[role], self.owner)
        if state == 'verified':
            require(record.is_file() and bounded_file(raw, BUNDLE_LIMIT, inputs) == data,
                    'completed generation is missing its original material')
        self.backend.verify_bundle(self.entries[role], source)
        inputs.unchanged()
        save_original(raw, data)
        # cosign writes its output directly here. Flush it before sealing even
        # when no copy was needed; a successful subprocess is not an fsync.
        with raw.open('rb') as stream:
            os.fsync(stream.fileno())
        release_publication.sync_directory(self.directory)
        save_original(record, release.encoded({'role': role, 'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)}))
        if state != 'verified':
            journal.record(row, 'verified')

    def signatures(self):
        with self.session() as journal:
            for role in ROLES[:3]:
                row, state = self.state(journal, role)
                raw, record = self.paths(role)
                if state is None:
                    require(not raw.exists() and not record.exists(), 'bundle exists without original intent')
                    journal.reserve(row); journal.record(row, 'started')
                    self.backend.sign(self.entries[role], raw, self.owner)
                else:
                    require(raw.exists(), 'signing began without recoverable material; inspect the original run')
                self.finish(journal, role)
        return {'signatures': 3, 'release_gate_passed': False}

    def prepare_provenance(self):
        with self.session() as journal:
            require(all(self.state(journal, role)[1] == 'verified' for role in ROLES[:3]), 'signatures must be sealed first')
            role = ROLES[-1]; row, state = self.state(journal, role); raw, record = self.paths(role)
            if state is not None:
                require(raw.exists(), 'attestation began without recoverable material; inspect the original run')
                self.finish(journal, role)
                return {'generate': False, 'release_gate_passed': False}
            require(not raw.exists() and not record.exists(), 'attestation exists without original intent')
            journal.reserve(row); journal.record(row, 'started')
            return {'generate': True, 'release_gate_passed': False}

    def capture_provenance(self, source):
        with self.session() as journal:
            self.finish(journal, ROLES[-1], source)

    def ready(self):
        with self.session() as journal:
            require(all(self.state(journal, role)[1] == 'verified' for role in ROLES), 'original signing is incomplete')
            for role in ROLES:
                self.finish(journal, role)


class Cosign:
    """Production keyless signing; no key or trust-policy override is exposed."""
    def __init__(self, executable, remote, work):
        self.executable, self.remote, self.work = Path(executable).absolute(), remote, module_publication.private_directory(work)
        self.executable_sha256 = json_value(bounded_file(Path(__file__).with_name('tools.json'), 64 * 1024))['cosign']['sha256']
        require(self.executable.resolve(strict=True) == self.executable and self.executable.is_file()
                and release.sha(self.executable) == self.executable_sha256,
                'cosign executable differs from its reviewed checksum')

    def environment(self, directory):
        environment = {key: value for key, value in os.environ.items() if key in (
            'ACTIONS_ID_TOKEN_REQUEST_URL', 'ACTIONS_ID_TOKEN_REQUEST_TOKEN')}
        environment.update(PATH='/usr/bin:/bin', HOME=str(directory), TMPDIR=str(directory),
                           XDG_CACHE_HOME=str(directory), XDG_CONFIG_HOME=str(directory), DOCKER_CONFIG=str(directory),
                           LANG='C.UTF-8', TZ='UTC')
        return environment

    def command(self, arguments):
        require(self.executable.resolve(strict=True) == self.executable
                and release.sha(self.executable) == self.executable_sha256,
                'cosign executable changed after selection')
        with tempfile.TemporaryDirectory(prefix='cosign-', dir=self.work) as temporary:
            directory = Path(temporary)
            environment = self.environment(directory)
            auths = {} if self.remote.fixture else {self.remote.host: {'auth': base64.b64encode(
                (self.remote.username+':'+self.remote.token).encode()).decode()}}
            with (directory/'config.json').open('x') as writer:
                os.chmod(directory/'config.json', 0o600)
                json.dump({'auths': auths}, writer)
            with (directory/'output.log').open('wb') as log:
                child = subprocess.Popen([str(self.executable), *arguments, '--timeout=60s'], cwd=directory,
                                         env=environment, stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
                deadline = time.monotonic()+75
                try:
                    while child.poll() is None:
                        require(time.monotonic() < deadline and log.tell() <= registry.PROCESS_LOG_LIMIT
                                and sum(p.stat().st_size for p in self.work.rglob('*') if p.is_file()) <= registry.WORK_LIMIT,
                                'cosign exceeded its deadline, output or storage quota')
                        time.sleep(0.05)
                    require(child.returncode == 0 and log.tell() <= registry.PROCESS_LOG_LIMIT,
                            'cosign '+arguments[0]+' could not be verified (exit '+str(child.returncode)
                            +'); inspect the original signing intent')
                finally:
                    try:
                        os.killpg(child.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    if child.poll() is None:
                        child.wait(timeout=5)

    def trust(self):
        return ['--certificate-identity', publish.IDENTITY, '--certificate-oidc-issuer', publish.ISSUER]

    def reference(self, entry):
        return self.remote.host+'/'+self.remote.repository+'@'+entry['subject']['digest']

    def sign(self, entry, path, owner):
        self.command(['sign', '--yes', '--upload=false', '--bundle', str(path),
                      '-a', 'cpra.io/source-commit='+owner['subjects']['commit'],
                      '-a', 'cpra.io/release-identity='+identity_digest(owner), self.reference(entry)])

    def verify_bundle(self, entry, path):
        self.command(['verify-blob-attestation', '--bundle', str(path), '--digestAlg', 'sha256',
                      '--digest', entry['subject']['digest'][7:], '--type', entry['predicate_type'], *self.trust()])

    def verify_remote(self, entry):
        operation = 'verify-attestation' if entry['predicate_type'] == PROVENANCE_TYPE else 'verify'
        self.command([operation, *(['--type', PROVENANCE_TYPE] if operation == 'verify-attestation' else []),
                      *self.trust(), self.reference(entry)])


def objects(bundles, directory, inputs):
    """Freeze bundle bytes and deterministic OCI envelopes, independent of retries."""
    result, envelopes = {}, {}
    def store(kind, media_type, data):
        row = {'kind': kind, 'digest': 'sha256:'+hashlib.sha256(data).hexdigest(), 'size': len(data), 'media_type': media_type}
        registry.validate_object(row)
        path = directory/row['digest'][7:]
        if (kind, row['digest']) not in result:
            path.write_bytes(data); inputs.remember(path)
            result[(kind, row['digest'])] = (row, path)
        return {'mediaType': media_type, 'digest': row['digest'], 'size': row['size']}
    config = store('blob', 'application/vnd.oci.empty.v1+json', b'{}')
    for entry in bundles.owner['entries']:
        path, _ = bundles.paths(entry['role'])
        layer = store('blob', BUNDLE_MEDIA, bounded_file(path, BUNDLE_LIMIT, inputs))
        annotations = {'org.opencontainers.image.created': bundles.owner['source_date'],
                       'dev.sigstore.bundle.content': 'dsse-envelope',
                       'dev.sigstore.bundle.predicateType': entry['predicate_type']}
        descriptor = store('manifest', oci.MANIFEST, release.encoded({
            'schemaVersion': 2, 'mediaType': oci.MANIFEST, 'artifactType': BUNDLE_MEDIA,
            'config': config, 'layers': [layer], 'subject': entry['subject'], 'annotations': annotations}))
        envelopes[entry['role']] = dict(descriptor, artifactType=BUNDLE_MEDIA, annotations=annotations)
    indexes = {}
    for entry in bundles.owner['entries'][:3]:
        selected = [envelopes[e['role']] for e in bundles.owner['entries'] if e['subject'] == entry['subject']]
        index = store('manifest', oci.INDEX, release.encoded({'schemaVersion': 2, 'mediaType': oci.INDEX, 'manifests': selected}))
        indexes[entry['subject']['digest']] = result.pop(('manifest', index['digest']))
    inputs.unchanged()
    return list(result.values()), indexes, envelopes


def publish_bundles(bundles, remote, oras, work, *, verify_only=False):
    """Idempotent original objects plus verified native/fallback discovery."""
    work = module_publication.private_directory(work)
    require(type(verify_only) is bool, 'invalid verification mode')
    bundles.ready()
    inputs = qualification.Inputs()
    inspect_cache(bundles.directory, bundles.manifest, inputs)
    with tempfile.TemporaryDirectory(prefix='image-referrers-', dir=work) as temporary:
        graph, indexes, envelopes = objects(bundles, Path(temporary), inputs)
        discovery = {subject: remote.referrers(subject) for subject in indexes}
        require(all(value is None for value in discovery.values()) or all(value is not None for value in discovery.values()),
                'registry referrer support is inconsistent across original subjects')
        native = next(iter(discovery.values())) is not None
        rows = [row for row, _ in graph]
        tags = [module_publication.marker('referrer-index', {'subject': subject, 'index': row})
                for subject, (row, _) in indexes.items()] if not native else []
        owner = {'release_identity': bundles.owner['release_identity'], 'origin': remote.origin,
                 'repository': remote.repository, 'signing': bundles.owner, 'objects': rows,
                 'indexes': {subject: row for subject, (row, _) in indexes.items()}, 'native_referrers': native}
        uploaded, reused = 0, 0
        with publish.TransferProgress(bundles.progress/TRANSFER_JOURNAL, owner, rows+tags) as journal:
            from chart import progress_order
            progress_order(journal, rows+tags)
            if verify_only:
                require(all(journal.states.get((row['kind'], row['digest'])) == 'verified' for row in rows+tags),
                        'release promotion requires completed original image-referrer progress')
            for entry, (subject, (row, _)) in zip(tags, indexes.items()):
                state = journal.states.get((entry['kind'], entry['digest']))
                require(state != 'verified' or any(record == {'kind': entry['kind'], 'digest': entry['digest'], 'state': 'started'}
                                                 for record in journal.records), 'fallback index has no original started intent')
                present = remote.verify(row, referrer_subject=subject)
                require(not present or state is not None, 'fallback index exists without original intent')
                require(present or state != 'verified', 'completed fallback index is missing; replacement is forbidden')
                inputs.unchanged()
            for row, path in graph:
                state = journal.states.get((row['kind'], row['digest']))
                present = remote.verify(row); inputs.unchanged()
                require(present or state != 'verified', 'completed referrer object is missing; replacement is forbidden')
                if state != 'verified':
                    journal.reserve(row)
                if not present:
                    if state is None:
                        journal.record(row, 'started')
                    remote.upload(oras, row, path, work, referrer=row['kind'] == 'manifest')
                    inputs.unchanged(); require(remote.verify(row), 'referrer upload has no verified readback')
                    uploaded += 1
                else:
                    reused += 1
                if state != 'verified':
                    journal.record(row, 'verified')
            for entry, (subject, (row, path)) in zip(tags, indexes.items()):
                state = journal.states.get((entry['kind'], entry['digest']))
                present = remote.verify(row, referrer_subject=subject); inputs.unchanged()
                require(not present or state is not None, 'fallback index exists without original intent')
                require(present or state != 'verified', 'completed fallback index is missing; replacement is forbidden')
                if state != 'verified':
                    journal.reserve(entry)
                if not present:
                    if state is None:
                        journal.record(entry, 'started')
                    remote.upload(oras, row, path, work, referrer_subject=subject)
                    inputs.unchanged(); require(remote.verify(row, referrer_subject=subject), 'fallback index has no verified readback')
                    uploaded += 1
                else:
                    reused += 1
                if state != 'verified':
                    journal.record(entry, 'verified')
            for row, _ in graph:
                require(remote.verify(row), 'complete original referrer graph is required')
                inputs.unchanged()
            for subject, (row, _) in indexes.items():
                if native:
                    found = remote.referrers(subject)
                    require(found is not None, 'native referrer discovery disappeared')
                    for entry in bundles.owner['entries']:
                        if entry['subject']['digest'] == subject:
                            descriptor = envelopes[entry['role']]
                            require(sum(all(item.get(key) == descriptor[key] for key in
                                            ('mediaType', 'digest', 'size', 'artifactType')) for item in found) == 1,
                                    'original bundle is missing or duplicated in referrer discovery')
                else:
                    require(remote.verify(row, referrer_subject=subject), 'fallback discovery changed during final readback')
            for entry in bundles.owner['entries']:
                bundles.backend.verify_remote(entry)
            inputs.unchanged()
        return {'status': 'verified', 'objects_verified': len(rows), 'uploaded': uploaded, 'reused': reused,
                'native_referrers': native, 'bundle_sha256': {role: release.sha(bundles.paths(role)[0]) for role in ROLES},
                'release_gate_passed': False, 'scope': 'original bundles, referrer graph and discovery; not independent release approval'}
