"""Read retained CPRa OCI transports without extracting or fetching content."""
import gzip
import hashlib
import os
from pathlib import PurePosixPath
import re
import tarfile
import zlib

from evidence import decode_json, decode_report


ARCHIVE_LIMIT = 2 * 1024 ** 3
MEMBER_LIMIT = 4096
METADATA_LIMIT = 16 * 1024 ** 2
EXPANDED_LIMIT = 2 * 1024 ** 3
INDEX = 'application/vnd.oci.image.index.v1+json'
MANIFEST = 'application/vnd.oci.image.manifest.v1+json'
CONFIG = 'application/vnd.oci.image.config.v1+json'
LAYERS = {'application/vnd.oci.image.layer.v1.tar': False,
          'application/vnd.oci.image.layer.v1.tar+gzip': True}


def require(condition, detail):
    if not condition:
        raise ValueError('Invalid retained OCI transport: ' + detail)


def canonical(name):
    value = PurePosixPath(name)
    return (bool(name) and len(name.encode('utf-8')) <= 1024 and name != '.'
            and not value.is_absolute() and '..' not in value.parts
            and value.as_posix() == name and not any(c in name for c in ('\\', ':', '\x00')))


def copy_transport(source, destination):
    """Keep a private, exclusive copy and record the exact transport bytes."""
    require(not source.is_symlink() and source.is_file(), 'transport must be a regular file')
    size = source.stat().st_size
    require(0 < size <= ARCHIVE_LIMIT, 'transport exceeds 2 GiB or is empty')
    digest = hashlib.sha256()
    count = 0
    descriptor = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, 'wb') as writer, source.open('rb') as reader:
        while data := reader.read(1024 * 1024):
            count += len(data)
            require(count <= size, 'transport changed during retention')
            writer.write(data)
            digest.update(data)
        require(count == size, 'transport changed during retention')
        writer.flush()
        os.fsync(writer.fileno())
    return {'path': destination.name, 'sha256': digest.hexdigest(), 'size': count}


def payloads(archive):
    files, members, names = {}, {}, set()
    total = 0
    for member in archive:
        name = member.name
        require(canonical(name) and name not in names, 'noncanonical or duplicate member path')
        names.add(name)
        require(len(names) <= MEMBER_LIMIT, 'more than 4096 members')
        require(not member.mode & 0o7000, 'privileged member')
        if member.isdir():
            continue
        require(member.isfile() and not member.issparse() and not member.mode & 0o7000,
                'link, special file or privileged member')
        total += member.size
        require(0 <= member.size <= ARCHIVE_LIMIT and total <= ARCHIVE_LIMIT,
                'declared payload exceeds 2 GiB')
        digest = hashlib.sha256()
        count = 0
        with archive.extractfile(member) as stream:
            while data := stream.read(1024 * 1024):
                count += len(data)
                require(count <= member.size, 'member exceeds its declared size')
                digest.update(data)
        require(count == member.size, 'member is truncated')
        value = digest.hexdigest()
        if name.startswith('blobs/'):
            require(name == 'blobs/sha256/' + value, 'blob path does not match its content')
        files[name] = {'sha256': value, 'size': count}
        members[name] = member
    return files, members


def metadata_bytes(archive, members, name):
    require(name in members and 0 < members[name].size <= METADATA_LIMIT,
            'missing, empty or oversized metadata')
    with archive.extractfile(members[name]) as stream:
        data = stream.read(METADATA_LIMIT + 1)
    require(len(data) == members[name].size, 'metadata is truncated')
    return data


def transport_file(path):
    require(not path.is_symlink() and path.is_file(), 'transport must be a regular file')
    require(0 < path.stat().st_size <= ARCHIVE_LIMIT, 'transport exceeds 2 GiB or is empty')


