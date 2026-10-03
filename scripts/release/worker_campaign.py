#!/usr/bin/env python3
"""Record the frozen external-worker crash/capacity and five-minute load tests.

Explicit invocation only. This invokes local process fixtures, never a fleet
benchmark, a 24-hour test, a publication operation or configured providers.
"""
import argparse
from datetime import datetime, timezone
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import time

import dependencies
from evidence import read_report
from release import encoded, evidence_identity
import worker_evidence as worker


POLICY = worker.ENVIRONMENT


def write_report(stream, value):
    data = encoded(value)
    worker.require(len(data) <= worker.MAX_REPORT_BYTES, 'campaign report exceeds its byte limit')
    stream.seek(0)
    stream.truncate()
    stream.write(data)
    stream.flush()
    os.fsync(stream.fileno())


def record(path):
    value, size = worker.browser_evidence.hash_file(path, worker.MAX_LOG_BYTES)
    return {'sha256': value, 'bytes': size}


def stop_group(process):
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        pass
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait(timeout=5)


def capture(command, root, environment, output, errors, timeout):
    """Retain bounded original output and terminate the fixture process group."""
    with output.open('xb') as stdout, errors.open('xb') as stderr:
        process = subprocess.Popen(command, cwd=root, env=environment, stdout=stdout, stderr=stderr,
                                   start_new_session=True)
        started = time.monotonic()
        try:
            while process.poll() is None:
                if time.monotonic() - started > timeout:
                    raise TimeoutError('worker fixture exceeded its process deadline')
                if any(path.stat().st_size > worker.MAX_LOG_BYTES for path in (output, errors)):
                    raise ValueError('worker fixture output exceeds its recording limit')
                time.sleep(0.25)
            worker.require(all(path.stat().st_size <= worker.MAX_LOG_BYTES for path in (output, errors)),
                           'worker fixture output exceeds its recording limit')
            return process.wait()
        finally:
            # A test panic can exit its parent before testing.T cleanup runs.
            # The session also owns any surviving server/worker fixtures.
            stop_group(process)


