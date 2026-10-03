#!/usr/bin/env python3
"""Destructive ONLY to an isolated CI host: exercise actual DEB/RPM manager and systemd.

Requires explicit --isolated-ci acknowledgement, root, systemd and no CPRa files or backups.
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
from evidence import decode_json, package_lifecycle as verify_package_lifecycle
from linux_packages import config
from release import ROOT, encoded, evidence_identity, package_version, sha

API_ORIGIN='http://127.0.0.1:8060'
AUTH_TOKEN_FILE=Path('/etc/cpra/auth.token')


def call(*args,check=True):
    result=subprocess.run([str(x) for x in args],check=False,capture_output=True,text=True,timeout=180)
    if check and result.returncode:
        raise RuntimeError('Command failed: '+str(args[0])+' '+str(args[1:])+'\n'+(result.stdout+result.stderr)[-5000:])
    return result


def active():
    return call('systemctl','is-active','--quiet','cpra.service',check=False).returncode==0


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        return None


def request(path):
    token=AUTH_TOKEN_FILE.read_text().strip()
    req=urllib.request.Request(API_ORIGIN+path,headers={'Authorization':'Bearer '+token})
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirects())
    with opener.open(req,timeout=2) as response:
        body=response.read(1024*1024+1)
    if len(body)>1024*1024:raise RuntimeError('package observation exceeds 1 MiB')
    result=decode_json(body,'package observation')
    if not isinstance(result,dict):raise RuntimeError('package observation is not an object')
    return result


def ready():
    deadline=time.monotonic()+30
    while True:
        try:
            if request('/api/v1/readyz').get('status')!='ready':
                raise RuntimeError('package readiness response is inconsistent')
            return
        except OSError:
            if time.monotonic()>deadline:raise
            time.sleep(.2)


def validate_platform(arch):
    architecture=platform.machine()
    supported={'amd64':('x86_64','AMD64'),'arm64':('aarch64','arm64','ARM64')}
    if platform.system()!='Linux' or architecture not in supported[arch]:
        raise RuntimeError('package architecture must match the native Linux host')
    return architecture


def installed_files(account):
    return {
        'binary_directory':(Path('/usr/bin'),0,0,0o755,'directory'),
        'binary':(Path('/usr/bin/cpra'),0,0,0o755,'regular'),
        'cli':(Path('/usr/bin/cpractl'),0,0,0o755,'regular'),
        'config_directory':(Path('/etc/cpra'),0,account.pw_gid,0o750,'directory'),
        'monitors':(Path('/etc/cpra/monitors.yaml'),0,account.pw_gid,0o640,'regular'),
        'runtime':(Path('/etc/cpra/runtime.yaml'),0,account.pw_gid,0o640,'regular'),
        'api_token':(Path('/etc/cpra/auth.token'),0,account.pw_gid,0o640,'regular'),
        'state_directory':(Path('/var/lib/cpra'),account.pw_uid,account.pw_gid,0o700,'directory'),
        'service_definition':(Path('/usr/lib/systemd/system/cpra.service'),0,0,0o644,'regular'),
    }


def process_identity(pid):
    if type(pid) is not int or pid<=0:raise RuntimeError('missing package service process')
    data=(Path('/proc')/str(pid)/'stat').read_text()
    head,tail=data.rsplit(') ',1)
    fields=tail.split()
    if int(head.split(' ',1)[0])!=pid or fields[0] not in ('R','S','D','I'):
        raise RuntimeError('package service process is not live')
    # stat field 22 follows the pid and parenthesized comm fields.
    return int(fields[19])


def observe_process(pid,binary,expected,account):
    started=process_identity(pid)
    proc=Path('/proc')/str(pid)
    fields=dict(line.split(':',1) for line in (proc/'status').read_text().splitlines() if ':' in line)
    for field,wanted in (('Uid',account.pw_uid),('Gid',account.pw_gid)):
        values=[int(value) for value in fields[field].split()]
        if values!=[wanted]*4:raise RuntimeError('package service executes under the wrong identity')
    if os.readlink(proc/'exe')!=str(binary.resolve()) or sha(proc/'exe')!=expected:
        raise RuntimeError('running package executable differs from the installed candidate')
    if process_identity(pid)!=started:raise RuntimeError('package service changed during observation')
    return {'pid':pid,'start_ticks':started,'uid':account.pw_uid,'gid':account.pw_gid,'binary_sha256':expected}


def observe_installation(phase,expected,account,*,running=False):
    paths=installed_files(account)
    filesystem={}
    for name,(path,uid,gid,mode,kind) in paths.items():
        info=path.lstat()
        actual_kind='directory' if stat.S_ISDIR(info.st_mode) else 'regular' if stat.S_ISREG(info.st_mode) else 'other'
        observed={'uid':info.st_uid,'gid':info.st_gid,'mode':stat.S_IMODE(info.st_mode),'type':actual_kind}
        if observed!={'uid':uid,'gid':gid,'mode':mode,'type':kind}:
            raise RuntimeError('installed package permissions differ: '+name)
        filesystem[name]=observed
    result={'phase':phase,'filesystem':filesystem,'service_uid':account.pw_uid,'service_gid':account.pw_gid}
    for name,key in (('binary','binary_sha256'),('cli','cli_sha256')):
        actual=sha(paths[name][0])
        if actual!=expected[key]:raise RuntimeError('installed package executable differs: '+name)
        result[key]=actual
    if running:
        pid=int(call('systemctl','show','--property=MainPID','--value','cpra.service').stdout.strip())
        result['process']=observe_process(pid,paths['binary'][0],expected['binary_sha256'],account)
        result['state']=request('/api/v1/state')
        overview=request('/api/v1/overview')
        result['monitor_count']=overview.get('total')
        pools=request('/api/v1/pools')
        result['pool_tasks']={name:{field:pool.get(field) for field in ('tasks_submitted','tasks_completed')}
                              for name,pool in pools.items()}
        if type(result['monitor_count']) is not int or result['monitor_count']!=0:
            raise RuntimeError('package fixture unexpectedly admitted monitors')
        if set(result['pool_tasks'])!={'pulse','intervention','code'} or any(
                type(value) is not int or value!=0 for pool in result['pool_tasks'].values() for value in pool.values()):
            raise RuntimeError('package fixture unexpectedly dispatched provider work')
        if int(call('systemctl','show','--property=MainPID','--value','cpra.service').stdout.strip())!=pid or process_identity(pid)!=result['process']['start_ticks']:
            raise RuntimeError('package service changed during API observation')
    return result


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
    architecture=validate_platform(args.arch)
    if os.geteuid()!=0:raise RuntimeError('requires root on an isolated test host')
    for path in (Path('/var/lib/cpra'),Path('/etc/cpra'),Path('/usr/bin/cpra'),Path('/usr/bin/cpractl'),
                 Path('/etc/systemd/system/cpra.service'),Path('/usr/lib/systemd/system/cpra.service'),
                 Path('/var/backups/cpra')):
        if path.exists() or path.is_symlink():raise RuntimeError('refusing to touch existing CPRa installation/state/configuration/backup: '+str(path))
    call('systemctl','is-system-running',check=False)
    if not Path('/run/systemd/system').is_dir():raise RuntimeError('requires booted systemd')
    manifest=json.loads((args.out/'RELEASE.json').read_text())
    identity_record=evidence_identity(manifest)
    args.evidence.parent.mkdir(parents=True,exist_ok=True)
    args.evidence.write_bytes(encoded({'status':'incomplete','format':args.format,'arch':args.arch,'version':manifest['version'],'commit':manifest['commit'],'release_identity':identity_record}))
    original=args.out/f"cpra-{manifest['version']}-linux-{args.arch}.{args.format}"
    fixtures=[]
    observations=[]
    expected_programs={'binary_sha256':sha(args.out/('linux_'+args.arch)/'cpra'),
                       'cli_sha256':sha(args.out/('linux_'+args.arch)/'cpractl')}
    def capture(phase,*,running=False):
        progress=json.loads(args.evidence.read_text())
        progress.update(pending_phase=phase,observations=observations)
        args.evidence.write_bytes(encoded(progress))
        observations.append(observe_installation(phase,expected_programs,account,running=running))
        progress.pop('pending_phase')
        args.evidence.write_bytes(encoded(progress))
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
    capture('fresh_install')
    runtime=Path('/etc/cpra/runtime.yaml');runtime.write_text(runtime.read_text()+'\n# operator edit retained\n')
    expected=runtime.read_bytes();token=Path('/etc/cpra/auth.token').read_bytes()
    call('systemctl','enable','--now','cpra.service');ready()
    capture('initial_startup',running=True)
    state=Path('/var/lib/cpra/operator-sentinel');state.write_bytes(b'preserve across upgrades/removal')
    install(upgraded);ready()
    capture('running_revision_upgrade',running=True)
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
    capture('prerelease_final_upgrade',running=True)
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
    capture('reinstall')
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
    result={'status':'pass','format':args.format,'arch':args.arch,'version':manifest['version'],'commit':manifest['commit'],
        'release_identity':identity_record,
        'scenarios':['fresh-stopped','edited-config','start-ready','running-revision-upgrade','different-version-stopped-upgrade',
            'semver-a.1-to-a-1','prerelease-to-final-running-upgrade','disabled-stopped-revision-upgrade','remove','reinstall',
            'purge-preserved-config' if args.format=='deb' else 'erase-rpmsave'],
        'fixture_versions':fixture_versions,
        'supervisor_pacing_seconds':61,
        'fixture_boundary':'Version fixtures contain the same candidate executable; this tests package ordering and hooks, not cross-binary storage-format compatibility.',
        'source_candidate':manifest['candidate'],'source_dirty':manifest['dirty'],
        'package_sha256':sha(original),**expected_programs,'architecture':architecture,'os':platform.system(),
        'observations':observations,'manifest_monitor_count':0,'provider_operations':0,
        'execution_environment':'linux-systemd-container' if Path('/.dockerenv').exists() or Path('/run/.containerenv').exists() else 'native-host',
        'scope':'real package manager and booted systemd on isolated test host',
        'os_release':Path('/etc/os-release').read_text(),'kernel':call('uname','-a').stdout.strip(),
        'systemd':call('systemctl','--version').stdout.strip(),'package_manager':call('dpkg' if args.format=='deb' else 'rpm','--version').stdout.strip(),
        'filesystem':call('stat','-f','--format=%T','/var/lib/cpra').stdout.strip()}
    verify_package_lifecycle(result,args.evidence.name,manifest,{'linux_'+args.arch:{'cpra':expected_programs['binary_sha256'],'cpractl':expected_programs['cli_sha256']}})
    args.evidence.write_bytes(encoded(result))
    for fixture in fixtures: fixture.unlink()

if __name__=='__main__':main()
