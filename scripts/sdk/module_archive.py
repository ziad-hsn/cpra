"""Shared Go module ZIP staging for private SDK and application verification."""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[2]
MODULE = 'github.com/ziad-hsn/cpra'
CORE = MODULE + '/sdk/go'
WORKER = CORE + '/worker'
MAX_GO_MOD = 16 * 1024 ** 2


def sha256(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def tool_environment(go, scratch):
    keep = ('HOME', 'USERPROFILE', 'SystemRoot', 'SYSTEMROOT', 'WINDIR', 'TMP', 'TEMP', 'TMPDIR')
    env = {key: os.environ[key] for key in keep if key in os.environ}
    env.update(PATH=str(Path(go).parent), GOTOOLCHAIN='local', GOENV='off', GOWORK='off',
               GOFLAGS='', GOEXPERIMENT='', CGO_ENABLED='0', GOSUMDB='sum.golang.org',
               GOPROXY='https://proxy.golang.org', GOPRIVATE='', GONOPROXY='', GONOSUMDB='',
               GOPATH=str(scratch / 'go-path'), GOCACHE=str(scratch / 'build-cache'),
               GOMODCACHE=str(scratch / 'module-cache'))
    return env


def build_archiver(go, scratch, source_root=ROOT):
    scratch = Path(scratch).resolve()
    scratch.mkdir(parents=True, exist_ok=True)
    executable = scratch / ('modulezip.exe' if os.name == 'nt' else 'modulezip')
    if executable.exists():
        raise ValueError('Select a new directory for the module archive tool')
    env = tool_environment(go, scratch)
    source = source_root / 'tools/modulezip'
    before = {name: sha256(source / name) for name in ('go.mod', 'go.sum', 'main.go')}
    subprocess.run([str(go), 'build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-p=2',
                    '-o', str(executable), '.'], cwd=source, env=env, check=True)
    if before != {name: sha256(source / name) for name in before}:
        raise ValueError('Module archive tool sources changed during compilation')
    return executable


def committed_source(root, destination):
    """Freeze only committed files; never filter file extensions or credentials."""
    root, destination = Path(root).resolve(), Path(destination).resolve()
    status = subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=all'], cwd=root)
    if status:
        raise ValueError('Module archive verification requires clean committed source')
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
    epoch = int(subprocess.check_output(['git', 'show', '-s', '--format=%ct', commit], cwd=root, text=True))
    destination.mkdir()
    with tempfile.TemporaryFile() as archive:
        subprocess.run(['git', 'archive', '--format=tar', commit], cwd=root, stdout=archive, check=True)
        archive.seek(0)
        with tarfile.open(fileobj=archive) as stream:
            stream.extractall(destination, filter='data')
    return commit, epoch


def read_go_mod(directory):
    with (Path(directory) / 'go.mod').open('rb') as source:
        data = source.read(MAX_GO_MOD + 1)
    if len(data) > MAX_GO_MOD:
        raise ValueError('go.mod exceeds the Go module size limit')
    return data


def stage_module(archiver, proxy, module, version, directory, source_time):
    if module not in (MODULE, CORE, WORKER):
        raise ValueError('Unexpected CPRa module path')
    if not isinstance(version, str) or not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?', version):
        raise ValueError('A canonical module version is required')
    destination = Path(proxy) / module / '@v'
    destination.mkdir(parents=True, exist_ok=True)
    paths = [destination / (version + suffix) for suffix in ('.zip', '.mod', '.info')]
    if any(path.exists() for path in paths):
        raise ValueError('Refusing to replace a staged module version')
    data = read_go_mod(directory)
    with tempfile.TemporaryDirectory(prefix='module-', dir=Path(proxy)) as temporary:
        archive = Path(temporary) / 'module.zip'
        raw = subprocess.check_output([str(archiver), '-source', str(Path(directory).resolve()),
                                       '-module', module, '-version', version, '-out', str(archive)], text=True)
        record = json.loads(raw)
        if not isinstance(record, dict) or record.get('Path') != module or record.get('Version') != version \
                or record.get('ArchiveSHA256') != sha256(archive):
            raise ValueError('Module archive tool returned a mismatched identity')
        for name in ('Sum', 'GoModSum'):
            value = record.get(name)
            if not isinstance(value, str) or not value.startswith('h1:'):
                raise ValueError('Module archive tool omitted a content hash')
            try:
                valid = len(base64.b64decode(value[3:], validate=True)) == 32
            except ValueError:
                valid = False
            if not valid:
                raise ValueError('Module archive tool returned an invalid content hash')
        if not isinstance(record.get('Requires'), dict) or any(not isinstance(k, str) or not isinstance(v, str)
                                                              for k, v in record['Requires'].items()):
            raise ValueError('Module archive tool omitted dependency identities')
        if data != read_go_mod(directory):
            raise ValueError('go.mod changed during module archive creation')
        with zipfile.ZipFile(archive) as contents:
            name = module + '@' + version + '/go.mod'
            matches = [entry for entry in contents.infolist() if entry.filename == name]
            if len(matches) != 1 or matches[0].file_size > MAX_GO_MOD:
                raise ValueError('Module archive must contain one bounded go.mod')
            with contents.open(matches[0]) as source:
                archived = source.read(MAX_GO_MOD + 1)
            if archived != data:
                raise ValueError('Archived go.mod differs from the selected source')
        # No go.mod/go.sum file is rewritten and no checksum is invented here.
        # Sum and GoModSum are calculated by x/mod over the selected file bytes.
        info = json.dumps({'Version': version, 'Time': source_time}, sort_keys=True).encode() + b'\n'
        with paths[0].open('xb') as output, archive.open('rb') as source:
            for block in iter(lambda: source.read(1 << 20), b''):
                output.write(block)
        with paths[1].open('xb') as output:
            output.write(data)
        with paths[2].open('xb') as output:
            output.write(info)
    return record
