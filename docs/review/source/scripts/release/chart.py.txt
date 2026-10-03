#!/usr/bin/env python3
"""Package frozen charts and publish their original bytes with verified recovery."""
import argparse
from datetime import datetime, timezone
import gzip
import hashlib
import io
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import yaml
import evidence
import oci
from oci_assembly import write_stream
from release import ROOT, checksums, encoded, evidence_identity, sha, valid_version
from package import archive


CHART_LIMIT = 16 * 1024 ** 2
CHART_METADATA_LIMIT = 64 * 1024
CONFIG_MEDIA = 'application/vnd.cncf.helm.config.v1+json'
LAYER_MEDIA = 'application/vnd.cncf.helm.chart.content.v1.tar+gzip'
REPOSITORY = 'ziad-hsn/charts/cpra'


def require(condition, detail):
    if not condition:
        raise ValueError('Invalid private chart package: ' + detail)


def source_inventory(manifest):
    root = ROOT / 'charts/cpra'
    require(root.resolve(strict=True) == root, 'chart source contains a link or alias')
    files, total, seen = {}, 0, 0
    def failed(error):
        raise error
    for current, directories, names in os.walk(root, followlinks=False, onerror=failed):
        seen += len(directories) + len(names)
        require(seen <= 4096, 'too many chart source entries')
        for name in directories:
            require(not (Path(current) / name).is_symlink(), 'chart source contains a link')
        for name in names:
            path = Path(current) / name
            require(not path.is_symlink() and path.is_file(), 'chart source contains a link or special file')
            total += path.stat().st_size
            require(total <= CHART_LIMIT, 'chart source exceeds its size limit')
            relative = path.relative_to(ROOT).as_posix()
            expected = manifest['source_files'].get(relative)
            require(isinstance(expected, dict) and expected.get('mode') == '100644'
                    and expected.get('sha256') == sha(path), 'chart source differs from frozen inventory')
            files[relative] = expected['sha256']
    expected = {name for name in manifest['source_files'] if name.startswith('charts/cpra/')}
    require(files and set(files) == expected, 'chart source inventory is incomplete')
    return files


def package_chart(manifest, output, helm, image_digest):
    identity = evidence_identity(manifest)
    require(isinstance(image_digest, str) and re.fullmatch(r'sha256:[a-f0-9]{64}', image_digest),
            'image digest must identify the assembled immutable OCI index')
    require(isinstance(manifest.get('chart_version'), str) and valid_version('v' + manifest['chart_version']),
            'chart version must be SemVer without the v prefix')
    epoch = manifest.get('source_date_epoch')
    require(type(epoch) is int and epoch >= 0, 'source epoch is invalid')
    output = Path(output).absolute()
    require(output.resolve(strict=True) == output and output.is_dir(), 'output contains a link or alias')
    target = output / ('cpra-chart-' + manifest['chart_version'] + '.tgz')
    require(not target.exists() and not target.is_symlink(), 'chart package already exists')
    sources = source_inventory(manifest)
    with tempfile.TemporaryDirectory(prefix='cpra-chart-release-') as temp:
        root = Path(temp)
        chart = root / 'cpra'
        shutil.copytree(ROOT / 'charts/cpra', chart)
        for name, digest in sources.items():
            require(sha(chart / Path(name).relative_to('charts/cpra')) == digest,
                    'staged chart differs from frozen source')
        require(source_inventory(manifest) == sources, 'chart source changed while staging')
        metadata = yaml.safe_load((chart / 'Chart.yaml').read_text())
        metadata.update(version=manifest['chart_version'], appVersion=manifest['version'])
        metadata.setdefault('annotations', {})['cpra.io/source-commit'] = manifest['commit']
        (chart / 'Chart.yaml').write_text(yaml.safe_dump(metadata, sort_keys=False))
        values = yaml.safe_load((chart / 'values.yaml').read_text())
        values['image'].update(tag='', digest=image_digest)
        (chart / 'values.yaml').write_text(yaml.safe_dump(values, sort_keys=False))
        fixture = ['--set', 'auth.existingSecret=release-test-v1', '--set', 'manifest.existingConfigMap=release-test-v1']
        subprocess.run([str(helm), 'lint', '--strict', str(chart), *fixture], check=True, timeout=60)
        subprocess.run([str(helm), 'package', str(chart), '--destination', str(root)], check=True, timeout=60)
        raw_package = root / ('cpra-' + manifest['chart_version'] + '.tgz')
        require(not raw_package.is_symlink() and raw_package.is_file()
                and raw_package.stat().st_size <= CHART_LIMIT, 'Helm package is missing or oversized')
        physical = root / 'helm-package.tar'
        expanded = 0
        with gzip.open(raw_package, 'rb') as reader, physical.open('xb') as writer:
            while data := reader.read(1024 ** 2):
                expanded += len(data)
                require(expanded <= 2 * CHART_LIMIT, 'physical chart archive exceeds its expansion limit')
                writer.write(data)
        oci.preflight(physical)
        entries, names, total = {}, set(), 0
        with tarfile.open(physical, 'r:') as stream:
            for member in stream:
                require(oci.canonical(member.name) and member.name.startswith('cpra/')
                        and not member.mode & 0o7000 and not member.issparse()
                        and member.name not in names and len(names) < 4096, 'unsafe or duplicate chart archive member')
                names.add(member.name)
                if member.isdir():
                    continue
                require(member.isfile(), 'linked or special chart member')
                total += member.size
                require(0 <= member.size <= CHART_LIMIT and total <= CHART_LIMIT,
                        'expanded chart exceeds its size limit')
                with stream.extractfile(member) as reader:
                    data = reader.read(member.size + 1)
                require(len(data) == member.size, 'truncated chart member')
                staged = chart / member.name.removeprefix('cpra/')
                require(staged.is_file() and not staged.is_symlink(), 'unexpected packaged file')
                if member.name not in ('cpra/Chart.yaml', 'cpra/values.yaml'):
                    require(sha(staged) == hashlib.sha256(data).hexdigest(), 'packaged file differs from staged chart')
                entries[member.name] = (data, False)
        require({'cpra/Chart.yaml', 'cpra/values.yaml', 'cpra/values.schema.json'} <= entries.keys(),
                'Helm omitted required chart inputs')
        require(yaml.safe_load(entries['cpra/Chart.yaml'][0]) == metadata
                and yaml.safe_load(entries['cpra/values.yaml'][0]) == values,
                'packaged chart differs from staged source or image identity')
        normalized = root / 'normalized.tgz'
        archive(normalized, entries, epoch)
        require(source_inventory(manifest) == sources, 'chart source changed during packaging')
        record = {'size': normalized.stat().st_size, 'sha256': sha(normalized)}
        with normalized.open('rb') as reader:
            try:
                write_stream(target, reader, record)
            except FileExistsError:
                raise
            except BaseException:
                if target.is_file() and not target.is_symlink():
                    target.unlink()
                raise
    return {'schema_version': 1, 'status': 'packaged', 'release_identity': identity,
            'image_digest': image_digest, 'chart_version': manifest['chart_version'],
            'archive': target.name, **record, 'source_sha256': sources,
            'release_gate_passed': False, 'publication_locked': True}


