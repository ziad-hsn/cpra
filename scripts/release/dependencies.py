#!/usr/bin/env python3
"""Prepare an isolated dependency cache for one frozen private release source."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess

from go_install import extract_release_source
from module_archive import CORE, WORKER, build_archiver, stage_module, tool_environment
from release import RECIPE, ROOT, encoded, evidence_identity, sha, valid_version

MODULES = {RECIPE['module']: '.', CORE: 'sdk/go', WORKER: 'sdk/go/worker'}
REPORT = 'dependencies.json'
MAX_OUTPUT = 32 * 1024 ** 2


def json_records(text):
    """Go emits consecutive JSON objects, rather than a JSON array."""
    if len(text.encode()) > MAX_OUTPUT:
        raise ValueError('Go dependency output exceeds the report limit')
    decoder = json.JSONDecoder()
    records = []
    position = 0
    while position < len(text):
        while position < len(text) and text[position].isspace():
            position += 1
        if position == len(text):
            break
        record, position = decoder.raw_decode(text, position)
        if not isinstance(record, dict) or record.get('Error'):
            raise ValueError('Go did not report a successful module record')
        records.append(record)
    return records


def go_output(go, arguments, *, cwd, env, out, label):
    output = out / (label + '.json')
    errors = out / (label + '.log')
    with output.open('x') as stdout, errors.open('x') as stderr:
        subprocess.run([str(go), *arguments], cwd=cwd, env=env, stdout=stdout,
                       stderr=stderr, check=True, timeout=900)
    if output.stat().st_size > MAX_OUTPUT:
        raise ValueError('Go dependency output exceeds the report limit')
    return output.read_text()


def compiler_environment(go, out, manifest, *, private=False):
    env = tool_environment(go, out)
    env.update(manifest['recipe']['environment'])
    env.update(GOMODCACHE=str(out / 'modules'), GOVCS='*:off',
               SOURCE_DATE_EPOCH=str(manifest['source_date_epoch']))
    if private:
        env.update(GOPROXY=(out / 'proxy').as_uri() + ',' + env['GOPROXY'],
                   GONOSUMDB=CORE + ',' + WORKER)
    return env


def module_requirements(value, expected):
    if not isinstance(value, dict) or not isinstance(value.get('Module'), dict) \
            or value['Module'].get('Path') != expected:
        raise ValueError('Source module path differs from the release contract')
    if value.get('Replace') or value.get('Exclude'):
        raise ValueError('Release dependency declarations cannot contain replace or exclude')
    requirements = {}
    for record in value.get('Require') or []:
        if not isinstance(record, dict):
            raise ValueError('Invalid module requirement')
        path, version = record.get('Path'), record.get('Version')
        if not isinstance(path, str) or not isinstance(version, str) or path in requirements:
            raise ValueError('Invalid or duplicate module requirement')
        if path == RECIPE['module'] or (path.startswith(RECIPE['module'] + '/') and path not in (CORE, WORKER)):
            raise ValueError('Unrecognized private CPRa module dependency')
        requirements[path] = version
    return requirements


def source_changes(source, inventory):
    changed = []
    for name, record in inventory.items():
        path = source / name
        if record['mode'] == '120000':
            same = path.is_symlink() and hashlib.sha256(os.readlink(path).encode()).hexdigest() == record['sha256']
        else:
            same = path.is_file() and not path.is_symlink() and sha(path) == record['sha256']
            if same and os.name != 'nt':
                same = bool(path.stat().st_mode & 0o111) == (record['mode'] == '100755')
        if not same:
            changed.append(name)
    for path in source.rglob('*'):
        if (path.is_file() or path.is_symlink()) and path.relative_to(source).as_posix() not in inventory:
            changed.append(path.relative_to(source).as_posix())
    return sorted(set(changed))


def cached_private_modules(records, archives, cache):
    """Bind Go's downloads to the privately constructed module payloads."""
    downloaded = {record.get('Path'): record for record in records}
    if len(downloaded) != len(records) or set(downloaded) != {CORE, WORKER}:
        raise ValueError('Private download must report exactly the SDK and worker modules')
    files = {}
    for module, expected in archives.items():
        actual = downloaded[module]
        for key in ('Path', 'Version', 'Sum', 'GoModSum'):
            if actual.get(key) != expected[key]:
                raise ValueError('Downloaded private module differs from its source archive')
        for key in ('Info', 'GoMod', 'Zip'):
            path = Path(actual.get(key, '')).resolve()
            if not path.is_relative_to(cache) or not path.is_file():
                raise ValueError('Go private module download escaped the isolated cache')
            relative = path.relative_to(cache).as_posix()
            digest = sha(path)
            if key == 'Zip' and digest != expected['ArchiveSHA256']:
                raise ValueError('Cached private ZIP differs from the staged payload')
            files[relative] = digest
    return files


