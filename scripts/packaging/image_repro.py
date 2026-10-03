#!/usr/bin/env python3
"""Compare unsigned OCI payloads built without cache from two independent roots."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts/release'))
from release import evidence_identity
from oci import copy_transport, docker_config_digest, read_image


def digest(data):
    return hashlib.sha256(data).hexdigest()


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
              'started': datetime.now(timezone.utc).isoformat(), 'source': metadata, 'builds': [],
              'release_identity': evidence_identity(metadata)}
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
            retained = args.out.parent / (args.out.stem + '-reference.docker.tar')
            report['reference_image']['archive'] = copy_transport(saved, retained)
            args.out.write_text(json.dumps(report, indent=2) + '\n')
            reference_config = docker_config_digest(retained, report['platform'])
            report['reference_image']['config_digest'] = reference_config
            for number in range(2):
                root = Path(tmp) / str(number)
                (root / 'context/docker').mkdir(parents=True)
                shutil.copyfile(ROOT / 'docker/Dockerfile', root / 'context/docker/Dockerfile')
                shutil.copyfile(ROOT / 'docker/runtime.yaml', root / 'context/docker/runtime.yaml')
                shutil.copytree(args.release_dir / target, root / 'release' / target)
                # BuildKit's timestamp rewriting alone is only an upper bound.
                # Prove older and newer checkout/extraction timestamps both
                # normalize to the actual tested payload, including directories.
                input_mtime = max(0, int(metadata['source_date_epoch']) + (-86400 if number == 0 else 86400))
                for tree in (root / 'context', root / 'release'):
                    for path in [*tree.rglob('*'), tree]:
                        os.utime(path, (input_mtime, input_mtime), follow_symlinks=False)
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
                retained = args.out.parent / (args.out.stem + f'-build-{number}.oci.tar')
                transport = copy_transport(output, retained)
                report['builds'].append({'archive': transport, 'input_mtime': input_mtime})
                args.out.write_text(json.dumps(report, indent=2) + '\n')
                image = read_image(retained, report['platform'])
                files = image['oci_payload']
                if image['config_digest'] != reference_config:
                    raise ValueError('rebuilt OCI config does not match the image used for runtime tests')
                report['builds'][-1].update(oci_payload=files,
                    config_digests=[image['config_digest']], manifest_digest=image['manifest_digest'],
                    expanded_bytes=image['expanded_bytes'],
                    payload_sha256=digest(json.dumps(files, sort_keys=True).encode()))
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
