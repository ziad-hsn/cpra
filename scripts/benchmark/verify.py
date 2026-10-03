#!/usr/bin/env python3
"""Check retained scale-campaign evidence without executing or publishing CPRa.

The output establishes consistency of the recorded inputs and observations. It
does not attest their provenance or replace review of resource stability and the
host environment. No output from this command authorizes publication.
"""
import argparse
import gzip
import hashlib
import json
import math
from pathlib import Path, PurePosixPath
import re
import tarfile

if __package__:
    from . import prepare
    from .preflight import MINIMUM_HOST_FREE
else:
    import prepare
    from preflight import MINIMUM_HOST_FREE


REPORT_LIMIT = 16 * 1024 ** 2
LINE_LIMIT = 4 * 1024 ** 2
SAMPLE_LIMIT = 512 * 1024 ** 2
FILE_LIMIT = 2 * 1024 ** 3
FAULTS = ('burst', 'slow', 'outage', 'recovery', 'crash')


def require(condition, detail):
    if not condition:
        raise ValueError('Invalid benchmark evidence: ' + detail)


def number(value, minimum=0):
    return type(value) in (int, float) and math.isfinite(value) and value >= minimum


def integer(value, minimum=0):
    return type(value) is int and value >= minimum


def digest(value):
    return isinstance(value, str) and re.fullmatch('[a-f0-9]{64}', value) is not None


def close(left, right):
    return number(left) and number(right) and math.isclose(left, right, rel_tol=1e-9, abs_tol=1e-6)


def decode(data, name):
    def fields(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, name + ': duplicate JSON field')
            result[key] = value
        return result

    def finite(value):
        parsed = float(value)
        require(math.isfinite(parsed), name + ': non-finite JSON number')
        return parsed

    try:
        value = json.loads(data, object_pairs_hook=fields, parse_float=finite, parse_constant=finite)
    except (UnicodeError, json.JSONDecodeError, RecursionError) as error:
        raise ValueError('Invalid benchmark evidence JSON: ' + name) from error
    require(isinstance(value, dict), name + ': expected an object')
    return value


class Evidence:
    """Bounded reads of fixed artifact names, retaining the bytes actually read."""
    def __init__(self, root):
        self.root = Path(root).resolve(strict=True)
        require(self.root.is_dir(), 'evidence root is not a directory')
        self.files = {}

    def path(self, name):
        relative = PurePosixPath(name)
        require(not relative.is_absolute() and '..' not in relative.parts
                and str(relative) == name and '\\' not in name, 'unsafe evidence path')
        path = self.root / relative
        require(path.resolve(strict=True) == path and path.is_file(), name + ': expected a regular, unaliased file')
        require(path.stat().st_size <= FILE_LIMIT, name + ': file exceeds 2 GiB')
        return path

    def record(self, name):
        path = self.path(name)
        value = prepare.sha256(path)
        if name in self.files:
            require(self.files[name] == value, name + ': evidence changed during verification')
        self.files[name] = value
        return path

    def read(self, name):
        path = self.path(name)
        with path.open('rb') as stream:
            data = stream.read(REPORT_LIMIT + 1)
        require(len(data) <= REPORT_LIMIT, name + ': report exceeds 16 MiB')
        value = hashlib.sha256(data).hexdigest()
        require(name not in self.files or self.files[name] == value, name + ': evidence changed during verification')
        self.files[name] = value
        return decode(data, name)

    def unchanged(self):
        for name in self.files:
            self.record(name)


