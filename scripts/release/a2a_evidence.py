#!/usr/bin/env python3
"""Read frozen A2A lifecycle, upstream protocol and configured-effect evidence."""
import argparse
from pathlib import Path
import re

import build_evidence as build
import management_evidence as management
import provider_evidence as provider
from evidence import read_report
from release import encoded, evidence_identity


REFERENCE_MODULE = build.RECIPE['module'] + '/internal/jobs/testdata/a2aagent'
REFERENCE_DIRECTORY = 'internal/jobs/testdata/a2aagent'
REFERENCE_SDK = 'github.com/a2aproject/a2a-go/v2'
REFERENCE_VERSION = 'v2.6.0'
REFERENCE_FILES = tuple(REFERENCE_DIRECTORY + '/' + name for name in ('main.go', 'go.mod', 'go.sum'))
REFERENCE_ENVIRONMENT = dict(build.ENVIRONMENT, CGO_ENABLED='0', GOPROXY='https://proxy.golang.org')
REFERENCE_TEST_ENVIRONMENT = dict(build.ENVIRONMENT, CPRA_CLI_BUILD_TAGS='a2ainterop')
SCENARIOS = ('message', 'completed', 'failed', 'rejected', 'canceled', 'input-required', 'auth-required')
PATHS = ('transport', 'intervention', 'code')
REFERENCE_TEST = 'TestA2AUpstreamServer'
CONTRACTS = {
    '': ('TestMainA2AConfiguredActionsSurviveRestart', 'TestMainA2ARecoveryEscalation',
         'TestMainA2AObservesOriginalTaskAfterRestart', 'TestMainA2AFileRestartPreservesOriginalConnection',
         'TestMainA2AFileValidationRequiresKeyBeforeStorage'),
    'internal/jobs': ('TestA2ASendAndTask', 'TestA2ADoesNotReplayLostSendReply',
                      'TestA2AConnectionValidation', 'TestA2ARequestValidationBeforeSending',
                      'TestA2ARejectsRedirectWithoutForwardingCredentials', 'TestA2ARejectsUntrustedTLSCertificate',
                      'TestA2ASendRespectsCancellationAndRequestTimeout', 'TestA2AResponseValidation',
                      'TestA2ATaskIdentityCannotChange', 'TestA2ABoundsResponsesAndRedactsErrors',
                      'TestA2AReceiptKeepsProtocolStateWithoutAgentOutput', 'TestA2ATaskPathEscapesIdentity',
                      'TestA2ARespectsSSRFProtection', 'TestA2AJobRequiresCommittedInvocation',
                      'TestA2AJobOutcomesKeepAcceptanceSeparateFromCompletion',
                      'TestA2AJobCommitsBeforePollingAndDoesNotReplayFailedCommits',
                      'TestA2AJobCancellationDoesNotCancelOrRepeatRemoteTask', 'TestA2AJobDeadlineBoundsUnfinishedTask',
                      'TestA2AJobConfigurationBounds', 'TestA2AJobRecordsChangesWithoutRetainingEveryPoll',
                      'TestA2AJobReceiptPanicStillFinalizesLocalInvocation',
                      'TestA2AConfigurationBuildsWithoutProviderIO'),
    'internal/controller/systems': ('TestA2AControllerCommitsStartAndReceiptBeforeNetworkProgress',
                                    'TestA2AControllerUnknownOutcomeSurvivesRestartWithoutSending',
                                    'TestA2AControllerChangedConfigurationCannotRedirectQueuedAction',
                                    'TestA2AControllerKeepsOldTargetReceiptAfterConfigurationChange',
                                    'TestA2AControllerReceiptCancellationDoesNotPoisonStorage',
                                    'TestA2AControllerProcessCrashRecovery',
                                    'TestAgentObserverReadFailuresPreserveUnknownActions',
                                    'TestAgentObserverBoundsRequestsAndAdvancesPastFailures',
                                    'TestAgentObserverShutdownCancelsReadAndJoins'),
    'internal/persistence': ('TestAgentReceiptCommitIsConditionalAndIdempotent', 'TestAgentReceiptSnapshotAndReadsAreDetached',
                             'TestAgentReceiptFencesRemoteWorkSeparatelyFromLocalReturn',
                             'TestAgentReceiptSuccessRequiresEvidenceForItsRole', 'TestAgentReceiptDoesNotOverrideAmbiguousResult',
                             'TestAgentReceiptCannotEnterThroughConfiguration', 'TestAgentReceiptRetainsOriginalConfiguration',
                             'TestAgentReceiptRejectsTerminalReplacementAndLateFailure', 'TestAgentReceiptCommandAndImageValidation',
                             'TestAgentReceiptReviewRequiresRemoteCompletion', 'TestAgentReceiptSurvivesProcessKill',
                             'TestAgentObservationRetainsUnknownOutcomeAndReview',
                             'TestAgentObservationRejectsStaleOrSubstitutedAuthority',
                             'TestAgentConnectionBindsOriginalActionAndSurvivesConfigurationRemoval',
                             'TestAgentConnectionSnapshotAndReadsAreDetached', 'TestAgentObservationCommandAndSnapshotBounds',
                             'TestAgentConnectionPreservesManifestAdoption', 'TestAgentObservationSurvivesProcessKill',
                             'TestAgentObservationPreservesConcurrentOperatorReview',
                             'TestRecoveryNotificationWaitsForExhaustedAttempts', 'TestRecoveryNotificationUnknownEndpointRemainsHeld',
                             'TestRecoveryNotificationRequiresConfirmedFailure', 'TestRecoveryNotificationControlsAndReplay',
                             'TestRecoveryNotificationReceiptAndEndpointIsolation', 'TestRecoveryNotificationConfigurationCancelsOriginalIntent',
                             'TestRecoveryNotificationPolicyValidation', 'TestRecoveryNotificationSnapshotAndFormatBoundary',
                             'TestRecoveryNotificationSurvivesProcessKill'),
    'internal/management': ('TestA2AObservationsPreserveReceiptAndUnknownOutcome',
                            'TestA2AConfigurationRejectsUnsafeResolvedValues', 'TestA2ACatalogRequiresProtectedAgentReferences',
                            'TestRecoveryNotificationObservationRetainsOriginalFailure'),
    'internal/drivertest': ('TestA2AVerificationObserver', 'TestA2AVerificationPersistsOutcomesAndRequiresObservedEffects',
                            'TestA2AVerificationRejectsUnsafeSetupBeforeOtherOperations',
                            'TestA2AVerificationExampleRequiresExplicitConfiguration',
                            'TestA2AVerificationProcessCrashPreservesUnknownAction'),
}


