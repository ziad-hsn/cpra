#!/usr/bin/env python3
"""Inventory tested container OS packages, Go programs and the embedded dashboard."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
from release import ROOT, checksums, encoded


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, default=ROOT/'dist/release')
    parser.add_argument('--syft', required=True)
    args = parser.parse_args(); args.out = args.out.resolve()
    manifest = json.loads((args.out/'RELEASE.json').read_text())
    frontend = json.loads((ROOT/'LICENSES/dashboard.json').read_text())
    for arch in ('amd64', 'arm64'):
        destination = args.out/f'cpra-image-linux-{arch}.spdx.json'
        subprocess.run([args.syft, 'scan', 'docker:cpra:tested-'+arch, '-o', 'spdx-json='+str(destination)], check=True)
        document = json.loads(destination.read_text())
        document['creationInfo']['created'] = manifest['source_date']
        document['documentNamespace'] = 'https://github.com/ziad-hsn/cpra/spdx/'+manifest['commit']+'/image/linux-'+arch
        for dependency in frontend:
            identity = dependency['name']+'@'+dependency['version']
            spdx = 'SPDXRef-npm-'+hashlib.sha256(identity.encode()).hexdigest()[:24]
            document.setdefault('packages', []).append({'SPDXID': spdx, 'name': dependency['name'],
                'versionInfo': dependency['version'], 'downloadLocation': 'NOASSERTION', 'filesAnalyzed': False,
                'licenseConcluded': 'NOASSERTION', 'licenseDeclared': dependency.get('declared_license', 'NOASSERTION'),
                'copyrightText': 'NOASSERTION', 'comment': 'Embedded dashboard; license texts accompany the image.'})
            document.setdefault('relationships', []).append({'spdxElementId': document['SPDXID'], 'relationshipType': 'DESCRIBES', 'relatedSpdxElement': spdx})
        destination.write_bytes(encoded(document))
    checksums(args.out)


if __name__ == '__main__':
    main()
