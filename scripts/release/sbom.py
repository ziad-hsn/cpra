#!/usr/bin/env python3
"""Generate target SPDX SBOMs, including the embedded dashboard's explicit inventory."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
from release import ROOT, RECIPE, encoded, checksums


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out',type=Path,default=ROOT/'dist/release')
    parser.add_argument('--syft',default='syft')
    args=parser.parse_args();args.out=args.out.resolve()
    for target in RECIPE['targets']:
        directory=args.out/target
        destination=args.out/f'cpra-{target}.spdx.json'
        subprocess.run([args.syft,'scan','dir:'+str(directory),'-o','spdx-json='+str(destination)],check=True)
        document=json.loads(destination.read_text())
        manifest=json.loads((directory/'RELEASE.json').read_text())
        document['creationInfo']['created']=manifest['source_date']
        document['documentNamespace']='https://github.com/ziad-hsn/cpra/spdx/'+manifest['commit']+'/'+target
        inventory=json.loads((directory/'DEPENDENCIES.json').read_text())
        for dependency in inventory['dependencies']:
            if dependency['ecosystem']!='npm':continue
            identity=dependency['name']+'@'+dependency['version']
            spdx='SPDXRef-npm-'+hashlib.sha256(identity.encode()).hexdigest()[:24]
            document.setdefault('packages',[]).append({'SPDXID':spdx,'name':dependency['name'],
                'versionInfo':dependency['version'],'downloadLocation':'NOASSERTION','filesAnalyzed':False,
                'licenseConcluded':'NOASSERTION','licenseDeclared':dependency.get('declared_license','NOASSERTION'),
                'copyrightText':'NOASSERTION','comment':'Embedded dashboard dependency; license texts are distributed in LICENSES/dashboard.txt.'})
            document.setdefault('relationships',[]).append({'spdxElementId':document['SPDXID'],'relationshipType':'DESCRIBES','relatedSpdxElement':spdx})
        destination.write_bytes(encoded(document))
    checksums(args.out)

if __name__=='__main__':main()
