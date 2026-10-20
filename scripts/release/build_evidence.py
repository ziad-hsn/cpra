#!/usr/bin/env python3
"""Inspect original Go, generation and dashboard results for a frozen release.

Reading these records executes no tests and grants no publication permission.
Independent review must authenticate the original executions.
"""
import argparse
from datetime import datetime
import math
import os
from pathlib import Path
import stat

import browser_evidence
import dependencies
from evidence import read_report
from release import RECIPE, encoded, evidence_identity
import sdk_evidence
import worker_evidence as worker


MINIMUM_COMPILER = worker.MINIMUM_COMPILER
MAX_LOG_BYTES = worker.MAX_LOG_BYTES
MAX_LINE_BYTES = worker.MAX_LINE_BYTES
MAX_SOURCE_FILES = worker.MAX_SOURCE_FILES
MAX_TEST_EVENTS = 100000
MAX_CAMPAIGN_SECONDS = 5 * 60 * 60
ENVIRONMENT = dict(worker.ENVIRONMENT)
ENVIRONMENT.pop('CPRA_WORKER_LOAD')
ENVIRONMENT.update(CPRA_WORKER_PROCESS_RACE='1', CPRA_RUN_CLI_INTEGRATION='1')
VARIANTS = (
    ('application-default', '.', RECIPE['module'], ''),
    ('application-builtins', '.', RECIPE['module'], ' '.join(RECIPE['build_tags'])),
    ('application-external', '.', RECIPE['module'], 'externaljobs'),
    ('application-builtins-external', '.', RECIPE['module'], ' '.join(RECIPE['build_tags']) + ' externaljobs'),
    ('sdk-default', 'sdk/go', sdk_evidence.CORE, ''),
    ('sdk-external', 'sdk/go', sdk_evidence.CORE, 'externaljobs'),
    ('worker-external', 'sdk/go/worker', sdk_evidence.WORKER, 'externaljobs'),
)
GENERATION_CHECKS = ('format', 'sdk-generation', 'brand-generation', 'dashboard-schema', 'dashboard-install',
                     'dashboard-audit', 'dashboard-freshness', 'dashboard-types', 'dashboard-lint',
                     'dashboard-tests', 'dashboard-rebuild')
SDK_OUTPUTS = {'sdk/go/' + name for name in (
    'api/generated_base.go', 'api/generated_external.go',
    'internal/transport/generated_base.go', 'internal/transport/generated_external.go',
    'api/schema_base.json', 'api/schema_external.json', 'api/schema_base.go', 'api/schema_external.go',
    'testdata/operations.json', 'generation.json')}
HELPERS = {
    (RECIPE['module'], 'TestMainWorkerServerProcessHelper'),
    (RECIPE['module'] + '/internal/controller', 'TestExecutorProcessLockProbe'),
    (RECIPE['module'] + '/internal/localadmin', 'TestAuthenticationProcessHelper'),
    (RECIPE['module'] + '/internal/localadmin', 'TestWorkerAuthenticationProcessHelper'),
    (RECIPE['module'] + '/internal/management', 'TestReselectionSpoolCrashChild'),
    (RECIPE['module'] + '/internal/management', 'TestStageProcessChild'),
    (RECIPE['module'] + '/internal/management', 'TestCollectionValidationWorkerRestartChild'),
    (RECIPE['module'] + '/internal/persistence', 'TestAuthenticationProcessHelper'),
    (RECIPE['module'] + '/internal/persistence', 'TestRestoreResetProcessHelper'),
    (RECIPE['module'] + '/internal/persistence', 'TestWorkerExpiryProcessHelper'),
    (RECIPE['module'] + '/internal/persistence', 'TestWorkerQueueProcessHelper'),
    (RECIPE['module'] + '/internal/persistence', 'TestWorkerResultProcessCrashHelper'),
    (RECIPE['module'] + '/internal/persistence', 'TestWorkerScheduleProcessHelper'),
    (sdk_evidence.WORKER, 'TestServerProcessHelper'),
}
COMPANION_TESTS = {
    (package, name): 'browser' for package, names in browser_evidence.TESTS.items() for name in names
}
COMPANION_TESTS.update({
    (RECIPE['module'], 'TestMainWorkerLoadSmoke'): 'external_worker',
    (RECIPE['module'], 'TestMainWorkerLoad'): 'external_worker',
    (RECIPE['module'] + '/internal/httpserver', 'TestManagementBrowser'): 'browser',
})


