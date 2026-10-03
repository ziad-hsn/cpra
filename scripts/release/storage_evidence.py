#!/usr/bin/env python3
"""Inspect two-version storage results against exact frozen native archives.

Hashes establish retained-input consistency, not execution authenticity. Native
platform coverage and independent review remain explicit publication gates.
"""
import argparse
from pathlib import Path
import platform
import re

import browser_evidence
from evidence import read_report
import publish
import release


SOURCE = 'scripts/native/upgrade.py'
SUPPORTED_TARGETS = ('linux_amd64', 'linux_arm64')
LOGS = ('certificate.log', 'live-backup.log', 'previous.log', 'previous-backup.log',
        'candidate.log', 'candidate-backup.log', 'older-compatible-restore.log',
        'older-restore.log', 'older-owner.log', 'restore.log', 'fenced-restore.log',
        'reprovision.log', 'restored.log')
EFFECTS = ('/recover', '/notify-held', '/notify-delivered')
MAX_LOG_BYTES = 8 * 1024 ** 2


def require(condition, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent storage-upgrade evidence: ' + detail)


def archive_name(manifest, target):
    suffix = '.zip' if target.startswith('windows_') else '.tar.gz'
    return 'cpra-' + manifest['version'] + '-' + target.replace('_', '-') + suffix


def action_map(actions, states):
    require(isinstance(actions, list) and len(actions) == 3
            and all(isinstance(action, dict) for action in actions), 'three action observations are required')
    ids = [action.get('id') for action in actions]
    require(all(isinstance(identity, str) and re.fullmatch('[a-f0-9]{64}', identity) for identity in ids)
            and len(set(ids)) == 3, 'action identities are missing or duplicated')
    require(all(action.get('executionID') == action['id']
                and isinstance(action.get('executionRevision'), str)
                and re.fullmatch('[a-f0-9]{64}', action['executionRevision']) for action in actions),
            'action execution identity is missing or invalid')
    require(sorted(action.get('state', '') for action in actions) == sorted(states)
            and sorted(action.get('kind', '') for action in actions) == ['code', 'code', 'intervention'],
            'action states or roles differ from the crash fixture')
    return {action['id']: action for action in actions}


def history_ids(history):
    require(isinstance(history, list) and 0 < len(history) <= 500
            and all(isinstance(event, dict) and isinstance(event.get('id'), str)
                    and event['id'] and event.get('monitorID') == 'upgrade-monitor' for event in history),
            'retained monitor history is missing or invalid')
    ids = {event['id'] for event in history}
    require(len(ids) == len(history), 'history contains duplicate events')
    return ids


def effect_counts(counts, failed_checks):
    require(isinstance(counts, dict) and all(type(value) is int and value >= 0 for value in counts.values())
            and all(counts.get(path) == 1 for path in EFFECTS)
            and counts.get('failed_health_checks') == failed_checks
            and counts.get('/health', 0) >= failed_checks,
            'independent target counts show missing work or repeated external actions')


def case_result(case, candidate_format):
    require(isinstance(case, dict) and case.get('case') in ('log', 'snapshot')
            and case.get('status') == 'passed' and case.get('phase') == 'candidate_recovered',
            'a log or snapshot upgrade did not complete')
    snapshot = case['case'] == 'snapshot'
    require(type(case.get('snapshot_count')) is int
            and (case['snapshot_count'] > 0 if snapshot else case['snapshot_count'] == 0)
            and case.get('snapshot_interval') == ('1s' if snapshot else '1h'),
            'snapshot/log recovery boundaries differ')
    require(case.get('backup_formats') == {'previous': None, 'candidate': candidate_format}
            and case.get('older_restore') == {'original_backup_accepted': True, 'candidate_backup_rejected': True}
            and case.get('older_owner_rejected_upgraded_state') is True
            and type(case.get('older_owner_exit_code')) is int and case['older_owner_exit_code'] != 0
            and case.get('restore_rejected_before_reprovision') is True and case.get('old_token_status') == 401,
            'unsupported rollback, stopped backup or restored authorization was not exercised')
    initial = action_map(case.get('interrupted_actions'), ('started', 'started', 'succeeded'))
    observed = action_map(case.get('observed_actions'), ('started', 'started', 'succeeded'))
    require(initial.keys() == observed.keys(), 'actions changed before forced termination')
    effect_counts(case.get('provider_counts'), 1)
    before = case.get('before')
    require(isinstance(before, dict) and isinstance(before.get('node_id'), str) and before['node_id'],
            'original committed node observation is missing')
    monitor = before.get('monitor', {})
    uid = monitor.get('metadata', {}).get('uid')
    incident = monitor.get('status', {}).get('incidentID')
    require(isinstance(uid, str) and uid and isinstance(incident, str) and incident
            and before.get('incident', {}).get('id') == incident
            and before['incident'].get('acknowledgedBy') == 'upgrade/operator',
            'original monitor, incident or acknowledgement is missing')
    original_history = history_ids(before.get('history'))
    original_events = {event['id']: event for event in before['history']}
    for phase, failures in (('upgrade', 9), ('restore', 17)):
        result = case.get(phase)
        require(isinstance(result, dict) and result.get('node_id') == before['node_id']
                and result.get('monitor', {}).get('metadata', {}).get('uid') == uid
                and result['monitor'].get('metadata', {}).get('id') == 'upgrade-monitor'
                and result['monitor'].get('status', {}).get('incidentID') == incident
                and result.get('incident', {}).get('id') == incident
                and result['incident'].get('acknowledgedBy') == 'upgrade/operator',
                phase + ': monitor identity, incident or ownership changed')
        final = action_map(result.get('actions'), ('succeeded', 'unknown', 'unknown'))
        require(final.keys() == initial.keys(), phase + ': action identity changed')
        for identity, action in final.items():
            expected = 'succeeded' if initial[identity]['state'] == 'succeeded' else 'unknown'
            require(action['state'] == expected and (expected != 'unknown' or action.get('held') is True)
                    and action.get('kind') == initial[identity]['kind']
                    and action.get('executionID') == initial[identity]['executionID']
                    and action.get('executionRevision') == initial[identity]['executionRevision']
                    and action.get('monitorID') == 'upgrade-monitor'
                    and action.get('incarnationUID') == uid and action.get('incidentID') == incident,
                    phase + ': confirmed delivery was repeated or interrupted work was redirected/resolved')
        require(original_history <= history_ids(result.get('history')), phase + ': original history was lost')
        retained_events = {event['id']: event for event in result['history']}
        require(all(retained_events[identity] == event for identity, event in original_events.items()),
                phase + ': immutable historical events changed')
        effect_counts(result.get('provider_counts'), failures)
    return {'case': case['case'], 'snapshots': case['snapshot_count'], 'unknown_actions': 2,
            'confirmed_deliveries': 1, 'failed_checks': 17,
            'provider_calls': {path: 1 for path in EFFECTS}}


def validate_pair(manifest, previous):
    identity, previous_identity = release.evidence_identity(manifest), release.evidence_identity(previous)
    require(previous['commit'] != manifest['commit'] and previous['version'] != manifest['version'],
            'upgrade requires two distinct frozen source versions')
    old_format, new_format = previous.get('storage_format_version'), manifest.get('storage_format_version')
    require(type(old_format) is int and type(new_format) is int and 1 < old_format < new_format
            and new_format == release.RECIPE['storage_format_version'],
            'this fixture requires a newer recorded storage format, not a same-format upgrade')
    require('externaljobs' not in manifest['recipe'].get('build_tags', [])
            and 'externaljobs' not in previous['recipe'].get('build_tags', []),
            'built-in upgrade fixture cannot qualify optional worker state')
    return identity, previous_identity


def check_executables(manifest, previous_release, distribution, previous_distribution, candidate_stage, previous_stage):
    """Reject mismatched sources/artifacts before starting a test owner."""
    release.check_manifest(manifest)
    previous = read_report(previous_release)
    identity, previous_identity = validate_pair(manifest, previous)
    architecture = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine())
    require(platform.system() == 'Linux' and architecture, 'requires a native Linux runner')
    target = 'linux_' + architecture
    executables = {}
    for label, metadata, artifacts, stage in [('candidate', manifest, distribution, candidate_stage),
                                             ('previous', previous, previous_distribution, previous_stage)]:
        stage = Path(stage).absolute()
        require(stage.resolve(strict=True) == stage, 'stage contains a link or alias')
        artifacts = Path(artifacts).absolute()
        require(artifacts.resolve(strict=True) == artifacts, 'distribution contains a link or alias')
        require(read_report(stage / 'RELEASE.json') == metadata, 'stage metadata differs: ' + label)
        expected = publish.archive_programs(artifacts, metadata, target)
        for program, role in [('cpra', 'binary'), ('cpractl', 'cli')]:
            path = stage / program
            require(path.resolve(strict=True) == path and path.stat().st_mode & 0o111,
                    'staged executable is linked or not executable')
            value, _ = browser_evidence.hash_file(path, publish.ARCHIVE_PROGRAM_LIMIT)
            require(value == expected[program], 'stage differs from its frozen native archive: ' + label + '/' + program)
            executables[label + '_' + role] = value
    require(executables['candidate_binary'] != executables['previous_binary'], 'requires two distinct binaries')
    return {'schema_version': 1, 'scope': 'native storage-upgrade input validation', 'target': target,
            'release_identity': identity, 'previous_release_identity': previous_identity,
            'release_gate_passed': False, 'executables': executables}


