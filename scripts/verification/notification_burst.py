#!/usr/bin/env python3
"""Bounded, private Linux/WSL notification scenarios against a loopback receiver.

Run scenarios sequentially under the task's disk/process guard:
  burst: 30 monitors, red delay 2s, outage 150s, recovery 20s;
    40 monitors, red delay 3s, outage 180s, recovery 20s.
    --shared-colors uses yellow/red/green on one URL with threshold 2 and applies
    --receiver-delay to all colors (default 0.5s), outage 150s, recovery 60s,
    matching the retained 60-monitor setup.
  effect-500-restart: 1 monitor, record the red effect then return 500, restart
    after the committed unknown outcome, and prove that it is not repeated.
  queued-restart: 30 monitors, hold the first red response while capturing queued
    identities, kill the owner, then resume the same state and destinations.

--binary-sha256 and --build-record bind the executable to the selected successful
private build. Each --out must be a new directory with an existing parent.
The record includes source commit/diff, compiler, commands and harness digest.
Outputs are evidence, including on failure; this script never removes them.
The receiver observes calls independently of CPRa's committed action outcomes;
planned in-flight kills remain unknown and are never reissued by this harness.
These are bounded behavior checks, not account or performance qualification.
The receiver uses HTTP/1.0, as the retained fixtures did, with an explicit listen
backlog and bounded request evidence. Host-wide counters do not attribute a
timeout to this receiver. Cumulative clock drift makes a case incomplete.
"""
import argparse
import collections
import datetime as dt
import hashlib
import http.server
import json
import os
import pathlib
import secrets
import signal
import socket
import stat
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


MAX_OUTPUT = 512 * 1024**2
MAX_LOG = 16 * 1024**2
MAX_BODY = 1024**2
MAX_HEALTH_RECORDS = 60 * 900
MAX_ENVIRONMENT_SAMPLES = 200


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace('+00:00', 'Z')


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def write_json(path, value):
    with path.open('x', encoding='utf-8') as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write('\n')
        stream.flush()
        os.fsync(stream.fileno())


def output_bytes(directory):
    size = 0
    for parent, directories, files in os.walk(directory, followlinks=False):
        for name in directories + files:
            path = pathlib.Path(parent) / name
            try:
                info = path.lstat()
            except FileNotFoundError:
                # Snapshot files may be renamed while accounting runs.
                continue
            if stat.S_ISLNK(info.st_mode):
                raise RuntimeError('unexpected_output_link')
            size += info.st_blocks * 512
    return size


def seconds_between(start, end):
    if not start or not end:
        return None
    return (dt.datetime.fromisoformat(end.replace('Z', '+00:00')) -
            dt.datetime.fromisoformat(start.replace('Z', '+00:00'))).total_seconds()


def clock_reading():
    return {'wall_seconds': time.time(), 'monotonic_seconds': time.monotonic(),
            'boottime_seconds': time.clock_gettime(time.CLOCK_BOOTTIME) if hasattr(time, 'CLOCK_BOOTTIME') else None}


def clock_observation(initial, previous, current):
    elapsed = current['monotonic_seconds'] - initial['monotonic_seconds']
    step = current['monotonic_seconds'] - previous['monotonic_seconds']
    cumulative = current['wall_seconds'] - initial['wall_seconds'] - elapsed
    step_drift = current['wall_seconds'] - previous['wall_seconds'] - step
    boot = None
    if current.get('boottime_seconds') is not None and initial.get('boottime_seconds') is not None:
        boot = current['boottime_seconds'] - initial['boottime_seconds'] - elapsed
    return {'elapsed_seconds': elapsed, 'step_seconds': step, 'cumulative_wall_drift_seconds': cumulative,
            'step_wall_drift_seconds': step_drift, 'boottime_minus_monotonic_seconds': boot,
            'discontinuity': step > 10 or step < 0 or abs(step_drift) > 3 or abs(cumulative) > 3 or
                             boot is not None and abs(boot) > 3}


def environment_snapshot():
    """Bounded host/cgroup evidence, not attribution of another process's work."""
    result = {'at': utc(), 'scope': 'host/cgroup shared with unrelated work'}
    paths = {'load': '/proc/loadavg', 'cpu_pressure': '/proc/pressure/cpu',
             'io_pressure': '/proc/pressure/io', 'memory_pressure': '/proc/pressure/memory'}
    try:
        cgroup = next(line[3:] for line in pathlib.Path('/proc/self/cgroup').read_text().splitlines()
                      if line.startswith('0::'))
        if '..' not in pathlib.PurePosixPath(cgroup).parts:
            base = pathlib.Path('/sys/fs/cgroup') / cgroup.lstrip('/')
            paths.update({key: str(base / name) for key, name in
                          [('cgroup_cpu_limit', 'cpu.max'), ('cgroup_cpu', 'cpu.stat'),
                           ('cgroup_memory_limit', 'memory.max')]})
    except (OSError, StopIteration):
        pass
    for name, path in paths.items():
        try:
            with open(path, encoding='ascii') as stream:
                result[name] = stream.read(2048).strip()
        except OSError:
            result[name] = None
    try:
        with open('/proc/net/netstat', encoding='ascii') as stream:
            lines = stream.read(16384).splitlines()
        for index, line in enumerate(lines[:-1]):
            if line.startswith('TcpExt:'):
                counters = dict(zip(line.split()[1:], lines[index + 1].split()[1:]))
                result['host_listen'] = {key: int(counters[key]) for key in ('ListenOverflows', 'ListenDrops') if key in counters}
                break
    except (OSError, ValueError):
        result['host_listen'] = None
    return result


