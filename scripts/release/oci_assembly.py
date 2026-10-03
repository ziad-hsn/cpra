#!/usr/bin/env python3
"""Assemble verified Linux image bytes privately; never load or publish them."""
import argparse
import hashlib
import io
import os
from pathlib import Path
import shutil
import tarfile

import evidence
import oci
import release


DEFAULT_LIMIT = 1536 * 1024 ** 2
PLATFORMS = ('amd64', 'arm64')


def require(condition, detail):
    if not condition:
        raise ValueError('Invalid private OCI assembly: ' + detail)


def regular(path, directory=False):
    path = Path(path).absolute()
    require(path.resolve(strict=True) == path, 'path contains a link or alias')
    require(path.is_dir() if directory else path.is_file(), 'expected a regular input')
    return path


def images(manifest, report_directory, inputs):
    """Re-read both independent builds and their retained runtime reference."""
    directory = regular(report_directory, directory=True)
    result = {}
    for arch in PLATFORMS:
        name = 'image-repro-' + arch + '.json'
        path = directory / name
        inputs.remember(path)
        report = evidence.read_report(path)
        require(report.get('status') == 'pass' and report.get('source') == manifest
                and report.get('release_identity') == release.evidence_identity(manifest),
                'image reproduction does not identify the frozen release')
        for suffix in ('-reference.docker.tar', '-build-0.oci.tar', '-build-1.oci.tar'):
            inputs.remember(directory / ('image-repro-' + arch + suffix))
        evidence.image_reproducibility(directory, report, name)
        transport = directory / ('image-repro-' + arch + '-build-0.oci.tar')
        result[arch] = {'path': transport, 'image': oci.read_image(transport, 'linux/' + arch)}
    return result


def graph(manifest, observed):
    """Calculate the exact index bytes and the deduplicated blob inventory."""
    blobs, owners, descriptors, platforms = {}, {}, [], {}
    for arch in PLATFORMS:
        entry = observed[arch]
        image = entry['image']
        for name, record in image['oci_payload'].items():
            if not name.startswith('blobs/'):
                continue
            require(name not in blobs or blobs[name] == record, 'conflicting shared blob')
            blobs[name] = record
            owners.setdefault(name, entry['path'])
        digest = image['manifest_digest']
        platforms[arch] = digest
        name = 'blobs/sha256/' + digest.removeprefix('sha256:')
        descriptors.append({'mediaType': oci.MANIFEST, 'digest': digest,
                            'size': blobs[name]['size'],
                            'platform': {'os': 'linux', 'architecture': arch}})
    index = release.encoded({'schemaVersion': 2, 'mediaType': oci.INDEX, 'manifests': descriptors})
    digest = 'sha256:' + hashlib.sha256(index).hexdigest()
    subjects = {'image': 'ghcr.io/ziad-hsn/cpra', 'digest': digest,
                'version': manifest['version'], 'commit': manifest['commit'], 'platforms': platforms}
    generated = {
        'OCI.json': release.encoded(subjects),
        'layout/oci-layout': release.encoded({'imageLayoutVersion': '1.0.0'}),
        'layout/blobs/sha256/' + digest.removeprefix('sha256:'): index,
        'layout/index.json': release.encoded({'schemaVersion': 2, 'mediaType': oci.INDEX, 'manifests': [
            {'mediaType': oci.INDEX, 'digest': digest, 'size': len(index),
             'annotations': {'org.opencontainers.image.ref.name': manifest['version']}}]}),
    }
    require('blobs/sha256/' + digest.removeprefix('sha256:') not in blobs,
            'index collides with an input blob')
    return subjects, generated, blobs, owners


