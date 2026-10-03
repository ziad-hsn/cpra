#!/usr/bin/env python3
"""Inspect private release evidence without executing workloads or publishing.

Every configured component goes through its retained-input reader. An ordinary
assessment stays locked. A separate pre-publication decision requires all
readers and signed independent approval under an operator-pinned trust policy;
it does not establish successful public signing, downloads or publication.
"""
import argparse
import hashlib
import os
from pathlib import Path, PurePosixPath
import re
import stat
import sys
import tarfile
import zipfile
import zlib

import release
from evidence import read_report
import browser_evidence
import provider_evidence
import publish
import sdk_evidence
import worker_evidence
import build_evidence
import security_evidence
import management_evidence
import a2a_evidence
import accessibility_evidence
import storage_evidence
import review_evidence
import publication_evidence
import oci_assembly
import image_sbom

sys.path.insert(0, str(release.ROOT))
from scripts.benchmark import verify as benchmark_evidence


MAX_INPUT_BYTES = 2 * 1024 ** 3
MAX_TOTAL_BYTES = 64 * 1024 ** 3
MAX_INPUT_FILES = 100000
MAX_REPORT_BYTES = 16 * 1024 ** 2
EVIDENCE_ERRORS = (OSError, ValueError, KeyError, TypeError, RuntimeError,
                   EOFError, OverflowError, tarfile.TarError, zipfile.BadZipFile, zlib.error)
COMPONENTS = {
    'management': 'Frozen server/SDK/CLI management contracts and lifecycle checks.',
    'external_worker': 'Frozen worker protocol, encrypted journal, process-crash and load checks.',
    'a2a': 'Frozen A2A interoperability, escalation, persistence and independent effect checks.',
    'sdk': 'Both compilers consuming the frozen private SDK and worker archives.',
    'browser': 'Both compiler/tag variants and retained browser security scenarios.',
    'accessibility': 'Complete browser accessibility review of management and observation flows.',
    'security': 'Authentication, authorization, secret handling, vulnerabilities and build exclusion.',
    'build_matrix': 'Minimum/release compilers, module/tag/race/generation and dashboard checks.',
    'packaging': 'Reproducibility, six native targets, services, packages, OCI, Compose, Helm and docs.',
    'storage_upgrade': 'Actual older/newer binaries, format compatibility and stopped restore.',
    'providers': 'Independent live-account effects for every required provider role.',
    'benchmark': 'Matched comparisons and fault scenarios; no endurance run is required.',
    'publication_plan': 'Frozen dependency/artifact graph and reviewed signing, readback and resume procedure.',
    'independent_review': 'Authenticated independent review of the exact source and retained evidence.',
}
READERS = {'sdk', 'browser', 'accessibility', 'external_worker', 'build_matrix', 'security', 'management', 'a2a', 'packaging', 'storage_upgrade', 'providers', 'benchmark', 'independent_review', 'publication_plan'}
READER_FILES = (
    'scripts/release/qualification.py', 'scripts/release/release.py',
    'scripts/release/recipe.json', 'scripts/release/toolchains.json',
    'scripts/release/package.py', 'scripts/release/publish.py',
    'scripts/release/module_publication.py',
    'scripts/release/release_publication.py',
    'scripts/release/image_signing.py',
    'scripts/release/registry.py', 'scripts/release/evidence.py',
    'scripts/release/oci.py', 'scripts/release/dependencies.py',
    'scripts/release/go_install.py', 'scripts/release/sdk_evidence.py',
    'scripts/release/browser_evidence.py', 'scripts/release/provider_evidence.py',
    'scripts/release/worker_evidence.py', 'scripts/release/worker_campaign.py',
    'scripts/release/build_evidence.py', 'scripts/release/build_campaign.py',
    'scripts/release/security_evidence.py', 'scripts/release/verify_worker_exclusion.py',
    'scripts/release/management_evidence.py', 'scripts/release/a2a_evidence.py',
    'scripts/release/accessibility_evidence.py', 'scripts/release/browser_record.py',
    'scripts/release/storage_evidence.py', 'scripts/native/upgrade.py',
    'scripts/release/review_evidence.py', 'scripts/release/publication_evidence.py',
    'scripts/release/oci_assembly.py', 'scripts/release/chart.py',
    'scripts/release/image_sbom.py', 'scripts/release/sbom.py',
    'scripts/release/tools.json', 'LICENSES/dashboard.json',
    'scripts/sdk/module_archive.py', 'scripts/sdk/prepare_dependencies.py',
    'scripts/verification/summarize.py', 'scripts/benchmark/prepare.py',
    'scripts/benchmark/verify.py', 'scripts/benchmark/preflight.py',
)


