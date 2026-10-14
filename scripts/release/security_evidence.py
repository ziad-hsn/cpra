#!/usr/bin/env python3
"""Read frozen security checks and original findings without executing code."""
import argparse
from datetime import datetime
import hashlib
import math
from pathlib import Path
import re
import subprocess

import build_evidence as build
from evidence import read_report
from release import RECIPE, encoded, evidence_identity
import verify_worker_exclusion as exclusion


SCANNER_MODULE = 'golang.org/x/vuln'
SCANNER_VERSION = 'v1.7.0'
DATABASE = 'https://vuln.go.dev'
SCAN_ENVIRONMENT = dict(build.ENVIRONMENT, CGO_ENABLED='0')
CONTRACTS = {
    'application': {
        'internal/httpauth': ('TestNamedPrincipalsAndExactPermissions', 'TestBasicNeverUsesManagementPrivileges',
                             'TestTransportOriginAndContentType', 'TestRevocationAndConfigurationOwnership',
                             'TestBoundsValidationAndRedaction'),
        'internal/httpserver': ('TestServerStartFailsClosedWithoutCredentials',
                               'TestExplicitAnonymousAccessRequiresLoopbackAndNoAuthority',
                               'TestManagementAuthenticationAndUnverifiedReadiness',
                               'TestManagementCollectionPreflightAuthorizationDoesNotReadBody'),
        'internal/persistence': ('TestCatalogEncryptedSnapshotAndLogRestart',
                                 'TestAuthenticationRejectsInvalidPoliciesAndCorruptSnapshot',
                                 'TestRestoreResetResumesEveryCommittedPhaseAfterProcessKill'),
    },
    'sdk': {'': ('TestRedirectNeverForwardsCredentials', 'TestTLSAndURLPolicy',
                 'TestBoundedResponseAndAmbiguousMutation', 'TestErrorRedactionAndIdentity',
                 'TestUnknownDriverReadCannotBeReapplied')},
    'worker': {'': ('TestJournalEncryptionRestartAndIdentity', 'TestJournalRejectsPathsAndMissingKey',
                    'TestDiagnosticsBoundsAndErrorsDoNotLeak', 'TestChangedServerIdentityStopsBeforeExecution')},
}
BASE_CONTRACTS = {
    'application': {'': ('TestMainExternalJobsExcludedFromBaseBuild',),
                    'internal/httpserver': ('TestWorkerDefaultBuildExcludesSchemaAndRoutes',),
                    'internal/cpractl/cli': ('TestLocalExtensionsExcludeWorkerAuthentication',)},
    'sdk': {'api': ('TestBaseSchemaExcludesExtension',)},
}
EXTERNAL_CONTRACTS = {
    'application': {'internal/httpauth': ('TestWorkerCredentialDeniedByManagementHTTP',
                                        'TestWorkerAuthenticationRejectsBeforePolicyRead'),
                    'internal/httpserver': ('TestWorkerProductionRoutesRequireOptInAndWorkerAuthority',
                                           'TestJobTypeHTTPAuthorizesBeforeBodyRead')},
    'sdk': {'': ('TestWorkerRequiresAuthAndTLS',)},
}