def campaign(args):
    root, out = args.source_dir.absolute(), args.out.absolute()
    artifacts = out.with_name(out.stem + '.artifacts')
    dependency_directory = args.dependencies.absolute()
    worker.require(dependency_directory.resolve(strict=False) == dependency_directory,
                   'dependency directory contains a link or alias')
    worker.require(out.resolve(strict=False) == out and not out.is_relative_to(root)
                   and not out.is_relative_to(dependency_directory),
                   'campaign output must be separate from source and dependency inputs')
    worker.require(not out.exists() and not artifacts.exists(), 'output exists; choose a new campaign path')
    manifest = read_report(args.release)
    identity = evidence_identity(manifest)
    expected = worker.source_inputs(manifest)
    worker.require(worker.source_snapshot(root) == expected, 'source differs from the frozen worker inputs')
    go = Path(shutil.which(args.go) or args.go).absolute()
    worker.require(go.resolve(strict=True) == go and go.is_file(), 'compiler must be a canonical executable')
    cache = dependencies.cache_environment(dependency_directory, manifest)
    environment = {key: os.environ[key] for key in ('PATH', 'HOME', 'TMP', 'TEMP', 'TMPDIR', 'GOCACHE') if key in os.environ}
    environment.update(POLICY, **cache)
    environment['PATH'] = str(go.parent) + os.pathsep + environment.get('PATH', '')
    observed = subprocess.check_output([str(go), 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH'],
                                       env=environment, text=True, timeout=20)
    tool = worker.decode_report(observed, 'worker compiler')
    compiler = tool.get('GOVERSION')
    host = {'system': platform.system(), 'machine': platform.machine(), 'os_version': platform.platform()}
    worker.require(compiler in (worker.MINIMUM_COMPILER, manifest['recipe']['go_version'])
                   and (tool.get('GOOS'), tool.get('GOARCH')) == ('linux', 'amd64')
                   and host['system'] == 'Linux' and host['machine'] in ('x86_64', 'amd64'),
                   'campaign requires the minimum or release compiler on native Linux amd64')
    race = compiler != worker.MINIMUM_COMPILER
    environment['CPRA_WORKER_PROCESS_RACE'] = '1' if race else '0'
    out.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    report = {'schema_version': 1, 'result': 'running', 'release_qualified': False,
              'release_identity': identity, 'go': compiler, 'compiler_sha256': worker.browser_evidence.hash_file(go, worker.MAX_SOURCE_BYTES)[0],
              'platform': 'linux/amd64', 'host': host, 'race': race, 'workspace': 'off', 'required_tests': list(worker.TESTS),
              'recorded_at': datetime.now(timezone.utc).isoformat(),
              'scope': 'Frozen local external-worker process and five-minute load fixtures; not fleet capacity or provider evidence.',
              'command': worker.command(go, race), 'environment': dict(POLICY, CPRA_WORKER_PROCESS_RACE='1' if race else '0'),
              'source_changed': []}
    started = time.monotonic()
    with os.fdopen(os.open(out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w+b') as stream:
        write_report(stream, report)
        try:
            artifacts.mkdir(mode=0o700)
            source = artifacts / 'source.json'
            source.write_bytes(encoded(expected))
            report.update(source_inventory=str(source.relative_to(out.parent)), source_inventory_sha256=record(source)['sha256'])
            dependency = read_report(dependency_directory / dependencies.REPORT)
            sdk = dependency['private_modules'][worker.sdk_evidence.CORE]
            report['sdk_module'] = {name: sdk[name] for name in ('Path', 'Version', 'Sum', 'GoModSum', 'ArchiveSHA256')}
            cache_path = Path(cache['GOMODCACHE']) / 'cache/download' / sdk['Path'] / '@v'
            for name, suffix in (('sdk.zip', '.zip'), ('sdk.mod', '.mod'), ('sdk.info', '.info')):
                payload = cache_path / (sdk['Version'] + suffix)
                shutil.copyfile(payload, artifacts / name)
                (artifacts / name).chmod(0o600)
            report['module_inputs'] = {name: record(artifacts / name) for name in ('sdk.zip', 'sdk.mod', 'sdk.info')}
            for role, directory in (('application', root), ('worker', root / 'sdk/go/worker')):
                for action, arguments, name in (('graph', ['list', '-m', '-json', 'all'], role + '-modules.jsonl'),
                                                 ('verify', ['mod', 'verify'], role + '-verify.log')):
                    output, errors = artifacts / name, artifacts / (role + '-' + action + '-stderr.log')
                    status = capture([str(go), *arguments], directory, environment, output, errors, 120)
                    worker.require(status == 0, 'compiler could not inspect or verify the ' + role + ' dependencies')
                    report['module_inputs'][name] = record(output)
            def remember(path, limit, expected):
                value, size = worker.browser_evidence.hash_file(path, limit)
                worker.require(value == expected, 'retained dependency changed')
                return value, size
            worker.module_inputs(manifest, report, artifacts, remember)
            write_report(stream, report)
            output, errors = artifacts / 'go.jsonl', artifacts / 'stderr.log'
            remaining = 1200 - (time.monotonic() - started)
            worker.require(remaining > 0, 'worker fixture exceeded its total deadline')
            report['exit_code'] = capture(report['command'], root, environment, output, errors, min(1000, remaining))
            report['logs'] = {str(path.relative_to(out.parent)): record(path) for path in (output, errors)}
            after = worker.source_snapshot(root)
            report['source_changed'] = sorted(name for name in set(expected) | set(after) if expected.get(name) != after.get(name))
            worker.require(report['exit_code'] == 0, 'worker test process failed')
            worker.require(not report['source_changed'], 'worker campaign source changed during execution')
            results = worker.raw_results(output, compiler, race)
            report['load'] = results['load']
            report['result'] = 'passed'
        except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, KeyboardInterrupt) as error:
            report.update(result='failed', error=str(error) or 'worker campaign was interrupted')
        finally:
            report['seconds'] = round(time.monotonic() - started, 3)
            write_report(stream, report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--source-dir', type=Path, required=True)
    parser.add_argument('--dependencies', type=Path, required=True)
    parser.add_argument('--go', required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    def interrupted(_number, _frame):
        raise InterruptedError('worker campaign received a termination request')

    previous = signal.signal(signal.SIGTERM, interrupted)
    try:
        result = campaign(args)
    finally:
        signal.signal(signal.SIGTERM, previous)
    print(result['result'])
    return 0 if result['result'] == 'passed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
