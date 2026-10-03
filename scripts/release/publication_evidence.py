"""Inspect the frozen publication graph and stopped/resumed release procedure.

This validates a plan against retained bytes. It does not upload, sign, publish,
or treat a planned readback as an executed one. The existing publisher stays
locked until its complete implementation and actual execution are qualified.
"""
import argparse
import base64
import os
from pathlib import Path
import re

from evidence import decode_report, read_report
import publish
import release
import review_evidence
import sdk_evidence


CORE, WORKER = sdk_evidence.CORE, sdk_evidence.WORKER
APP = release.RECIPE['module']
STEPS = (
    'freeze-and-review', 'check-version-absence', 'sign-and-attest',
    'verify-private-readback', 'publish-sdk-tag', 'verify-public-sdk',
    'publish-worker-tag', 'verify-public-worker', 'publish-application-tag',
    'attach-verified-assets', 'publish-immutable-release',
    'publish-image-and-chart-aliases', 'verify-public-distributions', 'publish-matching-docs',
)
SIGNING = {'workflow_identity': publish.IDENTITY, 'oidc_issuer': publish.ISSUER,
           'repository': publish.REPOSITORY, 'source_ref': 'refs/heads/main',
           'checksum_bundle': 'SHA256SUMS.sigstore.json',
           'provenance_bundle': 'provenance.sigstore.json', 'trusted_root': 'trusted_root.jsonl',
           'verify_exact_subject_digests': True, 'offline_verification_required': True}
RESUME = {'same_source_and_artifacts_only': True, 'replace_published_objects': False,
          'verify_existing_objects_before_resume': True, 'on_mismatch': 'stop-and-use-new-version',
          'record_progress_privately': True}


def require(condition, detail):
    if not condition:
        raise ValueError('Invalid publication procedure evidence: ' + detail)


def named(path):
    require(isinstance(path, str) and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.+-]{0,255}', path),
            'expected a flat, canonical artifact filename')
    return path


def exact_policy(value, expected):
    return (isinstance(value, dict) and set(value) == set(expected)
            and all(type(value[name]) is type(wanted) and value[name] == wanted
                    for name, wanted in expected.items()))


def oci_subjects(directory, manifest):
    oci = read_report(Path(directory) / 'OCI.json')
    require(oci.get('image') == 'ghcr.io/ziad-hsn/cpra' and oci.get('version') == manifest['version']
            and oci.get('commit') == manifest['commit'] and isinstance(oci.get('platforms'), dict)
            and set(oci['platforms']) == {'amd64', 'arm64'}
            and all(isinstance(value, str) and re.fullmatch('sha256:[a-f0-9]{64}', value)
                    for value in [oci.get('digest'), *oci['platforms'].values()])
            and len({oci['digest'], *oci['platforms'].values()}) == 3,
            'final OCI subjects are incomplete or belong to another source')
    return oci


def artifact_inventory(directory, manifest, inputs):
    directory = Path(directory).absolute()
    require(directory.resolve(strict=True) == directory and directory.is_dir(),
            'distribution directory contains a link or alias')
    raw = review_evidence.content(directory / 'SHA256SUMS', inputs, 1 << 20)
    inventory = {}
    for line in raw.decode('utf-8').splitlines():
        match = re.fullmatch(r'([a-f0-9]{64})  (.+)', line)
        require(match is not None, 'malformed checksum inventory')
        digest, name = match.groups()
        named(name)
        require(name not in inventory and name != 'SHA256SUMS' and not name.endswith('.sigstore.json'),
                'duplicate, recursive, or signed checksum entry')
        require(len(inventory) < 1000, 'too many distribution assets')
        inputs.remember(directory / name, digest)
        inventory[name] = digest
    require(inventory, 'checksum inventory is empty')
    actual = set()
    for path in directory.iterdir():
        require(not path.is_symlink(), 'distribution contains a symlink')
        if path.is_file() and path.name != 'SHA256SUMS' and not path.name.endswith('.sigstore.json'):
            actual.add(path.name)
    require(set(inventory) == actual, 'checksum inventory omits or adds a distribution asset')
    version = manifest['version']
    required = {'RELEASE.json', 'OCI.json', 'trusted_root.jsonl',
                'cpra-' + version + '-source.tar.gz', 'cpra-chart-' + manifest['chart_version'] + '.tgz'}
    for target in release.RECIPE['targets']:
        required.add('cpra-' + version + '-' + target.replace('_', '-') +
                     ('.zip' if target.startswith('windows_') else '.tar.gz'))
        required.add('cpra-' + target + '.spdx.json')
    for arch in ('amd64', 'arm64'):
        required.update({'cpra-' + version + '-linux-' + arch + '.' + kind for kind in ('deb', 'rpm')})
        required.add('cpra-image-linux-' + arch + '.spdx.json')
    require(required <= inventory.keys(), 'final graph omits required archives, packages, SBOMs or deployment bytes')
    require(read_report(directory / 'RELEASE.json') == manifest, 'distribution belongs to another release')
    return inventory


