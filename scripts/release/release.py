#!/usr/bin/env python3
"""Build and package the recorded release recipe. This tool never publishes."""
import argparse
from datetime import datetime, timezone
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import zipfile

import package

ROOT = Path(__file__).resolve().parents[2]
RECIPE = json.loads(Path(__file__).with_name('recipe.json').read_text())
TOOLCHAINS = json.loads(Path(__file__).with_name('toolchains.json').read_text())
SEMVER = re.compile(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?\Z')


def valid_version(version):
    if not isinstance(version, str) or not SEMVER.fullmatch(version):
        return False
    if '-' not in version:
        return True
    return all(re.fullmatch('[0-9A-Za-z-]+', part) and
               not (part.isdigit() and len(part) > 1 and part[0] == '0')
               for part in version.split('-', 1)[1].split('.'))


def package_version(version):
    """Preserve full SemVer precedence in both dpkg and RPM comparisons.

    Numeric identifiers use numeric comparison after an 'a' marker. Textual
    identifiers use a 'b' marker and fixed-width lowercase ASCII encodings;
    their 'a' terminator sorts before every encoded character. This preserves
    numeric-before-text, ASCII text ordering, and shorter-prefix precedence.
    The outer tilde makes every prerelease sort below its final release.
    """
    if not valid_version(version):
        raise ValueError('Invalid package version')
    base, separator, prerelease = version[1:].partition('-')
    if not separator:
        return base
    parts = []
    for identifier in prerelease.split('.'):
        if identifier.isdigit():
            parts.append('a'+identifier+'a')
        else:
            parts.append('b'+''.join(chr(98+ord(char)//26)+chr(97+ord(char)%26) for char in identifier)+'a')
    return base+'~'+''.join(parts)


def encoded(value):
    return (json.dumps(value, indent=2, sort_keys=True) + '\n').encode()


def sha(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024*1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def run(args, *, env=None, cwd=ROOT):
    return subprocess.check_output([str(x) for x in args], cwd=cwd, env=env, text=True).strip()


def environment(go, manifest, target=None):
    # Do not inherit compiler, module policy, workspace, experiment or VCS flags.
    keep = ('PATH','HOME','USERPROFILE','SYSTEMROOT','SystemRoot','WINDIR','TEMP','TMP','TMPDIR','GOCACHE','GOMODCACHE')
    env = {key: os.environ[key] for key in keep if key in os.environ}
    env.update(manifest['recipe']['environment'])
    env['PATH'] = str(Path(go).resolve().parent) + os.pathsep + env.get('PATH','')
    env['SOURCE_DATE_EPOCH'] = str(manifest['source_date_epoch'])
    if target:
        env['GOOS'], env['GOARCH'] = target.split('_')
    return env


def ldflags(manifest):
    prefix = manifest['recipe']['module'] + '/internal/version.'
    return ' '.join('-X '+prefix+key+'='+manifest[value] for key,value in
                    [('Version','version'),('Commit','commit'),('Date','source_date')])


def tree_digest(directory):
    digest = hashlib.sha256()
    for path in sorted(directory.rglob('*')):
        if path.is_file():
            digest.update(str(path.relative_to(directory)).encode()+b'\0'+path.read_bytes()+b'\0')
    return digest.hexdigest()


def source_inventory():
    """Record only committed inputs, preserving executable and symlink modes."""
    inventory = {}
    entries = [entry for entry in subprocess.check_output(['git', 'ls-tree', '-rz', 'HEAD'], cwd=ROOT).split(b'\0') if entry]
    object_ids = [entry.split(b'\t', 1)[0].split()[2] for entry in entries]
    contents = subprocess.check_output(['git', 'cat-file', '--batch'], input=b'\n'.join(object_ids)+b'\n', cwd=ROOT)
    offset = 0
    for entry in entries:
        header, raw_name = entry.split(b'\t', 1)
        mode, kind, _ = header.decode().split()
        name = raw_name.decode()
        if kind != 'blob':
            raise ValueError('Submodules require an explicit source release policy: ' + name)
        end = contents.index(b'\n', offset)
        size = int(contents[offset:end].split()[2])
        data = contents[end+1:end+1+size]
        offset = end+size+2
        inventory[name] = {'mode': mode, 'sha256': hashlib.sha256(data).hexdigest()}
    return inventory


def storage_format(build_tags):
    """Resolve the build-selected format constant without executing source code."""
    directory = ROOT / 'internal/persistence'
    external = 'externaljobs' in build_tags
    entry = directory / ('job_type_external.go' if external else 'extensions_base.go')
    constraint = '//go:build externaljobs' if external else '//go:build !externaljobs'
    if entry.read_text().splitlines()[0] != constraint:
        raise ValueError('Storage format entry has an unexpected build constraint')
    definition = re.compile(r'^const ([A-Za-z][A-Za-z0-9_]*) = ([A-Za-z][A-Za-z0-9_]*|[0-9]+)$', re.MULTILINE)
    selected = dict(definition.findall(entry.read_text())).get('LatestFormatVersion')
    sources = [path for path in directory.glob('*.go') if not path.name.endswith('_test.go')]
    seen = set()
    while selected and not selected.isdigit():
        if selected in seen or len(seen) >= 8:
            raise ValueError('Storage format constant aliases are cyclic or too deep')
        seen.add(selected)
        candidates = [value for path in sources for name, value in definition.findall(path.read_text()) if name == selected]
        if len(candidates) != 1:
            raise ValueError('Storage format alias must have one explicit definition')
        selected = candidates[0]
    if not selected:
        raise ValueError('Storage format constant is not explicitly resolvable')
    return int(selected)


def check_manifest(manifest):
    if manifest.get('schema_version') != 1 or manifest.get('recipe') != RECIPE:
        raise ValueError('Unsupported release manifest or recipe; use its matching source archive')
    if not valid_version(manifest.get('version','')):
        raise ValueError('Release version must be vMAJOR.MINOR.PATCH with optional prerelease')
    if not re.fullmatch('[a-f0-9]{40}', manifest.get('commit','')):
        raise ValueError('Release commit must be a full SHA1')
    expected = datetime.fromtimestamp(manifest['source_date_epoch'],timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    if manifest.get('source_date') != expected:
        raise ValueError('Source timestamp and epoch do not agree')
    if tree_digest(ROOT/'internal/httpserver/assets') != manifest['dashboard_sha256']:
        raise ValueError('Embedded dashboard differs from RELEASE.json')
    if manifest.get('toolchain_archives') != TOOLCHAINS[RECIPE['go_version']]:
        raise ValueError('Compiler archive pins differ from RELEASE.json')
    if manifest.get('storage_format_version') != RECIPE['storage_format_version']:
        raise ValueError('Storage format differs from RELEASE.json')
    if storage_format(RECIPE['build_tags']) != manifest['storage_format_version']:
        raise ValueError('Storage implementation differs from the recorded release format')
    if not manifest.get('candidate'):
        if not manifest.get('source_files'):
            raise ValueError('Official builds require an immutable source inventory')
        for name, record in manifest['source_files'].items():
            if Path(name).is_absolute() or '..' in Path(name).parts:
                raise ValueError('Unsafe source inventory path')
            path = ROOT/name
            if record['mode'] == '120000':
                data = os.readlink(path).encode()
            else:
                if not path.is_file() or path.is_symlink():
                    raise ValueError('Source input is not a regular file: ' + name)
                data = path.read_bytes()
            if hashlib.sha256(data).hexdigest() != record['sha256']:
                raise ValueError('Source input changed after release preparation: ' + name)
        # All embedded files and Go compilation inputs must belong to the
        # approved source. An ignored file must never leak into an artifact.
        build_inputs = list(ROOT.glob('*.go'))
        for directory in ('internal', 'cmd'):
            build_inputs.extend((ROOT/directory).rglob('*.go'))
        for directory in RECIPE['embedded_inputs']:
            build_inputs.extend(path for path in (ROOT/directory).rglob('*') if path.is_file())
        for path in build_inputs:
            if str(path.relative_to(ROOT)) not in manifest['source_files']:
                raise ValueError('Unrecorded build or embedded input: ' + str(path.relative_to(ROOT)))


def prepare(version, out, candidate=False):
    if not valid_version(version):
        raise ValueError('Version must be vMAJOR.MINOR.PATCH, optionally with a prerelease; build metadata is not packaged')
    dirty = bool(run(['git','status','--porcelain','--untracked-files=all']))
    if dirty and not candidate:
        raise ValueError('Official release requires a clean committed tree, including generated assets')
    commit = run(['git','rev-parse','HEAD'])
    epoch = int(run(['git','show','-s','--format=%ct','HEAD']))
    manifest = {'schema_version':1,'version':version,'commit':commit,
                'source_date_epoch':epoch,'source_date':datetime.fromtimestamp(epoch,timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
                'candidate':candidate,'dirty':dirty,'recipe':RECIPE,
                'dashboard_sha256':tree_digest(ROOT/'internal/httpserver/assets'),
                'toolchain_archives':TOOLCHAINS[RECIPE['go_version']],
                'storage_format_version':RECIPE['storage_format_version'],
                'package_revision':RECIPE['package_revision'],
                'package_version':package_version(version),
                'chart_version':re.search(r'^version:\s*(\S+)', (ROOT/'charts/cpra/Chart.yaml').read_text(), re.M)[1],
                'source_files':source_inventory() if not candidate else {}}
    out.mkdir(parents=True,exist_ok=True)
    (out/'RELEASE.json').write_bytes(encoded(manifest))
    return manifest


def validate_toolchain(go, manifest):
    version = run([go,'env','GOVERSION'],env=environment(go,manifest))
    if version != manifest['recipe']['go_version']:
        raise ValueError(f"Expected {manifest['recipe']['go_version']}; selected toolchain is {version}")


def build(go, manifest, out, targets, goreleaser=None):
    check_manifest(manifest)
    validate_toolchain(go,manifest)
    before = {name:sha(ROOT/name) for name in ('go.mod','go.sum')}
    env = environment(go,manifest)
    subprocess.run([go,'mod','download'],cwd=ROOT,env=env,check=True)
    subprocess.run([go,'mod','verify'],cwd=ROOT,env=env,check=True)
    if goreleaser:
        if targets != RECIPE['targets']:
            raise ValueError('GoReleaser builds the complete target matrix; omit --targets')
        env.update(CPRA_VERSION=manifest['version'],CPRA_COMMIT=manifest['commit'],CPRA_DATE=manifest['source_date'])
        subprocess.run([goreleaser,'build','--snapshot','--clean','--parallelism','2'],cwd=ROOT,env=env,check=True)
        artifacts = json.loads((ROOT/'dist/goreleaser/artifacts.json').read_text())
        for target in targets:
            goos,goarch = target.split('_')
            for binary in RECIPE['binaries']:
                found = [a for a in artifacts if a.get('type')=='Binary' and a.get('goos')==goos and a.get('goarch')==goarch
                         and Path(a['path']).name == binary+('.exe' if goos=='windows' else '')]
                if len(found)!=1:
                    raise ValueError(f'Expected exactly one GoReleaser artifact for {target}/{binary}')
                dest = out/target/Path(found[0]['path']).name
                dest.parent.mkdir(parents=True,exist_ok=True)
                shutil.copyfile(ROOT/found[0]['path'],dest)
                dest.chmod(0o755)
    else:
        for target in targets:
            for binary,main in RECIPE['binaries'].items():
                dest=out/target/(binary+('.exe' if target.startswith('windows_') else ''))
                dest.parent.mkdir(parents=True,exist_ok=True)
                subprocess.run([go,'build',*RECIPE['build_flags'],'-tags',' '.join(RECIPE['build_tags']),
                                '-ldflags',ldflags(manifest),'-o',str(dest.resolve()),main],
                               cwd=ROOT,env=environment(go,manifest,target),check=True)
    if before != {name:sha(ROOT/name) for name in before}:
        raise ValueError('Build modified go.mod or go.sum; release input is not immutable')
    for target in targets:
        stage(go,manifest,out,target)
    (out/'RELEASE.json').write_bytes(encoded(manifest))


def stage(go, manifest, out, target):
    destination=out/target
    # Preserve binaries, but never retain old notices/docs from a previous run.
    for child in destination.iterdir():
        if child.name not in [name+('.exe' if target.startswith('windows_') else '') for name in RECIPE['binaries']]:
            if child.is_dir(): shutil.rmtree(child)
            else: child.unlink()
    binaries=[destination/(name+('.exe' if target.startswith('windows_') else '')) for name in RECIPE['binaries']]
    inventory,notices=package.dependency_inventory(' '.join(RECIPE['build_tags']),binaries,go,environment(go,manifest,target))
    for name in ('LICENSE','README.md','go.mod','LICENSES/dashboard.txt','brand/BRANDING.md',
                 'brand/fonts/LICENSE.txt','brand/dist/svg/cpra-horizontal-dark.svg','brand/dist/svg/cpra-horizontal-color.svg'):
        path=destination/name
        path.parent.mkdir(parents=True,exist_ok=True)
        shutil.copyfile(ROOT/name,path)
    for name,data in notices.items():
        path=destination/'THIRD_PARTY_NOTICES'/name
        path.parent.mkdir(parents=True,exist_ok=True)
        path.write_bytes(data)
    for directory in ('examples','docs'):
        for source in sorted((ROOT/directory).glob('*.yaml' if directory=='examples' else '*.md')):
            path=destination/directory/source.name
            path.parent.mkdir(parents=True,exist_ok=True)
            shutil.copyfile(source,path)
    (destination/'RELEASE.json').write_bytes(encoded(manifest))
    (destination/'DEPENDENCIES.json').write_bytes(encoded({'target':target,'dependencies':inventory,
        'binaries':{p.name:sha(p) for p in binaries}}))


def source_archive(manifest,out):
    if manifest['candidate'] or manifest['dirty']:
        raise ValueError('Candidate/dirty binaries cannot claim a committed-source rebuild archive')
    if run(['git','rev-parse','HEAD']) != manifest['commit'] or run(['git','status','--porcelain','--untracked-files=all']):
        raise ValueError('Source archive requires the original clean release commit')
    raw=subprocess.check_output(['git','archive','--format=tar',manifest['commit']],cwd=ROOT)
    path=out/f"cpra-{manifest['version']}-source.tar.gz"
    with path.open('wb') as dest, gzip.GzipFile(filename='',mode='wb',fileobj=dest,mtime=manifest['source_date_epoch']) as compressed:
        with tarfile.open(fileobj=compressed,mode='w') as target, tarfile.open(fileobj=io.BytesIO(raw),mode='r:') as source:
            for member in source:
                if member.name=='RELEASE.json':
                    raise ValueError('RELEASE.json is generated release metadata, not a tracked source file')
                member.uid=member.gid=0
                member.uname=member.gname=''
                if member.isfile(): member.mode=0o755 if member.mode&0o111 else 0o644
                elif member.isdir(): member.mode=0o755
                target.addfile(member,source.extractfile(member) if member.isfile() else None)
            data=encoded(manifest)
            info=tarfile.TarInfo('RELEASE.json');info.size=len(data);info.mtime=manifest['source_date_epoch'];info.mode=0o644
            target.addfile(info,io.BytesIO(data))
    return path


def compose_bundle(manifest, out):
    entries = {}
    for name in ('docker-compose.yml', 'runtime.yaml', '.env.example'):
        data = (ROOT/'docker'/name).read_bytes()
        if name == '.env.example':
            data = re.sub(rb'(?m)^CPRA_IMAGE=.*$', ('CPRA_IMAGE=ghcr.io/ziad-hsn/cpra:'+manifest['version']).encode(), data)
        entries[name] = (data, False)
    for name in ('LICENSE',):
        entries[name] = ((ROOT/name).read_bytes(), False)
    entries['RELEASE.json'] = (encoded(manifest), False)
    guide = (ROOT/'docs/container-helm.md').read_text()
    guide = re.sub(r'\]\(([^):]+\.md)(#[^)]*)?\)', lambda match: ']('+'https://github.com/ziad-hsn/cpra/blob/'+manifest['commit']+'/docs/'+match[1]+(match[2] or '')+')', guide)
    entries['OPERATIONS.md'] = (guide.encode(), False)
    entries['README.md'] = (('# CPRa Compose deployment\n\nThis bundle is for CPRa {version}, source {commit}.\n\nCopy `.env.example` to `.env`. Set absolute `CPRA_MANIFEST_FILE` and\n`CPRA_TOKEN_FILE` paths to files readable by container UID/GID 1001. The bundle\ncontains no credentials. Its image reference names this release version;\noperators may replace it with the corresponding verified digest from OCI.json.\n\nFrom this extracted directory, run:\n\n```sh\ndocker compose --env-file .env -f docker-compose.yml config --quiet\ndocker compose --env-file .env -f docker-compose.yml up -d\ndocker compose --env-file .env -f docker-compose.yml ps\n```\n\nState remains in the stable CPRA_DATA_VOLUME (default cpra-data). Ordinary\nrecreation and `docker compose down` retain it. Do not use `down --volumes`\nwhen preserving state. Stop the owner before a complete backup or migration.\nSee [OPERATIONS.md](OPERATIONS.md) for storage, permissions and upgrade rules;\nits repository-relative examples refer to the source checkout, while the\ncommands above apply directly to this bundle.\n').format(version=manifest['version'], commit=manifest['commit']).encode(), False)
    path = out/f"cpra-{manifest['version']}-compose.tar.gz"
    package.archive(path, entries, manifest['source_date_epoch'])
    return path


def pack(manifest,out,targets,source=True):
    check_manifest(manifest)
    for target in targets:
        directory=out/target
        entries={str(p.relative_to(directory)):(p.read_bytes(),bool(p.stat().st_mode&0o111))
                 for p in directory.rglob('*') if p.is_file()}
        name=f"cpra-{manifest['version']}-{target.replace('_','-')}"
        if target.startswith('windows_'):
            # ZIP DOS timestamps have a 1980 lower bound and two-second precision.
            stamp=datetime.fromtimestamp(max(315532800,manifest['source_date_epoch']),timezone.utc)
            with zipfile.ZipFile(out/(name+'.zip'),'w',compression=zipfile.ZIP_DEFLATED,compresslevel=9) as archive:
                for path,(data,executable) in sorted(entries.items()):
                    info=zipfile.ZipInfo(path,stamp.timetuple()[:6]);info.create_system=3
                    info.external_attr=(0o100755 if executable else 0o100644)<<16
                    info.compress_type=zipfile.ZIP_DEFLATED
                    archive.writestr(info,data,compresslevel=9)
        else:
            package.archive(out/(name+'.tar.gz'),entries,manifest['source_date_epoch'])
    compose_bundle(manifest,out)
    if source:
        source_archive(manifest,out)
    checksums(out)


def checksums(out):
    files=sorted(p for p in out.iterdir() if p.is_file() and p.name!='SHA256SUMS' and not p.name.endswith('.sigstore.json'))
    (out/'SHA256SUMS').write_text(''.join(f'{sha(p)}  {p.name}\n' for p in files))


def verify(out):
    lines=(out/'SHA256SUMS').read_text().splitlines()
    if not lines:
        raise ValueError('Checksum manifest is empty')
    seen=set()
    for line in lines:
        digest,name=line.split('  ',1)
        if Path(name).name!=name or name in seen or not re.fullmatch('[a-f0-9]{64}',digest):
            raise ValueError('Unsafe or duplicate checksum entry')
        seen.add(name)
        if sha(out/name)!=digest:
            raise ValueError(f'Checksum mismatch: {name}')


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command',choices=['prepare','build','pack','checksums','verify'])
    parser.add_argument('--out',type=Path,default=ROOT/'dist/release')
    parser.add_argument('--manifest',type=Path)
    parser.add_argument('--version')
    parser.add_argument('--candidate',action='store_true')
    parser.add_argument('--go',default=os.environ.get('GO','go'))
    parser.add_argument('--goreleaser')
    parser.add_argument('--targets',nargs='+',choices=RECIPE['targets'],default=RECIPE['targets'])
    parser.add_argument('--no-source',action='store_true')
    args=parser.parse_args()
    args.out=args.out.resolve()
    go=shutil.which(args.go) or args.go
    if args.command=='prepare':
        if not args.version: parser.error('prepare requires --version')
        prepare(args.version,args.out,args.candidate)
    elif args.command in ('checksums','verify'):
        globals()[args.command](args.out)
    else:
        path=args.manifest or (ROOT/'RELEASE.json' if (ROOT/'RELEASE.json').is_file() else args.out/'RELEASE.json')
        manifest=json.loads(path.read_text())
        if args.command=='build': build(go,manifest,args.out,args.targets,args.goreleaser)
        else: pack(manifest,args.out,args.targets,not args.no_source)

if __name__=='__main__':
    main()
