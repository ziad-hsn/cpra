"""Create immutable nested-module tags, verify downloads, then tag the application.

This helper does not authorize publication. The publisher CLI and workflow stay
locked until the complete private gate and independent review are qualified.
"""
from contextlib import contextmanager
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

from evidence import decode_report
import publication_evidence
import publish
import qualification
import registry
import release
import sdk_evidence

CORE, WORKER, APP = publication_evidence.CORE, publication_evidence.WORKER, publication_evidence.APP
WORK_LIMIT = 1536 * 1024 ** 2
LOG_LIMIT = 1024 ** 2
JSON_LIMIT = 64 * 1024


def require(condition, detail):
    if not condition:
        raise ValueError('Module publication stopped: ' + detail)


def private_directory(path):
    require(os.name == 'posix', 'publication requires the Linux release runner')
    path = Path(path).absolute()
    mode = path.stat()
    require(path.resolve(strict=True) == path and path.is_dir()
            and mode.st_uid == os.getuid() and not stat.S_IMODE(mode.st_mode) & 0o077,
            'use a private owned directory without links or aliases')
    return path


def valid_tag(tag):
    if not isinstance(tag, str):
        return False
    for prefix in ('sdk/go/worker/', 'sdk/go/', ''):
        if tag.startswith(prefix) and release.valid_version(tag[len(prefix):]):
            return True
    return False


class GitHub:
    """Only GET and create-reference; never update or delete a published tag."""
    def __init__(self, token=None, *, origin='https://api.github.com',
                 allow_loopback_http=False, timeout=30):
        require(isinstance(origin, str), 'invalid GitHub API origin')
        local = re.fullmatch(r'http://127\.0\.0\.1:([1-9][0-9]{0,4})', origin)
        fixture = allow_loopback_http and local is not None and int(local[1]) <= 65535
        require(origin == 'https://api.github.com' or fixture, 'invalid GitHub API origin')
        require(type(timeout) in (int, float) and 0 < timeout <= 300, 'invalid HTTP deadline')
        require((fixture and token is None) or (not fixture and isinstance(token, str)
                and 0 < len(token) <= 4096 and re.fullmatch(r'[A-Za-z0-9._~+/-]+=*', token)),
                'explicit credentials are required and cannot be sent to fixtures')
        self.origin, self.token, self.fixture, self.timeout = origin, token, fixture, timeout
        self.opener = urllib.request.build_opener(registry.NoRedirect())
        self.commit = None

    def request(self, path, *, data=None, absent=False):
        base = '/repos/' + publish.REPOSITORY
        require(path == base or path.startswith(base + '/'), 'unexpected repository path')
        headers = {'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28',
                   'User-Agent': 'CPRa-release-publisher'}
        if self.token is not None:
            headers['Authorization'] = 'Bearer ' + self.token
        body = None if data is None else json.dumps(data, separators=(',', ':')).encode()
        if body is not None:
            headers['Content-Type'] = 'application/json'
        request = urllib.request.Request(self.origin + path, data=body, headers=headers,
                                         method='GET' if body is None else 'POST')
        try:
            with self.opener.open(request, timeout=self.timeout) as response:
                require(response.status == (200 if body is None else 201), 'unexpected GitHub status')
                value = registry.metadata(response, self.timeout)
        except urllib.error.HTTPError as error:
            with error:
                if error.code == 404 and absent and self.commit is not None:
                    value = registry.metadata(error, self.timeout)
                    require(value.get('message') == 'Not Found', 'unrecognized reference absence')
                    return None
            raise ValueError('GitHub operation could not be confirmed; inspect the same tag before resuming') from None
        except (urllib.error.URLError, OSError, http.client.HTTPException):
            raise ValueError('GitHub operation could not be confirmed; inspect the same tag before resuming') from None
        require(isinstance(value, dict), 'GitHub returned invalid metadata')
        return value

    def prepare(self, commit):
        require(isinstance(commit, str) and re.fullmatch('[a-f0-9]{40}', commit), 'invalid source commit')
        # Repository access is established before treating a reference 404 as
        # absence. Authentication failure is never interpreted as a free tag.
        base = '/repos/' + publish.REPOSITORY
        repository = self.request(base)
        require(repository.get('full_name') == publish.REPOSITORY and repository.get('private') is False,
                'canonical public repository access is not established')
        permissions = repository.get('permissions')
        require(isinstance(permissions, dict) and permissions.get('push') is True,
                'repository write permission is not established')
        source = self.request(base + '/git/commits/' + commit)
        require(source.get('sha') == commit, 'remote source commit differs')
        self.commit = commit

    def reference(self, tag):
        require(self.commit is not None and valid_tag(tag), 'invalid or unprepared reference lookup')
        result = self.request('/repos/' + publish.REPOSITORY + '/git/ref/tags/'
                              + urllib.parse.quote(tag, safe='/'), absent=True)
        if result is None:
            return None
        require(result.get('ref') == 'refs/tags/' + tag and isinstance(result.get('object'), dict),
                'reference response differs from the requested tag')
        value = result['object']
        require(value.get('type') == 'commit' and isinstance(value.get('sha'), str)
                and re.fullmatch('[a-f0-9]{40}', value['sha']), 'tag is not the original lightweight commit reference')
        return value['sha']

    def create(self, tag, commit):
        require(self.commit == commit and valid_tag(tag), 'invalid or unprepared tag creation')
        value = self.request('/repos/' + publish.REPOSITORY + '/git/refs',
                             data={'ref': 'refs/tags/' + tag, 'sha': commit})
        require(value.get('ref') == 'refs/tags/' + tag and isinstance(value.get('object'), dict)
                and value['object'].get('type') == 'commit'
                and value['object'].get('sha') == commit, 'tag creation returned a different identity')


