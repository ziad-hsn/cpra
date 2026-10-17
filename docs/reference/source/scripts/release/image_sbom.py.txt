#!/usr/bin/env python3
"""Inventory exact private OCI bytes and retain the original scanner outputs."""
import argparse
import base64
import copy
from collections import Counter
import hashlib
import io
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time

import evidence
import oci_assembly
import release
from sbom import append_frontend


DOCUMENT_LIMIT = 16 * 1024 ** 2
DEFAULT_LIMIT = 512 * 1024 ** 2
ENTRY_LIMIT = 100000
SCAN_TIMEOUT = 120
CONFIG = b'check-for-app-update: false\nenrich: []\n'
SOURCE_FILES = ('scripts/release/image_sbom.py', 'scripts/release/sbom.py',
                'scripts/release/tools.json', 'LICENSES/dashboard.json')


def require(condition, detail):
    if not condition:
        raise ValueError('Invalid image SBOM: ' + detail)


def read_json(path):
    path = oci_assembly.regular(path)
    with path.open('rb') as stream:
        data = stream.read(DOCUMENT_LIMIT + 1)
    require(len(data) <= DOCUMENT_LIMIT, 'document exceeds 16 MiB')
    return evidence.decode_json(data, path.name)


def source_inputs(manifest, inputs):
    release.evidence_identity(manifest)
    for name in SOURCE_FILES:
        row = manifest['source_files'].get(name)
        require(isinstance(row, dict), 'frozen source omits ' + name)
        inputs.remember(release.ROOT / name, row['sha256'])
    pins = read_json(release.ROOT / 'scripts/release/tools.json')['syft']
    require(evidence.digest(pins.get('executable_sha256')), 'scanner executable has no reviewed pin')
    frontend = read_json(release.ROOT / 'LICENSES/dashboard.json')
    require(isinstance(frontend, list) and len(frontend) <= ENTRY_LIMIT,
            'invalid dashboard dependency inventory')
    return pins, frontend


