#!/usr/bin/env python3
"""Exercise versioned go install from a source module proxy outside any checkout.

This proves the source-package contract before its stable Git tag exists. Public
proxy resolution is checked separately after publication; this test never creates
or moves a Git tag and never needs Node, pnpm or Git during installation.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import zipfile
from release import RECIPE, encoded, sha


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--go', required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    go = str(Path(shutil.which(args.go) or args.go).resolve())
    with tempfile.TemporaryDirectory(prefix='cpra-go-install-') as temporary:
        root = Path(temporary); work = root/'outside-checkout'; work.mkdir()
        with tarfile.open(args.source) as archive:
            manifest = json.load(archive.extractfile('RELEASE.json'))
            version = manifest['version']; module = RECIPE['module']
            proxy = root/'proxy'/module/'@v'; proxy.mkdir(parents=True)
            (proxy/(version+'.info')).write_bytes(encoded({'Version': version, 'Time': manifest['source_date']}))
            (proxy/(version+'.mod')).write_bytes(archive.extractfile('go.mod').read())
            (proxy/'list').write_text(version+'\n')
            with zipfile.ZipFile(proxy/(version+'.zip'), 'w', zipfile.ZIP_DEFLATED) as destination:
                for member in archive:
                    if member.isfile() and not member.name.startswith(('.git/', 'vendor/', 'node_modules/')):
                        destination.writestr(module+'@'+version+'/'+member.name, archive.extractfile(member).read())
        # Restrict executable discovery to the selected Go distribution. Its
        # pure-Go compiler uses its own tool directory; no Git or frontend tool
        # can accidentally make these versioned installs work.
        env = {key: os.environ[key] for key in ('HOME', 'USERPROFILE', 'SystemRoot', 'SYSTEMROOT', 'TMP', 'TEMP', 'TMPDIR', 'GOCACHE') if key in os.environ}
        env.update(GOMODCACHE=str(root/'mod-cache'), GOPATH=str(root/'gopath'), GOENV='off', GOWORK='off', GOTOOLCHAIN='local', CGO_ENABLED='0', GOFLAGS='', GOEXPERIMENT='',
                   GOPROXY=(root/'proxy').as_uri()+',https://proxy.golang.org', GOSUMDB='sum.golang.org',
                   GONOSUMDB=module, GONOPROXY='', GOPRIVATE='', PATH=str(Path(go).parent), GOBIN=str(root/'bin'))
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
        'scope': 'native versioned installation from a source-derived local module proxy; no Git, Node or pnpm on PATH; public proxy not exercised',
        'installations': results}))


if __name__ == '__main__':
    main()
