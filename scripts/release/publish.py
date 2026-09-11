#!/usr/bin/env python3
"""Read back a signed staging draft before creating any installable version tag.

Only the protected, manually dispatched main workflow invokes this tool. Draft
status does not hide a Go module tag; the staging tag deliberately is not SemVer.
"""
import argparse
import hashlib
import tarfile
import zipfile
import json
import os
from pathlib import Path
import subprocess
import tempfile
from release import ROOT, RECIPE, sha, verify
from registry import assert_absent

REPOSITORY = 'ziad-hsn/cpra'
IDENTITY = 'https://github.com/ziad-hsn/cpra/.github/workflows/release.yml@refs/heads/main'
ISSUER = 'https://token.actions.githubusercontent.com'


def run(*args, **kwargs):
    return subprocess.run([str(a) for a in args], check=True, **kwargs)


def archive_programs(out, manifest, target):
    name = f"cpra-{manifest['version']}-{target.replace('_', '-')}"
    suffix = '.exe' if target.startswith('windows_') else ''
    expected = {}
    if suffix:
        with zipfile.ZipFile(out/(name+'.zip')) as archive:
            inventory = json.loads(archive.read('DEPENDENCIES.json'))['binaries']
            identity = json.loads(archive.read('RELEASE.json'))
            for program in ('cpra', 'cpractl'):
                expected[program] = hashlib.sha256(archive.read(program+suffix)).hexdigest()
    else:
        with tarfile.open(out/(name+'.tar.gz')) as archive:
            inventory = json.load(archive.extractfile('DEPENDENCIES.json'))['binaries']
            identity = json.load(archive.extractfile('RELEASE.json'))
            for program in ('cpra', 'cpractl'):
                expected[program] = hashlib.file_digest(archive.extractfile(program), 'sha256').hexdigest()
    if identity != manifest or any(expected[program] != inventory.get(program+suffix) for program in expected):
        raise ValueError('Archive identity or executable inventory disagrees with final payload: ' + target)
    return expected


