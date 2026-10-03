#!/usr/bin/env python3
"""Build committed benchmark inputs with the recorded, uninstrumented release recipe."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import math
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

if __package__:
    from .preflight import inspect
else:
    from preflight import inspect

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts/release'))
import release as release_build
from evidence import read_report

BASELINE = 'a370969b041b399c0778318d8915ce059fd74294'
RECIPE = json.loads((ROOT / 'scripts/release/recipe.json').read_text())
TOOLCHAINS = json.loads((ROOT / 'scripts/release/toolchains.json').read_text())


def command(args, cwd):
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


def sha256(path):
    with pathlib.Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def digest_json(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()).hexdigest()


def build_flags(tags, manifest=None):
    flags = [*RECIPE['build_flags'], '-tags', ' '.join(tags)]
    if manifest is not None:
        flags += ['-ldflags', release_build.ldflags(manifest)]
    return flags


def release_identity(manifest):
    identity = release_build.evidence_identity(manifest)
    if manifest['recipe'] != RECIPE or manifest['toolchain_archives'] != TOOLCHAINS[RECIPE['go_version']]:
        raise ValueError('Benchmark release metadata uses a different recipe or compiler archive')
    epoch = manifest.get('source_date_epoch')
    if type(epoch) is not int or epoch < 0:
        raise ValueError('Benchmark source timestamp must be an integer Unix epoch')
    try:
        source_date = datetime.fromtimestamp(epoch, timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    except (OverflowError, OSError, ValueError) as error:
        raise ValueError('Benchmark source timestamp is outside the supported range') from error
    if manifest.get('source_date') != source_date:
        raise ValueError('Benchmark source date differs from its recorded Unix epoch')
    return identity


def validate_source(manifest, inventory):
    """Compare extracted Git inputs with the complete frozen source inventory."""
    expected = manifest['source_files']
    if set(inventory) != set(expected):
        raise ValueError('Benchmark source inventory differs from the frozen release')
    for name, record in expected.items():
        if record['mode'] == '120000':
            observed = inventory[name]
            valid = (isinstance(observed, dict) and set(observed) == {'kind', 'target'}
                     and observed['kind'] == 'symlink' and isinstance(observed['target'], str)
                     and hashlib.sha256(observed['target'].encode()).hexdigest() == record['sha256'])
        else:
            valid = inventory[name] == {'kind': 'file', 'mode': int(record['mode'][-3:], 8), 'sha256': record['sha256']}
        if not valid:
            raise ValueError('Benchmark source file differs from the frozen release: ' + name)


def recorded_environment(target):
    goos, goarch = target.split('_')
    return {**RECIPE['environment'], 'GOOS': goos, 'GOARCH': goarch}


def build_environment(go, epoch, module_cache, build_cache):
    keep = ('PATH', 'HOME', 'USERPROFILE', 'SYSTEMROOT', 'SystemRoot', 'WINDIR', 'TEMP', 'TMP', 'TMPDIR')
    env = {key: os.environ[key] for key in keep if key in os.environ}
    env.update(RECIPE['environment'])
    env.update(SOURCE_DATE_EPOCH=str(epoch), GOMODCACHE=str(module_cache), GOCACHE=str(build_cache))
    env['PATH'] = str(go.parent) + os.pathsep + env.get('PATH', '')
    return env


def inspect_compiler(go, env):
    info = json.loads(subprocess.check_output([str(go), 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH'],
                                             cwd=go.parent, env=env, text=True))
    if info.get('GOVERSION') != RECIPE['go_version']:
        raise ValueError('Benchmark preparation requires the recorded compiler ' + RECIPE['go_version'])
    if info.get('GOHOSTOS') != 'linux' or info.get('GOHOSTARCH') not in ('amd64', 'arm64'):
        raise ValueError('The agreed WSL benchmark requires a native Linux compiler')
    return info


def extract_source(root, commit, archive, source):
    subprocess.run(['git', 'archive', '--format=tar', '--output', str(archive), commit], cwd=root, check=True)
    source.mkdir()
    with tarfile.open(archive) as stream:
        stream.extractall(source, filter='data')


def source_inventory(source):
    records = {}
    for path in sorted(source.rglob('*')):
        if path.is_symlink():
            record = {'kind': 'symlink', 'target': os.readlink(path)}
        elif path.is_file():
            record = {'kind': 'file', 'mode': path.stat().st_mode & 0o777, 'sha256': sha256(path)}
        else:
            continue
        records[path.relative_to(source).as_posix()] = record
    return records


def run_go(args, *, cwd, env, log):
    subprocess.run([str(arg) for arg in args], cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)


def prepare_builds(root, go, out, module_cache, tags, manifest):
    root, go, out, module_cache = (pathlib.Path(p).resolve() for p in (root, go, out, module_cache))
    if command(['git', 'status', '--porcelain', '--untracked-files=all'], root):
        raise ValueError('Benchmark preparation requires a clean committed candidate')
    candidate = command(['git', 'rev-parse', 'HEAD'], root)
    identity = release_identity(manifest)
    if candidate != manifest['commit']:
        raise ValueError('Benchmark checkout differs from the frozen release commit')
    if command(['git', 'rev-parse', BASELINE + '^{commit}'], root) != BASELINE:
        raise ValueError('Published baseline commit could not be verified')
    if out.is_relative_to(root):
        raise ValueError('Select a new benchmark output directory outside the source checkout')
    if not module_cache.is_dir() or module_cache.is_relative_to(root):
        raise ValueError('Select an explicit prepared module cache outside the source checkout')
    if out.exists():
        raise ValueError('Refusing to overwrite benchmark artifacts; select a new output directory')
    if module_cache.is_relative_to(out) or out.is_relative_to(module_cache):
        raise ValueError('The prepared module cache must be separate from the output directory')
    initial_env = build_environment(go, 0, module_cache, out / 'build-cache')
    compiler = inspect_compiler(go, initial_env)
    target = compiler['GOHOSTOS'] + '_' + compiler['GOHOSTARCH']
    settings = recorded_environment(target)
    compiler_digest = sha256(go)
    out.mkdir(mode=0o700, parents=True)
    report = {'schema_version': 3, 'baseline_commit': BASELINE, 'recipe': RECIPE, 'tags': tags,
              'release': manifest, 'release_identity': identity,
              'toolchain': {'version': compiler['GOVERSION'], 'target': target, 'binary_sha256': compiler_digest,
                            'archive_sha256': TOOLCHAINS[RECIPE['go_version']][target]},
              'module_cache': str(module_cache), 'builds': []}
    with (out / 'build.log').open('w') as log, tempfile.TemporaryDirectory(prefix='source-', dir=out) as temporary:
        for name, commit in (('candidate', candidate), ('baseline', BASELINE)):
            source = pathlib.Path(temporary) / name
            archive = out / (name + '-source.tar')
            epoch = int(command(['git', 'show', '-s', '--format=%ct', commit], root))
            extract_source(root, commit, archive, source)
            before = source_inventory(source)
            if name == 'candidate':
                validate_source(manifest, before)
                if epoch != manifest['source_date_epoch']:
                    raise ValueError('Benchmark candidate source timestamp differs from the frozen release')
            flags = build_flags(tags, manifest if name == 'candidate' else None)
            env = build_environment(go, epoch, module_cache, out / 'build-cache')
            env.update(settings)
            # Only the explicit cache may contain the privately qualified SDK.
            # No workspace, replacement or checksum-policy fallback is installed.
            run_go([go, 'mod', 'download'], cwd=source, env=env, log=log)
            run_go([go, 'mod', 'verify'], cwd=source, env=env, log=log)
            outputs = [(name, '.')]
            if name == 'candidate':
                outputs.append(('target', './cmd/cpra-bench-target'))
            for program, package in outputs:
                binary = out / program
                run_go([go, 'build', *flags, '-o', str(binary), package], cwd=source, env=env, log=log)
                report['builds'].append({'name': program, 'commit': commit, 'dirty': False,
                                        'go_version': compiler['GOVERSION'], 'flags': flags, 'environment': settings,
                                        'source_date_epoch': epoch, 'source_sha256': sha256(archive),
                                        'source_tree_sha256': digest_json(before),
                                        'binary': str(binary), 'sha256': sha256(binary)})
            if source_inventory(source) != before:
                raise ValueError(name + ' source inputs changed during dependency resolution or compilation; inspect build.log')
            (out / (name + '-source.json')).write_text(json.dumps(before, indent=2, sort_keys=True) + '\n')
    if sha256(go) != compiler_digest:
        raise ValueError('Compiler executable changed during preparation')
    if command(['git', 'rev-parse', 'HEAD'], root) != candidate \
            or command(['git', 'status', '--porcelain', '--untracked-files=all'], root):
        raise ValueError('Candidate checkout changed during preparation')
    verify_builds(report, out / 'candidate', out / 'target', out / 'baseline')
    (out / 'RELEASE.json').write_bytes(release_build.encoded(manifest))
    (out / 'builds.json').write_text(json.dumps(report, indent=2, sort_keys=True) + '\n')
    return report


def verify_builds(metadata, candidate, target, baseline=None):
    """Validate recorded build settings and the exact binaries to be executed.

    This is an integrity check, not signed provenance or performance evidence.
    The source/compiler archives and preparation log remain review inputs.
    """
    if not isinstance(metadata, dict) or type(metadata.get('schema_version')) is not int \
            or metadata['schema_version'] != 3 or metadata.get('recipe') != RECIPE \
            or metadata.get('baseline_commit') != BASELINE:
        raise ValueError('Benchmark metadata must use the current recipe and prepared schema version 3')
    manifest = metadata.get('release')
    if metadata.get('release_identity') != release_identity(manifest):
        raise ValueError('Benchmark metadata omits or contradicts its frozen release identity')
    tags = metadata.get('tags')
    if not isinstance(tags, list) or any(not isinstance(tag, str) or not re.fullmatch(r'[A-Za-z0-9_.]+', tag) for tag in tags) \
            or len(tags) != len(set(tags)):
        raise ValueError('Benchmark build tags are invalid')
    compiler = metadata.get('toolchain')
    if not isinstance(compiler, dict) or compiler.get('version') != RECIPE['go_version'] \
            or compiler.get('target') not in ('linux_amd64', 'linux_arm64') \
            or compiler.get('archive_sha256') != TOOLCHAINS[RECIPE['go_version']][compiler['target']] \
            or not re.fullmatch(r'[a-f0-9]{64}', str(compiler.get('binary_sha256', ''))):
        raise ValueError('Benchmark compiler identity does not match the recorded recipe')
    rows = metadata.get('builds')
    if not isinstance(rows, list) or len(rows) != 3 or any(not isinstance(row, dict) for row in rows) \
            or sorted(str(row.get('name')) for row in rows) != ['baseline', 'candidate', 'target']:
        raise ValueError('Benchmark metadata requires exactly one baseline, candidate and target')
    builds = {row['name']: row for row in rows}
    for name, row in builds.items():
        if row.get('dirty') is not False or row.get('go_version') != RECIPE['go_version'] \
                or row.get('flags') != build_flags(tags, manifest if name != 'baseline' else None) \
                or row.get('environment') != recorded_environment(compiler['target']):
            raise ValueError(name + ' does not have the required clean source and matching release build settings')
        if not re.fullmatch(r'[a-f0-9]{40}', str(row.get('commit', ''))) \
                or type(row.get('source_date_epoch')) is not int or row['source_date_epoch'] < 0 \
                or any(not re.fullmatch(r'[a-f0-9]{64}', str(row.get(key, ''))) for key in
                       ('sha256', 'source_sha256', 'source_tree_sha256')):
            raise ValueError(name + ' is missing its source or binary identity')
    if builds['baseline']['commit'] != BASELINE:
        raise ValueError('Benchmark baseline is not the published comparison commit')
    if builds['candidate']['commit'] != manifest['commit'] \
            or builds['candidate']['source_date_epoch'] != manifest['source_date_epoch']:
        raise ValueError('Benchmark candidate differs from the frozen release source')
    for key in ('commit', 'source_sha256', 'source_tree_sha256', 'source_date_epoch'):
        if builds['target'][key] != builds['candidate'][key]:
            raise ValueError('Benchmark target must be built from the same candidate source')
    for name, path in (('candidate', candidate), ('target', target), ('baseline', baseline)):
        if path is not None and builds[name]['sha256'] != sha256(path):
            raise ValueError(name + ' binary digest differs from the prepared build metadata')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', default='go')
    parser.add_argument('--tags', default=' '.join(RECIPE['build_tags']))
    parser.add_argument('--module-cache', required=True, type=pathlib.Path,
                        help='explicit isolated cache containing the qualified module versions')
    parser.add_argument('--manifest', required=True, type=pathlib.Path, help='frozen release RELEASE.json')
    parser.add_argument('--out', required=True, type=pathlib.Path)
    parser.add_argument('--fixture-headroom-gib', type=float, default=5)
    args = parser.parse_args()
    if not math.isfinite(args.fixture_headroom_gib) or args.fixture_headroom_gib < 0:
        parser.error('fixture headroom must be finite and non-negative')
    tags = args.tags.replace(',', ' ').split()
    if any(not re.fullmatch(r'[A-Za-z0-9_.]+', tag) for tag in tags) or len(tags) != len(set(tags)):
        parser.error('build tags must be distinct Go build tag names')
    preflight = inspect(int(args.fixture_headroom_gib * 1024 ** 3))
    if not preflight['ready']:
        print(json.dumps(preflight, sort_keys=True))
        return 2
    go = pathlib.Path(shutil.which(args.go) or args.go).resolve()
    try:
        prepare_builds(ROOT, go, args.out, args.module_cache, tags, read_report(args.manifest))
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        parser.exit(1, str(error) + '\n')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