def summarize(ids, received, actions, expected_unknown=(), failed_responses=(), configured_colors=('red', 'green')):
    """Do not equate a received call, a written response, and a committed result."""
    expected, expected_unknown, failed_responses = set(ids), set(expected_unknown), set(failed_responses)
    problems, colors = [], {}
    for color in configured_colors:
        calls = [event for event in received if event['color'] == color]
        seen = collections.Counter(event['monitor_id'] for event in calls)
        facts = [action for action in actions if action.get('kind') == 'code' and action.get('color') == color]
        committed = collections.Counter(action['monitorID'] for action in facts if action['state'] == 'succeeded')
        states = collections.Counter(action['state'] for action in facts)
        unknown = sorted(action['id'] for action in facts if action['state'] == 'unknown')
        action_ids = {action['id'] for action in facts}
        expected_holds = expected_unknown & action_ids
        superseded = [row for row in facts if color == 'yellow' and row['state'] == 'cancelled' and
                      row.get('outcome') == 'notification_superseded' and not row.get('startedAt') and not row.get('deadline')]
        superseded_ids = {row['id'] for row in superseded}
        superseded_monitors = {row['monitorID'] for row in superseded}
        expected_received = expected - superseded_monitors
        expected_success = expected_received - {action['monitorID'] for action in facts if action['id'] in expected_holds}
        action_identity = {action['id']: (action['monitorID'], action.get('deliveryID'), action.get('endpointID'))
                           for action in facts}
        mismatches = [event['action_id'] for event in calls
                      if action_identity.get(event['action_id']) !=
                      (event['monitor_id'], event.get('delivery_id'), event.get('endpoint_id'))]
        waits = [value for action in facts if (value := seconds_between(action.get('queuedAt'), action.get('startedAt'))) is not None]
        colors[color] = {
            'calls': len(calls), 'distinct_monitors_received': len(seen),
            'missing_at_receiver': sorted(expected_received - seen.keys()),
            'extra_monitor_ids': sorted(seen.keys() - expected_received),
            'repeated_monitor_calls': {key: count for key, count in seen.items() if count != 1},
            'repeated_action_calls': {key: count for key, count in collections.Counter(event['action_id'] for event in calls).items() if count != 1},
            'repeated_delivery_calls': {key: count for key, count in collections.Counter(event['delivery_id'] for event in calls).items() if count != 1},
            'http_response_written': sum(event.get('response_written') is True for event in calls),
            'committed_states': dict(states), 'unknown_action_ids': unknown,
            'missing_confirmed_success': sorted(expected_success - committed.keys()),
            'expected_unknown_action_ids': sorted(expected_holds),
            'never_started_superseded_action_ids': sorted(superseded_ids),
            'max_prestart_seconds': max(waits, default=None),
            'waited_more_than_execution_budget': sum(value > 45 for value in waits),
            'receiver_action_ids_missing_from_api': sorted({event['action_id'] for event in calls} - action_ids),
            'receiver_action_identity_mismatch': sorted(set(mismatches)),
        }
        if set(seen) != expected_received or any(count != 1 for count in seen.values()):
            problems.append(color + '_receiver_coverage_or_duplicates')
        if set(committed) != expected_success or any(count != 1 for count in committed.values()):
            problems.append(color + '_committed_coverage_or_duplicates')
        if len(action_ids) != len(facts):
            problems.append(color + '_duplicate_action_records')
        if set(unknown) != expected_holds or any(action['state'] != 'succeeded' and action['id'] not in expected_holds | superseded_ids for action in facts):
            problems.append(color + '_non_success_outcomes')
        if colors[color]['receiver_action_ids_missing_from_api'] or mismatches:
            problems.append(color + '_unreconciled_receiver_identity')
        if any(event.get('response_written') is not True and event['action_id'] not in failed_responses for event in calls):
            problems.append(color + '_response_write_failure')
        if color == 'yellow':
            intent_count = collections.Counter(row['monitorID'] for row in facts)
            if set(intent_count) != expected or any(count != 1 for count in intent_count.values()):
                problems.append('yellow_original_intent_coverage_or_duplicates')
    if expected_unknown - {action['id'] for action in actions}:
        problems.append('missing_expected_unknown_action')
    return {'status': 'pass' if not problems else 'fail', 'problems': problems, 'colors': colors}


