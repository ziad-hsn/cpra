#!/usr/bin/env python3
"""Bounded, real-network outage-isolation regression; not fleet qualification."""
import argparse
import asyncio
import hashlib
import json
import math
import os
import pathlib
import re
import secrets
import shutil
import signal
import ssl
import tempfile
import time
import urllib.request


def percentile(values, fraction):
    if not values:
        return None
    values = sorted(values)
    return values[min(len(values) - 1, int((len(values) - 1) * fraction))]


def latency_summary(observations):
    """Retain unavailable estimates and the coverage of the last sampled window."""
    summaries = {}
    for observation in observations:
        slo = observation.get('slo', {})
        for report in slo.get('reports', []):
            driver = report['driver']
            summary = summaries.setdefault(driver, {
                'maximum_reported_queue_p99_ms': None, 'maximum_reported_result_p99_ms': None,
                'maximum_timeouts': 0, 'maximum_missed': 0, 'conditions': [],
                'last_sample_count': 0, 'last_expected_count': 0,
                'last_window_seconds': None, 'last_coverage_complete': False})
            for target, measurement in (('queue', 'scheduling_queue'), ('result', 'scheduled_result')):
                value = report.get(measurement, {}).get('p99_ms')
                if type(value) in (int, float) and math.isfinite(value) and value >= 0:
                    key = 'maximum_reported_' + target + '_p99_ms'
                    previous = summary[key]
                    summary[key] = value if previous is None else max(previous, value)
            summary['maximum_timeouts'] = max(summary['maximum_timeouts'], report.get('timeouts', 0))
            summary['maximum_missed'] = max(summary['maximum_missed'], report.get('missed', 0))
            if report.get('condition') not in summary['conditions']:
                summary['conditions'].append(report.get('condition'))
            summary.update(last_sample_count=report.get('samples', 0),
                           last_expected_count=report.get('expected', 0),
                           last_window_seconds=slo.get('window_seconds'),
                           last_coverage_complete=slo.get('coverage_complete', False))
    return summaries


def fault_window_gaps(times, begin, end):
    """Include silent time at both boundaries, even when no target was visited."""
    inside = [at for at in times if begin <= at <= end]
    return [b-a for a,b in zip([begin]+inside, inside+[end])]


def duration_budget(onset, fault, recovery, activation=0):
    # Startup, bounded API reads and supervisor shutdown are part of the limit.
    total = onset + fault + recovery + activation + 100
    if onset < 0 or fault < 10 or recovery < 10 or total > 900:
        raise ValueError('scenario including startup and shutdown must fit within 900 seconds')
    return total