def require(condition, detail):
    if not condition:
        raise ValueError('Invalid private qualification evidence: ' + detail)


def path_from(base, value):
    require(isinstance(value, str) and value and '\\' not in value
            and '\x00' not in value and '..' not in PurePosixPath(value).parts
            and str(PurePosixPath(value)) == value, 'expected a canonical local path')
    path = Path(value)
    return (path if path.is_absolute() else base / path).absolute()


def regular(path, directory=False):
    require(path.resolve(strict=True) == path, 'input path contains a link or alias: ' + str(path))
    mode = path.stat().st_mode
    require(stat.S_ISDIR(mode) if directory else stat.S_ISREG(mode),
            'input is not a regular ' + ('directory: ' if directory else 'file: ') + str(path))
    return path


class Inputs:
    """Record consumed bytes and reject changes across component inspection."""
    def __init__(self):
        self.files = {}
        self.total_bytes = 0

    def remember(self, path, expected=None):
        path = regular(Path(path).absolute())
        require(path.stat().st_size <= MAX_INPUT_BYTES, 'input exceeds 2 GiB: ' + path.name)
        value, count = hashlib.sha256(), 0
        with path.open('rb') as stream:
            while block := stream.read(1024 ** 2):
                count += len(block)
                require(count <= MAX_INPUT_BYTES, 'input exceeds 2 GiB: ' + path.name)
                value.update(block)
        value = value.hexdigest()
        name = str(path)
        require(expected is None or expected == value, 'retained input changed: ' + name)
        require(name not in self.files or self.files[name] == value, 'input changed during assessment: ' + name)
        if name not in self.files:
            require(len(self.files) < MAX_INPUT_FILES, 'too many retained inputs')
            require(self.total_bytes + count <= MAX_TOTAL_BYTES, 'retained inputs exceed 64 GiB')
            self.total_bytes += count
        self.files[name] = value
        return value

    def directory(self, path):
        path = regular(path, directory=True)
        seen = 0
        def failed(error):
            raise error

        for root, directories, files in os.walk(path, followlinks=False, onerror=failed):
            seen += len(directories) + len(files)
            require(seen <= MAX_INPUT_FILES, 'too many distribution entries')
            directories.sort()
            for name in directories:
                regular(Path(root) / name, directory=True)
            for name in sorted(files):
                self.remember(Path(root) / name)

    def merge(self, values):
        require(isinstance(values, dict), 'reader omitted retained input fingerprints')
        for name, value in values.items():
            require(isinstance(name, str) and Path(name).is_absolute()
                    and isinstance(value, str) and re.fullmatch('[a-f0-9]{64}', value),
                    'invalid retained input fingerprint')
            self.remember(Path(name), value)

    def unchanged(self):
        for name, value in list(self.files.items()):
            self.remember(Path(name), value)