def restart_findings(before, after, restarted_at):
    """A restart may hold an in-flight action, but cannot replace its identity."""
    final = {action['id']: action for action in after}
    problems, queued, held = [], [], []
    identity = ('monitorID', 'deliveryID', 'endpointID', 'incarnationUID', 'notificationEpisode', 'queuedAt')
    for original in before:
        if original.get('kind') != 'code' or original.get('color') != 'red':
            continue
        action = final.get(original['id'])
        if action is None or any(action.get(key) != original.get(key) for key in identity):
            problems.append('restart_lost_original_identity')
            continue
        if original['state'] == 'queued':
            queued.append(original['id'])
            if action['state'] != 'succeeded' or seconds_between(restarted_at, action.get('startedAt')) is None:
                problems.append('queued_original_not_completed')
                continue
            if seconds_between(restarted_at, action['startedAt']) < 0:
                problems.append('queued_start_crossed_capture_boundary')
            if original.get('deadline') or original.get('executionBudget') != '45s':
                problems.append('queued_budget_contract_absent')
            if seconds_between(action.get('startedAt'), action.get('deadline')) != 45:
                problems.append('first_start_budget_not_preserved')
        elif original['state'] in ('started', 'unknown'):
            held.append(original['id'])
            if action['state'] != 'unknown' or any(action.get(key) != original.get(key) for key in ('startedAt', 'deadline')):
                problems.append('restart_changed_unknown_outcome_or_budget')
        elif original['state'] == 'succeeded' and action['state'] != 'succeeded':
            problems.append('restart_changed_confirmed_outcome')
    return {'problems': sorted(set(problems)), 'original_queued_action_ids': sorted(queued),
            'original_unknown_or_inflight_action_ids': sorted(held)}


def burst_boundaries(actions):
    """Report unexercised queue delay separately from a lost notification."""
    completed = [row for row in actions if row.get('kind') == 'code' and row.get('color') == 'red' and row.get('state') == 'succeeded']
    waited = [row['id'] for row in completed
              if (delay := seconds_between(row.get('queuedAt'), row.get('startedAt'))) is not None and delay > 45]
    successful = [row for row in actions if row.get('kind') == 'code' and row.get('state') == 'succeeded']
    invalid = [row['id'] for row in successful if row.get('executionBudget') != '45s' or
               seconds_between(row.get('startedAt'), row.get('deadline')) != 45]
    return {'successful_red_count': len(completed), 'successful_red_waited_over_45s': sorted(waited),
            'long_prestart_boundary_exercised': bool(waited), 'execution_budget_violations': sorted(invalid)}


def delivery_fields(path, data, colors, shared):
    """Accept incidentless warning envelopes, without making paths identities."""
    if not isinstance(data, dict):
        raise ValueError('invalid envelope')
    color = data.get('color')
    if color not in colors or path != '/alert/' + ('hook' if shared else color):
        raise ValueError('unexpected color or route')
    keys = ('monitor_id', 'action_id', 'delivery_id', 'episode_id', 'endpoint_id')
    if any(not isinstance(data.get(key), str) or not 1 <= len(data[key]) <= 1024 for key in keys):
        raise ValueError('missing identity')
    incident = data.get('incident_id', '')
    if not isinstance(incident, str) or len(incident) > 1024 or color != 'yellow' and not incident:
        raise ValueError('missing incident identity')
    return dict(((key, data[key]) for key in keys), incident_id=incident, color=color)


def green_exposure_findings(received, actions):
    """Provider correlation spans warning/escalation; action episodes can differ."""
    by_id = {row['id']: row for row in actions}
    missing = []
    for row in received:
        if row['color'] != 'green':
            continue
        opening = [old for old in received if old['color'] in ('yellow', 'red') and
                   old['monitor_id'] == row['monitor_id'] and old['episode_id'] == row['episode_id'] and
                   old.get('received_at', '') <= row.get('received_at', '')]
        if not any(by_id.get(old['action_id'], {}).get('startedAt') for old in opening):
            missing.append(row['action_id'])
    return sorted(missing)


def fleet_findings(value, ids, health):
    rows = value.get('monitors', [])
    if value.get('total') != len(ids) or len(rows) != len(ids) or {row.get('monitor_id') for row in rows} != set(ids):
        return ['incomplete_fleet_observation']
    if any(row.get('health') != health for row in rows):
        return [health + '_fleet_not_observed']
    return []


