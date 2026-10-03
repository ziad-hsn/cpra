#!/usr/bin/env python3
"""Inspect frozen API inputs and original server, SDK and CLI contract results.

SDK transport fixtures and actual server integration remain separate evidence.
This reader runs no application code and cannot authorize publication.
"""
import argparse
import copy
import hashlib
from pathlib import Path
import re
import zipfile

import build_evidence as build
from evidence import decode_json, read_report
from release import encoded, evidence_identity


MAX_CONTRACT_BYTES = 2 * 1024 * 1024
MAX_OPERATIONS = 4096
CONTRACT_FILES = ('api/openapi/operations.json', 'api/openapi/contract.base.json',
                  'api/openapi/models.base.json', 'api/openapi/externaljobs.json')
SDK_CONTRACT_FILES = ('testdata/operations.json', 'api/schema_base.json', 'api/schema_external.json')
CLI_TESTS = ('TestMainManagementRealCLI', 'TestMainManagementRealCLIDiff',
             'TestMainManagementCollectionApplyCLI', 'TestMainManagementCollectionReselectionCLI',
             'TestMainManagementCollectionReselectionCLILostReplies')
CONTRACTS = {
    'application': {
        '': (*CLI_TESTS, 'TestMainManagedTLSWritesRecoverAuthoritativeCatalog',
             'TestMainManagementValidationAndInvalidBootstrapDoNotAdmitConfiguration',
             'TestMainManagementTransportSettingsFailBeforeState'),
        'internal/httpserver': (
            'TestManagementRealSDKAndSharedResources', 'TestManagementStrictInputsAndReferencedDelete',
            'TestManagementAuthenticationAndUnverifiedReadiness', 'TestManagementRoutesRequireExplicitPairedConfiguration',
            'TestManagementPrepareDeleteHasNoEffectUntilCommit', 'TestManagementMutationReceiptsRemainCommittedUntilOwnerApplies',
            'TestManagementReadinessAndDrainingRejectWritesKeepReads', 'TestManagementCommitBoundaryRechecksReadinessAndCancellation',
            'TestManagementIncidentControlsRealSDK', 'TestManagementSnoozeVersionAndOperationContract',
            'TestManagementControlsRejectInvalidAndUnauthorizedRequests', 'TestManagementIncidentPaginationFreezesAndBindsAccess',
            'TestManagementObservationSDKContractsAndSourceUnits', 'TestManagementObservationPaginationAndAuthorization',
            'TestManagementUnavailableAndBoundedObservations', 'TestManagementOldestPendingAgeUsesRealPendingQueue',
            'TestManagementHistoryRealSDKPreservesOriginalAudit', 'TestManagementHistoryCursorBindsPrincipalFilterAndRetention',
            'TestManagementHistoryByteBoundPreservesEveryEvent', 'TestManagementSnapshotReadsAllowConcurrentRequestsAndRevocation',
            'TestManagementSnapshotReadsRecheckExpiryAndGeneration', 'TestManagementActionReviewRequiresActualExecutorFence',
            'TestManagementRecoveryIsGuardedIntentWithPendingReceipt', 'TestManagementActionShowsSeparateLateProviderEvidence',
            'TestManagementOperationListFreezesLiveAndTerminalReceipts', 'TestManagementOperationListBindsCursorAndAdvertisesRegisteredAccess',
            'TestManagementOperationReadsDoNotHoldAuthorityOrCursorLocks', 'TestManagementOperationReadsRecheckClockAndGeneration',
            'TestManagementCollectionPreflightSDKUsesStagedReferencesWithoutStateEffects',
            'TestManagementCollectionPreflightMalformedFinalResourceHasZeroEffects',
            'TestManagementCollectionPreflightMissingReferenceIsAnUncommittedValidationResult',
            'TestManagementCollectionPreflightResourceAndItemBounds', 'TestManagementCollectionPreflightPermissionDenialPrecedesDecryption',
            'TestCollectionAdmissionHTTPStagesOriginalBytesWithoutActivation', 'TestCollectionAdmissionHTTPRejectsPrincipalRemapBeforeResponse',
            'TestCollectionAdmissionHTTPRechecksAuthorizationAfterKeyWrap', 'TestManagementOperationAdmissionIdentitiesThroughTLSAndSDK',
            'TestManagementAllocationUnconfirmedErrorInteroperatesWithSDK', 'TestCollectionValidationHTTPOriginalAsyncResultAndPagination',
            'TestCollectionValidationHTTPAuthorizationAndCursorBinding', 'TestCollectionValidationHTTPRejectionAndNoVerdictRemainDistinct',
            'TestCollectionActivationHTTPOriginalAdmissionAndRetry', 'TestCollectionActivationHTTPFinalResponseFence',
            'TestCollectionExecutionHTTPCommittedAndAppliedRemainDistinct', 'TestCollectionExecutionHTTPAuthorizationCursorAndBounds',
            'TestCollectionExecutionHTTPBlockedReadAuthorizationAndIndependentReads',
            'TestCollectionReselectionHTTPRealSDKPreservesPrefixAndDoesNotActivate',
            'TestCollectionReselectionHTTPChangedRawFailsWithoutAppend', 'TestCollectionReselectionHTTPAuthorizationBeforeBody',
            'TestCollectionCancellationHTTPOriginalReceiptAndInactivePrefix', 'TestCollectionCancellationHTTPConcurrentRetriesAndShutdown'),
        'internal/controller': ('TestCatalogControllerCreateEditDisableDeleteAndRestart',
                                'TestCatalogControllerQueuedChecksCannotUseSupersededTarget',
                                'TestCatalogCredentialRotationRetainsLateActionEvidenceWithoutRedelivery',
                                'TestCatalogManualRecoveryAndHistoricalReviewUseOwnerReceipts',
                                'TestCatalogPauseExposureTracksOverlapDriverChangeRemovalAndRestart'),
        'internal/cpractl/cli': ('TestManagementDocumentedResourcesPassCatalogValidation',
                                'TestManagementEventsUseBoundedV2HistoryAndPreserveAuditFields',
                                'TestManagementOperationsListUsesOneBoundedTLSPage',
                                'TestManagementExecutionResultsUseOneExactSDKPage',
                                'TestCollectionApplyMalformedFinalFilePreventsAllRequests',
                                'TestCollectionWaitCancellationDoesNotCancelServer',
                                'TestCollectionResumeUploadLostResponsesRetainHandles'),
    },
    'sdk': {
        '': ('TestMutationPreconditionsAndPresence', 'TestRedirectNeverForwardsCredentials', 'TestTLSAndURLPolicy',
             'TestBoundedResponseAndAmbiguousMutation', 'TestErrorRedactionAndIdentity', 'TestReadRetriesOnly',
             'TestCancellationAndLocalTokenFailure', 'TestUnknownDriverReadCannotBeReapplied', 'TestIteratorBoundedAndLazy',
             'TestWaitCancellationDoesNotCancelOperation', 'TestPatchRejectsDangerousInputs', 'TestMergePatchNullIsTransmitted',
             'TestReselectionInputBoundsBeforeHTTP', 'TestReselectionMutationFailureNeverRetries',
             'TestReselectionResponseIdentityAndBounds', 'TestReselectionAsyncAdmissionDisposition'),
        'api': ('TestOptionalPresence', 'TestEveryBuiltInDriverRoundTrips', 'TestMutableSpecPreservesZeroAndEmpty',
                'TestObservationKnownZeroAndOlderOptionalFields', 'TestHistoryAuditFieldsRoundTrip',
                'TestRecipientAndGroupContract', 'TestAccessRequiresExplicitPermissions',
                'TestActionReviewAndEvidenceRoundTrip', 'TestCollectionReceiptCountPresence'),
        'collection': ('TestFrozenBytesOrderingAndOverlap', 'TestYAMLJSONAndHugeManifestStreaming',
                       'TestSharedOnlyAndCrossFileReferences', 'TestListMultiDocumentDuplicateKeysAndLimits',
                       'TestSourceURLsAreSeparateBoundedAndRedacted', 'TestMalformedLastFileNeverCreatesOperation',
                       'TestApplyFullValidationBeforeActivationAndResumeIdentity', 'TestApplyChunksAndAmbiguousHandle',
                       'TestHTTPResumeAfterCommittedUploadLosesReply', 'TestApplyNeverActivatesUnsupportedOrUnboundValidation',
                       'TestApplyReconcilesOnlyOriginalUncertainActivation',
                       'TestCollectionWaitRetainsOriginalPartialObservationOnCancellation',
                       'TestFrozenIdentityBindsRawSourcesAndExactResourceBytes', 'TestFrozenReadbackRejectsTamperedResource',
                       'TestFrozenCumulativeSourceQuotaAndCanceledEmpty', 'TestInventoryExactResourceBytesSurvivePublicSDKUpload',
                       'TestAll35ManifestDriverVariants'),
    },
}
EXTERNAL_CONTRACTS = {'application': {'internal/httpserver': (
    'TestJobTypeHTTPRealSDKReceiptsAndRestart', 'TestJobTypeHTTPGatesAndDiscovery',
    'TestJobTypeHTTPInvalidInputHasNoMutation')}, 'sdk': {'': ('TestWorkerRequiresAuthAndTLS',)}}