def configuration(config, base):
    require(isinstance(config, dict) and set(config) == {'schema_version', 'components'}
            and type(config['schema_version']) is int and config['schema_version'] == 1,
            'unsupported assessment configuration')
    components = config['components']
    require(isinstance(components, dict) and set(components) <= COMPONENTS.keys(),
            'unknown component or ambiguous configuration')
    paths = {}
    for name, entry in components.items():
        if entry is None:
            continue
        require(name in READERS, name + ': no qualified reader is implemented')
        fields = {'sdk': {'dependencies', 'consumers'}, 'browser': {'campaigns'},
                  'accessibility': {'campaigns'},
                  'external_worker': {'campaigns'},
                  'build_matrix': {'campaigns'},
                  'security': {'campaigns'},
                  'management': {'campaigns'},
                  'a2a': {'campaigns'},
                  'storage_upgrade': {'campaigns', 'previous_release', 'previous_distribution'},
                  'independent_review': {'assessment', 'policy', 'records'},
                  'publication_plan': {'plan', 'dependencies'},
                  'packaging': {'directory'}, 'providers': {'build', 'reports'},
                  'benchmark': {'builds', 'comparison', 'faults'}}[name]
        valid_fields = ([fields, fields | {'build', 'reports'}] if name == 'a2a' else
                        [fields, fields | {'review'}] if name == 'accessibility' else
                        [fields, fields | {'assembly'}, fields | {'image_sboms'},
                         fields | {'assembly', 'image_sboms'}] if name == 'packaging' else [fields])
        require(isinstance(entry, dict) and set(entry) in valid_fields, name + ': invalid reader inputs')
        parsed = {}
        for field, value in entry.items():
            if field in ('consumers', 'campaigns', 'reports', 'records'):
                maximum = (review_evidence.MAX_REVIEWERS if field == 'records' else
                           len(release.RECIPE['targets']) if name == 'storage_upgrade' and field == 'campaigns' else
                           2 if field == 'campaigns' and name in ('external_worker', 'build_matrix', 'security', 'management', 'a2a') else
                           {'consumers': 2, 'campaigns': 4, 'reports': provider_evidence.MAX_REPORTS}[field])
                require(isinstance(value, list) and 0 < len(value) <= maximum, name + ': invalid report count')
                parsed[field] = [path_from(base, item) for item in value]
                require(len(set(parsed[field])) == len(parsed[field]), name + ': duplicate report path')
            else:
                parsed[field] = path_from(base, value)
        paths[name] = parsed
    return paths


def reader_source(manifest, inputs):
    require(manifest['recipe'] == release.RECIPE
            and manifest['toolchain_archives'] == release.TOOLCHAINS[release.RECIPE['go_version']],
            'release recipe or compiler pins differ from the readers')
    for name in READER_FILES:
        record = manifest['source_files'].get(name)
        require(isinstance(record, dict) and record.get('mode') in ('100644', '100755'),
                'frozen source omits the reader: ' + name)
        inputs.remember(release.ROOT / name, record['sha256'])