def preflight(path):
    """Bound physical headers before tarfile decodes or accumulates extensions."""
    transport_file(path)
    extensions = {tarfile.XHDTYPE, tarfile.GNUTYPE_LONGNAME,
                  tarfile.GNUTYPE_LONGLINK, tarfile.SOLARIS_XHDTYPE}
    allowed = extensions | {tarfile.REGTYPE, tarfile.AREGTYPE, tarfile.DIRTYPE}
    extended = 0
    count = 0
    with path.open('rb') as stream:
        size = os.fstat(stream.fileno()).st_size
        while stream.tell() < size:
            header = stream.read(tarfile.BLOCKSIZE)
            if header == bytes(tarfile.BLOCKSIZE):
                break
            require(len(header) == tarfile.BLOCKSIZE, 'truncated tar header')
            member = tarfile.TarInfo.frombuf(header, 'utf-8', 'surrogateescape')
            count += 1
            require(count <= MEMBER_LIMIT, 'more than 4096 physical headers (including members)')
            require(member.type != tarfile.XGLTYPE, 'global tar metadata is outside the release recipe')
            require(member.type in allowed, 'link, special file or sparse outer member')
            require(0 <= member.size <= ARCHIVE_LIMIT, 'invalid declared payload size')
            end = stream.tell() + ((member.size + tarfile.BLOCKSIZE - 1)
                                   // tarfile.BLOCKSIZE) * tarfile.BLOCKSIZE
            require(end <= size, 'truncated declared tar payload')
            if member.type in extensions:
                extended += member.size
                require(extended <= METADATA_LIMIT, 'extended tar metadata exceeds 16 MiB')
                data = stream.read(member.size)
                require(not re.search(rb'(?:^|\n)[0-9]+ GNU\.sparse\.', data),
                        'sparse outer metadata is outside the release recipe')
            stream.seek(end)


def docker_config_digest(path, platform):
    """Bind reproduction to the config bytes in the saved runtime-test image."""
    preflight(path)
    with tarfile.open(path, mode='r:') as archive:
        files, members = payloads(archive)
        manifest = decode_json(metadata_bytes(archive, members, 'manifest.json'), 'manifest.json')
        require(isinstance(manifest, list) and len(manifest) == 1
                and isinstance(manifest[0], dict), 'reference must contain one image')
        name = manifest[0].get('Config')
        require(isinstance(name, str) and canonical(name) and name in files,
                'missing or noncanonical reference config')
        config = decode_report(metadata_bytes(archive, members, name), name)
        require((config.get('os'), config.get('architecture')) == tuple(platform.split('/')),
                'reference config platform mismatch')
        return 'sha256:' + files[name]['sha256']


def read_image(path, platform):
    """Verify the layout, descriptor graph, config and uncompressed layer hashes.

    CPRa exports one Linux platform, SHA-256 blobs and ordinary or gzip layers.
    Other OCI features require a separately reviewed release recipe and reader.
    """
    require(platform in ('linux/amd64', 'linux/arm64'), 'unsupported release platform')
    preflight(path)
    with tarfile.open(path, mode='r:') as archive:
        files, members = payloads(archive)

        def metadata(name):
            return decode_report(metadata_bytes(archive, members, name), name)

        def descriptor(value, types):
            require(isinstance(value, dict) and value.get('mediaType') in types,
                    'unsupported descriptor media type')
            digest = value.get('digest')
            require(isinstance(digest, str) and re.fullmatch(r'sha256:[a-f0-9]{64}', digest),
                    'invalid descriptor digest')
            name = 'blobs/sha256/' + digest.removeprefix('sha256:')
            size = value.get('size')
            require(type(size) is int and size > 0 and name in files
                    and files[name]['size'] == size, 'missing blob or descriptor size mismatch')
            require('data' not in value, 'inline descriptor data is outside the release recipe')
            observed = value.get('platform')
            if observed is not None:
                require(isinstance(observed, dict)
                        and (observed.get('os'), observed.get('architecture')) == tuple(platform.split('/')),
                        'descriptor platform mismatch')
            return name

        require(metadata('oci-layout').get('imageLayoutVersion') == '1.0.0', 'wrong layout version')
        index = metadata('index.json')
        visited = set()
        for depth in range(9):
            require(index.get('mediaType') in (None, INDEX), 'wrong index media type')
            require(index.get('schemaVersion') == 2 and type(index['schemaVersion']) is int,
                    'wrong index schema version')
            references = index.get('manifests')
            require(isinstance(references, list) and len(references) == 1,
                    'release requires exactly one platform image')
            reference = references[0]
            name = descriptor(reference, {INDEX, MANIFEST})
            require(name not in visited, 'descriptor cycle')
            visited.add(name)
            obj = metadata(name)
            require(obj.get('mediaType') in (None, reference['mediaType']),
                    'descriptor and document media types differ')
            if reference['mediaType'] == MANIFEST:
                manifest_name = name
                manifest = obj
                break
            require(depth < 8, 'descriptor nesting exceeds eight indexes')
            index = obj
        require(manifest.get('schemaVersion') == 2 and type(manifest['schemaVersion']) is int,
                'wrong manifest schema version')
        config_name = descriptor(manifest.get('config'), {CONFIG})
        config = metadata(config_name)
        require(config.get('os') == 'linux' and config.get('architecture') == platform.split('/')[1],
                'config platform mismatch')
        layers = manifest.get('layers')
        rootfs = config.get('rootfs')
        require(isinstance(layers, list) and 0 < len(layers) <= 128
                and isinstance(rootfs, dict) and rootfs.get('type') == 'layers',
                'missing layer or root filesystem metadata')
        expected = rootfs.get('diff_ids')
        require(isinstance(expected, list) and len(expected) == len(layers)
                and all(isinstance(value, str) and re.fullmatch(r'sha256:[a-f0-9]{64}', value)
                        for value in expected), 'invalid uncompressed layer digests')
        expanded = 0
        for layer, expected_digest in zip(layers, expected):
            name = descriptor(layer, LAYERS)
            digest = hashlib.sha256()
            with archive.extractfile(members[name]) as raw:
                stream = gzip.GzipFile(fileobj=raw) if LAYERS[layer['mediaType']] else raw
                try:
                    while data := stream.read(1024 * 1024):
                        expanded += len(data)
                        require(expanded <= EXPANDED_LIMIT, 'expanded layers exceed 2 GiB')
                        digest.update(data)
                except (OSError, EOFError, zlib.error) as error:
                    raise ValueError('Invalid retained OCI transport: corrupt compressed layer') from error
                finally:
                    if stream is not raw:
                        stream.close()
            require('sha256:' + digest.hexdigest() == expected_digest, 'uncompressed layer digest mismatch')
        return {'oci_payload': files, 'config_digest': 'sha256:' + files[config_name]['sha256'],
                'manifest_digest': 'sha256:' + files[manifest_name]['sha256'],
                'configuration': config, 'expanded_bytes': expanded}
