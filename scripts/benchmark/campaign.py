#!/usr/bin/env python3
"""External CPRa comparison and endurance harness. Uses real elapsed time.

Large campaigns always inspect physical C: free space before creating fixtures.
Smoke mode is explicitly a harness check and cannot satisfy a scale gate.
"""
import argparse
import gzip
import hashlib
import http.client
import json
import math
import os
import pathlib
import platform
import secrets
import selectors
import signal
import subprocess
import threading
import time
import urllib.parse
import urllib.error
import urllib.request

from preflight import inspect
from prepare import verify_builds

BASELINE = 'a370969b041b399c0778318d8915ce059fd74294'


def atomic_json(path, value):
    temporary = path.with_suffix(path.suffix + '.tmp')
    with temporary.open('w') as stream:
        json.dump(value, stream, indent=2)
        stream.write('\n')
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(path)


class RequestError(RuntimeError):
    def __init__(self, message, *, status=None, retryable=False):
        super().__init__(message)
        self.status = status
        self.retryable = retryable


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class HTTPClient:
    """One local fixture origin, with no redirects or ambient proxy settings."""
    def __init__(self, origin, *, token=None, response_limit=4 * 1024 ** 2):
        parsed = urllib.parse.urlsplit(origin)
        if parsed.scheme != 'http' or parsed.hostname not in ('127.0.0.1', '::1') \
                or parsed.username is not None or parsed.password is not None \
                or parsed.path not in ('', '/') or parsed.query or parsed.fragment \
                or parsed.port is None or not 0 < parsed.port <= 65535:
            raise ValueError('benchmark HTTP origin must be a numeric loopback address and port')
        if type(response_limit) is not int or not 0 < response_limit <= 64 * 1024 ** 2:
            raise ValueError('benchmark HTTP response limit must be between 1 byte and 64 MiB')
        self.origin = origin.rstrip('/')
        self._token = token
        self._response_limit = response_limit
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def _request(self, path, method):
        parsed = urllib.parse.urlsplit(path)
        if not path.startswith('/') or path.startswith('//') or parsed.scheme or parsed.netloc or parsed.fragment:
            raise ValueError('benchmark HTTP requests require an origin-relative path')
        headers = {'Accept': 'application/json'}
        if self._token is not None:
            headers['Authorization'] = 'Bearer ' + self._token
        request = urllib.request.Request(self.origin + path, headers=headers, method=method)
        try:
            with self._opener.open(request, timeout=10) as response:
                body = response.read(self._response_limit + 1)
        except urllib.error.HTTPError as error:
            status = error.code
            error.close()
            raise RequestError('benchmark HTTP request failed with status ' + str(status),
                               status=status, retryable=status == 503) from None
        except (OSError, http.client.HTTPException):
            raise RequestError('benchmark HTTP connection failed', retryable=True) from None
        if len(body) > self._response_limit:
            raise RequestError('benchmark HTTP response exceeds its byte limit')
        return body

    def get(self, path):
        body = self._request(path, 'GET')
        try:
            return json.loads(body)
        except (ValueError, UnicodeError):
            raise RequestError('benchmark HTTP response is not valid JSON') from None

    def post(self, path):
        self._request(path, 'POST')


def create_api_token(path):
    token = secrets.token_urlsafe(32)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, 'w', encoding='ascii') as stream:
            stream.write(token + '\n')
            stream.flush()
            os.fsync(stream.fileno())
    except OSError:
        path.unlink()
        raise
    return token


def controller_command(binary, config, port, token_file, runtime_config=None):
    command = [str(binary), '-yaml', str(config), '-web.addr', f'127.0.0.1:{port}',
               '-web.auth-file', str(token_file)]
    if runtime_config is not None:
        command += ['-runtime-config', str(runtime_config)]
    return command


def wait_for_fleet(api, count, controller, *, timeout=1800, interval=1):
    deadline = time.monotonic() + timeout
    while True:
        if controller.poll() is not None:
            raise RuntimeError('controller exited before loading the complete fleet')
        try:
            overview = api.get('/api/v1/overview')
            if not isinstance(overview, dict) or type(overview.get('total')) is not int:
                raise RequestError('benchmark overview response has an invalid fleet count')
            if overview['total'] == count:
                return overview
        except RequestError as error:
            if not error.retryable:
                raise
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise RuntimeError('controller did not load the complete fleet before the startup deadline')
        time.sleep(min(interval, remaining))