def stage_artifacts(artifacts, stage, inputs):
    """Keep raw job outputs separate and refuse ambiguous distribution bytes."""
    artifacts, stage = artifacts.absolute(), stage.absolute()
    regular(artifacts, directory=True)
    require(stage.resolve(strict=False) == stage, 'staging path contains a link or alias')
    require(not stage.is_relative_to(artifacts) and not artifacts.is_relative_to(stage),
            'assessment staging must be separate from downloaded artifacts')
    stage.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    require(stage.parent.resolve(strict=True) == stage.parent, 'staging parent contains a link or alias')
    stage.mkdir(mode=0o700, exist_ok=False)
    distribution = stage / 'distribution'
    distribution.mkdir(mode=0o700)
    copied = {}
    required = set(publish.packaging_reports())
    for directory in sorted(artifacts.iterdir()):
        regular(directory, directory=True)
        assets = directory.name in ('release-assets', 'private-oci-assembly', 'private-chart-package',
                                    'private-image-sboms')
        packaging = directory.name.startswith(('native-evidence-', 'package-evidence-',
                                               'container-evidence-', 'chart-evidence-', 'documentation-evidence-'))
        if not (assets or packaging):
            continue
        for path in sorted(directory.iterdir()):
            if directory.name == 'private-oci-assembly' and path.name not in ('RELEASE.json', 'OCI.json'):
                continue
            if directory.name == 'private-chart-package' and not re.fullmatch(r'cpra-chart-[A-Za-z0-9.+-]+\.tgz', path.name):
                continue
            if directory.name == 'private-image-sboms' and path.name not in (
                    'cpra-image-linux-amd64.spdx.json', 'cpra-image-linux-arm64.spdx.json'):
                continue
            if not assets and path.name not in required and not (
                    directory.name.startswith('container-evidence-') and path.name.endswith('.tar')):
                continue
            require(re.fullmatch(r'[A-Za-z0-9._+-]+', path.name) is not None, 'noncanonical distribution filename')
            value = inputs.remember(path)
            key = path.name.casefold()
            if key in copied:
                require(copied[key] == (path.name, value), 'conflicting downloaded distribution file: ' + path.name)
                continue
            copied[key] = (path.name, value)
            target = distribution / path.name
            with path.open('rb') as source, os.fdopen(os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'wb') as output:
                count = 0
                while block := source.read(1024 ** 2):
                    count += len(block)
                    require(count <= MAX_INPUT_BYTES, 'distribution file grew beyond its limit')
                    output.write(block)
                output.flush()
                os.fsync(output.fileno())
            inputs.remember(target, value)
    config = {'schema_version': 1, 'components': {
        'packaging': {'directory': str(distribution)},
        'sdk': {'dependencies': str(artifacts / 'dependency-preparation-build'),
                'consumers': [str(artifacts / ('sdk-consumer-' + version) / ('consumer-' + version + '.json'))
                              for version in (sdk_evidence.MINIMUM_COMPILER, release.RECIPE['go_version'])]},
        'external_worker': {'campaigns': [str(artifacts / ('worker-evidence-' + version) / ('worker-' + version + '.json'))
                                         for version in (sdk_evidence.MINIMUM_COMPILER, release.RECIPE['go_version'])]},
        'build_matrix': {'campaigns': [str(artifacts / ('build-evidence-' + version) / ('build-' + version + '.json'))
                                     for version in (sdk_evidence.MINIMUM_COMPILER, release.RECIPE['go_version'])]},
        'security': {'campaigns': [str(artifacts / ('build-evidence-' + version) / ('build-' + version + '.json'))
                                 for version in (sdk_evidence.MINIMUM_COMPILER, release.RECIPE['go_version'])]},
        'management': {'campaigns': [str(artifacts / ('build-evidence-' + version) / ('build-' + version + '.json'))
                                   for version in (sdk_evidence.MINIMUM_COMPILER, release.RECIPE['go_version'])]},
        'a2a': {'campaigns': [str(artifacts / ('build-evidence-' + version) / ('build-' + version + '.json'))
                            for version in (sdk_evidence.MINIMUM_COMPILER, release.RECIPE['go_version'])]},
        'browser': {'campaigns': [str(artifacts / ('browser-evidence-' + version + '-' + variant) /
                                    ('browser-' + version + '-' + variant + '.json'))
                                  for version in (sdk_evidence.MINIMUM_COMPILER, release.RECIPE['go_version'])
                                  for variant in ('default', 'externaljobs')]},
    }}
    assembly = artifacts / 'private-oci-assembly'
    if assembly.exists():
        config['components']['packaging']['assembly'] = str(assembly)
    image_inventories = artifacts / 'private-image-sboms'
    if image_inventories.exists():
        config['components']['packaging']['image_sboms'] = str(image_inventories)
    config['components']['accessibility'] = {'campaigns': list(config['components']['browser']['campaigns'])}
    review = artifacts / 'accessibility-review' / 'manual-review.json'
    if review.exists():
        config['components']['accessibility']['review'] = str(review)
    upgrade_jobs = sorted(path for path in artifacts.iterdir() if path.name.startswith('storage-upgrade-'))
    if upgrade_jobs:
        previous = stage / 'previous-distribution'
        previous.mkdir(mode=0o700)
        previous_files, campaigns = {}, []
        for directory in upgrade_jobs:
            regular(directory, directory=True)
            target = directory.name.removeprefix('storage-upgrade-')
            require(target in storage_evidence.SUPPORTED_TARGETS, 'unsupported storage-upgrade artifact target')
            campaigns.append(str(directory / ('upgrade-' + target + '.json')))
            original = regular(directory / 'previous', directory=True)
            metadata = read_report(regular(original / 'RELEASE.json'))
            release.evidence_identity(metadata)
            names = ('RELEASE.json', storage_evidence.archive_name(metadata, target))
            for name in names:
                path = original / name
                value = inputs.remember(path)
                if name in previous_files:
                    require(previous_files[name] == value, 'storage-upgrade jobs use different previous releases')
                    continue
                previous_files[name] = value
                destination = previous / name
                with path.open('rb') as source, destination.open('xb') as output:
                    os.chmod(destination, 0o600)
                    count = 0
                    while block := source.read(1024 ** 2):
                        count += len(block)
                        require(count <= MAX_INPUT_BYTES, 'previous distribution grew beyond its limit')
                        output.write(block)
                    output.flush()
                    os.fsync(output.fileno())
                inputs.remember(destination, value)
        config['components']['storage_upgrade'] = {
            'campaigns': campaigns, 'previous_release': str(previous / 'RELEASE.json'),
            'previous_distribution': str(previous),
        }
    config_path = stage / 'qualification.json'
    with config_path.open('xb') as output:
        output.write(release.encoded(config))
        output.flush()
        os.fsync(output.fileno())
    inputs.remember(config_path)
    inputs.unchanged()
    return config


