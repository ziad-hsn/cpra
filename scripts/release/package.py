#!/usr/bin/env python3
"""Build local distribution archives from an allowlist, with dependency notices."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import struct
import tarfile
import time

ROOT = Path(__file__).resolve().parents[2]

def command(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True)

def json_objects(text):
    decoder = json.JSONDecoder()
    pos = 0
    while pos < len(text):
        while pos < len(text) and text[pos].isspace():
            pos += 1
        if pos == len(text):
            break
        value, pos = decoder.raw_decode(text, pos)
        yield value

def license_files(directory):
    return sorted(p for p in directory.iterdir() if p.is_file() and
                  re.match(r'(?i)^(licen[cs]e|copying|notice|copyright)([.\-_].*)?$', p.name))

def dependency_inventory(tags, binaries=None, go=None, env=None):
    modules = {}
    go = go or os.environ.get('GO', 'go')
    arguments = [go, 'list', '-mod=readonly']
    if tags:
        arguments += ['-tags', tags]
    arguments += ['-deps', '-json', '.', './cmd/cpractl']
    if binaries:
        for binary in binaries:
            info = json.loads(subprocess.check_output([go, 'version', '-m', '-json', str(binary)], cwd=ROOT, env=env, text=True))
            for module in info.get('Deps', []):
                module = module.get('Replace', module)
                if not module.get('Version'):
                    raise RuntimeError(f"Local module replacement is not releasable: {module['Path']}")
                resolved = json.loads(subprocess.check_output([go, 'mod', 'download', '-json', module['Path']+'@'+module['Version']], cwd=ROOT, env=env, text=True))
                modules[module['Path']] = resolved
    else:
        for package in json_objects(subprocess.check_output(arguments, cwd=ROOT, env=env, text=True)):
            module = package.get('Module', {})
            if module and not module.get('Main'):
                module = module.get('Replace', module)
                modules[module['Path']] = module
    inventory, notices = [], {}
    for name, module in sorted(modules.items()):
        directory = Path(module['Dir'])
        found = license_files(directory)
        if not found:
            raise RuntimeError(f'No dependency license file found: {name}')
        identity = f"go/{name}@{module['Version']}"
        inventory.append({'ecosystem': 'go', 'name': name, 'version': module['Version'],
                          'license_files': [p.name for p in found]})
        for path in found:
            notices[f'{identity}/{path.name}'] = path.read_bytes()
    stored = ROOT / 'LICENSES/dashboard.json'
    if stored.is_file():
        frontend = json.loads(stored.read_text())
        frontend_notices = {'dashboard/LICENSES.txt': (ROOT / 'LICENSES/dashboard.txt').read_bytes()}
    else:
        frontend, frontend_notices = dashboard_inventory()
    inventory.extend(frontend)
    notices.update(frontend_notices)
    goroot = Path(subprocess.check_output([go, 'env', 'GOROOT'], env=env, text=True).strip())
    notices['go-toolchain/LICENSE'] = (goroot / 'LICENSE').read_bytes()
    return sorted(inventory, key=lambda x: (x['ecosystem'], x['name'])), notices

def bundled_font_inventory():
    """Read identity from the actual vendored TTF, without guessing a package URL."""
    path=ROOT/'brand/fonts/RobotoSlab-Bold.ttf';data=path.read_bytes();names={}
    for index in range(struct.unpack_from('>H',data,4)[0]):
        tag,_,offset,_=struct.unpack_from('>4sIII',data,12+16*index)
        if tag!=b'name':continue
        _,count,storage=struct.unpack_from('>HHH',data,offset)
        for ordinal in range(count):
            platform,_,_,identity,length,start=struct.unpack_from('>HHHHHH',data,offset+6+12*ordinal)
            value=data[offset+storage+start:offset+storage+start+length]
            names[identity]=value.decode('utf-16-be' if platform in (0,3) else 'mac_roman')
        break
    license_url=names.get(14,'')
    if license_url not in ('http://www.apache.org/licenses/LICENSE-2.0','https://www.apache.org/licenses/LICENSE-2.0'):
        raise ValueError('Vendored font license changed; review its distribution contract')
    version=names.get(5,'')
    if not version.startswith('Version '):raise ValueError('Vendored font has no explicit version identity')
    return {'ecosystem':'font','name':names[1]+' '+names[2],'version':version.removeprefix('Version '),
            'declared_license':'Apache-2.0','license_files':['brand/fonts/LICENSE.txt'],
            'copyright':names.get(0,'NOASSERTION'),'source_identity':names[3],
            'source_file':'brand/fonts/RobotoSlab-Bold.ttf','sha256':hashlib.sha256(data).hexdigest(),
            'license_url':license_url}

def dashboard_inventory():
    inventory, notices = [], {}
    # Resolve the installed production dependency graph using Node's parent
    # node_modules lookup. Paths are dereferenced for pnpm's linked store.
    def resolve(directory, name):
        for parent in (directory, *directory.parents):
            candidate = parent / 'node_modules' / name / 'package.json'
            if candidate.is_file():
                return candidate.resolve().parent
        raise RuntimeError(f'Installed JS dependency not found: {name}')
    pending = [(ROOT / 'dashboard', name, False) for name in
               json.loads((ROOT / 'dashboard/package.json').read_text())['dependencies']]
    seen = set()
    while pending:
        parent, name, optional = pending.pop()
        try:
            directory = resolve(parent, name)
        except RuntimeError:
            if optional:
                continue
            raise
        package = json.loads((directory / 'package.json').read_text())
        identity = f"npm/{package['name']}@{package['version']}"
        if identity in seen:
            continue
        seen.add(identity)
        found = license_files(directory)
        if not found:
            raise RuntimeError(f'No dependency license file found: {identity}')
        inventory.append({'ecosystem': 'npm', 'name': package['name'], 'version': package['version'],
                          'declared_license': package.get('license', 'NOASSERTION'),
                          'license_files': [p.name for p in found]})
        for path in found:
            notices[f'{identity}/{path.name}'] = path.read_bytes()
        optional_names = package.get('optionalDependencies', {})
        for name in package.get('dependencies', {}) | optional_names:
            pending.append((directory, name, name in optional_names))
    inventory.append(bundled_font_inventory())
    notices['fonts/RobotoSlab/LICENSE.txt'] = (ROOT / 'brand/fonts/LICENSE.txt').read_bytes()
    return sorted(inventory, key=lambda x: x['name']), notices

def archive(path, entries, epoch):
    import gzip
    with path.open('wb') as output, gzip.GzipFile(filename='', mode='wb', fileobj=output, mtime=epoch) as compressed:
        with tarfile.open(fileobj=compressed, mode='w') as tar:
            for name, (contents, executable) in sorted(entries.items()):
                info = tarfile.TarInfo(name)
                info.size, info.mtime, info.mode = len(contents), epoch, 0o755 if executable else 0o644
                tar.addfile(info, io.BytesIO(contents))

def main():
    parser = argparse.ArgumentParser(description='Collect dependency notices; use release.py for release archives')
    parser.add_argument('--version', default='dev')
    parser.add_argument('--tags', default='')
    parser.add_argument('--out', default='dist/release')
    parser.add_argument('--notices-only', action='store_true', required=True)
    args = parser.parse_args()
    output = ROOT / args.out
    output.mkdir(parents=True, exist_ok=True)
    inventory, notices = dependency_inventory(args.tags)
    for name, contents in notices.items():
        target = output / 'THIRD_PARTY_NOTICES' / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(contents)
    (output / 'DEPENDENCIES.json').write_text(json.dumps(inventory, indent=2) + '\n')

if __name__ == '__main__':
    main()