def require(condition, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent build matrix evidence: ' + detail)


def source_inputs(manifest):
    expected = manifest['source_files']
    required = {'go.mod', 'go.sum', 'Makefile', 'main.go', 'sdk/go/go.mod', 'sdk/go/go.sum',
                'sdk/go/worker/go.mod', 'sdk/go/worker/go.sum', 'tools/sdkgen/go.mod',
                'tools/sdkgen/go.sum', 'scripts/release/build_campaign.py',
                'scripts/release/build_evidence.py', 'dashboard/package.json',
                'dashboard/pnpm-lock.yaml', 'dashboard/build-manifest.json'}
    required |= SDK_OUTPUTS
    require(required <= expected.keys(), 'frozen source omits matrix inputs')
    require(len(expected) <= MAX_SOURCE_FILES and all(
        isinstance(record, dict) and record.get('mode') in ('100644', '100755')
        and browser_evidence.digest(record.get('sha256')) for record in expected.values()),
        'source inventory is not bounded regular files')
    return expected


def source_snapshot(root):
    """Include unexpected files; exclude only Git and generated dependency caches."""
    root = Path(root).absolute()
    require(root.resolve(strict=True) == root, 'source directory contains a link or alias')
    values = {}
    def failed(error):
        raise error
    for current, directories, files in os.walk(root, followlinks=False, onerror=failed):
        directories[:] = sorted(name for name in directories if name not in ('.git', '__pycache__', 'node_modules')
                                and (Path(current) / name).relative_to(root).as_posix() != 'dashboard/dist')
        require(all(not (Path(current) / name).is_symlink() for name in directories), 'source contains a linked directory')
        for name in sorted(files):
            path = Path(current) / name
            relative = path.relative_to(root).as_posix()
            if name.endswith('.pyc'):
                continue
            mode = path.lstat().st_mode
            require(stat.S_ISREG(mode), 'source input is not regular: ' + relative)
            value, _ = browser_evidence.hash_file(path, worker.MAX_SOURCE_BYTES)
            values[relative] = {'mode': '100755' if mode & 0o111 else '100644', 'sha256': value}
            require(len(values) <= MAX_SOURCE_FILES, 'source inventory exceeds its file limit')
    return values


def commands(go, tags):
    selection = ['-tags=' + tags] if tags else []
    return {
        'packages': [str(go), 'list', '-json', *selection, './...'],
        'vet': [str(go), 'vet', '-race', *selection, './...'],
        'tests': [str(go), 'test', '-json', '-count=1', '-timeout=45m', '-race', *selection, './...'],
    }


def package_inventory(path, module):
    rows = worker.module_records(worker.bounded_text(path, MAX_LOG_BYTES))
    packages = {}
    for row in rows:
        name = row.get('ImportPath')
        require(isinstance(name, str) and (name == module or name.startswith(module + '/'))
                and row.get('Module', {}).get('Path') == module and row.get('Incomplete') is not True,
                'package selection crosses a module boundary or contains an error')
        require(name not in packages, 'duplicate selected package')
        files = row.get('TestGoFiles', []) + row.get('XTestGoFiles', [])
        require(isinstance(files, list) and all(isinstance(name, str) and name.endswith('_test.go') for name in files),
                'package test-file inventory is malformed')
        packages[name] = bool(files)
        require(len(packages) <= MAX_SOURCE_FILES, 'package selection exceeds its record limit')
    require(packages, 'empty package selection')
    return packages


def test_results(path, packages, tags):
    terminal, running, completed, skips, test_elapsed = {}, set(), {}, [], {}
    minimum_seconds = 0
    total = 0
    with path.open('rb') as stream:
        while line := stream.readline(MAX_LINE_BYTES + 1):
            total += len(line)
            require(len(line) <= MAX_LINE_BYTES and total <= MAX_LOG_BYTES, 'raw Go output exceeds its byte limit')
            event = browser_evidence.parse_event(line)
            package, name, action = event.get('Package'), event.get('Test'), event['Action']
            require(package in packages and (name is None or isinstance(name, str) and name),
                    'raw Go output has unexpected package or test coverage')
            require(action not in ('fail', 'build-fail'), 'raw Go output contains a failure')
            key = (package, name)
            if name is not None and action == 'run':
                require(key not in running and key not in completed, 'test was started more than once')
                running.add(key)
                require(len(running) + len(completed) <= MAX_TEST_EVENTS, 'test event inventory exceeds its limit')
            if action not in ('pass', 'skip'):
                continue
            elapsed = event.get('Elapsed', 0)
            require(type(elapsed) in (int, float) and math.isfinite(elapsed) and elapsed >= 0,
                    'Go completion has an invalid duration')
            if name is None:
                require(package not in terminal and (action == 'pass' or not packages[package]),
                        'duplicate package completion or a skipped test package')
                require(not any(p == package for p, _ in running), 'package completed with unfinished tests')
                require(elapsed + 0.001 >= max(test_elapsed.get(package, [0])), 'package duration contradicts its tests')
                minimum_seconds = max(minimum_seconds, elapsed)
                terminal[package] = action
            else:
                require(key in running and key not in completed and package not in terminal,
                        'test completion is duplicated or has no execution')
                running.remove(key)
                completed[key] = action
                test_elapsed.setdefault(package, []).append(elapsed)
                if action == 'skip':
                    exemption = ('subprocess_helper' if key in HELPERS else COMPANION_TESTS.get(key))
                    if name == 'TestStartupSourceValidationRejectsUnavailableDriverWithoutOpeningProviders' \
                            and package == RECIPE['module'] + '/internal/management' \
                            and set(RECIPE['build_tags']) <= set(tags.split()):
                        exemption = 'all_builtin_drivers_present'
                    skips.append({'package': package, 'test': name, 'covered_by': exemption})
    require(set(terminal) == set(packages) and not running, 'selected packages did not all complete')
    require(all(not tested or any(p == package for p, _ in completed) for package, tested in packages.items()),
            'a selected test package ran no tests')
    return {'packages': len(packages), 'minimum_seconds': minimum_seconds,
            'passed': sum(value == 'pass' for value in completed.values()),
            'skipped': skips, 'unverified_skips': [row for row in skips if row['covered_by'] is None]}


def frontend_results(path):
    report = read_report(path)
    total = report.get('numTotalTests')
    require(type(total) is int and total > 0 and report.get('success') is True
            and type(report.get('numPassedTests')) is int
            and report.get('numPassedTests') == total
            and all(type(report.get(field)) is int and report[field] == 0 for field in
                    ('numFailedTests', 'numPendingTests', 'numTodoTests')),
            'dashboard tests failed, skipped, or did not execute')
    rows = report.get('testResults')
    require(isinstance(rows, list) and rows and all(isinstance(row, dict) and row.get('status') == 'passed'
            and isinstance(row.get('assertionResults'), list) and row['assertionResults']
            and all(case.get('status') == 'passed' for case in row['assertionResults']) for row in rows),
            'original dashboard case results are incomplete')
    require(sum(len(row['assertionResults']) for row in rows) == total, 'dashboard count contradicts its cases')
    return {'tests': total, 'files': len(rows)}


def check_environment():
    environment = dict(ENVIRONMENT, GOPROXY='https://proxy.golang.org', CGO_ENABLED='0', CI='true',
                       NODE_TLS_REJECT_UNAUTHORIZED='1', npm_config_strict_ssl='true')
    for name in ('CPRA_WORKER_PROCESS_RACE', 'CPRA_RUN_CLI_INTEGRATION'):
        environment.pop(name)
    return environment


def check_commands(go, programs, directory, source):
    python, pnpm = programs['python'], programs['pnpm']
    return {
        'format': ('.', [str(Path(go).parent / 'gofmt'), '-l', *sorted(name for name in source if name.endswith('.go'))]),
        'sdk-generation': ('.', [python, '-B', 'scripts/sdk/regenerate_check.py', '--go', go,
                                '--out', str(directory / 'generation.json')]),
        'brand-generation': ('.', [python, '-B', 'scripts/brand/generate.py', '--check']),
        'dashboard-schema': ('.', [python, '-B', 'scripts/dashboard/generate_api.py', '--check']),
        'dashboard-install': ('dashboard', [pnpm, 'install', '--frozen-lockfile', '--prod=false']),
        # In pinned pnpm, --prod/--dev select ONLY that class. Clearing both
        # selectors includes both classes; optional packages stay included.
        'dashboard-audit': ('dashboard', [pnpm, 'audit', '--json', '--audit-level=low',
                                         '--only=', '--prod=false', '--dev=false', '--optional=true',
                                         '--ignore-registry-errors=false', '--ignore-unfixable=false']),
        'dashboard-freshness': ('.', [python, '-B', 'scripts/dashboard/asset_manifest.py']),
        'dashboard-types': ('dashboard', [pnpm, 'exec', 'tsc', '--noEmit']),
        'dashboard-lint': ('dashboard', [pnpm, 'run', 'lint']),
        'dashboard-tests': ('dashboard', [pnpm, 'exec', 'vitest', 'run', '--reporter=json',
                                          '--outputFile=' + str(directory / 'dashboard-tests.json')]),
        'dashboard-rebuild': ('.', [python, '-B', 'scripts/release/dashboard_build.py', '--check',
                                    '--pnpm', pnpm, '--go', go]),
    }


def verify_checks(report, manifest, path, remember):
    directory = path.with_name(path.stem + '.artifacts')
    programs = report.get('programs')
    require(isinstance(programs, dict) and set(programs) == {'python', 'pnpm', 'node'}
            and all(isinstance(value, str) and Path(value).is_absolute() for value in programs.values()),
            'generation/dashboard executables are missing')
    frontend = report.get('frontend')
    require(isinstance(frontend, dict) and frontend.get('node') == RECIPE['frontend']['node_version']
            and frontend.get('pnpm') == RECIPE['frontend']['pnpm_version'], 'frontend tools are not pinned')
    checks = report['checks']
    require(isinstance(checks, list) and len(checks) == len(GENERATION_CHECKS), 'generation/dashboard coverage is incomplete')
    commands = check_commands(report['compiler'], programs, directory, manifest['source_files'])
    for row, name in zip(checks, GENERATION_CHECKS):
        relative, command = commands[name]
        require(isinstance(row, dict) and row.get('name') == name and row.get('directory') == relative
                and row.get('command') == command and row.get('environment') == check_environment()
                and row.get('source_changed') == [] and type(row.get('exit_code')) is int and row['exit_code'] == 0,
                'generation/dashboard command, policy or result differs')
        for channel in ('stdout', 'stderr'):
            record = row.get(channel)
            require(isinstance(record, dict) and browser_evidence.digest(record.get('sha256'))
                    and worker.integer(record.get('bytes')), 'generation raw output is missing')
            retained = browser_evidence.retained_path(path.parent, record.get('file'))
            require(retained == directory / (name + '-' + channel + '.log'), 'generation raw output belongs to another step')
            _, size = remember(retained, MAX_LOG_BYTES, record['sha256'])
            require(size == record['bytes'], 'generation output size differs')
        if name == 'format':
            require(row['stdout']['bytes'] == 0 and row['stderr']['bytes'] == 0, 'Go sources are unformatted')
        if name == 'dashboard-audit':
            frontend_audit(directory / (name + '-stdout.log'))
    payloads = report.get('check_results')
    require(isinstance(payloads, dict) and set(payloads) == {'generation.json', 'dashboard-tests.json'},
            'original generation or dashboard results are missing')
    for name, record in payloads.items():
        require(isinstance(record, dict) and browser_evidence.digest(record.get('sha256'))
                and worker.integer(record.get('bytes')), 'generation result identity is invalid')
        _, size = remember(directory / name, worker.MAX_REPORT_BYTES, record['sha256'])
        require(size == record['bytes'], 'generation result size differs')
    generation = read_report(directory / 'generation.json')
    require(generation.get('status') == 'pass' and generation.get('sourceRoots') == 2
            and generation.get('comparedToCheckedInOutputs') is True
            and generation.get('compiler') == 'go version ' + RECIPE['go_version'] + ' linux/amd64'
            and isinstance(generation.get('outputs'), dict) and set(generation['outputs']) == SDK_OUTPUTS
            and all(browser_evidence.digest(value) and manifest['source_files'].get(name, {}).get('sha256') == value
                    for name, value in generation['outputs'].items()),
            'generation did not reproduce all frozen SDK outputs from two roots')
    dashboard = frontend_results(directory / 'dashboard-tests.json')
    require(report.get('dashboard_tests') == dashboard, 'dashboard summary contradicts its original cases')
    return dashboard


def frontend_audit(path):
    audit = read_report(path)
    metadata = audit.get('metadata')
    classes = ('dependencies', 'devDependencies', 'optionalDependencies', 'totalDependencies')
    require(isinstance(metadata, dict)
            and all(type(metadata.get(name)) is int and metadata[name] > 0 for name in classes)
            and all(metadata[name] <= metadata['totalDependencies'] for name in classes),
            'frontend dependency audit inventory is missing or excludes a required dependency class')
    levels = metadata.get('vulnerabilities')
    require(audit.get('advisories') == {} and isinstance(levels, dict)
            and set(levels) == {'info', 'low', 'moderate', 'high', 'critical'}
            and all(type(count) is int and count == 0 for count in levels.values()),
            'frontend dependency audit is incomplete or contains advisories')


def verify_build_campaigns(manifest, paths):
    identity = evidence_identity(manifest)
    require(manifest['recipe'] == RECIPE, 'build recipe differs from the frozen release')
    require(len(paths) == 2 and len(set(map(lambda path: Path(path).absolute(), paths))) == 2,
            'two distinct compiler campaigns are required')
    expected_source = source_inputs(manifest)
    inputs, limits, compilers, results, outstanding = {}, {}, set(), [], []
    sdk_identity = None

    def remember(path, limit=worker.MAX_REPORT_BYTES, expected=None):
        path = Path(path).absolute()
        require(path.resolve(strict=True) == path, 'retained input contains a link or alias')
        value, size = browser_evidence.hash_file(path, limit)
        require(expected is None or expected == value, 'retained input changed: ' + path.name)
        require(str(path) not in inputs or inputs[str(path)] == value, 'input changed during inspection')
        inputs[str(path)], limits[str(path)] = value, limit
        return value, size

    for path in map(lambda item: Path(item).absolute(), paths):
        remember(path)
        report = read_report(path)
        require(type(report.get('schema_version')) is int and report['schema_version'] == 1
                and report.get('result') == 'passed' and report.get('release_qualified') is False
                and report.get('release_identity') == identity and report.get('source_changed') == []
                and report.get('release_changed') is False
                and report.get('workspace') == 'off', 'campaign is failed, unfrozen or uses another release')
        compiler = report.get('go')
        require(compiler in (MINIMUM_COMPILER, RECIPE['go_version']) and compiler not in compilers,
                'minimum/release compiler is missing or duplicated')
        require(report.get('environment') == ENVIRONMENT and report.get('race') is True
                and report.get('platform') == 'linux/amd64' and browser_evidence.digest(report.get('compiler_sha256')),
                'compiler policy, race instrumentation or platform differs')
        host = report.get('host')
        require(isinstance(host, dict) and host.get('system') == 'Linux' and host.get('machine') in ('x86_64', 'amd64')
                and isinstance(host.get('os_version'), str) and 0 < len(host['os_version']) <= 512,
                'native execution host is missing')
        try:
            recorded = datetime.fromisoformat(report['recorded_at'].replace('Z', '+00:00'))
        except (KeyError, ValueError, TypeError, AttributeError) as error:
            raise ValueError('Invalid build recording timestamp') from error
        require(recorded.utcoffset() is not None and type(report.get('seconds')) in (int, float)
                and math.isfinite(report['seconds']) and 0 <= report['seconds'] <= MAX_CAMPAIGN_SECONDS,
                'campaign duration or recording time is invalid')
        source = browser_evidence.retained_path(path.parent, report.get('source_inventory'))
        require(browser_evidence.digest(report.get('source_inventory_sha256')), 'source inventory digest is missing')
        remember(source, expected=report.get('source_inventory_sha256'))
        require(read_report(source) == expected_source, 'recorded source differs from frozen source')
        go = report.get('compiler')
        require(isinstance(go, str) and Path(go).is_absolute(), 'recorded compiler is not absolute')
        directory = path.with_name(path.stem + '.artifacts')
        require(browser_evidence.digest(report.get('release_metadata_sha256')), 'retained release metadata digest is missing')
        remember(directory / 'RELEASE.json', expected=report['release_metadata_sha256'])
        require(read_report(directory / 'RELEASE.json') == manifest, 'original release metadata differs from selected release')
        module_subset = dict(report, module_inputs={name: record for name, record in report.get('module_inputs', {}).items()
            if name not in ('sdk-modules.jsonl', 'sdk-verify.log')})
        module = worker.module_inputs(manifest, module_subset, directory, remember)
        current_sdk = {key: module[key] for key in ('Path', 'Version', 'Sum', 'GoModSum', 'ArchiveSHA256')}
        require(sdk_identity is None or current_sdk == sdk_identity, 'SDK dependency differs between compiler campaigns')
        sdk_identity = current_sdk
        for name in ('sdk-modules.jsonl', 'sdk-verify.log'):
            record = report['module_inputs'].get(name)
            require(isinstance(record, dict) and browser_evidence.digest(record.get('sha256'))
                    and worker.integer(record.get('bytes')), 'SDK graph or verification log is missing')
            _, size = remember(directory / name, MAX_LOG_BYTES, record['sha256'])
            require(size == record['bytes'], 'SDK module log size differs')
        dependencies.dependency_graph(worker.module_records(worker.bounded_text(directory / 'sdk-modules.jsonl', MAX_LOG_BYTES)),
                                      sdk_evidence.CORE, {})
        require(worker.bounded_text(directory / 'sdk-verify.log').strip() == 'all modules verified',
                'SDK module contents were not verified')
        rows = report.get('variants')
        require(isinstance(rows, list) and len(rows) == len(VARIANTS), 'module/tag matrix is incomplete')
        variant_results = []
        for row, (name, relative, module, tags) in zip(rows, VARIANTS):
            require(isinstance(row, dict) and row.get('name') == name and row.get('directory') == relative
                    and row.get('tags') == tags and row.get('commands') == commands(go, tags),
                    'variant command narrows coverage or changes driver tags')
            expected_env = dict(ENVIRONMENT, CPRA_CLI_BUILD_TAGS=tags)
            require(row.get('environment') == expected_env and row.get('source_changed') == [],
                    'variant environment or source changed')
            require(set(row.get('logs', {})) == {'packages', 'vet', 'tests'}, 'variant raw outputs are missing')
            for action in ('packages', 'vet', 'tests'):
                step = row['logs'][action]
                require(isinstance(step, dict) and type(step.get('exit_code')) is int and step['exit_code'] == 0,
                        'a matrix process did not complete successfully')
                for channel in ('stdout', 'stderr'):
                    record = step.get(channel)
                    require(isinstance(record, dict) and browser_evidence.digest(record.get('sha256'))
                            and worker.integer(record.get('bytes')), 'raw output identity is invalid')
                    retained = browser_evidence.retained_path(path.parent, record.get('file'))
                    require(retained == directory / (name + '-' + action + '-' + channel + '.log'),
                            'raw output belongs to another matrix step')
                    _, size = remember(retained, MAX_LOG_BYTES, record['sha256'])
                    require(size == record['bytes'], 'raw output size differs')
            require(worker.bounded_text(directory / (name + '-vet-stdout.log'), MAX_LOG_BYTES).strip() == ''
                    and worker.bounded_text(directory / (name + '-vet-stderr.log'), MAX_LOG_BYTES).strip() == '',
                    'vet produced diagnostics')
            packages = package_inventory(directory / (name + '-packages-stdout.log'), module)
            result = test_results(directory / (name + '-tests-stdout.log'), packages, tags)
            require(row.get('tests') == result, 'test summary contradicts original completion events')
            outstanding.extend(dict(row, variant=name, compiler=compiler) for row in result['unverified_skips'])
            variant_results.append({'name': name, 'race': True, **result})
        require(report['seconds'] + 0.05 >= sum(row['minimum_seconds'] for row in variant_results),
                'recorded wall time contradicts sequential test executions')
        # Generation and dashboard execution are part of this gate. A Go-only
        # record is useful evidence but cannot satisfy the complete matrix.
        checks = report.get('checks', [])
        if compiler == RECIPE['go_version']:
            if checks:
                verify_checks(report, manifest, path, remember)
            else:
                outstanding.append({'compiler': compiler, 'requirement': 'generation and dashboard checks'})
        else:
            require(checks == [], 'generation must use the recorded release compiler')
        compilers.add(compiler)
        results.append({'report': str(path), 'compiler': compiler, 'variants': variant_results})
    require(compilers == {MINIMUM_COMPILER, RECIPE['go_version']}, 'compiler coverage is incomplete')
    for name, value in inputs.items():
        remember(Path(name), limits[name], value)
    return {'schema_version': 1, 'status': 'incomplete' if outstanding else 'consistent_pending_review',
            'scope': 'frozen Go module/tag/race, generation and dashboard input consistency',
            'release_identity': identity, 'release_gate_passed': False, 'input_sha256': inputs,
            'reports': results, 'unverified': outstanding,
            'companion_requirements': ['browser', 'external_worker', 'security', 'independent_review'],
            'remaining': ['Authenticate original execution evidence and complete independent review.',
                          'Complete native platform, browser, security and aggregate release requirements.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--campaign', type=Path, action='append', required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    out = args.out.absolute()
    require(out.resolve(strict=False) == out and out not in [path.absolute() for path in [args.release, *args.campaign]],
            'output aliases or replaces an input')
    result = verify_build_campaigns(read_report(args.release), args.campaign)
    out.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with out.open('xb') as stream:
        stream.write(encoded(result))
        stream.flush()
        os.fsync(stream.fileno())
    print(result['status'])


if __name__ == '__main__':
    main()
