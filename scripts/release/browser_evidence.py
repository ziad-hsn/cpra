#!/usr/bin/env python3
"""Inspect retained browser campaigns against one frozen release source.

This reader establishes input consistency and required browser coverage. It
does not authenticate evidence, complete accessibility review, or authorize
publication. The aggregate release gate and independent review remain separate.
"""
import argparse
from datetime import datetime
import hashlib
import json
import math
import os
from pathlib import Path, PurePosixPath
import re
import sys

from evidence import read_report
from release import RECIPE, encoded, evidence_identity

MINIMUM_COMPILER = RECIPE['minimum_go_version']
MAX_METADATA_BYTES = 16 * 1024 ** 2
MAX_LOG_BYTES = 64 * 1024 ** 2
MAX_LINE_BYTES = 1024 * 1024
MAX_TERMINAL_EVENTS = 10000
TESTS = {
    'github.com/ziad-hsn/cpra': (
        'TestMainManagementControlsBrowser', 'TestMainManagementRecoveryReviewBrowser',
        'TestMainManagementCollectionBrowser', 'TestMainManagementCollectionExecutionBrowser',
        'TestMainManagementCollectionActivationBrowser', 'TestMainManagementCollectionReselectionBrowser',
        'TestMainManagementCollectionContinuationBrowser',
    ),
    'github.com/ziad-hsn/cpra/internal/httpserver': ('TestManagementBrowser',),
}
SCREENSHOTS = {
    'operator-review.png', 'reader-operations.png', 'reader-monitor-state.png',
    'reader-incidents.png', 'reader-metrics.png', 'mobile-reader-incidents.png',
    'mobile-reader-metrics.png',
}


