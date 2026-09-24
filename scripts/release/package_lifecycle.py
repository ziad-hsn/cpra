#!/usr/bin/env python3
"""Destructive ONLY to an isolated CI host: exercise actual DEB/RPM manager and systemd.

Requires explicit --isolated-ci acknowledgement, root, systemd and no installed CPRa.
Never run this against an operator's machine or existing CPRa data.
"""
import argparse
import json
import os
import platform
import pwd
import stat
from pathlib import Path
import subprocess
import time
import urllib.request
from linux_packages import config
from release import ROOT, encoded, package_version, sha


def call(*args,check=True):
    result=subprocess.run([str(x) for x in args],check=False,capture_output=True,text=True,timeout=180)
    if check and result.returncode:
        raise RuntimeError('Command failed: '+str(args[0])+' '+str(args[1:])+'\n'+(result.stdout+result.stderr)[-5000:])
    return result


def active():
    return call('systemctl','is-active','--quiet','cpra.service',check=False).returncode==0


def ready():
    token=Path('/etc/cpra/auth.token').read_text().strip()
    deadline=time.monotonic()+30
    while True:
        try:
            req=urllib.request.Request('http://127.0.0.1:8060/api/v1/readyz',headers={'Authorization':'Bearer '+token})
            with urllib.request.urlopen(req,timeout=2) as response:
                if response.status==200:return
        except OSError:
            if time.monotonic()>deadline:raise
            time.sleep(.2)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--isolated-ci',action='store_true',required=True)
    parser.add_argument('--out',type=Path,default=ROOT/'dist/release')
    parser.add_argument('--format',choices=['deb','rpm'],required=True)
    parser.add_argument('--arch',choices=['amd64','arm64'],default='arm64' if platform.machine() in ('aarch64','arm64') else 'amd64')
    parser.add_argument('--nfpm',default='nfpm')
    parser.add_argument('--evidence',type=Path,required=True)
    args=parser.parse_args();args.out=args.out.resolve()
    if args.evidence.exists():parser.error('evidence exists; preserve the previous attempt and choose a new path')
    try:
        exercise(args)
    except Exception as error:
        data=json.loads(args.evidence.read_text()) if args.evidence.exists() else {'format':args.format,'arch':args.arch}
        data.update(status='failed',error=str(error))
        args.evidence.parent.mkdir(parents=True,exist_ok=True)
        args.evidence.write_bytes(encoded(data))
        raise