def process(command, directory, environment, *, timeout):
    """Bound child output, storage and lifetime; never expose downloaded diagnostics."""
    require(type(timeout) in (int, float) and 0 < timeout <= 300, 'invalid process deadline')
    with (directory / 'stdout.json').open('wb') as output, (directory / 'stderr.log').open('wb') as errors:
        child = subprocess.Popen(command, cwd=directory, env=environment, stdin=subprocess.DEVNULL,
                                 stdout=output, stderr=errors, start_new_session=True)
        deadline = time.monotonic() + timeout
        try:
            while child.poll() is None:
                require(time.monotonic() < deadline and output.tell() <= JSON_LIMIT
                        and errors.tell() <= LOG_LIMIT
                        and sum(p.stat().st_size for p in directory.rglob('*') if p.is_file()) <= WORK_LIMIT,
                        'download exceeded its deadline, output or storage quota')
                time.sleep(0.05)
            require(child.returncode == 0 and output.tell() <= JSON_LIMIT and errors.tell() <= LOG_LIMIT,
                    'public module download failed; keep subsequent tags unpublished')
        finally:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            if child.poll() is None:
                child.wait(timeout=5)
    return (directory / 'stdout.json').read_bytes()


@contextmanager
def public_download(go, module, version, compiler, work, *, timeout=120):
    """A fresh public-proxy download authenticated by the ordinary Go checksum DB."""
    require(isinstance(module, str) and re.fullmatch(r'[a-z0-9.-]+(?:/[a-zA-Z0-9._-]+)+', module)
            and release.valid_version(version), 'invalid public module identity')
    go, work = Path(go).absolute(), private_directory(work)
    require(go.resolve(strict=True) == go and go.is_file() and os.access(go, os.X_OK), 'invalid Go executable')
    require(isinstance(compiler, str) and re.fullmatch(r'go[0-9]+\.[0-9]+\.[0-9]+', compiler),
            'invalid recorded compiler version')
    with tempfile.TemporaryDirectory(prefix='module-readback-', dir=work) as temporary:
        directory = Path(temporary)
        environment = {'PATH': str(go.parent), 'LANG': 'C.UTF-8', 'TZ': 'UTC', 'TMPDIR': str(directory),
                       'GOENV': 'off', 'GOWORK': 'off', 'GOTOOLCHAIN': 'local', 'GOFLAGS': '',
                       'GOEXPERIMENT': '', 'CGO_ENABLED': '0', 'GOAUTH': 'off', 'GOVCS': '*:off',
                       'GOPROXY': 'https://proxy.golang.org',
                       'GOSUMDB': 'sum.golang.org', 'GOPRIVATE': '', 'GONOPROXY': '', 'GONOSUMDB': '',
                       'GOPATH': str(directory / 'go-path'), 'GOCACHE': str(directory / 'build-cache'),
                       'GOMODCACHE': str(directory / 'module-cache')}
        selected = process([str(go), 'env', 'GOVERSION'], directory, environment, timeout=timeout).decode().strip()
        require(selected == compiler, 'selected compiler differs from the recorded release')
        (directory / 'go.mod').write_text('module cpra-release-readback\n\ngo 1.26.0\n')
        raw = process([str(go), 'mod', 'download', '-json', module + '@' + version],
                      directory, environment, timeout=timeout)
        value = decode_report(raw, 'public module download')
        require(isinstance(value, dict) and value.get('Path') == module and value.get('Version') == version
                and not value.get('Error') and not value.get('Replace'), 'download returned another module or an error')
        for field in ('Sum', 'GoModSum'):
            require(sdk_evidence.content_hash(value.get(field)), 'download omitted a valid Go content checksum')
        paths = {}
        for field, limit in (('Zip', sdk_evidence.MAX_ARCHIVE_BYTES),
                             ('GoMod', sdk_evidence.MAX_METADATA_BYTES), ('Info', JSON_LIMIT)):
            name = value.get(field)
            require(isinstance(name, str), 'download omitted its original payload paths')
            path = Path(name).absolute()
            try:
                require(path.is_relative_to(directory / 'module-cache') and path.resolve(strict=True) == path,
                        'download payload escapes its owned cache')
                sdk_evidence.hash_file(path, limit)
            except OSError:
                raise ValueError('Module download payload is missing or unavailable') from None
            paths[field] = path
        yield value, paths


