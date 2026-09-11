#!/usr/bin/env python3
"""Compare unsigned OCI payloads built without cache from two independent roots."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def inventory(path):
    files = {}
    config_digests = []
    with tarfile.open(path) as archive:
        for member in archive:
            if member.isdir():
                continue
            if not member.isfile() or member.name in files:
                raise ValueError('OCI transport contains a duplicate or non-regular payload entry')
            contents = archive.extractfile(member).read()
            value = digest(contents)
            if member.name.startswith('blobs/sha256/') and member.name.rsplit('/', 1)[1] != value:
                raise ValueError('OCI blob content does not match its digest')
            files[member.name] = {'sha256': value, 'size': len(contents)}
            if len(contents) < 1024 * 1024:
                try:
                    obj = json.loads(contents)
                    if isinstance(obj, dict) and obj.get('config', {}).get('mediaType') == 'application/vnd.oci.image.config.v1+json':
                        config_digests.append(obj['config']['digest'])
                except (ValueError, UnicodeError):
                    pass
    if 'index.json' not in files or 'oci-layout' not in files:
        raise ValueError('build output is not a complete OCI layout')
    return files, sorted(config_digests)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--release-dir', type=Path, required=True, help='Directory with linux_ARCH/cpra and RELEASE.json')
    p.add_argument('--arch', choices=['amd64', 'arm64'], required=True)
    p.add_argument('--reference-image', required=True, help='Locally loaded image used by the runtime tests')
    p.add_argument('--out', type=Path, required=True)
    args = p.parse_args()
    if args.out.exists():
        p.error('evidence already exists; use a new output path')
    args.out.parent.mkdir(parents=True, exist_ok=True)
    target = 'linux_' + args.arch
    metadata = json.loads((args.release_dir / target / 'RELEASE.json').read_text())
    report = {'status': 'incomplete', 'platform': 'linux/' + args.arch,
              'started': datetime.now(timezone.utc).isoformat(), 'source': metadata, 'builds': []}
    args.out.write_text(json.dumps(report, indent=2) + '\n')
    try:
        reference = json.loads(subprocess.check_output(['docker', 'image', 'inspect', args.reference_image]))[0]
        report['reference_image'] = {'name': args.reference_image, 'id': reference['Id']}
        with tempfile.TemporaryDirectory(prefix='cpra-image-repro-') as tmp:
            # Docker's containerd store may expose an image manifest digest as Id;
            # the classic store uses the config digest. Read the actual saved
            # config rather than assuming either representation from inspect.
            saved = Path(tmp) / 'reference.tar'
            subprocess.run(['docker', 'image', 'save', '-o', str(saved), args.reference_image], check=True, timeout=120)
            with tarfile.open(saved) as archive:
                manifest = json.load(archive.extractfile('manifest.json'))
                if len(manifest) != 1:
                    raise ValueError('reference must contain exactly one platform image')
                reference_config = 'sha256:' + digest(archive.extractfile(manifest[0]['Config']).read())
            report['reference_image']['config_digest'] = reference_config
            for number in range(2):
                root = Path(tmp) / str(number)
                (root / 'context/docker').mkdir(parents=True)
                shutil.copyfile(ROOT / 'docker/Dockerfile', root / 'context/docker/Dockerfile')
                shutil.copyfile(ROOT / 'docker/runtime.yaml', root / 'context/docker/runtime.yaml')
                shutil.copytree(args.release_dir / target, root / 'release' / target)
                output = root / 'image.tar'
                cmd = ['docker', 'buildx', 'build', '--no-cache', '--provenance=false',
                       '--platform', 'linux/' + args.arch, '--tag', 'cpra:reproducible-payload',
                       '--build-context', 'release=' + str(root / 'release'),
                       '--file', str(root / 'context/docker/Dockerfile'),
                       '--build-arg', 'VERSION=' + metadata['version'],
                       '--build-arg', 'COMMIT=' + metadata['commit'],
                       '--build-arg', 'SOURCE_DATE_EPOCH=' + str(metadata['source_date_epoch']),
                       '--output', f'type=oci,dest={output},rewrite-timestamp=true', str(root / 'context')]
                result = subprocess.run(cmd, text=True, capture_output=True, timeout=600)
                if result.returncode:
                    raise RuntimeError(result.stderr[-5000:])
                files, configs = inventory(output)
                if configs != [reference_config]:
                    raise ValueError('rebuilt OCI config does not match the image used for runtime tests')
                report['builds'].append({'oci_payload': files,
                                         'config_digests': configs,
                                         'payload_sha256': digest(json.dumps(files, sort_keys=True).encode())})
                args.out.write_text(json.dumps(report, indent=2) + '\n')
            if report['builds'][0]['oci_payload'] != report['builds'][1]['oci_payload']:
                raise ValueError('OCI layout or blob bytes differ between independent builds')
            report['status'] = 'pass'
    except Exception as exc:
        report['status'] = 'fail'
        report['error'] = str(exc)
    report['finished'] = datetime.now(timezone.utc).isoformat()
    report['boundary'] = 'OCI index, layout and all content-addressed blobs; excludes tar transport headers and signing metadata'
    args.out.write_text(json.dumps(report, indent=2) + '\n')
    print(str(args.out) + ': ' + report['status'])
    return 0 if report['status'] == 'pass' else 1


if __name__ == '__main__':
    raise SystemExit(main())
