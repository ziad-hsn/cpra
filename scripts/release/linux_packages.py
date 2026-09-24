#!/usr/bin/env python3
"""Create DEB/RPM packages from the exact staged release binaries using nFPM OSS."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
from release import ROOT, RECIPE, check_manifest, checksums, encoded, package_version


def version_fields(version, format):
    if format not in ('deb', 'rpm'):
        raise ValueError('Unsupported package format')
    return package_version(version)


def config(manifest, out, target, format):
    stage=out/target
    contents=[]
    for name in ('cpra','cpractl'):
        contents.append({'src':str(stage/name),'dst':'/usr/bin/'+name,'file_info':{'mode':0o755}})
    for name in ('runtime.yaml','monitors.yaml'):
        contents.append({'src':str(ROOT/'packaging/linux'/name),'dst':'/etc/cpra/'+name,'type':'config|noreplace',
                         'file_info':{'mode':0o640,'owner':'root','group':'cpra'}})
    contents.append({'src':str(ROOT/'packaging/linux/cpra.service'),'dst':'/usr/lib/systemd/system/cpra.service'})
    for name in ('LICENSE','LICENSES','THIRD_PARTY_NOTICES','DEPENDENCIES.json','RELEASE.json'):
        path=stage/name
        for item in sorted(path.rglob('*')) if path.is_dir() else [path]:
            if item.is_file():
                contents.append({'src':str(item),'dst':'/usr/share/doc/cpra/'+str(item.relative_to(stage)),
                                 'file_info':{'mode':0o644}})
    contents += [{'dst':'/var/lib/cpra','type':'dir','file_info':{'mode':0o700,'owner':'cpra','group':'cpra'}},
                 {'dst':'/etc/cpra','type':'dir','file_info':{'mode':0o750,'owner':'root','group':'cpra'}}]
    scripts={key:str(ROOT/'packaging/linux'/f'{format}-{name}.sh') for key,name in
             [('preinstall','preinstall'),('postinstall','postinstall'),('preremove','preremove'),('postremove','postremove')]}
    data={'name':'cpra','arch':target.split('_')[1],'platform':'linux',
          'version':version_fields(manifest['version'],format),'version_schema':'none','release':manifest['package_revision'],
          'maintainer':'CPRa maintainers <ziad-hsn@users.noreply.github.com>',
          'description':'Durable single-node monitoring and incident automation.\nConfiguration and durable data are preserved during upgrades and removal.',
          'homepage':'https://github.com/ziad-hsn/cpra','license':'MIT',
          'mtime':manifest['source_date'],'contents':contents,'scripts':scripts,
          'rpm':{'buildhost':'reproducible.cpra'},
          'depends':['ca-certificates']}
    if format=='deb':
        data['depends']+=['passwd', 'coreutils', 'libc-bin', 'grep']
    else:
        data['depends']+=['shadow-utils', 'coreutils', 'glibc-common', 'grep']
        data['rpm']['scripts']={'posttrans':str(ROOT/'packaging/linux/rpm-posttrans.sh')}
    return data


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out',type=Path,default=ROOT/'dist/release')
    parser.add_argument('--nfpm',default='nfpm')
    args=parser.parse_args();args.out=args.out.resolve()
    manifest=json.loads((args.out/'RELEASE.json').read_text());check_manifest(manifest)
    for arch in ('amd64','arm64'):
        for format in ('deb','rpm'):
            data=config(manifest,args.out,'linux_'+arch,format)
            config_path=args.out/f'nfpm-{arch}-{format}.json'
            config_path.write_bytes(encoded(data))
            env=dict(os.environ,SOURCE_DATE_EPOCH=str(manifest['source_date_epoch']))
            subprocess.run([args.nfpm,'package','--config',str(config_path),'--packager',format,
                            '--target',str(args.out/f"cpra-{manifest['version']}-linux-{arch}.{format}")],check=True,env=env,cwd=ROOT)
            config_path.unlink()
    checksums(args.out)

if __name__=='__main__':main()
