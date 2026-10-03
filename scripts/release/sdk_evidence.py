#!/usr/bin/env python3
"""Inspect retained private SDK consumer inputs for one frozen release.

This is an input-consistency check, not the aggregate publication gate. Server
contracts, generation, full module tests, independent review and later public
downloads must be qualified separately.
"""
import argparse
import base64
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import sys
import zipfile

from dependencies import CORE, WORKER, cache_environment
from evidence import read_report
from release import RECIPE, encoded, evidence_identity, valid_version

MAX_ARCHIVE_BYTES = 500 * 1024 ** 2
MAX_METADATA_BYTES = 16 * 1024 ** 2
MAX_ARCHIVE_FILES = 100000
MINIMUM_COMPILER = 'go1.25.0'
MODULE_ROOTS = {CORE: 'sdk/go', WORKER: 'sdk/go/worker'}
CHECKS = {
    'external consumer', 'independent default dependency graph',
    'custom API negative compilation', 'worker negative compilation',
    'tagged consumers', 'no workspace or replace', 'downloaded source identity',
    'packaged readmes and licenses', 'rendered Go package documentation',
    'executable examples from downloaded modules',
}
DOCUMENTED_PACKAGES = {(CORE + suffix, tags) for suffix in ('', '/api', '/collection')
                       for tags in ('', 'externaljobs')} | {(WORKER, 'externaljobs')}


def require(condition, detail):
    if not condition:
        raise ValueError('Incomplete or inconsistent SDK evidence: ' + detail)


def hash_file(path, limit):
    require(not path.is_symlink() and path.is_file(), 'input is not a regular file: ' + path.name)
    require(path.stat().st_size <= limit, 'input exceeds its byte limit: ' + path.name)
    digest = hashlib.sha256()
    size = 0
    with path.open('rb') as stream:
        while block := stream.read(64 * 1024):
            size += len(block)
            require(size <= limit, 'input grew beyond its byte limit: ' + path.name)
            digest.update(block)
    return digest.hexdigest(), size


def content_hash(value):
    if not isinstance(value, str) or not value.startswith('h1:'):
        return False
    try:
        return len(base64.b64decode(value[3:], validate=True)) == 32
    except ValueError:
        return False


def documentation_inputs(module):
    names = {'README.md', 'LICENSE', 'go.mod', 'go.sum', 'doc.go', 'example_test.go'}
    if module == CORE:
        names.update(package + '/' + name for package in ('api', 'collection')
                     for name in ('doc.go', 'example_test.go'))
    return names


def archive_contents(path, module, version, manifest):
    """Read bounded file digests without extracting a downloaded module."""
    prefix = module + '@' + version + '/'
    root = MODULE_ROOTS[module] + '/'
    files = {}
    folded = set()
    total = 0
    with zipfile.ZipFile(path) as archive:
        require(0 < len(archive.infolist()) <= MAX_ARCHIVE_FILES, 'archive file count is out of bounds')
        for entry in archive.infolist():
            require(entry.filename.startswith(prefix), 'archive has a different module or version prefix')
            name = entry.filename[len(prefix):]
            require(name and str(PurePosixPath(name)) == name and not PurePosixPath(name).is_absolute()
                    and '..' not in PurePosixPath(name).parts and '\\' not in name and ':' not in name,
                    'unsafe module archive path')
            require(not entry.is_dir() and stat.S_IFMT(entry.external_attr >> 16) in (0, stat.S_IFREG),
                    'archive contains an irregular file')
            require(name.casefold() not in folded, 'duplicate or case-colliding module archive path')
            folded.add(name.casefold())
            require(not (module == CORE and name.startswith('worker/')), 'SDK archive includes the nested worker module')
            require(name == 'go.mod' or PurePosixPath(name).name.lower() != 'go.mod',
                    'archive has an unexpected nested or case-variant go.mod')
            record = manifest['source_files'].get(root + name)
            require(isinstance(record, dict) and record.get('mode') in ('100644', '100755'),
                    'archive file is absent from frozen regular source: ' + name)
            limit = MAX_METADATA_BYTES if name in ('go.mod', 'LICENSE') else MAX_ARCHIVE_BYTES
            require(entry.file_size <= limit and total + entry.file_size <= MAX_ARCHIVE_BYTES,
                    'archive uncompressed byte limit exceeded')
            digest = hashlib.sha256()
            size = 0
            with archive.open(entry) as source:
                while block := source.read(64 * 1024):
                    size += len(block)
                    require(size <= limit and total + size <= MAX_ARCHIVE_BYTES,
                            'archive content exceeds its declared byte limit')
                    digest.update(block)
            require(size == entry.file_size, 'archive content size differs from its header')
            total += size
            require(digest.hexdigest() == record['sha256'], 'archive differs from frozen source: ' + name)
            files[name] = digest.hexdigest()
    require(documentation_inputs(module) <= files.keys(), 'archive is missing documentation or module inputs')
    expected = {name[len(root):] for name, record in manifest['source_files'].items()
                if name.startswith(root) and record['mode'] in ('100644', '100755')
                and not (module == CORE and name.startswith('sdk/go/worker/'))}
    require(files.keys() == expected, 'archive omitted or added frozen module files')
    return files