def dependency_identity(record, archives):
    fields = ('Path', 'Version', 'Sum', 'GoModSum')
    if any(not isinstance(record.get(key), str) or not record[key] for key in fields) or record.get('Replace'):
        raise ValueError('Dependency download omitted an identity or used a replacement')
    path = record['Path']
    if path == RECIPE['module'] or (path.startswith(RECIPE['module'] + '/') and path not in archives):
        raise ValueError('Dependency download selected an unrecognized CPRa module')
    if path in archives and any(record[key] != archives[path][key] for key in fields):
        raise ValueError('Dependency graph selected a different private module')
    return {key: record[key] for key in fields}


def dependency_graph(rows, module, archives):
    if [row.get('Path') for row in rows if row.get('Main') is True] != [module]:
        raise ValueError('Verified dependency graph has the wrong main module')
    paths = set()
    for row in rows:
        path = row.get('Path')
        if not isinstance(path, str) or path in paths or row.get('Replace'):
            raise ValueError('Invalid or replaced module in verified dependency graph')
        paths.add(path)
        if row.get('Main') is True:
            continue
        if path == RECIPE['module'] or (path.startswith(RECIPE['module'] + '/') and path not in archives):
            raise ValueError('Verified graph selected an unrecognized CPRa module')
        if path in archives and (row.get('Version') != archives[path]['Version'] or
                                 any(key in row and row[key] != archives[path][key] for key in ('Sum', 'GoModSum'))):
            raise ValueError('Verified graph selected a different private module')
    return [{key: row[key] for key in ('Path', 'Version', 'Main', 'Sum', 'GoModSum') if key in row} for row in rows]


def cache_environment(directory, manifest):
    """Validate recorded cache provenance before a build selects its location.

    Go must still verify cached archives and extracted directories. This report
    does not replace Go's checksum checks or establish public SDK publication.
    """
    directory = Path(directory).resolve()
    report = json.loads((directory / REPORT).read_text())
    if not isinstance(report, dict) or type(report.get('schema_version')) is not int \
            or report['schema_version'] != 1 or report.get('status') != 'pass' \
            or report.get('evidence_class') != 'private_dependency_cache' \
            or report.get('release_identity') != evidence_identity(manifest) \
            or report.get('bootstrap_checksum_exceptions') != [CORE, WORKER] \
            or report.get('verification_environment') != manifest['recipe']['environment']:
        raise ValueError('Dependency cache does not match these frozen release inputs')
    modules = report.get('private_modules')
    files = report.get('private_cache_files')
    if not isinstance(modules, dict) or set(modules) != {CORE, WORKER} \
            or not isinstance(files, dict) or not files:
        raise ValueError('Dependency cache omits private module identities')
    cache = directory / 'modules'
    if cache.is_symlink() or not cache.is_dir():
        raise ValueError('Dependency cache directory is unavailable')
    go_path = directory / 'go-path'
    if go_path.is_symlink() or (go_path.exists() and not go_path.is_dir()):
        raise ValueError('Dependency GOPATH must remain in the private directory')
    expected = set()
    for module, record in modules.items():
        if not isinstance(record, dict) or record.get('Path') != module or not valid_version(record.get('Version')):
            raise ValueError('Invalid private module identity in dependency cache')
        expected.update('cache/download/' + module + '/@v/' + record['Version'] + suffix
                        for suffix in ('.info', '.mod', '.zip'))
    if set(files) != expected:
        raise ValueError('Dependency cache file inventory is incomplete')
    for name, digest in files.items():
        if not isinstance(name, str):
            raise ValueError('Unsafe recorded dependency cache path')
        relative = PurePosixPath(name)
        if relative.is_absolute() or '..' in relative.parts \
                or '\\' in name or str(relative) != name:
            raise ValueError('Unsafe recorded dependency cache path')
        path = (cache / name).resolve()
        if not path.is_relative_to(cache) or not path.is_file() or sha(path) != digest:
            raise ValueError('Private dependency cache bytes have changed')
    return {'GOMODCACHE': str(cache), 'GOPATH': str(go_path)}