def require(condition, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent management evidence: ' + detail)


def capture_contracts(root, manifest, artifacts, parent):
    """Keep original non-secret schema inputs alongside each build recording."""
    directory = artifacts / 'contracts'
    directory.mkdir(mode=0o700)
    records = {}
    for name in CONTRACT_FILES:
        path = root / name
        require(path.resolve(strict=True) == path, 'contract source contains a link or alias')
        value, size = build.browser_evidence.hash_file(path, MAX_CONTRACT_BYTES)
        require(manifest['source_files'].get(name, {}).get('sha256') == value, 'contract source differs: ' + name)
        target = directory / Path(name).name
        with path.open('rb') as source, target.open('xb') as output:
            target.chmod(0o600)
            data = source.read(MAX_CONTRACT_BYTES + 1)
            require(len(data) == size and hashlib.sha256(data).hexdigest() == value, 'contract changed during capture')
            output.write(data)
        records[name] = {'file': target.relative_to(parent).as_posix(), 'sha256': value, 'bytes': size}
    return records


def normalized_refs(value):
    if isinstance(value, dict):
        return {key: (item.replace('./models.json#/components/', '#/components/') if key == '$ref' and isinstance(item, str)
                      else normalized_refs(item)) for key, item in value.items()}
    if isinstance(value, list):
        return [normalized_refs(item) for item in value]
    return value


def contract_inventory(payloads):
    inventory = decode_json(payloads[CONTRACT_FILES[0]], CONTRACT_FILES[0])
    require(isinstance(inventory, list) and 0 < len(inventory) <= MAX_OPERATIONS, 'operation inventory is empty or unbounded')
    require(payloads[CONTRACT_FILES[0]] == payloads['sdk/go/testdata/operations.json'], 'server and SDK inventories differ')
    base, models, extension = [decode_json(payloads[name], name) for name in CONTRACT_FILES[1:]]
    require(all(isinstance(value, dict) for value in (base, models, extension)), 'schema input is not an object')
    require(base.get('openapi') == models.get('openapi') == '3.1.2' and models.get('paths') == {}
            and isinstance(extension.get('paths'), dict) and isinstance(extension.get('schemas'), dict), 'schema format differs')
    expected = copy.deepcopy(base)
    expected['components'] = copy.deepcopy(models['components'])
    schemas_by_tag = {}
    for tagged, name in ((False, 'schema_base'), (True, 'schema_external')):
        if tagged:
            for path, methods in extension['paths'].items():
                require(path not in expected['paths'], 'extension overwrites a base path')
                expected['paths'][path] = methods
            require(not (set(extension['schemas']) & set(expected['components']['schemas'])), 'extension overwrites a base model')
            expected['components']['schemas'].update(extension['schemas'])
        schema_name = 'sdk/go/api/' + name + '.json'
        schema = decode_json(payloads[schema_name], schema_name)
        require(schema == normalized_refs(expected), 'generated SDK schema differs from canonical inputs: ' + name)
        schemas_by_tag['externaljobs' if tagged else ''] = set(schema['components']['schemas'])
        operations, operation_specs = {}, {}
        for path, methods in schema['paths'].items():
            for method, operation in methods.items():
                if method not in ('get', 'put', 'post', 'delete', 'patch', 'head', 'options', 'trace'):
                    continue
                identity = operation.get('operationId')
                require(isinstance(identity, str) and identity not in operations, 'duplicate or missing schema operation identity')
                operations[identity] = (method.upper(), path)
                operation_specs[identity] = operation
        selected = {row.get('operationID'): (row.get('method'), row.get('path')) for row in inventory
                    if isinstance(row, dict) and (tagged or row.get('buildTag') == '')}
        require(operations == selected, 'schema operations do not match the complete inventory')
    ids, routes = set(), set()
    for row in inventory:
        fields = {'operationID', 'method', 'path', 'schema', 'request', 'sdkMethod',
                  'buildTag', 'contractTest', 'cas', 'listing'}
        require(isinstance(row, dict) and fields <= row.keys() <= fields | {'availability'}
                and row.get('availability', 'implemented') == 'implemented', 'inventory fields differ')
        identity, tag = row['operationID'], row['buildTag']
        route = (row['method'], row['path'])
        require(isinstance(identity, str) and re.fullmatch('[A-Z][A-Za-z0-9]*', identity)
                and identity not in ids and route not in routes, 'inventory has duplicate identities or routes')
        require(tag in ('', 'externaljobs') and isinstance(row['path'], str) and row['path'].startswith('/api/v2/')
                and type(row['cas']) is bool and type(row['listing']) is bool, 'inventory metadata differs')
        require(isinstance(row['sdkMethod'], str) and re.fullmatch(r'[A-Z][A-Za-z0-9]*\.[A-Z][A-Za-z0-9]*', row['sdkMethod']),
                'SDK method identity is invalid')
        require(row['contractTest'] in (('TestExternalOperationInventory',) if tag else
                                       ('TestOperationInventory', 'TestReselectionOperationInventory')), 'inventory test is not supported')
        operation = operation_specs[identity]
        responses = operation.get('responses', {})
        no_content = responses.get('204')
        reference = '#/components/schemas/' + row['schema'] if isinstance(row['schema'], str) else None
        success_models = {value.get('content', {}).get('application/json', {}).get('schema', {}).get('$ref')
                          for status, value in responses.items() if status.startswith('2')}
        require(isinstance(row['request'], str) and isinstance(row['schema'], str)
                and ((row['schema'] in schemas_by_tag[tag] and reference in success_models) or
                     (row['schema'] == '' and isinstance(no_content, dict) and 'content' not in no_content)),
                'inventory response model is missing or differs from the wire schema')
        body = operation.get('requestBody', {})
        content = body.get('content', {})
        if row['request'] == 'binary':
            body_schema = content.get('application/octet-stream', {}).get('schema', {})
            require(body_schema.get('type') == 'string' and body_schema.get('format') == 'binary'
                    and body_schema.get('maxLength') == 1048576, 'binary source framing differs')
        elif row['request']:
            media_type = 'application/merge-patch+json' if row['request'] == 'MergePatch' else 'application/json'
            require(row['request'] in schemas_by_tag[tag]
                    and content.get(media_type, {}).get('schema', {}).get('$ref') == '#/components/schemas/' + row['request'],
                    'inventory request model or media type differs')
        else:
            require(not body, 'inventory omitted a request body')
        require(not row['cas'] or any(item.get('name') == 'If-Match' and item.get('in') == 'header' and item.get('required') is True
                                     for item in operation.get('parameters', [])), 'version precondition is missing from the wire schema')
        ids.add(identity); routes.add(route)
    return inventory


def required_contracts(row, module, inventory):
    role = row['name'].split('-')[0]
    external = 'externaljobs' in row['tags'].split()
    selections = [CONTRACTS.get(role, {}), EXTERNAL_CONTRACTS.get(role, {}) if external else {}]
    result = {(module + ('/' + package if package else ''), name)
              for selection in selections for package, names in selection.items() for name in names}
    if role == 'sdk':
        selected = [item for item in inventory if external or item['buildTag'] == '']
        result.update((module, item['contractTest']) for item in selected)
        result.update((module, item['contractTest'] + '/' + item['operationID']) for item in selected)
    return result


def contract_results(path, required, cli_tests, compiler, tags):
    completed, output = set(), {name: '' for name in cli_tests}
    total = 0
    with path.open('rb') as stream:
        while line := stream.readline(build.MAX_LINE_BYTES + 1):
            total += len(line)
            require(len(line) <= build.MAX_LINE_BYTES and total <= build.MAX_LOG_BYTES, 'Go event input exceeds its byte limit')
            event = build.browser_evidence.parse_event(line)
            name, package = event.get('Test'), event.get('Package')
            if event['Action'] == 'pass' and name:
                completed.add((package, name))
            if package == build.RECIPE['module'] and name in output and event['Action'] == 'output':
                output[name] += event.get('Output', '')
                require(len(output[name].encode()) <= 256 * 1024, 'CLI diagnostics exceed their byte limit')
    missing = sorted(required - completed)
    helpers = []
    for name, text in output.items():
        if (build.RECIPE['module'], name) not in completed:
            continue
        matches = re.findall(r'Native CLI helper uses (go[0-9.]+); build tags: ("[^\n]*")', text)
        require(len(matches) == 1 and matches[0] == (compiler, encoded(tags).decode().strip()),
                'actual CLI compiler or tags are missing or differ: ' + name)
        helpers.append(name)
    return {'required': len(required), 'passed': len(required & completed),
            'missing': [{'package': package, 'test': name} for package, name in missing], 'native_cli_helpers': helpers}


def verify_management_campaigns(manifest, paths):
    matrix = build.verify_build_campaigns(manifest, paths)
    inputs, missing, reports = dict(matrix['input_sha256']), list(matrix['unverified']), []
    for path in map(lambda value: Path(value).absolute(), paths):
        report = read_report(path)
        artifacts = path.with_name(path.stem + '.artifacts')
        retained = report.get('contract_inputs')
        if retained is None:
            missing.append({'compiler': report['go'], 'requirement': 'original canonical API inputs'})
            continue
        require(isinstance(retained, dict) and set(retained) == set(CONTRACT_FILES), 'canonical API inputs are incomplete')
        payloads = {}
        for name, record in retained.items():
            require(isinstance(record, dict), 'contract fingerprint is missing')
            original = build.browser_evidence.retained_path(path.parent, record.get('file'))
            require(original == artifacts / 'contracts' / Path(name).name, 'contract belongs to another recording')
            value, size = build.browser_evidence.hash_file(original, MAX_CONTRACT_BYTES)
            require(record.get('sha256') == value == manifest['source_files'].get(name, {}).get('sha256')
                    and type(record.get('bytes')) is int and record['bytes'] == size, 'contract identity or size differs')
            inputs[str(original)] = value
            with original.open('rb') as stream:
                data = stream.read(MAX_CONTRACT_BYTES + 1)
            require(len(data) == size and hashlib.sha256(data).hexdigest() == value, 'contract changed during inspection')
            payloads[name] = data
        with zipfile.ZipFile(artifacts / 'sdk.zip') as archive:
            prefix = build.sdk_evidence.CORE + '@' + report['sdk_module']['Version'] + '/'
            for name in SDK_CONTRACT_FILES:
                entry = archive.getinfo(prefix + name)
                require(entry.file_size <= MAX_CONTRACT_BYTES, 'SDK schema exceeds its byte limit')
                with archive.open(entry) as stream:
                    payloads['sdk/go/' + name] = stream.read(MAX_CONTRACT_BYTES + 1)
                require(len(payloads['sdk/go/' + name]) == entry.file_size, 'SDK schema content size differs')
                require(hashlib.sha256(payloads['sdk/go/' + name]).hexdigest() == manifest['source_files']['sdk/go/' + name]['sha256'],
                        'SDK schema changed during inspection')
        inventory = contract_inventory(payloads)
        variants = []
        for row, (_, _directory, module, _tags) in zip(report['variants'], build.VARIANTS):
            if row['name'].startswith('worker-'):
                continue
            required = required_contracts(row, module, inventory)
            result = contract_results(artifacts / (row['name'] + '-tests-stdout.log'), required,
                                      CLI_TESTS if row['name'].startswith('application-') else (), report['go'], row['tags'])
            missing.extend(dict(item, variant=row['name'], compiler=report['go']) for item in result['missing'])
            variants.append({'name': row['name'], **result})
        reports.append({'compiler': report['go'], 'operations': {'base': sum(not row['buildTag'] for row in inventory),
                                                                'external_enabled': len(inventory)}, 'variants': variants})
    for name, value in inputs.items():
        require(build.browser_evidence.hash_file(Path(name), build.MAX_LOG_BYTES)[0] == value, 'retained management input changed')
    return {'schema_version': 1, 'status': 'incomplete' if missing else 'consistent_pending_review',
            'scope': 'frozen Linux amd64 server, SDK wire, CLI and management lifecycle contract consistency',
            'release_identity': evidence_identity(manifest), 'release_gate_passed': False,
            'input_sha256': inputs, 'reports': reports, 'unverified': missing,
            'companion_requirements': ['browser', 'security', 'external_worker', 'accessibility', 'independent_review'],
            'remaining': ['Authenticate original executions and review API coverage against the approved contract.',
                          'SDK inventory fixtures prove transport coverage; the named server integration tests prove their respective scenarios.',
                          'CLI binaries are local test builds; packaged execution and other platforms require separate evidence.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--campaign', type=Path, action='append', required=True)
    args = parser.parse_args()
    result = verify_management_campaigns(read_report(args.release), args.campaign)
    print(encoded(result).decode(), end='')
    return 0 if result['status'] == 'consistent_pending_review' else 2


if __name__ == '__main__':
    raise SystemExit(main())