def prepared_inputs(evidence, commit):
    metadata = evidence.read('builds.json')
    require(evidence.read('RELEASE.json') == metadata.get('release'), 'prepared release metadata changed')
    programs = [evidence.record(name) for name in ('candidate', 'target', 'baseline')]
    prepare.verify_builds(metadata, *programs)
    require(metadata['tags'] == prepare.RECIPE['build_tags'], 'qualification requires the recorded full-driver build')
    rows = {row['name']: row for row in metadata['builds']}
    require(rows['candidate']['commit'] == commit, 'candidate commit differs from the selected source')
    for name in ('baseline', 'candidate'):
        row = rows[name]
        inventory = evidence.read(name + '-source.json')
        require(prepare.digest_json(inventory) == row['source_tree_sha256'], name + ': source inventory digest differs')
        archive_name = name + '-source.tar'
        archive = evidence.record(archive_name)
        require(evidence.files[archive_name] == row['source_sha256'], name + ': source archive digest differs')
        observed = {}
        total = 0
        with tarfile.open(archive, mode='r:') as stream:
            require(stream.pax_headers.get('comment') == row['commit'], name + ': archive commit is absent or different')
            for member in stream:
                path = PurePosixPath(member.name)
                require(not path.is_absolute() and '..' not in path.parts and '\\' not in member.name
                        and str(path) == member.name.rstrip('/'), name + ': unsafe source member')
                if member.isdir():
                    continue
                require(member.name not in observed and len(observed) < 100000, name + ': duplicate or excessive source members')
                if member.issym():
                    record = {'kind': 'symlink', 'target': member.linkname}
                else:
                    require(member.isfile() and not member.islnk(), name + ': unsupported source member')
                    total += member.size
                    require(total <= FILE_LIMIT, name + ': source payload exceeds 2 GiB')
                    with stream.extractfile(member) as source:
                        record = {'kind': 'file', 'mode': member.mode & 0o777,
                                  'sha256': hashlib.file_digest(source, 'sha256').hexdigest()}
                observed[member.name] = record
        require(observed == inventory, name + ': archive and recorded source inventory differ')
        if name == 'candidate':
            prepare.validate_source(metadata['release'], inventory)
    return metadata, rows


def trials(mode):
    if mode == 'compare':
        return [(f'{build}-{count}-{repeat}', build, count, 900, 'healthy')
                for count in (10000, 100000, 1000000) for repeat in range(3)
                for build in ('baseline', 'candidate')]
    if mode == 'faults':
        return [(name, 'candidate', 1000000, 900, name) for name in FAULTS]
    return [('soak', 'candidate', 1000000, 86400, 'healthy')]


def host_identity(report):
    require(report.get('ready') is True and report.get('baseline_commit') == prepare.BASELINE,
            'physical host preflight did not pass for the selected baseline')
    allowance = report.get('required_fixture_headroom_bytes')
    require(report.get('required_host_free_bytes') == MINIMUM_HOST_FREE and integer(allowance)
            and integer(report.get('host_free_bytes'), MINIMUM_HOST_FREE + allowance), 'physical host space was insufficient')
    require(integer(report.get('cpu_count'), 1) and isinstance(report.get('hostname'), str)
            and bool(report['hostname'].strip()), 'host CPU count or identity is missing')
    platform = report.get('platform')
    require(isinstance(platform, str) and platform.startswith('Linux-')
            and ('microsoft' in platform.lower() or 'wsl' in platform.lower()), 'campaign did not record the selected WSL environment')
    base = report.get('ubuntu_vhd_base')
    require(isinstance(base, str) and base.lower().startswith('c:'), 'Ubuntu backing storage is not on C:')
    return {key: report[key] for key in ('hostname', 'platform', 'cpu_count', 'ubuntu_vhd_base')}