def module_graph(directory, manifest, inputs):
    directory = Path(directory).absolute()
    require(directory.resolve(strict=True) == directory and directory.is_dir(),
            'dependency directory contains a link or alias')
    raw = review_evidence.content(directory / 'dependencies.json', inputs, 16 << 20)
    record = decode_report(raw, 'dependencies.json')
    require(type(record.get('schema_version')) is int and record['schema_version'] == 1
            and record.get('status') == 'pass'
            and record.get('evidence_class') == 'private_dependency_cache'
            and record.get('release_identity') == release.evidence_identity(manifest)
            and record.get('verification_environment') == manifest['recipe']['environment'],
            'module preparation differs from frozen source')
    modules, requirements, files = (record.get(name) for name in
                                   ('private_modules', 'requirements', 'private_cache_files'))
    require(isinstance(modules, dict) and set(modules) == {CORE, WORKER}
            and isinstance(requirements, dict) and set(requirements) == {APP, CORE, WORKER}
            and isinstance(files, dict), 'incomplete module graph')
    graph, expected_files = {}, set()
    for module, root in ((CORE, 'sdk/go'), (WORKER, 'sdk/go/worker')):
        row = modules[module]
        require(isinstance(row, dict) and row.get('Path') == module and release.valid_version(row.get('Version')),
                'invalid module version')
        version = row['Version']
        artifacts = {}
        for suffix in ('.info', '.mod', '.zip'):
            name = 'cache/download/' + module + '/@v/' + version + suffix
            expected_files.add(name)
            digest = files.get(name)
            require(isinstance(digest, str) and re.fullmatch('[a-f0-9]{64}', digest),
                    'module graph omits a retained payload')
            inputs.remember(directory / 'modules' / name, digest)
            artifacts[suffix] = digest
        archive = directory / 'modules/cache/download' / module / '@v' / (version + '.zip')
        require(artifacts['.zip'] == row.get('ArchiveSHA256'), 'module ZIP differs from recorded build input')
        sdk_evidence.archive_contents(archive, module, version, manifest)
        require(artifacts['.mod'] == manifest['source_files'][root + '/go.mod']['sha256'],
                'module declaration differs from frozen source')
        info_path = archive.with_suffix('.info')
        require(read_report(info_path) == {'Version': version, 'Time': manifest['source_date']},
                'module publication version or source time differs')
        for field in ('Sum', 'GoModSum'):
            value = row.get(field)
            require(isinstance(value, str) and value.startswith('h1:'), 'module checksum is missing')
            try:
                decoded = base64.b64decode(value[3:], validate=True)
            except ValueError:
                raise ValueError('Invalid publication procedure evidence: invalid module checksum') from None
            require(len(decoded) == 32, 'invalid module checksum size')
        graph[module] = {'version': version, 'tag': root + '/' + version,
                         'zip_sha256': artifacts['.zip'], 'mod_sha256': artifacts['.mod'],
                         'info_sha256': artifacts['.info'], 'sum': row['Sum'], 'go_mod_sum': row['GoModSum']}
    require(set(files) == expected_files, 'private cache inventory is ambiguous')
    edges = {}
    for module, values in requirements.items():
        require(isinstance(values, dict) and all(isinstance(path, str) and isinstance(version, str)
                                               for path, version in values.items()), 'invalid dependency edges')
        private = {path: version for path, version in values.items() if path == APP or path.startswith(APP + '/')}
        require(set(private) <= {CORE, WORKER}, 'module graph has an unexpected application dependency')
        require(all(version == graph[path]['version'] for path, version in private.items()),
                'dependent module pins another SDK or worker version')
        edges[module] = private
    require(not edges[CORE] and edges[WORKER] == {CORE: graph[CORE]['version']}
            and edges[APP].get(CORE) == graph[CORE]['version'], 'SDK/worker/application publication order is cyclic or inconsistent')
    graph[APP] = {'version': manifest['version'], 'tag': manifest['version']}
    for module in graph:
        graph[module]['requires'] = edges[module]
    return graph