async def scenario(binary, output, name, slow, healthy, timeout, cadence, tcp=False, cli=None, activation_timeout=600, onset=0, fault=60, recovery=30):
    duration_budget(onset, fault, recovery, activation_timeout if cli else 0)
    work = pathlib.Path(tempfile.mkdtemp(prefix=name + '-', dir=output))
    started = time.monotonic()
    visits = {str(n): [] for n in range(healthy)}
    counters = {'slow_requests': 0, 'healthy_requests': 0, 'connections': 0, 'peak_connections': 0}
    slow_visits = {str(n): {'before_fault': 0, 'hung': 0, 'recovered': 0, 'timeout_duration_hangs': 0, 'maximum_hang_seconds': 0} for n in range(slow)}
    counters.update(hanging=0, peak_hanging=0, disconnected_hangs=0, unexpected_requests=0)
    tasks = set()
    writers = set()
    faults_start, faults_end = float('inf'), float('inf')
    process = None
    snapshots = []
    apply_task = None
    environment = {key: os.environ[key] for key in ('PATH', 'HOME', 'LANG', 'GOMAXPROCS') if key in os.environ}
    environment.update(XDG_CONFIG_HOME=str(work / 'user-config'),
                       XDG_STATE_HOME=str(work / 'user-state'), TZ='UTC',
                       TMPDIR=str(work / 'tmp'), CPRA_LOG_FORMAT='json', CPRA_LOG_LEVEL='info')
    (work / 'tmp').mkdir(mode=0o700)
    state = work / 'state'
    with binary.open('rb') as executable:
        binary_digest = hashlib.file_digest(executable, 'sha256').hexdigest()
    result = {'scenario': name, 'slow_monitors': slow, 'healthy_monitors': healthy,
              'timeout_seconds': timeout, 'cadence_seconds': cadence,
              'fault_onset_seconds': onset, 'fault_duration_seconds': fault, 'recovery_duration_seconds': recovery,
              'scenario_deadline_seconds': 900,
              'storage': 'raft', 'scope': 'private development isolation regression; not frozen release qualification',
              'gomaxprocs': os.environ.get('GOMAXPROCS', 'runtime default'),
              'binary_sha256': binary_digest}
    result['minimum_proven_hang_seconds'] = timeout * .8
    result['configuration_mode'] = 'management, one monitor followed by collection growth' if cli else 'file startup'
    token = secrets.token_urlsafe(32)
    config = work / 'config'
    config.mkdir(mode=0o700)
    (config / 'token').write_text(token)
    (config / 'token').chmod(0o600)
    (work / 'notifications').mkdir(mode=0o700)

    async def target(reader, writer):
        task = asyncio.current_task()
        tasks.add(task)
        writers.add(writer)
        counters['connections'] += 1
        counters['peak_connections'] = max(counters['peak_connections'], counters['connections'])
        try:
            while True:
                header = await reader.readuntil(b'\r\n\r\n')
                path = header.split(b' ', 2)[1].decode('ascii')
                if path.startswith('/slow/'):
                    key = path.removeprefix('/slow/')
                    if key not in slow_visits:
                        counters['unexpected_requests'] += 1
                        return
                    counters['slow_requests'] += 1
                    if faults_start <= time.monotonic() < faults_end:
                        slow_visits[key]['hung'] += 1
                        counters['hanging'] += 1
                        counters['peak_hanging'] = max(counters['peak_hanging'], counters['hanging'])
                        hung_at = time.monotonic()
                        try:
                            await reader.read()  # Actual missing HTTP response until client timeout.
                            counters['disconnected_hangs'] += 1
                            duration = time.monotonic() - hung_at
                            slow_visits[key]['maximum_hang_seconds'] = max(slow_visits[key]['maximum_hang_seconds'], duration)
                            if duration >= timeout * .8:
                                slow_visits[key]['timeout_duration_hangs'] += 1
                        finally:
                            counters['hanging'] -= 1
                        return
                    if time.monotonic() >= faults_end:
                        slow_visits[key]['recovered'] += 1
                    else:
                        slow_visits[key]['before_fault'] += 1
                else:
                    key = path.removeprefix('/healthy/')
                    if key not in visits:
                        counters['unexpected_requests'] += 1
                        return
                    visits[key].append(time.monotonic() - started)
                    counters['healthy_requests'] += 1
                writer.write(b'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK')
                await writer.drain()
        except (asyncio.IncompleteReadError, ConnectionError, asyncio.CancelledError):
            pass
        finally:
            counters['connections'] -= 1
            writer.close()
            writers.discard(writer)
            tasks.discard(task)

    async def tcp_target(reader, writer, key):
        visits[key].append(time.monotonic() - started)
        counters['healthy_requests'] += 1
        writer.close()
        await writer.wait_closed()

    http = await asyncio.start_server(target, '127.0.0.1', 0, limit=65536)
    port = http.sockets[0].getsockname()[1]
    tcp_servers = []
    if tcp:
        for n in range(healthy):
            tcp_servers.append(await asyncio.start_server(lambda r, w, key=str(n): tcp_target(r, w, key), '127.0.0.1', 0))
    monitors = []
    for n in range(slow):
        monitors.append({'id': f'slow-{n}', 'name': f'Slow {n}', 'enabled': True,
                         'pulse_check': {'type': 'http', 'interval': f'{cadence}s', 'timeout': f'{timeout}s',
                                         'unhealthy_threshold': 3, 'healthy_threshold': 2,
                                         'config': {'url': f'http://127.0.0.1:{port}/slow/{n}'}},
                         'codes': {'red': {'dispatch': True, 'notify': 'log', 'config': {'file': 'alerts.jsonl'}},
                                   'green': {'dispatch': True, 'notify': 'log', 'config': {'file': 'alerts.jsonl'}}}})
    for n in range(healthy):
        check = {'type': 'http', 'interval': f'{cadence}s', 'timeout': '2s',
                 'config': {'url': f'http://127.0.0.1:{port}/healthy/{n}'}}
        if tcp:
            check = {'type': 'tcp', 'interval': '5s', 'timeout': '2s',
                     'config': {'host': '127.0.0.1', 'port': tcp_servers[n].sockets[0].getsockname()[1]}}
        monitors.append({'id': f'healthy-{n}', 'name': f'Healthy {n}', 'enabled': True, 'pulse_check': check})
    (config / 'monitors.json').write_text(json.dumps({'monitors': monitors}))
    (config / 'runtime.yaml').write_text('storage:\n  mode: raft\n  directory: ' + str(work / 'state') +
                                     '\n  snapshot_interval: 20s\nnotifications:\n  log_directory: ' + str(work / 'notifications') + '\n')
    # Ask the application to bind an available port discovered locally. The tiny
    # release/bind race is reported as startup failure, never silently retried.
    probe = await asyncio.start_server(lambda r, w: w.close(), '127.0.0.1', 0)
    api_port = probe.sockets[0].getsockname()[1]
    probe.close()
    await probe.wait_closed()
    command = [str(binary), '-yaml', str(config / 'monitors.json'), '-runtime-config', str(config / 'runtime.yaml'),
               '-web.addr', f'127.0.0.1:{api_port}', '-web.auth-file', str(config / 'token')]
    log_path = output / (name + '.log')
    origin = f'http://127.0.0.1:{api_port}'
    connection = []
    context = None

    async def run_cli(arguments, seconds=150):
        child = await asyncio.create_subprocess_exec(str(cli), *connection, *arguments, cwd=work, env=environment,
                                                     stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
        stdout, stderr = bytearray(), bytearray()
        async def drain(stream, buffer):
            while data := await stream.read(8192):
                if len(stdout) + len(stderr) + len(data) > 2 * 1024**2:
                    raise RuntimeError('bounded CLI output quota exceeded')
                buffer.extend(data)
        readers = [asyncio.create_task(drain(child.stdout, stdout)), asyncio.create_task(drain(child.stderr, stderr))]
        try:
            await asyncio.wait_for(asyncio.gather(*readers, child.wait()), seconds)
        except BaseException:
            if child.returncode is None:
                child.kill()
                await child.wait()
            raise
        finally:
            for reader in readers:
                if not reader.done():
                    reader.cancel()
            await asyncio.gather(*readers, return_exceptions=True)
            if token.encode() in stdout or token.encode() in stderr:
                if 'apply' in arguments[:3]:
                    result['collection_command'] = {'exit_code': child.returncode, 'credential_output_rejected': True}
                raise RuntimeError('credential appeared in CLI output')
            if 'apply' in arguments[:3]:
                step = {'exit_code': child.returncode}
                # Retain the original handle and bounded receipt on failures as
                # well as success. Never retain arbitrary diagnostic output.
                for line in stderr.decode(errors='replace').splitlines():
                    if re.fullmatch(r'Operation: op\.[A-Za-z0-9._-]{1,200}', line):
                        step['operation_id'] = line.removeprefix('Operation: ')
                try:
                    receipt = json.loads(stdout)
                    if isinstance(receipt, dict) and re.fullmatch(r'op\.[A-Za-z0-9._-]{1,200}', receipt.get('id', '')):
                        step['receipt'] = receipt
                except (ValueError, TypeError):
                    pass
                result['collection_command'] = step
        if child.returncode != 0:
            raise RuntimeError('management fixture command failed: ' + arguments[0])
        return stdout.decode()

    def observe(route):
        req = urllib.request.Request(f'{origin}/api/v1/{route}', headers={'Authorization': 'Bearer ' + token})
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
        with opener.open(req, timeout=2) as response:
            return json.load(response)

    try:
        if cli:
            with cli.open('rb') as executable:
                result['cli_sha256'] = hashlib.file_digest(executable, 'sha256').hexdigest()
            await run_cli(['local', 'init', '--management'])
            layout = json.loads(await run_cli(['local', 'paths']))
            managed_config = pathlib.Path(layout['config_dir'])
            state = pathlib.Path(layout['state_dir'])
            if not managed_config.is_relative_to(work) or not state.is_relative_to(work):
                raise RuntimeError('initializer escaped the private fixture directory')
            # Establish actual checks and pool sizing before admitting the new
            # collection. The seed is excluded from apply to avoid a replacement.
            seed = next(m for m in monitors if m['id'] == 'healthy-0')
            (managed_config / 'monitors.yaml').write_text(json.dumps({'monitors': [seed]}))
            (config / 'growth.json').write_text(json.dumps({'monitors': [m for m in monitors if m['id'] != seed['id']]}))
            token = (managed_config / 'operator.token').read_text().strip()
            origin = f'https://localhost:{api_port}'
            context = ssl.create_default_context(cafile=str(managed_config / 'localhost-ca.pem'))
            connection = ['--server', origin, '--ca-file', str(managed_config / 'localhost-ca.pem'),
                          '--token-file', str(managed_config / 'operator.token')]
            command = [str(binary), '-yaml', str(managed_config / 'monitors.yaml'),
                       '-runtime-config', str(managed_config / 'runtime.yaml'), '-web.addr', f'127.0.0.1:{api_port}']
        with log_path.open('xb') as log:
            process = await asyncio.create_subprocess_exec(*command, stdout=log, stderr=log, cwd=work, env=environment)
            ready_by = time.monotonic() + 20
            while True:
                if process.returncode is not None:
                    raise RuntimeError('application exited during startup')
                try:
                    await asyncio.to_thread(observe, 'readyz')
                    break
                except Exception:
                    if time.monotonic() >= ready_by:
                        raise RuntimeError('readiness deadline exceeded') from None
                    await asyncio.sleep(.2)
            if cli:
                deadline = time.monotonic() + 30
                while len(visits['0']) < 2:
                    if time.monotonic() >= deadline:
                        raise RuntimeError('seed monitor did not establish pre-growth checks')
                    await asyncio.sleep(.1)
                result['seed_checks_before_growth'] = len(visits['0'])
                result['activation_deadline_seconds'] = activation_timeout
                result['pools_before_growth'] = await asyncio.to_thread(observe, 'pools')
                seed_last = started + visits['0'][-1]
            started = time.monotonic()
            visits = {str(n): [] for n in range(healthy)}
            activated = None if cli else started
            if cli:
                # A negative first point preserves the last pre-growth seed
                # check. Do not discard the transition while waiting on apply.
                visits['0'].append(seed_last - started)
                apply_task = asyncio.create_task(run_cli(['--output', 'json', 'apply', '-f', str(config / 'growth.json'),
                                                         '--wait', '--timeout', f'{activation_timeout}s'], seconds=activation_timeout+60))
            else:
                faults_start, faults_end = started + onset, started + onset + fault
            previous_mono, previous_wall = started, time.time()
            max_rss = max_fds = 0
            cpu_start = pathlib.Path(f'/proc/{process.pid}/stat').read_text().split()
            max_loop_delay = 0
            while activated is None or time.monotonic() - activated < onset + fault + recovery:
                if activated is None and time.monotonic() - started > activation_timeout+60:
                    raise RuntimeError('collection activation deadline exceeded')
                if activated is None and apply_task.done():
                    receipt = json.loads(await apply_task)
                    summary = receipt.get('executionResult', {}).get('summary', {})
                    expected = slow + healthy - 1
                    if (receipt.get('itemCount') != expected or summary.get('outcome') != 'completed' or
                            summary.get('itemCount') != expected or summary.get('accepted') != expected or
                            summary.get('childApplied') != expected):
                        raise RuntimeError('growth collection did not apply the expected resource count')
                    activated = time.monotonic()
                    result['collection_apply_seconds'] = activated - started
                    faults_start, faults_end = activated + onset, activated + onset + fault
                tick = time.monotonic()
                row = {'at_seconds': tick - started, 'phase': 'activation' if activated is None else 'healthy' if tick < faults_start else 'fault' if tick < faults_end else 'recovery'}
                for route in ('queues', 'pools', 'slo', 'state'):
                    try:
                        row[route] = await asyncio.to_thread(observe, route)
                    except Exception as error:
                        row[route] = {'observation_error': type(error).__name__}
                rss = int(pathlib.Path(f'/proc/{process.pid}/statm').read_text().split()[1]) * os.sysconf('SC_PAGE_SIZE')
                max_rss = max(max_rss, rss)
                max_fds = max(max_fds, len(list(pathlib.Path(f'/proc/{process.pid}/fd').iterdir())))
                snapshots.append(row)
                if log_path.stat().st_size > 8 * 1024**2:
                    raise RuntimeError('bounded log quota exceeded')
                await asyncio.sleep(max(0, 1 - (time.monotonic() - tick)))
                mono, wall = time.monotonic(), time.time()
                max_loop_delay = max(max_loop_delay, mono - tick - 1)
                if abs((wall - previous_wall) - (mono - previous_mono)) > 1:
                    raise RuntimeError('clock discontinuity or sleep detected')
                previous_mono, previous_wall = mono, wall
            elapsed = time.monotonic() - started
            cpu_end = pathlib.Path(f'/proc/{process.pid}/stat').read_text().split()
            cpu_seconds = sum(int(cpu_end[i]) - int(cpu_start[i]) for i in (13, 14)) / os.sysconf('SC_CLK_TCK')
            offset = activated - started
            after = {key: [at-offset for at in times if at >= offset] for key, times in visits.items()}
            gaps = [b-a for times in after.values() for a, b in zip([0]+times, times+[elapsed-offset])]
            fault_gaps = [gap for times in after.values() for gap in fault_window_gaps(times, onset, onset+fault)]
            if cli:
                seed_gaps = [b-a for a,b in zip(visits['0'], visits['0'][1:]+[elapsed])]
                first = [times[0] for key, times in visits.items() if key != '0' and times]
                result.update(seed_maximum_gap_across_growth_seconds=max(seed_gaps),
                              new_monitor_first_request_since_apply_seconds={'count': len(first), 'maximum': max(first, default=None), 'p99': percentile(first, .99)})
            result.update(seconds=elapsed, post_activation_seconds=elapsed-offset, counts=counters,
                          slow_checks_by_id=slow_visits, healthy_checks_min=min(map(len, after.values())),
                          healthy_checks_max=max(map(len, after.values())), maximum_healthy_gap_seconds=max(gaps),
                          p99_healthy_gap_seconds=percentile(gaps, .99), fault_maximum_gap_seconds=max(fault_gaps),
                          peak_rss_bytes=max_rss, peak_descriptors=max_fds, process_cpu_seconds=cpu_seconds,
                          target_event_loop_maximum_delay_seconds=max_loop_delay, observations=snapshots)
            result['observed_latency_by_driver'] = latency_summary(snapshots)
            result['passed'] = (result['healthy_checks_min'] > 1 and max(gaps) < 30 and max_loop_delay < 1
                                and all(counts['timeout_duration_hangs'] > 0 and counts['recovered'] > 0 for counts in slow_visits.values())
                                and counters['peak_hanging'] >= min(slow, 8) and counters['unexpected_requests'] == 0
                                and result.get('seed_maximum_gap_across_growth_seconds', 0) < 30)
    except Exception as error:
        result.update(passed=False, failure_type=type(error).__name__, detail=str(error) if isinstance(error, RuntimeError) else 'scenario could not complete')
    finally:
        if apply_task is not None and not apply_task.done():
            apply_task.cancel()
            await asyncio.gather(apply_task, return_exceptions=True)
        if process is not None and process.returncode is None:
            process.send_signal(signal.SIGTERM)
            try:
                await asyncio.wait_for(process.wait(), 50)
            except asyncio.TimeoutError:
                process.kill()
                await process.wait()
                result.update(passed=False, shutdown='deadline exceeded')
        if process is not None:
            result['exit_code'] = process.returncode
            if process.returncode != 0:
                result['passed'] = False
        http.close()
        await http.wait_closed()
        for tcp_server in tcp_servers:
            tcp_server.close()
            await tcp_server.wait_closed()
        for writer in list(writers):
            writer.close()
        for task in list(tasks):
            task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)
        result['snapshot_count'] = len(list(state.glob('snapshots/*/meta.json')))
        result.setdefault('observations', snapshots)
        result.setdefault('counts', counters)
        result.setdefault('slow_checks_by_id', slow_visits)
        result['healthy_checks_by_id'] = {key: len([at for at in times if at >= 0]) for key, times in visits.items()}
        result['fixture_allocated_bytes'] = sum(p.lstat().st_blocks*512 for p in work.rglob('*') if not p.is_symlink())
        result['log_sha256'] = None
        if log_path.exists():
            with log_path.open('rb') as log:
                result['log_sha256'] = hashlib.file_digest(log, 'sha256').hexdigest()
        (output / (name + '.json')).write_text(json.dumps(result, indent=2) + '\n')
        # Only this invocation's private disposable state and token are removed.
        shutil.rmtree(work)
    print(json.dumps({k:v for k,v in result.items() if k not in ('observations', 'slow_checks_by_id', 'healthy_checks_by_id', 'collection_command')}), flush=True)
    return result['passed']


