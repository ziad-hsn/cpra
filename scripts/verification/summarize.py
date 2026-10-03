#!/usr/bin/env python3
"""Validate retained provider evidence and assemble the complete driver matrix.

Supply positive suite reports only. Negative reports belong in their adjacent
suite summaries. Local/mock/sandbox coverage never becomes live-account proof.
"""
import argparse
import hashlib
import json
import math
import pathlib
import re

INVENTORY = tuple((kind, driver) for kind, names in (
    ('pulse', 'http tcp icmp dns udp grpc docker tls redis postgres mysql mongo rabbitmq kafka'),
    ('intervention', 'docker webhook kubernetes aws systemd a2a'),
    ('code', 'log slack pagerduty email webhook telegram discord opsgenie mattermost victorops pushover datadog teams twilio a2a'),
) for driver in names.split())
EVIDENCE_RANK = {'mock_contract': 1, 'local_integration': 2, 'provider_sandbox': 3, 'live_account': 4}
MAX_JSON_BYTES = 16 * 1024 ** 2


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(64 * 1024), b''):
            value.update(block)
    return value.hexdigest()


def read_json(path):
    """Read bounded, unambiguous retained evidence without following links."""
    if path.is_symlink() or not path.is_file():
        raise ValueError('provider evidence must be a regular file')
    with path.open('rb') as stream:
        data = stream.read(MAX_JSON_BYTES + 1)
    if len(data) > MAX_JSON_BYTES:
        raise ValueError('provider evidence exceeds 16 MiB')

    def fields(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError('duplicate provider evidence field')
            value[key] = item
        return value

    def invalid(_):
        raise ValueError('non-finite provider evidence number')

    def number(raw):
        value = float(raw)
        if not math.isfinite(value):
            invalid(raw)
        return value

    value = json.loads(data, object_pairs_hook=fields, parse_constant=invalid, parse_float=number)
    if not isinstance(value, dict):
        raise ValueError('provider evidence must be an object')
    return value


def retain_binary_hash(metadata, details):
    if 'binary_sha256' not in details:
        return
    fingerprint = details['binary_sha256']
    if not isinstance(fingerprint, str) or not re.fullmatch('[0-9a-f]{64}', fingerprint):
        raise ValueError('invalid binary hash in adjacent evidence')
    if 'binary_sha256' in metadata and metadata['binary_sha256'] != fingerprint:
        raise ValueError('adjacent evidence identifies conflicting binaries')
    metadata['binary_sha256'] = fingerprint


def read_report(path, *, allow_empty=False):
    path = pathlib.Path(path).absolute()
    report = read_json(path)
    records = report['records']
    keys = [(row['kind'], row['driver']) for row in records]
    if len(keys) != len(INVENTORY) or set(keys) != set(INVENTORY):
        raise ValueError('report must contain each of the 35 known driver types exactly once')
    metadata = {'report': str(path), 'report_sha256': digest(path), 'run_id': report['run_id'],
                'version': report['version'], 'go_version': report['go_version']}
    retain_binary_hash(metadata, report)
    suite = path.with_suffix('.suite.json')
    if suite.exists():
        details = read_json(suite)
        if (('passed' in details and details['passed'] is not True)
                or ('status' in details and details['status'] != 'pass')
                or ('passed' not in details and 'status' not in details)):
            raise ValueError('adjacent suite has not passed')
        if any(row.get('passed') is not True for row in details.get('scenarios', [])):
            raise ValueError('adjacent suite contains a failing scenario')
        metadata.update(suite=str(suite), suite_sha256=digest(suite))
        retain_binary_hash(metadata, details)
    fixture = pathlib.Path(str(path) + '.fixture.json')
    environment = pathlib.Path(str(path) + '.environment.json')
    environment_hash = None
    if environment.exists():
        details = read_json(environment)
        if details.get('cleanup_confirmed') is not True or details.get('preflight', {}).get('ready') is not True:
            raise ValueError('local fixture environment has no successful admission and cleanup')
        environment_hash = digest(environment)
        metadata.update(fixture_environment=str(environment), fixture_environment_sha256=environment_hash)
        retain_binary_hash(metadata, details)
    if fixture.exists():
        details = read_json(fixture)
        if details.get('status') != 'pass' or details.get('cleanup_failures'):
            raise ValueError('adjacent fixture did not finish successfully and clean up')
        if 'report_sha256' in details and details['report_sha256'] != metadata['report_sha256']:
            raise ValueError('adjacent fixture fingerprints a different report')
        if 'environment_sha256' in details and details['environment_sha256'] != environment_hash:
            raise ValueError('adjacent fixture fingerprints a different environment')
        metadata.update(fixture=str(fixture), fixture_sha256=digest(fixture))
        retain_binary_hash(metadata, details)
    verified = []
    for row in records:
        if row['status'] == 'not_configured':
            if any(row.get(key) is not None and row.get(key) is not False
                   for key in ('configured', 'operation_invoked', 'accepted', 'observed')):
                raise ValueError('enabled but unconfigured scenario is not a positive-suite report')
            continue
        if row['status'] != 'pass':
            raise ValueError('report contains a failed scenario; supply positive reports only')
        category = row.get('evidence_type')
        boundary = row.get('observation_boundary')
        if category not in EVIDENCE_RANK or boundary not in ('effect', 'api_acceptance'):
            raise ValueError('unknown evidence classification or observation boundary')
        if row.get('configured') is not True or row.get('operation_invoked') is not True or row.get('accepted') is not True:
            raise ValueError('passing scenario did not invoke and receive acceptance from its driver')
        if boundary == 'effect':
            if row.get('observed') is not True:
                raise ValueError('passing effect scenario has no independent observation')
        elif ((row['kind'], row['driver'], category) != ('code', 'twilio', 'provider_sandbox')
              or row.get('observed') is not False):
            raise ValueError('API acceptance is restricted to the explicit Twilio test-credential boundary')
        reference = row.get('evidence_ref', '')
        if not isinstance(reference, str) or not reference or reference in ('.', '..') \
                or pathlib.Path(reference).name != reference or '\\' in reference or ':' in reference:
            raise ValueError('evidence reference must be a retained filename')
        directory = pathlib.Path(str(path) + '.evidence')
        if directory.is_symlink() or not directory.is_dir():
            raise ValueError('provider evidence directory must not be a link')
        evidence_file = directory / reference
        evidence = read_json(evidence_file)
        evidence_hash = digest(evidence_file)
        if evidence_hash != row.get('evidence_sha256'):
            raise ValueError('retained evidence hash does not match the report')
        expected = {key: row[key] for key in ('kind', 'driver', 'evidence_type', 'observation_boundary', 'accepted', 'observed')}
        expected['run_id'] = report['run_id']
        if row['driver'] == 'a2a':
            if (not isinstance(row.get('monitor_id'), str) or not 0 < len(row['monitor_id'].encode()) <= 256
                    or not isinstance(row.get('action_id'), str) or not re.fullmatch('[0-9a-f]{64}', row['action_id'])
                    or row.get('action_state') != 'succeeded'):
                raise ValueError('passing A2A evidence requires its committed original action')
            expected.update({key: row[key] for key in ('monitor_id', 'action_id', 'action_state')})
        if any(evidence.get(key) != value for key, value in expected.items()):
            raise ValueError('retained evidence does not match the run, driver, or claimed boundary')
        if boundary == 'effect' and (not isinstance(evidence.get('before'), dict)
                                     or not isinstance(evidence.get('after'), dict)
                                     or evidence['after'].get('observed') is not True):
            raise ValueError('retained evidence does not include a baseline and observed effect')
        duration = row.get('duration_ms')
        if type(duration) not in (int, float) or duration < 0:
            raise ValueError('provider evidence has an invalid duration')
        try:
            finite = math.isfinite(duration)
        except OverflowError:
            finite = False
        if not finite:
            raise ValueError('provider evidence has an invalid duration')
        verified.append({'kind': row['kind'], 'driver': row['driver'], 'evidence_type': category,
                         'observation_boundary': boundary, 'report': str(path),
                         'run_id': report['run_id'], 'evidence_file': str(evidence_file),
                         'evidence_sha256': evidence_hash, 'duration_ms': row['duration_ms']})
    if not verified and not allow_empty:
        raise ValueError('positive report contains no passing evidence')
    return metadata, verified


def summarize(paths, *, allow_empty=False):
    if not paths:
        raise ValueError('at least one positive report is required')
    sources, candidates = [], {key: [] for key in INVENTORY}
    seen = set()
    for path in paths:
        resolved = pathlib.Path(path).resolve()
        if resolved in seen:
            raise ValueError('duplicate input report')
        seen.add(resolved)
        source, records = read_report(pathlib.Path(path).absolute(), allow_empty=allow_empty)
        sources.append(source)
        for row in records:
            candidates[row['kind'], row['driver']].append(row)
    # Build/Go differences must be visible and require separate matrices; a
    # successful old binary must not fill a gap in a newer candidate's results.
    if len({(source['version'], source['go_version']) for source in sources}) != 1:
        raise ValueError('reports identify different source versions or Go toolchains')
    hashes = {source['binary_sha256'] for source in sources if 'binary_sha256' in source}
    if len(hashes) > 1:
        raise ValueError('reports with binary fingerprints identify different binaries')
    records, counts = [], {key: 0 for key in EVIDENCE_RANK}
    for key, evidence in candidates.items():
        if not evidence:
            records.append({'kind': key[0], 'driver': key[1], 'status': 'not_verified', 'evidence': []})
            continue
        evidence.sort(key=lambda row: (-EVIDENCE_RANK[row['evidence_type']], row['report']))
        selected = evidence[0]
        counts[selected['evidence_type']] += 1
        records.append({'kind': key[0], 'driver': key[1], 'status': 'pass',
                        'strongest_evidence_type': selected['evidence_type'],
                        'observation_boundary': selected['observation_boundary'], 'evidence': evidence})
    covered = sum(counts.values())
    return {'status': 'complete_coverage' if covered == len(INVENTORY) else 'partial_coverage',
            'known_driver_count': len(INVENTORY), 'covered_driver_count': covered,
            'all_drivers_covered': covered == len(INVENTORY),
            'all_providers_verified': counts['live_account'] == len(INVENTORY),
            'formal_vendor_certification': False, 'strongest_evidence_counts': counts,
            'binary_sha256': next(iter(hashes), None),
            'reports_without_binary_fingerprint': [source['report'] for source in sources if 'binary_sha256' not in source],
            'sources': sources, 'records': records,
            'limitations': ['Local integrations, mocks and provider sandboxes do not establish live-account certification.',
                            'Source/version equality alone cannot establish binary identity for reports without a fingerprint.',
                            'The gRPC driver checks TCP reachability; no health RPC is claimed.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--reports', nargs='+', required=True)
    parser.add_argument('--out', required=True)
    parser.add_argument('--require-all', action='store_true', help='Exit 2 unless all 35 drivers have evidence of a supported category')
    args = parser.parse_args()
    output = pathlib.Path(args.out).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    try:
        result = summarize(args.reports)
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
        result = {'status': 'invalid_evidence', 'all_drivers_covered': False, 'all_providers_verified': False,
                  'reason': str(error)}
        code = 1
    else:
        code = 2 if args.require_all and not result['all_drivers_covered'] else 0
    temporary = output.with_suffix(output.suffix + '.tmp')
    temporary.write_text(json.dumps(result, indent=2) + '\n')
    temporary.replace(output)
    print(json.dumps({key: result[key] for key in ('status', 'all_drivers_covered', 'all_providers_verified')}))
    return code


if __name__ == '__main__':
    raise SystemExit(main())
