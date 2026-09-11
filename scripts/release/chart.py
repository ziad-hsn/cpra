#!/usr/bin/env python3
"""Stage the versioned chart against a tested OCI digest, normalizing tar metadata."""
import argparse
import json
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import yaml
from release import ROOT, encoded, checksums
from package import archive


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, default=ROOT/'dist/release')
    parser.add_argument('--helm', required=True)
    parser.add_argument('--image-digest', required=True)
    args = parser.parse_args(); args.out = args.out.resolve()
    if not re.fullmatch(r'sha256:[a-f0-9]{64}', args.image_digest):
        parser.error('image digest must identify the tested immutable OCI index')
    manifest = json.loads((args.out/'RELEASE.json').read_text())
    with tempfile.TemporaryDirectory(prefix='cpra-chart-release-') as temp:
        root = Path(temp); chart = root/'cpra'; shutil.copytree(ROOT/'charts/cpra', chart)
        metadata = yaml.safe_load((chart/'Chart.yaml').read_text())
        metadata.update(version=manifest['chart_version'], appVersion=manifest['version'])
        metadata.setdefault('annotations', {})['cpra.io/source-commit'] = manifest['commit']
        (chart/'Chart.yaml').write_text(yaml.safe_dump(metadata, sort_keys=False))
        values = yaml.safe_load((chart/'values.yaml').read_text())
        values['image'].update(tag='', digest=args.image_digest)
        (chart/'values.yaml').write_text(yaml.safe_dump(values, sort_keys=False))
        fixture = ['--set', 'auth.existingSecret=release-test-v1', '--set', 'manifest.existingConfigMap=release-test-v1']
        subprocess.run([args.helm, 'lint', '--strict', str(chart), *fixture], check=True)
        subprocess.run([args.helm, 'package', str(chart), '--destination', str(root)], check=True)
        entries = {}
        with tarfile.open(root/f"cpra-{manifest['chart_version']}.tgz") as stream:
            for member in stream:
                if member.isfile(): entries[member.name] = (stream.extractfile(member).read(), False)
        archive(args.out/f"cpra-chart-{manifest['chart_version']}.tgz", entries, manifest['source_date_epoch'])
    checksums(args.out)


if __name__ == '__main__':
    main()
