#!/usr/bin/env python3
"""Inspect retained external-worker process and load evidence for frozen source.

This checks original inputs and results; independent review authenticates them.
It never runs workers, accesses providers, or authorizes publication.
"""
import argparse
from datetime import datetime
import math
import os
import json
from pathlib import Path
import re
import stat

import browser_evidence
import dependencies
import sdk_evidence
from evidence import decode_report, read_report
from release import RECIPE, encoded, evidence_identity


PACKAGE = 'github.com/ziad-hsn/cpra'
MINIMUM_COMPILER = RECIPE['minimum_go_version']
TESTS = ('TestMainWorkerProcesses', 'TestMainWorkerLoad')
PREFIXES = ('internal/', 'cmd/', 'sdk/go/', 'api/', 'examples/', 'scripts/release/')
EXACT = {'go.mod', 'go.sum', '.github/workflows/ci.yml', '.github/workflows/release.yml'}
LIMITS = {'Concurrency': 16, 'Records': 4096, 'LiveBytes': 256 << 20,
          'OutcomeBytes': 128 << 10, 'DiagnosticBytes': 64 << 10, 'AllocatedBytes': 512 << 20}
ENVIRONMENT = {'GOENV': 'off', 'GOTOOLCHAIN': 'local', 'GOWORK': 'off',
               'GOFLAGS': '-mod=readonly -pgo=off -trimpath -buildvcs=false -p=2',
               'GOEXPERIMENT': '', 'CGO_ENABLED': '1', 'GOPROXY': 'off', 'GOSUMDB': 'sum.golang.org',
               'GONOSUMDB': '', 'GONOPROXY': '', 'GOPRIVATE': '', 'CPRA_WORKER_LOAD': '1',
               'GOMAXPROCS': '2', 'GOMEMLIMIT': '768MiB', 'GOVCS': '*:off',
               'GOAMD64': 'v1', 'GOARM64': 'v8.0', 'TZ': 'UTC'}
MAX_LOG_BYTES = 64 << 20
MAX_LINE_BYTES = 1 << 20
MAX_REPORT_BYTES = 16 << 20
MAX_SOURCE_BYTES = 256 << 20
MAX_SOURCE_FILES = 10000


