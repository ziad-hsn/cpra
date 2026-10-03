#!/usr/bin/env python3
"""Record real browser execution from extracted, unchanged release source."""
import argparse
import os
from pathlib import Path
import sys
import tarfile
import urllib.request

import accessibility_evidence as accessibility
import browser_evidence as evidence
import build_evidence as build
import dependencies
from evidence import read_report
from release import ROOT, encoded, evidence_identity

sys.path.insert(0, str(ROOT / 'scripts/dashboard'))
import browser_campaign


def fingerprint(path, parent):
    value, size = evidence.hash_file(path, accessibility.MAX_SCANNER_BYTES)
    return {'file': path.relative_to(parent).as_posix(), 'sha256': value, 'bytes': size}


def retain(path, data):
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'wb') as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def download_scanner(url):
    # The caller obtains this exact public URL from scanner_lock. No API or
    # provider credentials, configured transports or authentication are used.
    with urllib.request.urlopen(url, timeout=20) as response:
        data = response.read(accessibility.MAX_SCANNER_BYTES + 1)
    accessibility.require(len(data) <= accessibility.MAX_SCANNER_BYTES, 'scanner download exceeds its byte limit')
    return data


def scanner_inputs(root, module):
    lock = (root / 'scripts/dashboard/browser/package-lock.json').read_bytes()
    record = accessibility.scanner_lock(lock)
    directory = module.parent / 'axe-core'
    for name in ('package.json', 'axe.min.js'):
        evidence.hash_file(directory / name, accessibility.MAX_SCANNER_BYTES)
    package, script = (directory / 'package.json').read_bytes(), (directory / 'axe.min.js').read_bytes()
    archive = download_scanner(record['resolved'])
    accessibility.verify_scanner(lock, archive, package, script)
    return {'lock': lock, 'archive': archive, 'package': package, 'script': script}


def record(args):
    root, out = args.source_dir.absolute(), args.out.absolute()
    artifacts = out.with_name(out.stem + '.artifacts')
    dependency_directory = args.dependencies.absolute()
    accessibility.require(out.resolve(strict=False) == out
                          and root.resolve(strict=True) == root
                          and dependency_directory.resolve(strict=True) == dependency_directory
                          and not out.is_relative_to(root) and not out.is_relative_to(dependency_directory),
                          'output must be separate from canonical source/dependency paths')
    accessibility.require(not out.exists() and not artifacts.exists(), 'output exists; choose a new record path')
    report = {'result': 'failed', 'release_qualified': False}
    owned_output = False
    try:
        release_path = args.release.absolute()
        release_hash = evidence.hash_file(release_path, evidence.MAX_METADATA_BYTES)[0]
        manifest = read_report(release_path)
        identity = evidence_identity(manifest)
        expected = build.source_inputs(manifest)
        accessibility.require(build.source_snapshot(root) == expected, 'source differs from the frozen release')
        cache = dependencies.cache_environment(dependency_directory, manifest)
        environment = {name: os.environ[name] for name in ('PATH', 'HOME', 'TMPDIR', 'TEMP', 'TMP', 'GOCACHE')
                       if name in os.environ}
        environment.update(cache, GOWORK='off')
        module = Path(args.module).absolute()
        scanner = scanner_inputs(root, module)
        dependency_path = dependency_directory / dependencies.REPORT
        dependency_bytes = dependency_path.read_bytes()
        report = browser_campaign.campaign(args, root=root, source_files=expected,
                                          source_commit=manifest['commit'], environment=environment)
        owned_output = True
        report['release_identity'] = identity
        tools = artifacts / 'tools'
        tools.mkdir(mode=0o700)
        retained = {}
        for name, filename in accessibility.TOOL_FILES.items():
            path = tools / filename
            retain(path, scanner[name])
            retained[name] = fingerprint(path, out.parent)
        dependency_copy = artifacts / 'dependencies.json'
        retain(dependency_copy, dependency_bytes)
        report['dependency_record'] = fingerprint(dependency_copy, out.parent)
        scans = {}
        for path in sorted((artifacts / 'screenshots').glob('accessibility-*.json')):
            accessibility.require(len(scans) < accessibility.MAX_SCANS, 'scan catalog exceeds its bound')
            accessibility.scan_report(read_report(path))
            item = fingerprint(path, out.parent)
            scans[item['file']] = {'sha256': item['sha256'], 'bytes': item['bytes']}
        report['accessibility'] = {'scanner': retained, 'scans': scans}
        accessibility.require(scans, 'actual browser execution produced no accessibility scans')
        accessibility.require(build.source_snapshot(root) == expected, 'frozen source changed during browser execution')
        accessibility.require(evidence.hash_file(release_path, evidence.MAX_METADATA_BYTES)[0] == release_hash,
                              'release metadata changed during browser execution')
        accessibility.require(dependency_path.read_bytes() == dependency_bytes,
                              'dependency provenance changed during browser execution')
        directory = module.parent / 'axe-core'
        accessibility.require((directory / 'package.json').read_bytes() == scanner['package']
                              and (directory / 'axe.min.js').read_bytes() == scanner['script'],
                              'executed scanner changed during browser execution')
    except (OSError, ValueError, KeyError, TypeError, RuntimeError, tarfile.TarError) as error:
        report.update(result='failed', error=str(error))
    out.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    if owned_output:
        # The delegated recorder owns this newly created private file. Existing
        # outputs were rejected before any input read or process execution.
        with out.open('wb') as stream:
            stream.write(encoded(report))
            stream.flush()
            os.fsync(stream.fileno())
    else:
        retain(out, encoded(report))
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--source-dir', type=Path, required=True)
    parser.add_argument('--dependencies', type=Path, required=True)
    parser.add_argument('--go', required=True)
    parser.add_argument('--node', required=True)
    parser.add_argument('--module', required=True)
    parser.add_argument('--browser', required=True)
    parser.add_argument('--tags', choices=('', 'externaljobs'), default='')
    parser.add_argument('--race', action=argparse.BooleanOptionalAction, default=True)
    parser.add_argument('--timeout', default='45m')
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    result = record(args)
    print(result['result'])
    return 0 if result['result'] == 'passed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
