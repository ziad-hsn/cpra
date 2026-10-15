#!/usr/bin/env python3
"""Record the complete frozen Go matrix and release-compiler frontend checks.

This is private verification. It runs no fleet benchmark, endurance campaign,
configured provider verification, signing, tagging or publication operation.
"""
import argparse
from datetime import datetime, timezone
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import tempfile
import time

import build_evidence as build
import dependencies
from evidence import read_report
from release import encoded, evidence_identity
import worker_campaign
import worker_evidence as worker
import security_evidence as security
import management_evidence as management
import a2a_evidence as a2a


# Go's 45-minute test timeout applies to each package binary. An all-package
# command also includes compilation and packages waiting for a test slot.
TEST_COMMAND_SECONDS = 90 * 60


def fingerprint(path, parent=None):
    value, size = build.browser_evidence.hash_file(path, build.MAX_LOG_BYTES)
    record = {'sha256': value, 'bytes': size}
    if parent is not None:
        record['file'] = path.relative_to(parent).as_posix()
    return record


def executable(name):
    path = Path(shutil.which(name) or name).absolute().resolve(strict=True)
    build.require(path.is_file() and os.access(path, os.X_OK), 'missing executable: ' + path.name)
    return path


def record_security(args, manifest, root, artifacts, compiler, go, environment, capture, report, progress):
    """Record exact exclusion commands and symbol-level scanner messages."""
    python = executable('python3')
    section = {'python': str(python), 'scans': []}
    report['security'] = section
    output = artifacts / 'worker-exclusion.json'
    command = [str(python), str(root / 'scripts/release/verify_worker_exclusion.py'),
               '--go', str(go), '--workspace', 'off', '--out', str(output)]
    section['exclusion'] = {'command': command}
    progress()
    # Exclusion builds intentionally omit the matrix's trimpath/race settings.
    # Retaining both object sets inflated the local matrix's scratch footprint.
    # capture stops the child process group before this owned cache is removed;
    # original logs and exclusion evidence remain outside the disposable cache.
    with tempfile.TemporaryDirectory(prefix='exclusion-cache-', dir=artifacts) as cache:
        isolated = dict(environment, GOCACHE=cache)
        section['exclusion'].update(capture(command, '.', 'security-exclusion', isolated, 1800))
    if output.is_file():
        section['exclusion']['report'] = fingerprint(output, args.out.absolute().parent)
    progress()
    security.require(section['exclusion']['exit_code'] == 0, 'worker build exclusion failed')
    security.verify_exclusion(read_report(output), manifest, compiler, str(go))
    if compiler == build.MINIMUM_COMPILER:
        return
    security.require(args.govulncheck is not None, '--security requires --govulncheck for the release compiler')
    scanner = executable(args.govulncheck)
    section['scanner'] = str(scanner)
    shutil.copyfile(scanner, artifacts / 'govulncheck')
    (artifacts / 'govulncheck').chmod(0o600)
    section['scanner_binary'] = fingerprint(artifacts / 'govulncheck', args.out.absolute().parent)
    tool = capture([str(go), 'version', '-m', str(scanner)], '.', 'scanner-build-info', environment, 20)
    security.require(tool['exit_code'] == 0, 'scanner build identity is unavailable')
    section['scanner_build_info'] = tool['stdout']
    security.verify_scanner_identity(worker.bounded_text(artifacts / 'scanner-build-info-stdout.log'), str(scanner))
    env = dict(environment, **security.SCAN_ENVIRONMENT)
    for name, relative, module, tags in build.VARIANTS:
        selection = [str(go), 'list', '-json', *(['-tags=' + tags] if tags else []), './...']
        row = {'name': name, 'directory': relative, 'tags': tags, 'environment': security.SCAN_ENVIRONMENT,
               'command': security.scan_command(scanner, tags), 'packages': {'command': selection}}
        section['scans'].append(row)
        progress()
        row['packages'].update(capture(selection, relative, name + '-scan-packages', env, 180))
        security.require(row['packages']['exit_code'] == 0, 'scanner package selection failed: ' + name)
        row.update(capture(row['command'], relative, name + '-vulnerabilities', env, 1200))
        progress()
        security.require(row['exit_code'] == 0, 'vulnerability analysis failed: ' + name)
        packages = build.package_inventory(artifacts / (name + '-scan-packages-stdout.log'), module)
        role = name.split('-')[0]
        modules = {value['Path']: value.get('Version', '') for value in worker.module_records(
            worker.bounded_text(artifacts / (role + '-modules.jsonl'), build.MAX_LOG_BYTES))}
        row['findings'] = security.scan_results(artifacts / (name + '-vulnerabilities-stdout.log'), compiler, packages, modules)
        progress()
        security.require(fingerprint(scanner)['sha256'] == section['scanner_binary']['sha256'],
                         'scanner binary changed during execution')
        security.require(not row['findings']['called'], 'reachable vulnerabilities were found: ' + name)