def inventory(generated, blobs):
    result = {name: {'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)}
              for name, data in generated.items()}
    result.update({'layout/' + name: record for name, record in blobs.items()})
    return result


def write_stream(path, stream, record):
    """Write exclusively, bound the copy, verify it, and flush before closing."""
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    digest, count = hashlib.sha256(), 0
    with os.fdopen(descriptor, 'wb') as writer:
        while data := stream.read(1024 ** 2):
            count += len(data)
            require(count <= record['size'], 'source grew during copying')
            writer.write(data)
            digest.update(data)
        require(count == record['size'] and digest.hexdigest() == record['sha256'],
                'copied bytes differ from verified input')
        writer.flush()
        os.fsync(writer.fileno())


def output_inventory(directory):
    directory = regular(directory, directory=True)
    result = {}
    def failed(error):
        raise error
    seen = 0
    for root, directories, files in os.walk(directory, followlinks=False, onerror=failed):
        seen += len(directories) + len(files)
        require(seen <= 2 * oci.MEMBER_LIMIT + 12, 'too many output entries')
        for name in directories:
            path = regular(Path(root) / name, directory=True)
            require(path.relative_to(directory).as_posix() in ('layout', 'layout/blobs', 'layout/blobs/sha256'),
                    'unexpected output directory')
        for name in files:
            path = regular(Path(root) / name)
            relative = path.relative_to(directory).as_posix()
            require(len(result) < 2 * oci.MEMBER_LIMIT + 8, 'too many output files')
            result[relative] = {'sha256': release.sha(path), 'size': path.stat().st_size}
    return result


def report(manifest, release_path, directory, inputs, subjects, payload):
    retained = {'RELEASE.json': inputs.files[str(release_path)]}
    retained.update({Path(name).name: digest for name, digest in inputs.files.items()
                     if Path(name).parent == directory})
    return {'schema_version': 1, 'status': 'assembled',
            'evidence_class': 'private_oci_assembly', 'release_identity': release.evidence_identity(manifest),
            'source': manifest, 'inputs': retained, 'subjects': subjects, 'payload': payload,
            'release_gate_passed': False, 'publication_locked': True}


def assemble(release_path, report_directory, output, *, max_bytes=DEFAULT_LIMIT):
    # The assessment owns the shared input accounting contract. Import lazily
    # because its publication reader also consumes assembly records.
    from qualification import Inputs
    require(type(max_bytes) is int and max_bytes > 0, 'output quota must be a positive integer')
    release_path = regular(release_path)
    directory = regular(report_directory, directory=True)
    output = Path(output).absolute()
    require(not output.exists() and not output.is_symlink(), 'output already exists')
    require(output.parent.resolve(strict=True) == output.parent, 'output parent contains a link or alias')
    require(not output.is_relative_to(directory) and not directory.is_relative_to(output)
            and not release_path.is_relative_to(output), 'output overlaps retained inputs')
    inputs = Inputs()
    inputs.remember(release_path)
    manifest = evidence.read_report(release_path)
    release.evidence_identity(manifest)
    observed = images(manifest, directory, inputs)
    subjects, generated, blobs, owners = graph(manifest, observed)
    generated['RELEASE.json'] = release_path.read_bytes()
    payload = inventory(generated, blobs)
    record = report(manifest, release_path, directory, inputs, subjects, payload)
    record_bytes = release.encoded(record)
    require(sum(row['size'] for row in payload.values()) + len(record_bytes) <= max_bytes,
            'output exceeds the configured byte quota')
    inputs.unchanged()
    output.mkdir(mode=0o700)
    try:
        (output / 'layout').mkdir(mode=0o700)
        (output / 'layout/blobs').mkdir(mode=0o700)
        (output / 'layout/blobs/sha256').mkdir(mode=0o700)
        for name, data in generated.items():
            write_stream(output / name, io.BytesIO(data), payload[name])
        for source in sorted(set(owners.values())):
            oci.preflight(source)
            with tarfile.open(source, 'r:') as archive:
                members = {member.name: member for member in archive}
                for name in sorted(n for n in owners if owners[n] == source):
                    member = members[name]
                    require(member.isfile() and member.size == blobs[name]['size'], 'source member changed')
                    with archive.extractfile(member) as stream:
                        write_stream(output / 'layout' / name, stream, blobs[name])
        require(output_inventory(output) == payload, 'assembled payload is incomplete or changed')
        inputs.unchanged()
        write_stream(output / 'oci-assembly.json', io.BytesIO(record_bytes),
                     {'size': len(record_bytes), 'sha256': hashlib.sha256(record_bytes).hexdigest()})
        return record
    except BaseException:
        shutil.rmtree(output)
        raise


def verify_assembly(directory, manifest, inputs, *, reports):
    """Verify original inputs again and compare every assembled file byte."""
    directory = regular(directory, directory=True)
    path = directory / 'oci-assembly.json'
    inputs.remember(path)
    record = evidence.read_report(path)
    require(record.get('source') == manifest, 'assembly belongs to another frozen source')
    release_path = regular(directory / 'RELEASE.json')
    inputs.remember(release_path)
    require(evidence.read_report(release_path) == manifest, 'original release metadata changed')
    from qualification import Inputs
    original = Inputs()
    original.remember(release_path)
    report_directory = regular(reports, directory=True)
    observed = images(manifest, report_directory, original)
    subjects, generated, blobs, _ = graph(manifest, observed)
    generated['RELEASE.json'] = release_path.read_bytes()
    payload = inventory(generated, blobs)
    require(record == report(manifest, release_path, report_directory, original, subjects, payload),
            'assembly record differs from its original image transports')
    actual = output_inventory(directory)
    del actual['oci-assembly.json']
    require(actual == payload, 'assembled payload is incomplete or changed')
    inputs.merge(original.files)
    inputs.directory(directory)
    inputs.unchanged()
    return subjects


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', required=True, type=Path)
    parser.add_argument('--reports', required=True, type=Path)
    parser.add_argument('--out', required=True, type=Path)
    parser.add_argument('--max-bytes', type=int, default=DEFAULT_LIMIT)
    parser.add_argument('--verify', action='store_true', help='only re-read an existing assembly and its retained inputs')
    args = parser.parse_args()
    if args.verify:
        from qualification import Inputs
        verify_assembly(args.out, evidence.read_report(args.release), Inputs(), reports=args.reports)
    else:
        assemble(args.release, args.reports, args.out, max_bytes=args.max_bytes)


if __name__ == '__main__':
    main()