def verify_plan(manifest, plan_path, directory, dependency_directory, inputs, *, components=None):
    identity = release.evidence_identity(manifest)
    raw = review_evidence.content(plan_path, inputs, 1 << 20)
    plan = decode_report(raw, Path(plan_path).name)
    require(set(plan) == {'schema_version', 'release_identity', 'repository', 'procedure',
                         'artifacts', 'modules', 'images', 'chart', 'signing', 'steps', 'resume'}
            and type(plan['schema_version']) is int and plan['schema_version'] == 1
            and plan['release_identity'] == identity and plan['repository'] == publish.REPOSITORY,
            'unsupported plan or another release identity')
    directory = Path(directory).absolute()
    require(not Path(plan_path).absolute().is_relative_to(directory),
            'plan must be outside its own checksummed distribution')
    procedure = plan['procedure']
    require(isinstance(procedure, dict) and set(procedure) == {'path', 'sha256'}
            and procedure['path'] == 'docs/release-engineering.md'
            and manifest['source_files'].get(procedure['path'], {}).get('sha256') == procedure['sha256'],
            'procedure is not part of the reviewed source')
    inputs.remember(release.ROOT / procedure['path'], procedure['sha256'])
    artifacts = artifact_inventory(directory, manifest, inputs)
    require(plan['artifacts'] == artifacts, 'planned artifacts differ from final distribution bytes')
    modules = module_graph(dependency_directory, manifest, inputs)
    require(plan['modules'] == modules, 'planned versions, module payloads, dependencies or nested tags differ')
    if components is not None and components.get('sdk', {}).get('status') == 'consistent_pending_review':
        require(components['sdk'].get('versions') == {name: modules[name]['version'] for name in (CORE, WORKER)},
                'planned SDK versions differ from the executed consumer campaigns')
    oci = oci_subjects(directory, manifest)
    require(plan['images'] == {'repository': oci['image'], 'index_digest': oci['digest'],
                               'platform_digests': oci['platforms']}, 'planned OCI signing subjects differ')
    require(plan['chart'] == {'repository': 'oci://ghcr.io/ziad-hsn/charts/cpra',
                              'version': manifest['chart_version'], 'image_digest': oci['digest'],
                              'archive': 'cpra-chart-' + manifest['chart_version'] + '.tgz'},
            'chart publication is not bound to the selected index and chart version')
    require(exact_policy(plan['signing'], SIGNING), 'signature verification policy omits expected identity, issuer, source or offline inputs')
    require(plan['steps'] == list(STEPS), 'publication order omits qualification, dependency readback, immutable release or documentation')
    require(exact_policy(plan['resume'], RESUME), 'resume could replace published objects or skip digest verification')
    inputs.unchanged()
    prerequisite = (components is not None and all(components.get(name, {}).get('status') == 'consistent_pending_review'
                                                   for name in ('sdk', 'packaging')))
    return {'schema_version': 1, 'status': 'consistent_pending_review' if prerequisite else 'incomplete',
            'release_identity': identity, 'release_gate_passed': False, 'publication_locked': True,
            'scope': 'frozen publication procedure and retained payload graph; no executed publication',
            'modules': modules, 'artifact_count': len(artifacts), 'signing': SIGNING,
            'publication_executed': False, 'public_downloads_verified': False,
            'publisher_implementation_qualified': False, 'input_sha256': dict(inputs.files),
            'remaining': ['Independent procedure review, qualified publisher implementation and actual signature/readback execution.']}


def prepare_plan(manifest, directory, dependency_directory, inputs):
    """Derive identities from retained inputs; never invent expected digests."""
    identity = release.evidence_identity(manifest)
    guide = 'docs/release-engineering.md'
    guide_record = manifest['source_files'].get(guide)
    require(isinstance(guide_record, dict) and guide_record.get('mode') in ('100644', '100755'),
            'frozen source omits the publication procedure')
    inputs.remember(release.ROOT / guide, guide_record['sha256'])
    artifacts = artifact_inventory(directory, manifest, inputs)
    modules = module_graph(dependency_directory, manifest, inputs)
    oci = oci_subjects(directory, manifest)
    return {'schema_version': 1, 'release_identity': identity, 'repository': publish.REPOSITORY,
            'procedure': {'path': guide, 'sha256': guide_record['sha256']},
            'artifacts': artifacts, 'modules': modules,
            'images': {'repository': oci['image'], 'index_digest': oci['digest'], 'platform_digests': oci['platforms']},
            'chart': {'repository': 'oci://ghcr.io/ziad-hsn/charts/cpra', 'version': manifest['chart_version'],
                      'image_digest': oci['digest'], 'archive': 'cpra-chart-' + manifest['chart_version'] + '.tgz'},
            'signing': dict(SIGNING), 'steps': list(STEPS), 'resume': dict(RESUME)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--distribution', type=Path, required=True)
    parser.add_argument('--dependencies', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True, help='new plan outside source and retained input directories')
    args = parser.parse_args()
    out = args.out.absolute()
    roots = [release.ROOT, args.distribution.absolute(), args.dependencies.absolute()]
    require(out.resolve(strict=False) == out and not any(out.is_relative_to(root) for root in roots)
            and out != args.release.absolute() and not out.exists(), 'plan output overlaps inputs or already exists')
    # Import only for the command-line collector; readers stay independent of
    # the aggregate module during import.
    from qualification import Inputs, reader_source
    inputs = Inputs()
    manifest = decode_report(review_evidence.content(args.release, inputs, 16 << 20), args.release.name)
    reader_source(manifest, inputs)
    plan = prepare_plan(manifest, args.distribution, args.dependencies, inputs)
    data = release.encoded(plan)
    require(len(data) <= 1 << 20, 'publication plan exceeds 1 MiB')
    inputs.unchanged()
    out.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    require(out.parent.resolve(strict=True) == out.parent, 'output parent contains a link or alias')
    with os.fdopen(os.open(out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'wb') as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())
    print('Private publication plan prepared; execution and publication remain unqualified.')


if __name__ == '__main__':
    main()