def record_a2a(args, manifest, root, artifacts, environment, capture, report, progress):
    """Build the pinned local reference server and record original protocol tests."""
    go = str(executable(args.a2a_reference_go))
    binary = artifacts / 'a2a-agent'
    section = {'compiler': go, 'directory': a2a.REFERENCE_DIRECTORY, 'binary_path': str(binary),
               'environment': a2a.REFERENCE_ENVIRONMENT, 'source_files': {}, 'commands': []}
    report['a2a_reference'] = section
    directory = artifacts / 'a2a-source'
    directory.mkdir(mode=0o700)
    for name in a2a.REFERENCE_FILES:
        original, target = root / name, directory / Path(name).name
        source = fingerprint(original)
        a2a.require(source['sha256'] == manifest['source_files'].get(name, {}).get('sha256'), 'reference source differs')
        shutil.copyfile(original, target)
        target.chmod(0o600)
        section['source_files'][name] = fingerprint(target, args.out.absolute().parent)
        a2a.require(section['source_files'][name]['sha256'] == source['sha256'], 'reference source changed during capture')
    commands = a2a.reference_commands(go)
    commands['build'] = [go, 'build', *manifest['recipe']['build_flags'], '-o', str(binary), '.']
    commands['build-info'][-1] = str(binary)
    env = dict(environment, **a2a.REFERENCE_ENVIRONMENT)
    env['PATH'] = str(Path(go).parent) + os.pathsep + environment['PATH']
    for name, command in commands.items():
        row = {'name': name, 'command': command}
        section['commands'].append(row)
        progress()
        row.update(capture(command, a2a.REFERENCE_DIRECTORY, 'a2a-reference-' + name, env, 600))
        progress()
        a2a.require(row['exit_code'] == 0, 'reference ' + name + ' failed')
        if name == 'environment':
            a2a.require(read_report(artifacts / 'a2a-reference-environment-stdout.log') == {
                'GOVERSION': manifest['recipe']['go_version'], 'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64',
                'GOOS': 'linux', 'GOARCH': 'amd64', 'GOWORK': 'off'}, 'reference requires the production compiler and native Linux amd64')
    section['binary'] = fingerprint(binary, args.out.absolute().parent)
    policy = dict(a2a.REFERENCE_TEST_ENVIRONMENT, CPRA_A2A_AGENT=str(binary))
    env = dict(environment, **policy)
    section['tests'] = {'environment': policy}
    for name, command in a2a.reference_test_commands(report['compiler']).items():
        row = {'command': command}
        section['tests'][name] = row
        progress()
        row.update(capture(command, '.', 'a2a-interop-' + name, env, 300))
        progress()
        a2a.require(row['exit_code'] == 0, 'upstream interoperability ' + name + ' failed')
    a2a.require(fingerprint(binary, args.out.absolute().parent) == section['binary'], 'reference executable changed')
    after = build.source_snapshot(root)
    section['source_changed'] = sorted(name for name in set(after) | set(manifest['source_files'])
                                      if after.get(name) != manifest['source_files'].get(name))
    a2a.require(not section['source_changed'], 'source changed during upstream interoperability')