def benchmark_source(manifest, entry, artifacts, inputs):
    """Match the tested controller to the final archive, not just its commit."""
    metadata_path = entry['builds'] / 'builds.json'
    source_path = entry['builds'] / 'candidate-source.json'
    inputs.remember(metadata_path)
    inputs.remember(source_path)
    metadata, source = read_report(metadata_path), read_report(source_path)
    require(metadata.get('release') == manifest and metadata.get('release_identity') == release.evidence_identity(manifest),
            'benchmark release metadata differs from the frozen release')
    benchmark_evidence.prepare.validate_source(manifest, source)
    require(metadata['recipe'] == manifest['recipe'] and metadata['tags'] == manifest['recipe']['build_tags'],
            'benchmark recipe or driver selection differs from the release')
    rows = {row['name']: row for row in metadata['builds']}
    require(rows['candidate']['source_date_epoch'] == manifest['source_date_epoch'],
            'benchmark source timestamp differs from the release')
    target = metadata['toolchain']['target']
    expected_hash = publish.archive_programs(artifacts, manifest, target)['cpra']
    require(rows['candidate']['sha256'] == expected_hash,
            'benchmark controller bytes differ from the final native archive')


def inspect_component(name, manifest, entry, paths, inputs, components=None):
    identity = release.evidence_identity(manifest)
    for field, value in entry.items():
        for path in value if isinstance(value, list) else [value]:
            regular(path, directory=field not in ('consumers', 'campaigns', 'reports', 'review', 'previous_release',
                                                 'assessment', 'policy', 'records', 'plan'))
    if name == 'sdk':
        result = sdk_evidence.verify_sdk_consumers(manifest, entry['dependencies'], entry['consumers'])
    elif name == 'browser':
        result = browser_evidence.verify_browser_campaigns(manifest, entry['campaigns'])
    elif name == 'accessibility':
        result = accessibility_evidence.verify_accessibility(manifest, entry['campaigns'], entry.get('review'))
    elif name == 'external_worker':
        result = worker_evidence.verify_worker_campaigns(manifest, entry['campaigns'])
    elif name == 'build_matrix':
        result = build_evidence.verify_build_campaigns(manifest, entry['campaigns'])
    elif name == 'security':
        result = security_evidence.verify_security_campaigns(manifest, entry['campaigns'])
    elif name == 'management':
        result = management_evidence.verify_management_campaigns(manifest, entry['campaigns'])
    elif name == 'a2a':
        result = a2a_evidence.verify_a2a_campaigns(manifest, entry['campaigns'], entry.get('build'), entry.get('reports', ()))
    elif name == 'providers':
        result = provider_evidence.verify_provider_reports(manifest, entry['build'], entry['reports'])
    elif name == 'storage_upgrade':
        require('packaging' in paths, 'storage upgrade requires the final distribution directory')
        result = storage_evidence.verify_upgrades(manifest, entry['previous_release'],
                                                  paths['packaging']['directory'],
                                                  entry['previous_distribution'], entry['campaigns'])
    elif name == 'publication_plan':
        require('packaging' in paths, 'publication procedure requires the final distribution directory')
        result = publication_evidence.verify_plan(manifest, entry['plan'], paths['packaging']['directory'],
                                                   entry['dependencies'], Inputs(), components=components)
    elif name == 'independent_review':
        require(components is not None, 'independent review requires current component outcomes')
        result = review_evidence.verify_reviews(manifest, entry['assessment'], entry['policy'],
                                                entry['records'], components, Inputs())
    elif name == 'packaging':
        metadata_path = entry['directory'] / 'RELEASE.json'
        inputs.remember(metadata_path)
        require(read_report(metadata_path) == manifest, 'distribution metadata differs from the selected release')
        inputs.directory(entry['directory'])
        result = publish.evidence(entry['directory'], manifest)
        if 'assembly' in entry:
            subjects = oci_assembly.verify_assembly(entry['assembly'], manifest, inputs,
                                                    reports=entry['directory'])
            require(read_report(entry['directory'] / 'OCI.json') == subjects,
                    'final image subjects differ from the private assembled bytes')
            result['oci_subjects'] = subjects
            if 'image_sboms' in entry:
                inventories = image_sbom.verify(entry['image_sboms'], manifest, inputs,
                                                assembly=entry['assembly'], reports=entry['directory'])
                for arch, value in inventories['inventory_sha256'].items():
                    inputs.remember(entry['directory'] / ('cpra-image-linux-' + arch + '.spdx.json'), value)
                result['image_sbom_sha256'] = inventories['inventory_sha256']
            else:
                result['status'] = 'incomplete'
                result['remaining'].append('Final image inventories and original scanner documents are missing.')
        else:
            result['status'] = 'incomplete'
            result['remaining'].append('Final private OCI layout and original image transports are missing.')
    else:
        require('packaging' in paths, 'benchmark requires the final distribution directory')
        artifacts = paths['packaging']['directory']
        benchmark_source(manifest, entry, artifacts, inputs)
        result = benchmark_evidence.verify(entry['builds'], entry['comparison'], entry['faults'],
                                           commit=manifest['commit'])
        require(result.get('candidate_commit') == manifest['commit'], 'benchmark candidate differs')
        roots = {key: entry[key if key != 'compare' else 'comparison']
                 for key in ('builds', 'compare', 'faults')}
        for key, values in result['inputs'].items():
            inputs.merge({str(roots[key] / filename): value for filename, value in values.items()})
        result['release_identity'] = identity
    require(result.get('release_identity') == identity, name + ': reader returned another release identity')
    require(result.get('status') in ('consistent_pending_review', 'incomplete')
            and result.get('release_gate_passed') is False, name + ': reader tried to authorize publication')
    if name in ('sdk', 'browser', 'accessibility', 'external_worker', 'build_matrix', 'security', 'management', 'a2a', 'providers', 'storage_upgrade', 'publication_plan', 'independent_review'):
        inputs.merge(result.get('input_sha256'))
    return result