def require(condition, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent security evidence: ' + detail)


def scan_command(scanner, tags):
    return [str(scanner), '-json', '-mode=source', '-scan=symbol', '-tags=' + ','.join(tags.split()), './...']


def timestamp(value):
    try:
        parsed = datetime.fromisoformat(value.replace('Z', '+00:00'))
        require(parsed.utcoffset() is not None, 'timestamp lacks a time zone')
        return parsed
    except (AttributeError, TypeError, ValueError) as error:
        raise ValueError('Invalid vulnerability database timestamp') from error


def scan_results(path, compiler, packages, modules):
    """JSON-mode exit zero is not evidence of zero vulnerabilities."""
    messages = build.worker.module_records(build.worker.bounded_text(path, build.MAX_LOG_BYTES))
    require(1 < len(messages) <= 100000 and isinstance(messages[0].get('config'), dict),
            'scanner configuration or original messages are missing')
    config, sbom, entries, findings = None, None, {}, []
    for message in messages:
        require(isinstance(message, dict) and len(message) == 1, 'ambiguous scanner message')
        name, value = next(iter(message.items()))
        require(name in ('config', 'progress', 'SBOM', 'osv', 'finding') and isinstance(value, dict),
                'unknown or malformed scanner message')
        if name == 'config':
            require(config is None, 'duplicate scanner configuration')
            config = value
            require(config.get('protocol_version') == 'v1.0.0' and config.get('scanner_name') == 'govulncheck'
                    and config.get('scanner_version') == SCANNER_VERSION and config.get('go_version') == compiler
                    and config.get('db') == DATABASE and config.get('scan_level') == 'symbol'
                    and config.get('scan_mode') == 'source', 'scanner version, database or analysis scope differs')
            timestamp(config.get('db_last_modified'))
        elif name == 'SBOM':
            require(sbom is None, 'duplicate scanner inventory')
            sbom = value
        elif name == 'osv':
            identifier = value.get('id')
            require(isinstance(identifier, str) and re.fullmatch(r'GO-\d{4}-\d{4,}', identifier)
                    and (identifier not in entries or entries[identifier] == value),
                    'invalid or contradictory vulnerability record')
            entries[identifier] = value
        elif name == 'finding':
            trace = value.get('trace')
            require(isinstance(value.get('osv'), str) and isinstance(trace, list) and 0 < len(trace) <= 1000
                    and all(isinstance(frame, dict) and isinstance(frame.get('module'), str)
                            and frame['module'] for frame in trace), 'finding has no valid execution trace')
            findings.append(value)
    require(isinstance(sbom, dict) and sbom.get('go_version') == compiler, 'scanner inventory or compiler is missing')
    roots = sbom.get('roots')
    require(isinstance(roots, list) and len(roots) == len(set(roots)) and set(roots) == set(packages),
            'scanner inventory omits or adds selected packages')
    selected = sbom.get('modules')
    require(isinstance(selected, list) and selected, 'scanner module inventory is empty')
    observed = {}
    expected = dict(modules, stdlib='v' + compiler.removeprefix('go'))
    for module in selected:
        require(isinstance(module, dict), 'scanner module record is malformed')
        name, version = module.get('path'), module.get('version', '')
        require(name in expected and name not in observed
                and version == expected[name], 'scanner module version differs from the selected dependency graph')
        observed[name] = version
    require('stdlib' in observed and any(name in observed for name in modules if not modules[name]),
            'scanner omitted the standard library or application module')
    affected = {}
    for finding in findings:
        identifier, frame = finding['osv'], finding['trace'][0]
        require(all(key not in frame or isinstance(frame[key], str) for key in ('version', 'package', 'function', 'receiver')),
                'finding trace fields are malformed')
        require(identifier in entries and frame['module'] in observed
                and frame.get('version', '') == observed[frame['module']],
                'finding is not bound to the retained database and dependency inventory')
        level = 'called' if frame.get('function') else 'imported' if frame.get('package') else 'required'
        affected.setdefault(identifier, set()).add(level)
    return {'database': DATABASE, 'database_last_modified': config['db_last_modified'],
            'roots': len(roots), 'modules': len(selected), 'findings': len(findings),
            'called': sorted(key for key, levels in affected.items() if 'called' in levels),
            'imported': sorted(key for key, levels in affected.items() if 'imported' in levels),
            'required': sorted(key for key, levels in affected.items() if 'required' in levels)}


def verify_exclusion(report, manifest, compiler, go):
    require(report.get('status') == 'pass' and report.get('workspace') == 'off'
            and report.get('evidenceClass') == 'local_worker_build_exclusion'
            and report.get('publicationReady') is False and report.get('workerExecutionQualified') is False
            and report.get('compiler') == 'go version ' + compiler + ' linux/amd64'
            and report.get('symbolCount') == len(exclusion.CASES), 'build exclusion did not pass the complete native contract')
    expected_env = {name: build.ENVIRONMENT.get(name, '') for name in (
        'GOTOOLCHAIN', 'GOENV', 'GOFLAGS', 'GOEXPERIMENT', 'GOWORK', 'GOPROXY', 'GOSUMDB',
        'GOPRIVATE', 'GONOSUMDB', 'GONOPROXY')}
    expected_env['GOFLAGS'] = ''  # The boundary commands explicitly pass -mod=readonly.
    require(report.get('goEnvironment') == expected_env, 'exclusion used a workspace or unverified dependencies')
    groups = {}
    for package, symbol, declaration in exclusion.CASES:
        groups.setdefault(package, []).append((symbol, declaration))
    checks = report.get('checks')
    require(isinstance(checks, list) and len(checks) >= 2, 'exclusion command records are missing')
    rows = iter(checks)

    def take(arguments, contents=None, symbols=None, absent=False):
        row = next(rows, None)
        require(isinstance(row, dict) and row.get('command') == [go, *arguments]
                and type(row.get('exitCode')) is int and type(row.get('seconds')) in (int, float)
                and type(row.get('timeoutSeconds')) is int
                and row['timeoutSeconds'] == exclusion.command_timeout(arguments)
                and row.get('timedOut', False) is False
                and math.isfinite(row['seconds']) and 0 <= row['seconds'] <= row['timeoutSeconds']
                and all(isinstance(row.get(key), str) and len(row[key].encode()) <= build.MAX_LOG_BYTES
                        for key in ('stdout', 'stderr')), 'exclusion command or original output is missing')
        if contents is not None:
            require(row.get('source') == contents and row.get('sourceSHA256') == hashlib.sha256(contents.encode()).hexdigest(),
                    'compiled declaration differs from the required symbol contract')
        if absent:
            require(row.get('expectedUnavailableSymbols') == symbols and 'expectedAvailableSymbols' not in row,
                    'negative compile selected a different symbol set')
            exclusion.require_symbols_absent(subprocess.CompletedProcess(row['command'], row['exitCode'],
                row['stdout'], row['stderr']), tuple(symbols))
        else:
            require(row['exitCode'] == 0 and 'expectedUnavailableSymbols' not in row, 'positive compile or boundary check failed')
            if symbols is not None:
                require(row.get('expectedAvailableSymbols') == symbols, 'positive compile omitted required symbols')
        return row

    version = take(['version'])
    require(version['stdout'].strip() == report['compiler'] and not version['stderr'].strip(),
            'compiler identity contradicts the original command')
    first = checks[1].get('command') if isinstance(checks[1], dict) else None
    require(isinstance(first, list) and len(first) == 8 and all(isinstance(value, str) for value in first)
            and Path(first[-1]).is_absolute() and Path(first[-1]).name == 'main.go'
            and Path(first[-2]).name == 'consumer' and Path(first[-2]).parent == Path(first[-1]).parent,
            'negative compilation paths are not the original scratch program')
    source, binary = first[-1], first[-2]

    def declaration(package, declarations, tags, symbols=None, absent=False):
        contents = 'package main\nimport excluded "' + exclusion.MODULE + '/' + package + '"\n' + '\n'.join(declarations) + '\nfunc main() {}\n'
        args = ['build', '-mod=readonly', '-p=1', *(['-gcflags=-e'] if absent else []), '-tags=' + tags,
                '-o', binary, source]
        return take(args, contents, symbols, absent)

    for package, declarations in groups.items():
        declaration(package, [item[1] for item in declarations], 'externaljobs', [item[0] for item in declarations])
    selections = []
    for tags in ('', ' '.join(RECIPE['build_tags'])):
        for package, name in (('internal/persistence', 'Store'), ('internal/httpauth', 'Authorizer'),
                              ('internal/localadmin', 'AuthenticationRequest')):
            declaration(package, ['var _ *excluded.' + name], tags)
        for package, declarations in groups.items():
            declaration(package, [item[1] for item in declarations], tags, [item[0] for item in declarations], True)
        row = take(['list', '-mod=readonly', '-tags=' + tags, '-json',
                    *[exclusion.MODULE + '/' + name for name in exclusion.PACKAGES]])
        packages = build.worker.module_records(row['stdout'])
        require(len(packages) == len(set(exclusion.PACKAGES)) and {p['ImportPath'] for p in packages}
                == {exclusion.MODULE + '/' + name for name in exclusion.PACKAGES}, 'boundary package selection differs')
        selected = {}
        for package in packages:
            module = package.get('Module', {})
            require(module.get('Path') in (exclusion.MODULE, exclusion.SDK_MODULE), 'boundary imports another module')
            prefix = 'sdk/go' if module['Path'] == exclusion.SDK_MODULE else ''
            relative = package['ImportPath'].removeprefix(module['Path']).strip('/')
            for name in package.get('GoFiles', []) + package.get('CgoFiles', []):
                require(isinstance(name, str) and Path(name).name == name and name.endswith('.go')
                        and not (name.startswith('worker_') and name.endswith('_external.go'))
                        and name not in ('worker_external.go', 'catalog_jobs_external.go'), 'worker implementation selected in normal build')
                key = '/'.join(value for value in (prefix, relative, name) if value)
                require(key in manifest['source_files'] and key not in selected, 'selected source is missing or duplicated')
                selected[key] = manifest['source_files'][key]['sha256']
        selections.append({'tags': tags, 'sha256': selected})
        dependencies = take(['list', '-mod=readonly', '-tags=' + tags, '-deps', '-f', '{{.ImportPath}}', './cmd/cpractl', '.'])
        require(not any(name == exclusion.SDK_MODULE + '/worker' or name.startswith(exclusion.SDK_MODULE + '/worker/')
                        for name in dependencies['stdout'].splitlines()), 'normal build imports the worker module')
        take(['test', '-mod=readonly', '-p=1', '-tags=' + tags, '-run', exclusion.BOUNDARY_TESTS,
              '-count=1', '-timeout=120s', './internal/httpserver', './internal/cpractl/cli'])
    require(next(rows, None) is None and report.get('selectedSources') == selections, 'exclusion source inventory differs')
    return {'symbols': len(exclusion.CASES), 'selections': 2}


def required_contracts(row, module):
    role = row['name'].split('-')[0]
    selections = [CONTRACTS.get(role, {}), (EXTERNAL_CONTRACTS if 'externaljobs' in row['tags'].split()
                                          else BASE_CONTRACTS).get(role, {})]
    return {(module + ('/' + package if package else ''), name)
            for selection in selections for package, names in selection.items() for name in names}


def verify_scanner_identity(info, scanner):
    require(info.splitlines() and info.splitlines()[0] == scanner + ': ' + RECIPE['go_version']
            and re.search(r'(?m)^\s*path\s+golang.org/x/vuln/cmd/govulncheck\s*$', info)
            and re.search(r'(?m)^\s*mod\s+golang.org/x/vuln\s+v1\.7\.0\s+h1:[A-Za-z0-9+/]+=\s*$', info)
            and '=> ' not in info, 'scanner binary identity or module differs')


def verify_security_campaigns(manifest, paths):
    identity = evidence_identity(manifest)
    matrix = build.verify_build_campaigns(manifest, paths)
    inputs, missing, results = dict(matrix['input_sha256']), list(matrix['unverified']), []

    def remember(path, record=None):
        path = Path(path).absolute()
        require(path.resolve(strict=True) == path, 'security input contains a link or alias')
        value, size = build.browser_evidence.hash_file(path, build.MAX_LOG_BYTES)
        require(record is None or (record.get('sha256') == value and record.get('bytes') == size), 'original security output changed')
        require(str(path) not in inputs or inputs[str(path)] == value, 'input changed during security inspection')
        inputs[str(path)] = value
        return path

    def original(parent, record, expected):
        require(isinstance(record, dict), 'original security output is missing')
        path = build.browser_evidence.retained_path(parent, record.get('file'))
        require(path == expected, 'security output belongs to another step')
        return remember(path, record)

    for path in map(lambda value: Path(value).absolute(), paths):
        report = read_report(path)
        compiler, go = report['go'], report['compiler']
        artifacts = path.with_name(path.stem + '.artifacts')
        security = report.get('security')
        if security is None:
            missing.append({'compiler': compiler, 'requirement': 'recorded security checks'})
            continue
        require(isinstance(security, dict) and isinstance(security.get('python'), str)
                and Path(security['python']).is_absolute(), 'security recorder is missing')
        row = security.get('exclusion')
        require(isinstance(row, dict) and row.get('exit_code') == 0, 'build exclusion command failed')
        command = row.get('command')
        require(isinstance(command, list) and len(command) == 8 and command[0] == security['python']
                and Path(command[1]).is_absolute() and command[1].endswith('/scripts/release/verify_worker_exclusion.py')
                and command[2:6] == ['--go', go, '--workspace', 'off']
                and command[6:] == ['--out', str(artifacts / 'worker-exclusion.json')], 'build exclusion command differs')
        for channel in ('stdout', 'stderr'):
            original(path.parent, row.get(channel), artifacts / ('security-exclusion-' + channel + '.log'))
        original(path.parent, row.get('report'), artifacts / 'worker-exclusion.json')
        exclusion_result = verify_exclusion(read_report(artifacts / 'worker-exclusion.json'), manifest, compiler, go)
        contracts = []
        for selected, (_, _, module, _tags) in zip(report['variants'], build.VARIANTS):
            completed = {(event.get('Package'), event.get('Test')) for event in
                         build.worker.module_records(build.worker.bounded_text(
                             artifacts / (selected['name'] + '-tests-stdout.log'), build.MAX_LOG_BYTES))
                         if event.get('Action') == 'pass' and event.get('Test')}
            required = required_contracts(selected, module)
            require(required <= completed, 'security contracts were absent or skipped: ' + selected['name'])
            contracts.append({'variant': selected['name'], 'passed': len(required)})
        scans = security.get('scans')
        if compiler == build.MINIMUM_COMPILER:
            require(scans == [], 'vulnerability qualification must use the production compiler')
            results.append({'compiler': compiler, 'exclusion': exclusion_result, 'contracts': contracts})
            continue
        require(isinstance(scans, list) and len(scans) == len(build.VARIANTS), 'vulnerability module/tag matrix is incomplete')
        scanner = security.get('scanner')
        require(isinstance(scanner, str) and Path(scanner).is_absolute(), 'pinned scanner is missing')
        original(path.parent, security.get('scanner_binary'), artifacts / 'govulncheck')
        original(path.parent, security.get('scanner_build_info'), artifacts / 'scanner-build-info-stdout.log')
        info = build.worker.bounded_text(artifacts / 'scanner-build-info-stdout.log', build.MAX_LOG_BYTES)
        verify_scanner_identity(info, scanner)
        graphs = {}
        for role in ('application', 'sdk', 'worker'):
            graphs[role] = {row['Path']: row.get('Version', '') for row in build.worker.module_records(
                build.worker.bounded_text(artifacts / (role + '-modules.jsonl'), build.MAX_LOG_BYTES))}
        scan_rows = []
        for row, (name, directory, module, tags) in zip(scans, build.VARIANTS):
            require(isinstance(row, dict) and row.get('name') == name and row.get('directory') == directory
                    and row.get('tags') == tags and row.get('environment') == SCAN_ENVIRONMENT
                    and row.get('command') == scan_command(scanner, tags) and row.get('exit_code') == 0,
                    'vulnerability command, tags, environment or completion differs')
            package_row = row.get('packages')
            selection = ['-tags=' + tags] if tags else []
            require(isinstance(package_row, dict) and package_row.get('command') == [go, 'list', '-json', *selection, './...']
                    and package_row.get('exit_code') == 0, 'scanner package selection differs')
            for channel in ('stdout', 'stderr'):
                original(path.parent, row.get(channel), artifacts / (name + '-vulnerabilities-' + channel + '.log'))
                original(path.parent, package_row.get(channel), artifacts / (name + '-scan-packages-' + channel + '.log'))
            packages = build.package_inventory(artifacts / (name + '-scan-packages-stdout.log'), module)
            result = scan_results(artifacts / (name + '-vulnerabilities-stdout.log'), compiler, packages, graphs[name.split('-')[0]])
            require(row.get('findings') == result, 'vulnerability summary contradicts original findings')
            if result['called']:
                missing.append({'variant': name, 'requirement': 'resolve reachable vulnerabilities', 'findings': result['called']})
            scan_rows.append({'variant': name, **result})
        results.append({'compiler': compiler, 'exclusion': exclusion_result, 'contracts': contracts, 'scans': scan_rows})
    for name, value in list(inputs.items()):
        require(build.browser_evidence.hash_file(Path(name), build.MAX_LOG_BYTES)[0] == value, 'retained security input changed')
    return {'schema_version': 1, 'status': 'incomplete' if missing else 'consistent_pending_review',
            'scope': 'frozen Linux amd64 security contracts, external-worker exclusion and vulnerability findings',
            'release_identity': identity, 'release_gate_passed': False, 'input_sha256': inputs,
            'reports': results, 'unverified': missing,
            'companion_requirements': ['browser', 'packaging', 'independent_review'],
            'remaining': ['Review original findings, scan dates, authentication and secret-handling behavior.',
                          'Complete browser security, other platform execution and independent security review.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--campaign', type=Path, action='append', required=True)
    args = parser.parse_args()
    result = verify_security_campaigns(read_report(args.release), args.campaign)
    print(encoded(result).decode(), end='')
    return 0 if result['status'] == 'consistent_pending_review' else 2


if __name__ == '__main__':
    raise SystemExit(main())
