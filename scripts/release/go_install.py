#!/usr/bin/env python3
"""Exercise versioned go install from a source module proxy outside any checkout.

This proves the source-package contract before its stable Git tag exists. Public
proxy resolution is checked separately after publication; this test never creates
or moves a Git tag and never needs Node, pnpm or Git during installation.
"""
import argparse
import hashlib
import json
import os
import platform
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys
import tarfile
import tempfile
from release import RECIPE, encoded, evidence_identity, sha

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'sdk'))
from module_archive import CORE, build_archiver, stage_module


def extract_release_source(path, source):
    """Verify the source inventory and omit generated RELEASE.json from modules."""
    with tarfile.open(path) as archive:
        metadata = archive.getmember('RELEASE.json')
        if not metadata.isfile() or metadata.size > 16 * 1024 ** 2:
            raise ValueError('Release metadata must be a bounded regular file')
        manifest = json.load(archive.extractfile(metadata))
        identity = evidence_identity(manifest)
        if manifest['recipe'] != RECIPE:
            raise ValueError('Source archive uses a different release recipe')
        inventory = manifest['source_files']
        seen = set()
        members = []
        for member in archive.getmembers():
            name = member.name.rstrip('/') if member.isdir() else member.name
            if name in seen or not name or PurePosixPath(name).is_absolute() \
                    or '..' in PurePosixPath(name).parts or str(PurePosixPath(name)) != name or '\\' in name:
                raise ValueError('Duplicate or unsafe release archive path')
            seen.add(name)
            if member.isdir() or name == 'RELEASE.json':
                continue
            record = inventory.get(name)
            if record is None:
                raise ValueError('Release archive contains an unrecorded source file: ' + name)
            if (record['mode'] == '120000' and not member.issym()) \
                    or (record['mode'] != '120000' and not member.isfile()):
                raise ValueError('Release source kind does not match its inventory: ' + name)
            if member.isfile() and bool(member.mode & 0o111) != (record['mode'] == '100755'):
                raise ValueError('Release source executable mode differs from its inventory: ' + name)
            members.append(member)
        if not set(inventory).issubset(seen):
            raise ValueError('Release archive is missing recorded source files')
        source.mkdir()
        archive.extractall(source, members=members, filter='data')
    for name, record in inventory.items():
        path = source / name
        if record['mode'] == '120000':
            digest = hashlib.sha256(os.readlink(path).encode()).hexdigest()
        else:
            if path.is_symlink() or not path.is_file():
                raise ValueError('Release source is not the recorded regular file: ' + name)
            digest = sha(path)
        if digest != record['sha256']:
            raise ValueError('Release source bytes differ from their recorded inventory: ' + name)
    return manifest, identity


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--go', required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    go = str(Path(shutil.which(args.go) or args.go).resolve())
    with tempfile.TemporaryDirectory(prefix='cpra-go-install-') as temporary:
        root = Path(temporary); work = root/'outside-checkout'; work.mkdir()
        source = root / 'source'
        manifest, identity_record = extract_release_source(args.source, source)
        version = manifest['version']; module = RECIPE['module']
        archiver = build_archiver(go, root / 'archive-tool', source)
        proxy = root / 'proxy'
        application = stage_module(archiver, proxy, module, version, source, manifest['source_date'])
        sdk_version = application['Requires'].get(CORE)
        if not sdk_version:
            raise ValueError('Application source does not pin the SDK module version')
        sdk = stage_module(archiver, proxy, CORE, sdk_version, source / 'sdk/go', manifest['source_date'])
        # Restrict executable discovery to the selected Go distribution. Its
        # pure-Go compiler uses its own tool directory; no Git or frontend tool
        # can accidentally make these versioned installs work.
        env = {key: os.environ[key] for key in ('HOME', 'USERPROFILE', 'SystemRoot', 'SYSTEMROOT', 'TMP', 'TEMP', 'TMPDIR') if key in os.environ}
        env.update(GOMODCACHE=str(root/'mod-cache'), GOPATH=str(root/'gopath'), GOENV='off', GOWORK='off', GOTOOLCHAIN='local', CGO_ENABLED='0', GOFLAGS='', GOEXPERIMENT='',
                   GOPROXY=(root/'proxy').as_uri()+',https://proxy.golang.org', GOSUMDB='sum.golang.org',
                   GONOSUMDB=module, GONOPROXY='', GOPRIVATE='', GOVCS='*:off',
                   PATH=str(Path(go).parent), GOBIN=str(root/'bin'), GOCACHE=str(root/'build-cache'))
        results = []
        for tags in ('', ' '.join(RECIPE['build_tags'])):
            for package, name in ((module, 'cpra'), (module+'/cmd/cpractl', 'cpractl')):
                subprocess.run([go, 'install', '-p=2', '-tags', tags, package+'@'+version], cwd=work, env=env, check=True)
                executable = root/'bin'/(name+('.exe' if os.name=='nt' else ''))
                output = subprocess.check_output([str(executable), '-version' if name=='cpra' else '--version'], cwd=work, env=env, text=True)
                if version not in output:
                    raise RuntimeError('go install did not expose its actual module version')
                results.append({'command': package+'@'+version, 'tags': tags, 'identity': output.strip(), 'sha256': sha(executable)})
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_bytes(encoded({'status': 'pass', 'commit': manifest['commit'], 'version': version,
        'release_identity': identity_record,
        'os': platform.system(), 'architecture': platform.machine(), 'os_version': platform.platform(),
        'scope': 'native versioned installation from a source-derived local module proxy; no Git, Node or pnpm on PATH; public proxy not exercised',
        'module_archive_tool': 'golang.org/x/mod/zip@v0.38.0',
        'modules': [application, sdk],
        'installations': results}))


if __name__ == '__main__':
    main()