def require(condition, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent browser evidence: ' + detail)


def digest(value):
    return isinstance(value, str) and re.fullmatch('[a-f0-9]{64}', value) is not None


def hash_file(path, limit):
    require(not path.is_symlink() and path.is_file(), 'input is not a regular file: ' + path.name)
    require(path.stat().st_size <= limit, 'input exceeds its byte limit: ' + path.name)
    value = hashlib.sha256()
    size = 0
    with path.open('rb') as stream:
        while block := stream.read(64 * 1024):
            size += len(block)
            require(size <= limit, 'input grew beyond its byte limit: ' + path.name)
            value.update(block)
    return value.hexdigest(), size


def retained_path(directory, name):
    require(isinstance(name, str) and name and str(PurePosixPath(name)) == name
            and not PurePosixPath(name).is_absolute() and '..' not in PurePosixPath(name).parts
            and '\\' not in name and ':' not in name, 'unsafe retained input path')
    path = directory
    for part in PurePosixPath(name).parts:
        path = path / part
        require(not path.is_symlink(), 'retained input has a symlink component')
    return path


def source_inputs(manifest):
    """Derive the producer's required inventory from frozen regular source."""
    prefixes = ('internal/', 'cmd/', 'sdk/go/', 'api/', 'examples/',
                'scripts/dashboard/', 'scripts/release/')
    exact = {'go.mod', 'go.sum', '.github/workflows/ci.yml',
             'dashboard/build-manifest.json', 'brand/palette.json', 'brand/dist/palette.css',
             'LICENSES/dashboard.json', 'LICENSES/dashboard.txt'}
    selected = {}
    for name, record in manifest['source_files'].items():
        if record['mode'] not in ('100644', '100755'):
            continue
        path = PurePosixPath(name)
        frontend = (name.startswith(('dashboard/src/', 'dashboard/public/'))
                    or path.parent == PurePosixPath('dashboard')
                    and path.suffix in ('.json', '.yaml', '.ts', '.js', '.html')
                    and not path.name.endswith('.tsbuildinfo'))
        if (name.startswith(prefixes) or name in exact or frontend
                or '/' not in name and name.endswith('.go')):
            selected[name] = record['sha256']
    require(exact <= selected.keys() and 'main.go' in selected
            and 'main_actions_browser_test.go' in selected
            and 'internal/httpserver/management_browser_test.go' in selected,
            'frozen source is missing browser or dashboard inputs')
    return selected


def parse_event(line):
    def fields(pairs):
        values = {}
        for key, value in pairs:
            require(key not in values, 'duplicate raw Go event field')
            values[key] = value
        return values

    def constant(_):
        require(False, 'non-finite raw Go event value')

    def number(raw):
        value = float(raw)
        require(math.isfinite(value), 'non-finite raw Go event value')
        return value

    try:
        event = json.loads(line, object_pairs_hook=fields, parse_constant=constant, parse_float=number)
    except (UnicodeError, json.JSONDecodeError) as error:
        raise ValueError('Invalid raw browser Go event') from error
    require(isinstance(event, dict) and event.get('Action') in {
        'start', 'run', 'pause', 'cont', 'output', 'pass', 'fail', 'skip', 'build-output', 'build-fail',
    }, 'raw Go output is not a recognized event object')
    for field in ('Package', 'Test', 'Output'):
        require(field not in event or isinstance(event[field], str), 'invalid raw Go event field type')
    require(event['Action'] != 'build-fail', 'browser package did not compile')
    return event


def terminal_results(path, dashboard_sha256=None, compiler=None):
    events = []
    terminal = {}
    required = {(package, name) for package, names in TESTS.items() for name in names}
    required.update((package, None) for package in TESTS)
    total = 0
    served = []
    with path.open('rb') as stream:
        while line := stream.readline(MAX_LINE_BYTES + 1):
            total += len(line)
            require(len(line) <= MAX_LINE_BYTES and total <= MAX_LOG_BYTES, 'raw Go output exceeds its byte limit')
            event = parse_event(line)
            if (event['Action'] == 'output'
                    and event.get('Package') == 'github.com/ziad-hsn/cpra/internal/httpserver'
                    and event.get('Test') == 'TestManagementBrowser'
                    and 'dashboard assets index sha256=' in event.get('Output', '')):
                match = re.search(r'dashboard assets index sha256=([a-f0-9]{64}); compiler=(go[0-9.]+); '
                                  r'platform=([a-z0-9]+/[a-z0-9]+)', event['Output'])
                require(match is not None, 'served dashboard/compiler observation is malformed')
                served.append(match.groups())
            if event['Action'] not in ('pass', 'fail', 'skip'):
                continue
            require(event['Action'] == 'pass', 'raw Go output contains failed or skipped coverage')
            package, name = event.get('Package'), event.get('Test')
            require(package in TESTS and (name is None or name.split('/')[0] in TESTS[package]),
                    'raw Go output contains unexpected test coverage')
            key = (package, name)
            require(key not in terminal, 'duplicate terminal raw Go event')
            require(type(event.get('Elapsed')) in (int, float)
                    and math.isfinite(event['Elapsed']) and event['Elapsed'] >= 0,
                    'terminal Go event has no valid elapsed time')
            terminal[key] = 'pass'
            events.append(event)
            require(len(events) <= MAX_TERMINAL_EVENTS, 'too many terminal browser events')
    require(required <= terminal.keys(), 'required browser test or package completion is missing')
    if dashboard_sha256 is not None:
        require(len(served) == 1 and served[0][:2] == (dashboard_sha256, compiler),
                'served dashboard index or compiled runtime differs from the frozen source')
    return events


def verify_browser_campaigns(manifest, paths):
    identity = evidence_identity(manifest)
    require(manifest['recipe'] == RECIPE, 'release uses a different build recipe')
    require(len(paths) == 4 and len({str(Path(path).absolute()) for path in paths}) == 4,
            'four distinct compiler/tag reports are required')
    expected_source = source_inputs(manifest)
    expected_matrix = {(compiler, tags, compiler != MINIMUM_COMPILER)
                       for compiler in (MINIMUM_COMPILER, RECIPE['go_version'])
                       for tags in ('', 'externaljobs')}
    observed = set()
    inputs, limits, reports, compiler_tools = {}, {}, [], {}
    browser_tool = None

    def remember(path, limit=MAX_METADATA_BYTES):
        value, size = hash_file(path, limit)
        key = str(path.absolute())
        require(key not in inputs or inputs[key] == value, 'input changed during inspection')
        inputs[key], limits[key] = value, limit
        return value, size

    for path in map(Path, paths):
        require(path.suffix == '.json', 'browser report must use a JSON filename')
        report_hash, _ = remember(path)
        report = read_report(path)
        require(report.get('result') == 'passed' and report.get('release_qualified') is False
                and type(report.get('exit_code')) is int and report['exit_code'] == 0
                and report.get('source_changed') == [], 'browser command did not complete on unchanged inputs')
        require(report.get('source_commit') == manifest['commit'], 'browser source commit differs from the frozen release')
        require(report.get('workspace') == {'path': 'off', 'sha256': None},
                'development workspace evidence cannot qualify frozen-source browser coverage')
        require(report.get('required_tests') == {package: list(names) for package, names in TESTS.items()},
                'reported browser requirements differ from required coverage')
        require(type(report.get('race')) is bool and isinstance(report.get('go'), str)
                and isinstance(report.get('tags'), str), 'invalid browser compiler/tag identity')
        variant = (report['go'], report['tags'], report['race'])
        require(variant in expected_matrix and variant not in observed, 'missing, duplicate or unsupported compiler/tag coverage')
        observed.add(variant)
        command = report.get('command')
        pattern = '^(' + '|'.join(name for names in TESTS.values() for name in names) + ')$'
        expected_command = ['test', '-count=1', '-timeout=45m', '-json', '-tags=' + report['tags'], '-run', pattern]
        if report['race']:
            expected_command.append('-race')
        expected_command.extend(('.', './internal/httpserver'))
        require(isinstance(command, list) and len(command) == len(expected_command) + 1
                and isinstance(command[0], str) and Path(command[0]).is_absolute()
                and command[1:] == expected_command, 'recorded browser invocation differs from required coverage')
        require(report.get('node') == RECIPE['frontend']['node_version']
                and report.get('playwright') == '1.56.1'
                and isinstance(report.get('chromium'), str) and report['chromium'].strip(),
                'browser tooling identity is missing or differs from its pins')
        tools = report.get('tool_sha256')
        require(isinstance(tools, dict) and set(tools) == {'go', 'node', 'chromium'}
                and all(digest(value) for value in tools.values()), 'browser tool digests are incomplete')
        require(report['go'] not in compiler_tools or compiler_tools[report['go']] == tools['go'],
                'the same compiler identity used different tool bytes')
        compiler_tools[report['go']] = tools['go']
        current_browser = (report['chromium'], tools['chromium'], tools['node'])
        require(browser_tool is None or browser_tool == current_browser, 'browser or Node bytes changed between matrix entries')
        browser_tool = current_browser
        try:
            recorded = datetime.fromisoformat(report.get('recorded_at', '').replace('Z', '+00:00'))
        except (ValueError, TypeError, AttributeError) as error:
            raise ValueError('Browser report timestamp is invalid') from error
        require(recorded.utcoffset() is not None and type(report.get('seconds')) in (int, float)
                and math.isfinite(report['seconds']) and report['seconds'] > 0,
                'browser report has no bounded elapsed duration or timestamp offset')

        stem = path.stem + '.artifacts/'
        require(report.get('source_inventory') == stem + 'source.json', 'source inventory path differs from retained layout')
        source_path = retained_path(path.parent, report['source_inventory'])
        require(remember(source_path)[0] == report.get('source_inventory_sha256'), 'retained source inventory digest changed')
        require(read_report(source_path) == expected_source, 'browser input hashes differ from the complete frozen source inventory')
        expected_logs = {stem + 'go.jsonl', stem + 'stderr.log'}
        logs = report.get('logs')
        require(isinstance(logs, dict) and set(logs) == expected_logs, 'raw browser logs were not retained')
        for name, record in logs.items():
            payload = retained_path(path.parent, name)
            value, size = remember(payload, MAX_LOG_BYTES)
            require(isinstance(record, dict) and type(record.get('bytes')) is int
                    and record['bytes'] == size and record.get('sha256') == value, 'raw browser log bytes changed')
        require('internal/httpserver/assets/index.html' in expected_source, 'frozen embedded dashboard index is missing')
        events = terminal_results(retained_path(path.parent, stem + 'go.jsonl'),
                                  expected_source['internal/httpserver/assets/index.html'], report['go'])
        require(report.get('test_results') == events, 'browser summary contradicts the retained raw Go results')

        images = report.get('screenshots')
        require(isinstance(images, dict) and set(images) == {stem + 'screenshots/' + name for name in SCREENSHOTS},
                'required browser screenshots were not retained')
        for name, record in images.items():
            image = retained_path(path.parent, name)
            value, size = remember(image)
            require(isinstance(record, dict) and type(record.get('bytes')) is int
                    and record['bytes'] == size and size >= 8 and record.get('sha256') == value,
                    'browser screenshot bytes changed')
            with image.open('rb') as stream:
                require(stream.read(8) == b'\x89PNG\r\n\x1a\n', 'browser screenshot is not PNG data')
        reports.append({'report': str(path.absolute()), 'sha256': report_hash,
                        'compiler': report['go'], 'tags': report['tags'], 'race': report['race']})
    require(observed == expected_matrix, 'minimum or release compiler/tag coverage is missing')
    for name, value in inputs.items():
        require(hash_file(Path(name), limits[name])[0] == value, 'input changed during browser evidence inspection')
    return {'schema_version': 1, 'status': 'consistent_pending_review',
            'scope': 'retained frozen-source browser campaigns', 'release_gate_passed': False,
            'release_identity': identity, 'reports': reports, 'input_sha256': inputs,
            'remaining': ['Independent review and authentication of the retained evidence.',
                          'Complete accessibility and the other candidate, provider, platform and comparative-performance gates.',
                          'Aggregate release gate and authorized publication/readback.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--campaign', type=Path, action='append', required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with os.fdopen(os.open(args.out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'wb') as output:
        try:
            manifest_digest = hash_file(args.release, MAX_METADATA_BYTES)[0]
            report = verify_browser_campaigns(read_report(args.release), args.campaign)
            require(hash_file(args.release, MAX_METADATA_BYTES)[0] == manifest_digest, 'release manifest changed during inspection')
            report['input_sha256'][str(args.release.absolute())] = manifest_digest
        except (ValueError, OSError, RuntimeError) as error:
            output.write(encoded({'schema_version': 1, 'status': 'invalid_evidence',
                                  'release_gate_passed': False, 'error': str(error)}))
            output.flush()
            os.fsync(output.fileno())
            print(str(error), file=sys.stderr)
            return 1
        output.write(encoded(report))
        output.flush()
        os.fsync(output.fileno())
    print(report['status'])
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