class ChartLoader(yaml.SafeLoader):
    """Bound metadata parsing and reject ambiguous mappings and alias graphs."""
    def __init__(self, stream):
        super().__init__(stream)
        self.depth, self.nodes = 0, 0

    def compose_node(self, parent, index):
        self.depth += 1
        self.nodes += 1
        try:
            require(self.depth <= 32 and self.nodes <= 4096
                    and not self.check_event(yaml.AliasEvent), 'chart metadata is too complex or contains aliases')
            return super().compose_node(parent, index)
        finally:
            self.depth -= 1

    def construct_mapping(self, node, deep=False):
        mapping = {}
        for key_node, value_node in node.value:
            key = self.construct_object(key_node, deep=deep)
            require(isinstance(key, str) and key not in mapping, 'chart metadata has a duplicate or non-string key')
            mapping[key] = self.construct_object(value_node, deep=deep)
        return mapping


def package_metadata(path, work):
    """Read bounded metadata without extracting or executing chart contents."""
    require(not path.is_symlink() and path.is_file() and 0 < path.stat().st_size <= CHART_LIMIT,
            'chart archive must be a nonempty regular file of at most 16 MiB')
    physical = work / 'chart.tar'
    expanded = 0
    with gzip.open(path, 'rb') as reader, physical.open('xb') as writer:
        while data := reader.read(1024 ** 2):
            expanded += len(data)
            require(expanded <= 2 * CHART_LIMIT, 'physical chart archive exceeds its expansion limit')
            writer.write(data)
    oci.preflight(physical)
    metadata, names, files, total = {}, set(), set(), 0
    with tarfile.open(physical, 'r:') as stream:
        for member in stream:
            require(oci.canonical(member.name) and member.name.startswith('cpra/')
                    and not member.mode & 0o7000 and not member.issparse()
                    and member.name not in names and len(names) < 4096, 'unsafe or duplicate chart archive member')
            names.add(member.name)
            if member.isdir():
                require(member.size == 0, 'chart directory has unexpected content')
                continue
            require(member.isfile(), 'linked or special chart member')
            files.add(member.name)
            total += member.size
            require(0 <= member.size <= CHART_LIMIT and total <= CHART_LIMIT, 'expanded chart exceeds its size limit')
            if member.name in ('cpra/Chart.yaml', 'cpra/values.yaml'):
                require(0 < member.size <= CHART_METADATA_LIMIT, 'chart metadata exceeds 64 KiB')
                with stream.extractfile(member) as reader:
                    data = reader.read(member.size + 1)
                require(len(data) == member.size, 'truncated chart metadata')
                value = yaml.load(data, Loader=ChartLoader)
                require(isinstance(value, dict), 'chart metadata must be a mapping')
                metadata[member.name] = value
    require({'cpra/Chart.yaml', 'cpra/values.yaml', 'cpra/values.schema.json'} <= files,
            'chart archive omits required inputs')
    return metadata['cpra/Chart.yaml'], metadata['cpra/values.yaml']