def evidence(out, manifest):
    required = ['reproducibility.json', 'documentation.json']
    required += ['package-'+format+'-'+arch+'.json' for format in ('deb','rpm') for arch in ('amd64','arm64')]
    required += ['native-'+target+'.json' for target in RECIPE['targets']]
    required += ['go-install-'+target+'.json' for target in RECIPE['targets']]
    required += ['service-'+target+'.json' for target in RECIPE['targets']]
    required += ['service-user-'+target+'.json' for target in RECIPE['targets'] if not target.startswith('windows_')]
    required += ['container-'+arch+'.json' for arch in ('amd64', 'arm64')]
    required += ['image-repro-'+arch+'.json' for arch in ('amd64', 'arm64')]
    required += ['chart-helm3-to-4.json', 'chart-helm4.json']
    program_hashes = {}
    for name in required:
        report = json.loads((out/name).read_text())
        if report.get('status') != 'pass':
            raise ValueError('Required release evidence did not pass: ' + name)
        if name.startswith(('native-', 'go-install-', 'service-', 'package-')) or name in ('reproducibility.json', 'documentation.json', 'package-deb.json', 'package-rpm.json'):
            if report.get('commit') != manifest['commit']:
                raise ValueError('Evidence belongs to a different source candidate: ' + name)
        if name.startswith('package-'):
            _, package_format, package_arch = name.removesuffix('.json').split('-')
            payload = out/f"cpra-{manifest['version']}-linux-{package_arch}.{package_format}"
            if report.get('format') != package_format or report.get('arch') != package_arch or report.get('package_sha256') != sha(payload):
                raise ValueError('Tested Linux package differs from final downloaded payload: ' + name)
            if report.get('source_candidate') is not False or report.get('source_dirty') is not False:
                raise ValueError('Package evidence was not recorded from a clean official candidate: ' + name)
        if name.startswith(('native-', 'service-')):
            prefix = 'service-user-' if name.startswith('service-user-') else ('service-' if name.startswith('service-') else 'native-')
            target = name.removeprefix(prefix).removesuffix('.json')
            if target not in program_hashes:
                program_hashes[target] = archive_programs(out, manifest, target)
            expected = program_hashes[target]
            if report.get('binary_sha256') != expected['cpra'] or report.get('cli_sha256') != expected['cpractl']:
                raise ValueError('Tested executables differ from final archive bytes: ' + name)
            if report.get('source_candidate') is not False or report.get('source_dirty') is not False:
                raise ValueError('Native evidence was not recorded from a clean official candidate: ' + name)
            goos, arch = target.split('_')
            if report.get('os') != {'linux':'Linux','darwin':'Darwin','windows':'Windows'}[goos] or report.get('architecture') not in ({'amd64':('x86_64','AMD64'), 'arm64':('aarch64','arm64','ARM64')}[arch]):
                raise ValueError('Native execution platform does not match the published target: ' + name)
        if name.startswith('service-') and report.get('execution_environment') != 'native-host':
            raise ValueError('Container/emulation does not establish native service support: ' + name)
        if name.startswith(('image-repro-', 'container-', 'chart-')) and report.get('source', {}).get('commit') != manifest['commit']:
            raise ValueError('Image/deployment evidence belongs to a different candidate: ' + name)
        if name.startswith('chart-') and report.get('csi_rwop_fencing') != 'pass':
            raise ValueError('Default RWOP storage was not exercised: ' + name)
    comparison = json.loads((out/'reproducibility.json').read_text())
    if not comparison.get('packages_compared') or len(comparison.get('unsigned_payloads', [])) != 2:
        raise ValueError('Release requires two extracted-source comparisons including native packages')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, default=ROOT/'dist/release')
    parser.add_argument('--cosign', required=True)
    args = parser.parse_args(); out = args.out.resolve()
    manifest = json.loads((out/'RELEASE.json').read_text()); version = manifest['version']
    if manifest['candidate'] or manifest['dirty']:
        parser.error('candidate/dirty builds cannot be published')
    if os.environ.get('GITHUB_REPOSITORY') != REPOSITORY or os.environ.get('GITHUB_REF') != 'refs/heads/main' or os.environ.get('GITHUB_EVENT_NAME') != 'workflow_dispatch':
        parser.error('publication requires the canonical manually dispatched main workflow')
    if os.environ.get('GITHUB_SHA') != manifest['commit']:
        parser.error('workflow commit does not match RELEASE.json')
    verify(out); evidence(out, manifest)
    assert_absent("ziad-hsn/cpra", manifest["version"])
    assert_absent("ziad-hsn/charts/cpra", manifest["chart_version"])
    token = os.environ.get('RELEASE_ADMIN_READ_TOKEN')
    if not token:
        parser.error('protected release environment requires RELEASE_ADMIN_READ_TOKEN to verify immutable-release settings')
    admin_env = dict(os.environ, GH_TOKEN=token)
    setting = json.loads(subprocess.check_output(['gh', 'api', 'repos/'+REPOSITORY+'/immutable-releases'], env=admin_env, text=True))
    if setting.get('enabled') is not True:
        parser.error('immutable releases must be enabled before stable-tag publication')
    if subprocess.run(['git', 'ls-remote', '--exit-code', '--tags', 'origin', 'refs/tags/'+version], capture_output=True).returncode == 0:
        parser.error('version tag already exists; never overwrite or move a published version')
    if subprocess.run(['gh', 'release', 'view', version], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
        parser.error('release already exists; inspect its evidence instead of overwriting assets')
    names = [line.split('  ', 1)[1] for line in (out/'SHA256SUMS').read_text().splitlines()]
    names += ['SHA256SUMS', 'SHA256SUMS.sigstore.json', 'provenance.sigstore.json']

    def signature(directory):
        run(args.cosign, 'verify-blob', '--bundle', directory/'SHA256SUMS.sigstore.json',
            '--certificate-identity', IDENTITY, '--certificate-oidc-issuer', ISSUER, directory/'SHA256SUMS')

    def readback(tag):
        with tempfile.TemporaryDirectory(prefix='cpra-release-readback-') as temp:
            download = Path(temp)
            run('gh', 'release', 'download', tag, '--dir', download)
            if (download/'SHA256SUMS').read_bytes() != (out/'SHA256SUMS').read_bytes():
                raise RuntimeError('Uploaded checksum manifest differs')
            verify(download); signature(download); evidence(download, manifest)
            for name in names[:-3]:
                run('gh', 'attestation', 'verify', download/name, '--repo', REPOSITORY,
                    '--signer-workflow', REPOSITORY+'/.github/workflows/release.yml',
                    '--cert-identity', IDENTITY, '--cert-oidc-issuer', ISSUER,
                    '--source-digest', manifest['commit'], '--source-ref', 'refs/heads/main',
                    '--bundle', download/'provenance.sigstore.json')

    signature(out)
    notes = out/'release-notes.txt'
    notes.write_text('Release from '+manifest['commit']+'.\n\n'
        'RELEASE.json records source identity, compiler checksums, frontend digest, storage format and the build recipe. '
        'SHA256SUMS, SPDX files, provenance bundles and per-platform evidence accompany the exact tested artifacts. '
        'Provider fixtures do not establish live-account certification. This packaging release adds no million-monitor or endurance claim.\n')
    staging = 'cpra-validation/'+manifest['commit']+'-'+os.environ['GITHUB_RUN_ID']
    run('gh', 'release', 'create', staging, '--target', manifest['commit'], '--draft', '--title', 'Verification for CPRa '+version,
        '--notes-file', notes, *[out/name for name in names])
    readback(staging)
    # The first installable tag is created only after complete runtime, source
    # reconstruction, package, chart, documentation and signed read-back gates.
    run('gh', 'api', '--method', 'POST', 'repos/'+REPOSITORY+'/git/refs',
        '-f', 'ref=refs/tags/'+version, '-f', 'sha='+manifest['commit'])
    run('gh', 'release', 'create', version, '--verify-tag', '--draft', '--title', 'CPRa '+version,
        '--notes-file', notes, *[out/name for name in names])
    readback(version)
    run('gh', 'release', 'edit', version, '--draft=false', '--prerelease='+str('-' in version).lower())
    published = json.loads(subprocess.check_output(['gh', 'api', 'repos/'+REPOSITORY+'/releases/tags/'+version], text=True))
    if published.get('immutable') is not True:
        raise RuntimeError('Published release is not immutable; do not advertise or overwrite it')
    run('gh', 'release', 'delete', staging, '--yes', '--cleanup-tag')


if __name__ == '__main__':
    main()
