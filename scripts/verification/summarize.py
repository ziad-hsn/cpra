#!/usr/bin/env python3
"""Validate retained provider evidence and assemble the complete driver matrix.

Supply positive suite reports only. Negative reports belong in their adjacent
suite summaries. Local/mock/sandbox coverage never becomes live-account proof.
"""
import argparse
import hashlib
import json
import pathlib
import re

INVENTORY = tuple((kind, driver) for kind, names in (
    ('pulse', 'http tcp icmp dns udp grpc docker tls redis postgres mysql mongo rabbitmq kafka'),
    ('intervention', 'docker webhook kubernetes aws systemd'),
    ('code', 'log slack pagerduty email webhook telegram discord opsgenie mattermost victorops pushover datadog teams twilio'),
) for driver in names.split())
EVIDENCE_RANK = {'mock_contract': 1, 'local_integration': 2, 'provider_sandbox': 3, 'live_account': 4}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def retain_binary_hash(metadata, details):
    if 'binary_sha256' not in details:
        return
    fingerprint = details['binary_sha256']
    if not isinstance(fingerprint, str) or not re.fullmatch('[0-9a-f]{64}', fingerprint):
        raise ValueError('invalid binary hash in adjacent evidence')
    if 'binary_sha256' in metadata and metadata['binary_sha256'] != fingerprint:
        raise ValueError('adjacent evidence identifies conflicting binaries')
    metadata['binary_sha256'] = fingerprint


def read_report(path):
    path = pathlib.Path(path).resolve(strict=True)
    report = json.loads(path.read_text())
    records = report['records']
    keys = [(row['kind'], row['driver']) for row in records]
    if len(keys) != len(INVENTORY) or set(keys) != set(INVENTORY):
        raise ValueError('report must contain each of the 33 known driver types exactly once')
    metadata = {'report': str(path), 'report_sha256': digest(path), 'run_id': report['run_id'],
                'version': report['version'], 'go_version': report['go_version']}
    suite = path.with_suffix('.suite.json')
    if suite.exists():
        details = json.loads(suite.read_text())
        if (('passed' in details and details['passed'] is not True)
                or ('status' in details and details['status'] != 'pass')
                or ('passed' not in details and 'status' not in details)):
            raise ValueError('adjacent suite has not passed')
        if any(row.get('passed') is not True for row in details.get('scenarios', [])):
            raise ValueError('adjacent suite contains a failing scenario')
        metadata.update(suite=str(suite), suite_sha256=digest(suite))
        retain_binary_hash(metadata, details)
    fixture = pathlib.Path(str(path) + '.fixture.json')
    if fixture.exists():
        details = json.loads(fixture.read_text())
        if details.get('status') != 'pass' or details.get('cleanup_failures'):
            raise ValueError('adjacent fixture did not finish successfully and clean up')
        if 'report_sha256' in details and details['report_sha256'] != metadata['report_sha256']:
            raise ValueError('adjacent fixture fingerprints a different report')
        metadata.update(fixture=str(fixture), fixture_sha256=digest(fixture))
        retain_binary_hash(metadata, details)
    verified = []
    for row in records:
        if row['status'] == 'not_configured':
            if row.get('configured'):
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
        if not reference or pathlib.Path(reference).name != reference:
            raise ValueError('evidence reference must be a retained filename')
        directory = pathlib.Path(str(path) + '.evidence').resolve(strict=True)
        evidence_file = (directory / reference).resolve(strict=True)
        if not evidence_file.is_relative_to(directory):
            raise ValueError('evidence reference escapes the report evidence directory')
        evidence_hash = digest(evidence_file)
        if evidence_hash != row.get('evidence_sha256'):
            raise ValueError('retained evidence hash does not match the report')
        evidence = json.loads(evidence_file.read_text())
        if not isinstance(evidence, dict):
            raise ValueError('retained evidence must be an object')
        expected = {key: row[key] for key in ('kind', 'driver', 'evidence_type', 'observation_boundary', 'accepted', 'observed')}
        expected['run_id'] = report['run_id']
        if any(evidence.get(key) != value for key, value in expected.items()):
            raise ValueError('retained evidence does not match the run, driver, or claimed boundary')
        if boundary == 'effect' and (not isinstance(evidence.get('before'), dict)
                                     or not isinstance(evidence.get('after'), dict)
                                     or evidence['after'].get('observed') is not True):
            raise ValueError('retained evidence does not include a baseline and observed effect')
        verified.append({'kind': row['kind'], 'driver': row['driver'], 'evidence_type': category,
                         'observation_boundary': boundary, 'report': str(path),
                         'run_id': report['run_id'], 'evidence_file': str(evidence_file),
                         'evidence_sha256': evidence_hash, 'duration_ms': row['duration_ms']})
    if not verified:
        raise ValueError('positive report contains no passing evidence')
    return metadata, verified


def summarize(paths):
    if not paths:
        raise ValueError('at least one positive report is required')
    sources, candidates = [], {key: [] for key in INVENTORY}
    seen = set()
    for path in paths:
        resolved = pathlib.Path(path).resolve()
        if resolved in seen:
            raise ValueError('duplicate input report')
        seen.add(resolved)
        source, records = read_report(resolved)
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
    parser.add_argument('--require-all', action='store_true', help='Exit 2 unless all 33 drivers have evidence of a supported category')
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