async def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=pathlib.Path)
    parser.add_argument('--output', required=True, type=pathlib.Path)
    parser.add_argument('--cli', type=pathlib.Path, help='cpractl executable for the post-startup catalog scenario')
    parser.add_argument('--scenarios', choices=('file', 'growth', 'late-http', 'late-tcp', 'all'), default='file')
    parser.add_argument('--activation-timeout', type=int, default=600, help='collection activation allowance in seconds (30–900); measured separately from cadence')
    args = parser.parse_args()
    if not 30 <= args.activation_timeout <= 900:
        parser.error('--activation-timeout must be between 30 and 900 seconds')
    if args.scenarios in ('growth', 'all') and args.cli is None:
        parser.error('--cli is required for catalog growth')
    try:
        duration_budget(0, 60, 30, args.activation_timeout if args.scenarios in ('growth', 'all') else 0)
    except ValueError as error:
        parser.error(str(error))
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    results = []
    if args.scenarios in ('file', 'all'):
        results.append(await scenario(args.binary.resolve(), args.output.resolve(), '200-slow-http-healthy-tcp', 200, 1, 10, 10, tcp=True))
        results.append(await scenario(args.binary.resolve(), args.output.resolve(), '300-slow-http-700-healthy-http', 300, 700, 5, 10))
    if args.scenarios in ('late-http', 'all'):
        results.append(await scenario(args.binary.resolve(), args.output.resolve(), 'late-300-slow-http-700-healthy-http', 300, 700, 5, 10, onset=400, recovery=60))
    if args.scenarios in ('late-tcp', 'all'):
        results.append(await scenario(args.binary.resolve(), args.output.resolve(), 'late-300-slow-http-700-healthy-tcp', 300, 700, 5, 10, tcp=True, onset=400, recovery=60))
    if args.scenarios in ('growth', 'all'):
        results.append(await scenario(args.binary.resolve(), args.output.resolve(), 'catalog-growth-300-slow-700-healthy', 300, 700, 5, 10, cli=args.cli.resolve(), activation_timeout=args.activation_timeout))
    raise SystemExit(0 if all(results) else 1)


if __name__ == '__main__':
    asyncio.run(main())
