#!/usr/bin/env python3
"""Measure a retained alias fixture with the compiled validation-only path.

Run under the task's disk/process guard, one fixture at a time. A new output
directory is required. The 2,000,000 KiB RLIMIT_AS matches `ulimit -v 2000000`;
it limits virtual address space, not RSS. GNU time reports maximum RSS in KiB.
No result upgrades older measurements to a matched-compiler comparison.
"""
import argparse
import hashlib
import json
import os
import pathlib
import re
import resource
import selectors
import signal
import subprocess
import sys
import time


MAX_OUTPUT = 32768
VIRTUAL_KIB = 2000000


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def write_json(path, value):
    with path.open('x', encoding='utf-8') as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write('\n')
        stream.flush()
        os.fsync(stream.fileno())


def classify(exit_code, diagnostic, maximum_rss_kib, store_created, stop_reason):
    """This checks memory-scenario completion, not parsed fleet coverage."""
    quota = any(message in diagnostic for message in (
        'YAML aliases exceed the expansion or retained-anchor limit',
        'expanded monitor spool exceeds limit', 'expanded YAML entry exceeds limit'))
    accepted = 'supplied configuration valid; not applied to an existing catalog.' in diagnostic
    unsafe = any(message in diagnostic.lower() for message in ('fatal error:', 'panic:', 'out of memory'))
    if stop_reason:
        return {'status': 'incomplete', 'disposition': 'measurement_incomplete'}
    if unsafe or accepted and quota or store_created or maximum_rss_kib is None or maximum_rss_kib <= 0:
        return {'status': 'fail', 'disposition': 'unsafe_or_unmeasured_validation_result'}
    if exit_code == 0 and accepted:
        return {'status': 'pass', 'disposition': 'accepted_validation_within_limits'}
    if exit_code == 1 and quota:
        return {'status': 'pass', 'disposition': 'controlled_expansion_quota_rejection'}
    return {'status': 'fail', 'disposition': 'unexpected_validation_result'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=pathlib.Path, required=True)
    parser.add_argument('--binary-sha256', required=True)
    parser.add_argument('--build-record', type=pathlib.Path, required=True)
    parser.add_argument('--fixture', type=pathlib.Path, required=True)
    parser.add_argument('--out', type=pathlib.Path, required=True)
    args = parser.parse_args()
    binary, fixture = args.binary.resolve(strict=True), args.fixture.resolve(strict=True)
    if digest(binary) != args.binary_sha256 or args.build_record.stat().st_size > 1024**2:
        parser.error('binary digest or build record does not match')
    build = json.loads(args.build_record.read_text())
    command = build.get('command', [])
    try:
        output = pathlib.Path(command[command.index('-o') + 1])
        if (build.get('exit') != 0 or build.get('stop_reason') or not build.get('head') or not build.get('compiler') or
                command[1] != 'build' or not output.is_absolute() or output.resolve() != binary):
            raise ValueError()
    except (ValueError, IndexError, TypeError):
        parser.error('successful source/compiler build record must identify this executable')
    if fixture.stat().st_size > 4 * 1024**2:
        parser.error('retained fixture exceeds this measurement input bound')
    out = args.out.absolute()
    out.mkdir(mode=0o700, parents=False, exist_ok=False)
    os.umask(0o077)
    forbidden = out / 'forbidden-store'
    command = ['/usr/bin/time', '-f', 'maximum_rss_kib=%M\nelapsed_seconds=%e\nchild_exit=%x',
               '-o', str(out / 'resources.txt'), '--', str(binary), '-validate', '-yaml', str(fixture),
               '-data-dir', str(forbidden)]
    report = {'scope': 'private validation-only development measurement; no native/provider/release qualification',
              'status': 'incomplete', 'binary_sha256': args.binary_sha256, 'fixture': str(fixture),
              'fixture_sha256': digest(fixture), 'fixture_bytes': fixture.stat().st_size,
              'source': build, 'harness_sha256': digest(pathlib.Path(__file__)), 'command': command,
              'platform': dict(zip(('system', 'node', 'release', 'version', 'machine'), os.uname())),
              'python_version': sys.version.split()[0],
              'gnu_time_sha256': digest(pathlib.Path('/usr/bin/time')),
              'runtime_limits': {key: os.environ.get(key) for key in ('GOMAXPROCS', 'GOMEMLIMIT')},
              'virtual_address_space_limit_kib': VIRTUAL_KIB, 'wall_deadline_seconds': 60,
              'output_byte_limit': MAX_OUTPUT, 'comparison': 'older RSS/OOM evidence is historical, not compiler matched'}
    report.update(parsed_monitor_count_available=False, parsed_monitor_count=None,
                  coverage='memory-scenario completion only; the selected binary does not report parsed monitor count')
    write_json(out / 'input.json', report)
    interrupted = False

    def interrupt(*_):
        nonlocal interrupted
        interrupted = True

    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, interrupt)

    def limits():
        resource.setrlimit(resource.RLIMIT_AS, (VIRTUAL_KIB * 1024, VIRTUAL_KIB * 1024))
        resource.setrlimit(resource.RLIMIT_CORE, (0, 0))

    child, raw, stop_reason = None, bytearray(), None
    started = time.monotonic()
    try:
        env = {key: value for key, value in os.environ.items() if not key.startswith('CPRA_AUTH_TOKEN')}
        child = subprocess.Popen(command, cwd=out, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                 start_new_session=True, preexec_fn=limits)
        with selectors.DefaultSelector() as selected:
            selected.register(child.stdout, selectors.EVENT_READ)
            while selected.get_map():
                if interrupted or time.monotonic() - started > 60:
                    stop_reason = 'interrupted' if interrupted else 'time_limit'
                    break
                for key, _ in selected.select(0.1):
                    data = os.read(key.fileobj.fileno(), MAX_OUTPUT + 1)
                    if not data:
                        selected.unregister(key.fileobj)
                        continue
                    raw.extend(data)
                    if len(raw) > MAX_OUTPUT:
                        stop_reason = 'diagnostic_byte_limit'
                        break
                if stop_reason:
                    break
        if stop_reason is None:
            child.wait(timeout=max(0.1, 60 - (time.monotonic() - started)))
    except BaseException as error:
        stop_reason = type(error).__name__
    finally:
        if child is not None:
            if child.poll() is None:
                try:
                    os.killpg(child.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                try:
                    child.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    try:
                        os.killpg(child.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    child.wait(timeout=3)
            child.stdout.close()
        # Never print or retain the large URL if a broken validator echoes it.
        text = raw[:MAX_OUTPUT].decode('utf-8', errors='replace')
        lines = text.splitlines()
        if any(len(line) > 1024 for line in lines):
            stop_reason = stop_reason or 'diagnostic_line_limit'
        safe = '\n'.join(line if len(line) <= 1024 else '[oversized diagnostic line withheld]' for line in lines)
        with (out / 'diagnostic.txt').open('x', encoding='utf-8') as stream:
            stream.write(safe + '\n')
        resources = (out / 'resources.txt').read_text() if (out / 'resources.txt').exists() else ''
        rss = re.search(r'^maximum_rss_kib=(\d+)$', resources, re.MULTILINE)
        code = child.returncode if child is not None else None
        report.update(classify(code, text, int(rss.group(1)) if rss else None, forbidden.exists(), stop_reason))
        if digest(binary) != args.binary_sha256 or digest(fixture) != report['fixture_sha256']:
            stop_reason = 'input_changed'
            report['status'] = 'incomplete'
        report.update(exit=code, stop_reason=stop_reason, elapsed_seconds=time.monotonic() - started,
                      maximum_rss_kib=int(rss.group(1)) if rss else None,
                      explicit_store_created=forbidden.exists(), diagnostic_bytes=len(raw))
        write_json(out / 'result.json', report)
    print(json.dumps({'result': str(out / 'result.json'), 'status': report['status'],
                      'maximum_rss_kib': report['maximum_rss_kib']}), flush=True)
    return 0 if report['status'] == 'pass' else 1


if __name__ == '__main__':
    raise SystemExit(main())