def campaign(args):
    root, out = args.source_dir.absolute(), args.out.absolute()
    artifacts = out.with_name(out.stem + '.artifacts')
    dependency_directory = args.dependencies.absolute()
    build.require(out.resolve(strict=False) == out and dependency_directory.resolve(strict=False) == dependency_directory
                  and not out.is_relative_to(root) and not out.is_relative_to(dependency_directory),
                  'output must be separate from canonical source/dependency paths')
    build.require(not out.exists() and not artifacts.exists(), 'output exists; choose a new campaign path')
    release_path = args.release.absolute()
    build.require(release_path.resolve(strict=True) == release_path, 'release metadata contains a link or alias')
    release_before = fingerprint(release_path)
    manifest = read_report(release_path)
    identity = evidence_identity(manifest)
    expected = build.source_inputs(manifest)
    build.require(build.source_snapshot(root) == expected, 'source differs from the frozen release')
    go = executable(args.go)
    cache = dependencies.cache_environment(dependency_directory, manifest)
    environment = {key: os.environ[key] for key in ('PATH', 'HOME', 'TMP', 'TEMP', 'TMPDIR', 'GOCACHE') if key in os.environ}
    environment.update(build.ENVIRONMENT, **cache)
    environment['PATH'] = str(go.parent) + os.pathsep + environment.get('PATH', '')
    tool = worker.decode_report(subprocess.check_output([str(go), 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH'],
                                                        env=environment, text=True, timeout=20), 'matrix compiler')
    compiler = tool.get('GOVERSION')
    host = {'system': platform.system(), 'machine': platform.machine(), 'os_version': platform.platform()}
    build.require(compiler in (build.MINIMUM_COMPILER, manifest['recipe']['go_version'])
                  and (tool.get('GOOS'), tool.get('GOARCH')) == ('linux', 'amd64')
                  and host['system'] == 'Linux' and host['machine'] in ('x86_64', 'amd64'),
                  'matrix requires a selected compiler on native Linux amd64')
    report = {'schema_version': 1, 'result': 'running', 'release_qualified': False,
              'release_identity': identity, 'go': compiler, 'compiler': str(go),
              'compiler_sha256': build.browser_evidence.hash_file(go, worker.MAX_SOURCE_BYTES)[0],
              'platform': 'linux/amd64', 'host': host, 'race': True, 'workspace': 'off',
              'recorded_at': datetime.now(timezone.utc).isoformat(),
              'scope': 'Frozen Go compatibility and frontend execution; native packaging, providers and performance are separate.',
              'environment': dict(build.ENVIRONMENT), 'source_changed': [], 'release_changed': False,
              'variants': [], 'checks': []}
    out.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    started = time.monotonic()
    with os.fdopen(os.open(out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w+b') as stream:
        worker_campaign.write_report(stream, report)
        try:
            artifacts.mkdir(mode=0o700)
            shutil.copyfile(release_path, artifacts / 'RELEASE.json')
            (artifacts / 'RELEASE.json').chmod(0o600)
            report['release_metadata_sha256'] = fingerprint(artifacts / 'RELEASE.json')['sha256']
            build.require(report['release_metadata_sha256'] == release_before['sha256']
                          and read_report(artifacts / 'RELEASE.json') == manifest, 'release metadata changed during capture')
            source = artifacts / 'source.json'
            source.write_bytes(encoded(expected))
            source.chmod(0o600)
            report.update(source_inventory=source.relative_to(out.parent).as_posix(),
                          source_inventory_sha256=fingerprint(source)['sha256'])
            report['contract_inputs'] = management.capture_contracts(root, manifest, artifacts, out.parent)
            dependency = read_report(dependency_directory / dependencies.REPORT)
            sdk = dependency['private_modules'][worker.sdk_evidence.CORE]
            report['sdk_module'] = {name: sdk[name] for name in ('Path', 'Version', 'Sum', 'GoModSum', 'ArchiveSHA256')}
            cache_path = Path(cache['GOMODCACHE']) / 'cache/download' / sdk['Path'] / '@v'
            for name, suffix in (('sdk.zip', '.zip'), ('sdk.mod', '.mod'), ('sdk.info', '.info')):
                shutil.copyfile(cache_path / (sdk['Version'] + suffix), artifacts / name)
                (artifacts / name).chmod(0o600)
            report['module_inputs'] = {name: fingerprint(artifacts / name) for name in ('sdk.zip', 'sdk.mod', 'sdk.info')}

            def capture(command, relative, name, env, timeout):
                remaining = build.MAX_CAMPAIGN_SECONDS - (time.monotonic() - started)
                build.require(remaining > 0, 'matrix exceeded its five-hour process deadline')
                output, errors = (artifacts / (name + '-' + channel + '.log') for channel in ('stdout', 'stderr'))
                status = worker_campaign.capture(command, root / relative, env, output, errors, min(timeout, remaining))
                return {'exit_code': status, 'stdout': fingerprint(output, out.parent),
                        'stderr': fingerprint(errors, out.parent)}

            for role, directory in (('application', '.'), ('sdk', 'sdk/go'), ('worker', 'sdk/go/worker')):
                for action, arguments, name in (('graph', ['list', '-m', '-json', 'all'], role + '-modules.jsonl'),
                                                 ('verify', ['mod', 'verify'], role + '-verify.log')):
                    result = capture([str(go), *arguments], directory, role + '-' + action, environment, 120)
                    build.require(result['exit_code'] == 0, 'module inspection or verification failed: ' + role)
                    original = build.browser_evidence.retained_path(out.parent, result['stdout']['file'])
                    shutil.copyfile(original, artifacts / name)
                    report['module_inputs'][name] = fingerprint(artifacts / name)

            # Reject affected dependencies before the long-running correctness
            # matrix. Keep the original scanner messages in failed evidence.
            if getattr(args, 'security', False):
                record_security(args, manifest, root, artifacts, compiler, go, environment, capture, report,
                                lambda: worker_campaign.write_report(stream, report))

            for name, relative, _module, tags in build.VARIANTS:
                invocation = build.commands(go, tags)
                policy = dict(build.ENVIRONMENT, CPRA_CLI_BUILD_TAGS=tags)
                env = dict(environment, **policy)
                row = {'name': name, 'directory': relative, 'tags': tags, 'commands': invocation,
                       'environment': policy, 'source_changed': [], 'logs': {}}
                report['variants'].append(row)
                worker_campaign.write_report(stream, report)
                for action, timeout in (('packages', 180), ('vet', 900), ('tests', TEST_COMMAND_SECONDS)):
                    row['logs'][action] = capture(invocation[action], relative, name + '-' + action, env, timeout)
                    worker_campaign.write_report(stream, report)
                    build.require(row['logs'][action]['exit_code'] == 0, name + ': ' + action + ' failed')
                packages = build.package_inventory(artifacts / (name + '-packages-stdout.log'), _module)
                row['tests'] = build.test_results(artifacts / (name + '-tests-stdout.log'), packages, tags)
                after = build.source_snapshot(root)
                row['source_changed'] = sorted(path for path in set(expected) | set(after) if expected.get(path) != after.get(path))
                build.require(not row['source_changed'], 'source changed during ' + name)
                worker_campaign.write_report(stream, report)

            if getattr(args, 'a2a_reference_go', None):
                record_a2a(args, manifest, root, artifacts, environment, capture, report,
                           lambda: worker_campaign.write_report(stream, report))

            if compiler == manifest['recipe']['go_version']:
                programs = {'python': str(executable('python3')), 'node': str(executable('node')), 'pnpm': str(executable(args.pnpm))}
                report['programs'] = programs
                frontend_environment = {name: environment[name] for name in (
                    'PATH', 'HOME', 'TMP', 'TEMP', 'TMPDIR', 'GOCACHE', 'GOMODCACHE', 'GOPATH') if name in environment}
                frontend_environment.update(build.check_environment())
                report['frontend'] = {name: subprocess.check_output([programs[name], '--version'],
                    env=frontend_environment, text=True, timeout=20).strip() for name in ('node', 'pnpm')}
                build.require(report['frontend'] == {'node': manifest['recipe']['frontend']['node_version'],
                              'pnpm': manifest['recipe']['frontend']['pnpm_version']}, 'frontend compiler versions differ')
                commands = build.check_commands(str(go), programs, artifacts, expected)
                for name in build.GENERATION_CHECKS:
                    relative, command = commands[name]
                    row = {'name': name, 'directory': relative, 'command': command,
                           'environment': build.check_environment(), 'source_changed': []}
                    report['checks'].append(row)
                    worker_campaign.write_report(stream, report)
                    row.update(capture(command, relative, name, frontend_environment, 1800))
                    worker_campaign.write_report(stream, report)
                    build.require(row['exit_code'] == 0, name + ' failed')
                    if name == 'format':
                        build.require(row['stdout']['bytes'] == 0 and row['stderr']['bytes'] == 0, 'Go sources are unformatted')
                    after = build.source_snapshot(root)
                    row['source_changed'] = sorted(path for path in set(expected) | set(after) if expected.get(path) != after.get(path))
                    build.require(not row['source_changed'], 'source changed during ' + name)
                report['check_results'] = {name: fingerprint(artifacts / name)
                                          for name in ('generation.json', 'dashboard-tests.json')}
                report['dashboard_tests'] = build.frontend_results(artifacts / 'dashboard-tests.json')
            report['result'] = 'passed'
        except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, KeyboardInterrupt) as error:
            report.update(result='failed', error=str(error) or 'matrix was interrupted')
        finally:
            try:
                after = build.source_snapshot(root)
                report['source_changed'] = sorted(path for path in set(expected) | set(after) if expected.get(path) != after.get(path))
                if report['source_changed']:
                    report.update(result='failed', error='matrix source changed')
            except (OSError, ValueError) as error:
                report.update(result='failed', source_snapshot_error=str(error), error='final source inspection failed')
            try:
                report['release_changed'] = fingerprint(release_path) != release_before
                if report['release_changed']:
                    report.update(result='failed', error='release metadata changed')
            except (OSError, ValueError) as error:
                report.update(result='failed', release_changed=True, error='final metadata inspection failed: ' + str(error))
            report['seconds'] = round(time.monotonic() - started, 3)
            worker_campaign.write_report(stream, report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--source-dir', type=Path, required=True)
    parser.add_argument('--dependencies', type=Path, required=True)
    parser.add_argument('--go', required=True)
    parser.add_argument('--pnpm', default='pnpm')
    parser.add_argument('--security', action='store_true', help='also record build exclusion and production-compiler vulnerability checks')
    parser.add_argument('--govulncheck', help='govulncheck v1.7.0 binary built with the production compiler')
    parser.add_argument('--a2a-reference-go', help='production compiler for the pinned local upstream A2A fixture')
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    def interrupted(_number, _frame):
        raise InterruptedError('matrix received a termination request')
    previous = signal.signal(signal.SIGTERM, interrupted)
    try:
        result = campaign(args)
    finally:
        signal.signal(signal.SIGTERM, previous)
    print(result['result'])
    return 0 if result['result'] == 'passed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
