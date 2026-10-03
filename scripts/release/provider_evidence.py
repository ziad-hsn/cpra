#!/usr/bin/env python3
"""Build a source-bound verifier and inspect retained provider campaigns.

Working-tree builds are private diagnostic artifacts. Only a frozen build can
be compared with RELEASE.json, and even consistent complete provider evidence
requires independent review and the other release gates before publication.
"""
import argparse
from datetime import datetime, timezone
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys

import release
from evidence import read_report

sys.path.insert(0, str(release.ROOT / 'scripts/sdk'))
from module_archive import tool_environment
from prepare_dependencies import run_go, source_inventory

_spec = importlib.util.spec_from_file_location(
    'cpra_provider_summary', release.ROOT / 'scripts/verification/summarize.py')
summary = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(summary)

MAX_BINARY_BYTES = 1024 ** 3
MAX_LOG_BYTES = 16 * 1024 ** 2
MAX_REPORTS = 128
ENTRYPOINT = './cmd/cpra-verify'
PROGRAM = 'cpra-verify'
SOURCE_REQUIREMENTS = {'cmd/cpra-verify/main.go', 'internal/drivertest/runner.go',
                       'internal/version/version.go', 'scripts/verification/summarize.py',
                       'scripts/release/provider_evidence.py'}