def fault_intervals(result, mode, scenario, measurement_start):
    faults = result.get('declared_faults')
    expected = 24 if mode == 'soak' else (0 if scenario == 'healthy' else 1)
    require(isinstance(faults, list) and len(faults) == expected, 'declared fault campaign is incomplete')
    for index, fault in enumerate(faults):
        require(isinstance(fault, dict), 'fault must be an object')
        kind = 'cohort_outage' if mode == 'soak' else ('process_kill' if scenario == 'crash' else scenario)
        require(fault.get('type') == kind and number(fault.get('start'))
                and number(fault.get('actual_end'), fault['start']), 'fault type or completion is missing')
        offset = 60 + index * 3600 if mode == 'soak' else 30
        # Injection is scheduled relative to the end of the five-minute warmup.
        require(300 + offset <= fault['start'] <= 320 + offset, 'fault began outside its scheduled observation interval')
        if kind != 'process_kill':
            duration = 2 if scenario == 'burst' else 125
            require(close(fault.get('planned_end'), fault['start'] + duration)
                    and fault['planned_end'] <= fault['actual_end'] <= fault['planned_end'] + 20,
                    'fault duration differs from the agreed workload')
            cohort = 100 if mode == 'soak' or scenario == 'recovery' else 1000000
            require(type(fault.get('cohort')) is int and fault['cohort'] == cohort
                    and type(fault.get('episode')) is int and fault['episode'] == index + 1,
                    'fault cohort or episode differs')
        else:
            require(fault['actual_end'] - fault['start'] <= 600, 'process crash recovery exceeded the startup deadline')
        require(fault['actual_end'] + 300 < measurement_start + result['actual_measurement_seconds'],
                'measurement ended before fault recovery could be observed')
    return faults


def slo_sample(view, healthy):
    require(isinstance(view, dict) and view.get('window_seconds') == 300
            and view.get('queue_target_ms') == 250 and view.get('result_target_ms') == 5000,
            'SLO window or thresholds differ from the agreed targets')
    require(type(view.get('coverage_complete')) is bool, 'SLO coverage is missing')
    rows = view.get('reports')
    require(isinstance(rows, list) and len(rows) <= 128, 'SLO reports are missing or unbounded')
    require(not healthy or (view['coverage_complete'] and rows), 'healthy window lacks complete SLO coverage')
    seen = set()
    for row in rows:
        require(isinstance(row, dict) and isinstance(row.get('driver'), str)
                and row['driver'] and row['driver'] not in seen, 'invalid or duplicate SLO driver')
        seen.add(row['driver'])
        for key in ('samples', 'expected', 'missed', 'overdue', 'pending', 'timeouts', 'queue_met', 'result_met'):
            require(integer(row.get(key)), 'missing SLO counter: ' + key)
        require(row['expected'] == row['samples'] + row['missed'] + row['overdue']
                and row['timeouts'] <= row['samples'], 'SLO denominator omits missed or overdue checks')
        for met, attainment in (('queue_met', 'queue_attainment'), ('result_met', 'result_attainment')):
            require(row[met] <= row['samples'], 'SLO successes exceed completed observations')
            ratio = row[met] / row['expected'] if row['expected'] else None
            require((ratio is None and row.get(attainment) is None) or close(row.get(attainment), ratio),
                    'SLO attainment differs from the exact counters')
            require(not healthy or (row['samples'] >= 1000 and ratio is not None and ratio >= .99),
                    'healthy window breaches the percentile threshold or lacks observations')
        for key in ('scheduling_queue', 'execution', 'scheduled_result'):
            percentiles = row.get(key)
            require(isinstance(percentiles, dict), 'percentile observations are missing')
            values = [percentiles.get(p) for p in ('p50_ms', 'p95_ms', 'p99_ms')]
            require(all(value is None for value in values) or
                    (all(number(value) for value in values) and values == sorted(values)), 'invalid percentile ordering')
            require(not healthy or all(number(value) for value in values), 'healthy window has unavailable percentiles')