def require(condition, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent A2A evidence: ' + detail)


def reference_commands(go):
    return {
        'environment': [go, 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH', 'GOOS', 'GOARCH', 'GOWORK'],
        'download': [go, 'mod', 'download'], 'verify': [go, 'mod', 'verify'],
        'modules': [go, 'list', '-m', '-json', 'all'], 'packages': [go, 'list', '-json', './...'],
        'vet': [go, 'vet', './...'],
        'build': [go, 'build', *build.RECIPE['build_flags'], '-o', 'REFERENCE_BINARY', '.'],
        'build-info': [go, 'version', '-m', 'REFERENCE_BINARY'],
    }


def reference_test_commands(go):
    return {'packages': [go, 'list', '-json', '-tags=a2ainterop', './internal/jobs'],
            'tests': [go, 'test', '-json', '-count=1', '-timeout=4m', '-race', '-tags=a2ainterop', './internal/jobs']}


def required_contracts():
    result = {(build.RECIPE['module'] + ('/' + package if package else ''), name)
              for package, names in CONTRACTS.items() for name in names}
    root = build.RECIPE['module']
    for source in ('api', 'manifest'):
        for role in ('intervention', 'notification'):
            result.add((root, 'TestMainA2AConfiguredActionsSurviveRestart/' + source + '/' + role))
    for test in ('TestMainA2AObservesOriginalTaskAfterRestart', 'TestMainA2AFileRestartPreservesOriginalConnection'):
        for role in ('intervention', 'notification'):
            for change in ('unchanged', 'reconfigured', 'removed'):
                result.add((root, test + '/' + role + '/' + change))
    for case in ('api/exhausted', 'manifest/exhausted', 'api/first-attempt', 'api/docker-server-error',
                 'api/docker-lost-reply', 'api/agent-lost-reply'):
        result.add((root, 'TestMainA2ARecoveryEscalation/' + case))
    for stage in ('started', 'receipt', 'snapshot', 'outcome', 'finished'):
        result.add((root + '/internal/persistence', 'TestAgentReceiptSurvivesProcessKill/' + stage))
    for test, stages in (
        ('TestRecoveryNotificationSurvivesProcessKill', ('before_result', 'queued', 'started', 'accepted', 'snapshot')),
        ('TestAgentObservationSurvivesProcessKill', ('working', 'snapshot', 'observed')),
    ):
        result.update((root + '/internal/persistence', test + '/' + stage) for stage in stages)
    result.update((root + '/internal/controller/systems', 'TestA2AControllerProcessCrashRecovery/' + stage)
                  for stage in ('before-admission', 'after-start', 'after-send', 'after-receipt', 'before-result', 'after-result'))
    return result


def reference_results(manifest, report, artifacts, remember):
    section = report['a2a_reference']
    go = section.get('compiler')
    binary = artifacts / 'a2a-agent'
    require(isinstance(go, str) and Path(go).is_absolute() and section.get('environment') == REFERENCE_ENVIRONMENT,
            'reference compiler or environment differs')
    require(section.get('directory') == REFERENCE_DIRECTORY and section.get('source_changed') == [],
            'reference source location or contents changed')
    sources = section.get('source_files')
    require(isinstance(sources, dict) and set(sources) == set(REFERENCE_FILES), 'reference source payloads are missing')
    for name, record in sources.items():
        remember(record, artifacts / 'a2a-source' / Path(name).name, manifest['source_files'].get(name, {}).get('sha256'))
    module = build.worker.bounded_text(artifacts / 'a2a-source/go.mod')
    require(re.search(r'(?m)^module ' + re.escape(REFERENCE_MODULE) + r'[ \t]*$', module)
            and re.search(r'(?m)^go 1\.26\.0[ \t]*$', module)
            and re.search(r'(?m)^(?:require[ \t]+|[ \t]+)' + re.escape(REFERENCE_SDK)
                          + r'[ \t]+' + re.escape(REFERENCE_VERSION) + r'[ \t]*$', module),
            'upstream SDK or module requirement differs')
    commands = reference_commands(go)
    commands['build'] = [go, 'build', *build.RECIPE['build_flags'], '-o', section.get('binary_path'), '.']
    commands['build-info'][-1] = section.get('binary_path')
    require(isinstance(section.get('binary_path'), str) and Path(section['binary_path']).is_absolute(), 'reference binary path is missing')
    rows = section.get('commands')
    require(isinstance(rows, list) and all(isinstance(row, dict) for row in rows)
            and [row.get('name') for row in rows] == list(commands), 'reference command inventory is incomplete')
    for row in rows:
        require(row.get('command') == commands[row['name']] and type(row.get('exit_code')) is int and row['exit_code'] == 0,
                'reference command failed or changes coverage')
        for channel in ('stdout', 'stderr'):
            remember(row.get(channel), artifacts / ('a2a-reference-' + row['name'] + '-' + channel + '.log'))
    require(read_report(artifacts / 'a2a-reference-environment-stdout.log') == {
        'GOVERSION': build.RECIPE['go_version'], 'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64',
        'GOOS': 'linux', 'GOARCH': 'amd64', 'GOWORK': 'off'}, 'reference compiler or native host differs')
    require(build.worker.bounded_text(artifacts / 'a2a-reference-verify-stdout.log').strip() == 'all modules verified',
            'reference dependencies were not verified')
    require(all(build.worker.bounded_text(artifacts / ('a2a-reference-vet-' + field + '.log')).strip() == ''
                for field in ('stdout', 'stderr')), 'reference vet produced diagnostics')
    graph = build.worker.module_records(build.worker.bounded_text(artifacts / 'a2a-reference-modules-stdout.log'))
    build.dependencies.dependency_graph(graph, REFERENCE_MODULE, {})
    selected = [row for row in graph if row.get('Path') == REFERENCE_SDK]
    require(len(selected) == 1 and selected[0].get('Version') == REFERENCE_VERSION, 'reference graph selected another SDK')
    sums = {}
    for line in build.worker.bounded_text(artifacts / 'a2a-source/go.sum').splitlines():
        fields = line.split()
        require(len(fields) == 3 and re.fullmatch(r'h1:[A-Za-z0-9+/]{43}=', fields[2])
                and tuple(fields[:2]) not in sums, 'reference checksum inventory is malformed or duplicated')
        sums[tuple(fields[:2])] = fields[2]
    for row in graph:
        if row.get('Main') is True:
            continue
        for field, suffix in (('Sum', ''), ('GoModSum', '/go.mod')):
            if field not in row and row['Path'] != REFERENCE_SDK:
                continue
            require(isinstance(row.get('Version'), str) and row.get(field) is not None
                    and sums.get((row['Path'], row['Version'] + suffix)) == row[field],
                    'reference graph checksums differ from the frozen source')
    package_path = artifacts / 'a2a-reference-packages-stdout.log'
    require(build.package_inventory(package_path, REFERENCE_MODULE) == {REFERENCE_MODULE: False},
            'reference package selection differs')
    packages = build.worker.module_records(build.worker.bounded_text(package_path))
    require(len(packages) == 1 and packages[0].get('Name') == 'main'
            and packages[0].get('GoFiles') == ['main.go'], 'reference executable sources differ')
    remember(section.get('binary'), binary, limit=provider.MAX_BINARY_BYTES)
    info = build.worker.bounded_text(artifacts / 'a2a-reference-build-info-stdout.log')
    require(info.splitlines() and info.splitlines()[0] == section['binary_path'] + ': ' + build.RECIPE['go_version'],
            'reference executable compiler differs')
    main, settings = provider.build_settings(info)
    require(main == REFERENCE_MODULE and all(settings.get(key) == value for key, value in {
        'GOOS': 'linux', 'GOARCH': 'amd64', 'CGO_ENABLED': '0', 'GOAMD64': 'v1', '-trimpath': 'true',
        '-buildmode': 'exe', '-compiler': 'gc'}.items()) and not any(
            key.startswith('vcs.') or key in ('-race', '-cover', '-gcflags', '-ldflags', '-tags') for key in settings),
            'reference executable settings differ')
    dependencies, selected_modules = [], {row['Path']: row for row in graph}
    for line in info.splitlines()[1:]:
        fields = line.strip().split('\t')
        if fields[0] == 'dep':
            require(len(fields) == 4 and sums.get((fields[1], fields[2])) == fields[3]
                    and selected_modules.get(fields[1], {}).get('Version') == fields[2],
                    'reference executable dependency differs from frozen checksums')
            dependencies.append(fields[1])
        require(fields[0] != '=>', 'reference executable has a replaced dependency')
    require(REFERENCE_SDK in dependencies and len(dependencies) == len(set(dependencies)),
            'reference executable does not identify the pinned SDK or duplicates dependencies')
    tests = section.get('tests')
    require(isinstance(tests, dict) and tests.get('environment') == dict(REFERENCE_TEST_ENVIRONMENT, CPRA_A2A_AGENT=section['binary_path']),
            'interop environment or reference executable differs')
    for name, command in reference_test_commands(report['compiler']).items():
        row = tests.get(name)
        require(isinstance(row, dict) and row.get('command') == command and type(row.get('exit_code')) is int
                and row['exit_code'] == 0, 'interop selection or completion differs')
        for channel in ('stdout', 'stderr'):
            remember(row.get(channel), artifacts / ('a2a-interop-' + name + '-' + channel + '.log'))
    packages = build.package_inventory(artifacts / 'a2a-interop-packages-stdout.log', build.RECIPE['module'])
    require(set(packages) == {build.RECIPE['module'] + '/internal/jobs'}, 'interop selected an unexpected package')
    selection = build.worker.module_records(build.worker.bounded_text(artifacts / 'a2a-interop-packages-stdout.log'))
    require('a2a_interop_test.go' in selection[0].get('TestGoFiles', []), 'upstream interoperability source was excluded')
    result = build.test_results(artifacts / 'a2a-interop-tests-stdout.log', packages, 'a2ainterop')
    required = {(build.RECIPE['module'] + '/internal/jobs', REFERENCE_TEST)} | {
        (build.RECIPE['module'] + '/internal/jobs', REFERENCE_TEST + '/' + path + '/' + scenario)
        for path in PATHS for scenario in SCENARIOS}
    coverage = management.contract_results(artifacts / 'a2a-interop-tests-stdout.log', required, (), report['go'], 'a2ainterop')
    return {'upstream_sdk': REFERENCE_SDK + '@' + REFERENCE_VERSION, 'scenarios': 21, 'coverage': coverage,
            'package_results': result, 'evidence_class': 'upstream_protocol_fixture'}


def verify_a2a_campaigns(manifest, paths, build_directory=None, reports=()):
    require(set(REFERENCE_FILES) <= manifest['source_files'].keys(), 'frozen reference source is missing')
    matrix = build.verify_build_campaigns(manifest, paths)
    inputs, limits = dict(matrix['input_sha256']), {name: build.MAX_LOG_BYTES for name in matrix['input_sha256']}
    missing, results = list(matrix['unverified']), []

    def remember(record, expected, source=None, limit=build.MAX_LOG_BYTES):
        require(isinstance(record, dict), 'retained A2A payload is missing')
        path = build.browser_evidence.retained_path(current.parent, record.get('file'))
        require(path == expected, 'A2A payload belongs to another step')
        value, size = build.browser_evidence.hash_file(path, limit)
        require(value == record.get('sha256') and type(record.get('bytes')) is int and size == record['bytes']
                and (source is None or value == source), 'A2A payload fingerprint or frozen source differs')
        require(str(path) not in inputs or inputs[str(path)] == value, 'input changed across inspection')
        inputs[str(path)], limits[str(path)] = value, limit

    for current in map(lambda value: Path(value).absolute(), paths):
        report = read_report(current)
        artifacts = current.with_name(current.stem + '.artifacts')
        rows = []
        for row in report['variants'][:4]:
            result = management.contract_results(artifacts / (row['name'] + '-tests-stdout.log'), required_contracts(), (), report['go'], row['tags'])
            missing.extend(dict(item, compiler=report['go'], variant=row['name']) for item in result['missing'])
            rows.append({'variant': row['name'], **result})
        reference = None
        if report.get('a2a_reference') is None:
            missing.append({'compiler': report['go'], 'requirement': 'original upstream reference build and interoperability execution'})
        else:
            require(isinstance(report['a2a_reference'], dict), 'reference recording is malformed')
            reference = reference_results(manifest, report, artifacts, remember)
            missing.extend(dict(item, compiler=report['go'], variant='a2ainterop') for item in reference['coverage']['missing'])
            missing.extend(dict(item, compiler=report['go'], variant='a2ainterop') for item in reference['package_results']['unverified_skips'])
        results.append({'compiler': report['go'], 'lifecycle_contracts': rows, 'reference': reference})
    require((build_directory is None) == (not reports), 'configured-effect build and reports must be supplied together')
    effects = []
    if build_directory is not None:
        effect_result = provider.verify_provider_reports(manifest, build_directory, reports)
        for name, value in effect_result['input_sha256'].items():
            require(name not in inputs or inputs[name] == value, 'configured effects changed a shared input')
            inputs[name] = value
        limits.update({name: provider.MAX_BINARY_BYTES if Path(name).name == provider.PROGRAM else provider.MAX_LOG_BYTES
                       for name in effect_result['input_sha256']})
        effects = [row for row in effect_result['providers']['records'] if row['driver'] == 'a2a']
    for kind in ('intervention', 'code'):
        selected = next((row for row in effects if row['kind'] == kind), None)
        if selected is None or selected.get('status') != 'pass' or selected.get('strongest_evidence_type') != 'live_account' \
                or selected.get('observation_boundary') != 'effect':
            missing.append({'requirement': 'configured agent effect', 'kind': kind, 'driver': 'a2a'})
    for name, value in inputs.items():
        require(build.browser_evidence.hash_file(Path(name), limits[name])[0] == value, 'retained A2A input changed')
    return {'schema_version': 1, 'status': 'incomplete' if missing else 'consistent_pending_review',
            'scope': 'frozen Linux amd64 A2A lifecycle, upstream interoperability and designated agent effects',
            'release_identity': evidence_identity(manifest), 'release_gate_passed': False,
            'formal_vendor_certification': False, 'input_sha256': inputs, 'reports': results,
            'configured_effects': effects, 'unverified': missing,
            'companion_requirements': ['management', 'security', 'packaging', 'independent_review'],
            'remaining': ['Authenticate original executions, agent authority and independently observed effects.',
                          'Protocol fixture acceptance does not establish completed recovery or monitored-service health.',
                          'Review upstream fixture dependencies and complete other platform and aggregate release checks.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--campaign', type=Path, action='append', required=True)
    parser.add_argument('--provider-build', type=Path)
    parser.add_argument('--provider-report', type=Path, action='append', default=[])
    args = parser.parse_args()
    result = verify_a2a_campaigns(read_report(args.release), args.campaign, args.provider_build, args.provider_report)
    print(encoded(result).decode(), end='')
    return 0 if result['status'] == 'consistent_pending_review' else 2


if __name__ == '__main__':
    raise SystemExit(main())