def target_address(target, *, timeout=30):
    """Read one bounded startup line without waiting forever on a live child."""
    deadline = time.monotonic() + timeout
    output = bytearray()
    with selectors.DefaultSelector() as selector:
        selector.register(target.stdout, selectors.EVENT_READ)
        while True:
            if target.poll() is not None:
                raise RuntimeError('target exited before reporting its address')
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError('target did not report its address before the startup deadline')
            if not selector.select(remaining):
                continue
            chunk = os.read(target.stdout.fileno(), 4097 - len(output))
            if not chunk:
                raise RuntimeError('target closed its startup output before reporting an address')
            output.extend(chunk)
            if len(output) > 4096:
                raise RuntimeError('target startup address exceeds its byte limit')
            if b'\n' in output:
                try:
                    return output.split(b'\n', 1)[0].decode('ascii').strip()
                except UnicodeError:
                    raise RuntimeError('target startup address is not ASCII') from None


def process_resources(pid):
    root = pathlib.Path('/proc') / str(pid)
    fields = (root / 'stat').read_text().split()
    status = dict(line.split(':', 1) for line in (root / 'status').read_text().splitlines() if ':' in line)
    return {'cpu_seconds': (int(fields[13]) + int(fields[14])) / os.sysconf('SC_CLK_TCK'),
            'rss_bytes': int(status['VmRSS'].split()[0]) * 1024,
            'threads': int(status['Threads']), 'descriptors': len(list((root / 'fd').iterdir()))}


def rotate_log(pipe, path):
    stream = path.open('wb')
    size = 0
    try:
        while chunk := pipe.read(65536):
            if size + len(chunk) > 8 * 1024 ** 2:
                stream.close()
                for n in range(2, 0, -1):
                    old = pathlib.Path(str(path) + f'.{n}')
                    if old.exists():
                        old.replace(pathlib.Path(str(path) + f'.{n + 1}'))
                path.replace(pathlib.Path(str(path) + '.1'))
                stream = path.open('wb')
                size = 0
            stream.write(chunk)
            size += len(chunk)
    finally:
        stream.close()


def stop(process):
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
        try:
            process.wait(timeout=40)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=10)


def manifest(path, monitors, target, seed):
    with path.open('w') as stream:
        stream.write('{"monitors":[')
        for number in range(monitors):
            if number:
                stream.write(',')
            row = {'id': f'benchmark-{seed}-{number}', 'name': f'benchmark-{seed}-{number:07d}',
                   'pulse_check': {'type': 'http', 'interval': '60s', 'timeout': '5s', 'unhealthy_threshold': 1, 'healthy_threshold': 1,
                                   'config': {'url': target + f'/check/{number}', 'expected_status': [204]}}}
            # A bounded representative cohort exercises history during faults.
            if number < 100:
                row['intervention'] = {'action': 'webhook', 'target': {'type': 'webhook', 'url': target + f'/action/{number}', 'timeout': '5s'}}
                row['codes'] = {'red': {'notify': 'log', 'config': {'file': str(path.parent / 'events.jsonl')}},
                                'green': {'notify': 'log', 'config': {'file': str(path.parent / 'events.jsonl')}}}
            json.dump(row, stream, separators=(',', ':'))
        stream.write(']}\n')