def publication_objects(manifest, path, image_digest, work, inputs):
    """Build a deterministic Helm OCI envelope around the tested archive."""
    from publish import release_bytes
    from registry import validate_object
    require(isinstance(manifest.get('chart_version'), str) and valid_version('v' + manifest['chart_version']),
            'chart version must be SemVer without the v prefix')
    require(isinstance(image_digest, str) and re.fullmatch(r'sha256:[a-f0-9]{64}', image_digest),
            'chart image must identify the original assembled index')
    require(type(manifest.get('source_date_epoch')) is int and manifest['source_date_epoch'] >= 0,
            'source epoch is invalid')
    source_date = datetime.fromtimestamp(manifest['source_date_epoch'], timezone.utc).isoformat().replace('+00:00', 'Z')
    require(manifest.get('source_date') == source_date, 'source date differs from its epoch')
    inputs.remember(path)
    metadata, values = package_metadata(path, work)
    require(metadata.get('name') == 'cpra' and metadata.get('apiVersion') == 'v2'
            and metadata.get('type') == 'application' and not metadata.get('dependencies')
            and metadata.get('version') == manifest['chart_version']
            and metadata.get('appVersion') == manifest['version']
            and isinstance(metadata.get('annotations'), dict)
            and metadata['annotations'].get('cpra.io/source-commit') == manifest['commit'],
            'chart identity differs from the frozen release')
    image = values.get('image')
    require(isinstance(image, dict) and image.get('repository') == 'ghcr.io/ziad-hsn/cpra'
            and image.get('digest') == image_digest and image.get('tag') == '',
            'chart image differs from the original assembled index')
    objects = []
    def object_file(kind, media_type, data=None):
        digest = inputs.files[str(path)] if data is None else hashlib.sha256(data).hexdigest()
        row = {'kind': kind, 'digest': 'sha256:' + digest,
               'size': path.stat().st_size if data is None else len(data), 'media_type': media_type}
        validate_object(row)
        target = path if data is None else work / digest
        if data is not None:
            write_stream(target, io.BytesIO(data), {'size': len(data), 'sha256': digest})
            inputs.remember(target, digest)
        objects.append((row, target))
        return {'mediaType': media_type, 'digest': row['digest'], 'size': row['size']}
    config_data = release_bytes(metadata)
    require(len(config_data) <= CHART_METADATA_LIMIT, 'encoded chart metadata exceeds 64 KiB')
    config = object_file('blob', CONFIG_MEDIA, config_data)
    layer = object_file('blob', LAYER_MEDIA)
    envelope = {'schemaVersion': 2, 'mediaType': oci.MANIFEST, 'config': config, 'layers': [layer],
                'annotations': {'org.opencontainers.image.title': 'cpra',
                                'org.opencontainers.image.version': manifest['chart_version'],
                                'org.opencontainers.image.created': source_date,
                                'org.opencontainers.image.revision': manifest['commit'],
                                'org.opencontainers.image.source': 'https://github.com/ziad-hsn/cpra'}}
    object_file('manifest', oci.MANIFEST, release_bytes(envelope))
    inputs.unchanged()
    return objects


def progress_order(journal, entries):
    offset = 0
    for entry in entries:
        started = False
        for state in ('started', 'verified'):
            record = {'kind': entry['kind'], 'digest': entry['digest'], 'state': state}
            if offset < len(journal.records) and journal.records[offset] == record:
                require(entry['kind'] != 'chart-tag' or state != 'verified' or started,
                        'chart version verification has no original started intent')
                started = started or state == 'started'
                offset += 1
            elif state == 'verified':
                require(offset == len(journal.records), 'chart publication progress is out of order')
                return
    require(offset == len(journal.records), 'chart publication progress is duplicated or out of order')


def version_state(journal, entry, remote, row, version):
    state = journal.states.get((entry['kind'], entry['digest']))
    present = remote.verify(row, tag=version)
    require(not present or state is not None, 'chart version exists without original publication progress')
    require(present or state != 'verified', 'previously verified chart version is missing; replacement is forbidden')
    return state, present