def resources(sample, candidate):
    values = {}
    for owner in ('controller', 'target'):
        row = sample.get(owner)
        require(isinstance(row, dict) and number(row.get('cpu_seconds')), 'process resource samples are missing')
        for key in ('rss_bytes', 'threads', 'descriptors'):
            require(integer(row.get(key), 1), 'process resource measurement is missing: ' + key)
        values.update({owner + '.' + key: value for key, value in row.items()})
    require(isinstance(sample.get('queues'), dict) and isinstance(sample.get('pools'), dict), 'queue or pool observations are missing')
    for pipeline in ('pulse', 'intervention', 'code'):
        queue = sample['queues'].get(pipeline)
        pool = sample['pools'].get(pipeline)
        require(isinstance(queue, dict) and isinstance(pool, dict), 'pipeline observations are missing')
        for key in ('queue_depth', 'capacity', 'dropped'):
            require(integer(queue.get(key)), 'queue accounting is missing: ' + key)
            values['queue.' + pipeline + '.' + key] = queue[key]
        for key in ('current_capacity', 'running_workers', 'waiting_tasks'):
            require(integer(pool.get(key)), 'pool accounting is missing: ' + key)
            values['pool.' + pipeline + '.' + key] = pool[key]
    if candidate:
        storage, usage, runtime = (sample.get(key) for key in ('storage', 'storage_usage', 'runtime'))
        require(isinstance(storage, dict) and storage.get('ready') is True and storage.get('mode') == 'raft',
                'durable storage is not ready in Raft mode')
        for key in ('commit_latency_ms', 'snapshot_duration_ms'):
            require(number(storage.get(key)), 'storage timing observation is missing')
            values['storage.' + key] = storage[key]
        require(isinstance(usage, dict) and isinstance(runtime, dict), 'storage or runtime accounting is missing')
        require(usage.get('available') is True, 'storage accounting is unavailable')
        for key in ('bytes', 'history_bytes', 'snapshot_bytes', 'snapshot_files'):
            require(integer(usage.get(key)), 'storage accounting is missing: ' + key)
            values['storage.' + key] = usage[key]
        for key in ('heap_bytes', 'goroutines'):
            require(integer(runtime.get(key), 1), 'runtime accounting is missing: ' + key)
            values['runtime.' + key] = runtime[key]
    return values


def audit(row, count, previous=None, final=False):
    require(isinstance(row, dict) and type(row.get('configured_monitors')) is int
            and row['configured_monitors'] == count, 'target monitor count differs')
    for key in ('received', 'completed', 'actions', 'duplicate_actions', 'early_checks', 'fault_epoch'):
        require(integer(row.get(key)) and (previous is None or row[key] >= previous[key]), 'target counters reset or are missing')
    require(row['duplicate_actions'] == 0, 'target observed a duplicate intervention')
    if final:
        require(row.get('distinct') == count and integer(row.get('minimum_checks'), 1)
                and integer(row.get('maximum_checks'), row['minimum_checks']) and digest(row.get('count_digest')),
                'final distinct-monitor accounting is incomplete')
        counts = row.get('cohort_actions')
        require(isinstance(counts, list) and len(counts) == min(100, count)
                and all(integer(value) for value in counts) and sum(counts) == row['actions'],
                'per-monitor intervention accounting is incomplete')