def exercise(args):
    if os.geteuid()!=0:raise RuntimeError('requires root on an isolated test host')
    if Path('/var/lib/cpra').exists() or Path('/etc/cpra').exists():raise RuntimeError('refusing to touch existing CPRa state/configuration')
    call('systemctl','is-system-running',check=False)
    if not Path('/run/systemd/system').is_dir():raise RuntimeError('requires booted systemd')
    manifest=json.loads((args.out/'RELEASE.json').read_text())
    args.evidence.parent.mkdir(parents=True,exist_ok=True)
    args.evidence.write_bytes(encoded({'status':'incomplete','format':args.format,'arch':args.arch,'version':manifest['version'],'commit':manifest['commit']}))
    original=args.out/f"cpra-{manifest['version']}-linux-{args.arch}.{args.format}"
    fixtures=[]
    fixture_versions=[]
    def package_fixture(name, version, revision):
        conf=config(manifest,args.out,'linux_'+args.arch,args.format)
        conf['version']=package_version(version);conf['release']=str(revision)
        fixture=args.out/('lifecycle-'+name+'.json');fixture.write_bytes(encoded(conf))
        target=args.out/('lifecycle-'+name+'.'+args.format)
        call(args.nfpm,'package','--config',fixture,'--packager',args.format,'--target',target)
        fixtures.extend([fixture,target]);fixture_versions.append({'name':name,'version':conf['version'],'revision':str(revision)})
        return target
    revision=int(manifest['package_revision'])
    upgraded=package_fixture('revision',manifest['version'],revision+1)
    major,minor,patch=manifest['version'].removeprefix('v').split('-',1)[0].split('.')
    next_version='v'+major+'.'+minor+'.'+str(int(patch)+1)
    early=package_fixture('early',next_version+'-a.1',1)
    late=package_fixture('late',next_version+'-a-1',1)
    final=package_fixture('final',next_version,1)
    final_revision=package_fixture('final-revision',next_version,2)
    def install(path):
        if args.format=='deb':call('dpkg','--install',path)
        else:call('rpm','--upgrade','--replacepkgs',path)
    def remove(purge=False):
        if args.format=='deb':call('dpkg','--purge' if purge else '--remove','cpra')
        else:call('rpm','--erase','cpra')
    install(original)
    if active():raise RuntimeError('fresh install started an unconfigured service')
    account=pwd.getpwnam('cpra')
    token_stat=Path('/etc/cpra/auth.token').stat()
    if token_stat.st_uid!=0 or token_stat.st_gid!=account.pw_gid or stat.S_IMODE(token_stat.st_mode)!=0o640:
        raise RuntimeError('token must be administrator-owned and readable by the service group only')
    if Path('/usr/bin/cpra').stat().st_uid!=0 or Path('/var/lib/cpra').stat().st_uid!=account.pw_uid:
        raise RuntimeError('binary/state ownership does not separate administration and service data')
    runtime=Path('/etc/cpra/runtime.yaml');runtime.write_text(runtime.read_text()+'\n# operator edit retained\n')
    expected=runtime.read_bytes();token=Path('/etc/cpra/auth.token').read_bytes()
    call('systemctl','enable','--now','cpra.service');ready()
    state=Path('/var/lib/cpra/operator-sentinel');state.write_bytes(b'preserve across upgrades/removal')
    install(upgraded);ready()
    if not active():raise RuntimeError('running upgrade did not restart service')
    if runtime.read_bytes()!=expected or Path('/etc/cpra/auth.token').read_bytes()!=token:raise RuntimeError('upgrade replaced config or token')
    call('systemctl','stop','cpra.service')
    install(early)
    if active():raise RuntimeError('different-version stopped upgrade unexpectedly started service')
    if call('systemctl','is-enabled','--quiet','cpra.service',check=False).returncode:
        raise RuntimeError('stopped upgrade lost the enabled state')
    install(late)
    if active():raise RuntimeError('prerelease ordering upgrade unexpectedly started service')
    call('systemctl','start','cpra.service');ready()
    # The real unit allows three starts per 60 seconds. These independent
    # upgrade scenarios can reach that bound even when every process is healthy.
    # Let its window expire without weakening or resetting the supervisor policy.
    time.sleep(61)
    install(final);ready()
    if not active():raise RuntimeError('prerelease-to-final upgrade did not restart a running service')
    call('systemctl','disable','--now','cpra.service')
    install(final_revision)
    if active() or call('systemctl','is-enabled','--quiet','cpra.service',check=False).returncode==0:
        raise RuntimeError('stopped and disabled upgrade changed the prior service state')
    if runtime.read_bytes()!=expected or Path('/etc/cpra/auth.token').read_bytes()!=token:
        raise RuntimeError('version or revision upgrade replaced config or token')
    remove()
    if state.read_bytes()!=b'preserve across upgrades/removal':raise RuntimeError('remove deleted state')
    if args.format=='deb' and runtime.read_bytes()!=expected:raise RuntimeError('DEB remove lost conffile')
    install(final_revision)
    if active():raise RuntimeError('reinstall unexpectedly started service')
    if args.format=='deb':
        if runtime.read_bytes()!=expected:raise RuntimeError('reinstall replaced edited conffile')
        remove(purge=True)
        if Path('/var/backups/cpra/package-config/runtime.yaml').read_bytes()!=expected:raise RuntimeError('purge lost preserved config')
        backup=Path('/var/backups/cpra/package-config').stat()
        if backup.st_uid!=0 or stat.S_IMODE(backup.st_mode)!=0o700:raise RuntimeError('purge recovery configuration is not administrator-private')
    else:
        remove()
        backups=list(Path('/etc/cpra').glob('runtime.yaml*'))
        if not any(path.read_bytes()==expected for path in backups):raise RuntimeError('RPM removal lost noreplace config')
    if state.read_bytes()!=b'preserve across upgrades/removal':raise RuntimeError('purge deleted durable state')
    args.evidence.parent.mkdir(parents=True,exist_ok=True)
    args.evidence.write_bytes(encoded({'status':'pass','format':args.format,'arch':args.arch,'version':manifest['version'],'commit':manifest['commit'],
        'scenarios':['fresh-stopped','edited-config','start-ready','running-revision-upgrade','different-version-stopped-upgrade',
            'semver-a.1-to-a-1','prerelease-to-final-running-upgrade','disabled-stopped-revision-upgrade','remove','reinstall',
            'purge-preserved-config' if args.format=='deb' else 'erase-rpmsave'],
        'fixture_versions':fixture_versions,
        'supervisor_pacing_seconds':61,
        'fixture_boundary':'Version fixtures contain the same candidate executable; this tests package ordering and hooks, not cross-binary storage-format compatibility.',
        'source_candidate':manifest['candidate'],'source_dirty':manifest['dirty'],
        'package_sha256':sha(original),'binary_sha256':sha(args.out/('linux_'+args.arch)/'cpra'),'cli_sha256':sha(args.out/('linux_'+args.arch)/'cpractl'),
        'execution_environment':'linux-systemd-container' if Path('/.dockerenv').exists() or Path('/run/.containerenv').exists() else 'native-host',
        'scope':'real package manager and booted systemd on isolated test host',
        'os_release':Path('/etc/os-release').read_text(),'kernel':call('uname','-a').stdout.strip(),
        'systemd':call('systemctl','--version').stdout.strip(),'package_manager':call('dpkg' if args.format=='deb' else 'rpm','--version').stdout.strip(),
        'filesystem':call('stat','-f','--format=%T','/var/lib/cpra').stdout.strip()}))
    for fixture in fixtures: fixture.unlink()

if __name__=='__main__':main()
