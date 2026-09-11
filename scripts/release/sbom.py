#!/usr/bin/env python3
"""Generate target SPDX SBOMs, including the embedded dashboard's explicit inventory."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
from release import ROOT, RECIPE, encoded, checksums


def append_frontend(document, dependencies):
    """Include embedded packages and vendored assets using their observed identity."""
    for dependency in dependencies:
        ecosystem=dependency['ecosystem']
        if ecosystem not in ('npm','font'):continue
        identity=dependency['name']+'@'+dependency.get('version','')+'#'+dependency.get('sha256','')
        spdx='SPDXRef-'+ecosystem+'-'+hashlib.sha256(identity.encode()).hexdigest()[:24]
        package={'SPDXID':spdx,'name':dependency['name'],'downloadLocation':'NOASSERTION','filesAnalyzed':False,
            'licenseConcluded':'NOASSERTION','licenseDeclared':dependency.get('declared_license','NOASSERTION'),
            'copyrightText':dependency.get('copyright','NOASSERTION'),
            'comment':'Embedded dashboard dependency; license texts are distributed in LICENSES/dashboard.txt.'}
        if dependency.get('version'):package['versionInfo']=dependency['version']
        if dependency.get('sha256'):
            package['checksums']=[{'algorithm':'SHA256','checksumValue':dependency['sha256']}]
            package['sourceInfo']='Vendored '+dependency['source_file']+'; embedded identity '+dependency['source_identity']
        document.setdefault('packages',[]).append(package)
        document.setdefault('relationships',[]).append({'spdxElementId':document['SPDXID'],'relationshipType':'DESCRIBES','relatedSpdxElement':spdx})


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
        append_frontend(document,inventory['dependencies'])
        destination.write_bytes(encoded(document))
    checksums(args.out)

if __name__=='__main__':main()