def scan_documents(manifest, subjects, layout, arch, native, spdx, pin):
    """Bind both scanner formats to the selected manifest and configuration."""
    require(isinstance(native, dict) and isinstance(spdx, dict), 'scanner output is not an object')
    descriptor, source = native.get('descriptor'), native.get('source')
    require(isinstance(descriptor, dict) and descriptor.get('name') == 'syft'
            and descriptor.get('version') == pin['version'], 'wrong scanner version')
    require(isinstance(source, dict) and source.get('type') == 'image'
            and source.get('name') == subjects['image'] and source.get('version') == manifest['version'],
            'scanner source identity differs')
    metadata = source.get('metadata')
    require(isinstance(metadata, dict) and metadata.get('manifestDigest') == subjects['platforms'][arch],
            'scanner selected another platform manifest')
    raw = {}
    for field in ('manifest', 'config'):
        try:
            raw[field] = base64.b64decode(metadata.get(field, ''), validate=True)
        except (ValueError, TypeError) as error:
            raise ValueError('Invalid image SBOM: invalid encoded ' + field) from error
        require(raw[field], 'scanner omitted original ' + field + ' bytes')
    digest = lambda data: 'sha256:' + hashlib.sha256(data).hexdigest()
    require(digest(raw['manifest']) == subjects['platforms'][arch], 'original manifest digest differs')
    original = layout / 'blobs/sha256' / subjects['platforms'][arch][7:]
    require(original.read_bytes() == raw['manifest'], 'scanner manifest differs from assembled bytes')
    image = evidence.decode_report(raw['manifest'], arch + ' manifest')
    configuration = image['config']['digest']
    require(metadata.get('imageID') == configuration and digest(raw['config']) == configuration,
            'scanner configuration digest differs')
    require((layout / 'blobs/sha256' / configuration[7:]).read_bytes() == raw['config'],
            'scanner configuration differs from assembled bytes')
    config = evidence.decode_report(raw['config'], arch + ' configuration')
    require(config.get('os') == 'linux' and config.get('architecture') == arch,
            'scanner configuration has another architecture')
    require(metadata.get('os', '') in ('', 'linux') and metadata.get('architecture', '') in ('', arch),
            'scanner summary contradicts its original configuration')
    # Syft 1.51.1 leaves summary os/architecture empty for an OCI layout.
    # The retained configuration bytes, rather than those summaries, identify it.
    artifacts = native.get('artifacts')
    packages, relationships, files = spdx.get('packages'), spdx.get('relationships'), spdx.get('files', [])
    require(all(isinstance(rows, list) and len(rows) <= ENTRY_LIMIT
                and all(isinstance(row, dict) for row in rows)
                for rows in (artifacts, packages, relationships, files)),
            'unbounded or missing package inventory')
    creation = spdx.get('creationInfo')
    require(spdx.get('spdxVersion') == 'SPDX-2.3' and spdx.get('SPDXID') == 'SPDXRef-DOCUMENT'
            and spdx.get('dataLicense') == 'CC0-1.0' and isinstance(creation, dict)
            and 'Tool: syft-' + pin['version'] in creation.get('creators', []),
            'unexpected SPDX format or scanner identity')
    ids = [item.get('SPDXID') for item in packages + files]
    require(all(isinstance(value, str) and value.startswith('SPDXRef-') for value in ids)
            and len(set(ids)) == len(ids), 'missing or duplicate SPDX identity')
    roots = [p for p in packages if p.get('name') == subjects['image']
             and p.get('versionInfo') == manifest['version']]
    require(len(roots) == 1 and {'algorithm': 'SHA256', 'checksumValue': subjects['platforms'][arch][7:]}
            in roots[0].get('checksums', []), 'SPDX root does not identify the selected image')
    artifact_ids = [row.get('id') for row in artifacts]
    require(all(isinstance(value, str) and value for value in artifact_ids)
            and len(set(artifact_ids)) == len(artifact_ids), 'duplicate or missing native package identity')
    native_packages = Counter((row.get('name'), row.get('version') or '') for row in artifacts)
    spdx_packages = Counter((row.get('name'), row.get('versionInfo') or '')
                            for row in packages if row is not roots[0])
    require(native_packages == spdx_packages, 'scanner formats contain different package identities')
    elements = {*ids, spdx['SPDXID']}
    require(all(row.get('spdxElementId') in elements and row.get('relatedSpdxElement') in elements
                for row in relationships), 'SPDX relationship references an unknown element')


def normalized(manifest, arch, spdx, frontend):
    document = copy.deepcopy(spdx)
    document['creationInfo']['created'] = manifest['source_date']
    document['documentNamespace'] = ('https://github.com/ziad-hsn/cpra/spdx/'
                                     + manifest['commit'] + '/image/linux-' + arch)
    append_frontend(document, frontend)
    for name in ('packages', 'files', 'relationships'):
        if name in document:
            document[name] = sorted(document[name], key=release.encoded)
    ids = [p['SPDXID'] for p in document['packages']]
    require(len(ids) == len(set(ids)), 'dashboard inventory introduces duplicate package identities')
    data = release.encoded(document)
    require(len(data) <= DOCUMENT_LIMIT, 'normalized document exceeds 16 MiB')
    return data


def footprint(directory):
    total, count = 0, 0
    for root, directories, files in os.walk(directory, followlinks=False):
        count += len(directories) + len(files)
        require(count <= ENTRY_LIMIT, 'scanner output has too many entries')
        require(not any((Path(root) / name).is_symlink() for name in directories),
                'scanner output contains a linked directory')
        for name in files:
            path = Path(root) / name
            try:
                require(not path.is_symlink() and path.is_file(), 'scanner output contains an irregular file')
                total += path.stat().st_size
            except FileNotFoundError:
                # The scanner owns temporary files and may remove them during a scan.
                continue
    return total


