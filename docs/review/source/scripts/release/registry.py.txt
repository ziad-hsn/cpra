#!/usr/bin/env python3
"""Transfer exact OCI objects and verify bytes at digests or explicit version tags."""
import argparse
import base64
import json
import math
import os
from pathlib import Path
import re
import hashlib
import signal
import stat
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from evidence import decode_report

REPOSITORIES = ('ziad-hsn/cpra', 'ziad-hsn/charts/cpra')
METADATA_LIMIT = 64 * 1024
OBJECT_LIMIT = 2 * 1024 ** 3
PROCESS_LOG_LIMIT = 1024 ** 2
WORK_LIMIT = 1536 * 1024 ** 2
WORK_FILE_LIMIT = 100000
TOOL_LIMIT = 64 * 1024 ** 2


def private_work_bytes(work):
    """Account for existing transfer files before reserving another copy."""
    work = Path(work).absolute()
    try:
        mode = work.lstat()
        if (work.resolve(strict=True) != work or not stat.S_ISDIR(mode.st_mode)
                or mode.st_uid != os.getuid() or stat.S_IMODE(mode.st_mode) & 0o077):
            raise ValueError('Transfer requires a private owned working directory')
        total = count = 0
        def failed(_):
            raise ValueError('Transfer working directory cannot be inspected')
        for parent, directories, files in os.walk(work, followlinks=False, onerror=failed):
            for name in directories + files:
                count += 1
                if count > WORK_FILE_LIMIT:
                    raise ValueError('Transfer working directory exceeds its file quota')
                try:
                    entry = (Path(parent) / name).lstat()
                except FileNotFoundError:
                    continue  # A transfer subprocess may remove its own temporary file.
                if stat.S_ISREG(entry.st_mode):
                    total += entry.st_size
                elif not stat.S_ISDIR(entry.st_mode):
                    raise ValueError('Transfer working directory contains a link or nonregular file')
                if total > WORK_LIMIT:
                    raise ValueError('Transfer working directory exceeds its storage quota')
        return total
    except OSError:
        raise ValueError('Transfer working directory cannot be inspected') from None


def source_stat(path):
    """Reject aliases and special files before opening an upload input."""
    try:
        path = Path(path).absolute()
        mode = path.lstat()
        if (path.resolve(strict=True) != path or not stat.S_ISREG(mode.st_mode)
                or mode.st_nlink != 1):
            raise ValueError('Upload input must be an unaliased regular file')
        return mode
    except OSError:
        raise ValueError('Upload input cannot be inspected') from None


def file_identity(mode):
    return (mode.st_dev, mode.st_ino, mode.st_mode, mode.st_nlink,
            mode.st_size, mode.st_mtime_ns, mode.st_ctime_ns)


def verified_copy(source, destination, size, sha256, *, deadline, maximum=OBJECT_LIMIT, mode=0o400):
    """Freeze a bounded file and check its digest before any external operation."""
    if (type(size) is not int or not 0 <= size <= maximum
            or not isinstance(sha256, str) or not re.fullmatch(r'[a-f0-9]{64}', sha256)
            or type(deadline) not in (int, float) or not math.isfinite(deadline)
            or mode not in (0o400, 0o500)):
        raise ValueError('Invalid upload input descriptor or deadline')
    source, destination = Path(source).absolute(), Path(destination).absolute()
    before = source_stat(source)
    if before.st_size != size:
        raise ValueError('Upload input size differs from its descriptor')
    if time.monotonic() >= deadline:
        raise ValueError('Upload input copy deadline exceeded')
    created = False
    try:
        flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK
        with os.fdopen(os.open(source, flags), 'rb') as reader:
            if file_identity(os.fstat(reader.fileno())) != file_identity(before):
                raise ValueError('Upload input changed before reading')
            output = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            created = True
            with os.fdopen(output, 'wb') as writer:
                digest, total = hashlib.sha256(), 0
                while True:
                    if time.monotonic() >= deadline:
                        raise ValueError('Upload input copy deadline exceeded')
                    data = reader.read(min(1024 ** 2, size - total + 1))
                    if not data:
                        break
                    total += len(data)
                    if total > size:
                        raise ValueError('Upload input grew while reading')
                    writer.write(data)
                    digest.update(data)
                if total != size or digest.hexdigest() != sha256:
                    raise ValueError('Upload input differs from its reviewed checksum')
                if (file_identity(os.fstat(reader.fileno())) != file_identity(before)
                        or file_identity(source_stat(source)) != file_identity(before)):
                    raise ValueError('Upload input changed while reading')
                writer.flush()
                os.fchmod(writer.fileno(), mode)
                os.fsync(writer.fileno())
    except (OSError, ValueError) as error:
        if created:
            destination.unlink(missing_ok=True)
        if isinstance(error, ValueError):
            raise
        raise ValueError('Upload input could not be copied') from None


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None  # Inspect the HTTP error; never forward registry credentials.


