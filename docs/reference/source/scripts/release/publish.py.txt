#!/usr/bin/env python3
"""Require complete original private qualification before public operations.

The private workflow remains locked. Direct protected publication requires all
retained component readers and independent approval under an operator-pinned
trust policy. A stored ready summary or packaging evidence alone is insufficient;
public signature and readback verification still follows admission.
"""
import argparse
import hashlib
import tarfile
import zipfile
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import struct
import subprocess
import tempfile
import zlib
from release import ROOT, RECIPE, evidence_identity, sha, verify
from evidence import decode_report, read_report, verify_packaging_outcomes
import oci
import oci_assembly

REPOSITORY = 'ziad-hsn/cpra'
IDENTITY = 'https://github.com/ziad-hsn/cpra/.github/workflows/release.yml@refs/heads/main'
ISSUER = 'https://token.actions.githubusercontent.com'
ARCHIVE_LIMIT = 1024 * 1024 * 1024
ARCHIVE_MEMBER_LIMIT = 4096
ARCHIVE_METADATA_LIMIT = 16 * 1024 * 1024
ARCHIVE_PROGRAM_LIMIT = 512 * 1024 * 1024
ZIP_DIRECTORY_LIMIT = 4 * 1024 * 1024
PROGRESS_LIMIT = 8 * 1024 * 1024
PROGRESS_FILES = ('images.jsonl', 'modules.jsonl', 'image-tag.jsonl', 'releases.jsonl', 'chart.jsonl',
                  'image-signing.jsonl', 'image-referrers.jsonl', 'asset-signing.jsonl')


