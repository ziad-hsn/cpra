#!/usr/bin/env python3
"""Install a checksum-pinned official Go distribution without toolchain switching."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import tarfile
import tempfile
import urllib.request
import zipfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', default='go1.27.1')
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    pins = json.loads(Path(__file__).with_name('toolchains.json').read_text())
    systems = {'Linux': 'linux', 'Darwin': 'darwin', 'Windows': 'windows'}
    arches = {'x86_64': 'amd64', 'AMD64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64', 'ARM64': 'arm64'}
    target = systems.get(platform.system(), '') + '_' + arches.get(platform.machine(), '')
    if args.version not in pins or target not in pins[args.version]:
        parser.error('No reviewed compiler checksum for this version and native platform')
    destination = args.out.resolve()
    if destination.exists():
        parser.error('Refusing to replace an existing toolchain; select a new output directory')
    destination.parent.mkdir(parents=True, exist_ok=True)
    suffix = '.zip' if target.startswith('windows_') else '.tar.gz'
    url = 'https://go.dev/dl/' + args.version + '.' + target.replace('_', '-') + suffix
    with tempfile.TemporaryDirectory(prefix='cpra-go-', dir=destination.parent) as temp:
        staging = Path(temp)
        archive = staging / ('download' + suffix)
        digest = hashlib.sha256()
        with urllib.request.urlopen(url, timeout=120) as response, archive.open('wb') as output:
            for chunk in iter(lambda: response.read(1024 * 1024), b''):
                digest.update(chunk)
                output.write(chunk)
        if digest.hexdigest() != pins[args.version][target]:
            raise ValueError('Downloaded compiler SHA256 does not match the reviewed pin')
        if suffix == '.zip':
            with zipfile.ZipFile(archive) as stream:
                stream.extractall(staging)
        else:
            with tarfile.open(archive) as stream:
                stream.extractall(staging, filter='data')
        shutil.move(str(staging / 'go'), destination)
    print(destination / 'bin')
    if os.environ.get('GITHUB_PATH'):
        with open(os.environ['GITHUB_PATH'], 'a') as stream:
            stream.write(str(destination / 'bin') + '\n')


if __name__ == '__main__':
    main()
