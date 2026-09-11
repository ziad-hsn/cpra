#!/usr/bin/env python3
"""Install pinned Linux amd64 release tools, verifying bytes before execution."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import platform
import tarfile
import urllib.request


def install(name, pin, out):
    request = urllib.request.Request(pin['url'], headers={'User-Agent':'CPRa-release-builder'})
    with urllib.request.urlopen(request, timeout=120) as response:
        data = response.read(250 * 1024 * 1024 + 1)
    if hashlib.sha256(data).hexdigest() != pin['sha256']:
        raise ValueError(f'{name}: downloaded SHA256 does not match the reviewed pin')
    if pin['member']:
        with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as archive:
            member = archive.getmember(pin['member'])
            if not member.isfile():
                raise ValueError(f'{name}: executable must be a regular archive member')
            data = archive.extractfile(member).read()
    target = out / name
    target.write_bytes(data)
    target.chmod(0o755)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, default=Path('bin/release-tools'))
    parser.add_argument('tools', nargs='*')
    args = parser.parse_args()
    if platform.system() != 'Linux' or platform.machine() not in ('x86_64','AMD64','aarch64','arm64'):
        parser.error('pinned tool downloads support designated Linux builders')
    pins = json.loads(Path(__file__).with_name('tools.json').read_text())
    args.out.mkdir(parents=True, exist_ok=True)
    for name in args.tools or pins:
        pin = pins[name]
        if platform.machine() in ('aarch64', 'arm64'):
            if 'linux_arm64' not in pin:
                parser.error(name + ': no reviewed ARM64 tool archive; select a supported tool explicitly')
            pin = pin['linux_arm64']
        install(name, pin, args.out)

if __name__ == '__main__':
    main()