def require(condition, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent worker evidence: ' + detail)


def selected(name):
    return name.startswith(PREFIXES) or name in EXACT or '/' not in name and name.endswith('.go')


def source_inputs(manifest):
    expected = {name: record for name, record in manifest['source_files'].items() if selected(name)}
    required = EXACT | {'main.go', 'main_worker_process_test.go', 'main_worker_capacity_test.go',
                        'main_worker_load_test.go', 'sdk/go/go.mod', 'sdk/go/go.sum',
                        'sdk/go/worker/go.mod', 'sdk/go/worker/go.sum',
                        'sdk/go/worker/server_process_test.go', 'scripts/release/worker_campaign.py',
                        'scripts/release/worker_evidence.py'}
    require(required <= expected.keys(), 'frozen source omits worker campaign inputs')
    require(len(expected) <= MAX_SOURCE_FILES and all(record['mode'] in ('100644', '100755')
                                                   for record in expected.values()),
            'worker source must be bounded regular files')
    return expected


def source_snapshot(root):
    """Include added inputs as well as changes; workspaces and output are excluded."""
    root = Path(root).absolute()
    require(root.resolve(strict=True) == root, 'source directory contains a link or alias')
    names = set(EXACT)
    names.update(path.name for path in root.iterdir() if path.name.endswith('.go'))
    for prefix in PREFIXES:
        directory = root / prefix
        require(directory.is_dir() and not directory.is_symlink(), 'source directory is missing: ' + prefix)
        for current, directories, files in os.walk(directory, followlinks=False):
            directories[:] = sorted(name for name in directories if name not in ('.git', '__pycache__'))
            require(all(not (Path(current) / name).is_symlink() for name in directories),
                    'source directory contains a link')
            names.update((Path(current) / name).relative_to(root).as_posix() for name in files
                         if not name.endswith('.pyc'))
            require(len(names) <= MAX_SOURCE_FILES, 'source inventory exceeds its file limit')
    values = {}
    for name in sorted(names):
        path = root / name
        mode = path.lstat().st_mode
        require(stat.S_ISREG(mode), 'source input is not regular: ' + name)
        value, _ = browser_evidence.hash_file(path, MAX_SOURCE_BYTES)
        values[name] = {'mode': '100755' if mode & 0o111 else '100644', 'sha256': value}
    return values


def process_cases():
    cases = set()
    modes = ('normal', 'start-reply', 'handler', 'result-request', 'result-reply',
             'server-restart', 'uncooperative', 'late-evidence', 'revoked')
    for category in ('check', 'recovery', 'notification'):
        for mode in modes:
            if category != 'check' or mode != 'late-evidence':
                cases.add('TestMainWorkerProcesses/' + category + '/' + mode)
        if category == 'check':
            cases.add('TestMainWorkerProcesses/check/outbox')
        limits = ('live-bytes', 'allocated-bytes')
        if category != 'check':
            limits += ('unknown-bytes', 'unknown-records')
        cases.update('TestMainWorkerProcesses/capacity/' + category + '/' + limit for limit in limits)
    return cases


def command(go, race):
    return [str(go), 'test', '-p=2', '-count=1', '-timeout=15m', '-json', '-tags=externaljobs',
            '-run', '^(' + '|'.join(TESTS) + ')$', *(['-race'] if race else []), '.']


def integer(value, minimum=0):
    return type(value) is int and value >= minimum


def load_measurement(value):
    require(isinstance(value, dict), 'mixed-load measurement is not an object')
    seconds = value.get('duration_seconds')
    require(type(seconds) in (float, int) and math.isfinite(seconds) and 300 <= seconds <= 360,
            'mixed load did not measure five actual minutes')
    require(value.get('limits') == LIMITS and type(value.get('monitors')) is int and value['monitors'] == 64,
            'mixed load changed the default journal limits or monitor count')
    for field in ('duplicate_invocations', 'unreconciled_outcomes'):
        require(type(value.get(field)) is int and value[field] == 0, field + ' is not zero')
    totals = value.get('target_invocations')
    require(isinstance(totals, dict) and set(totals) == {'check', 'recovery', 'notification'}
            and all(integer(count, 64) for count in totals.values())
            and totals['check'] >= 64 * int(seconds / 30), 'independent target coverage is insufficient')
    require(type(value.get('target_peak_handlers')) is int and value['target_peak_handlers'] == 16,
            'independent target did not reach the configured concurrency')
    samples, saturated = value.get('status_samples'), value.get('saturated_samples')
    require(integer(samples, 240) and samples <= math.ceil(seconds) + 1 and integer(saturated)
            and samples >= saturated and saturated * 3 >= samples,
            'mixed load did not sustain measured concurrency pressure')
    require(integer(value.get('expired_check_receipts')) and value['expired_check_receipts'] <= totals['check'],
            'authoritative expired-check accounting is invalid')
    require(integer(value.get('expired_check_finalizations'))
            and value['expired_check_finalizations'] + value['expired_check_receipts'] <= totals['check'],
            'explicit expired-check finalization accounting is invalid')
    drained = value.get('drained')
    require(isinstance(drained, dict) and all(type(drained.get(field)) is int and drained[field] == 0
            for field in ('Records', 'ActiveHandlers', 'PendingOutcomes', 'UnknownActions', 'LiveBytes', 'ReservedBytes')),
            'accepted work or retained outcomes did not drain')
    require(drained.get('AdmissionAvailable') is True and integer(drained.get('AdmissionCapacity'), 1)
            and integer(drained.get('AllocatedBytes')) and drained['AllocatedBytes'] <= LIMITS['AllocatedBytes'],
            'drained journal remains unavailable or exceeds its allocation limit')
    windows = value.get('windows')
    require(isinstance(windows, list) and 9 <= len(windows) <= 12, 'load progress windows are incomplete')
    previous_time, previous_counts = 0, dict.fromkeys(totals, 0)
    for window in windows:
        require(isinstance(window, dict), 'invalid load progress window')
        elapsed, counts = window.get('elapsed_seconds'), window.get('invocations')
        require(type(elapsed) in (int, float) and math.isfinite(elapsed)
                and 30 <= elapsed - previous_time <= 40 and elapsed <= seconds,
                'load progress windows are unordered or stalled')
        require(isinstance(counts, dict) and set(counts) == set(totals)
                and all(integer(counts[kind]) and previous_counts[kind] < counts[kind] <= totals[kind]
                        for kind in totals), 'a handler category did not progress in a measurement window')
        require(integer(window.get('active_handlers')) and window['active_handlers'] <= 16,
                'measurement window exceeded worker concurrency')
        previous_time, previous_counts = elapsed, counts
    require(seconds - previous_time <= 40, 'the final measurement window is missing')
    require(value.get('release_qualified') is False and value.get('public_configuration_qualified') is False,
            'mixed-load fixtures were promoted to release or public configuration qualification')
    return value


def module_records(data):
    def fields(pairs):
        result = {}
        for name, value in pairs:
            require(name not in result, 'duplicate raw module field')
            result[name] = value
        return result

    def constant(_):
        require(False, 'non-finite raw module value')

    def finite(raw):
        value = float(raw)
        require(math.isfinite(value), 'non-finite raw module value')
        return value

    decoder = json.JSONDecoder(object_pairs_hook=fields, parse_constant=constant, parse_float=finite)
    require(len(data.encode()) <= MAX_LOG_BYTES, 'raw module graph exceeds its byte limit')
    records, position = [], 0
    while position < len(data):
        while position < len(data) and data[position].isspace():
            position += 1
        if position == len(data):
            break
        row, position = decoder.raw_decode(data, position)
        require(isinstance(row, dict) and 'Error' not in row and 'Replace' not in row,
                'raw module graph contains an error or replacement')
        records.append(row)
        require(len(records) <= MAX_SOURCE_FILES, 'module graph exceeds its record limit')
    return records


def bounded_text(path, limit=MAX_REPORT_BYTES):
    with path.open('rb') as stream:
        data = stream.read(limit + 1)
    require(len(data) <= limit, 'raw dependency output exceeds its byte limit')
    return data.decode('utf-8')


def module_inputs(manifest, report, directory, remember):
    module = report.get('sdk_module')
    require(isinstance(module, dict) and module.get('Path') == sdk_evidence.CORE
            and sdk_evidence.valid_version(module.get('Version'))
            and all(sdk_evidence.content_hash(module.get(key)) for key in ('Sum', 'GoModSum'))
            and browser_evidence.digest(module.get('ArchiveSHA256')), 'SDK dependency identity is missing')
    retained = report.get('module_inputs')
    names = {'sdk.zip', 'sdk.mod', 'sdk.info', 'application-modules.jsonl', 'worker-modules.jsonl',
             'application-verify.log', 'worker-verify.log'}
    require(isinstance(retained, dict) and set(retained) == names, 'original module inputs are missing')
    for name, record in retained.items():
        require(isinstance(record, dict) and browser_evidence.digest(record.get('sha256'))
                and integer(record.get('bytes')), 'module input fingerprint is invalid')
        _, size = remember(directory / name, MAX_LOG_BYTES, record['sha256'])
        require(size == record['bytes'], 'retained module input size differs')
    require(retained['sdk.zip']['sha256'] == module['ArchiveSHA256'], 'SDK archive digest differs')
    files = sdk_evidence.archive_contents(directory / 'sdk.zip', sdk_evidence.CORE, module['Version'], manifest)
    require(retained['sdk.mod']['sha256'] == files['go.mod'], 'SDK definition differs from its source archive')
    info = read_report(directory / 'sdk.info')
    require(info.get('Version') == module['Version'] and info.get('Time') == manifest['source_date'],
            'SDK module metadata differs from frozen version or source time')
    for role, path in (('application', RECIPE['module']), ('worker', sdk_evidence.WORKER)):
        raw = bounded_text(directory / (role + '-modules.jsonl'))
        rows = module_records(raw)
        dependencies.dependency_graph(rows, path, {sdk_evidence.CORE: module})
        selected_sdk = [row for row in rows if row.get('Path') == sdk_evidence.CORE]
        require(len(selected_sdk) == 1 and all(selected_sdk[0].get(key) == module[key]
                for key in ('Path', 'Version', 'Sum', 'GoModSum')), 'compiler selected another SDK dependency')
        require(bounded_text(directory / (role + '-verify.log')).strip() == 'all modules verified',
                'compiler did not verify cached module contents')
    return module


def raw_results(path, compiler, race):
    terminal, measurements, helpers = {}, [], {}
    output = {}
    expected = process_cases() | set(TESTS) | {None}
    total = 0
    with path.open('rb') as stream:
        while line := stream.readline(MAX_LINE_BYTES + 1):
            total += len(line)
            require(len(line) <= MAX_LINE_BYTES and total <= MAX_LOG_BYTES, 'raw Go output exceeds its byte limit')
            event = browser_evidence.parse_event(line)
            require(event.get('Package') == PACKAGE, 'raw Go output has an unexpected package')
            name = event.get('Test')
            require(name is None or name in expected, 'raw Go output has unexpected test coverage')
            if event['Action'] == 'output':
                text = event.get('Output', '')
                require(isinstance(text, str), 'raw Go output text is invalid')
                output.setdefault(name, []).append(text)
            if event['Action'] not in ('pass', 'fail', 'skip'):
                continue
            require(event['Action'] == 'pass', 'raw Go output contains failed or skipped coverage')
            require(name not in terminal, 'duplicate terminal raw Go event')
            elapsed = event.get('Elapsed')
            require(type(elapsed) in (int, float) and math.isfinite(elapsed) and elapsed >= 0,
                    'terminal Go event has no valid elapsed time')
            terminal[name] = elapsed
    require(set(terminal) == expected, 'process cases, load test, or package completion are missing')
    # test2json can split one output line across several events. Keep streams
    # separate by test so interleaved events cannot splice two observations.
    for name, chunks in output.items():
        for text in ''.join(chunks).split('\n'):
            require(len(text.encode('utf-8')) <= MAX_LINE_BYTES, 'assembled Go output line exceeds its byte limit')
            if 'SDK worker helper uses ' in text:
                match = re.search(r'SDK worker helper uses (go[0-9.]+); race instrumentation: (true|false)', text)
                require(match is not None and name in TESTS and name not in helpers,
                        'worker helper compiler observation is malformed or repeated')
                require(match.group(1) == compiler and (match.group(2) == 'true') == race,
                        'the separate worker helper used another compiler or race setting')
                helpers[name] = True
            if 'WORKER_LOAD_EVIDENCE ' in text:
                require(name == 'TestMainWorkerLoad', 'load measurement belongs to another test')
                measurements.append(load_measurement(decode_report(text.split('WORKER_LOAD_EVIDENCE ', 1)[1], 'worker load')))
    require(set(helpers) == set(TESTS), 'separate worker compiler observations are missing')
    require(len(measurements) == 1, 'exactly one raw mixed-load measurement is required')
    measurement = measurements[0]
    require(terminal['TestMainWorkerLoad'] >= measurement['duration_seconds'] and terminal[None] >= terminal['TestMainWorkerLoad'],
            'Go test completion contradicts the measured duration')
    return {'process_cases': sorted(process_cases()), 'load': measurement, 'test_elapsed': terminal['TestMainWorkerLoad']}


def verify_worker_campaigns(manifest, paths):
    identity = evidence_identity(manifest)
    require(manifest['recipe'] == RECIPE, 'release uses a different build recipe')
    require(len(paths) == 2 and len(set(map(lambda p: Path(p).absolute(), paths))) == 2,
            'exactly two distinct compiler reports are required')
    expected_source = source_inputs(manifest)
    inputs, limits, compilers, results, sdk_identity = {}, {}, set(), [], None

    def remember(path, limit=MAX_REPORT_BYTES, expected=None):
        path = Path(path).absolute()
        require(path.resolve(strict=True) == path, 'retained input contains a link or alias')
        value, size = browser_evidence.hash_file(path, limit)
        require(expected is None or expected == value, 'retained input changed: ' + path.name)
        require(str(path) not in inputs or inputs[str(path)] == value, 'input changed while being inspected')
        inputs[str(path)], limits[str(path)] = value, limit
        return value, size

    for path in map(lambda p: Path(p).absolute(), paths):
        remember(path)
        report = read_report(path)
        require(type(report.get('schema_version')) is int and report['schema_version'] == 1
                and report.get('result') == 'passed' and type(report.get('exit_code')) is int and report['exit_code'] == 0
                and report.get('release_qualified') is False and report.get('release_identity') == identity,
                'campaign did not pass for the selected frozen release')
        require(report.get('source_changed') == [] and report.get('workspace') == 'off'
                and report.get('required_tests') == list(TESTS), 'source, workspace or test selection differs')
        compiler = report.get('go')
        require(compiler in (MINIMUM_COMPILER, RECIPE['go_version']) and compiler not in compilers,
                'compiler coverage is duplicated, missing or unsupported')
        race = compiler != MINIMUM_COMPILER
        require(type(report.get('race')) is bool and report['race'] == race
                and report.get('platform') == 'linux/amd64', 'race or native platform coverage differs')
        host = report.get('host')
        require(isinstance(host, dict) and host.get('system') == 'Linux' and host.get('machine') in ('x86_64', 'amd64')
                and isinstance(host.get('os_version'), str) and 0 < len(host['os_version']) <= 512,
                'native host identity is missing or differs from the target')
        invocation = report.get('command')
        require(isinstance(invocation, list) and invocation and isinstance(invocation[0], str)
                and Path(invocation[0]).is_absolute() and invocation == command(invocation[0], race),
                'recorded invocation differs from required coverage')
        require(browser_evidence.digest(report.get('compiler_sha256')), 'compiler digest is missing')
        require(report.get('environment') == dict(ENVIRONMENT, CPRA_WORKER_PROCESS_RACE='1' if race else '0'),
                'ambient workspace, checksum or execution overrides are present')
        try:
            recorded = datetime.fromisoformat(report['recorded_at'].replace('Z', '+00:00'))
        except (KeyError, TypeError, ValueError, AttributeError) as error:
            raise ValueError('Invalid worker recording timestamp') from error
        seconds = report.get('seconds')
        require(recorded.utcoffset() is not None and type(seconds) in (int, float) and math.isfinite(seconds)
                and 300 <= seconds <= 1200, 'recording duration or time offset is invalid')
        source = browser_evidence.retained_path(path.parent, report.get('source_inventory'))
        require(browser_evidence.digest(report.get('source_inventory_sha256')), 'source inventory digest is missing')
        remember(source, expected=report.get('source_inventory_sha256'))
        require(read_report(source) == expected_source, 'recorded source differs from frozen worker inputs')
        directory = path.with_name(path.stem + '.artifacts')
        expected_logs = {(directory / name).relative_to(path.parent).as_posix() for name in ('go.jsonl', 'stderr.log')}
        logs = report.get('logs')
        require(isinstance(logs, dict) and set(logs) == expected_logs, 'raw Go logs are missing or unexpected')
        for name, record in logs.items():
            require(isinstance(record, dict) and browser_evidence.digest(record.get('sha256'))
                    and integer(record.get('bytes')), 'invalid raw log identity')
            value, size = remember(browser_evidence.retained_path(path.parent, name), MAX_LOG_BYTES, record['sha256'])
            require(size == record['bytes'], 'raw log size differs from its recorded size')
        result = raw_results(directory / 'go.jsonl', compiler, race)
        require(report.get('load') == result['load'], 'campaign summary contradicts the original load observation')
        module = module_inputs(manifest, report, directory, remember)
        current_sdk = {key: module[key] for key in ('Path', 'Version', 'Sum', 'GoModSum', 'ArchiveSHA256')}
        require(sdk_identity is None or sdk_identity == current_sdk, 'SDK dependency changed between compiler campaigns')
        sdk_identity = current_sdk
        require(seconds >= result['test_elapsed'], 'process wall time contradicts raw Go completion')
        compilers.add(compiler)
        results.append({'report': str(path), 'compiler': compiler, 'race': race, 'sdk_module': module, **result})
    require(compilers == {MINIMUM_COMPILER, RECIPE['go_version']}, 'minimum or release compiler coverage is missing')
    for name, value in inputs.items():
        remember(Path(name), limits[name], value)
    return {'schema_version': 1, 'status': 'consistent_pending_review',
            'scope': 'frozen external-worker process and five-minute load input consistency',
            'release_identity': identity, 'release_gate_passed': False, 'input_sha256': inputs, 'reports': results,
            'remaining': ['Authenticate original execution evidence and complete independent review.',
                          'Complete the other aggregate release requirements before publication.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--campaign', type=Path, action='append', required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    out = args.out.absolute()
    require(out.resolve(strict=False) == out and out not in [p.absolute() for p in [args.release, *args.campaign]],
            'output aliases or replaces an input')
    result = verify_worker_campaigns(read_report(args.release), args.campaign)
    out.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with out.open('xb') as stream:
        stream.write(encoded(result))
        stream.flush()
        os.fsync(stream.fileno())
    print(result['status'])


if __name__ == '__main__':
    main()