def read_samples(evidence, name, result, mode, candidate, host):
    path = evidence.record(name + '/samples.jsonl.gz')
    previous = first_measured = last = None
    faults = None
    count = total = healthy_count = 0
    peak = {}
    first_resources = last_resources = None
    with gzip.open(path, 'rb') as stream:
        while line := stream.readline(LINE_LIMIT + 1):
            total += len(line)
            require(len(line) <= LINE_LIMIT and total <= SAMPLE_LIMIT, name + ': sample byte limit exceeded')
            count += 1
            require(count <= 300 + result['measurement_seconds'] + 120, name + ': sample count exceeds observation budget')
            sample = decode(line, name + ': sample ' + str(count))
            require(number(sample.get('at'), result['started_at']) and number(sample.get('elapsed')),
                    name + ': sample clocks are missing')
            elapsed = sample['elapsed']
            if first_measured is None and elapsed >= 300:
                first_measured = sample
                require(close(sample['at'], result.get('measurement_started_at')) and elapsed <= 320,
                        name + ': measurement did not begin after the agreed warmup')
                faults = fault_intervals(result, mode, result['scenario'], elapsed)
            if previous is None:
                require(elapsed < 20, name + ': warmup samples are missing')
            else:
                wall_gap, gap = sample['at'] - previous['at'], elapsed - previous['elapsed']
                crash_gap = any(fault['type'] == 'process_kill' and
                                previous['elapsed'] <= fault['start'] <= elapsed and
                                elapsed <= fault['actual_end'] + 20 for fault in result['declared_faults'])
                require(gap > 0 and wall_gap > 0 and abs(wall_gap - gap) <= 5 and (gap <= 20 or crash_gap),
                        name + ': missing samples, interrupted time, or clock discontinuity')
            audit(sample.get('target_audit'), result['monitors'], previous['target_audit'] if previous else None)
            dashboard = sample.get('dashboard')
            require(isinstance(dashboard, dict) and type(dashboard.get('total')) is int
                    and dashboard['total'] == result['monitors'] and dashboard.get('page_size') == 100
                    and integer(dashboard.get('returned')) and dashboard['returned'] <= 100,
                    name + ': bounded full-fleet navigation was not observed')
            require(number(sample.get('target_audit_latency')) and sample['target_audit_latency'] <= .25,
                    name + ': target audit latency exceeded its saturation boundary')
            values = resources(sample, candidate)
            if previous:
                cores = (sample['target']['cpu_seconds'] - previous['target']['cpu_seconds']) / (sample['at'] - previous['at'])
                require(number(cores) and cores <= host['cpu_count'] * .8 and close(sample.get('target_cpu_cores'), cores),
                        name + ': target CPU saturation or mismatched observations')
            for key, value in values.items():
                require(number(value), name + ': invalid resource value')
                peak[key] = max(value, peak.get(key, 0))
            if first_measured is sample:
                first_resources = values
            last_resources = values
            if candidate:
                in_fault = any(fault['start'] <= elapsed < fault['actual_end'] + 300
                               for fault in (faults or result['declared_faults']))
                require(type(sample.get('declared_fault_window')) is bool
                        and sample['declared_fault_window'] == in_fault, name + ': false fault exclusion')
                healthy = elapsed >= 300 and not in_fault
                slo_sample(sample.get('slo'), healthy)
                healthy_count += int(healthy)
            previous = last = sample
    require(first_measured is not None and last is not None and count == result.get('samples'), name + ': sample file is truncated')
    require(close(last['elapsed'], result.get('elapsed_seconds'))
            and last['elapsed'] >= first_measured['elapsed'] + result['measurement_seconds'] - 20,
            name + ': samples do not cover the full measurement')
    require(close(peak['controller.rss_bytes'], result.get('peak_rss_bytes'))
            and close(peak['controller.descriptors'], result.get('peak_descriptors')), name + ': peak resources disagree')
    expected_healthy = healthy_count if candidate else 0
    require(type(result.get('healthy_slo_windows')) is int and result['healthy_slo_windows'] == expected_healthy,
            name + ': healthy-window summary differs from retained samples')
    require(not candidate or healthy_count > 0, name + ': no healthy SLO observations')
    audit(result.get('final_target_audit'), result['monitors'], last['target_audit'], final=True)
    received = result['final_target_audit']['received'] - first_measured['target_audit']['received']
    rate = received / result['actual_measurement_seconds']
    require(close(rate, result.get('achieved_rate')), name + ': reported throughput differs from target accounting')
    if candidate and result['scenario'] == 'healthy':
        require(rate >= result['monitors'] / 60 * .99, name + ': candidate throughput below 99 percent of required cadence')
    if mode == 'soak':
        require(result['final_target_audit']['actions'] > 0 and last['storage_usage']['snapshot_files'] >= 1
                and last['storage_usage']['history_bytes'] > 0, 'soak lacks snapshot or retained-history activity')
    return {'samples': count, 'healthy_slo_windows': healthy_count, 'achieved_rate': rate,
            'first_resources': first_resources, 'last_resources': last_resources, 'peak_resources': peak}