def scan(executable, manifest, subjects, layout, arch, directory, max_bytes):
    """Use an explicit local source, isolated cache and bounded process lifetime."""
    with tempfile.TemporaryDirectory(prefix='scan-', dir=directory) as working:
        working = Path(working)
        environment = {key: os.environ[key] for key in ('PATH', 'LANG') if key in os.environ}
        environment.update(TMPDIR=str(working), XDG_CACHE_HOME=str(working / 'cache'), TZ='UTC')
        arguments = [str(executable), 'scan', 'oci-dir:' + str(layout), '--platform', 'linux/' + arch,
                     '--source-name', subjects['image'], '--source-version', manifest['version'],
                     '--config', str(directory / 'syft-config.yaml'),
                     '-o', 'syft-json=' + str(directory / (arch + '.syft.json')),
                     '-o', 'spdx-json=' + str(directory / (arch + '.spdx.json'))]
        log = directory / (arch + '.log')
        with log.open('xb') as output:
            process = subprocess.Popen(arguments, cwd=working, env=environment,
                                       stdout=output, stderr=subprocess.STDOUT)
            started = time.monotonic()
            try:
                while True:
                    require(footprint(directory) <= max_bytes, 'scanner exceeded the configured byte quota')
                    require(time.monotonic() - started <= SCAN_TIMEOUT, 'scanner deadline exceeded')
                    try:
                        code = process.wait(timeout=0.2)
                        break
                    except subprocess.TimeoutExpired:
                        pass
                require(code == 0, 'scanner failed for linux/' + arch)
                require(footprint(directory) <= max_bytes, 'scanner exceeded the configured byte quota')
                require(log.stat().st_size <= DOCUMENT_LIMIT, 'scanner log exceeds 16 MiB')
            finally:
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=2)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=5)