def verify_download(module, row, manifest, value, paths):
    require(value.get('Path') == module and value.get('Version') == row['version']
            and value.get('Sum') == row['sum'] and value.get('GoModSum') == row['go_mod_sum'],
            'public checksums differ from the qualified module')
    require(release.sha(paths['GoMod']) == row['mod_sha256'], 'public go.mod differs from frozen source')
    info = decode_report(paths['Info'].read_bytes(), 'public module info')
    require(info.get('Version') == row['version'] and info.get('Time') == manifest['source_date'],
            'public module version or source timestamp differs')
    # Go content hashes and the complete file set are authoritative. Proxy ZIP
    # compression and metadata can legitimately differ from the private ZIP.
    files = sdk_evidence.archive_contents(paths['Zip'], module, row['version'], manifest)
    return {'module': module, 'version': row['version'], 'sum': value['Sum'],
            'go_mod_sum': value['GoModSum'], 'file_count': len(files),
            'download_sha256': {field: release.sha(path) for field, path in paths.items()}}


def marker(kind, identity):
    return {'kind': kind, 'digest': 'sha256:' + hashlib.sha256(release.encoded(identity)).hexdigest()}


def publish_modules(manifest, dependency_directory, github, reader, progress, work):
    """Resume only this graph; SDK and worker readback precede the application tag.

    Call only after the independently reviewed aggregate gate and private signed
    readback. The executable publisher currently blocks those calls completely.
    """
    manifest = decode_report(release.encoded(manifest), 'frozen module publication source')
    work = private_directory(work)
    inputs = qualification.Inputs()
    graph = publication_evidence.module_graph(dependency_directory, manifest, inputs)
    require(not work.is_relative_to(Path(dependency_directory).absolute())
            and not Path(progress).absolute().is_relative_to(Path(dependency_directory).absolute()),
            'progress and downloads cannot mutate qualified dependencies')
    identity = {'release': release.evidence_identity(manifest), 'origin': github.origin,
                'repository': publish.REPOSITORY, 'modules': graph}
    tags = {module: marker('git-ref', {'tag': row['tag'], 'commit': manifest['commit']})
            for module, row in graph.items()}
    downloads = {module: marker('go-module', {'module': module, **graph[module]}) for module in (CORE, WORKER)}
    objects = [tags[CORE], downloads[CORE], tags[WORKER], downloads[WORKER], tags[APP]]
    receipts, created, reused = [], [], []
    with publish.TransferProgress(progress, identity, objects) as journal:
        expected = [dict(kind=row['kind'], digest=row['digest'], state=state)
                    for row in objects for state in ('started', 'verified')]
        require(journal.records == expected[:len(journal.records)], 'module progress is out of order or duplicated')
        inputs.unchanged()
        github.prepare(manifest['commit'])
        # Preflight all names before making the first installable tag. An
        # unrelated existing version cannot strand a partly published graph.
        for module, row in graph.items():
            current = github.reference(row['tag'])
            state = journal.states.get((tags[module]['kind'], tags[module]['digest']))
            require(current is None or (state is not None and current == manifest['commit']),
                    'tag already exists without matching original progress, or points to another commit')
            require(current is not None or state != 'verified', 'previously verified tag is missing')
        for module in (CORE, WORKER, APP):
            row, entry = graph[module], tags[module]
            inputs.unchanged()
            for tag in created + reused:
                require(github.reference(tag) == manifest['commit'], 'previous dependency tag changed during publication')
            current = github.reference(row['tag'])
            state = journal.states.get((entry['kind'], entry['digest']))
            if current is None:
                require(state != 'verified', 'previously verified tag is missing')
                journal.reserve(entry)
                if state is None:
                    journal.record(entry, 'started')
                inputs.unchanged()
                github.create(row['tag'], manifest['commit'])
                inputs.unchanged()
                require(github.reference(row['tag']) == manifest['commit'], 'created tag readback differs')
                created.append(row['tag'])
            else:
                require(state is not None and current == manifest['commit'], 'existing tag no longer matches original progress')
                reused.append(row['tag'])
            if state != 'verified':
                journal.record(entry, 'verified')
            if module != APP:
                entry = downloads[module]
                state = journal.states.get((entry['kind'], entry['digest']))
                if state is None:
                    journal.reserve(entry)
                    journal.record(entry, 'started')
                inputs.unchanged()
                with reader(module, row['version'], work) as (value, paths):
                    receipts.append(verify_download(module, row, manifest, value, paths))
                    inputs.unchanged()
                if state != 'verified':
                    journal.record(entry, 'verified')
        inputs.unchanged()
        for tag in created + reused:
            require(github.reference(tag) == manifest['commit'], 'published tag changed before final readback')
    return {'created_tags': created, 'reused_tags': reused, 'module_readbacks': receipts,
            'release_gate_passed': False,
            'scope': 'ordered immutable tags and module readback only; no asset, alias or release publication'}