def assess(manifest, config, base, inputs=None):
    inputs = inputs if inputs is not None else Inputs()
    identity = release.evidence_identity(manifest)
    paths = configuration(config, base)
    reader_source(manifest, inputs)
    components = {}
    for name, requirement in COMPONENTS.items():
        if name not in READERS:
            components[name] = {'status': 'not_implemented', 'requirement': requirement}
        elif name not in paths:
            components[name] = {'status': 'missing', 'requirement': requirement}
        else:
            try:
                components[name] = inspect_component(name, manifest, paths[name], paths, inputs, components)
            except FileNotFoundError as error:
                components[name] = {'status': 'missing', 'requirement': requirement, 'reason': str(error)}
            except EVIDENCE_ERRORS as error:
                components[name] = {'status': 'invalid_evidence', 'requirement': requirement, 'reason': str(error)}
    inputs.unchanged()
    return {'schema_version': 1, 'status': 'invalid_evidence' if any(
                row['status'] == 'invalid_evidence' for row in components.values()) else 'incomplete',
            'scope': 'private frozen-release evidence assessment', 'release_identity': identity,
            'release_gate_passed': False, 'publication_locked': True,
            'endurance': {'required': False, 'performed_by_this_assessment': False, 'claim_qualified': False},
            'components': components, 'input_sha256': inputs.files,
            'remaining': [name for name, row in components.items() if row['status'] != 'consistent_pending_review'],
            'review_required': ['Authenticate retained execution evidence and complete independent review.',
                                'Qualify the publisher and actual signature/readback execution before replacing publication locks.']}