def write(path, data):
    oci_assembly.write_stream(path, io.BytesIO(data),
                              {'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)})


def record(manifest, subjects, pin, payload):
    return {'schema_version': 1, 'status': 'generated', 'source': manifest,
            'release_identity': release.evidence_identity(manifest), 'subjects': subjects,
            'scanner': {'name': 'syft', 'version': pin['version'],
                        'executable_sha256': pin['executable_sha256']},
            'payload': payload, 'release_gate_passed': False, 'publication_locked': True}


def verify(directory, manifest, inputs, *, assembly, reports):
    directory = oci_assembly.regular(directory, directory=True)
    pin, frontend = source_inputs(manifest, inputs)
    subjects = oci_assembly.verify_assembly(assembly, manifest, inputs, reports=reports)
    require(read_json(directory / 'RELEASE.json') == manifest, 'retained source identity differs')
    require((directory / 'dashboard-dependencies.json').read_bytes()
            == (release.ROOT / 'LICENSES/dashboard.json').read_bytes(), 'retained dashboard inventory differs')
    require((directory / 'syft-config.yaml').read_bytes() == CONFIG, 'scanner configuration differs')
    expected = {'RELEASE.json', 'dashboard-dependencies.json', 'syft-config.yaml', 'image-sbom.json'}
    for arch in oci_assembly.PLATFORMS:
        native, spdx = read_json(directory / (arch + '.syft.json')), read_json(directory / (arch + '.spdx.json'))
        scan_documents(manifest, subjects, Path(assembly) / 'layout', arch, native, spdx, pin)
        name = 'cpra-image-linux-' + arch + '.spdx.json'
        require((directory / name).read_bytes() == normalized(manifest, arch, spdx, frontend),
                'final inventory differs from the original scan')
        expected.update((arch + '.syft.json', arch + '.spdx.json', arch + '.log', name))
    require({p.name for p in directory.iterdir()} == expected, 'unexpected or missing retained files')
    payload = {}
    for name in sorted(expected - {'image-sbom.json'}):
        path = oci_assembly.regular(directory / name)
        require(path.stat().st_size <= DOCUMENT_LIMIT, 'retained document exceeds 16 MiB')
        payload[name] = {'sha256': inputs.remember(path), 'size': path.stat().st_size}
    inputs.remember(directory / 'image-sbom.json')
    require(read_json(directory / 'image-sbom.json') == record(manifest, subjects, pin, payload),
            'retained record differs from original documents')
    inputs.unchanged()
    return {'status': 'pass', 'subjects': subjects, 'release_gate_passed': False,
            'publication_locked': True,
            'inventory_sha256': {arch: payload['cpra-image-linux-' + arch + '.spdx.json']['sha256']
                                 for arch in oci_assembly.PLATFORMS}}


def generate(release_path, assembly, reports, output, executable, *, max_bytes=DEFAULT_LIMIT):
    from qualification import Inputs
    inputs = Inputs()
    release_path = oci_assembly.regular(release_path)
    inputs.remember(release_path)
    manifest = read_json(release_path)
    assembly = oci_assembly.regular(assembly, directory=True)
    reports = oci_assembly.regular(reports, directory=True)
    pin, frontend = source_inputs(manifest, inputs)
    subjects = oci_assembly.verify_assembly(assembly, manifest, inputs, reports=reports)
    executable = oci_assembly.regular(executable)
    inputs.remember(executable, pin['executable_sha256'])
    require(type(max_bytes) is int and max_bytes > 0, 'invalid byte quota')
    output = Path(output).absolute()
    require(output.resolve(strict=False) == output, 'output contains a link or alias')
    require(not output.exists(), 'output already exists')
    for path in (release_path, Path(assembly).absolute(), Path(reports).absolute(), executable):
        require(not path.is_relative_to(output) and not output.is_relative_to(path), 'output overlaps an input')
    require(output.parent.is_dir(), 'output parent is missing')
    inputs.unchanged()
    output.mkdir(mode=0o700)
    try:
        write(output / 'RELEASE.json', release_path.read_bytes())
        write(output / 'dashboard-dependencies.json', (release.ROOT / 'LICENSES/dashboard.json').read_bytes())
        write(output / 'syft-config.yaml', CONFIG)
        for arch in oci_assembly.PLATFORMS:
            scan(executable, manifest, subjects, Path(assembly) / 'layout', arch, output, max_bytes)
            native, spdx = read_json(output / (arch + '.syft.json')), read_json(output / (arch + '.spdx.json'))
            scan_documents(manifest, subjects, Path(assembly) / 'layout', arch, native, spdx, pin)
            write(output / ('cpra-image-linux-' + arch + '.spdx.json'), normalized(manifest, arch, spdx, frontend))
            require(footprint(output) <= max_bytes, 'output exceeded the configured byte quota')
        inputs.unchanged()
        payload = {p.name: {'sha256': release.sha(p), 'size': p.stat().st_size}
                   for p in sorted(output.iterdir())}
        result = record(manifest, subjects, pin, payload)
        write(output / 'image-sbom.json', release.encoded(result))
        require(footprint(output) <= max_bytes, 'record exceeded the configured byte quota')
        verify(output, manifest, inputs, assembly=assembly, reports=reports)
        return result
    except BaseException:
        shutil.rmtree(output)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', required=True, type=Path)
    parser.add_argument('--assembly', required=True, type=Path)
    parser.add_argument('--reports', required=True, type=Path)
    parser.add_argument('--out', required=True, type=Path)
    parser.add_argument('--syft', type=Path)
    parser.add_argument('--max-bytes', type=int, default=DEFAULT_LIMIT)
    parser.add_argument('--verify', action='store_true')
    args = parser.parse_args()
    if args.verify:
        from qualification import Inputs
        verify(args.out, read_json(args.release), Inputs(), assembly=args.assembly, reports=args.reports)
    else:
        if args.syft is None:
            parser.error('--syft is required when generating an inventory')
        generate(args.release, args.assembly, args.reports, args.out, args.syft, max_bytes=args.max_bytes)


if __name__ == '__main__':
    main()