def verify_sdk_consumers(manifest, dependency_directory, paths):
    identity = evidence_identity(manifest)
    require(manifest['recipe'] == RECIPE, 'release uses a different build recipe')
    require(len(paths) == 2 and len(set(map(lambda p: Path(p).resolve(), paths))) == 2,
            'exactly two distinct compiler reports are required')
    dependency_directory = Path(dependency_directory).resolve()
    require(dependency_directory.is_dir(), 'private dependency preparation is unavailable')
    inputs = {}
    limits = {}

    def remember(path, limit=MAX_METADATA_BYTES):
        path = Path(path)
        digest, _ = hash_file(path, limit)
        key = str(path.absolute())
        require(key not in inputs or inputs[key] == digest, 'input changed while being inspected')
        inputs[key] = digest
        limits[key] = limit
        return digest

    dependency_path = dependency_directory / 'dependencies.json'
    remember(dependency_path)
    dependency = read_report(dependency_path)
    cache_environment(dependency_directory, manifest)
    archives = dependency.get('private_modules')
    require(isinstance(archives, dict) and set(archives) == {CORE, WORKER}, 'private module identities are incomplete')
    versions = {}
    for module, record in archives.items():
        require(isinstance(record, dict) and record.get('Path') == module
                and valid_version(record.get('Version')) and content_hash(record.get('Sum'))
                and content_hash(record.get('GoModSum')) and isinstance(record.get('Requires'), dict)
                and isinstance(record.get('ArchiveSHA256'), str)
                and re.fullmatch('[a-f0-9]{64}', record['ArchiveSHA256']) is not None,
                'invalid compiler-reported private module identity')
        versions[module] = record['Version']
    require(archives[WORKER]['Requires'].get(CORE) == versions[CORE], 'worker dependency selects another SDK version')
    requirements = dependency.get('requirements')
    require(isinstance(requirements, dict) and isinstance(requirements.get(RECIPE['module']), dict)
            and requirements[RECIPE['module']].get(CORE) == versions[CORE],
            'application dependency does not select the tested SDK version')
    canonical_contents = {}
    for name, expected in dependency['private_cache_files'].items():
        limit = MAX_ARCHIVE_BYTES if name.endswith('.zip') else MAX_METADATA_BYTES
        payload = dependency_directory / 'modules' / name
        require(remember(payload, limit) == expected,
                'private dependency payload changed')
    for module in (CORE, WORKER):
        payload = dependency_directory / 'modules/cache/download' / module / '@v' / (versions[module] + '.zip')
        require(inputs[str(payload.absolute())] == archives[module]['ArchiveSHA256'],
                'canonical dependency archive digest is inconsistent')
        canonical_contents[module] = archive_contents(payload, module, versions[module], manifest)

    reports = []
    compilers = set()
    for path in map(Path, paths):
        remember(path)
        report = read_report(path)
        require(report.get('status') == 'pass' and report.get('publicationReady') is False
                and report.get('evidenceClass') == 'local_module_archive'
                and report.get('candidateContentIdentityMatched') is True,
                'consumer did not complete private archive verification')
        require(report.get('sourceCommit') == manifest['commit'] and report.get('versions') == versions,
                'consumer selected a different source commit or version graph')
        compiler = re.fullmatch(r'go version (go[0-9.]+) ([a-z0-9]+/[a-z0-9]+)', str(report.get('compiler', '')))
        require(compiler is not None, 'consumer compiler identity is missing')
        require(compiler[1] not in compilers, 'duplicate compiler coverage')
        compilers.add(compiler[1])
        checks = report.get('checks')
        require(isinstance(checks, list) and all(isinstance(value, str) for value in checks)
                and len(checks) == len(set(checks)) and CHECKS <= set(checks), 'consumer checks are incomplete or duplicated')
        docs = report.get('packageDocumentation')
        require(isinstance(docs, list) and len(docs) == len(DOCUMENTED_PACKAGES), 'package documentation coverage is incomplete')
        observed = []
        for row in docs:
            require(isinstance(row, dict) and row.get('documentationRendered') is True
                    and row.get('examplesExecuted') is True, 'package documentation or examples did not pass')
            require(isinstance(row.get('package'), str) and isinstance(row.get('buildTags'), str),
                    'invalid package documentation identity')
            observed.append((row['package'], row['buildTags']))
        require(set(observed) == DOCUMENTED_PACKAGES, 'package or build-tag coverage is wrong')
        source_archives = report.get('moduleArchives')
        require(isinstance(source_archives, dict) and set(source_archives) == {CORE, WORKER},
                'consumer archive identities are incomplete')
        for module, record in source_archives.items():
            require(isinstance(record, dict) and all(record.get(key) == archives[module][key]
                    for key in ('Path', 'Version', 'Sum', 'GoModSum', 'Requires')),
                    'consumer content identities differ from dependency preparation')
            require(isinstance(record.get('ArchiveSHA256'), str)
                    and re.fullmatch('[a-f0-9]{64}', record['ArchiveSHA256']) is not None,
                    'consumer archive digest is missing or invalid')
        downloads = report.get('downloadedModules')
        require(isinstance(downloads, list) and len(downloads) == 2
                and all(isinstance(row, dict) and isinstance(row.get('Path'), str) for row in downloads),
                'downloaded module identities are missing')
        require({row.get('Path') for row in downloads} == {CORE, WORKER}, 'downloaded module identities are duplicated or unexpected')
        for row in downloads:
            require(all(row.get(key) == archives[row['Path']][key] for key in ('Path', 'Version', 'Sum', 'GoModSum')),
                    'downloaded module checksum or version differs from its source-derived identity')
        retained = report.get('retainedModules')
        require(isinstance(retained, dict) and set(retained) == {CORE, WORKER}, 'consumer module payloads were not retained')
        directory = path.with_name(path.name + '.modules')
        require(not directory.is_symlink() and directory.is_dir(), 'retained module directory is unavailable')
        documented = report.get('documentationArchiveInputs')
        require(isinstance(documented, dict) and set(documented) == {CORE, WORKER}, 'documentation input inventory is missing')
        for module, role in ((CORE, 'sdk'), (WORKER, 'worker')):
            require(documented[module] == sorted(documentation_inputs(module)), 'documentation input inventory differs from the archive contract')
            origins = retained[module]
            require(isinstance(origins, dict) and set(origins) == {'source', 'downloaded'}, 'retained archive origins are incomplete')
            content = {}
            for origin, fields in origins.items():
                require(isinstance(fields, dict) and set(fields) == {'Zip', 'GoMod', 'Info'}, 'retained payload inventory is incomplete')
                payloads = {}
                for field, suffix in (('Zip', '.zip'), ('GoMod', '.mod'), ('Info', '.info')):
                    record = fields[field]
                    name = role + '-' + origin + suffix
                    require(isinstance(record, dict) and record.get('file') == name and type(record.get('bytes')) is int,
                            'invalid retained module path or size')
                    payload = directory / name
                    limit = MAX_ARCHIVE_BYTES if field == 'Zip' else MAX_METADATA_BYTES
                    digest, size = hash_file(payload, limit)
                    require(digest == record.get('sha256') and size == record['bytes'], 'retained module bytes have changed')
                    remember(payload, limit)
                    payloads[field] = payload
                if origin == 'source':
                    require(fields['Zip']['sha256'] == source_archives[module]['ArchiveSHA256'],
                            'source ZIP differs from its recorded archive digest')
                content[origin] = archive_contents(payloads['Zip'], module, versions[module], manifest)
                require(fields['GoMod']['sha256'] == content[origin]['go.mod'], 'retained .mod differs from the archived source definition')
                info = read_report(payloads['Info'])
                require(info.get('Version') == versions[module] and isinstance(info.get('Time'), str)
                        and re.fullmatch(r'[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}'
                                         r'(?:\.[0-9]{1,9})?(?:Z|[+-][0-9]{2}:[0-9]{2})', info['Time']) is not None,
                        'module metadata version or timestamp is invalid')
                try:
                    parsed_time = datetime.fromisoformat(info['Time'].replace('Z', '+00:00'))
                except ValueError as error:
                    raise ValueError('Invalid SDK module source timestamp') from error
                require(parsed_time.utcoffset() is not None, 'module metadata timestamp lacks its offset')
                if origin == 'source':
                    require(info['Time'] == manifest['source_date'], 'private module metadata differs from frozen source time')
            require(content['source'] == content['downloaded'], 'downloaded content differs from the source-derived module')
            require(content['source'] == canonical_contents[module], 'consumer differs from canonical dependency contents')
        reports.append({'report': str(path.absolute()), 'sha256': inputs[str(path.absolute())],
                        'compiler': compiler[1], 'target': compiler[2]})
    require(compilers == {MINIMUM_COMPILER, manifest['recipe']['go_version']}, 'minimum or release compiler coverage is missing')
    for name, expected in inputs.items():
        require(hash_file(Path(name), limits[name])[0] == expected, 'input changed during SDK evidence inspection')
    return {'schema_version': 1, 'status': 'consistent_pending_review',
            'scope': 'private downloaded SDK/worker consumer inputs',
            'release_gate_passed': False, 'public_download_qualified': False,
            'release_identity': identity, 'versions': versions, 'reports': reports,
            'input_sha256': inputs,
            'remaining': ['Server interoperability and complete module/tag/security/generation checks.',
                          'Independent review and the other aggregate release gates.',
                          'Public downloaded-consumer verification after authorized publication.']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--dependencies', type=Path, required=True)
    parser.add_argument('--consumer', type=Path, action='append', required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with os.fdopen(os.open(args.out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'wb') as output:
        try:
            manifest = read_report(args.release)
            manifest_digest = hash_file(args.release, MAX_METADATA_BYTES)[0]
            report = verify_sdk_consumers(manifest, args.dependencies, args.consumer)
            require(hash_file(args.release, MAX_METADATA_BYTES)[0] == manifest_digest, 'release manifest changed during inspection')
            report['input_sha256'][str(args.release.absolute())] = manifest_digest
        except (ValueError, OSError, zipfile.BadZipFile, RuntimeError) as error:
            output.write(encoded({'schema_version': 1, 'status': 'invalid_evidence',
                                  'release_gate_passed': False, 'error': str(error)}))
            output.flush()
            os.fsync(output.fileno())
            print(str(error), file=sys.stderr)
            return 1
        output.write(encoded(report))
        output.flush()
        os.fsync(output.fileno())
    print(report['status'])
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
