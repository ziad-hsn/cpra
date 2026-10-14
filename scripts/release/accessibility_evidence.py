#!/usr/bin/env python3
"""Inspect frozen browser scans and retained manual accessibility reviews.

Scans cannot establish complete WCAG conformance. This reader binds the scanner,
its findings and human review records to the candidate; independent authentication
of those records and publication authorization remain separate.
"""
import argparse
import base64
from datetime import datetime
import hashlib
import io
import math
import os
from pathlib import Path
import re
import tarfile

import browser_evidence as browser
from evidence import decode_json, read_report
from release import encoded, evidence_identity

SCANNER_VERSION = '4.13.0'
MAX_SCANNER_BYTES = 8 * 1024 ** 2
MAX_SCAN_BYTES = 1024 ** 2
MAX_SCANS = 64
CHECKPOINTS = {
    'verify_browser': {'sign-in', 'secret-editor', 'monitor-conflict', 'delete-confirmation', 'reader-monitor'},
    'verify_actions_browser': {'guarded-recovery', 'unknown-review-inconclusive', 'unknown-review-accepted',
                               'reader-operations', 'reader-monitor-state', 'reader-incidents', 'reader-metrics',
                               'mobile-reader-incidents', 'mobile-reader-incidents-scrolled', 'mobile-reader-metrics'},
    'verify_controls_browser': {'control-acknowledge', 'control-dismiss', 'control-reopen-notifications',
                                'control-snooze', 'control-disable', 'control-end-snooze', 'control-enable'},
}
MANUAL_CASES = {
    'keyboard-navigation', 'form-labels-instructions', 'validation-announcements',
    'dialog-focus-and-dismissal', 'pending-write-focus', 'conflict-and-unknown-announcements',
    'reader-navigation', 'monitor-controls', 'collection-import-errors',
    'operation-progress-pagination', 'contrast-all-themes', 'zoom-and-reflow',
    'focus-visible-and-not-obscured', 'screen-reader-announcements',
}
TOOL_FILES = {'lock': 'browser-package-lock.json', 'archive': 'axe-core.tgz',
              'package': 'axe-package.json', 'script': 'axe.min.js'}
CONTRAST_REASONS = {
    'unreported', 'bgImage', 'bgGradient', 'imgNode', 'bgOverlap', 'complexTextShadows',
    'fgAlpha', 'elmPartiallyObscured', 'elmPartiallyObscuring', 'outsideViewport', 'equalRatio',
    'shortTextContent', 'nonBmp', 'pseudoContent', 'colorParse', 'fgOnShadowColor', 'shadowOnBgColor', 'hidden',
}