def reconcile(api, target_audit, count, seed):
    """Compare real target effects with retained committed action results."""
    succeeded = unknown = failed = 0
    entries = 0
    monitors = []
    observed = target_audit.get('cohort_actions')
    if not isinstance(observed, list) or len(observed) != min(100, count) \
            or any(type(value) is not int or value < 0 for value in observed) \
            or sum(observed) != target_audit['actions']:
        raise RuntimeError('target did not supply complete per-monitor intervention accounting')
    for n in range(min(100, count)):
        ident = f'benchmark-{seed}-{n}'
        cursor = ''
        cursors, event_ids, terminals = set(), set(), {}
        outcomes = {'action_succeeded': 0, 'action_unknown': 0, 'action_failed': 0}
        while True:
            query = urllib.parse.urlencode({'monitor_id':ident,'limit':500,'cursor':cursor})
            page = api.get('/api/v1/history?' + query)
            if not isinstance(page, dict) or not isinstance(page.get('events'), list) or len(page['events']) > 500:
                raise RuntimeError('history audit returned an invalid or unbounded page')
            for event in page['events']:
                entries += 1
                if not isinstance(event, dict) or event.get('monitor_id') != ident \
                        or not isinstance(event.get('id'), str) or not event['id'] or event['id'] in event_ids:
                    raise RuntimeError('history audit returned a duplicate event or another monitor')
                event_ids.add(event['id'])
                if event.get('kind') == 'intervention' and event.get('type') in outcomes:
                    action = event.get('action_id')
                    if not isinstance(action, str) or not action or action in terminals:
                        raise RuntimeError('history audit returned duplicate or missing terminal action identity')
                    terminals[action] = event['type']
                    outcomes[event['type']] += 1
                if entries > 1000000:
                    raise RuntimeError('history audit exceeded bounded campaign event budget')
            cursor = page.get('next_cursor','')
            if not isinstance(cursor, str) or cursor in cursors or len(cursors) >= 10000:
                raise RuntimeError('history audit returned a repeated or unbounded cursor')
            if not cursor: break
            cursors.add(cursor)
        succeeded += outcomes['action_succeeded']
        unknown += outcomes['action_unknown']
        failed += outcomes['action_failed']
        monitors.append({'monitor_id': ident, 'target_accepted': observed[n],
                         'committed_successes': outcomes['action_succeeded'],
                         'unknown_outcomes': outcomes['action_unknown'], 'confirmed_failures': outcomes['action_failed'],
                         'terminal_digest': hashlib.sha256(json.dumps(terminals, sort_keys=True, separators=(',', ':')).encode()).hexdigest()})
    return {'target_accepted':target_audit['actions'],'committed_successes':succeeded,
            'unknown_outcomes':unknown,'confirmed_failures':failed,'history_events':entries,
            'duplicate_interventions':target_audit['duplicate_actions'],
            'monitors': monitors,
            'reconciled':all(row['target_accepted']==row['committed_successes'] for row in monitors)
                         and unknown==0 and target_audit['duplicate_actions']==0}


