#!/usr/bin/env python3
"""Bounded destination-fairness regression using actual local HTTP receivers."""
import argparse
import asyncio
from datetime import datetime
import hashlib
import json
import os
import pathlib
import secrets
import shutil
import signal
import tempfile
import time
import urllib.request


EXPECTED = {f'slow-{n}' for n in range(24)} | {f'healthy-{n}' for n in range(6)}


async def finish_cleanup(cleanup, timeout=60):
    """Finish owned cleanup despite cancellation, then let the caller propagate it."""
    finalizer = asyncio.create_task(asyncio.wait_for(cleanup(), timeout))
    cancelled = False
    while True:
        try:
            await asyncio.shield(finalizer)
            return cancelled, None
        except asyncio.CancelledError:
            cancelled = True
            if finalizer.cancelled():
                return True, 'cleanup cancelled'
        except Exception as error:
            return cancelled, type(error).__name__


def validate_actions(kind, items, effects):
    """Conserve every original intent and reconcile independently received effects."""
    if len(items) != len(EXPECTED) or {a['monitorID'] for a in items} != EXPECTED or len({a['id'] for a in items}) != len(EXPECTED):
        raise RuntimeError('action inventory lost or duplicated an intent')
    records = {a['monitorID']: a for a in items}
    timestamp = lambda value: datetime.fromisoformat(value.replace('Z', '+00:00')).timestamp()
    for identity, action in records.items():
        queued, deadline = timestamp(action['queuedAt']), timestamp(action['deadline'])
        if abs(deadline - queued - (45 if kind == 'notification' else 15)) > .001:
            raise RuntimeError('original action budget changed')
        started_at = timestamp(action['startedAt']) if action.get('startedAt') else None
        if started_at is not None and not queued <= started_at < deadline:
            raise RuntimeError('action started outside its authorization')
        received = effects.get(identity, [])
        if len(received) > 1:
            raise RuntimeError('receiver observed a repeated effect')
        if received:
            effect = received[0]
            if started_at is None or not started_at <= effect['received_wall'] <= deadline + .25:
                raise RuntimeError('receiver effect has no timely start')
            if kind == 'notification' and effect['action_id'] != action['id']:
                raise RuntimeError('notification effect belongs to a different action')
            if identity.startswith('slow-'):
                if action['state'] != 'unknown' or not action['held'] or 'closed_wall' not in effect:
                    raise RuntimeError('accepted effect did not remain unknown and held')
                # Network scheduling has a small tolerance; it cannot grant a
                # fresh operation budget to work that already waited in queue.
                call_deadline = min(deadline, started_at + (10 if kind == 'notification' else 15))
                if effect['closed_wall'] > call_deadline + 1:
                    raise RuntimeError('receiver connection exceeded original deadline')
            elif action['state'] != 'succeeded':
                raise RuntimeError('healthy receiver outcome was lost')
        elif identity.startswith('healthy-'):
            raise RuntimeError('healthy destination made no progress')
        elif started_at is None:
            if action['state'] != 'cancelled':
                raise RuntimeError('unstarted intent has no definite cancellation')
        elif action['state'] != 'unknown' or not action['held']:
            # A near-deadline partial request may not reach readexactly. The
            # receiver's missing complete body is not proof of no remote effect.
            raise RuntimeError('started unobserved intent lost its conservative hold')
    return records