def validate_previous_run(previous, run, jobs, repository, run_id):
    """Bind privately downloaded artifacts to a completed dispatch and build."""
    release.evidence_identity(previous)
    require(isinstance(repository, str) and re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository)
            and isinstance(run_id, str) and re.fullmatch(r'[1-9][0-9]{0,19}', run_id),
            'previous repository or run identifier is invalid')
    require(isinstance(run, dict) and type(run.get('id')) is int and run['id'] == int(run_id)
            and run.get('event') == 'workflow_dispatch' and run.get('status') == 'completed'
            and run.get('path') == '.github/workflows/release.yml'
            and run.get('head_sha') == previous['commit'],
            'previous run is unfinished, untrusted or from another source/workflow')
    for field in ('repository', 'head_repository'):
        value = run.get(field)
        require(isinstance(value, dict) and value.get('full_name') == repository and value.get('private') is True,
                'previous run is not from this private repository')
    require(isinstance(jobs, dict) and type(jobs.get('total_count')) is int
            and isinstance(jobs.get('jobs'), list) and 0 < jobs['total_count'] <= 100
            and len(jobs['jobs']) == jobs['total_count']
            and all(isinstance(job, dict) and type(job.get('run_id')) is int
                    and job['run_id'] == int(run_id) for job in jobs['jobs']),
            'previous job inventory is incomplete or from another run')
    builds = [job for job in jobs['jobs'] if job.get('name') == 'build']
    require(len(builds) == 1 and builds[0].get('status') == 'completed'
            and builds[0].get('conclusion') == 'success' and builds[0].get('head_sha') == previous['commit'],
            'previous frozen artifact build did not complete successfully')