def check_campaign(evidence, mode, metadata, rows):
    require(evidence.read('builds.json') == metadata, mode + ': prepared inputs differ')
    host = host_identity(evidence.read('preflight.json'))
    campaign = evidence.read('campaign.json')
    expected = trials(mode)
    require(campaign.get('mode') == mode and campaign.get('status') == 'measured_pending_review'
            and campaign.get('baseline_commit') == prepare.BASELINE
            and campaign.get('release_gate_passed') is False and campaign.get('review_required') is True,
            mode + ': campaign is failed, incomplete, smoke-only, or missing its review boundary')
    runs = campaign.get('runs')
    require(type(campaign.get('planned_runs')) is int and campaign['planned_runs'] == len(expected)
            and isinstance(runs, list) and len(runs) == len(expected), mode + ': required run matrix is incomplete')
    summaries = []
    for result, (name, build, count, duration, scenario) in zip(runs, expected):
        require(result == evidence.read(name + '/result.json'), name + ': campaign summary differs from retained run')
        require(result.get('status') == 'measured_pending_review' and result.get('failures') == []
                and result.get('target_saturation_detected') is False, name + ': run did not complete cleanly')
        for key, value in (('monitors', count), ('loaded_monitors', count), ('interval_seconds', 60),
                           ('warmup_seconds', 300), ('measurement_seconds', duration)):
            require(type(result.get(key)) is int and result[key] == value, name + ': wrong workload ' + key)
        require(result.get('scenario') == scenario and result.get('binary_sha256') == rows[build]['sha256'], name + ': wrong build or scenario')
        require(result.get('internal_percentiles') == ('candidate' if build == 'candidate' else 'unavailable in baseline'),
                name + ': baseline percentile availability is misrepresented')
        require(integer(result.get('seed')) and number(result.get('started_at'), 1)
                and number(result.get('measurement_started_at'), result['started_at'] + 300)
                and number(result.get('actual_measurement_seconds'), duration)
                and number(result.get('finished_at'), result['measurement_started_at'] + result['actual_measurement_seconds']),
                name + ': full-duration measurement is missing')
        require(close(result.get('target_rate'), count / 60), name + ': wrong target rate')
        cleanup = result.get('cleanup')
        require(isinstance(cleanup, dict) and all(cleanup.get(key) is True for key in
                ('controller_stopped', 'target_stopped', 'token_removed')), name + ': cleanup is incomplete')
        observed = read_samples(evidence, name, result, mode, build == 'candidate', host)
        if build == 'candidate':
            reconciliation = evidence.read(name + '/reconciliation.json')
            require(reconciliation.get('reconciled') is True, name + ': outcomes do not reconcile')
            for key in ('target_accepted', 'committed_successes', 'unknown_outcomes', 'confirmed_failures',
                        'history_events', 'duplicate_interventions'):
                require(integer(reconciliation.get(key)), name + ': reconciliation counter is missing')
            require(reconciliation['target_accepted'] == reconciliation['committed_successes'] == result['final_target_audit']['actions']
                    and reconciliation['unknown_outcomes'] == reconciliation['duplicate_interventions'] == 0,
                    name + ': action outcomes or duplicate accounting disagree')
            require(reconciliation['history_events'] >= sum(reconciliation[key] for key in
                    ('committed_successes', 'unknown_outcomes', 'confirmed_failures')), name + ': terminal history is incomplete')
            monitors = reconciliation.get('monitors')
            require(isinstance(monitors, list) and len(monitors) == min(100, count), name + ': per-monitor outcomes are missing')
            for index, monitor in enumerate(monitors):
                require(isinstance(monitor, dict) and monitor.get('monitor_id') == f'benchmark-{result["seed"]}-{index}'
                        and digest(monitor.get('terminal_digest')), name + ': per-monitor outcome identity is missing')
                for key in ('target_accepted', 'committed_successes', 'unknown_outcomes', 'confirmed_failures'):
                    require(integer(monitor.get(key)), name + ': per-monitor outcome counter is missing')
                require(monitor['target_accepted'] == monitor['committed_successes'] == result['final_target_audit']['cohort_actions'][index]
                        and monitor['unknown_outcomes'] == 0, name + ': per-monitor outcomes disagree')
            for key in ('target_accepted', 'committed_successes', 'unknown_outcomes', 'confirmed_failures'):
                require(sum(row[key] for row in monitors) == reconciliation[key], name + ': outcome totals disagree')
        summaries.append({'run': name, 'build': build, 'seed': result['seed'], 'started_at': result['started_at'],
                          'finished_at': result['finished_at'], **observed})
    return host, summaries