def bootstrap(source_archive, go, worker_version, out):
    out = Path(out).resolve()
    source_archive = Path(source_archive).resolve()
    go = Path(shutil.which(str(go)) or go).resolve()
    if not valid_version(worker_version):
        raise ValueError('Select an explicit canonical worker version')
    if out.is_relative_to(ROOT) or source_archive.is_relative_to(out):
        raise ValueError('Select a new dependency directory outside the source checkout and archive')
    out.mkdir(mode=0o700, parents=True, exist_ok=False)
    report = {'schema_version': 1, 'status': 'incomplete', 'evidence_class': 'private_dependency_cache',
              'recorded_at': datetime.now(timezone.utc).isoformat(),
              'public_download_qualified': False, 'release_qualified': False}
    source = out / 'source'
    try:
        report['source_archive_sha256'] = sha(source_archive)
        manifest, identity = extract_release_source(source_archive, source)
        report['release_identity'] = identity
        public = compiler_environment(go, out, manifest)
        private = compiler_environment(go, out, manifest, private=True)
        report['verification_environment'] = {key: public[key] for key in RECIPE['environment']}
        report['bootstrap_checksum_exceptions'] = [CORE, WORKER]
        compiler = json.loads(go_output(go, ['env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH'],
                                      cwd=source, env=public, out=out, label='compiler'))
        if compiler.get('GOVERSION') != manifest['recipe']['go_version']:
            raise ValueError('Dependency preparation requires the recorded release compiler')
        compiler['binary_sha256'] = sha(go)
        report['compiler'] = compiler
        requirements = {}
        for index, (module, relative) in enumerate(MODULES.items()):
            metadata = json.loads(go_output(go, ['mod', 'edit', '-json'], cwd=source / relative,
                                          env=public, out=out, label=f'module-{index}'))
            requirements[module] = module_requirements(metadata, module)
        sdk_version = requirements[RECIPE['module']].get(CORE)
        if not valid_version(sdk_version) or requirements[WORKER].get(CORE) != sdk_version:
            raise ValueError('Application and worker must require the same selected SDK version')
        report['requirements'] = requirements
        archiver = build_archiver(go, out / 'archive-tool', source)
        archives = {module: stage_module(archiver, out / 'proxy', module, version,
                                        source / MODULES[module], manifest['source_date'])
                    for module, version in ((CORE, sdk_version), (WORKER, worker_version))}
        report['private_modules'] = archives
        # Explicit downloads use a disposable consumer, outside frozen source.
        consumer = out / 'download'; consumer.mkdir()
        (consumer / 'go.mod').write_text('module example.com/cpra-dependency-bootstrap\n\ngo 1.25.0\n')
        downloads = json_records(go_output(go, ['mod', 'download', '-json',
                                    CORE + '@' + sdk_version, WORKER + '@' + worker_version],
                                    cwd=consumer, env=private, out=out, label='private-download'))
        report['private_cache_files'] = cached_private_modules(downloads, archives, out / 'modules')
        report['downloads'] = {}
        for index, (module, relative) in enumerate(MODULES.items()):
            rows = json_records(go_output(go, ['mod', 'download', '-json', 'all'], cwd=source / relative,
                                         env=private, out=out, label=f'download-{index}'))
            report['downloads'][module] = [dependency_identity(row, archives) for row in rows]
        changed = source_changes(source, manifest['source_files'])
        if changed:
            report.update(status='input_changes', changed_inputs=changed)
            raise ValueError('Dependency resolution changed frozen inputs; review the retained source changes and freeze a new candidate')
        report['graphs'] = {}
        for index, (module, relative) in enumerate(MODULES.items()):
            go_output(go, ['mod', 'verify'], cwd=source / relative, env=public,
                      out=out, label=f'verify-{index}')
            rows = json_records(go_output(go, ['list', '-mod=readonly', '-m', '-json', 'all'],
                                         cwd=source / relative, env=public, out=out, label=f'graph-{index}'))
            report['graphs'][module] = dependency_graph(rows, module, archives)
        changed = source_changes(source, manifest['source_files'])
        if changed:
            report.update(status='input_changes', changed_inputs=changed)
            raise ValueError('Verification changed frozen source inputs')
        if sha(go) != compiler['binary_sha256']:
            raise ValueError('Selected compiler changed during dependency preparation')
        if any(sha(out / 'modules' / name) != digest for name, digest in report['private_cache_files'].items()):
            raise ValueError('Private dependency cache changed during verification')
        report['status'] = 'pass'
    except Exception as error:
        if report['status'] == 'incomplete':
            report['status'] = 'failed'
        report['error'] = str(error)
        raise
    finally:
        (out / REPORT).write_bytes(encoded(report))
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True, help='frozen source archive with RELEASE.json')
    parser.add_argument('--go', required=True)
    parser.add_argument('--worker-version', required=True)
    parser.add_argument('--out', type=Path, required=True, help='new private dependency directory outside the checkout')
    args = parser.parse_args()
    bootstrap(args.source, args.go, args.worker_version, args.out)
    print('Private dependency cache prepared; public SDK downloads and release qualification remain separate gates.')


if __name__ == '__main__':
    main()
