#!/usr/bin/env python3
"""External CPRa comparison and endurance harness. Uses real elapsed time.

Large campaigns always inspect physical C: free space before creating fixtures.
Smoke mode is explicitly a harness check and cannot satisfy a scale gate.
"""
import argparse
import gzip
import hashlib
import json
import os
import pathlib
import platform
import signal
import subprocess
import threading
import time
import urllib.parse
import urllib.request

from preflight import inspect

BASELINE = 'a370969b041b399c0778318d8915ce059fd74294'


def atomic_json(path, value):
    temporary = path.with_suffix(path.suffix + '.tmp')
    with temporary.open('w') as stream:
        json.dump(value, stream, indent=2)
        stream.write('\n')
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(path)


def request(url):
    with urllib.request.urlopen(url, timeout=10) as response:
        return json.load(response)


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
    for n in range(min(100, count)):
        ident = f'benchmark-{seed}-{n}'
        cursor = ''
        while True:
            query = urllib.parse.urlencode({'monitor_id':ident,'limit':500,'cursor':cursor})
            page = request(api+'/api/v1/history?'+query)
            for event in page['events']:
                entries += 1
                if event.get('kind') == 'intervention':
                    succeeded += event['type'] == 'action_succeeded'
                    unknown += event['type'] == 'action_unknown'
                    failed += event['type'] == 'action_failed'
            cursor = page.get('next_cursor','')
            if not cursor: break
            if entries > 1000000: raise RuntimeError('history audit exceeded bounded campaign event budget')
    return {'target_accepted':target_audit['actions'],'committed_successes':succeeded,
            'unknown_outcomes':unknown,'confirmed_failures':failed,'history_events':entries,
            'duplicate_interventions':target_audit['duplicate_actions'],
            'reconciled':target_audit['actions']==succeeded and unknown==0 and target_audit['duplicate_actions']==0}