async def scenario(binary, output, kind):
    work = server = probe = None
    token = secrets.token_urlsafe(32)
    slow_started = asyncio.Event()
    recovered = asyncio.Event()
    tasks, writers = set(), set()
    calls, effects, hung, observations = {}, {}, [], []
    active = peak_active = 0
    started = time.monotonic()
    process = None
    cancelled = False
    report = {'kind': kind, 'passed': False, 'slow_monitors': 24, 'healthy_monitors': 6,
              'scope': 'private real-network development regression; not frozen release qualification',
              'storage': 'raft', 'measurement_seconds': 35, 'fault_seconds': 20,
              'work_deadline_seconds': 70, 'cleanup_deadline_seconds': 60, 'hard_deadline_seconds': 150,
              'binary_sha256': None}

    async def target(reader, writer):
        nonlocal active, peak_active
        task = asyncio.current_task()
        tasks.add(task)
        writers.add(writer)
        holding = False
        try:
            header = await asyncio.wait_for(reader.readuntil(b'\r\n\r\n'), 5)
            path = header.split(b' ', 2)[1].decode('ascii')
            length = next((int(line.split(b':', 1)[1]) for line in header.split(b'\r\n') if line.lower().startswith(b'content-length:')), 0)
            if length > 65536:
                raise RuntimeError('fixture body limit exceeded')
            body = await reader.readexactly(length)
            if path.startswith('/check/'):
                if path.endswith('/healthy'):
                    await asyncio.wait_for(slow_started.wait(), 10)
                status = b'200 OK' if recovered.is_set() else b'500 Synthetic outage'
            elif path in ('/slow', '/healthy'):
                payload = json.loads(body)
                identity = payload['monitor_id']
                if identity not in EXPECTED or identity.split('-', 1)[0] != path[1:]:
                    raise RuntimeError('unexpected fixture identity')
                calls.setdefault(identity, []).append(time.monotonic() - started)
                effect = {'received_wall': time.time(), 'action_id': payload.get('action_id')}
                effects.setdefault(identity, []).append(effect)
                if path == '/slow':
                    # The complete request reached the receiver. Its effect is
                    # counted independently before deliberately losing a reply.
                    slow_started.set()
                    active += 1
                    peak_active = max(peak_active, active)
                    holding = True
                    began = time.monotonic()
                    await reader.read()
                    hung.append(time.monotonic() - began)
                    effect['closed_wall'] = time.time()
                    return
                status = b'200 OK'
            else:
                raise RuntimeError('unexpected fixture request')
            writer.write(b'HTTP/1.1 ' + status + b'\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
            await writer.drain()
        except (asyncio.IncompleteReadError, ConnectionError, asyncio.CancelledError):
            pass
        except Exception as error:
            report['receiver_error'] = type(error).__name__
        finally:
            if holding:
                active -= 1
            writer.close()
            writers.discard(writer)
            tasks.discard(task)


    def observe(route):
        request = urllib.request.Request(f'http://127.0.0.1:{api_port}{route}', headers={'Authorization': 'Bearer ' + token})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=2) as response:
            data = response.read(2 * 1024**2 + 1)
        if len(data) > 2 * 1024**2:
            raise RuntimeError('observation quota exceeded')
        return json.loads(data)

    async def cleanup():
        try:
            if process is not None and process.returncode is None:
                try:
                    process.send_signal(signal.SIGTERM)
                except ProcessLookupError:
                    pass
                try:
                    await asyncio.wait_for(process.wait(), 45)
                except asyncio.TimeoutError:
                    try:
                        process.kill()
                    except ProcessLookupError:
                        pass
                    await asyncio.wait_for(process.wait(), 5)
                    report.update(passed=False, shutdown='deadline exceeded')
        finally:
            if process is not None:
                report['exit_code'] = process.returncode
                if process.returncode != 0:
                    report['passed'] = False
            if server is not None: server.close()
            if probe is not None: probe.close()
            for writer in list(writers): writer.close()
            owned = list(tasks)
            for task in owned: task.cancel()
            await asyncio.wait_for(asyncio.gather(*owned, return_exceptions=True), 5)
            if server is not None: await asyncio.wait_for(server.wait_closed(), 5)
            if probe is not None: await asyncio.wait_for(probe.wait_closed(), 5)

    try:
        async with asyncio.timeout(70):
            with binary.open('rb') as executable:
                report['binary_sha256'] = hashlib.file_digest(executable, 'sha256').hexdigest()
            work = pathlib.Path(tempfile.mkdtemp(prefix=kind + '-', dir=output))
            (work / 'token').write_text(token)
            (work / 'token').chmod(0o600)
            server = await asyncio.start_server(target, '127.0.0.1', 0, limit=65536)
            port = server.sockets[0].getsockname()[1]
            monitors = []
            for group, count in (('slow', 24), ('healthy', 6)):
                for n in range(count):
                    identity = f'{group}-{n}'
                    monitor = {'id': identity, 'name': identity, 'enabled': True,
                               'pulse_check': {'type': 'http', 'interval': '1s', 'timeout': '2s', 'unhealthy_threshold': 1, 'healthy_threshold': 2,
                                               'config': {'url': f'http://127.0.0.1:{port}/check/{group}'}}}
                    destination = f'http://127.0.0.1:{port}/{group}'
                    if kind == 'notification':
                        monitor['codes'] = {'red': {'dispatch': True, 'notify': 'webhook', 'config': {'url': destination}}}
                    else:
                        monitor['intervention'] = {'action': 'webhook', 'max_attempts': 1, 'target': {
                            'url': destination, 'timeout': '15s', 'body': json.dumps({'monitor_id': identity})}}
                    monitors.append(monitor)
            (work / 'monitors.json').write_text(json.dumps({'monitors': monitors}))
            (work / 'runtime.yaml').write_text('storage:\n  mode: raft\n  directory: ' + str(work / 'state') + '\n  snapshot_interval: 20s\n')
            probe = await asyncio.start_server(lambda r, w: w.close(), '127.0.0.1', 0)
            api_port = probe.sockets[0].getsockname()[1]
            probe.close()
            await probe.wait_closed()
            environment = {key: os.environ[key] for key in ('PATH', 'HOME', 'LANG', 'GOMAXPROCS') if key in os.environ}
            environment.update(XDG_CONFIG_HOME=str(work / 'config'), XDG_STATE_HOME=str(work / 'user-state'),
                               TMPDIR=str(work), TZ='UTC', CPRA_LOG_FORMAT='json', CPRA_LOG_LEVEL='warn')
            log_path = output / f'{kind}.log'

            with log_path.open('xb') as log:
                process = await asyncio.create_subprocess_exec(str(binary), '-yaml', str(work / 'monitors.json'),
                    '-runtime-config', str(work / 'runtime.yaml'), '-web.addr', f'127.0.0.1:{api_port}',
                    '-web.auth-file', str(work / 'token'), stdout=log, stderr=log, cwd=work, env=environment)
                ready_by = time.monotonic() + 15
                while True:
                    if process.returncode is not None:
                        raise RuntimeError('application exited during startup')
                    try:
                        await asyncio.to_thread(observe, '/api/v1/readyz')
                        break
                    except Exception:
                        if time.monotonic() > ready_by:
                            raise RuntimeError('readiness deadline exceeded') from None
                        await asyncio.sleep(.1)
                measured = time.monotonic()
                peak_rss = peak_fds = 0
                previous_wall, previous_mono = time.time(), measured
                while time.monotonic() - measured < 35:
                    if time.monotonic() - measured >= 20:
                        recovered.set()  # Cancel unsent work; drain received effects before the final read.
                    if process.returncode is not None or log_path.stat().st_size > 8 * 1024**2:
                        raise RuntimeError('process exited or bounded log quota exceeded')
                    observations.append({'at': time.monotonic() - started, 'queues': await asyncio.to_thread(observe, '/api/v1/queues')})
                    peak_rss = max(peak_rss, int(pathlib.Path(f'/proc/{process.pid}/statm').read_text().split()[1]) * os.sysconf('SC_PAGE_SIZE'))
                    peak_fds = max(peak_fds, len(list(pathlib.Path(f'/proc/{process.pid}/fd').iterdir())))
                    await asyncio.sleep(.5)
                    wall, mono = time.time(), time.monotonic()
                    if abs((wall - previous_wall) - (mono - previous_mono)) > 1:
                        raise RuntimeError('clock discontinuity or sleep detected')
                    previous_wall, previous_mono = wall, mono
                actions = await asyncio.to_thread(observe, '/api/v2/actions?limit=500')
                if actions.get('nextCursor'):
                    raise RuntimeError('unexpected action page overflow')
                report['actions'] = actions['items']
                records = validate_actions(kind, actions['items'], effects)
                states = {key: value['state'] for key, value in records.items()}
                slow_first = min((times[0] for identity, times in calls.items() if identity.startswith('slow-')), default=None)
                healthy_delays = [calls[f'healthy-{n}'][0] - slow_first for n in range(6)] if slow_first is not None and all(f'healthy-{n}' in calls for n in range(6)) else []
                report.update(calls=calls, states=states, healthy_action_delays_seconds=healthy_delays,
                              completed_hang_seconds=hung, peak_active_slow_destination=peak_active,
                              peak_rss_bytes=peak_rss, peak_descriptors=peak_fds, observations=observations)
                report['passed'] = (len(healthy_delays) == 6 and max(healthy_delays) < 5 and min(healthy_delays) >= 0
                    # Later queued interventions retain their original deadline,
                    # so they may have only a short remaining execution budget.
                    and peak_active == 1 and hung and max(hung) > 8 and all(len(times) == 1 for times in calls.values())
                    and all(states.get(f'healthy-{n}') == 'succeeded' for n in range(6)) and 'receiver_error' not in report)
    except asyncio.CancelledError:
        cancelled = True
        report.update(passed=False, failure='cancelled')
    except Exception as error:
        report.update(passed=False, failure=type(error).__name__, detail=str(error) if isinstance(error, RuntimeError) else 'scenario could not complete')
    finally:
        cleanup_cancelled, cleanup_error = await finish_cleanup(cleanup)
        cancelled = cancelled or cleanup_cancelled
        if cancelled:
            report.update(passed=False, failure='cancelled')
        if cleanup_error:
            if process is not None and process.returncode is None:
                try:
                    process.kill()
                except ProcessLookupError:
                    pass
                try:
                    await asyncio.wait_for(process.wait(), 5)
                except asyncio.TimeoutError:
                    report['child_unreaped'] = True
            report.update(passed=False, cleanup_error=cleanup_error)
        report.update(seconds=time.monotonic()-started, calls=calls, effects=effects)
        if report['seconds'] > 150: report['passed'] = False
        if any(len(times) != 1 for times in calls.values()): report['passed'] = False
        try:
            validate_actions(kind, report.get('actions', []), effects)
        except (RuntimeError, KeyError, ValueError) as error:
            report.update(passed=False, final_accounting_error=str(error))
        if 'receiver_error' in report:
            report['passed'] = False
        (output / f'{kind}.json').write_text(json.dumps(report, indent=2) + '\n')
        if work is not None and (process is None or process.returncode is not None):
            shutil.rmtree(work)  # Only this invocation's disposable state and token.
        elif work is not None:
            report['retained_work_directory'] = str(work)
            (output / f'{kind}.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps({k: v for k, v in report.items() if k not in ('observations', 'calls', 'states', 'effects', 'actions')}), flush=True)
    if cancelled:
        raise asyncio.CancelledError
    return report['passed']


async def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=pathlib.Path)
    parser.add_argument('--output', required=True, type=pathlib.Path)
    args = parser.parse_args()
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    results = []
    for kind in ('notification', 'intervention'):
        results.append(await scenario(args.binary.resolve(), args.output.resolve(), kind))
    raise SystemExit(0 if all(results) else 1)


if __name__ == '__main__':
    asyncio.run(main())