def require(condition, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent accessibility evidence: ' + detail)


def scanner_lock(data):
    lock = decode_json(data, 'browser dependency lock')
    record = lock.get('packages', {}).get('node_modules/axe-core', {})
    url = 'https://registry.npmjs.org/axe-core/-/axe-core-' + SCANNER_VERSION + '.tgz'
    require(record.get('version') == SCANNER_VERSION and record.get('resolved') == url,
            'scanner version or distribution origin differs from its pin')
    integrity = record.get('integrity')
    require(isinstance(integrity, str) and integrity.startswith('sha512-'), 'scanner integrity pin is missing')
    try:
        value = base64.b64decode(integrity[7:], validate=True)
    except ValueError as error:
        raise ValueError('Scanner integrity pin is malformed') from error
    require(len(value) == 64, 'scanner integrity pin is malformed')
    return record


def verify_scanner(lock, archive, package, script):
    record = scanner_lock(lock)
    require(len(archive) <= MAX_SCANNER_BYTES
            and 'sha512-' + base64.b64encode(hashlib.sha512(archive).digest()).decode() == record['integrity'],
            'original scanner archive differs from the locked distribution')
    with tarfile.open(fileobj=io.BytesIO(archive), mode='r:gz') as source:
        required = {'package/package.json': package, 'package/axe.min.js': script}
        seen, count, size = set(), 0, 0
        for member in source:
            count += 1
            size += member.size
            require(count <= 2000 and size <= 128 * 1024 ** 2, 'scanner archive exceeds its inspection limits')
            if member.name not in required:
                continue
            require(member.name not in seen and member.isfile() and 0 < member.size <= MAX_SCANNER_BYTES,
                    'scanner distribution member is duplicated or invalid')
            seen.add(member.name)
            with source.extractfile(member) as stream:
                require(stream.read(MAX_SCANNER_BYTES + 1) == required[member.name],
                        'executed scanner bytes differ from the locked archive')
        require(seen == required.keys(), 'scanner archive omits executable inputs')
    require(decode_json(package, 'scanner package').get('version') == SCANNER_VERSION,
            'executed scanner package version differs')


def contrast_check(value):
    require(isinstance(value, dict) and set(value) == {
        'group', 'reason', 'foreground', 'background', 'shadow', 'ratio', 'expectedRatio',
        'fontSizePt', 'fontSizePx', 'fontWeight'}, 'contrast check contains unredacted or missing fields')
    require(value['group'] in ('any', 'all', 'none') and isinstance(value['reason'], str)
            and value['reason'] in CONTRAST_REASONS, 'contrast reason or check group is not allowlisted')
    for name in ('foreground', 'background', 'shadow'):
        require(value[name] is None or isinstance(value[name], str)
                and re.fullmatch('#[a-f0-9]{6}', value[name]), 'contrast color contains invalid or private data')
    for name, minimum, maximum in (('ratio', 1, 21), ('expectedRatio', 1, 21),
                                   ('fontSizePt', 0, 4096), ('fontSizePx', 0, 4096)):
        item = value[name]
        require(item is None or type(item) in (int, float) and math.isfinite(item)
                and minimum <= item <= maximum, 'contrast measurement is invalid or out of bounds')
    require(value['fontWeight'] in (None, 'normal', 'bold'), 'contrast font weight contains private data')
    require(value['ratio'] is None or value['foreground'] is not None and value['background'] is not None,
            'contrast ratio has no measured foreground or background')


def scan_report(value):
    require(isinstance(value, dict) and set(value) == {
        'checkpoint', 'scanner', 'violations', 'needsReview', 'contrastPasses', 'passedRules', 'automatedOnly'},
        'scan contains missing fields or unredacted payloads')
    require(value['scanner'] == SCANNER_VERSION and value['automatedOnly'] is True,
            'scan identity or measurement boundary differs')
    require(isinstance(value['passedRules'], list) and value['passedRules']
            and all(isinstance(rule, str) and re.fullmatch('[a-z0-9-]{1,100}', rule)
                    for rule in value['passedRules']), 'scan omitted passing rule coverage')
    for name in ('violations', 'needsReview', 'contrastPasses'):
        require(isinstance(value[name], list) and len(value[name]) <= 1000, 'unbounded scan findings')
        seen = set()
        for item in value[name]:
            require(isinstance(item, dict) and set(item) == {'rule', 'impact', 'nodeCount', 'locations', 'contrast'},
                    'finding contains unredacted DOM data or missing fields')
            rule = item['rule']
            require(isinstance(rule, str) and re.fullmatch('[a-z0-9-]{1,100}', rule) and rule not in seen,
                    'invalid or duplicate finding identity')
            seen.add(rule)
            require(item['impact'] in (None, 'minor', 'moderate', 'serious', 'critical')
                    and type(item['nodeCount']) is int and 0 < item['nodeCount'] <= 100000,
                    'finding has an invalid impact or node count')
            require(isinstance(item['locations'], list) and len(item['locations']) == min(item['nodeCount'], 100)
                    and all(isinstance(location, str) and re.fullmatch(
                        r'[a-z][a-z0-9-]*:nth-of-type\([1-9][0-9]*\)( > [a-z][a-z0-9-]*:nth-of-type\([1-9][0-9]*\)){0,7}',
                        location) for location in item['locations']), 'finding locations are absent or contain private selectors')
            require(isinstance(item['contrast'], list), 'contrast measurements are missing')
            if name == 'contrastPasses':
                require(rule == 'color-contrast' and rule in value['passedRules'], 'non-contrast or unreported passing rule')
            if rule != 'color-contrast':
                require(not item['contrast'], 'non-contrast finding contains measurement payloads')
                continue
            require(len(item['contrast']) == len(item['locations']), 'contrast locations are incomplete')
            for location, measurement in zip(item['locations'], item['contrast']):
                require(isinstance(measurement, dict) and set(measurement) == {'location', 'checks'}
                        and measurement['location'] == location, 'contrast location differs or contains unredacted data')
                checks = measurement['checks']
                require(isinstance(checks, list) and 0 < len(checks) <= 3, 'contrast check count is invalid')
                groups = set()
                for check in checks:
                    contrast_check(check)
                    require(check['group'] not in groups, 'contrast check group is repeated')
                    groups.add(check['group'])


def verify_accessibility(manifest, paths, manual_review=None):
    baseline = browser.verify_browser_campaigns(manifest, paths)
    inputs = dict(baseline['input_sha256'])
    limits = {name: browser.MAX_LOG_BYTES for name in inputs}
    missing, findings, campaigns = [], {}, []

    def remember(path, record=None, limit=MAX_SCAN_BYTES):
        value, size = browser.hash_file(path, limit)
        if record is not None:
            require(isinstance(record, dict) and record.get('sha256') == value
                    and type(record.get('bytes')) is int and record['bytes'] == size, 'retained payload bytes changed')
        name = str(path.absolute())
        require(name not in inputs or inputs[name] == value, 'input changed during inspection')
        inputs[name], limits[name] = value, limit
        return value

    def payload(parent, record, expected, limit=MAX_SCAN_BYTES):
        require(isinstance(record, dict), 'retained payload is missing')
        path = browser.retained_path(parent, record.get('file'))
        require(path == expected, 'retained payload belongs to a different campaign')
        remember(path, record, limit)
        return path

    for current in map(lambda path: Path(path).absolute(), paths):
        report = read_report(current)
        require(report.get('release_identity') == evidence_identity(manifest), 'scanner campaign has another release identity')
        section = report.get('accessibility')
        require(isinstance(section, dict) and set(section) == {'scanner', 'scans'}, 'scanner recording is missing')
        directory = current.with_name(current.stem + '.artifacts')
        scanner = section['scanner']
        require(isinstance(scanner, dict) and set(scanner) == set(TOOL_FILES), 'original scanner inputs are missing')
        data = {key: payload(current.parent, scanner[key], directory / 'tools' / name,
                             MAX_SCANNER_BYTES).read_bytes() for key, name in TOOL_FILES.items()}
        require(hashlib.sha256(data['lock']).hexdigest()
                == manifest['source_files'].get('scripts/dashboard/browser/package-lock.json', {}).get('sha256'),
                'scanner lock differs from the frozen source')
        verify_scanner(**data)
        scans = section['scans']
        require(isinstance(scans, dict) and 0 < len(scans) <= MAX_SCANS, 'bounded scan catalog is missing')
        observed, ordinals = {key: set() for key in CHECKPOINTS}, {key: [] for key in CHECKPOINTS}
        for name, record in sorted(scans.items()):
            path = browser.retained_path(current.parent, name)
            require(path.parent == directory / 'screenshots', 'scan is outside the campaign evidence directory')
            match = re.fullmatch(r'accessibility-(verify_browser|verify_actions_browser|verify_controls_browser)-([0-9]{2})-([a-z0-9-]{1,80})\.json', path.name)
            require(match is not None, 'scan has an unexpected checkpoint identity')
            script, ordinal, checkpoint = match.groups()
            require(checkpoint in CHECKPOINTS[script] and (checkpoint == 'sign-in' or checkpoint not in observed[script]),
                    'unexpected or duplicated scan checkpoint')
            observed[script].add(checkpoint)
            ordinals[script].append(int(ordinal))
            scan_hash = remember(path, record)
            value = read_report(path)
            scan_report(value)
            require(value['checkpoint'] == checkpoint, 'scan label differs from its recorded identity')
            for item in value['violations']:
                missing.append({'campaign': inputs[str(current)], 'checkpoint': checkpoint,
                                'rule': item['rule'], 'requirement': 'confirmed accessibility violation'})
            for item in value['needsReview']:
                key = (inputs[str(current)], scan_hash, item['rule'])
                findings[key] = {'checkpoint': checkpoint, 'node_count': item['nodeCount'],
                                 'locations': item['locations']}
        require(observed == CHECKPOINTS and all(sorted(values) == list(range(1, len(values) + 1))
                                               for values in ordinals.values()), 'required scan coverage or ordering is missing')
        campaigns.append({'report': str(current), 'sha256': inputs[str(current)], 'scans': len(scans)})

    reviewed, cases = set(), {}
    if manual_review is not None:
        current = Path(manual_review).absolute()
        remember(current, limit=browser.MAX_METADATA_BYTES)
        review = read_report(current)
        require(review.get('schema_version') == 1 and type(review['schema_version']) is int
                and review.get('release_identity') == evidence_identity(manifest)
                and review.get('full_wcag_conformance_claimed') is False, 'manual review has another scope or release identity')
        require(isinstance(review.get('reviewer'), str) and 0 < len(review['reviewer'].strip()) <= 256,
                'manual reviewer identity is missing')
        try:
            recorded = datetime.fromisoformat(review.get('recorded_at', '').replace('Z', '+00:00'))
        except (ValueError, TypeError, AttributeError) as error:
            raise ValueError('Manual accessibility review has an invalid timestamp') from error
        require(recorded.utcoffset() is not None, 'manual review has no timestamp offset')
        require(review.get('campaign_sha256') == sorted(item['sha256'] for item in campaigns),
                'manual review does not cover the original browser campaigns')

        def notes(item):
            require(item.get('outcome') in ('pass', 'fail', 'not_tested'), 'manual outcome is invalid')
            require(isinstance(item.get('tools'), list) and 0 < len(item['tools']) <= 16
                    and all(isinstance(tool, str) and 0 < len(tool.strip()) <= 256 for tool in item['tools']),
                    'actual manual-review tools are missing')
            record = item.get('notes')
            require(isinstance(record, dict), 'original manual review notes are missing')
            path = browser.retained_path(current.parent, record.get('file'))
            require(path != current, 'manual review cannot cite itself as execution evidence')
            remember(path, record)
            require(path.stat().st_size > 0, 'original manual notes are empty')

        require(isinstance(review.get('cases'), list) and len(review['cases']) <= len(MANUAL_CASES),
                'manual cases are unbounded or absent')
        for item in review['cases']:
            require(isinstance(item, dict) and isinstance(item.get('id'), str)
                    and item['id'] in MANUAL_CASES and item['id'] not in cases,
                    'unknown or duplicate manual case')
            notes(item)
            cases[item['id']] = item['outcome']
        require(isinstance(review.get('scan_reviews'), list) and len(review['scan_reviews']) <= len(findings),
                'manual finding reviews are unbounded or absent')
        for item in review['scan_reviews']:
            require(isinstance(item, dict), 'manual finding review is invalid')
            key = (item.get('campaign_sha256'), item.get('scan_sha256'), item.get('rule'))
            require(all(isinstance(value, str) for value in key) and key in findings and key not in reviewed,
                    'manual finding review belongs to another scan or is duplicated')
            notes(item)
            reviewed.add(key)
            if item['outcome'] != 'pass':
                missing.append({'requirement': 'manual finding review', 'campaign': key[0],
                                'scan': key[1], 'rule': key[2], 'outcome': item['outcome']})
    for case in sorted(MANUAL_CASES):
        if cases.get(case) != 'pass':
            missing.append({'requirement': 'manual accessibility case', 'case': case,
                            'outcome': cases.get(case, 'not_tested')})
    for key in sorted(findings.keys() - reviewed):
        missing.append({'requirement': 'manual finding review', 'campaign': key[0],
                        'scan': key[1], 'rule': key[2], 'outcome': 'not_tested', **findings[key]})
    for name, value in inputs.items():
        require(browser.hash_file(Path(name), limits[name])[0] == value, 'input changed during accessibility inspection')
    return {'schema_version': 1, 'status': 'incomplete' if missing else 'consistent_pending_review',
            'scope': 'frozen browser scans and retained manual accessibility review',
            'release_identity': evidence_identity(manifest), 'release_gate_passed': False,
            'full_wcag_conformance_claimed': False, 'input_sha256': inputs,
            'campaigns': campaigns, 'unverified': missing,
            'companion_requirements': ['browser', 'management', 'independent_review'],
            'remaining': ['Authenticate the original scan executions and human review records.',
                          'Complete the aggregate release assessment and authorized publication.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--campaign', type=Path, action='append', required=True)
    parser.add_argument('--manual-review', type=Path)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with os.fdopen(os.open(args.out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'wb') as output:
        try:
            manifest_digest = browser.hash_file(args.release, browser.MAX_METADATA_BYTES)[0]
            result = verify_accessibility(read_report(args.release), args.campaign, args.manual_review)
            require(browser.hash_file(args.release, browser.MAX_METADATA_BYTES)[0] == manifest_digest,
                    'release manifest changed during inspection')
            result['input_sha256'][str(args.release.absolute())] = manifest_digest
        except (ValueError, OSError, KeyError, TypeError, RuntimeError, tarfile.TarError) as error:
            result = {'schema_version': 1, 'status': 'invalid_evidence', 'release_gate_passed': False, 'error': str(error)}
        output.write(encoded(result))
        output.flush()
        os.fsync(output.fileno())
    print(result['status'])
    return {'consistent_pending_review': 0, 'incomplete': 2, 'invalid_evidence': 1}[result['status']]


if __name__ == '__main__':
    raise SystemExit(main())
