"""Resume source-bound GitHub release operations without overwriting assets.

The executable publisher remains locked. Call this library only after the full
private qualification gate; fixtures do not establish GitHub or signing proof.
"""
import base64
from contextlib import contextmanager
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import shutil
import stat
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

from evidence import decode_json, decode_report
import module_publication
import publication_evidence
import publish
import qualification
import registry
import release

JSON_LIMIT = 4 * 1024**2
ASSET_LIMIT = 1024**3
FILE_LIMIT = 1000
PAGE_LIMIT = 100
WORK_LIMIT = 1536 * 1024**2
SIGNED_FILES = ('SHA256SUMS', 'SHA256SUMS.sigstore.json', 'provenance.sigstore.json', 'trusted_root.jsonl')
SIGNING_LIMIT = 64 * 1024**2
SIGNING_LEDGER = 'signing.json'
ASSET_SIGNING_CACHE = 'asset-signing'
ASSET_SIGNING_JOURNAL = 'asset-signing.jsonl'
ASSET_SIGNING_ROLES = ('provenance.sigstore.json', 'SHA256SUMS.sigstore.json')


def signing_bytes(path, limit=SIGNING_LIMIT, expected=None):
    path = Path(path).absolute()
    require(path.resolve(strict=True) == path, 'signing input contains a link or alias')
    mode = path.stat()
    require(stat.S_ISREG(mode.st_mode) and mode.st_nlink == 1 and 0 < mode.st_size <= limit,
            'signing input is nonregular, aliased, empty or exceeds its quota')
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    with os.fdopen(descriptor, 'rb') as stream:
        opened = os.fstat(stream.fileno())
        require((opened.st_dev, opened.st_ino, opened.st_size, opened.st_nlink) ==
                (mode.st_dev, mode.st_ino, mode.st_size, 1), 'signing input changed before reading')
        data = stream.read(limit + 1)
    require(len(data) == mode.st_size and len(data) <= limit, 'signing input changed while reading')
    require(expected is None or hashlib.sha256(data).hexdigest() == expected,
            'signing input differs from its inspected bytes')
    return data


def save_signing_file(path, data, *, replace=False):
    """Flush original bytes before publishing their completion record."""
    if (path.exists() or path.is_symlink()) and not replace:
        require(signing_bytes(path, max(len(data), 1)) == data, 'original signing material cannot be replaced')
        return
    with tempfile.TemporaryDirectory(prefix='asset-material-', dir=path.parent.parent) as temporary:
        staged = Path(temporary)/'material'
        with staged.open('xb') as writer:
            os.chmod(staged, 0o600)
            writer.write(data); writer.flush(); os.fsync(writer.fileno())
        require(replace or not path.exists() and not path.is_symlink(), 'original signing material appeared concurrently')
        os.replace(staged, path) if replace else os.rename(staged, path)
        sync_directory(path.parent)


def require(condition, detail):
    if not condition:
        raise ValueError('Release publication stopped: ' + detail)


def identifier(value):
    require(type(value) is int and 0 < value < 2**64, 'invalid remote object identity')
    return str(value)