class TransferProgress:
    """Private append-only hints; remote bytes remain authoritative on resume."""
    def __init__(self, path, identity, objects):
        self.path, self.identity = Path(path).absolute(), identity
        self.objects = {(row['kind'], row['digest']): row for row in objects}
        self.states, self.records, self.receipts, self.descriptor = {}, [], {}, None

    def __enter__(self):
        if os.name != 'posix':
            raise ValueError('Publication progress requires the Linux release runner')
        import fcntl
        parent = self.path.parent
        mode = parent.stat()
        if (parent.resolve(strict=True) != parent or not parent.is_dir()
                or mode.st_uid != os.getuid() or stat.S_IMODE(mode.st_mode) & 0o077):
            raise ValueError('Publication progress requires a private owned directory')
        self.descriptor = os.open(self.path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
        try:
            mode = os.fstat(self.descriptor)
            if (not stat.S_ISREG(mode.st_mode) or mode.st_nlink != 1
                    or mode.st_uid != os.getuid() or stat.S_IMODE(mode.st_mode) & 0o077):
                raise ValueError('Publication progress must be a private, unaliased regular file')
            try:
                fcntl.flock(self.descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise ValueError('Publication progress is locked by another owner') from None
            data = os.read(self.descriptor, PROGRESS_LIMIT + 1)
            if len(data) > PROGRESS_LIMIT:
                raise ValueError('Publication progress exceeds its byte quota')
            if data:
                end = len(data) if data.endswith(b'\n') else data.rfind(b'\n') + 1
                lines = data[:end].splitlines()
                header = decode_report(lines[0], 'publication progress') if lines else {}
                if type(header.get('schema_version')) is not int or header != self.header():
                    raise ValueError('Publication progress belongs to a different source, graph or registry')
                for line in lines[1:]:
                    row = decode_report(line, 'publication progress')
                    if not isinstance(row.get('kind'), str) or not isinstance(row.get('digest'), str):
                        raise ValueError('Invalid publication progress identity')
                    key = (row.get('kind'), row.get('digest'))
                    if (set(row) not in ({'kind', 'digest', 'state'}, {'kind', 'digest', 'state', 'receipt'}) or key not in self.objects
                            or row['state'] not in ('started', 'verified')
                            or self.states.get(key) == 'verified'):
                        raise ValueError('Invalid publication progress transition')
                    if 'receipt' in row:
                        self.validate_receipt(row['state'], row['receipt'])
                        self.receipts[key] = row['receipt']
                    self.states[key] = row['state']
                    self.records.append(row)
                if end != len(data):
                    os.ftruncate(self.descriptor, end)
                    os.fsync(self.descriptor)
            else:
                self.append(self.header())
                directory = os.open(parent, os.O_RDONLY | os.O_DIRECTORY)
                try:
                    os.fsync(directory)
                finally:
                    os.close(directory)
            return self
        except BaseException:
            os.close(self.descriptor)
            self.descriptor = None
            raise

    def header(self):
        return {'schema_version': 1, 'identity': self.identity}

    def reserve(self, row, *, receipt=False):
        records = [dict(kind=row['kind'], digest=row['digest'], state=state)
                   for state in ('started', 'verified')]
        if receipt:
            records[-1]['receipt'] = {'id': 2**64 - 1}
        size = sum(len(release_bytes(record)) for record in records)
        if os.fstat(self.descriptor).st_size + size > PROGRESS_LIMIT:
            raise ValueError('Publication progress has no space for another verified transfer')

    def append(self, record):
        data = release_bytes(record)
        if os.fstat(self.descriptor).st_size + len(data) > PROGRESS_LIMIT:
            raise ValueError('Publication progress exceeds its byte quota')
        os.lseek(self.descriptor, 0, os.SEEK_END)
        while data:
            count = os.write(self.descriptor, data)
            if count <= 0:
                raise OSError('Publication progress write made no progress')
            data = data[count:]
        os.fsync(self.descriptor)

    @staticmethod
    def validate_receipt(state, receipt):
        if (state != 'verified' or not isinstance(receipt, dict) or set(receipt) != {'id'}
                or type(receipt['id']) is not int or not 0 < receipt['id'] < 2**64):
            raise ValueError('Invalid publication object receipt')

    def record(self, row, state, *, receipt=None):
        record = {'kind': row['kind'], 'digest': row['digest'], 'state': state}
        if receipt is not None:
            self.validate_receipt(state, receipt)
            record['receipt'] = dict(receipt)
        self.append(record)
        self.records.append(record)
        self.states[(row['kind'], row['digest'])] = state
        if receipt is not None:
            self.receipts[(row['kind'], row['digest'])] = dict(receipt)

    def __exit__(self, *_):
        os.close(self.descriptor)
        self.descriptor = None


def release_bytes(record):
    return (json.dumps(record, sort_keys=True, separators=(',', ':'), allow_nan=False) + '\n').encode()


def image_objects(directory, subjects):
    """Upload configs/layers, platform manifests, then the preserved index."""
    from registry import validate_object
    directory = Path(directory)
    blobs, manifests = {}, []
    def entry(kind, digest, size, media_type):
        row = {'kind': kind, 'digest': digest, 'size': size, 'media_type': media_type}
        validate_object(row)
        return row, directory / 'layout/blobs/sha256' / digest[7:]
    for arch in oci_assembly.PLATFORMS:
        digest = subjects['platforms'][arch]
        path = directory / 'layout/blobs/sha256' / digest[7:]
        manifest = read_report(path)
        manifests.append(entry('manifest', digest, path.stat().st_size, oci.MANIFEST))
        for descriptor in [manifest['config']] + manifest['layers']:
            row, path = entry('blob', descriptor['digest'], descriptor['size'], descriptor['mediaType'])
            key = row['digest']
            if key in blobs and blobs[key][0] != row:
                raise ValueError('Conflicting shared OCI descriptor')
            blobs[key] = (row, path)
    path = directory / 'layout/blobs/sha256' / subjects['digest'][7:]
    return ([blobs[digest] for digest in sorted(blobs)] + manifests
            + [entry('manifest', subjects['digest'], path.stat().st_size, oci.INDEX)])


def transfer_images(directory, reports, manifest, registry, oras, progress, work):
    """Used after qualification; does not publish tags, aliases or a release."""
    from qualification import Inputs
    if manifest.get('candidate') is not False or manifest.get('dirty') is not False:
        raise ValueError('OCI publication requires a clean frozen source')
    progress, work = Path(progress).absolute(), Path(work).absolute()
    mode = work.stat()
    if (work.resolve(strict=True) != work or not work.is_dir()
            or mode.st_uid != os.getuid() or stat.S_IMODE(mode.st_mode) & 0o077):
        raise ValueError('OCI transfer requires a private owned working directory')
    for source in (Path(directory).absolute(), Path(reports).absolute()):
        if progress == source or source in progress.parents or work == source or source in work.parents:
            raise ValueError('Publication working paths overlap retained OCI inputs')
    inputs = Inputs()
    subjects = oci_assembly.verify_assembly(directory, manifest, inputs, reports=reports)
    objects = image_objects(directory, subjects)
    identity = {'release_identity': evidence_identity(manifest), 'origin': registry.origin,
                'repository': registry.repository, 'subjects': subjects,
                'objects': [row for row, _ in objects]}
    uploaded, reused = 0, 0
    with TransferProgress(progress, identity, identity['objects']) as journal:
        for row, path in objects:
            inputs.unchanged()
            present = registry.verify(row)
            inputs.unchanged()
            prior = journal.states.get((row['kind'], row['digest']))
            if not present:
                if prior == 'verified':
                    raise ValueError('Previously verified registry object is missing; replacement is forbidden')
                journal.reserve(row)
                journal.record(row, 'started')
                registry.upload(oras, row, path, work)
                inputs.unchanged()
                if not registry.verify(row):
                    raise ValueError('OCI upload has no verified remote result; resume must recheck its outcome')
                uploaded += 1
            else:
                reused += 1
            inputs.unchanged()
            if prior != 'verified':
                journal.record(row, 'verified')
    inputs.unchanged()
    return {'subjects': subjects, 'objects_verified': len(objects), 'uploaded': uploaded,
            'reused': reused, 'release_gate_passed': False,
            'scope': 'exact OCI transfer and readback; no tag, alias or release publication'}


def tag_images(directory, reports, manifest, registry, oras, progress, work, *, preflight_only=False):
    """Bind the application version to the preserved index, with remote readback.

    This requires one protected publication owner. The registry protocol offers
    no conditional tag creation, so another publisher must not share ownership.
    """
    from module_publication import private_directory, marker
    from qualification import Inputs
    work = private_directory(work)
    progress = Path(progress).absolute()
    for source in (Path(directory).absolute(), Path(reports).absolute()):
        if progress.is_relative_to(source) or work.is_relative_to(source):
            raise ValueError('Publication working paths overlap retained OCI inputs')
    inputs = Inputs()
    subjects = oci_assembly.verify_assembly(directory, manifest, inputs, reports=reports)
    objects = image_objects(directory, subjects)
    row, path = objects[-1]
    identity = {'release_identity': evidence_identity(manifest), 'origin': registry.origin,
                'repository': registry.repository, 'subjects': subjects,
                'tag': manifest['version'], 'object': row}
    entry = marker('registry-tag', identity)
    with TransferProgress(progress, identity, [entry]) as journal:
        expected = [dict(kind=entry['kind'], digest=entry['digest'], state=state)
                    for state in ('started', 'verified')]
        if journal.records != expected[:len(journal.records)]:
            raise ValueError('Image-tag progress is out of order or duplicated')
        state = journal.states.get((entry['kind'], entry['digest']))
        present = registry.verify(row, tag=manifest['version'])
        inputs.unchanged()
        if present and state is None:
            raise ValueError('Image version already exists without original publication progress')
        if not present and state == 'verified':
            raise ValueError('Previously verified image version is missing; replacement is forbidden')
        if preflight_only:
            return {'subjects': subjects, 'tag': manifest['version'], 'available': not present,
                    'release_gate_passed': False, 'scope': 'version-name inspection only'}
        for object_row, _ in objects:
            if not registry.verify(object_row):
                raise ValueError('Image version requires the complete verified remote graph')
            inputs.unchanged()
        if not present:
            journal.reserve(entry)
            if state is None:
                journal.record(entry, 'started')
            inputs.unchanged()
            registry.upload(oras, row, path, work, tag=manifest['version'])
            inputs.unchanged()
            if not registry.verify(row, tag=manifest['version']):
                raise ValueError('Image version has no verified remote result; recheck before resuming')
        if state != 'verified':
            journal.record(entry, 'verified')
        inputs.unchanged()
    return {'subjects': subjects, 'tag': manifest['version'], 'reused': present,
            'release_gate_passed': False, 'scope': 'original index bytes at one application version'}


def progress_owner(manifest, run):
    """Validate authenticated workflow-run metadata before importing progress."""
    identity = evidence_identity(manifest)
    if not isinstance(run, dict):
        raise ValueError('Publication progress requires authenticated workflow-run metadata')
    repository = run.get('repository')
    head_repository = run.get('head_repository')
    if (not isinstance(repository, dict) or not isinstance(head_repository, dict)
            or type(repository.get('id')) is not int or repository['id'] <= 0
            or type(head_repository.get('id')) is not int
            or repository.get('id') != head_repository.get('id')
            or repository.get('full_name') != head_repository.get('full_name')
            or not isinstance(repository.get('full_name'), str)
            or not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository['full_name'])
            or type(run.get('id')) is not int or run['id'] <= 0
            or type(run.get('run_attempt')) is not int or run['run_attempt'] <= 0
            or type(run.get('workflow_id')) is not int or run['workflow_id'] <= 0
            or run.get('head_sha') != manifest['commit'] or run.get('head_branch') != 'main'
            or run.get('path') != '.github/workflows/release.yml'
            or run.get('event') != 'workflow_dispatch'):
        raise ValueError('Publication progress requires the same manually dispatched main workflow')
    return {'schema_version': 1, 'release_identity': identity,
            'repository': repository['full_name'], 'repository_id': repository['id'],
            'workflow_id': run['workflow_id'], 'run_id': run['id'], 'run_attempt': run['run_attempt']}


def prepare_progress(manifest, run, destination, *, incoming=None, previous_run=None):
    """Import only bounded source-bound journals; this performs no remote writes."""
    from module_publication import private_directory
    from qualification import Inputs
    owner = progress_owner(manifest, run)
    destination = Path(destination).absolute()
    private_directory(destination.parent)
    if destination.exists() or destination.is_symlink():
        raise ValueError('Publication progress destination already exists')
    payload = {}
    inputs = Inputs()
    if incoming is not None:
        if not isinstance(previous_run, dict):
            raise ValueError('Restored progress requires authenticated previous-run metadata')
        previous = progress_owner(manifest, previous_run)
        if (previous_run.get('status') != 'completed' or previous['run_id'] == owner['run_id']
                or any(previous[key] != owner[key]
                       for key in ('repository', 'repository_id', 'workflow_id', 'release_identity'))):
            raise ValueError('Restored progress does not belong to a completed matching workflow run')
        incoming = Path(incoming).absolute()
        if incoming.resolve(strict=True) != incoming or not incoming.is_dir():
            raise ValueError('Restored progress directory contains a link or alias')
        if destination.is_relative_to(incoming):
            raise ValueError('Publication progress destination overlaps its restored inputs')
        names = {path.name for path in incoming.iterdir()}
        if 'publication.json' not in names or not names <= {'publication.json', 'signed', 'image-signing', 'asset-signing', *PROGRESS_FILES}:
            raise ValueError('Restored progress contains missing or unexpected files')
        for name in sorted(names):
            path = incoming / name
            if name == 'asset-signing':
                import release_publication
                release_publication.asset_signing_cache(path, manifest, inputs)
                for entry in sorted(path.iterdir()):
                    if str(entry) not in inputs.files:
                        raise ValueError('Asset signing cache changed during restoration')
                    limit = (1024**2 if entry.name == 'identity.json' else 4096 if entry.name.endswith('.record.json')
                             else release_publication.SIGNING_LIMIT)
                    payload['asset-signing/'+entry.name] = release_publication.signing_bytes(entry, limit, inputs.files[str(entry)])
                continue
            if name == 'image-signing':
                import image_signing
                image_signing.inspect_cache(path, manifest, inputs)
                for entry in sorted(path.iterdir()):
                    if str(entry) not in inputs.files:
                        raise ValueError('Signing cache changed during restoration')
                    limit = (image_signing.BUNDLE_LIMIT if entry.name.endswith('.sigstore.json')
                             else 64 * 1024 if entry.name == 'identity.json' else 4096)
                    data = image_signing.bounded_file(entry, limit)
                    if hashlib.sha256(data).hexdigest() != inputs.files[str(entry)]:
                        raise ValueError('Signing cache changed during restoration')
                    payload['image-signing/'+entry.name] = data
                continue
            if name == 'signed':
                import release_publication
                release_publication.signing_cache(path, manifest, inputs)
                for filename in (*release_publication.SIGNED_FILES, release_publication.SIGNING_LEDGER):
                    entry = path/filename
                    limit = 1024**2 if filename == release_publication.SIGNING_LEDGER else release_publication.SIGNING_LIMIT
                    payload['signed/'+filename] = release_publication.signing_bytes(entry, limit, inputs.files[str(entry)])
                continue
            mode = path.lstat()
            if (not stat.S_ISREG(mode.st_mode) or mode.st_nlink != 1
                    or mode.st_size > (64 * 1024 if name == 'publication.json' else PROGRESS_LIMIT)):
                raise ValueError('Restored progress is linked, nonregular or oversized')
            inputs.remember(path)
            data = path.read_bytes()
            if name == 'publication.json':
                if decode_report(data, 'publication owner') != previous:
                    raise ValueError('Restored progress owner differs from authenticated workflow metadata')
            else:
                header = decode_report(data.split(b'\n', 1)[0], 'publication progress header')
                identity = header.get('identity')
                if (type(header.get('schema_version')) is not int or header['schema_version'] != 1
                        or not isinstance(identity, dict)
                        or identity.get('release_identity', identity.get('release')) != owner['release_identity']):
                    raise ValueError('Restored progress belongs to different release inputs')
                # Stage helpers validate the full journal and recheck remote
                # state under their lock before any mutation can occur.
                payload[name] = data
        inputs.directory(incoming)
    elif previous_run is not None:
        raise ValueError('Previous-run metadata requires its original progress artifact')
    inputs.unchanged()
    destination.mkdir(mode=0o700)
    try:
        payload['publication.json'] = release_bytes(owner)
        for name in ('signed', 'image-signing', 'asset-signing'):
            if any(path.startswith(name+'/') for path in payload):
                (destination/name).mkdir(mode=0o700)
        for name, data in payload.items():
            descriptor = os.open(destination / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, 'wb') as writer:
                writer.write(data)
                writer.flush()
                os.fsync(writer.fileno())
        for name in ('signed', 'image-signing', 'asset-signing'):
            if (destination/name).exists():
                descriptor = os.open(destination/name, os.O_RDONLY | os.O_DIRECTORY)
                try:
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
        descriptor = os.open(destination, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
        descriptor = os.open(destination.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
        inputs.unchanged()
    except BaseException:
        shutil.rmtree(destination)
        raise
    return owner


def run(*args, **kwargs):
    return subprocess.run([str(a) for a in args], check=True, **kwargs)


def zip_directory(path, target):
    """Bound the central directory before ZipFile allocates its member table.

    The recorded release recipe writes single-disk ZIPs without comments or
    ZIP64 records. Its member and payload limits require neither extension.
    """
    with path.open('rb') as stream:
        size = os.fstat(stream.fileno()).st_size
        if size < 22:
            raise ValueError('Invalid release archive: ' + target)
        stream.seek(-22, os.SEEK_END)
        marker, disk, directory_disk, disk_count, count, length, offset, comment = struct.unpack(
            '<4s4H2LH', stream.read(22))
    if (marker != b'PK\x05\x06' or disk != 0 or directory_disk != 0
            or disk_count != count or comment != 0 or offset + length != size - 22):
        raise ValueError('Archive ZIP layout differs from the release recipe: ' + target)
    if count > ARCHIVE_MEMBER_LIMIT or length > ZIP_DIRECTORY_LIMIT:
        raise ValueError('Archive ZIP directory exceeds its limits: ' + target)


def archive_programs(out, manifest, target):
    if target not in RECIPE['targets']:
        raise ValueError('Unsupported archive target: ' + target)
    name = f"cpra-{manifest['version']}-{target.replace('_', '-')}"
    suffix = '.exe' if target.startswith('windows_') else ''
    path = out/(name + ('.zip' if suffix else '.tar.gz'))
    if path.is_symlink() or not path.is_file() or path.stat().st_size > ARCHIVE_LIMIT:
        raise ValueError('Archive must be a regular file of at most 1 GiB: ' + target)
    programs = {program + suffix: program for program in RECIPE['binaries']}
    metadata = {}
    expected = {}
    names = set()
    total = 0

    def member(filename, size, regular, mode, open_member):
        nonlocal total
        canonical = PurePosixPath(filename)
        key = filename.casefold() if suffix else filename
        if (not filename or '\\' in filename or ':' in filename or '\x00' in filename
                or canonical.is_absolute() or '..' in canonical.parts
                or canonical.as_posix() != filename or filename == '.'
                or len(filename.encode('utf-8')) > 1024):
            raise ValueError('Archive contains a noncanonical path: ' + target)
        if key in names:
            raise ValueError('Archive contains a duplicate path: ' + target)
        names.add(key)
        if len(names) > ARCHIVE_MEMBER_LIMIT:
            raise ValueError('Archive contains more than 4096 members: ' + target)
        if not regular or mode & 0o7000:
            raise ValueError('Archive contains a link, special file or privileged mode: ' + target)
        total += size
        if size < 0 or total > ARCHIVE_LIMIT:
            raise ValueError('Archive payload exceeds 1 GiB: ' + target)
        if filename in ('RELEASE.json', 'DEPENDENCIES.json'):
            if not 0 < size <= ARCHIVE_METADATA_LIMIT:
                raise ValueError('Archive metadata must be nonempty and at most 16 MiB: ' + target)
            with open_member() as stream:
                data = stream.read(ARCHIVE_METADATA_LIMIT + 1)
            if len(data) != size:
                raise ValueError('Archive metadata size disagrees with its payload: ' + target)
            metadata[filename] = decode_report(data, filename)
        elif filename in programs:
            if not 0 < size <= ARCHIVE_PROGRAM_LIMIT or (not suffix and not mode & 0o111):
                raise ValueError('Archive executable is empty, oversized or lacks executable mode: ' + target)
            digest = hashlib.sha256()
            count = 0
            with open_member() as stream:
                while chunk := stream.read(1024 * 1024):
                    count += len(chunk)
                    if count > size:
                        raise ValueError('Archive executable exceeds its recorded size: ' + target)
                    digest.update(chunk)
            if count != size:
                raise ValueError('Archive executable size disagrees with its payload: ' + target)
            expected[programs[filename]] = digest.hexdigest()

    try:
        if suffix:
            zip_directory(path, target)
            with zipfile.ZipFile(path) as archive:
                for info in archive.infolist():
                    if info.orig_filename != info.filename:
                        raise ValueError('Archive contains a noncanonical path: ' + target)
                    mode = info.external_attr >> 16
                    regular = not info.is_dir() and stat.S_IFMT(mode) in (0, stat.S_IFREG)
                    if info.flag_bits & 1:
                        raise ValueError('Release archives must not be encrypted: ' + target)
                    member(info.filename, info.file_size, regular, mode,
                           lambda: archive.open(info))
        else:
            with tarfile.open(path, mode='r|gz') as archive:
                for info in archive:
                    member(info.name, info.size, info.isfile() and not info.issparse(), info.mode,
                           lambda: archive.extractfile(info))
    except (OSError, EOFError, tarfile.TarError, zipfile.BadZipFile, RuntimeError, zlib.error) as error:
        raise ValueError('Invalid release archive: ' + target) from error
    inventory = metadata.get('DEPENDENCIES.json', {})
    binaries = inventory.get('binaries')
    if (metadata.get('RELEASE.json') != manifest or inventory.get('target') != target
            or not isinstance(binaries, dict) or set(binaries) != set(programs)
            or set(expected) != set(programs.values())
            or any(expected[program] != binaries.get(program + suffix) for program in expected)):
        raise ValueError('Archive identity or executable inventory disagrees with final payload: ' + target)
    return expected


def packaging_reports():
    required = ['reproducibility.json', 'documentation.json']
    required += ['package-'+format+'-'+arch+'.json' for format in ('deb','rpm') for arch in ('amd64','arm64')]
    required += ['native-'+target+'.json' for target in RECIPE['targets']]
    required += ['go-install-'+target+'.json' for target in RECIPE['targets']]
    required += ['service-'+target+'.json' for target in RECIPE['targets']]
    required += ['service-user-'+target+'.json' for target in RECIPE['targets'] if not target.startswith('windows_')]
    required += ['container-'+arch+'.json' for arch in ('amd64', 'arm64')]
    required += ['image-repro-'+arch+'.json' for arch in ('amd64', 'arm64')]
    required += ['chart-helm3-to-4.json', 'chart-helm4.json']
    return required


def evidence(out, manifest):
    identity = evidence_identity(manifest)
    if manifest['recipe'] != RECIPE:
        raise ValueError('Evidence uses a different release recipe')
    required = packaging_reports()
    program_hashes = {}
    reports = {}
    for name in required:
        report = read_report(out/name)
        reports[name] = report
        if report.get('status') != 'pass':
            raise ValueError('Required release evidence did not pass: ' + name)
        if name.startswith(('native-', 'go-install-', 'service-', 'package-')) or name in ('reproducibility.json', 'documentation.json', 'package-deb.json', 'package-rpm.json'):
            if report.get('commit') != manifest['commit']:
                raise ValueError('Evidence belongs to a different source candidate: ' + name)
        if report.get('release_identity') != identity:
            raise ValueError('Evidence has a missing or mismatched release identity: ' + name)
        if 'version' in report and report['version'] != manifest['version']:
            raise ValueError('Evidence records a different application version: ' + name)
        if name.startswith('package-'):
            _, package_format, package_arch = name.removesuffix('.json').split('-')
            payload = out/f"cpra-{manifest['version']}-linux-{package_arch}.{package_format}"
            if report.get('format') != package_format or report.get('arch') != package_arch or report.get('package_sha256') != sha(payload):
                raise ValueError('Tested Linux package differs from final downloaded payload: ' + name)
            if report.get('source_candidate') is not False or report.get('source_dirty') is not False:
                raise ValueError('Package evidence was not recorded from a clean official candidate: ' + name)
        if name.startswith(('native-', 'service-')):
            prefix = 'service-user-' if name.startswith('service-user-') else ('service-' if name.startswith('service-') else 'native-')
            target = name.removeprefix(prefix).removesuffix('.json')
            if target not in program_hashes:
                program_hashes[target] = archive_programs(out, manifest, target)
            expected = program_hashes[target]
            if report.get('binary_sha256') != expected['cpra'] or report.get('cli_sha256') != expected['cpractl']:
                raise ValueError('Tested executables differ from final archive bytes: ' + name)
            if report.get('source_candidate') is not False or report.get('source_dirty') is not False:
                raise ValueError('Native evidence was not recorded from a clean official candidate: ' + name)
            goos, arch = target.split('_')
            if report.get('os') != {'linux':'Linux','darwin':'Darwin','windows':'Windows'}[goos] or report.get('architecture') not in ({'amd64':('x86_64','AMD64'), 'arm64':('aarch64','arm64','ARM64')}[arch]):
                raise ValueError('Native execution platform does not match the published target: ' + name)
        if name.startswith('service-') and report.get('execution_environment') != 'native-host':
            raise ValueError('Container/emulation does not establish native service support: ' + name)
        if name.startswith(('image-repro-', 'container-', 'chart-')) and report.get('source') != manifest:
            raise ValueError('Image/deployment evidence belongs to a different candidate: ' + name)
        if name.startswith('chart-') and report.get('csi_rwop_fencing') != 'pass':
            raise ValueError('Default RWOP storage was not exercised: ' + name)
    verify_packaging_outcomes(out, manifest, reports, program_hashes)
    return {'schema_version': 1, 'status': 'consistent_pending_review',
            'release_gate_passed': False, 'release_identity': identity,
            'reports': required, 'binary_sha256': program_hashes,
            'remaining': ['Independent packaging review and the complete private release gate.']}


def require_qualification(manifest, config_path, policy, policy_sha256, *, out, assembly, dependencies):
    """Never accept a ready summary instead of the original evidence readers."""
    from qualification import Inputs, configuration, publication_decision, require
    inputs = Inputs()
    config_path = Path(config_path).absolute()
    inputs.remember(config_path)
    config = read_report(config_path)
    paths = configuration(config, config_path.parent)
    expected = [('packaging', 'directory', out), ('packaging', 'assembly', assembly),
                ('sdk', 'dependencies', dependencies), ('publication_plan', 'dependencies', dependencies)]
    for component, field, path in expected:
        require(paths.get(component, {}).get(field) == Path(path).absolute(),
                'publication inputs differ from the qualified ' + component + '/' + field)
    inputs.remember(Path(out) / 'RELEASE.json')
    require(read_report(Path(out) / 'RELEASE.json') == manifest,
            'publication metadata differs from the selected frozen release')
    decision = publication_decision(manifest, config, config_path.parent, inputs=inputs,
                                    policy=policy, policy_sha256=policy_sha256)
    require(decision['prepublication_gate_passed'] is True,
            'Public publication is locked; incomplete gates: ' + ', '.join(decision['remaining']))
    inputs.unchanged()
    return inputs, decision


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, default=ROOT/'dist/release')
    parser.add_argument('--phase', choices=('prepare', 'capture-signing', 'restore-signing', 'images', 'release', 'image-tag', 'chart',
                                          'image-signatures', 'image-provenance-prepare', 'image-provenance-capture', 'image-attestations',
                                          'asset-provenance-prepare', 'asset-provenance-capture', 'asset-signature'), default='release')
    parser.add_argument('--cosign')
    parser.add_argument('--dependencies', type=Path)
    parser.add_argument('--go', type=Path)
    parser.add_argument('--progress-dir', type=Path)
    parser.add_argument('--work', type=Path)
    parser.add_argument('--image-assembly', type=Path)
    parser.add_argument('--oras', type=Path)
    parser.add_argument('--run-record', type=Path)
    parser.add_argument('--restore', type=Path)
    parser.add_argument('--previous-run-record', type=Path)
    parser.add_argument('--bundle', type=Path)
    parser.add_argument('--qualification', type=Path, help='original private component configuration, never a ready report')
    parser.add_argument('--review-policy', type=Path, help='operator trust pinned by the protected release environment')
    args = parser.parse_args()
    if args.phase == 'prepare':
        if args.run_record is None or args.progress_dir is None:
            parser.error('local progress preparation requires current-run metadata and a private destination')
        prepare_progress(read_report(args.out/'RELEASE.json'), read_report(args.run_record),
                         args.progress_dir, incoming=args.restore,
                         previous_run=read_report(args.previous_run_record) if args.previous_run_record else None)
        return
    if args.phase in ('capture-signing', 'restore-signing'):
        if args.progress_dir is None:
            parser.error('local signing preservation requires source-bound private progress')
        import release_publication
        operation = release_publication.capture_signing if args.phase == 'capture-signing' else release_publication.restore_signing
        result = operation(read_report(args.out/'RELEASE.json'), args.out, args.progress_dir)
        print(json.dumps(result, sort_keys=True))
        return
    if args.phase in ('asset-provenance-prepare', 'asset-provenance-capture'):
        if args.progress_dir is None or args.phase == 'asset-provenance-capture' and args.bundle is None:
            parser.error('local asset preservation requires source-bound progress and its original bundle')
        import release_publication
        signing = release_publication.AssetSigning(read_report(args.out/'RELEASE.json'), args.out, args.progress_dir)
        result = signing.prepare_provenance() if args.phase == 'asset-provenance-prepare' else signing.capture_provenance(args.bundle)
        print(json.dumps(result, sort_keys=True))
        return
    policy_sha256 = os.environ.get('CPRA_RELEASE_REVIEW_POLICY_SHA256')
    if args.qualification is None or args.review_policy is None or not policy_sha256:
        parser.error('Public publication is locked without original private qualification inputs and an operator-pinned review policy.')
    if any(value is None for value in (args.dependencies, args.go, args.progress_dir, args.work,
                                      args.cosign, args.image_assembly, args.oras)):
        parser.error('publication requires frozen dependencies, recorded tools, original image assembly and private progress/work directories')
    out = args.out.resolve()
    manifest = json.loads((out/'RELEASE.json').read_text()); version = manifest['version']
    if manifest['candidate'] or manifest['dirty']:
        parser.error('candidate/dirty builds cannot be published')
    if os.environ.get('GITHUB_REPOSITORY') != REPOSITORY or os.environ.get('GITHUB_REF') != 'refs/heads/main' or os.environ.get('GITHUB_EVENT_NAME') != 'workflow_dispatch':
        parser.error('publication requires the canonical manually dispatched main workflow')
    if os.environ.get('GITHUB_SHA') != manifest['commit']:
        parser.error('workflow commit does not match RELEASE.json')
    owner = read_report(args.progress_dir/'publication.json')
    if (owner.get('release_identity') != evidence_identity(manifest)
            or owner.get('repository') != REPOSITORY or type(owner.get('run_id')) is not int
            or str(owner['run_id']) != os.environ.get('GITHUB_RUN_ID')):
        parser.error('publication progress belongs to another release or workflow run')
    try:
        qualification_inputs, decision = require_qualification(manifest, args.qualification, args.review_policy,
            policy_sha256, out=out, assembly=args.image_assembly, dependencies=args.dependencies)
    except (OSError, ValueError, RuntimeError) as error:
        parser.error(str(error))
    verify(out); evidence(out, manifest)
    import registry as registry_client
    remote = registry_client.Registry('ziad-hsn/cpra', username=os.environ.get('GITHUB_ACTOR'),
                                      token=os.environ.get('GH_TOKEN'))
    planned = tag_images(args.image_assembly, out, manifest, remote, args.oras,
                         args.progress_dir/'image-tag.jsonl', args.work, preflight_only=True)
    if read_report(out/'OCI.json') != planned['subjects']:
        parser.error('distributed OCI subjects differ from the original assembly; nothing can be uploaded')
    import chart
    chart_remote = registry_client.Registry(chart.REPOSITORY, username=os.environ.get('GITHUB_ACTOR'),
                                            token=os.environ.get('GH_TOKEN'))
    chart_archive = out / ('cpra-chart-' + manifest['chart_version'] + '.tgz')
    chart.publish_chart(manifest, chart_archive, planned['subjects']['digest'], chart_remote, args.oras,
                        args.progress_dir/'chart.jsonl', args.work, preflight_only=True)
    token = os.environ.get('RELEASE_ADMIN_READ_TOKEN')
    if not token:
        parser.error('protected release environment requires RELEASE_ADMIN_READ_TOKEN to verify immutable-release settings')
    admin_env = dict(os.environ, GH_TOKEN=token)
    setting = json.loads(subprocess.check_output(['gh', 'api', 'repos/'+REPOSITORY+'/immutable-releases'], env=admin_env, text=True))
    if setting.get('enabled') is not True:
        parser.error('immutable releases must be enabled before stable-tag publication')
    qualification_inputs.unchanged()
    if args.phase == 'images':
        result = transfer_images(args.image_assembly, out, manifest, remote, args.oras,
                                 args.progress_dir/'images.jsonl', args.work)
        if read_report(out/'OCI.json') != result['subjects']:
            parser.error('distributed OCI subjects differ from the original transferred assembly')
        print(json.dumps(result, sort_keys=True))
        return
    import image_signing
    descriptors = {}
    for row, _ in image_objects(args.image_assembly, planned['subjects']):
        if not remote.verify(row):
            parser.error('image signing requires the complete original image graph')
        if row['kind'] == 'manifest':
            descriptors[row['digest']] = {'mediaType': row['media_type'], 'digest': row['digest'], 'size': row['size']}
    backend = image_signing.Cosign(args.cosign, remote, args.work)
    bundles = image_signing.Bundles(manifest, planned['subjects'], descriptors, args.progress_dir, backend)
    if args.phase in ('image-signatures', 'image-provenance-prepare', 'image-provenance-capture', 'image-attestations'):
        if args.phase == 'image-signatures':
            result = bundles.signatures()
        elif args.phase == 'image-provenance-prepare':
            result = bundles.prepare_provenance()
        elif args.phase == 'image-provenance-capture':
            if args.bundle is None:
                parser.error('capture requires the original attestation bundle')
            bundles.capture_provenance(args.bundle)
            result = {'captured': True, 'release_gate_passed': False}
        else:
            result = image_signing.publish_bundles(bundles, remote, args.oras, args.work)
        print(json.dumps(result, sort_keys=True))
        return
    # Independent invocations cannot skip signature generation/upload or trust
    # the previous workflow step's success. Promotion rechecks original bytes.
    image_signing.publish_bundles(bundles, remote, args.oras, args.work, verify_only=True)
    if args.phase == 'asset-signature':
        import release_publication
        result = release_publication.AssetSigning(manifest, out, args.progress_dir).sign_checksum(backend)
        print(json.dumps(result, sort_keys=True))
        return
    import release_publication
    release_publication.require(release_publication.AssetSigning(manifest, out, args.progress_dir).restore()['reused'],
                                'release promotion requires completed original asset-generation progress')
    names = [line.split('  ', 1)[1] for line in (out/'SHA256SUMS').read_text().splitlines()]
    names += ['SHA256SUMS', 'SHA256SUMS.sigstore.json', 'provenance.sigstore.json']

    def signature(directory):
        run(args.cosign, 'verify-blob', '--bundle', directory/'SHA256SUMS.sigstore.json',
            '--certificate-identity', IDENTITY, '--certificate-oidc-issuer', ISSUER, directory/'SHA256SUMS')

    def verify_assets(download):
        if (download/'SHA256SUMS').read_bytes() != (out/'SHA256SUMS').read_bytes():
            raise RuntimeError('Uploaded checksum manifest differs')
        verify(download); signature(download); evidence(download, manifest)
        for name in names[:-3]:
            run('gh', 'attestation', 'verify', download/name, '--repo', REPOSITORY,
                '--signer-workflow', REPOSITORY+'/.github/workflows/release.yml',
                '--cert-identity', IDENTITY, '--cert-oidc-issuer', ISSUER,
                '--source-digest', manifest['commit'], '--source-ref', 'refs/heads/main',
                '--bundle', download/'provenance.sigstore.json')

    if args.phase in ('image-tag', 'chart'):
        # A separate invocation cannot promote images based only on the local
        # step order: confirm the original tag, immutable release and all signed
        # downloaded assets again before creating its version reference.
        import release_publication
        client = release_publication.Releases(os.environ.get('GH_TOKEN'))
        release_publication.read_published(manifest, out, client, verify_assets, args.work)
        if args.phase == 'image-tag':
            result = tag_images(args.image_assembly, out, manifest, remote, args.oras,
                                args.progress_dir/'image-tag.jsonl', args.work)
        else:
            if planned['available']:
                parser.error('chart publication requires the original published application image version')
            for row, _ in image_objects(args.image_assembly, planned['subjects']):
                if not remote.verify(row):
                    parser.error('chart publication requires the complete original image graph')
            result = chart.publish_chart(manifest, chart_archive, planned['subjects']['digest'], chart_remote,
                                         args.oras, args.progress_dir/'chart.jsonl', args.work)
        print(json.dumps(result, sort_keys=True))
        return

    signature(out)
    # The first installable tag is created only after complete runtime, source
    # reconstruction, package, chart, documentation and signed read-back gates.
    import module_publication
    import release_publication
    client = release_publication.Releases(os.environ.get('GH_TOKEN'))
    def module_reader(module, module_version, work):
        return module_publication.public_download(args.go, module, module_version,
                                                  manifest['recipe']['go_version'], work)
    def modules():
        return module_publication.publish_modules(manifest, args.dependencies, client, module_reader,
                                                  args.progress_dir/'modules.jsonl', args.work)
    release_publication.capture_signing(manifest, out, args.progress_dir)
    result = release_publication.publish_release(manifest, out, client, modules, verify_assets,
                                                args.progress_dir/'releases.jsonl', args.work)
    print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    main()
