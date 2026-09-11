#!/usr/bin/env python3
"""Compare unsigned payloads from the checkout and two independent source extractions."""
import argparse
import json
import os
import shutil
import sys
from pathlib import Path
import subprocess
import tarfile
import tempfile
from release import RECIPE, encoded, sha


def compare(reference, rebuilt, targets, version, packages):
    hashes = {}
    for target in targets:
        suffix = '.exe' if target.startswith('windows_') else ''
        for binary in RECIPE['binaries']:
            relative = target + '/' + binary + suffix
            if sha(reference/relative) != sha(rebuilt/relative):
                raise RuntimeError('No-Git source rebuild differs for ' + relative)
            hashes[relative] = sha(rebuilt/relative)
        relative = f"cpra-{version}-{target.replace('_', '-')}" + ('.zip' if suffix else '.tar.gz')
        if sha(reference/relative) != sha(rebuilt/relative):
            raise RuntimeError('Unsigned distribution payload differs for ' + relative)
        hashes[relative] = sha(rebuilt/relative)
    compose = f'cpra-{version}-compose.tar.gz'
    if sha(reference/compose) != sha(rebuilt/compose):
        raise RuntimeError('Compose deployment payload differs')
    hashes[compose] = sha(rebuilt/compose)
    if packages:
        for arch in ('amd64', 'arm64'):
            for format in ('deb', 'rpm'):
                relative = f'cpra-{version}-linux-{arch}.{format}'
                if sha(reference/relative) != sha(rebuilt/relative):
                    raise RuntimeError('Unsigned package differs for ' + relative)
                hashes[relative] = sha(rebuilt/relative)
    return hashes


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--stage', type=Path, required=True, help='complete reference release directory, not a target subdirectory')
    parser.add_argument('--go', required=True)
    parser.add_argument('--nfpm', help='also reproduce all DEB/RPM packages with this pinned nFPM executable')
    parser.add_argument('--targets', nargs='+', choices=RECIPE['targets'], default=RECIPE['targets'])
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    args.source = args.source.resolve(); args.stage = args.stage.resolve()
    args.go = str(Path(shutil.which(args.go) or args.go).resolve())
    if args.nfpm: args.nfpm = str(Path(shutil.which(args.nfpm) or args.nfpm).resolve())
    manifest = json.loads((args.stage/'RELEASE.json').read_text())
    if args.nfpm and not {'linux_amd64', 'linux_arm64'}.issubset(args.targets):
        parser.error('Package comparison requires both Linux targets')
    reports = []
    for repetition in range(2):
        with tempfile.TemporaryDirectory(prefix=f'cpra-source-rebuild-{repetition}-') as temp:
            root = Path(temp)/'source'; root.mkdir()
            rebuilt = Path(temp)/'rebuilt'
            tools = Path(temp)/'tools'; tools.mkdir()
            (tools/'python3').symlink_to(sys.executable)
            isolated_env = dict(os.environ, PATH=str(tools)+os.pathsep+str(Path(args.go).parent))
            for unavailable in ('git', 'node', 'pnpm'):
                if shutil.which(unavailable, path=isolated_env['PATH']):
                    raise RuntimeError('Source rebuild unexpectedly exposes '+unavailable)
            with tarfile.open(args.source) as archive:
                archive.extractall(root, filter='data')
            if (root/'.git').exists():
                raise RuntimeError('Source archive unexpectedly includes Git metadata')
            subprocess.run(['python3', '-B', 'scripts/release/release.py', 'build', '--manifest', 'RELEASE.json',
                            '--go', args.go, '--out', str(rebuilt), '--targets', *args.targets], cwd=root, env=isolated_env, check=True)
            subprocess.run(['python3', '-B', 'scripts/release/release.py', 'pack', '--manifest', 'RELEASE.json',
                            '--out', str(rebuilt), '--no-source', '--targets', *args.targets], cwd=root, env=isolated_env, check=True)
            if args.nfpm:
                subprocess.run(['python3', '-B', 'scripts/release/linux_packages.py', '--out', str(rebuilt),
                                '--nfpm', str(Path(args.nfpm).resolve())], cwd=root, env=isolated_env, check=True)
            reports.append(compare(args.stage, rebuilt, args.targets, manifest['version'], bool(args.nfpm)))
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_bytes(encoded({'status': 'pass', 'commit': manifest['commit'], 'version': manifest['version'],
        'source_sha256': sha(args.source), 'recipe_sha256': sha(Path(__file__).with_name('recipe.json')),
        'excluded_tools': ['git', 'node', 'pnpm'],
        'scope': 'checkout plus two independent extracted source roots; no Git/Node generation in either extraction',
        'unsigned_payloads': reports, 'packages_compared': bool(args.nfpm)}))
    print('Unsigned executables and selected distribution payloads match in all three build roots')


if __name__ == '__main__':
    main()