def file_name(value):
    require(isinstance(value, str) and 0 < len(value) <= 200
            and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', value), 'invalid asset filename')
    return value


class Releases(module_publication.GitHub):
    """Canonical API/upload origins; byte readback never forwards credentials."""
    def headers(self, accept='application/vnd.github+json'):
        result = {'Accept': accept, 'X-GitHub-Api-Version': '2022-11-28',
                  'User-Agent': 'CPRa-release-publisher'}
        if self.token is not None:
            result['Authorization'] = 'Bearer ' + self.token
        return result

    def open_release(self, method, path, *, data=None, expected=200, binary=False, upload=False):
        require(self.commit is not None, 'repository access must be established first')
        base = '/repos/' + publish.REPOSITORY + '/releases'
        patterns = {
            'GET': (r'(?:\?per_page=(?:10|100)&page=[1-9][0-9]{0,2}|/[1-9][0-9]{0,19}'
                    r'(?:/assets\?per_page=100&page=[1-9][0-9]{0,2})?|/assets/[1-9][0-9]{0,19})'),
            'POST': r'(?:|/[1-9][0-9]{0,19}/assets\?name=[A-Za-z0-9._-]+&label=[A-Za-z0-9._-]+)',
            'PATCH': r'/[1-9][0-9]{0,19}', 'DELETE': r'/[1-9][0-9]{0,19}',
        }
        require(method in patterns and path.startswith(base)
                and re.fullmatch(patterns[method], path[len(base):]), 'unexpected release API path')
        require(upload == (method == 'POST' and '/assets?' in path), 'unexpected upload origin')
        origin = self.origin if self.fixture or not upload else 'https://uploads.github.com'
        headers = self.headers('application/octet-stream' if binary else 'application/vnd.github+json')
        if upload:
            headers['Content-Type'] = 'application/octet-stream'
            headers['Content-Length'] = str(os.fstat(data.fileno()).st_size)
        elif data is not None:
            data = json.dumps(data, separators=(',', ':'), allow_nan=False).encode()
            headers['Content-Type'] = 'application/json'
        request = urllib.request.Request(origin + path, data=data, method=method, headers=headers)
        try:
            response = self.opener.open(request, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            with error:
                if binary and method == 'GET' and error.code in (301, 302, 307, 308):
                    return self.storage_response(error.headers.get('Location', ''))
            raise ValueError('Release operation could not be confirmed; inspect the same operation before resuming') from None
        except (urllib.error.URLError, OSError, http.client.HTTPException):
            raise ValueError('Release operation could not be confirmed; inspect the same operation before resuming') from None
        if response.status != expected:
            response.close()
            raise ValueError('Release API returned an unexpected status')
        return response

    def storage_response(self, location):
        require(not self.fixture and isinstance(location, str) and len(location) <= 8192
                and not any(char in location for char in '\r\n\x00'), 'invalid asset storage redirect')
        url = urllib.parse.urlsplit(location)
        require(url.scheme == 'https' and url.netloc in ('release-assets.githubusercontent.com', 'objects.githubusercontent.com')
                and not url.fragment and url.path.startswith('/'), 'unsupported asset storage redirect')
        try:
            response = self.opener.open(urllib.request.Request(location, headers={'Accept': 'application/octet-stream'}),
                                        timeout=self.timeout)
        except (urllib.error.URLError, OSError, http.client.HTTPException):
            raise ValueError('Asset storage readback could not be confirmed') from None
        if response.status != 200:
            response.close()
            raise ValueError('Asset storage returned an unexpected status')
        return response

    def metadata(self, method, path, **kwargs):
        try:
            with self.open_release(method, path, **kwargs) as response:
                if kwargs.get('expected') == 204:
                    return None
                raw = b''.join(registry.body_chunks(response, JSON_LIMIT, time.monotonic() + self.timeout))
                return decode_json(raw, 'release response')
        except (OSError, http.client.HTTPException):
            raise ValueError('Release response could not be confirmed') from None

    def rows(self, path, size):
        for page in range(1, PAGE_LIMIT + 1):
            rows = self.metadata('GET', path + '?per_page=' + str(size) + '&page=' + str(page))
            require(isinstance(rows, list) and len(rows) <= size and all(isinstance(row, dict) for row in rows),
                    'invalid or oversized release listing')
            yield from rows
            if len(rows) < size:
                return
        raise ValueError('Release listing exceeds its page limit; no absence is established')

    def find(self, tag):
        matches = [row for row in self.rows('/repos/'+publish.REPOSITORY+'/releases', 10) if row.get('tag_name') == tag]
        require(len(matches) <= 1, 'multiple releases use the same tag')
        return self.get(matches[0]['id']) if matches else None

    def get(self, release_id):
        value = self.metadata('GET', '/repos/'+publish.REPOSITORY+'/releases/'+identifier(release_id))
        require(isinstance(value, dict) and value.get('id') == release_id and type(value.get('id')) is int,
                'release readback has a different object identity')
        return value

    def create_release(self, expected):
        value = self.metadata('POST', '/repos/'+publish.REPOSITORY+'/releases',
                              data=dict(expected, draft=True, generate_release_notes=False, make_latest='false'), expected=201)
        require(isinstance(value, dict), 'invalid creation response')
        identifier(value.get('id'))
        return value

    def assets(self, release_id):
        result = []
        for row in self.rows('/repos/'+publish.REPOSITORY+'/releases/'+identifier(release_id)+'/assets', 100):
            require(len(result) < FILE_LIMIT, 'remote asset listing exceeds its file limit')
            result.append(row)
        return result

    def upload(self, release_id, path, record, label, *, work):
        path, work = Path(path).absolute(), Path(work).absolute()
        require(isinstance(record, dict) and set(record) == {'size', 'sha256'}
                and type(record['size']) is int and 0 < record['size'] <= ASSET_LIMIT
                and isinstance(record['sha256'], str) and re.fullmatch(r'[a-f0-9]{64}', record['sha256']),
                'invalid upload input descriptor')
        require(registry.private_work_bytes(work) + record['size'] <= WORK_LIMIT,
                'upload copy would exceed the storage quota')
        query = urllib.parse.urlencode({'name': file_name(path.name), 'label': label})
        with tempfile.TemporaryDirectory(prefix='asset-upload-', dir=work) as temporary:
            payload = Path(temporary) / 'payload'
            registry.verified_copy(path, payload, record['size'], record['sha256'],
                                   deadline=time.monotonic()+self.timeout, maximum=ASSET_LIMIT)
            with payload.open('rb') as stream:
                payload.unlink()  # Stream the open verified inode, with no replaceable filename.
                value = self.metadata('POST', '/repos/'+publish.REPOSITORY+'/releases/'+identifier(release_id)+'/assets?'+query,
                                      data=stream, expected=201, upload=True)
        require(isinstance(value, dict), 'invalid asset upload response')
        return value

    def download(self, asset, record, destination):
        path = '/repos/'+publish.REPOSITORY+'/releases/assets/'+identifier(asset.get('id'))
        try:
            with self.open_release('GET', path, binary=True) as response, destination.open('xb') as writer:
                length = response.headers.get('Content-Length')
                require(length is None or length == str(record['size']), 'asset response size differs')
                digest, size = hashlib.sha256(), 0
                for data in registry.body_chunks(response, record['size'], time.monotonic()+self.timeout):
                    writer.write(data); digest.update(data); size += len(data)
                require(size == record['size'] and digest.hexdigest() == record['sha256'], 'asset readback bytes differ')
        except (OSError, http.client.HTTPException):
            raise ValueError('Asset readback could not be confirmed') from None

    def publish_release(self, release_id, prerelease):
        return self.metadata('PATCH', '/repos/'+publish.REPOSITORY+'/releases/'+identifier(release_id),
                             data={'draft': False, 'prerelease': prerelease, 'make_latest': 'false'})

    def delete_draft(self, release_id):
        self.metadata('DELETE', '/repos/'+publish.REPOSITORY+'/releases/'+identifier(release_id), expected=204)


def inventory(directory, manifest, inputs):
    """Bind checksummed assets and the exact signature/provenance bundle bytes."""
    directory = Path(directory).absolute()
    payload = publication_evidence.artifact_inventory(directory, manifest, inputs)
    for name in ('SHA256SUMS', 'SHA256SUMS.sigstore.json', 'provenance.sigstore.json'):
        inputs.remember(directory/name)
        payload[name] = release.sha(directory/name)
    result = {}
    for name, digest in sorted(payload.items()):
        file_name(name)
        size = (directory/name).stat().st_size
        require(0 < size <= ASSET_LIMIT, 'asset is empty or exceeds its byte limit')
        result[name] = {'sha256': digest, 'size': size}
    require(len(result) <= FILE_LIMIT and sum(row['size'] for row in result.values()) <= WORK_LIMIT,
            'release readback exceeds its file or storage quota')
    require({path.name for path in directory.iterdir()} == set(result),
            'distribution contains unexpected files or directories')
    inputs.unchanged()
    return result


def unsigned_files(directory, manifest, inputs):
    directory = Path(directory).absolute()
    require(directory.resolve(strict=True) == directory and directory.is_dir(), 'invalid distribution directory')
    result = {}
    for path in sorted(directory.iterdir()):
        file_name(path.name)
        if path.name in SIGNED_FILES:
            continue
        digest = inputs.remember(path)
        size = path.stat().st_size
        require(0 < size <= ASSET_LIMIT and len(result) < FILE_LIMIT, 'unsigned asset exceeds its quota')
        result[path.name] = {'sha256': digest, 'size': size}
    require(result and sum(row['size'] for row in result.values()) <= WORK_LIMIT, 'unsigned distribution exceeds its quota')
    require(decode_report((directory/'RELEASE.json').read_bytes(), 'release source') == manifest,
            'distribution belongs to a different release')
    return result


def signing_cache(directory, manifest, inputs):
    """Inspect cached public verification material; this grants no signing trust."""
    directory = Path(directory).absolute()
    require(directory.resolve(strict=True) == directory and directory.is_dir()
            and {p.name for p in directory.iterdir()} == {SIGNING_LEDGER, *SIGNED_FILES},
            'signing cache contains missing, unexpected or aliased inputs')
    ledger_path = directory/SIGNING_LEDGER
    require(ledger_path.stat().st_size <= 1024**2, 'signing ledger exceeds its quota')
    inputs.remember(ledger_path)
    ledger = decode_report(signing_bytes(ledger_path, 1024**2), 'signing cache')
    require(set(ledger) == {'schema_version', 'release_identity', 'unsigned', 'signed'}
            and type(ledger.get('schema_version')) is int and ledger['schema_version'] == 1
            and ledger['release_identity'] == release.evidence_identity(manifest), 'signing cache belongs to different source')
    for kind in ('unsigned', 'signed'):
        rows = ledger[kind]
        require(isinstance(rows, dict) and 0 < len(rows) <= FILE_LIMIT, 'invalid signing inventory')
        for name, row in rows.items():
            file_name(name)
            require(isinstance(row, dict) and set(row) == {'sha256', 'size'}
                    and isinstance(row['sha256'], str) and re.fullmatch('[a-f0-9]{64}', row['sha256'])
                    and type(row['size']) is int and 0 < row['size'] <= ASSET_LIMIT, 'invalid signing descriptor')
    require(set(ledger['signed']) == set(SIGNED_FILES) and not set(ledger['unsigned']) & set(SIGNED_FILES)
            and sum(row['size'] for row in ledger['signed'].values()) <= SIGNING_LIMIT,
            'signing inventory has unexpected files or exceeds its quota')
    for name, row in ledger['signed'].items():
        path = directory/name
        require(path.stat().st_size == row['size'], 'cached signing size differs')
        inputs.remember(path, row['sha256'])
        signing_bytes(path, SIGNING_LIMIT, row['sha256'])
    inputs.unchanged()
    return ledger


def sync_directory(directory):
    descriptor = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def signing_owner(progress, manifest):
    progress = module_publication.private_directory(progress)
    owner = decode_report((progress/'publication.json').read_bytes(), 'publication owner')
    require(owner.get('release_identity') == release.evidence_identity(manifest)
            and owner.get('repository') == publish.REPOSITORY, 'signing progress belongs to another release')
    return progress


def asset_bundle(data, role, owner):
    """Check input binding; cryptographic verification remains mandatory later."""
    bundle = decode_report(data, 'asset signing bundle')
    require(bundle.get('mediaType') == 'application/vnd.dev.sigstore.bundle.v0.3+json'
            and isinstance(bundle.get('verificationMaterial'), dict), 'invalid asset signing bundle')
    if role == 'SHA256SUMS.sigstore.json':
        signed = bundle.get('messageSignature')
        require(isinstance(signed, dict) and isinstance(signed.get('messageDigest'), dict)
                and signed['messageDigest'].get('algorithm') == 'SHA2_256'
                and signed['messageDigest'].get('digest') == base64.b64encode(
                    bytes.fromhex(owner['inputs']['SHA256SUMS']['sha256'])).decode(),
                'signature does not bind the original checksum manifest')
        signature = signed.get('signature')
    else:
        envelope = bundle.get('dsseEnvelope')
        require(isinstance(envelope, dict) and envelope.get('payloadType') == 'application/vnd.in-toto+json'
                and isinstance(envelope.get('signatures'), list) and len(envelope['signatures']) == 1
                and isinstance(envelope['signatures'][0], dict), 'invalid asset provenance envelope')
        try:
            statement = decode_report(base64.b64decode(envelope.get('payload'), validate=True), 'asset provenance')
        except (ValueError, TypeError, RecursionError):
            raise ValueError('Release publication stopped: invalid asset provenance payload') from None
        subjects = statement.get('subject')
        expected = {**owner['unsigned'], 'trusted_root.jsonl': owner['inputs']['trusted_root.jsonl']}
        require(statement.get('_type') == 'https://in-toto.io/Statement/v1'
                and statement.get('predicateType') == 'https://slsa.dev/provenance/v1'
                and isinstance(subjects, list) and len(subjects) == len(expected)
                and all(isinstance(s, dict) and set(s) == {'name', 'digest'}
                        and isinstance(s['name'], str) for s in subjects)
                and {s['name']: s['digest'] for s in subjects} == {
                    name: {'sha256': row['sha256']} for name, row in expected.items()},
                'provenance subjects differ from the original checksum inventory')
        predicate = statement.get('predicate')
        definition = predicate.get('buildDefinition') if isinstance(predicate, dict) else None
        require(isinstance(definition, dict)
                and definition.get('buildType') == 'https://actions.github.io/buildtypes/workflow/v1'
                and definition.get('externalParameters') == {'workflow': {
                    'ref': 'refs/heads/main', 'repository': 'https://github.com/'+publish.REPOSITORY,
                    'path': '.github/workflows/release.yml'}}
                and definition.get('resolvedDependencies') == [{
                    'uri': 'git+https://github.com/'+publish.REPOSITORY+'@refs/heads/main',
                    'digest': {'gitCommit': owner['release_identity']['commit']}}],
                'asset provenance does not bind the approved source and workflow')
        signature = envelope['signatures'][0].get('sig')
    try:
        decoded = base64.b64decode(signature, validate=True)
    except (ValueError, TypeError):
        raise ValueError('Release publication stopped: invalid asset signature encoding') from None
    require(0 < len(decoded) <= 4096, 'invalid asset signature size')


def asset_signing_cache(directory, manifest, inputs):
    """Inspect a partial generation cache without granting signing trust."""
    directory = qualification.regular(Path(directory).absolute(), directory=True)
    path = directory/'identity.json'
    owner = decode_report(signing_bytes(path, 1024**2), 'asset signing identity')
    inputs.remember(path)
    require(set(owner) == {'schema_version', 'release_identity', 'unsigned', 'inputs'}
            and type(owner['schema_version']) is int and owner['schema_version'] == 1
            and owner['release_identity'] == release.evidence_identity(manifest), 'asset signing belongs to different source')
    require(isinstance(owner['unsigned'], dict) and 0 < len(owner['unsigned']) <= FILE_LIMIT
            and not set(owner['unsigned']) & set(SIGNED_FILES)
            and isinstance(owner['inputs'], dict) and set(owner['inputs']) == {'trusted_root.jsonl', 'SHA256SUMS'},
            'invalid asset signing input inventory')
    for name, row in {**owner['unsigned'], **owner['inputs']}.items():
        file_name(name)
        require(isinstance(row, dict) and set(row) == {'sha256', 'size'}
                and isinstance(row['sha256'], str) and re.fullmatch('[a-f0-9]{64}', row['sha256'])
                and type(row['size']) is int and 0 < row['size'] <= ASSET_LIMIT, 'invalid signing input descriptor')
    allowed = {'identity.json', *owner['inputs']} | {name+suffix for name in ASSET_SIGNING_ROLES for suffix in ('', '.record.json')}
    require({p.name for p in directory.iterdir()} <= allowed, 'asset signing cache contains unexpected files')
    for name, row in owner['inputs'].items():
        data = signing_bytes(directory/name, SIGNING_LIMIT, row['sha256'])
        require(len(data) == row['size'], 'original signing input size differs')
        inputs.remember(directory/name, row['sha256'])
    sums = ''.join(row['sha256']+'  '+name+'\n' for name, row in sorted(
        {**owner['unsigned'], 'trusted_root.jsonl': owner['inputs']['trusted_root.jsonl']}.items())).encode()
    require(signing_bytes(directory/'SHA256SUMS', 1024**2) == sums, 'original checksums differ from signing inputs')
    for name in ASSET_SIGNING_ROLES:
        path, record = directory/name, directory/(name+'.record.json')
        if path.exists() or path.is_symlink():
            asset_bundle(signing_bytes(path), name, owner); inputs.remember(path)
        if record.exists() or record.is_symlink():
            value = decode_report(signing_bytes(record, 4096), 'asset signing record'); inputs.remember(record)
            require(path.is_file() and value == {'role': name, 'sha256': release.sha(path), 'size': path.stat().st_size},
                    'sealed asset bundle differs from its original bytes')
    require(sum(p.stat().st_size for p in directory.iterdir()) <= SIGNING_LIMIT, 'asset signing cache exceeds its quota')
    inputs.unchanged()
    return owner


class AssetSigning:
    """Commit generation intent before external signing; retain each outcome."""
    def __init__(self, manifest, directory, progress):
        self.progress, self.manifest = signing_owner(progress, manifest), manifest
        self.out, self.directory = Path(directory).absolute(), self.progress/ASSET_SIGNING_CACHE
        inputs = qualification.Inputs()
        unsigned = unsigned_files(self.out, manifest, inputs)
        if self.directory.exists() or self.directory.is_symlink():
            self.owner = asset_signing_cache(self.directory, manifest, inputs)
            require(self.owner['unsigned'] == unsigned, 'unsigned distribution differs from original signing inputs')
        else:
            require(not (self.progress/'signed').exists() and not (self.progress/ASSET_SIGNING_JOURNAL).exists(),
                    'older or incomplete signing progress lacks original asset-generation records; inspect the original run')
            self.owner = {'schema_version': 1, 'release_identity': release.evidence_identity(manifest), 'unsigned': unsigned,
                          'inputs': {name: {'sha256': hashlib.sha256(signing_bytes(self.out/name)).hexdigest(),
                                           'size': (self.out/name).stat().st_size}
                                     for name in ('trusted_root.jsonl', 'SHA256SUMS')}}
            self.directory.mkdir(mode=0o700)
            for name, row in self.owner['inputs'].items():
                save_signing_file(self.directory/name, signing_bytes(self.out/name, SIGNING_LIMIT, row['sha256']))
            save_signing_file(self.directory/'identity.json', release.encoded(self.owner))
            sync_directory(self.progress)
            asset_signing_cache(self.directory, manifest, inputs)
        module_publication.private_directory(self.directory)
        self.rows = [module_publication.marker('asset-generation', {'owner': self.owner, 'role': role})
                     for role in ASSET_SIGNING_ROLES]

    @contextmanager
    def session(self):
        with publish.TransferProgress(self.progress/ASSET_SIGNING_JOURNAL, self.owner, self.rows) as journal:
            expected = [dict(kind=row['kind'], digest=row['digest'], state=state)
                        for row in self.rows for state in ('started', 'verified')]
            require(journal.records == expected[:len(journal.records)], 'asset generation is duplicated or out of order')
            require(asset_signing_cache(self.directory, self.manifest, qualification.Inputs()) == self.owner,
                    'original signing inputs changed')
            for role, row in zip(ASSET_SIGNING_ROLES, self.rows):
                state = journal.states.get((row['kind'], row['digest']))
                raw, record = self.directory/role, self.directory/(role+'.record.json')
                require(state is not None or not (raw.exists() or record.exists()), 'asset bundle lacks generation intent')
                require(state != 'verified' or raw.exists() and record.exists(), 'completed asset generation lost original material')
            yield journal

    def finish(self, journal, role, source=None, backend=None):
        row = self.rows[ASSET_SIGNING_ROLES.index(role)]
        state = journal.states.get((row['kind'], row['digest']))
        require(state is not None, 'asset bundle has no committed generation intent')
        raw = self.directory/role
        data = signing_bytes(raw if source is None else source)
        asset_bundle(data, role, self.owner)
        save_signing_file(raw, data)
        if backend is not None:
            backend.command(['verify-blob', '--bundle', str(raw), *backend.trust(), str(self.directory/'SHA256SUMS')])
        require(signing_bytes(raw) == data, 'original asset bundle changed during verification')
        with raw.open('rb') as stream:
            os.fsync(stream.fileno())
        sync_directory(self.directory)
        save_signing_file(self.directory/(role+'.record.json'), release.encoded(
            {'role': role, 'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)}))
        if state != 'verified':
            journal.record(row, 'verified')

    def prepare_provenance(self):
        with self.session() as journal:
            row = self.rows[0]; state = journal.states.get((row['kind'], row['digest']))
            if state is not None:
                require((self.directory/ASSET_SIGNING_ROLES[0]).exists(),
                        'asset attestation began without recoverable material; inspect the original run')
                self.finish(journal, ASSET_SIGNING_ROLES[0])
                return {'generate': False, 'release_gate_passed': False}
            journal.reserve(row); journal.record(row, 'started')
        return {'generate': True, 'release_gate_passed': False}

    def capture_provenance(self, source):
        with self.session() as journal:
            self.finish(journal, ASSET_SIGNING_ROLES[0], source)
        return {'captured': True, 'release_gate_passed': False}

    def sign_checksum(self, backend):
        with self.session() as journal:
            require(journal.states.get((self.rows[0]['kind'], self.rows[0]['digest'])) == 'verified',
                    'asset provenance must be preserved before checksum signing')
            row = self.rows[1]; role = ASSET_SIGNING_ROLES[1]
            state = journal.states.get((row['kind'], row['digest']))
            require(state is None or (self.directory/role).exists(),
                    'checksum signing began without recoverable material; inspect the original run')
            # Cached binding/seals are not signing trust. Verify the complete
            # original DSSE statement before requesting another public signature.
            backend.command(['verify-blob-attestation', '--bundle', str(self.directory/ASSET_SIGNING_ROLES[0]),
                             '--digestAlg', 'sha256', '--digest', self.owner['unsigned']['RELEASE.json']['sha256'],
                             '--type', 'https://slsa.dev/provenance/v1', *backend.trust()])
            if state is None:
                journal.reserve(row); journal.record(row, 'started')
                backend.command(['sign-blob', '--yes', '--bundle', str(self.directory/role), str(self.directory/'SHA256SUMS')])
            self.finish(journal, role, backend=backend)
        self.restore()
        return {'signed': True, 'release_gate_passed': False}

    def restore(self):
        with self.session() as journal:
            for role, row in zip(ASSET_SIGNING_ROLES, self.rows):
                require(journal.states.get((row['kind'], row['digest'])) is None or (self.directory/role).exists(),
                        'asset signing began without recoverable material; inspect the original run')
            files = {name: signing_bytes(self.directory/name) for name in SIGNED_FILES if (self.directory/name).exists()}
            initial = ''.join(row['sha256']+'  '+name+'\n' for name, row in sorted(self.owner['unsigned'].items())).encode()
            for name, data in files.items():
                path = self.out/name
                if path.exists() or path.is_symlink():
                    current = signing_bytes(path)
                    require(current == data or name == 'SHA256SUMS' and current == initial,
                            'existing signing material differs from the original bytes')
            for name, data in files.items():
                path = self.out/name
                if path.exists() and signing_bytes(path) == data:
                    continue
                save_signing_file(path, data, replace=path.exists())
            complete = all(journal.states.get((row['kind'], row['digest'])) == 'verified' for row in self.rows)
            return {'reused': complete, 'inputs_ready': True, 'files': len(files), 'release_gate_passed': False}


def capture_signing(manifest, directory, progress):
    """Atomically retain original bundle bytes before any release mutation."""
    progress = signing_owner(progress, manifest)
    if (progress/ASSET_SIGNING_CACHE).exists():
        require(AssetSigning(manifest, directory, progress).restore()['reused'], 'original asset signing is incomplete')
    inputs = qualification.Inputs()
    assets = inventory(directory, manifest, inputs)
    ledger = {'schema_version': 1, 'release_identity': release.evidence_identity(manifest),
              'unsigned': {name: row for name, row in assets.items() if name not in SIGNED_FILES},
              'signed': {name: assets[name] for name in SIGNED_FILES}}
    require(sum(row['size'] for row in ledger['signed'].values()) <= SIGNING_LIMIT, 'signing cache exceeds its quota')
    destination = progress/'signed'
    if destination.exists() or destination.is_symlink():
        require(signing_cache(destination, manifest, inputs) == ledger, 'original signing cache cannot be replaced')
        return {'reused': True, 'files': len(SIGNED_FILES), 'release_gate_passed': False}
    # Staging outside the retained progress directory means a process kill before
    # rename cannot upload an incomplete cache as a completed journal artifact.
    with tempfile.TemporaryDirectory(prefix='signing-', dir=progress.parent) as temporary:
        staging = Path(temporary)/'signed'; staging.mkdir(mode=0o700)
        for name in SIGNED_FILES:
            data = signing_bytes(Path(directory)/name, SIGNING_LIMIT, ledger['signed'][name]['sha256'])
            with (staging/name).open('xb') as writer:
                os.chmod(staging/name, 0o600)
                writer.write(data); writer.flush(); os.fsync(writer.fileno())
        with (staging/SIGNING_LEDGER).open('xb') as writer:
            os.chmod(staging/SIGNING_LEDGER, 0o600)
            writer.write(release.encoded(ledger)); writer.flush(); os.fsync(writer.fileno())
        signing_cache(staging, manifest, inputs)
        inputs.unchanged(); sync_directory(staging)
        require(not destination.exists() and not destination.is_symlink(), 'signing cache already exists')
        os.rename(staging, destination); sync_directory(progress)
    return {'reused': False, 'files': len(SIGNED_FILES), 'release_gate_passed': False}


def restore_signing(manifest, directory, progress):
    progress = signing_owner(progress, manifest)
    cache = progress/'signed'
    if not cache.exists() and not cache.is_symlink():
        if (progress/ASSET_SIGNING_CACHE).exists() or (progress/ASSET_SIGNING_JOURNAL).exists():
            return AssetSigning(manifest, directory, progress).restore()
        return {'reused': False, 'inputs_ready': False, 'files': 0, 'release_gate_passed': False}
    inputs = qualification.Inputs()
    ledger = signing_cache(cache, manifest, inputs)
    directory = Path(directory).absolute()
    unsigned = unsigned_files(directory, manifest, inputs)
    require(unsigned == ledger['unsigned'], 'unsigned distribution differs from original signing inputs')
    # A freshly assembled distribution already has an unsigned checksum file.
    # Only that exact derived file may be replaced with the retained signed one.
    initial_sums = ''.join(row['sha256']+'  '+name+'\n' for name, row in unsigned.items()).encode()
    for name, row in ledger['signed'].items():
        destination = directory/name
        if destination.exists() or destination.is_symlink():
            digest = inputs.remember(destination)
            require(digest == row['sha256'] or (name == 'SHA256SUMS' and signing_bytes(destination, 1024**2) == initial_sums),
                    'existing signing material differs from the original bytes')
    inputs.unchanged()
    for name, row in ledger['signed'].items():
        destination = directory/name
        if destination.exists() and release.sha(destination) == row['sha256']:
            continue
        descriptor, temporary = tempfile.mkstemp(prefix='.signing-', dir=directory)
        try:
            data = signing_bytes(cache/name, SIGNING_LIMIT, row['sha256'])
            with os.fdopen(descriptor, 'wb') as writer:
                writer.write(data); writer.flush(); os.fsync(writer.fileno())
            require(release.sha(Path(temporary)) == row['sha256'], 'cached signing bytes changed during restoration')
            os.replace(temporary, destination); sync_directory(directory)
        finally:
            Path(temporary).unlink(missing_ok=True)
    expected = {**ledger['unsigned'], **ledger['signed']}
    require(inventory(directory, manifest, qualification.Inputs()) == dict(sorted(expected.items())),
            'restored signing inventory differs')
    return {'reused': True, 'inputs_ready': True, 'files': len(SIGNED_FILES), 'release_gate_passed': False}


def expected_release(manifest, role, assets):
    fingerprint = hashlib.sha256(release.encoded({'release': release.evidence_identity(manifest), 'assets': assets})).hexdigest()
    tag = manifest['version'] if role == 'application' else 'cpra-validation/'+manifest['commit']+'-'+fingerprint[:24]
    return {'tag_name': tag, 'target_commitish': manifest['commit'],
            'name': ('CPRa ' if role == 'application' else 'Verification for CPRa ')+manifest['version'],
            'body': 'Release '+manifest['version']+' from '+manifest['commit']+'.\n\n'
                    'RELEASE.json, SHA256SUMS, dependency inventories and retained verification reports identify the original artifacts.\n'
                    '<!-- cpra-publication:'+fingerprint+' -->',
            'prerelease': '-' in manifest['version']}


def release_matches(value, expected, *, published=False):
    require(isinstance(value, dict), 'invalid release metadata')
    identifier(value.get('id'))
    require(all(value.get(key) == field for key, field in expected.items())
            and type(value.get('prerelease')) is bool and type(value.get('draft')) is bool,
            'release identity, metadata or prerelease policy differs')
    require(value['draft'] is (not published), 'release publication state differs')
    if published:
        require(value.get('immutable') is True, 'published release is not immutable')


def asset_matches(value, name, record, label):
    require(isinstance(value, dict), 'invalid asset metadata')
    identifier(value.get('id'))
    require(value.get('name') == name and value.get('label') == label and value.get('state') == 'uploaded'
            and type(value.get('size')) is int and value['size'] == record['size']
            and value.get('digest') in (None, 'sha256:'+record['sha256']),
            'asset is incomplete, belongs to another operation or has different metadata; replacement is forbidden')


def read_published(manifest, directory, client, verify_assets, work):
    """Bounded signed readback before any image/chart version promotion."""
    manifest = decode_report(release.encoded(manifest), 'frozen release source')
    work = module_publication.private_directory(work)
    require(callable(verify_assets), 'signed-asset verification is required')
    inputs = qualification.Inputs()
    assets = inventory(directory, manifest, inputs)
    require(sum(path.stat().st_size for path in work.rglob('*') if path.is_file())
            + sum(row['size'] for row in assets.values()) <= WORK_LIMIT, 'working directory exceeds readback quota')
    expected = expected_release(manifest, 'application', assets)
    label = 'cpra-'+release.evidence_identity(manifest)['release_manifest_sha256']
    client.prepare(manifest['commit'])
    require(client.reference(manifest['version']) == manifest['commit'], 'application tag differs from source')
    value = client.find(manifest['version'])
    release_matches(value, expected, published=True)
    remote = client.assets(value['id'])
    names = [row.get('name') for row in remote]
    ids = [identifier(row.get('id')) for row in remote]
    require(all(isinstance(name, str) for name in names) and len(names) == len(assets)
            and set(names) == set(assets) and len(ids) == len(set(ids)), 'published asset inventory differs')
    with tempfile.TemporaryDirectory(prefix='release-readback-', dir=work) as temporary:
        destination = Path(temporary)
        for asset in remote:
            name = asset['name']
            asset_matches(asset, name, assets[name], label)
            client.download(asset, assets[name], destination/name)
        verify_assets(destination)
        inputs.unchanged()
    current = client.get(value['id'])
    release_matches(current, expected, published=True)
    require(client.reference(manifest['version']) == manifest['commit'], 'application tag changed during readback')
    return {'release_id': value['id'], 'assets_verified': len(assets), 'release_gate_passed': False}


def publish_release(manifest, directory, client, modules, verify_assets, progress, work):
    """Original drafts/assets, dependency tags, immutable final release, then cleanup.

    The caller owns the aggregate gate and signing policy. Verification must
    inspect downloaded bytes; this function never supplies a passing stub.
    """
    manifest = decode_report(release.encoded(manifest), 'frozen release source')
    work = module_publication.private_directory(work)
    directory, progress = Path(directory).absolute(), Path(progress).absolute()
    require(not work.is_relative_to(directory) and not progress.is_relative_to(directory),
            'progress and readback paths overlap retained distribution inputs')
    require(callable(modules) and callable(verify_assets), 'module and signed-asset verification callbacks are required')
    inputs = qualification.Inputs()
    assets = inventory(directory, manifest, inputs)
    require(sum(path.stat().st_size for path in work.rglob('*') if path.is_file())
            + sum(row['size'] for row in assets.values()) <= WORK_LIMIT,
            'working directory has insufficient readback quota')
    expected = {role: expected_release(manifest, role, assets) for role in ('staging', 'application')}
    identity = {'release_identity': release.evidence_identity(manifest), 'origin': client.origin,
                'repository': publish.REPOSITORY, 'assets': assets, 'releases': expected}
    marker = module_publication.marker
    created = {role: marker('release-create', {'role': role, **identity}) for role in expected}
    entries = {role: {name: marker('release-asset', {'role': role, 'name': name, 'record': row, **identity})
                      for name, row in assets.items()} for role in expected}
    module_entry = marker('release-modules', identity)
    published_entry = marker('release-publish', identity)
    cleaned_entry = marker('release-cleanup', identity)
    objects = [created['staging'], *entries['staging'].values(), module_entry,
               created['application'], *entries['application'].values(), published_entry, cleaned_entry]
    label = 'cpra-'+identity['release_identity']['release_manifest_sha256']
    def key(entry): return entry['kind'], entry['digest']
    with publish.TransferProgress(progress, identity, objects) as journal:
        sequence = [dict(kind=row['kind'], digest=row['digest'], state=state)
                    for row in objects for state in ('started', 'verified')]
        observed = [{name: row[name] for name in ('kind', 'digest', 'state')} for row in journal.records]
        require(observed == sequence[:len(observed)], 'release progress is out of order or duplicated')
        for row in journal.records:
            require((row['kind'] == 'release-modules') == ('receipt' not in row) or row['state'] == 'started',
                    'verified remote object has no authoritative receipt')
        for entry, original in ((published_entry, created['application']), (cleaned_entry, created['staging'])):
            receipt = journal.receipts.get(key(entry))
            if receipt is not None:
                require(receipt == journal.receipts.get(key(original)), 'remote receipts contradict the original object identity')
        client.prepare(manifest['commit'])
        found = {role: client.find(row['tag_name']) for role, row in expected.items()}
        inputs.unchanged()
        cleanup = journal.states.get(key(cleaned_entry))
        for role, value in found.items():
            state = journal.states.get(key(created[role]))
            if value is not None:
                require(state is not None, 'release already exists without original progress')
                is_published = role == 'application' and value.get('draft') is False
                require(not is_published or journal.states.get(key(published_entry)) is not None,
                        'release was published outside its original operation')
                release_matches(value, expected[role], published=is_published)
                receipt = journal.receipts.get(key(created[role]))
                require(receipt is None or receipt['id'] == value['id'], 'original release object was replaced')
            elif state == 'verified':
                require(role == 'staging' and cleanup is not None, 'previously verified release is missing')
        def start(entry, receipt=True):
            if journal.states.get(key(entry)) is None:
                journal.reserve(entry, receipt=receipt)
                journal.record(entry, 'started')
        def verified(entry, remote_id=None):
            if journal.states.get(key(entry)) != 'verified':
                journal.record(entry, 'verified', receipt={'id': remote_id} if remote_id is not None else None)
        def ensure(role):
            value = found[role]
            if value is None:
                start(created[role])
                inputs.unchanged()
                client.create_release(expected[role])
                inputs.unchanged()
                value = client.find(expected[role]['tag_name'])
                release_matches(value, expected[role])
            verified(created[role], value['id'])
            found[role] = value
            return value
        def readback(role, value):
            def current_assets():
                remote = client.assets(value['id'])
                names = [row.get('name') for row in remote]
                ids = [identifier(row.get('id')) for row in remote]
                require(all(isinstance(name, str) for name in names)
                        and len(names) == len(set(names)) and len(ids) == len(set(ids))
                        and all(name in assets for name in names),
                        'remote release has duplicate or unexpected assets')
                return {row['name']: row for row in remote}
            remote = current_assets()
            with tempfile.TemporaryDirectory(prefix='release-readback-', dir=work) as temporary:
                output = Path(temporary)
                for name, record in assets.items():
                    entry = entries[role][name]
                    state = journal.states.get(key(entry))
                    value_asset = remote.get(name)
                    if value_asset is None:
                        require(state != 'verified', 'previously verified asset is missing')
                        require(value['draft'] is True, 'published assets cannot be added or replaced')
                        start(entry)
                        inputs.unchanged()
                        client.upload(value['id'], directory/name, record, label, work=work)
                        inputs.unchanged()
                        current = current_assets()
                        value_asset = current.get(name)
                        require(value_asset is not None, 'uploaded asset has no confirmed remote result')
                    else:
                        require(state is not None, 'asset already exists without original progress')
                    asset_matches(value_asset, name, record, label)
                    receipt = journal.receipts.get(key(entry))
                    require(receipt is None or receipt['id'] == value_asset['id'], 'original asset object was replaced')
                    inputs.unchanged()
                    client.download(value_asset, record, output/name)
                    inputs.unchanged()
                    verified(entry, value_asset['id'])
                current = current_assets()
                require(set(current) == set(assets), 'remote asset inventory changed during readback')
                for name, record in assets.items():
                    asset_matches(current[name], name, record, label)
                    require(journal.receipts[key(entries[role][name])]['id'] == current[name]['id'],
                            'original asset object was replaced during readback')
                verify_assets(output)
                inputs.unchanged()
        if cleanup is None:
            staged = ensure('staging')
            readback('staging', staged)
        start(module_entry, receipt=False)
        modules()
        inputs.unchanged()
        verified(module_entry)
        final = ensure('application')
        require(client.reference(manifest['version']) == manifest['commit'], 'application tag differs from source')
        readback('application', final)
        if final['draft'] is True:
            require(journal.states.get(key(published_entry)) != 'verified', 'previously published release returned to draft')
            start(published_entry)
            inputs.unchanged()
            client.publish_release(final['id'], expected['application']['prerelease'])
        final = client.get(final['id'])
        release_matches(final, expected['application'], published=True)
        require(client.reference(manifest['version']) == manifest['commit'], 'published application tag changed')
        inputs.unchanged()
        readback('application', final)
        verified(published_entry, final['id'])
        start(cleaned_entry)
        stage = client.find(expected['staging']['tag_name'])
        if stage is not None:
            require(cleanup != 'verified', 'previously deleted draft was replaced')
            release_matches(stage, expected['staging'])
            require(stage['id'] == journal.receipts[key(created['staging'])]['id'], 'staging object was replaced')
            inputs.unchanged()
            client.delete_draft(stage['id'])
        require(client.find(expected['staging']['tag_name']) is None, 'draft cleanup has no verified remote result')
        inputs.unchanged()
        verified(cleaned_entry, journal.receipts[key(created['staging'])]['id'])
    return {'release_id': final['id'], 'tag': manifest['version'], 'immutable': True,
            'assets_verified': len(assets), 'staging_removed': True, 'release_gate_passed': False,
            'scope': 'release transport and recovery; caller owns complete private qualification and signing policy'}