def verify(builds, comparison, faults, soak=None, commit=None):
    require(isinstance(commit, str) and re.fullmatch('[a-f0-9]{40}', commit), 'a full candidate commit is required')
    prepared = Evidence(builds)
    metadata, rows = prepared_inputs(prepared, commit)
    inputs = {'builds': prepared}
    observed = {}
    host = None
    all_runs = []
    campaigns = [('compare', comparison), ('faults', faults)]
    if soak is not None:
        campaigns.append(('soak', soak))
    for mode, directory in campaigns:
        evidence = Evidence(directory)
        require(evidence.root not in [other.root for other in inputs.values()], 'campaign directories must be distinct')
        inputs[mode] = evidence
        current_host, runs = check_campaign(evidence, mode, metadata, rows)
        require(host is None or host == current_host, 'campaigns ran on different host environments')
        host = current_host
        observed[mode] = runs
        all_runs.extend(runs)
    require(len({run['seed'] for run in all_runs}) == 1, 'campaign workload seeds differ')
    chronological = sorted(all_runs, key=lambda run: run['started_at'])
    require(all(before['finished_at'] <= after['started_at'] for before, after in zip(chronological, chronological[1:])),
            'campaigns overlap on the selected host')
    for evidence in inputs.values():
        evidence.unchanged()
    return {'schema_version': 1, 'status': 'consistent_pending_review', 'release_gate_passed': False,
            'release_identity': metadata['release_identity'],
            'candidate_commit': commit, 'baseline_commit': prepare.BASELINE, 'host': host,
            'scope': 'comparisons_faults_and_endurance' if soak is not None else 'comparisons_and_faults',
            'endurance': {'included': soak is not None, 'required': False, 'claim_qualified': False},
            'builds': rows, 'campaigns': observed,
            'inputs': {name: evidence.files for name, evidence in inputs.items()},
            'review_required': ['resource stability after warmup', 'queue drainage after faults',
                                'host interference and hardware/filesystem record', 'source and compiler provenance',
                                'independent review against the frozen release artifacts']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--builds', required=True, type=Path, help='directory produced by prepare.py')
    parser.add_argument('--comparison', required=True, type=Path)
    parser.add_argument('--faults', required=True, type=Path)
    parser.add_argument('--soak', type=Path, help='optional existing endurance evidence; this reader starts no run')
    parser.add_argument('--candidate-commit', required=True)
    parser.add_argument('--out', required=True, type=Path)
    args = parser.parse_args()
    try:
        # Reserve the report before reading so reruns never replace earlier evidence.
        with args.out.open('x', encoding='utf-8') as stream:
            try:
                result = verify(args.builds, args.comparison, args.faults, args.soak, args.candidate_commit)
            except (ValueError, OSError, EOFError, tarfile.TarError, KeyError, TypeError, OverflowError) as error:
                result = {'schema_version': 1, 'status': 'invalid_evidence', 'release_gate_passed': False,
                          'reason': str(error), 'candidate_commit': args.candidate_commit}
            json.dump(result, stream, indent=2, sort_keys=True, allow_nan=False)
            stream.write('\n')
        print(json.dumps({'status': result['status'], 'release_gate_passed': False}))
        return 0 if result['status'] == 'consistent_pending_review' else 1
    except OSError as error:
        parser.exit(1, type(error).__name__ + ': cannot create a new evidence report\n')


if __name__ == '__main__':
    raise SystemExit(main())