def publish_chart(manifest, path, image_digest, remote, oras, progress, work, *, preflight_only=False):
    """Resume exact chart bytes under one protected registry publication owner."""
    from module_publication import marker, private_directory
    from publish import TransferProgress
    from qualification import Inputs
    identity = evidence_identity(manifest)
    work = private_directory(work)
    path, progress = Path(path).absolute(), Path(progress).absolute()
    require(path.resolve(strict=True) == path and path not in (progress, work), 'publication paths overlap or contain aliases')
    require(remote.repository == REPOSITORY, 'use the canonical chart repository')
    inputs = Inputs()
    with tempfile.TemporaryDirectory(prefix='chart-publication-', dir=work) as temporary:
        objects = publication_objects(manifest, path, image_digest, Path(temporary), inputs)
        rows = [row for row, _ in objects]
        row, manifest_path = objects[-1]
        owner = {'release_identity': identity, 'origin': remote.origin, 'repository': remote.repository,
                 'chart_version': manifest['chart_version'], 'image_digest': image_digest, 'objects': rows}
        tag = marker('chart-tag', owner)
        with TransferProgress(progress, owner, rows + [tag]) as journal:
            progress_order(journal, rows + [tag])
            _, present = version_state(journal, tag, remote, row, manifest['chart_version'])
            inputs.unchanged()
            if preflight_only:
                for object_row, _ in objects:
                    if journal.states.get((object_row['kind'], object_row['digest'])) == 'verified':
                        require(remote.verify(object_row),
                                'previously verified chart object is missing; replacement is forbidden')
                        inputs.unchanged()
                if present:
                    require(remote.verify(row, tag=manifest['chart_version']), 'chart version changed during preflight')
                    inputs.unchanged()
                return {'chart_version': manifest['chart_version'], 'manifest_digest': row['digest'],
                        'available': not present, 'release_gate_passed': False, 'scope': 'chart version inspection only'}
            uploaded, reused = 0, 0
            for object_row, object_path in objects:
                present = remote.verify(object_row)
                inputs.unchanged()
                state = journal.states.get((object_row['kind'], object_row['digest']))
                require(present or state != 'verified', 'previously verified chart object is missing; replacement is forbidden')
                if state != 'verified':
                    journal.reserve(object_row)
                if not present:
                    if state is None:
                        journal.record(object_row, 'started')
                    remote.upload(oras, object_row, object_path, work)
                    inputs.unchanged()
                    require(remote.verify(object_row), 'chart upload has no verified remote result; recheck before resuming')
                    uploaded += 1
                else:
                    reused += 1
                inputs.unchanged()
                if state != 'verified':
                    journal.record(object_row, 'verified')
            state, present = version_state(journal, tag, remote, row, manifest['chart_version'])
            inputs.unchanged()
            if state != 'verified':
                journal.reserve(tag)
            if not present:
                if state is None:
                    journal.record(tag, 'started')
                remote.upload(oras, row, manifest_path, work, tag=manifest['chart_version'])
                inputs.unchanged()
                require(remote.verify(row, tag=manifest['chart_version']),
                        'chart version has no verified remote result; recheck before resuming')
            for object_row, _ in objects:
                require(remote.verify(object_row), 'chart version requires the complete verified remote graph')
                inputs.unchanged()
            require(remote.verify(row, tag=manifest['chart_version']), 'chart version changed during final readback')
            inputs.unchanged()
            if state != 'verified':
                journal.record(tag, 'verified')
            return {'status': 'verified', 'chart_version': manifest['chart_version'],
                    'archive_sha256': rows[1]['digest'][7:], 'manifest_digest': row['digest'],
                    'objects_verified': len(objects), 'uploaded': uploaded, 'reused': reused,
                    'tag_reused': present, 'release_gate_passed': False,
                    'scope': 'original Helm archive, deterministic OCI envelope and one chart version'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, default=ROOT / 'dist/release')
    parser.add_argument('--helm', required=True)
    parser.add_argument('--image-digest', required=True)
    parser.add_argument('--report', type=Path)
    args = parser.parse_args()
    if args.report is not None:
        require(args.report.parent.absolute().resolve(strict=True) == args.report.parent.absolute()
                and not args.report.exists() and not args.report.is_symlink(), 'report path exists or contains an alias')
    record = package_chart(evidence.read_report(args.out / 'RELEASE.json'), args.out, args.helm, args.image_digest)
    if args.report is not None:
        data = encoded(record)
        write_stream(args.report, io.BytesIO(data), {'size': len(data), 'sha256': hashlib.sha256(data).hexdigest()})
    checksums(args.out)


if __name__ == '__main__':
    main()