def validate_object(row):
    if (set(row) != {'kind', 'digest', 'size', 'media_type'}
            or row['kind'] not in ('blob', 'manifest')
            or not isinstance(row['digest'], str)
            or not re.fullmatch(r'sha256:[0-9a-f]{64}', row['digest'])
            or type(row['size']) is not int or not 0 <= row['size'] <= OBJECT_LIMIT
            or not isinstance(row['media_type'], str)
            or not re.fullmatch(r'application/[a-z0-9.+-]+', row['media_type'])
            or (row['kind'] == 'manifest' and not row['size'])):
        raise ValueError('Invalid OCI object descriptor')


def validate_tag(row, tag, *, repository='ziad-hsn/cpra'):
    from release import valid_version
    version = 'v' + tag if repository == 'ziad-hsn/charts/cpra' and isinstance(tag, str) else tag
    if (repository not in REPOSITORIES or row['kind'] != 'manifest' or not valid_version(version)
            or (repository == 'ziad-hsn/charts/cpra'
                and row['media_type'] != 'application/vnd.oci.image.manifest.v1+json')):
        raise ValueError('Only an explicit version for this repository can reference a manifest')


def referrer_tag(row, subject, repository):
    if (repository != 'ziad-hsn/cpra' or row['kind'] != 'manifest'
            or row['media_type'] != 'application/vnd.oci.image.index.v1+json'
            or not isinstance(subject, str) or not re.fullmatch(r'sha256:[a-f0-9]{64}', subject)):
        raise ValueError('A referrer fallback index requires its original application subject')
    return subject.replace(':', '-', 1)


def body_chunks(response, limit, deadline):
    """read1 bounds wall time even when a peer trickles a partial HTTP body."""
    total = 0
    while True:
        if time.monotonic() >= deadline:
            raise ValueError('Registry response deadline exceeded')
        chunk = response.read1(min(1024 ** 2, limit - total + 1))
        if not chunk:
            return
        total += len(chunk)
        if total > limit:
            raise ValueError('Registry response exceeds its byte limit')
        yield chunk


def metadata(response, timeout):
    data = b''.join(body_chunks(response, METADATA_LIMIT, time.monotonic() + timeout))
    try:
        return decode_report(data, 'registry response')
    except ValueError:
        raise ValueError('Registry returned invalid metadata') from None