def require(condition, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent provider evidence: ' + detail)


def regular_path(directory, name):
    require(isinstance(name, str) and name and str(PurePosixPath(name)) == name
            and not PurePosixPath(name).is_absolute() and '..' not in PurePosixPath(name).parts
            and '\\' not in name and ':' not in name, 'unsafe retained path')
    path = directory
    for part in PurePosixPath(name).parts:
        path /= part
        require(not path.is_symlink(), 'retained path contains a link')
    return path


def fingerprint(path, limit):
    import hashlib
    require(not path.is_symlink() and path.is_file(), 'input is not a regular file: ' + path.name)
    value, size = hashlib.sha256(), 0
    with path.open('rb') as stream:
        while block := stream.read(64 * 1024):
            size += len(block)
            require(size <= limit, 'input exceeds its byte limit: ' + path.name)
            value.update(block)
    return {'sha256': value.hexdigest(), 'bytes': size}


def version_text(manifest):
    return '{} (commit {}, source date {})'.format(
        manifest['version'], manifest['commit'], manifest['source_date'])


def build_verifier(go, out, *, manifest=None, dependencies=None, cache=None):
    """Reserve output before building; preserve command logs on every failure."""
    require(dependencies is None or manifest is not None,
            'dependency provenance requires a frozen manifest')
    require(manifest is None or cache is None, 'working-tree cache cannot qualify a frozen build')
    go, out = Path(go).resolve(strict=True), Path(out).absolute()
    require(not out.resolve().is_relative_to(release.ROOT), 'select an output directory outside the checkout')
    out.mkdir(mode=0o700, parents=True, exist_ok=False)
    report = {'schema_version': 1, 'status': 'building', 'commands': [],
              'evidence_class': 'frozen_provider_build' if manifest else 'working_tree_provider_build',
              'release_qualified': False, 'public_download_qualified': False,
              'recorded_at': datetime.now(timezone.utc).isoformat()}
    original_root = release.ROOT
    try:
        if manifest is not None:
            require(manifest['recipe'] == release.RECIPE, 'build recipe differs from the approved recipe')
            report['release_identity'] = release.evidence_identity(manifest)
            release.check_manifest(manifest)
            source = original_root
            inputs = manifest['source_files']
            env = release.environment(go, manifest)
            if dependencies:
                from dependencies import cache_environment
                env = release.environment(go, manifest, dependency_paths=cache_environment(dependencies, manifest))
                report['dependency_report'] = fingerprint(Path(dependencies) / 'dependencies.json', MAX_LOG_BYTES)
        else:
            source = out / 'source'
            source.mkdir(mode=0o700)
            captured = source_inventory(original_root, source)
            inputs = {name: {'mode': '100755' if value['executable'] else '100644',
                             'sha256': value['sha256']} for name, value in captured.items()}
            report['base_commit'] = release.run(['git', 'rev-parse', 'HEAD'])
            env = tool_environment(go, Path(cache).resolve() if cache else out / 'cache')
            env.update(release.RECIPE['environment'])
        env.update(GOMAXPROCS='2', GOMEMLIMIT='768MiB', GOVCS='*:off')
        report['source_files'] = inputs
        report['recipe'] = release.RECIPE
        report['environment'] = {key: env[key] for key in release.RECIPE['environment']}
        report['compiler_sha256'] = fingerprint(go, MAX_BINARY_BYTES)['sha256']
        metadata = json.loads(run_go(go, source, ['env', '-json', 'GOVERSION', 'GOHOSTOS',
                              'GOHOSTARCH', 'GOOS', 'GOARCH', 'GOWORK'], env, out, 'go-env', report['commands']))
        target = metadata['GOOS'] + '_' + metadata['GOARCH']
        require(target in ('linux_amd64', 'linux_arm64'), 'all-driver verification requires a supported Linux target')
        require(target == metadata['GOHOSTOS'] + '_' + metadata['GOHOSTARCH'], 'verifier must be built for its native host')
        require(metadata['GOWORK'] == 'off', 'verifier must not use a workspace')
        require(metadata['GOVERSION'] in ('go1.25.0', release.RECIPE['go_version']), 'unsupported verifier compiler')
        if manifest:
            require(metadata['GOVERSION'] == manifest['recipe']['go_version'], 'frozen verifier uses a different compiler')
        report.update(go_version=metadata['GOVERSION'], target=target,
                      version=version_text(manifest) if manifest else 'dev (commit unknown, source date unknown)')
        run_go(go, source, ['mod', 'download'], env, out, 'go-download', report['commands'])
        run_go(go, source, ['mod', 'verify'], env, out, 'go-verify', report['commands'])
        executable = out / PROGRAM
        arguments = ['build', *release.RECIPE['build_flags'], '-tags',
                     ' '.join(release.RECIPE['build_tags'])]
        if manifest:
            arguments += ['-ldflags', release.ldflags(manifest)]
        arguments += ['-o', str(executable), ENTRYPOINT]
        run_go(go, source, arguments, env, out, 'go-build', report['commands'])
        run_go(go, source, ['version', '-m', str(executable)], env, out, 'build-info', report['commands'])
        report['executable'] = {'path': PROGRAM, **fingerprint(executable, MAX_BINARY_BYTES)}
        if manifest:
            release.check_manifest(manifest)
        else:
            require(source_inventory(original_root) == captured, 'working source changed during compilation')
            # The compiler may not alter the copied source or dependency declarations.
            require(all(fingerprint(source / name, 32 * 1024 ** 2)['sha256'] == value['sha256']
                        for name, value in inputs.items()), 'copied source changed during compilation')
        report.update(status='built', source_unchanged=True)
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        report.update(status='failed', error=str(error), source_unchanged=False)
        raise
    finally:
        release.ROOT = original_root
        (out / 'report.json').write_bytes(release.encoded(report))
    return report


def build_settings(text):
    settings, main = {}, None
    for line in text.splitlines()[1:]:
        fields = line.strip().split('\t')
        if fields[0] == 'path' and len(fields) == 2:
            require(main is None, 'duplicate executable package identity')
            main = fields[1]
        elif fields[0] == 'build' and len(fields) == 2 and '=' in fields[1]:
            key, value = fields[1].split('=', 1)
            require(key not in settings, 'duplicate executable build setting')
            settings[key] = value.strip('"')
    return main, settings


def verify_provider_reports(manifest, build_directory, paths):
    """Inspect actual retained inputs; never turn this result into publication approval."""
    identity = release.evidence_identity(manifest)
    require(manifest['recipe'] == release.RECIPE, 'manifest uses a different build recipe')
    require(SOURCE_REQUIREMENTS <= manifest['source_files'].keys(), 'frozen source omits verifier inputs')
    directory = Path(build_directory).absolute()
    require(not directory.is_symlink() and directory.is_dir(), 'verifier build directory is unavailable')
    build_path = regular_path(directory, 'report.json')
    before = {str(build_path): fingerprint(build_path, MAX_LOG_BYTES)['sha256']}
    build = read_report(build_path)
    require(type(build.get('schema_version')) is int and build['schema_version'] == 1 and build.get('status') == 'built'
            and build.get('evidence_class') == 'frozen_provider_build'
            and build.get('source_unchanged') is True
            and build.get('release_qualified') is False and build.get('public_download_qualified') is False,
            'verifier is not a successful frozen build')
    require(build.get('release_identity') == identity and build.get('source_files') == manifest['source_files'],
            'verifier source differs from the frozen release')
    require(build.get('recipe') == manifest['recipe'] and build.get('environment') == manifest['recipe']['environment'],
            'verifier settings differ from the release recipe')
    require(build.get('go_version') == manifest['recipe']['go_version']
            and build.get('version') == version_text(manifest), 'verifier version or compiler differs')
    require(isinstance(build.get('compiler_sha256'), str)
            and re.fullmatch('[a-f0-9]{64}', build['compiler_sha256']), 'missing compiler fingerprint')
    require(build.get('target') in ('linux_amd64', 'linux_arm64'), 'unsupported verifier target')
    executable = regular_path(directory, PROGRAM)
    actual = fingerprint(executable, MAX_BINARY_BYTES)
    require(actual['bytes'] > 0 and build.get('executable') == {'path': PROGRAM, **actual}, 'verifier executable changed')
    before[str(executable)] = actual['sha256']
    commands = build.get('commands')
    require(isinstance(commands, list) and len(commands) == 5, 'missing verifier build commands')
    expected = ('go-env', 'go-download', 'go-verify', 'go-build', 'build-info')
    for command, name in zip(commands, expected):
        require(isinstance(command, dict) and command.get('status') == 'pass'
                and type(command.get('exit_code')) is int and command['exit_code'] == 0,
                'verifier build command did not pass')
        for field in ('stdout', 'stderr'):
            require(command.get(field) == name + '.' + field, 'unexpected verifier command output')
            path = regular_path(directory, command[field])
            value = fingerprint(path, MAX_LOG_BYTES)
            require(value['sha256'] == command.get(field + '_sha256'), 'verifier command output changed')
            before[str(path)] = value['sha256']
    binary_args = ['build', *manifest['recipe']['build_flags'], '-tags',
                   ' '.join(manifest['recipe']['build_tags']), '-ldflags', release.ldflags(manifest)]
    command = commands[3].get('command')
    require(isinstance(command, list) and command[1:-3] == binary_args and command[-3] == '-o'
            and Path(command[-2]).name == PROGRAM and command[-1] == ENTRYPOINT, 'verifier build arguments differ')
    require(all(isinstance(item.get('command'), list) and item['command']
                and item['command'][0] == command[0] for item in commands),
            'verifier build commands identify different compilers')
    require(commands[0]['command'][1:] == ['env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH', 'GOOS', 'GOARCH', 'GOWORK']
            and commands[1]['command'][1:] == ['mod', 'download']
            and commands[2]['command'][1:] == ['mod', 'verify']
            and commands[4]['command'][1:] == ['version', '-m', command[-2]], 'incorrect verifier command inventory')
    metadata = read_report(directory / 'go-env.stdout')
    goos, goarch = build['target'].split('_')
    require(metadata == {'GOVERSION': build['go_version'], 'GOHOSTOS': goos, 'GOHOSTARCH': goarch,
                         'GOOS': goos, 'GOARCH': goarch, 'GOWORK': 'off'}, 'verifier host/compiler evidence differs')
    text = (directory / 'build-info.stdout').read_text()
    require(text.splitlines() and text.splitlines()[0].endswith(': ' + build['go_version']), 'binary compiler metadata differs')
    main, settings = build_settings(text)
    require(main == manifest['recipe']['module'] + '/cmd/cpra-verify', 'incorrect executable entrypoint')
    require(all(settings.get(key) == value for key, value in {
        'GOOS': goos, 'GOARCH': goarch, 'CGO_ENABLED': '0', '-trimpath': 'true',
        '-buildmode': 'exe', '-compiler': 'gc',
        '-tags': ','.join(manifest['recipe']['build_tags']),
        'GOAMD64' if goarch == 'amd64' else 'GOARM64': 'v1' if goarch == 'amd64' else 'v8.0',
    }.items()), 'actual executable settings differ from the release recipe')
    require(not any(key.startswith('vcs.') or key in ('-race', '-cover', '-gcflags') for key in settings),
            'verifier contains unapproved instrumentation or VCS settings')
    require(isinstance(paths, (list, tuple)) and 0 < len(paths) <= MAX_REPORTS, 'invalid provider report count')
    runs = set()
    for path in paths:
        path = Path(path).absolute()
        raw = summary.read_json(path)
        require(raw.get('binary_sha256') == actual['sha256'], 'provider report identifies another executable or omits its hash')
        require(raw.get('go_version') == build['go_version'] and raw.get('version') == build['version'],
                'provider report identifies another source/compiler')
        require(isinstance(raw.get('run_id'), str) and raw['run_id'] and raw['run_id'] not in runs,
                'missing or duplicate provider run identity')
        runs.add(raw['run_id'])
    result = summary.summarize(paths, allow_empty=True)
    for source in result['sources']:
        before[source['report']] = source['report_sha256']
        for field in ('suite', 'fixture', 'fixture_environment'):
            if field in source:
                before[source[field]] = source[field + '_sha256']
    for row in result['records']:
        for item in row['evidence']:
            before[item['evidence_file']] = item['evidence_sha256']
    for name, expected_hash in before.items():
        limit = MAX_BINARY_BYTES if name == str(executable) else MAX_LOG_BYTES
        require(fingerprint(Path(name), limit)['sha256'] == expected_hash, 'retained input changed during inspection')
    return {'schema_version': 1, 'status': 'consistent_pending_review' if result['all_providers_verified'] else 'incomplete',
            'release_identity': identity, 'release_gate_passed': False, 'formal_vendor_certification': False,
            'binary_sha256': actual['sha256'], 'target': build['target'], 'input_sha256': before,
            'providers': result, 'remaining': ['Independent provider review and the complete aggregate release gate.',
                                            'Actual live-account effects for every missing required driver role.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    build = sub.add_parser('build', help='build only; never invoke a provider')
    build.add_argument('--go', type=Path, required=True)
    build.add_argument('--out', type=Path, required=True)
    source = build.add_mutually_exclusive_group(required=True)
    source.add_argument('--manifest', type=Path)
    source.add_argument('--working-tree', action='store_true')
    build.add_argument('--dependencies', type=Path)
    build.add_argument('--cache', type=Path, help='private working-tree cache, never frozen provenance')
    verify = sub.add_parser('verify')
    verify.add_argument('--manifest', type=Path, required=True)
    verify.add_argument('--build', type=Path, required=True)
    verify.add_argument('--reports', type=Path, nargs='+', required=True)
    verify.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    try:
        manifest = read_report(args.manifest) if args.manifest else None
        if args.command == 'build':
            result = build_verifier(args.go, args.out, manifest=manifest,
                                    dependencies=args.dependencies, cache=args.cache)
            print(json.dumps({'status': result['status'], 'evidence_class': result['evidence_class'],
                              'report': str(args.out / 'report.json'), 'release_qualified': False}))
            return
        # Preserve every inspection, including rejection; never overwrite earlier evidence.
        with args.out.open('xb') as output:
            try:
                result = verify_provider_reports(manifest, args.build, args.reports)
            except (OSError, ValueError, KeyError, TypeError) as error:
                result = {'status': 'invalid_evidence', 'release_gate_passed': False, 'reason': str(error)}
            output.write(release.encoded(result))
            output.flush()
            os.fsync(output.fileno())
        if result['status'] == 'invalid_evidence':
            parser.exit(1, result['reason'] + '\n')
        print(json.dumps({'status': result['status'], 'release_gate_passed': False, 'report': str(args.out)}))
        if result['status'] == 'incomplete':
            raise SystemExit(2)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        parser.exit(1, str(error) + '\n')


if __name__ == '__main__':
    main()