def sha256(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as stream:
        for block in iter(lambda: stream.read(1 << 20), b''):
            digest.update(block)
    return digest.hexdigest()


def run_one(args, directory, binary, count, warmup, duration, upgraded, scenario='healthy'):
    directory.mkdir(mode=0o700, parents=True)
    result = {'status': 'incomplete', 'monitors': count, 'interval_seconds': 60, 'warmup_seconds': warmup,
              'measurement_seconds': duration, 'scenario': scenario, 'declared_faults': [], 'binary_sha256': sha256(binary),
              'internal_percentiles': 'candidate' if upgraded else 'unavailable in baseline',
              'started_at': time.time(), 'seed': args.seed, 'samples': 0}
    atomic_json(directory / 'progress.json', result)
    target = None
    controller = None
    logger = None
    token_file = None
    failures = []
    try:
        token_path = directory / 'api.token'
        token = create_api_token(token_path)
        token_file = token_path
        api = HTTPClient(f'http://127.0.0.1:{args.api_port}', token=token)
        with (directory / 'target.log').open('wb') as target_log:
            target = subprocess.Popen([args.target, '-monitors', str(count)], stdout=subprocess.PIPE,
                                      stderr=target_log, text=True)
        target_url = target_address(target)
        target_http = HTTPClient(target_url)
        config = directory / 'monitors.json'
        manifest(config, count, target_url, args.seed)
        runtime = None
        if upgraded:
            runtime = directory / 'runtime.yaml'
            runtime.write_text('storage:\n  mode: raft\n  directory: ' + str(directory / 'state') + '\n')
        command = controller_command(binary, config, args.api_port, token_file, runtime)
        environment = {key: value for key, value in os.environ.items()
                       if key not in ('CPRA_AUTH_TOKEN', 'CPRA_AUTH_TOKEN_FILE')}
        controller = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                      cwd=directory, env=environment)
        logger = threading.Thread(target=rotate_log, args=(controller.stdout, directory / 'controller.log'), daemon=True)
        logger.start()
        overview = wait_for_fleet(api, count, controller)
        result['api_authentication'] = 'per-run token file; bearer header to loopback API only'
        result['loaded_monitors'] = overview['total']
        started = time.monotonic()
        previous_wall, previous_mono = time.time(), started
        measurement_start = None
        beginning = None
        peak_rss = peak_fds = 0
        healthy_windows = 0
        previous_sample = None
        target_saturated = False
        fault_started = fault_stopped = False
        episode = 0
        last_episode = -1
        fault_until = 0
        crash_done = False
        rss_first, rss_last = [], []
        with gzip.open(directory / 'samples.jsonl.gz', 'wt') as samples:
            measurement_deadline = started + warmup + duration
            while time.monotonic() < measurement_deadline:
                if controller.poll() is not None or target.poll() is not None:
                    raise RuntimeError('controller or target exited')
                wall, mono = time.time(), time.monotonic()
                if mono - previous_mono > 20 or abs((wall - previous_wall) - (mono - previous_mono)) > 5:
                    raise RuntimeError('sleep, clock discontinuity or campaign interruption detected')
                previous_wall, previous_mono = wall, mono
                elapsed = mono - started
                # A bounded cohort creates retained history during the 24-hour
                # run; declared fault and recovery windows are excluded from
                # healthy SLO assertions, but remain in the recorded samples.
                measurement_elapsed = elapsed - warmup
                soak_episode = args.mode == 'soak' and measurement_elapsed >= 60 and int((measurement_elapsed-60)//3600) > last_episode
                one_fault = scenario in ('slow','outage','burst','recovery') and measurement_elapsed >= 30 and not fault_started
                if soak_episode or one_fault:
                    last_episode = int((measurement_elapsed-60)//3600) if soak_episode else 0
                    episode += 1
                    cohort = min(100,count) if soak_episode or scenario == 'recovery' else count
                    query = {'delay': '6s' if scenario in ('slow','burst') else '0s',
                             'outage': str(soak_episode or scenario in ('outage','recovery')).lower(),
                             'cohort': str(cohort), 'new_episode': 'true'}
                    target_http.post('/fault?' + urllib.parse.urlencode(query))
                    fault_started, fault_stopped = True, False
                    fault_until = elapsed + (2 if scenario == 'burst' else 125)
                    result['declared_faults'].append({'episode':episode,'start':elapsed,'planned_end':fault_until,'cohort':cohort,'type':'cohort_outage' if soak_episode else scenario})
                if fault_started and not fault_stopped and elapsed >= fault_until:
                    target_http.post('/fault?delay=0s&outage=false')
                    fault_stopped = True
                    result['declared_faults'][-1]['actual_end'] = elapsed
                if scenario == 'crash' and measurement_elapsed >= 30 and not crash_done:
                    controller.kill()
                    controller.wait(timeout=10)
                    logger.join(timeout=10)
                    controller = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                                  cwd=directory, env=environment)
                    logger = threading.Thread(target=rotate_log,args=(controller.stdout,directory/'restart.log'),daemon=True)
                    logger.start()
                    wait_for_fleet(api, count, controller, timeout=600)
                    crash_done = True
                    result['declared_faults'].append({'type':'process_kill','start':elapsed,'actual_end':time.monotonic()-started})
                    # This is a declared crash scenario, never uninterrupted soak.
                    previous_wall,previous_mono=time.time(),time.monotonic()
                in_fault_window = any(elapsed >= f['start'] and elapsed < f.get('actual_end',f.get('planned_end',elapsed))+300 for f in result['declared_faults'])
                audit_start = time.monotonic()
                audit = target_http.get('/audit')
                sample = {'at': wall, 'elapsed': elapsed, 'target_audit': audit,
                          'target_audit_latency': time.monotonic() - audit_start,
                          'controller': process_resources(controller.pid), 'target': process_resources(target.pid),
                          'queues': api.get('/api/v1/queues'), 'pools': api.get('/api/v1/pools')}
                if previous_sample is not None:
                    seconds = wall-previous_sample['at']
                    target_cores=(sample['target']['cpu_seconds']-previous_sample['target']['cpu_seconds'])/max(seconds,.001)
                    sample['target_cpu_cores']=target_cores
                    if target_cores > max(1,os.cpu_count() or 1)*.8 or sample['target_audit_latency'] > .25:
                        target_saturated=True
                previous_sample=sample
                # A bounded dashboard page is read every sample during measurement.
                page = api.get('/api/v1/monitors?size=100&page=' + str(1 + (result['samples'] * 997) % max(1, count // 100)))
                if page['total'] != count or len(page['monitors']) > 100:
                    raise RuntimeError('fleet navigation returned an invalid count or unbounded page')
                sample['dashboard'] = {'total': page['total'], 'returned': len(page['monitors']), 'page_size': 100}
                if upgraded:
                    sample['slo'] = api.get('/api/v1/slo')
                    state = api.get('/api/v1/state')
                    sample['storage'],sample['storage_usage'],sample['runtime'] = state['storage'],state['storage_usage'],state['process']
                    sample['declared_fault_window'] = in_fault_window
                    if not sample['storage']['ready']:
                        raise RuntimeError('durable storage became unavailable')
                peak_rss = max(peak_rss, sample['controller']['rss_bytes'])
                peak_fds = max(peak_fds, sample['controller']['descriptors'])
                if elapsed >= warmup:
                    if measurement_start is None:
                        measurement_start, beginning = mono, audit['received']
                        measurement_deadline = mono + duration
                        result['measurement_started_at'] = wall
                    if elapsed < warmup + 300:
                        rss_first.append(sample['controller']['rss_bytes'])
                    rss_last.append(sample['controller']['rss_bytes'])
                    rss_last = rss_last[-60:]
                    if upgraded and args.mode != 'smoke' and not in_fault_window:
                        view = sample['slo']
                        valid = view['coverage_complete'] and bool(view['reports']) and all(r['samples'] >= 1000 and r['queue_attainment'] is not None and r['queue_attainment'] >= .99 and r['result_attainment'] >= .99 for r in view['reports'])
                        if valid:
                            healthy_windows += 1
                        elif 'one or more measured SLO windows failed' not in failures:
                            failures.append('one or more measured SLO windows failed')
                samples.write(json.dumps(sample, separators=(',', ':')) + '\n')
                samples.flush()
                result.update(samples=result['samples'] + 1, elapsed_seconds=elapsed, peak_rss_bytes=peak_rss, peak_descriptors=peak_fds, healthy_slo_windows=healthy_windows)
                atomic_json(directory / 'progress.json', result)
                time.sleep(min(5, max(0, measurement_deadline - time.monotonic())))
        final = target_http.get('/audit?digest=true')
        measured = time.monotonic() - measurement_start if measurement_start else 0
        rate = (final['received'] - beginning) / measured if beginning is not None and measured else 0
        if upgraded:
            accounting = reconcile(api,final,count,args.seed)
            atomic_json(directory/'reconciliation.json',accounting)
            if args.mode!='smoke' and not accounting['reconciled']:
                failures.append('accepted recovery operations did not fully reconcile with committed outcomes')
        result['target_saturation_detected']=target_saturated
        if target_saturated and args.mode!='smoke':failures.append('target server saturation or audit delay limits this measurement')
        result.update(final_target_audit=final, achieved_rate=rate, target_rate=count / 60, actual_measurement_seconds=measured,
                      rss_first_mean=sum(rss_first) / max(1, len(rss_first)), rss_last_mean=sum(rss_last) / max(1, len(rss_last)))
        if args.mode != 'smoke':
            if final['duplicate_actions'] != 0:
                failures.append('target observed repeated intervention in one incident episode')
            if args.mode == 'soak' and (final['actions'] == 0 or sample['storage_usage']['snapshot_files'] < 1 or sample['storage_usage']['history_bytes'] == 0):
                failures.append('soak lacks required intervention, history or snapshot activity')
            if final['distinct'] != count:
                failures.append('not every distinct monitor reached the target')
            if upgraded and scenario == 'healthy' and rate < count / 60 * .99:
                failures.append('healthy achieved rate below 99% of specified cadence')
            if measured < duration - 6:
                failures.append('measurement duration incomplete')
        result['status'] = 'failed' if failures else ('harness_smoke_pass' if args.mode == 'smoke' else 'measured_pending_review')
    except (Exception, KeyboardInterrupt) as error:
        failures.append(str(error))
        result['status'] = 'incomplete'
    finally:
        cleanup = {}
        for name, process in (('controller', controller), ('target', target)):
            if process is not None:
                try:
                    stop(process)
                except (OSError, subprocess.SubprocessError) as error:
                    failures.append(name + ' stop failed (' + type(error).__name__ + ')')
            cleanup[name + '_stopped'] = process is None or process.poll() is not None
            if not cleanup[name + '_stopped']:
                failures.append(name + ' is still running; inspect the retained PID before another attempt')
                cleanup[name + '_pid'] = process.pid
        if target is not None and cleanup['target_stopped']:
            target.stdout.close()
        if logger is not None:
            logger.join(timeout=10)
            if logger.is_alive():
                failures.append('controller log drain did not finish')
        if token_file is not None and cleanup['controller_stopped']:
            try:
                token_file.unlink(missing_ok=True)
            except OSError as error:
                failures.append('API token cleanup failed (' + type(error).__name__ + ')')
        cleanup['token_removed'] = token_file is None or not token_file.exists()
        result['cleanup'] = cleanup
        if failures and result['status'] in ('harness_smoke_pass', 'measured_pending_review'):
            result['status'] = 'incomplete'
        result['failures'] = failures
        result['finished_at'] = time.time()
        atomic_json(directory / 'result.json', result)
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--mode', choices=['preflight', 'compare', 'soak', 'faults', 'smoke'], default='preflight')
    parser.add_argument('--candidate')
    parser.add_argument('--baseline')
    parser.add_argument('--builds', help='builds.json emitted by prepare.py; required for large campaigns')
    parser.add_argument('--target')
    parser.add_argument('--out', required=True)
    parser.add_argument('--seed', type=int, default=20260910)
    parser.add_argument('--api-port', type=int, default=18060)
    parser.add_argument('--fixture-headroom-gib', type=float, default=5)
    args = parser.parse_args()
    if not math.isfinite(args.fixture_headroom_gib) or args.fixture_headroom_gib < 0:
        parser.error('fixture headroom must be finite and non-negative')
    out = pathlib.Path(args.out).resolve()
    try:
        out.mkdir(mode=0o700, parents=True, exist_ok=False)
    except FileExistsError:
        parser.error('campaign output already exists; use a new directory to preserve earlier evidence')
    preflight = inspect(int(args.fixture_headroom_gib * 1024 ** 3))
    preflight.update(baseline_commit=BASELINE, hostname=platform.node(), cpu_count=os.cpu_count())
    atomic_json(out / 'preflight.json', preflight)
    if args.mode == 'preflight':
        return 0 if preflight['ready'] else 2
    if args.mode != 'smoke' and not preflight['ready']:
        atomic_json(out / 'campaign.json', {'status': 'blocked_by_environment', 'reason': preflight['reason'], 'comparisons_complete': False, 'soak_24h_complete': False})
        print(preflight['reason'])
        return 2
    if not args.candidate or not args.target or (args.mode == 'compare' and not args.baseline):
        parser.error('candidate and target binaries are required; comparison also requires the baseline')
    args.candidate, args.target = str(pathlib.Path(args.candidate).resolve()), str(pathlib.Path(args.target).resolve())
    if args.mode != 'smoke':
        if not args.builds:
            parser.error('large campaigns require --builds from prepare.py')
        try:
            build_metadata = json.loads(pathlib.Path(args.builds).read_text())
            verify_builds(build_metadata, args.candidate, args.target, args.baseline if args.mode == 'compare' else None)
        except (ValueError, OSError) as error:
            parser.error(str(error))
        atomic_json(out/'builds.json',build_metadata)
    runs = []
    if args.mode == 'smoke':
        trials = [('smoke', args.candidate, 10, 0, 10, True, 'healthy')]
    elif args.mode == 'soak':
        trials = [('soak', args.candidate, 1000000, 300, 24 * 3600, True, 'healthy')]
    elif args.mode == 'faults':
        trials = [(scenario, args.candidate, 1000000, 300, 900, True, scenario)
                  for scenario in ('burst', 'slow', 'outage', 'recovery', 'crash')]
    else:
        trials = [(f'{name}-{count}-{repeat}', binary, count, 300, 900, upgraded, 'healthy')
                  for count in (10000, 100000, 1000000) for repeat in range(3)
                  for name, binary, upgraded in (('baseline', str(pathlib.Path(args.baseline).resolve()), False),
                                                ('candidate', args.candidate, True))]
    for name, binary, count, warmup, duration, upgraded, scenario in trials:
        runs.append(run_one(args, out / name, binary, count, warmup, duration, upgraded, scenario))
        if runs[-1]['status'] == 'incomplete':
            break
    if len(runs) != len(trials) or any(r['status'] == 'incomplete' for r in runs):
        status = 'incomplete'
    elif any(r['status'] == 'failed' for r in runs):
        status = 'failed'
    else:
        status = 'harness_smoke_pass' if args.mode == 'smoke' else 'measured_pending_review'
    atomic_json(out / 'campaign.json', {'mode': args.mode, 'status': status, 'baseline_commit': BASELINE,
                                       'planned_runs': len(trials), 'runs': runs,
                                       'release_gate_passed': False, 'review_required': True})
    return 1 if status in ('failed', 'incomplete') else 0


if __name__ == '__main__':
    raise SystemExit(main())