def sha256(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as stream:
        for block in iter(lambda: stream.read(1 << 20), b''):
            digest.update(block)
    return digest.hexdigest()


def run_one(args, directory, binary, count, warmup, duration, upgraded, scenario='healthy'):
    directory.mkdir(parents=True)
    result = {'status': 'incomplete', 'monitors': count, 'interval_seconds': 60, 'warmup_seconds': warmup,
              'measurement_seconds': duration, 'scenario': scenario, 'declared_faults': [], 'binary_sha256': sha256(binary),
              'internal_percentiles': 'candidate' if upgraded else 'unavailable in baseline',
              'started_at': time.time(), 'seed': args.seed, 'samples': 0}
    atomic_json(directory / 'progress.json', result)
    target = subprocess.Popen([args.target, '-monitors', str(count)], stdout=subprocess.PIPE, stderr=(directory / 'target.log').open('wb'), text=True)
    controller = None
    logger = None
    failures = []
    try:
        target_url = target.stdout.readline().strip()
        if not target_url.startswith('http://127.0.0.1:'):
            raise RuntimeError('target did not provide its loopback address')
        config = directory / 'monitors.json'
        manifest(config, count, target_url, args.seed)
        port = args.api_port
        api = f'http://127.0.0.1:{port}'
        command = [binary, '-yaml', str(config), '-web.addr', f'127.0.0.1:{port}']
        if upgraded:
            runtime = directory / 'runtime.yaml'
            runtime.write_text('storage:\n  mode: raft\n  directory: ' + str(directory / 'state') + '\n')
            command += ['-runtime-config', str(runtime)]
        controller = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, cwd=directory)
        logger = threading.Thread(target=rotate_log, args=(controller.stdout, directory / 'controller.log'), daemon=True)
        logger.start()
        start_deadline = time.monotonic() + 1800
        while True:
            if controller.poll() is not None:
                raise RuntimeError('controller exited during load')
            try:
                overview = request(api + '/api/v1/overview')
                if overview['total'] == count:
                    break
            except (OSError, ValueError):
                pass
            if time.monotonic() > start_deadline:
                raise RuntimeError('controller did not load the complete fleet in 30 minutes')
            time.sleep(1)
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
                    urllib.request.urlopen(urllib.request.Request(target_url + '/fault?' + urllib.parse.urlencode(query), method='POST'), timeout=10).close()
                    fault_started, fault_stopped = True, False
                    fault_until = elapsed + (2 if scenario == 'burst' else 125)
                    result['declared_faults'].append({'episode':episode,'start':elapsed,'planned_end':fault_until,'cohort':cohort,'type':'cohort_outage' if soak_episode else scenario})
                if fault_started and not fault_stopped and elapsed >= fault_until:
                    urllib.request.urlopen(urllib.request.Request(target_url + '/fault?delay=0s&outage=false', method='POST'), timeout=10).close()
                    fault_stopped = True
                    result['declared_faults'][-1]['actual_end'] = elapsed
                if scenario == 'crash' and measurement_elapsed >= 30 and not crash_done:
                    controller.kill()
                    controller.wait(timeout=10)
                    logger.join(timeout=10)
                    controller = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, cwd=directory)
                    logger = threading.Thread(target=rotate_log,args=(controller.stdout,directory/'restart.log'),daemon=True)
                    logger.start()
                    recovery_deadline = time.monotonic()+600
                    while time.monotonic()<recovery_deadline:
                        try:
                            if request(api+'/api/v1/overview')['total']==count: break
                        except (OSError,ValueError): pass
                        time.sleep(1)
                    else: raise RuntimeError('crashed process did not recover the complete fleet')
                    crash_done = True
                    result['declared_faults'].append({'type':'process_kill','start':elapsed,'actual_end':time.monotonic()-started})
                    # This is a declared crash scenario, never uninterrupted soak.
                    previous_wall,previous_mono=time.time(),time.monotonic()
                in_fault_window = any(elapsed >= f['start'] and elapsed < f.get('actual_end',f.get('planned_end',elapsed))+300 for f in result['declared_faults'])
                audit_start = time.monotonic()
                audit = request(target_url + '/audit')
                sample = {'at': wall, 'elapsed': elapsed, 'target_audit': audit,
                          'target_audit_latency': time.monotonic() - audit_start,
                          'controller': process_resources(controller.pid), 'target': process_resources(target.pid),
                          'queues': request(api + '/api/v1/queues'), 'pools': request(api + '/api/v1/pools')}
                if previous_sample is not None:
                    seconds = wall-previous_sample['at']
                    target_cores=(sample['target']['cpu_seconds']-previous_sample['target']['cpu_seconds'])/max(seconds,.001)
                    sample['target_cpu_cores']=target_cores
                    if target_cores > max(1,os.cpu_count() or 1)*.8 or sample['target_audit_latency'] > .25:
                        target_saturated=True
                previous_sample=sample
                # A bounded dashboard page is read every sample during measurement.
                page = request(api + '/api/v1/monitors?size=100&page=' + str(1 + (result['samples'] * 997) % max(1, count // 100)))
                if page['total'] != count or len(page['monitors']) > 100:
                    raise RuntimeError('fleet navigation returned an invalid count or unbounded page')
                if upgraded:
                    sample['slo'] = request(api + '/api/v1/slo')
                    state = request(api + '/api/v1/state')
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
                    if upgraded and scenario == 'healthy' and args.mode != 'smoke' and not in_fault_window:
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
        final = request(target_url + '/audit?digest=true')
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
            if scenario == 'healthy' and rate < count / 60 * .99:
                failures.append('healthy achieved rate below 99% of specified cadence')
            if measured < duration - 6:
                failures.append('measurement duration incomplete')
        result['status'] = 'failed' if failures else ('harness_smoke_pass' if args.mode == 'smoke' else 'measured_pending_review')
    except (Exception, KeyboardInterrupt) as error:
        failures.append(str(error))
        result['status'] = 'incomplete'
    finally:
        if controller is not None:
            stop(controller)
        stop(target)
        if logger is not None:
            logger.join(timeout=10)
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
    out = pathlib.Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
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
        build_metadata = json.loads(pathlib.Path(args.builds).read_text())
        builds = {r['name']:r for r in build_metadata['builds']}
        if builds['baseline']['commit'] != BASELINE or builds['candidate']['dirty'] or builds['baseline']['go_version'] != builds['candidate']['go_version'] or builds['baseline']['flags'] != builds['candidate']['flags']:
            parser.error('candidate must be committed and compiler settings must match the published baseline')
        if builds['candidate']['sha256'] != sha256(args.candidate) or (args.mode == 'compare' and builds['baseline']['sha256'] != sha256(args.baseline)):
            parser.error('binary digest differs from the prepared build metadata')
        atomic_json(out/'builds.json',build_metadata)
    runs = []
    if args.mode == 'smoke':
        runs.append(run_one(args, out / 'smoke', args.candidate, 10, 0, 10, True))
    elif args.mode == 'soak':
        runs.append(run_one(args, out / 'soak', args.candidate, 1000000, 300, 24 * 3600, True))
    elif args.mode == 'faults':
        for scenario in ['burst', 'slow', 'outage', 'recovery', 'crash']:
            runs.append(run_one(args, out / scenario, args.candidate, 1000000, 300, 900, True, scenario))
    else:
        for count in [10000, 100000, 1000000]:
            for repeat in range(3):
                for name, binary, upgraded in [('baseline', str(pathlib.Path(args.baseline).resolve()), False), ('candidate', args.candidate, True)]:
                    runs.append(run_one(args, out / f'{name}-{count}-{repeat}', binary, count, 300, 900, upgraded))
                    if runs[-1]['status'] == 'incomplete':
                        break
    atomic_json(out / 'campaign.json', {'mode': args.mode, 'baseline_commit': BASELINE, 'runs': runs, 'release_gate_passed': False, 'review_required': True})
    return 1 if any(r['status'] in ('failed', 'incomplete') for r in runs) else 0


if __name__ == '__main__':
    raise SystemExit(main())