def verify_upgrades(manifest, previous_release, distribution, previous_distribution, campaigns):
    inputs, limits = {}, {}

    def remember(path, limit=browser_evidence.MAX_METADATA_BYTES, expected=None):
        path = Path(path).absolute()
        require(path.resolve(strict=True) == path, 'retained input contains a link or alias')
        value, count = browser_evidence.hash_file(path, limit)
        require(expected is None or value == expected, 'retained input changed: ' + path.name)
        require(str(path) not in inputs or inputs[str(path)] == value, 'input changed during inspection')
        inputs[str(path)], limits[str(path)] = value, limit
        return value, count

    remember(previous_release)
    previous = read_report(previous_release)
    identity, previous_identity = validate_pair(manifest, previous)
    new_format = manifest['storage_format_version']
    require(isinstance(campaigns, (list, tuple)) and 0 < len(campaigns) <= len(release.RECIPE['targets'])
            and len({str(Path(path).absolute()) for path in campaigns}) == len(campaigns),
            'upgrade campaign paths are empty, excessive or duplicated')
    source_hash = manifest['source_files'].get(SOURCE, {}).get('sha256')
    require(browser_evidence.digest(source_hash), 'frozen source omits the upgrade fixture')
    seen, results = set(), []
    for path in map(lambda value: Path(value).absolute(), campaigns):
        remember(path)
        report = read_report(path)
        require(type(report.get('schema_version')) is int and report['schema_version'] == 2
                and report.get('status') == 'passed' and report.get('release_gate_passed') is False
                and report.get('endurance_performed') is False
                and report.get('scope') == 'native two-binary storage upgrade fixture'
                and report.get('source_files') == {SOURCE: source_hash} and report.get('source_changed') == [],
                'upgrade report is incomplete, diagnostic-only, stale or from another source')
        arch = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(report.get('architecture'))
        require(isinstance(report.get('platform'), str) and report['platform'].startswith('Linux-') and arch,
                'fixture is not a supported native Linux execution')
        target = 'linux_' + arch
        require(target in SUPPORTED_TARGETS and target not in seen, 'platform is unsupported or duplicated')
        seen.add(target)
        expected = {}
        for label, metadata, directory in (('previous', previous, previous_distribution), ('candidate', manifest, distribution)):
            archive = Path(directory).absolute() / archive_name(metadata, target)
            remember(archive, publish.ARCHIVE_LIMIT)
            programs = publish.archive_programs(Path(directory), metadata, target)
            for program, role in (('cpra', 'binary'), ('cpractl', 'cli')):
                name = label + '_' + role
                record = report.get('executables', {}).get(name, {})
                version = record.get('version')
                require(record.get('sha256') == programs[program] and isinstance(version, str)
                        and re.search(r'(?<![A-Za-z0-9.+-])' + re.escape(metadata['version']) + r'(?![A-Za-z0-9.+-])', version)
                        and metadata['commit'] in version and 'dirty' not in version,
                        'tested executable differs from its frozen native archive: ' + name)
                expected[name] = programs[program]
        require(expected['previous_binary'] != expected['candidate_binary'], 'the same binary cannot prove an upgrade')
        cases = report.get('cases')
        require(isinstance(cases, list) and len(cases) == 2
                and all(isinstance(case, dict) for case in cases)
                and {case.get('case') for case in cases} == {'log', 'snapshot'},
                'both log-only and snapshot-plus-log campaigns are required')
        logs = report.get('logs')
        require(isinstance(logs, dict) and set(logs) == {case + '/' + name for case in ('log', 'snapshot') for name in LOGS},
                'original command and owner logs are incomplete')
        for name, record in logs.items():
            require(isinstance(record, dict) and set(record) == {'sha256', 'bytes'}
                    and browser_evidence.digest(record['sha256']) and type(record['bytes']) is int,
                    'invalid retained process-log inventory')
            log = browser_evidence.retained_path(path.with_name(path.name + '.inputs'), name)
            _, count = remember(log, MAX_LOG_BYTES, record['sha256'])
            require(count == record['bytes'], 'retained log length differs: ' + name)
        results.append({'target': target, 'executables': expected,
                        'cases': [case_result(case, new_format) for case in cases]})
    for name, value in list(inputs.items()):
        remember(Path(name), limits[name], value)
    remaining = sorted(set(release.RECIPE['targets']) - seen)
    return {'schema_version': 1, 'status': 'incomplete' if remaining else 'consistent_pending_review',
            'scope': 'frozen native storage-format upgrade evidence', 'release_identity': identity,
            'previous_release_identity': previous_identity, 'release_gate_passed': False,
            'endurance_required': False, 'targets': results, 'remaining_targets': remaining,
            'same_format_upgrade_qualified': False, 'input_sha256': inputs,
            'unverified': ['Authenticate original execution records and complete independent review.',
                           'Run remaining native platforms; cross-compilation is not native upgrade evidence.',
                           'Same-format upgrades need a compatible-rollback scenario.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('release', 'previous-release', 'distribution', 'previous-distribution', 'out'):
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--campaign', type=Path, action='append')
    parser.add_argument('--check-executables', action='store_true')
    parser.add_argument('--candidate-stage', type=Path)
    parser.add_argument('--previous-stage', type=Path)
    parser.add_argument('--previous-run', type=Path)
    parser.add_argument('--previous-jobs', type=Path)
    parser.add_argument('--previous-run-id')
    parser.add_argument('--repository')
    args = parser.parse_args()
    if args.check_executables:
        if args.campaign or args.candidate_stage is None or args.previous_stage is None:
            parser.error('--check-executables requires both stages and excludes --campaign')
        recorded_run = (args.previous_run, args.previous_jobs, args.previous_run_id, args.repository)
        if any(value is not None for value in recorded_run) and not all(value is not None for value in recorded_run):
            parser.error('previous-run verification requires the run, jobs, ID and repository together')
    elif not args.campaign or any(value is not None for value in (
            args.candidate_stage, args.previous_stage, args.previous_run,
            args.previous_jobs, args.previous_run_id, args.repository)):
        parser.error('inspection requires --campaign; stage arguments belong to --check-executables')
    manifest = read_report(args.release)
    if args.check_executables:
        if args.previous_run is not None:
            validate_previous_run(read_report(args.previous_release), read_report(args.previous_run),
                                  read_report(args.previous_jobs), args.repository, args.previous_run_id)
        result = check_executables(manifest, args.previous_release, args.distribution,
                                   args.previous_distribution, args.candidate_stage, args.previous_stage)
    else:
        result = verify_upgrades(manifest, args.previous_release, args.distribution,
                                 args.previous_distribution, args.campaign)
    require(not args.out.exists(), 'output must be new')
    args.out.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with args.out.open('xb') as output:
        output.write(release.encoded(result))


if __name__ == '__main__':
    main()