class Registry:
    def __init__(self, repository, *, origin='https://ghcr.io', username=None,
                 token=None, allow_loopback_http=False, timeout=30):
        parsed = urllib.parse.urlsplit(origin)
        fixture = (allow_loopback_http and parsed.scheme == 'http'
                   and parsed.hostname == '127.0.0.1' and parsed.port is not None
                   and origin == 'http://127.0.0.1:' + str(parsed.port))
        if repository not in REPOSITORIES or (origin != 'https://ghcr.io' and not fixture):
            raise ValueError('Invalid canonical registry origin or repository')
        if type(timeout) not in (int, float) or not 0 < timeout <= 300:
            raise ValueError('Invalid registry request deadline')
        if fixture and (username is not None or token is not None):
            raise ValueError('Production registry credentials cannot be used in a loopback fixture')
        if not fixture and (not isinstance(username, str) or not username
                            or not isinstance(token, str) or not token
                            or any(c in username + token for c in '\r\n\x00')):
            raise ValueError('Explicit registry credentials are required')
        self.repository, self.origin, self.host = repository, origin, parsed.netloc
        self.username, self.token = username, token
        self.fixture, self.timeout = fixture, timeout
        self.opener = urllib.request.build_opener(NoRedirect())
        self.bearer = None

    def authorization(self):
        if self.fixture:
            return {}
        if self.bearer is None:
            credentials = base64.b64encode((self.username + ':' + self.token).encode()).decode()
            query = urllib.parse.urlencode({'service': 'ghcr.io',
                                           'scope': 'repository:' + self.repository + ':pull'})
            request = urllib.request.Request('https://ghcr.io/token?' + query,
                                            headers={'Authorization': 'Basic ' + credentials})
            try:
                with self.opener.open(request, timeout=self.timeout) as response:
                    result = metadata(response, self.timeout)
            except (urllib.error.URLError, OSError):
                raise ValueError('Registry authentication could not be established') from None
            value = result.get('token')
            if (not isinstance(value, str) or len(value) > 16384
                    or not re.fullmatch(r'[A-Za-z0-9._~+/-]+=*', value)):
                raise ValueError('Registry returned an invalid authentication token')
            self.bearer = value
        return {'Authorization': 'Bearer ' + self.bearer}

    def response(self, kind, reference, *, query=''):
        if (kind not in ('blobs', 'manifests', 'tags', 'referrers') or not isinstance(reference, str)
                or (kind == 'tags' and reference != 'list')
                or (kind in ('blobs', 'referrers') and not re.fullmatch(r'sha256:[0-9a-f]{64}', reference))
                or (kind != 'tags' and not re.fullmatch(r'(sha256:[0-9a-f]{64}|[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})', reference))):
            raise ValueError('Invalid registry object reference')
        if query and (kind != 'referrers' or not isinstance(query, str) or len(query) > 1024
                      or re.search(r'[\x00-\x20\x7f#]', query)):
            raise ValueError('Invalid referrer pagination query')
        path = self.origin + '/v2/' + self.repository + '/' + kind + '/' + reference
        if query:
            path += '?' + query
        headers = self.authorization()
        headers['Accept'] = ','.join(('application/vnd.oci.image.manifest.v1+json',
                                     'application/vnd.oci.image.index.v1+json',
                                     'application/vnd.docker.distribution.manifest.v2+json',
                                     'application/vnd.docker.distribution.manifest.list.v2+json'))
        request = urllib.request.Request(path, headers=headers)
        try:
            return self.opener.open(request, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            with error:
                if error.code in (301, 302, 307, 308) and kind == 'blobs' and not self.fixture:
                    return self.storage_response(error.headers.get('Location', ''))
                if error.code != 404:
                    raise ValueError('Registry outcome is uncertain (HTTP ' + str(error.code) + ')') from None
                if kind == 'referrers':
                    # OCI 1.1 requires fallback on this endpoint's 404, which
                    # older registries may return as plain text. This does not
                    # establish absence of a manifest or fallback index.
                    for _ in body_chunks(error, METADATA_LIMIT, time.monotonic()+self.timeout):
                        pass
                    return None
                codes = (('BLOB_UNKNOWN', 'NAME_UNKNOWN') if kind == 'blobs' else
                         ('MANIFEST_UNKNOWN', 'NAME_UNKNOWN', 'UNSUPPORTED') if kind == 'referrers' else
                         ('MANIFEST_UNKNOWN', 'NAME_UNKNOWN'))
                errors = metadata(error, self.timeout).get('errors')
                if (not isinstance(errors, list) or not errors
                        or any(not isinstance(item, dict) or item.get('code') not in codes for item in errors)):
                    raise ValueError('Registry response does not establish object absence') from None
                return None
        except (urllib.error.URLError, OSError):
            raise ValueError('Registry outcome is uncertain; no absence was established') from None

    def referrers(self, subject):
        """Bound same-origin pagination. None means use the OCI tag schema."""
        path = '/v2/'+self.repository+'/referrers/'+subject
        query, seen, rows, digests = '', set(), [], set()
        for _ in range(32):
            if query in seen:
                raise ValueError('Registry referrer pagination repeats a page')
            seen.add(query)
            response = self.response('referrers', subject, query=query)
            if response is None:
                if len(seen) != 1:
                    raise ValueError('Registry referrer pagination disappeared')
                return None
            with response:
                if (response.status != 200 or response.headers.get('Content-Type', '').split(';')[0]
                        != 'application/vnd.oci.image.index.v1+json'):
                    raise ValueError('Registry returned an invalid referrer index')
                value, link = metadata(response, self.timeout), response.headers.get('Link')
            if (value.get('schemaVersion') != 2 or type(value.get('schemaVersion')) is not int
                    or value.get('mediaType') != 'application/vnd.oci.image.index.v1+json'
                    or not isinstance(value.get('manifests'), list)):
                raise ValueError('Registry returned invalid referrer descriptors')
            for row in value['manifests']:
                if not isinstance(row, dict):
                    raise ValueError('Invalid referrer descriptor')
                validate_object({'kind': 'manifest', 'digest': row.get('digest'),
                                 'size': row.get('size'), 'media_type': row.get('mediaType')})
                if row['digest'] in digests or len(rows) >= 512:
                    raise ValueError('Registry referrers are duplicated or exceed their limit')
                digests.add(row['digest']); rows.append(row)
            if link is None:
                return rows
            match = re.fullmatch(r'<([^<>\x00-\x20]{1,2048})>;\s*rel="?next"?', link)
            if match is None:
                raise ValueError('Registry returned an invalid referrer pagination link')
            location = urllib.parse.urlsplit(urllib.parse.urljoin(self.origin+path, match[1]))
            if (location.scheme+'://'+location.netloc != self.origin or location.path != path
                    or location.fragment or not location.query):
                raise ValueError('Referrer pagination cannot leave its original origin or subject')
            pairs = urllib.parse.parse_qsl(location.query, keep_blank_values=True, strict_parsing=True)
            if (not pairs or len({key for key, _ in pairs}) != len(pairs)
                    or any(key not in ('last', 'n') or not value or len(value) > 512
                           or re.search(r'[\x00-\x1f\x7f]', value) for key, value in pairs)
                    or any(key == 'n' and (not value.isdecimal() or not 0 < int(value) <= 500) for key, value in pairs)):
                raise ValueError('Invalid referrer pagination parameters')
            query = urllib.parse.urlencode(pairs)
        raise ValueError('Registry referrer pagination exceeds its page limit')

    def storage_response(self, location):
        """GHCR's documented storage host receives no registry authorization."""
        if not isinstance(location, str) or len(location) > 8192 or any(c in location for c in '\r\n\x00'):
            raise ValueError('Registry returned an invalid blob storage redirect')
        parsed = urllib.parse.urlsplit(location)
        if (parsed.scheme != 'https' or parsed.netloc != 'pkg-containers.githubusercontent.com'
                or parsed.fragment or not parsed.path.startswith('/')):
            raise ValueError('Registry returned an unsupported blob storage redirect')
        try:
            request = urllib.request.Request(location, headers={'Accept': 'application/octet-stream'})
            return self.opener.open(request, timeout=self.timeout)
        except (urllib.error.URLError, OSError):
            raise ValueError('Registry blob storage readback is uncertain') from None

    def verify(self, row, *, tag=None, referrer_subject=None):
        """Return False only for an authenticated, recognized missing object."""
        validate_object(row)
        if referrer_subject is not None:
            if tag is not None:
                raise ValueError('An OCI object cannot have both a version and referrer tag')
            tag = referrer_tag(row, referrer_subject, self.repository)
        elif tag is not None:
            validate_tag(row, tag, repository=self.repository)
        response = self.response('blobs' if row['kind'] == 'blob' else 'manifests',
                                 row['digest'] if tag is None else tag)
        if response is None:
            return False
        with response:
            if getattr(response, 'status', 200) != 200:
                raise ValueError('Registry returned an unexpected object status')
            headers = getattr(response, 'headers', {})
            length = headers.get('Content-Length')
            if length is not None and length != str(row['size']):
                raise ValueError('Registry object size differs from the frozen payload')
            header_digest = headers.get('Docker-Content-Digest')
            if header_digest is not None and header_digest != row['digest']:
                raise ValueError('Registry object digest header differs from the frozen payload')
            if row['kind'] == 'manifest' and headers.get('Content-Type', '').split(';')[0] != row['media_type']:
                raise ValueError('Registry manifest media type differs from the frozen payload')
            digest, size = hashlib.sha256(), 0
            for data in body_chunks(response, row['size'], time.monotonic() + self.timeout):
                digest.update(data)
                size += len(data)
            if size != row['size'] or 'sha256:' + digest.hexdigest() != row['digest']:
                raise ValueError('Registry object bytes differ from the frozen payload')
        return True

    def upload(self, oras, row, path, work, *, timeout=120, tag=None, referrer=False, referrer_subject=None):
        """Preserve bytes; callers must establish tag absence and journal intent."""
        validate_object(row)
        if type(referrer) is not bool or (referrer and row['kind'] != 'manifest'):
            raise ValueError('Invalid referrer transfer mode')
        if referrer_subject is not None:
            if tag is not None or referrer:
                raise ValueError('Invalid referrer fallback transfer mode')
            tag = referrer_tag(row, referrer_subject, self.repository)
        elif tag is not None:
            validate_tag(row, tag, repository=self.repository)
        oras, path, work = Path(oras).absolute(), Path(path).absolute(), Path(work).absolute()
        for entry in (oras, path, work):
            if entry.resolve(strict=True) != entry:
                raise ValueError('OCI transfer paths must not contain links or aliases')
        if type(timeout) not in (int, float) or not 0 < timeout <= 300:
            raise ValueError('Invalid OCI upload deadline')
        deadline = time.monotonic() + timeout
        pin = decode_report(Path(__file__).with_name('tools.json').read_bytes(), 'tool pins')['oras']
        tool_size = source_stat(oras).st_size
        if private_work_bytes(work) + tool_size + row['size'] > WORK_LIMIT:
            raise ValueError('OCI upload copies would exceed the storage quota')
        with tempfile.TemporaryDirectory(prefix='oras-', dir=work) as temporary:
            directory = Path(temporary)
            executable, payload = directory / 'oras', directory / 'payload'
            verified_copy(oras, executable, tool_size, pin['executable_sha256'],
                          deadline=deadline, maximum=TOOL_LIMIT, mode=0o500)
            verified_copy(path, payload, row['size'], row['digest'].removeprefix('sha256:'), deadline=deadline)
            config = directory / 'auth.json'
            auths = {}
            if not self.fixture:
                auths[self.host] = {'auth': base64.b64encode((self.username + ':' + self.token).encode()).decode()}
            descriptor = os.open(config, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, 'w') as writer:
                json.dump({'auths': auths}, writer)
            command = [str(executable), row['kind'], 'push', '--registry-config', str(config),
                       '--media-type', row['media_type']]
            if row['kind'] == 'blob':
                command.append('--no-tty')
            if referrer:
                # Keep fallback-index writes under our journal rather than
                # allowing ORAS to perform an unrecorded tag read/modify/write.
                command += ['--distribution-spec', 'v1.1-referrers-api']
            if self.fixture:
                command.append('--plain-http')
            reference = '@' + row['digest'] if tag is None else ':' + tag
            command += [self.host + '/' + self.repository + reference, str(payload)]
            environment = {'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8', 'TZ': 'UTC',
                           'TMPDIR': str(directory), 'XDG_CACHE_HOME': str(directory),
                           'XDG_CONFIG_HOME': str(directory)}
            with (directory / 'process.log').open('wb') as log:
                if time.monotonic() >= deadline:
                    raise ValueError('OCI upload exceeded its deadline before process start')
                process = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=log, stderr=log,
                                           env=environment, cwd=directory, start_new_session=True)
                try:
                    while process.poll() is None:
                        if (time.monotonic() >= deadline or log.tell() > PROCESS_LOG_LIMIT
                                or private_work_bytes(work) > WORK_LIMIT):
                            raise ValueError('OCI upload exceeded its deadline or storage quota')
                        time.sleep(0.05)
                    if (process.returncode != 0 or log.tell() > PROCESS_LOG_LIMIT
                            or private_work_bytes(work) > WORK_LIMIT):
                        raise ValueError('OCI upload failed (exit ' + str(process.returncode)
                                         + '); reverify remote bytes before resuming')
                finally:
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    if process.poll() is None:
                        process.wait(timeout=5)


def assert_absent(repository, tag):
    if repository not in REPOSITORIES or not re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', tag):
        raise ValueError('Invalid canonical registry repository or tag')
    if not os.environ.get('GH_TOKEN') or not os.environ.get('GITHUB_ACTOR'):
        raise ValueError('Authenticated registry inspection is required before version publication')
    registry = Registry(repository, username=os.environ['GITHUB_ACTOR'], token=os.environ['GH_TOKEN'])
    response = registry.response('manifests', tag)
    if response is not None:
        with response:
            raise ValueError('Registry version already exists and must never be overwritten: '+repository+':'+tag)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest', type=Path, default=Path('dist/release/RELEASE.json'))
    args = parser.parse_args(); manifest = json.loads(args.manifest.read_text())
    assert_absent('ziad-hsn/cpra', manifest['version'])
    assert_absent('ziad-hsn/charts/cpra', manifest['chart_version'])


if __name__ == '__main__':
    main()