def publication_decision(manifest, config, base, *, policy, policy_sha256, inputs=None):
    """Recheck original evidence and approval before a protected publisher acts."""
    inputs = inputs if inputs is not None else Inputs()
    require(isinstance(policy_sha256, str) and re.fullmatch('[a-f0-9]{64}', policy_sha256),
            'an operator trust policy pin is required')
    policy = Path(policy).absolute()
    paths = configuration(config, base)
    review = paths.get('independent_review')
    require(review is None or review['policy'] == policy,
            'candidate configuration cannot select another operator trust policy')
    require(inputs.remember(policy) == policy_sha256, 'operator trust policy changed')
    result = assess(manifest, config, base, inputs)
    ready = all(row['status'] == 'consistent_pending_review' for row in result['components'].values())
    result.update(
        status='ready_for_publication' if ready else result['status'],
        scope='private frozen-release publication decision',
        prepublication_gate_passed=ready,
        public_actions_performed=False,
        review_policy_sha256=policy_sha256,
        review_required=[] if ready else result['review_required'],
        postpublication_required=[
            'Verify official signatures and provenance against the expected workflow, issuer and digests.',
            'Verify public SDK and worker downloads without replacements or checksum exceptions.',
            'Verify uploaded assets, OCI objects and chart bytes before each dependent promotion.',
            'Verify immutable releases and documentation against the same frozen source.',
        ],
    )
    # This decision admits later protected actions; it neither executes them nor
    # opens the private workflow. Final release qualification remains separate.
    inputs.unchanged()
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument('--config', type=Path)
    source.add_argument('--artifacts', type=Path, help='separate directories from private workflow artifact downloads')
    parser.add_argument('--stage', type=Path, help='new directory required with --artifacts')
    parser.add_argument('--decision', action='store_true', help='require every reader and independent approval')
    parser.add_argument('--review-policy', type=Path, help='operator-owned trust policy outside candidate artifacts')
    parser.add_argument('--review-policy-sha256', help='operator-selected trust policy digest')
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    policy_options = (args.review_policy, args.review_policy_sha256)
    if args.decision and any(value is None for value in policy_options) \
            or not args.decision and any(value is not None for value in policy_options):
        parser.error('--decision requires --review-policy and --review-policy-sha256; assessment mode accepts neither')
    out, release_path = (path.absolute() for path in (args.out, args.release))
    require(out.resolve(strict=False) == out, 'output path contains a link or alias')
    config_path = args.config.absolute() if args.config is not None else None
    if (args.artifacts is not None) != (args.stage is not None):
        parser.error('--artifacts requires --stage; --stage cannot be used with --config')
    require(out not in (release_path, config_path), 'output cannot replace an input')
    require(args.review_policy is None or out != args.review_policy.absolute(),
            'output cannot replace the operator trust policy')
    if args.artifacts is not None and (out.is_relative_to(args.artifacts.absolute())
                                      or out.is_relative_to(args.stage.absolute())):
        parser.error('output must be outside raw artifacts and assessment staging')
    inputs = Inputs()
    try:
        inputs.remember(release_path)
        manifest = read_report(release_path)
        if args.artifacts is not None:
            release.evidence_identity(manifest)
            reader_source(manifest, inputs)
            config = stage_artifacts(args.artifacts, args.stage, inputs)
            base = args.stage.absolute()
        else:
            inputs.remember(config_path)
            config, base = read_report(config_path), config_path.parent
        paths = configuration(config, base)
        for entry in paths.values():
            for value in entry.values():
                for path in value if isinstance(value, list) else [value]:
                    if out == path or out.is_relative_to(path):
                        parser.error('output must be outside every retained input; no assessment was written')
        result = publication_decision(manifest, config, base, inputs=inputs,
                    policy=args.review_policy, policy_sha256=args.review_policy_sha256) if args.decision \
                 else assess(manifest, config, base, inputs)
    except EVIDENCE_ERRORS as error:
        result = {'schema_version': 1, 'status': 'invalid_evidence', 'release_gate_passed': False,
                  'publication_locked': True, 'reason': str(error), 'input_sha256': inputs.files}
    data = release.encoded(result)
    require(len(data) <= MAX_REPORT_BYTES, 'assessment exceeds 16 MiB')
    out.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    require(out.parent.resolve(strict=True) == out.parent, 'output parent contains a link or alias')
    with os.fdopen(os.open(out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'wb') as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())
    print(result['status'])
    return 1 if result['status'] == 'invalid_evidence' else 0 if result.get('prepublication_gate_passed') is True else 2


if __name__ == '__main__':
    raise SystemExit(main())
