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

def dependency_inventory(tags):
    modules = {}
    arguments = ['go', 'list']
    if tags:
        arguments += ['-tags', tags]
    arguments += ['-deps', '-json', '.', './cmd/cpractl']
    for package in json_objects(command(*arguments)):
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
    frontend, frontend_notices = dashboard_inventory()
    inventory.extend(frontend)
    notices.update(frontend_notices)
    goroot = Path(command('go', 'env', 'GOROOT').strip())
    notices['go-toolchain/LICENSE'] = (goroot / 'LICENSE').read_bytes()
    return sorted(inventory, key=lambda x: (x['ecosystem'], x['name'])), notices

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
    return sorted(inventory, key=lambda x: x['name']), notices

def source_files():
    files = set()
    for name in ['LICENSE', 'README.md', 'LICENSES/dashboard.txt', 'go.mod', 'go.sum',
                 'main.go', 'Makefile', '.dockerignore', '.gitignore', '.github/workflows/ci.yml',
                 'dashboard/.prettierrc']:
        path = ROOT / name
        if not path.is_file():
            raise RuntimeError(f'Missing release input: {name}')
        files.add(path)
    for name in ['internal', 'cmd/cpractl', 'examples', 'scripts/release', 'docker']:
        for path in (ROOT / name).rglob('*'):
            if path.is_file() and path.suffix in {'.go', '.json', '.yaml', '.yml', '.py', '.sh', '.md', '.html', '.js', '.css', '.svg'}:
                files.add(path)
    files.add(ROOT / 'docker/Dockerfile')
    for directory, children, names in os.walk(ROOT / 'dashboard'):
        children[:] = [name for name in children if name not in {'node_modules', 'dist', '.git', 'coverage'}]
        for name in names:
            path = Path(directory) / name
            if path.suffix in {'.json', '.yaml', '.ts', '.tsx', '.js', '.html', '.css', '.svg'}:
                files.add(path)
    return sorted(files)

def archive(path, entries, epoch):
    import gzip
    with path.open('wb') as output, gzip.GzipFile(filename='', mode='wb', fileobj=output, mtime=epoch) as compressed:
        with tarfile.open(fileobj=compressed, mode='w') as tar:
            for name, (contents, executable) in sorted(entries.items()):
                info = tarfile.TarInfo(name)
                info.size, info.mtime, info.mode = len(contents), epoch, 0o755 if executable else 0o644
                tar.addfile(info, io.BytesIO(contents))

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--version', required=True)
    parser.add_argument('--tags', default='')
    parser.add_argument('--out', default='dist/release')
    parser.add_argument('--bin-dir', default='bin')
    parser.add_argument('--notices-only', action='store_true')
    args = parser.parse_args()
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', args.version):
        raise SystemExit('Version must be a simple archive-safe name')
    output = ROOT / args.out
    output.mkdir(parents=True, exist_ok=True)
    epoch = int(os.environ.get('SOURCE_DATE_EPOCH', str(int(time.time()))))
    inventory, notices = dependency_inventory(args.tags)
    if args.notices_only:
        for name, contents in notices.items():
            target = output / 'THIRD_PARTY_NOTICES' / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(contents)
        (output / 'DEPENDENCIES.json').write_text(json.dumps(inventory, indent=2) + '\n')
        return
    has_git = (ROOT / '.git').exists()
    metadata = {'version': args.version, 'commit': command('git', 'rev-parse', 'HEAD').strip() if has_git else 'unknown',
                'dirty': bool(command('git', 'status', '--porcelain').strip()) if has_git else None,
                'toolchain': command('go', 'version').strip(), 'build_tags': args.tags,
                'source_date_epoch': epoch, 'dependencies': inventory}
    inventory_bytes = (json.dumps(metadata, indent=2) + '\n').encode()
    common = {'LICENSE': ((ROOT / 'LICENSE').read_bytes(), False),
              'README.md': ((ROOT / 'README.md').read_bytes(), False),
              'LICENSES/dashboard.txt': ((ROOT / 'LICENSES/dashboard.txt').read_bytes(), False),
              'DEPENDENCIES.json': (inventory_bytes, False)}
    common.update({f'THIRD_PARTY_NOTICES/{name}': (contents, False) for name, contents in notices.items()})
    artifacts = []
    for arch in ['amd64', 'arm64']:
        entries = dict(common)
        for name in ['cpra', 'cpractl']:
            entries[name] = ((ROOT / args.bin_dir / f'{name}-linux-{arch}').read_bytes(), True)
        entries['examples/monitors.yaml'] = ((ROOT / 'examples/monitors.yaml').read_bytes(), False)
        target = output / f'cpra-{args.version}-linux-{arch}.tar.gz'
        archive(target, entries, epoch)
        artifacts.append(target)
    source = dict(common)
    for path in source_files():
        source[str(path.relative_to(ROOT))] = (path.read_bytes(), path.suffix == '.sh')
    target = output / f'cpra-{args.version}-source.tar.gz'
    archive(target, source, epoch)
    artifacts.append(target)
    (output / 'DEPENDENCIES.json').write_bytes(inventory_bytes)
    checksums = ''.join(f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n' for p in artifacts)
    (output / 'SHA256SUMS').write_text(checksums)
    print(f'Packaged {len(artifacts)} archives, {len(inventory)} dependencies and {len(notices)} license/notice files in {output}')

if __name__ == '__main__':
    main()