class Receiver:
    def __init__(self, directory, ids, delay, red_status=200, hold_red=False, shared=False):
        self.directory, self.ids, self.delay = directory, set(ids), delay
        self.red_status, self.hold_red = red_status, hold_red
        self.shared = shared
        self.colors = ('yellow', 'red', 'green') if shared else ('red', 'green')
        self.started, self.healthy = time.monotonic(), True
        self.lock = threading.Lock()
        self.received, self.checks, self.errors = [], collections.Counter(), []
        self.active = 0
        self.handlers, self.peak_handlers, self.accepted_connections = 0, 0, 0
        self.health_count, self.health_bytes, self.health_response_failures = 0, 0, 0
        self.max_health_response_seconds, self.max_heartbeat_gap_seconds = 0, 0
        self.health_log = (directory / 'health-requests.jsonl').open('x', encoding='utf-8', buffering=1)
        self.stopping = threading.Event()
        self.release_red = threading.Event()
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = 'HTTP/1.0'

            def setup(self):
                super().setup()
                self.connection.settimeout(5)

            def log_message(self, *_):
                pass

            def handle(self):
                with owner.lock:
                    owner.handlers += 1
                    owner.peak_handlers = max(owner.peak_handlers, owner.handlers)
                try:
                    super().handle()
                finally:
                    with owner.lock:
                        owner.handlers -= 1

            def respond(self, code):
                try:
                    self.send_response(code)
                    self.send_header('Content-Length', '2')
                    self.end_headers()
                    self.wfile.write(b'ok')
                    self.wfile.flush()
                    return True
                except (OSError, TimeoutError):
                    return False

            def do_GET(self):
                if self.path != '/health':
                    self.respond(404)
                    return
                entered, entered_at = time.monotonic(), utc()
                with owner.lock:
                    healthy = owner.healthy
                    owner.checks['healthy' if healthy else 'unhealthy'] += 1
                    active = owner.handlers
                status = 200 if healthy else 503
                written = self.respond(status)
                duration = time.monotonic() - entered
                event = {'received_at': entered_at, 'elapsed_seconds': entered - owner.started,
                         'response_at': utc(), 'response_seconds': duration, 'status': status,
                         'response_written': written, 'active_handlers': active}
                with owner.lock:
                    owner.health_response_failures += not written
                    owner.max_health_response_seconds = max(owner.max_health_response_seconds, duration)
                    encoded = json.dumps(event, sort_keys=True) + '\n'
                    if owner.health_count >= MAX_HEALTH_RECORDS or owner.health_bytes + len(encoded) > MAX_LOG:
                        if 'health_evidence_limit' not in owner.errors:
                            owner.errors.append('health_evidence_limit')
                    else:
                        try:
                            owner.health_log.write(encoded)
                            owner.health_count += 1
                            owner.health_bytes += len(encoded)
                        except OSError:
                            if 'health_evidence_write_failure' not in owner.errors:
                                owner.errors.append('health_evidence_write_failure')

            def do_POST(self):
                try:
                    length = int(self.headers.get('Content-Length', '0'))
                    if length < 1 or length > MAX_BODY:
                        raise ValueError()
                    data = json.loads(self.rfile.read(length))
                    event = delivery_fields(self.path, data, owner.colors, owner.shared)
                    if event['monitor_id'] not in owner.ids:
                        raise ValueError()
                    color = event['color']
                    event.update(color=color, received_at=utc(), elapsed_seconds=time.monotonic() - owner.started)
                    with owner.lock:
                        if len(owner.received) >= len(owner.ids) * 8:
                            raise ValueError()
                        owner.received.append(event)
                        owner.active += 1
                except (ValueError, TypeError, OSError, TimeoutError):
                    with owner.lock:
                        if len(owner.errors) < 64:
                            owner.errors.append('invalid_or_excess_receiver_request')
                    self.respond(400)
                    return
                try:
                    if color == 'red':
                        if owner.hold_red and not owner.release_red.wait(8):
                            with owner.lock:
                                owner.errors.append('restart_receiver_gate_deadline')
                    if color == 'red' or owner.shared:
                        owner.stopping.wait(owner.delay)
                    status = owner.red_status if color == 'red' else 200
                    written = self.respond(status)
                    with owner.lock:
                        event.update(response_written=written, response_at=utc(), response_status=status)
                        with (owner.directory / 'receiver.jsonl').open('a', encoding='utf-8') as stream:
                            stream.write(json.dumps(event, sort_keys=True) + '\n')
                except OSError:
                    with owner.lock:
                        if len(owner.errors) < 64:
                            owner.errors.append('receiver_evidence_write_failure')
                finally:
                    with owner.lock:
                        owner.active -= 1

        class Server(http.server.ThreadingHTTPServer):
            request_queue_size = max(128, len(owner.ids) * 2)

            def get_request(self):
                request, address = super().get_request()
                with owner.lock:
                    owner.accepted_connections += 1
                return request, address

        def heartbeat():
            previous = time.monotonic()
            while not owner.stopping.wait(0.1):
                current = time.monotonic()
                with owner.lock:
                    owner.max_heartbeat_gap_seconds = max(owner.max_heartbeat_gap_seconds, current - previous)
                previous = current

        self.server = Server(('127.0.0.1', 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.heartbeat_thread = threading.Thread(target=heartbeat, daemon=True)
        self.thread.start()
        self.heartbeat_thread.start()

    def set_health(self, healthy):
        with self.lock:
            self.healthy = healthy

    def snapshot(self):
        with self.lock:
            return [dict(row) for row in self.received], {
                'shared_target': {phase: self.checks[phase] for phase in ('healthy', 'unhealthy')}
            }, list(self.errors)

    def timing(self):
        with self.lock:
            return {'protocol': 'HTTP/1.0', 'listen_backlog': self.server.request_queue_size, 'socket_timeout_seconds': 5,
                    'normal_response_status': 200, 'response_body_bytes': 2,
                    'accepted_connections': self.accepted_connections, 'active_handlers': self.handlers,
                    'peak_handlers': self.peak_handlers, 'health_records': self.health_count,
                    'health_response_write_failures': self.health_response_failures,
                    'max_health_response_seconds': self.max_health_response_seconds,
                    'max_heartbeat_gap_seconds': self.max_heartbeat_gap_seconds,
                    'health_record_limit': MAX_HEALTH_RECORDS, 'health_byte_limit': MAX_LOG,
                    'timing_scope': 'handler entry through response write; excludes requests not accepted by the server'}

    def close(self):
        self.stopping.set()
        self.release_red.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        self.heartbeat_thread.join(timeout=1)
        until = time.monotonic() + 6
        while time.monotonic() < until:
            with self.lock:
                if not self.active and not self.handlers:
                    break
            time.sleep(0.05)
        with self.lock:
            if self.active or self.handlers:
                self.errors.append('receiver_drain_deadline')
            try:
                self.health_log.flush()
                os.fsync(self.health_log.fileno())
                self.health_log.close()
            except OSError:
                self.errors.append('health_evidence_close_failure')


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=pathlib.Path, required=True)
    parser.add_argument('--binary-sha256', required=True)
    parser.add_argument('--build-record', type=pathlib.Path, required=True)
    parser.add_argument('--out', type=pathlib.Path, required=True)
    parser.add_argument('--scenario', choices=('burst', 'effect-500-restart', 'queued-restart'), default='burst')
    parser.add_argument('--shared-colors', action='store_true', help='Yellow/red/green share one receiver URL; threshold 2 and every response uses the selected delay')
    parser.add_argument('--monitors', type=int, choices=(1, 30, 40, 60), required=True)
    parser.add_argument('--outage-seconds', type=int)
    parser.add_argument('--recovery-seconds', type=int)
    parser.add_argument('--receiver-delay', type=float)
    args = parser.parse_args()
    if ((args.scenario == 'effect-500-restart' and args.monitors != 1) or
            (args.scenario == 'queued-restart' and args.monitors != 30) or
            (args.scenario == 'burst' and args.monitors == 1)):
        parser.error('burst uses 30/40/60 monitors; effect-500-restart uses 1; queued-restart uses 30')
    if args.shared_colors and (args.scenario != 'burst' or args.monitors != 60):
        parser.error('shared colors apply only to the 60-monitor burst')
    if args.outage_seconds is None:
        args.outage_seconds = 180 if args.monitors == 40 else 150
    if args.recovery_seconds is None:
        args.recovery_seconds = 20 if args.scenario == 'burst' and args.monitors in (30, 40) else 60
    if args.receiver_delay is None:
        args.receiver_delay = 0.5 if args.shared_colors else 3 if args.monitors == 40 else 2
    if not 150 <= args.outage_seconds <= 600 or not 20 <= args.recovery_seconds <= 180 or not 0 < args.receiver_delay <= 5:
        parser.error('scenario durations outside bounded qualification range')
    limit = (90 if args.scenario != 'burst' else 45) + 8 + args.outage_seconds + args.recovery_seconds + 60
    if limit > 900:
        parser.error('each scenario must finish within 15 minutes including shutdown')
    binary = args.binary.resolve(strict=True)
    if digest(binary) != args.binary_sha256:
        parser.error('binary digest differs from the selected build')
    if args.build_record.stat().st_size > MAX_BODY:
        parser.error('build record exceeds its byte bound')
    build = json.loads(args.build_record.read_text())
    if build.get('exit') != 0 or build.get('stop_reason') or not build.get('head') or not build.get('compiler'):
        parser.error('successful source/compiler build record required')
    command = build.get('command', [])
    try:
        compiled = pathlib.Path(command[command.index('-o') + 1])
        if len(command) < 2 or command[1] != 'build' or not compiled.is_absolute() or compiled.resolve() != binary:
            raise ValueError()
    except (ValueError, IndexError, TypeError):
        parser.error('build record must identify the selected absolute output binary')
    out = args.out.absolute()
    out.mkdir(mode=0o700, parents=False, exist_ok=False)
    os.umask(0o077)
    initial_clock = previous_clock = clock_reading()
    started = initial_clock['monotonic_seconds']
    interrupted, log_error = threading.Event(), threading.Event()
    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, lambda *_: interrupted.set())
    ids = [f'svc{number:03d}' for number in range(args.monitors)]
    colors = ('yellow', 'red', 'green') if args.shared_colors else ('red', 'green')
    unhealthy_threshold = 2 if args.shared_colors else 1
    report = {'status': 'incomplete', 'scope': 'private development behavior check; no publication or provider-account qualification',
              'started_at': utc(), 'source': build, 'binary_sha256': args.binary_sha256,
              'harness_sha256': digest(pathlib.Path(__file__)), 'monitors': args.monitors,
              'scenario': args.scenario, 'platform': dict(zip(('system', 'node', 'release', 'version', 'machine'), os.uname())),
              'python_version': sys.version.split()[0],
              'clocks': {name: vars(time.get_clock_info(name)) for name in ('time', 'monotonic')},
              'interval_seconds': 2, 'check_timeout_seconds': 1, 'healthy_threshold': 2,
              'unhealthy_threshold': unhealthy_threshold, 'colors': colors, 'shared_color_destination': args.shared_colors,
              'check_accounting': 'one shared health URL; target counts are aggregate, complete API pages establish per-monitor observations',
              'outage_seconds': args.outage_seconds, 'recovery_seconds': args.recovery_seconds,
              'red_receiver_delay_seconds': args.receiver_delay,
              'green_receiver_delay_seconds': args.receiver_delay if args.shared_colors else 0, 'hard_limit_seconds': limit}
    report['clock_limits'] = {'cumulative_drift_seconds': 3, 'step_drift_seconds': 3, 'observation_gap_seconds': 10}
    report['receiver_profile'] = {'protocol': 'HTTP/1.0', 'listen_backlog': max(128, args.monitors * 2),
                                  'socket_timeout_seconds': 5, 'health_record_limit': MAX_HEALTH_RECORDS,
                                  'normal_response_status': 200, 'response_body_bytes': 2,
                                  'health_byte_limit': MAX_LOG, 'reason': 'match retained protocol; bound receiver capacity and evidence'}
    write_json(out / 'input.json', report)
    receiver, process, logger, final_actions = None, None, None, None
    process_number, restart_actions, expected_unknown, failed_responses = 0, [], [], []
    observation_problems = []
    peaks = {'rss_bytes': 0, 'descriptors': 0}
    last_accounting = 0
    environment_samples = []
    token = secrets.token_hex(32)
    try:
        receiver = Receiver(out, ids, args.receiver_delay,
                            red_status=500 if args.scenario == 'effect-500-restart' else 200,
                            hold_red=args.scenario == 'queued-restart', shared=args.shared_colors)
        target = 'http://127.0.0.1:' + str(receiver.server.server_port)
        monitors = [{'id': monitor, 'name': monitor, 'pulse_check': {'type': 'http', 'interval': '2s', 'timeout': '1s',
                    'unhealthy_threshold': unhealthy_threshold, 'healthy_threshold': 2, 'config': {'url': target + '/health'}},
                    'codes': {color: {'dispatch': True, 'notify': 'webhook', 'config': {'url': target + '/alert/' + ('hook' if args.shared_colors else color)}}
                              for color in colors}} for monitor in ids]
        write_json(out / 'monitors.json', {'monitors': monitors})
        write_json(out / 'runtime.json', {'storage': {'mode': 'raft', 'directory': str(out / 'data'),
                   'batch_delay': '5ms', 'batch_size': 1000, 'snapshot_interval': '5m', 'snapshot_retain': 3},
                   'history': {'retention_days': 30}})
        with socket.socket() as reservation:
            reservation.bind(('127.0.0.1', 0))
            port = reservation.getsockname()[1]
        base = 'http://127.0.0.1:' + str(port)
        command = [str(binary), '-yaml', str(out / 'monitors.json'), '-runtime-config', str(out / 'runtime.json'),
                   '-data-dir', str(out / 'data'), '-web.addr', '127.0.0.1:' + str(port)]
        env = {key: value for key, value in os.environ.items() if not key.startswith('CPRA_AUTH_TOKEN')}
        env['CPRA_AUTH_TOKEN'] = token
        report['command'] = command
        report['processes'] = []

        def collect_log(child, name):
            pending, written, marker = b'', 0, token.encode()
            try:
                with (out / name).open('xb') as stream:
                    while chunk := os.read(child.stdout.fileno(), 4096):
                        pending += chunk
                        if marker in pending:
                            log_error.set()
                            pending = pending.replace(marker, b'[redacted token]')
                        split = max(0, len(pending) - len(marker) + 1)
                        safe, pending = pending[:split], pending[split:]
                        if written + len(safe) > MAX_LOG:
                            log_error.set()
                        else:
                            stream.write(safe)
                            written += len(safe)
                    if written + len(pending) <= MAX_LOG:
                        stream.write(pending)
                    else:
                        log_error.set()
                    stream.flush()
                    os.fsync(stream.fileno())
            except OSError:
                log_error.set()

        def launch():
            nonlocal process, logger, process_number
            process_number += 1
            log_name = f'controller-{process_number}.log'
            report['processes'].append({'started_at': utc(), 'log': log_name})
            process = subprocess.Popen(command, cwd=out, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            logger = threading.Thread(target=collect_log, args=(process, log_name), daemon=True)
            logger.start()

        def stop_process(force=False):
            nonlocal process, logger
            if process is None:
                return
            child = process
            active = child.poll() is None
            try:
                if active:
                    child.send_signal(signal.SIGKILL if force else signal.SIGTERM)
                child.wait(timeout=5 if force else 45)
            except ProcessLookupError:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)
                report.update(status='incomplete', stop_reason='shutdown_deadline')
            report['processes'][-1].update(stopped_at=utc(), exit=child.returncode, requested_kill=force)
            if child.returncode != (-signal.SIGKILL if force and active else 0):
                report.update(status='incomplete', stop_reason=report.get('stop_reason', 'process_exit_failure'))
            if logger is not None:
                logger.join(timeout=5)
                if logger.is_alive() or log_error.is_set():
                    report.update(status='incomplete', stop_reason='log_capture_failure')
            process, logger = None, None

        launch()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

        def api(path):
            request = urllib.request.Request(base + path, headers={'Authorization': 'Bearer ' + token})
            with opener.open(request, timeout=3) as response:
                raw = response.read(MAX_BODY + 1)
                if len(raw) > MAX_BODY:
                    raise RuntimeError('api_response_byte_limit')
                return json.loads(raw)

        def guard():
            nonlocal previous_clock, last_accounting
            current = clock_reading()
            clock = current['monotonic_seconds']
            observation = clock_observation(initial_clock, previous_clock, current)
            report['last_clock_observation'] = observation
            if interrupted.is_set() or clock - started > limit or process.poll() is not None or log_error.is_set():
                raise RuntimeError('interrupted_deadline_process_or_log_failure')
            if observation['discontinuity']:
                raise RuntimeError('unobserved_pause_or_clock_discontinuity')
            previous_clock = current
            if clock - last_accounting > 5:
                last_accounting = clock
                if len(environment_samples) >= MAX_ENVIRONMENT_SAMPLES:
                    raise RuntimeError('environment_evidence_limit')
                environment_samples.append({'clock': observation, 'receiver': receiver.timing(),
                                            'environment': environment_snapshot()})
                _, _, errors = receiver.snapshot()
                if errors:
                    raise RuntimeError('receiver_failure')
                if output_bytes(out) > MAX_OUTPUT or os.statvfs(out).f_bavail * os.statvfs(out).f_frsize < 64 * 1024**2:
                    raise RuntimeError('output_quota_or_disk_reserve')
                status = pathlib.Path(f'/proc/{process.pid}/status').read_text()
                for line in status.splitlines():
                    if line.startswith('VmRSS:'):
                        peaks['rss_bytes'] = max(peaks['rss_bytes'], int(line.split()[1]) * 1024)
                peaks['descriptors'] = max(peaks['descriptors'], len(list(pathlib.Path(f'/proc/{process.pid}/fd').iterdir())))

        def wait(seconds):
            until, progress_at = time.monotonic() + seconds, time.monotonic()
            while time.monotonic() < until:
                guard()
                if time.monotonic() >= progress_at:
                    calls, _, _ = receiver.snapshot()
                    print(json.dumps({'phase': report.get('phase'), 'elapsed_seconds': round(time.monotonic() - started, 1),
                                      'receiver_calls': dict(collections.Counter(row['color'] for row in calls))}), flush=True)
                    progress_at = time.monotonic() + 20
                interrupted.wait(min(0.25, max(0, until - time.monotonic())))

        def actions():
            rows, cursor = [], ''
            for _ in range(10):
                query = urllib.parse.urlencode({'limit': 500, **({'cursor': cursor} if cursor else {})})
                page = api('/api/v2/actions?' + query)
                rows.extend(page['items'])
                cursor = page.get('nextCursor', '')
                if not cursor:
                    return rows
            raise RuntimeError('action_pagination_limit')

        def capture(name):
            value = {'at': utc(), 'actions': actions(), 'monitors': api('/api/v1/monitors?size=500'),
                     'state': api('/api/v1/state'), 'slo': api('/api/v1/slo')}
            write_json(out / (name + '.json'), value)
            return value

        def ready():
            deadline = time.monotonic() + 45
            while True:
                guard()
                try:
                    api('/api/v1/readyz')
                    fleet = api('/api/v1/monitors?size=500')
                    if fleet['total'] == len(ids):
                        return
                except (OSError, ValueError):
                    pass
                if time.monotonic() > deadline:
                    raise RuntimeError('startup_readiness_deadline')
                interrupted.wait(0.25)

        report['phase'] = 'startup'
        ready()
        report['phase'] = 'warmup'
        wait(8)
        observation_problems.extend(fleet_findings(capture('warmup')['monitors'], ids, 'healthy'))
        receiver.set_health(False)
        report['phase'], report['outage_started_at'] = 'outage', utc()
        outage_end = time.monotonic() + args.outage_seconds
        if args.scenario != 'burst':
            boundary_deadline = time.monotonic() + 20
            while True:
                guard()
                rows = [row for row in actions() if row.get('kind') == 'code' and row.get('color') == 'red']
                if args.scenario == 'effect-500-restart':
                    complete = len(rows) == 1 and rows[0]['state'] == 'unknown'
                else:
                    complete = len(rows) == len(ids) and any(row['state'] == 'queued' for row in rows) and any(row['state'] == 'started' for row in rows)
                    calls, _, _ = receiver.snapshot()
                    complete = complete and {row['action_id'] for row in calls} == {row['id'] for row in rows if row['state'] == 'started'}
                if complete:
                    restart_actions = rows
                    break
                if time.monotonic() > boundary_deadline:
                    raise RuntimeError('restart_boundary_not_observed')
                interrupted.wait(0.1)
            calls, _, _ = receiver.snapshot()
            expected_unknown = [row['id'] for row in restart_actions if row['state'] in ('started', 'unknown')]
            if not expected_unknown or any(row['state'] not in ('queued', 'started', 'unknown', 'succeeded') for row in restart_actions):
                raise RuntimeError('unexpected_restart_boundary')
            if args.scenario == 'queued-restart':
                # The response gate holds execution permits while this exact
                # original queued set is captured and the owner is stopped.
                if receiver.release_red.is_set() or set(row['action_id'] for row in calls) != set(expected_unknown):
                    raise RuntimeError('queued_restart_receiver_boundary_not_proven')
                failed_responses = expected_unknown
            write_json(out / 'before-restart.json', {'at': utc(), 'actions': restart_actions, 'received': calls})
            report['phase'] = 'planned-process-kill'
            stop_process(force=True)
            receiver.release_red.set()
            if report.get('stop_reason'):
                raise RuntimeError('planned_process_stop_failed')
            report['restarted_at'] = utc()
            launch()
            report['phase'] = 'restart'
            ready()
            capture('after-restart')
            report['phase'] = 'outage-after-restart'
        wait(max(0, outage_end - time.monotonic()))
        observation_problems.extend(fleet_findings(capture('outage')['monitors'], ids, 'unhealthy'))
        receiver.set_health(True)
        report['phase'], report['recovery_started_at'] = 'recovery', utc()
        wait(args.recovery_seconds)
        final = capture('final')
        observation_problems.extend(fleet_findings(final['monitors'], ids, 'healthy'))
        final_actions = final['actions']
        calls, checks, receiver_errors = receiver.snapshot()
        report.update(summarize(ids, calls, final['actions'], expected_unknown, failed_responses, colors))
        report.update(check_counts=checks, receiver_errors=receiver_errors, process_peaks=peaks)
        if receiver_errors or any(not counts['healthy'] or not counts['unhealthy'] for counts in checks.values()):
            report['status'] = 'fail'
            report['problems'].append('receiver_or_check_coverage')
        if digest(binary) != args.binary_sha256:
            raise RuntimeError('binary_changed_during_run')
    except BaseException as error:
        report.update(status='incomplete', stop_reason=type(error).__name__)
        if isinstance(error, RuntimeError):
            report['stop_reason'] = str(error)
    finally:
        if process is not None:
            stop_process()
        if receiver is not None:
            receiver.close()
            calls, checks, errors = receiver.snapshot()
            timing = receiver.timing()
            write_json(out / 'receiver-summary.json', {'received': calls, 'checks': checks, 'errors': errors, 'timing': timing})
            report['receiver_timing'] = timing
            if final_actions is not None and report['status'] != 'incomplete':
                # Include any extra calls during drain in reconciliation; they
                # must not disappear merely because the API was read earlier.
                report.update(summarize(ids, calls, final_actions, expected_unknown, failed_responses, colors))
                report.update(check_counts=checks, receiver_errors=errors)
                report['green_without_correlated_started_opening'] = green_exposure_findings(calls, final_actions)
                if report['green_without_correlated_started_opening']:
                    report['status'] = 'fail'
                    report['problems'].append('green_without_correlated_started_opening')
                if observation_problems:
                    report['status'] = 'fail'
                    report['problems'].extend(observation_problems)
                if any(not counts['healthy'] or not counts['unhealthy'] for counts in checks.values()):
                    report['status'] = 'fail'
                    report['problems'].append('check_coverage')
                if restart_actions:
                    report['restart'] = restart_findings(restart_actions, final_actions, report['restarted_at'])
                    if report['restart']['problems']:
                        report['status'] = 'fail'
                        report['problems'].extend(report['restart']['problems'])
                if args.scenario == 'burst':
                    report['boundaries'] = burst_boundaries(final_actions)
                    report['boundaries']['long_prestart_required'] = not args.shared_colors
                    if report['boundaries']['execution_budget_violations']:
                        report['status'] = 'fail'
                        report['problems'].append('successful_notification_execution_budget_changed')
                    if report['status'] == 'pass' and not args.shared_colors and not report['boundaries']['long_prestart_boundary_exercised']:
                        report.update(status='incomplete', stop_reason='long_prestart_boundary_unexercised')
            if errors:
                report.update(status='incomplete', stop_reason='receiver_failure')
        final_clock = clock_observation(initial_clock, previous_clock, clock_reading())
        report['final_clock_observation'] = final_clock
        # Shutdown can legitimately exceed one observation interval. A changed
        # clock offset still invalidates wall-timestamp duration evidence.
        if (abs(final_clock['cumulative_wall_drift_seconds']) > 3 or
                final_clock['boottime_minus_monotonic_seconds'] is not None and
                abs(final_clock['boottime_minus_monotonic_seconds']) > 3):
            report.update(status='incomplete', stop_reason='unobserved_pause_or_clock_discontinuity')
        write_json(out / 'environment.json', {'initial_clock': initial_clock, 'samples': environment_samples,
                                            'final_clock': final_clock, 'final_environment': environment_snapshot()})
        report.update(finished_at=utc(), elapsed_seconds=time.monotonic() - started)
        write_json(out / 'result.json', report)
    print(json.dumps({'result': str(out / 'result.json'), 'status': report['status']}), flush=True)
    return 0 if report['status'] == 'pass' else 1


if __name__ == '__main__':
    raise SystemExit(main())
