"""Prepare genuine module-file changes before freezing a private CPRa release.

This command copies current Git-selected source bytes, resolves the private SDK
through a canonical local module proxy, and retains Go-generated proposals. It
never edits the checkout, creates a Git identity, or qualifies a release.
"""
import argparse
from datetime import datetime, timezone
import difflib
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import signal
import stat
import subprocess
import time

from module_archive import (CORE, MODULE, ROOT, WORKER, build_archiver, sha256,
                            stage_module, tool_environment)

MODULES = {MODULE: '.', CORE: 'sdk/go', WORKER: 'sdk/go/worker'}
MAX_SOURCE_BYTES = 256 * 1024 ** 2
MAX_FILE_BYTES = 32 * 1024 ** 2
MAX_SOURCE_FILES = 20_000
MAX_LOG_BYTES = 16 * 1024 ** 2
VERSION = re.compile(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?')


def write_json(path, value):
    Path(path).write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def source_inventory(root, destination=None):
    """Capture tracked edits/deletions and nonignored new files without Git data."""
    root = Path(root).resolve()
    raw = subprocess.check_output(['git', 'ls-files', '--cached', '--others',
                                   '--exclude-standard', '-z'], cwd=root)
    names = sorted(set(raw.decode().split('\0')) - {''})
    if len(names) > MAX_SOURCE_FILES:
        raise ValueError('Source selection exceeds the file quota')
    inventory, total = {}, 0
    for name in names:
        relative = PurePosixPath(name)
        if relative.is_absolute() or relative.as_posix() != name \
                or any(part in ('', '.', '..', '.git') for part in relative.parts):
            raise ValueError('Unsafe source path')
        if relative.name.startswith('.env') and relative.name != '.env.example':
            raise ValueError('Dotenv inputs must not enter dependency preparation: ' + name)
        source = root / name
        if not source.resolve().is_relative_to(root):
            raise ValueError('Source path escapes the checkout: ' + name)
        try:
            original = source.lstat()
        except FileNotFoundError:
            continue  # A tracked deletion is part of the current working source.
        if not stat.S_ISREG(original.st_mode) or source.is_symlink():
            raise ValueError('Source selection requires regular files: ' + name)
        if original.st_size > MAX_FILE_BYTES:
            raise ValueError('Source file exceeds the byte quota: ' + name)
        digest, size = hashlib.sha256(), 0
        target = None
        if destination is not None:
            target = Path(destination) / name
            target.parent.mkdir(parents=True, exist_ok=True)
        with source.open('rb') as stream:
            output = target.open('xb') if target else None
            try:
                for block in iter(lambda: stream.read(1 << 20), b''):
                    size += len(block)
                    total += len(block)
                    if size > MAX_FILE_BYTES or total > MAX_SOURCE_BYTES:
                        raise ValueError('Source selection exceeds the byte quota')
                    digest.update(block)
                    if output:
                        output.write(block)
            finally:
                if output:
                    output.close()
        current = source.lstat()
        identity = lambda value: (value.st_dev, value.st_ino, value.st_size,
                                   value.st_mtime_ns, value.st_ctime_ns, value.st_mode)
        if identity(original) != identity(current) or size != original.st_size:
            raise ValueError('Source changed during capture: ' + name)
        mode = 0o700 if original.st_mode & 0o111 else 0o600
        if target:
            target.chmod(mode)
        inventory[name] = {'sha256': digest.hexdigest(), 'bytes': size,
                           'mode': stat.S_IMODE(original.st_mode),
                           'executable': bool(original.st_mode & 0o111)}
    return inventory


def run_go(go, source, args, env, out, name, commands, timeout=900):
    """Retain command output with a time and output bound."""
    stdout, stderr = out / (name + '.stdout'), out / (name + '.stderr')
    command = [str(go), *args]
    record = {'command': command, 'directory': str(source), 'stdout': stdout.name,
              'stderr': stderr.name, 'status': 'running',
              'started_at': datetime.now(timezone.utc).isoformat()}
    commands.append(record)
    print('Preparing ' + name, flush=True)
    started = time.monotonic()
    failure = None
    with stdout.open('xb') as output, stderr.open('xb') as errors:
        process = subprocess.Popen(command, cwd=source, env=env, stdout=output,
                                   stderr=errors, start_new_session=True)
        try:
            while process.poll() is None:
                if time.monotonic() - started > timeout:
                    failure = 'Go command exceeded its time budget'
                    break
                if stdout.stat().st_size + stderr.stat().st_size > MAX_LOG_BYTES:
                    failure = 'Go command exceeded its output quota'
                    break
                time.sleep(0.1)
        finally:
            if process.poll() is None:
                if os.name == 'nt':
                    process.terminate()
                else:
                    os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    if os.name == 'nt':
                        process.kill()
                    else:
                        os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
    record.update(exit_code=process.returncode, seconds=round(time.monotonic() - started, 3),
                  stdout_sha256=sha256(stdout), stderr_sha256=sha256(stderr))
    if stdout.stat().st_size + stderr.stat().st_size > MAX_LOG_BYTES:
        failure = 'Go command exceeded its output quota'
    record['status'] = 'pass' if process.returncode == 0 and failure is None else 'fail'
    if record['status'] != 'pass':
        raise ValueError(failure or ('Go command failed; inspect ' + stderr.name))
    return stdout.read_text()


def module_contract(metadata, module, sdk_version=None):
    if not isinstance(metadata, dict) or metadata.get('Module', {}).get('Path') != module \
            or metadata.get('Go') != '1.26.0':
        raise ValueError('Module path or Go compatibility changed: ' + module)
    if metadata.get('Replace') or metadata.get('Exclude') or metadata.get('Toolchain'):
        raise ValueError('Preparation forbids replace, exclude and toolchain directives')
    requirements = {}
    for item in metadata.get('Require') or []:
        path, version = item.get('Path'), item.get('Version')
        if not isinstance(path, str) or not isinstance(version, str) or path in requirements:
            raise ValueError('Invalid or duplicate module requirement')
        if path == MODULE or (path.startswith(MODULE + '/') and path != CORE):
            raise ValueError('Invalid SDK dependency direction: ' + path)
        requirements[path] = version
    if module == CORE and CORE in requirements:
        raise ValueError('SDK must not depend on itself')
    if module != CORE:
        selected = requirements.get(CORE)
        if not isinstance(selected, str) or not VERSION.fullmatch(selected) \
                or (sdk_version is not None and selected != sdk_version):
            raise ValueError('Application and worker must retain the same explicit SDK version')
    return requirements


def proposals(source, before):
    """Only genuine Go module-file edits may differ from the captured source."""
    allowed = {str(PurePosixPath(relative) / name)
               for relative in MODULES.values() for name in ('go.mod', 'go.sum')}
    allowed = {name.removeprefix('./') for name in allowed}
    changes = []
    actual = {path.relative_to(source).as_posix() for path in source.rglob('*') if path.is_file()}
    if actual - before.keys() - allowed:
        raise ValueError('Go preparation added unexpected source files')
    for name, original in before.items():
        path = source / name
        if not path.is_file() or path.is_symlink():
            raise ValueError('Go preparation removed or replaced a source file: ' + name)
        current = sha256(path)
        if current == original['sha256']:
            continue
        if name not in allowed:
            raise ValueError('Go preparation changed a non-module source file: ' + name)
        changes.append({'path': name, 'before_sha256': original['sha256'],
                        'after_sha256': current})
    for name in sorted(actual - before.keys()):
        changes.append({'path': name, 'before_sha256': None, 'after_sha256': sha256(source / name)})
    return sorted(changes, key=lambda item: item['path'])


def copy_prepared_source(directory, destination):
    """Verify retained preparation bytes before a separate consumer campaign."""
    directory = Path(directory).resolve(strict=True)
    report_path, inputs_path = directory / 'report.json', directory / 'source-inputs.json'
    for path in (report_path, inputs_path):
        if path.is_symlink() or not path.is_file() or path.stat().st_size > MAX_LOG_BYTES:
            raise ValueError('Preparation metadata must be bounded regular files')
    def read_metadata(path):
        with path.open('rb') as stream:
            data = stream.read(MAX_LOG_BYTES + 1)
        if len(data) > MAX_LOG_BYTES:
            raise ValueError('Preparation metadata exceeds its byte quota')
        return json.loads(data)
    report, inputs = read_metadata(report_path), read_metadata(inputs_path)
    if not isinstance(report, dict) or report.get('schema_version') != 1 \
            or report.get('evidence_class') != 'working_tree_dependency_preparation' \
            or report.get('status') != 'prepared' or report.get('source_unchanged') is not True \
            or report.get('release_qualified') is not False or report.get('public_download_qualified') is not False \
            or report.get('source_inputs_sha256') != sha256(inputs_path):
        raise ValueError('Preparation is incomplete or has a different evidence boundary')
    if not isinstance(inputs, dict) or len(inputs) > MAX_SOURCE_FILES:
        raise ValueError('Preparation source inventory exceeds its file quota')
    expected = {}
    for name, value in inputs.items():
        if not isinstance(value, dict):
            raise ValueError('Invalid preparation source inventory')
        expected[name] = value.get('sha256')
    allowed = {str(PurePosixPath(relative) / name)
               for relative in MODULES.values() for name in ('go.mod', 'go.sum')}
    changes = report.get('proposals')
    if not isinstance(changes, list) or len(changes) > len(allowed):
        raise ValueError('Invalid module-file proposals')
    seen = set()
    for change in changes:
        if not isinstance(change, dict):
            raise ValueError('Invalid module-file proposal')
        name = change.get('path')
        if name not in allowed or name in seen or change.get('before_sha256') != expected.get(name):
            raise ValueError('Module-file proposal does not match the original inventory')
        expected[name] = change.get('after_sha256')
        seen.add(name)
    source = directory / 'source'
    if source.is_symlink() or not source.is_dir():
        raise ValueError('Preparation source must be a regular directory')
    actual, nodes = set(), 0
    for path in source.rglob('*'):
        nodes += 1
        if nodes > MAX_SOURCE_FILES * 2:
            raise ValueError('Preparation source exceeds its filesystem entry quota')
        if path.is_symlink():
            raise ValueError('Preparation source contains a link')
        if path.is_file():
            actual.add(path.relative_to(source).as_posix())
    if actual != expected.keys():
        raise ValueError('Preparation source file inventory changed')
    destination = Path(destination)
    destination.mkdir(mode=0o700)
    total = 0
    for name, digest in expected.items():
        relative = PurePosixPath(name)
        if relative.is_absolute() or relative.as_posix() != name \
                or any(part in ('', '.', '..', '.git') for part in relative.parts) \
                or not isinstance(digest, str) or not re.fullmatch('[0-9a-f]{64}', digest):
            raise ValueError('Invalid preparation source identity')
        path = source / name
        with path.open('rb') as stream:
            data = stream.read(MAX_FILE_BYTES + 1)
        total += len(data)
        if len(data) > MAX_FILE_BYTES or total > MAX_SOURCE_BYTES:
            raise ValueError('Preparation source exceeds its byte quota')
        if hashlib.sha256(data).hexdigest() != digest:
            raise ValueError('Preparation source content changed: ' + name)
        executable = bool(inputs.get(name, {}).get('executable', False))
        if bool(path.stat().st_mode & 0o111) != executable:
            raise ValueError('Preparation source executable mode changed: ' + name)
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        with target.open('xb') as output:
            output.write(data)
        target.chmod(0o700 if executable else 0o600)
    return report


def prepare(go, out, worker_version, root=ROOT, *, refresh_sdk_checksum=False):
    root, out, go = Path(root).resolve(), Path(out).resolve(), Path(go).resolve(strict=True)
    if out.is_relative_to(root) or root.is_relative_to(out):
        raise ValueError('Select a new private output directory outside the checkout')
    if not VERSION.fullmatch(worker_version):
        raise ValueError('An explicit canonical worker version is required')
    old_umask = os.umask(0o077)
    report = {'schema_version': 1, 'evidence_class': 'working_tree_dependency_preparation',
              'status': 'incomplete', 'release_qualified': False, 'public_download_qualified': False,
              'independent_review': 'pending', 'commands': [], 'module_archives': {}}
    original, created = None, False
    try:
        out.mkdir(parents=True, exist_ok=False)
        created = True
        write_json(out / 'report.json', report)
        source = out / 'source'
        source.mkdir()
        report['base_commit'] = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
        report['source_selection'] = 'current tracked and nonignored working-tree files'
        report['working_tree_dirty'] = bool(subprocess.check_output(
            ['git', 'status', '--porcelain', '--untracked-files=all'], cwd=root))
        original = source_inventory(root, source)
        write_json(out / 'source-inputs.json', original)
        report['source_inputs_sha256'] = sha256(out / 'source-inputs.json')
        report['source_bytes'] = sum(item['bytes'] for item in original.values())
        env = tool_environment(go, out)
        env.update(GOVCS='*:off', GOMAXPROCS='2', GOMEMLIMIT='768MiB')
        run = lambda directory, args, name: run_go(go, directory, args, env, out, name, report['commands'])
        report['compiler'] = {'path': str(go), 'sha256': sha256(go),
                              'version': run(source, ['version'], 'compiler-version').strip()}
        contracts = {}
        for index, (module, relative) in enumerate(MODULES.items()):
            metadata = json.loads(run(source / relative, ['mod', 'edit', '-json'], 'module-before-' + str(index)))
            contracts[module] = module_contract(metadata, module)
        sdk_version = contracts[MODULE][CORE]
        module_contract(json.loads((out / 'module-before-2.stdout').read_text()), WORKER, sdk_version)
        report['versions'] = {CORE: sdk_version, WORKER: worker_version}
        # Normalize the leaf first: its final bytes determine dependent sums.
        run(source / 'sdk/go', ['mod', 'tidy', '-compat=1.26'], 'sdk-tidy')
        run(source / 'sdk/go', ['mod', 'download', '-json', 'all'], 'sdk-download')
        archiver = build_archiver(go, out / 'archiver', source)
        report['archiver_sha256'] = sha256(archiver)
        epoch = int(subprocess.check_output(['git', 'show', '-s', '--format=%ct', report['base_commit']], cwd=root, text=True))
        source_time = datetime.fromtimestamp(epoch, timezone.utc).isoformat().replace('+00:00', 'Z')
        report['module_info_time'] = source_time
        report['module_info_time_basis'] = 'base commit time; not an assertion that the working tree is committed'
        proxy = out / 'proxy'
        sdk = stage_module(archiver, proxy, CORE, sdk_version, source / 'sdk/go', source_time)
        report['module_archives'][CORE] = sdk
        env.update(GOPROXY=proxy.as_uri() + ',https://proxy.golang.org', GONOSUMDB=CORE + ',' + WORKER)
        for name, relative in [('application', '.'), ('worker', 'sdk/go/worker')]:
            if refresh_sdk_checksum:
                # This is a proposal in the disposable source copy. Refresh
                # only this SDK version's changed archive/definition checksums;
                # Go verifies the new values against the staged source bytes.
                path = source / relative / 'go.sum'
                if path.exists():
                    original_rows = path.read_bytes().splitlines(keepends=True)
                    selected = {(CORE + ' ' + sdk_version + ' ').encode(): sdk['Sum'],
                                (CORE + ' ' + sdk_version + '/go.mod ').encode(): sdk['GoModSum']}
                    stale = [row for row in original_rows
                             if any(row.startswith(prefix) and row.strip().split()[-1].decode() != expected
                                    for prefix, expected in selected.items())]
                    if stale:
                        report.setdefault('sdk_checksum_refresh', []).append({
                            'path': path.relative_to(source).as_posix(),
                            'version': sdk_version, 'source_archive_sum': sdk['Sum'],
                            'source_module_sum': sdk['GoModSum'],
                            'previous_rows': [row.decode().strip() for row in stale],
                        })
                        path.write_bytes(b''.join(row for row in original_rows if row not in stale))
            run(source / relative, ['mod', 'tidy', '-compat=1.26'], name + '-tidy')
            run(source / relative, ['mod', 'download', '-json', 'all'], name + '-download')
        for index, (module, relative) in enumerate(MODULES.items()):
            metadata = json.loads(run(source / relative, ['mod', 'edit', '-json'], 'module-after-' + str(index)))
            module_contract(metadata, module, sdk_version)
            run(source / relative, ['mod', 'verify'], 'module-verify-' + str(index))
            run(source / relative, ['list', '-mod=readonly', '-m', '-json', 'all'], 'module-graph-' + str(index))
        worker = stage_module(archiver, proxy, WORKER, worker_version, source / 'sdk/go/worker', source_time)
        report['module_archives'][WORKER] = worker
        consumer = out / 'consumer'
        consumer.mkdir()
        (consumer / 'go.mod').write_text('module cpra.invalid/dependency-consumer\n\ngo 1.26.0\n')
        raw = run(consumer, ['mod', 'download', '-json', CORE + '@' + sdk_version,
                             WORKER + '@' + worker_version], 'private-module-download')
        decoder, rows, remainder = json.JSONDecoder(), [], raw.lstrip()
        while remainder:
            row, end = decoder.raw_decode(remainder)
            rows.append(row)
            remainder = remainder[end:].lstrip()
        if len(rows) != 2 or {row.get('Path') for row in rows} != {CORE, WORKER}:
            raise ValueError('Go did not download both private modules')
        for row in rows:
            expected = report['module_archives'][row['Path']]
            if any(row.get(key) != expected[key] for key in ('Path', 'Version', 'Sum', 'GoModSum')):
                raise ValueError('Go download differs from the canonical module payload')
            archive = Path(row['Zip']).resolve(strict=True)
            if not archive.is_relative_to(out / 'module-cache') or sha256(archive) != expected['ArchiveSHA256']:
                raise ValueError('Go downloaded a different module ZIP')
        report['proposals'] = proposals(source, original)
        diffs = []
        for change in report['proposals']:
            name = change['path']
            before = (root / name).read_text().splitlines(keepends=True) if change['before_sha256'] else []
            after = (source / name).read_text().splitlines(keepends=True)
            diffs.extend(difflib.unified_diff(before, after, fromfile='original/' + name, tofile='prepared/' + name))
        (out / 'module-files.diff').write_text(''.join(diffs))
        if source_inventory(root) != original:
            raise ValueError('The checkout changed during dependency preparation')
        if sha256(go) != report['compiler']['sha256']:
            raise ValueError('The compiler changed during dependency preparation')
        report['source_unchanged'] = True
        report['status'] = 'prepared'
    except Exception as error:
        report.update(status='failed', error=str(error))
        if created and (out / 'report.json').is_file():
            write_json(out / 'report.json', report)
        raise
    finally:
        if created and (out / 'report.json').is_file():
            write_json(out / 'report.json', report)
        os.umask(old_umask)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--worker-version', required=True)
    parser.add_argument('--refresh-sdk-checksum', action='store_true',
                        help='propose refreshed SDK archive and go.mod checksums for an unpublished private candidate; only copied files change')
    args = parser.parse_args()
    try:
        report = prepare(args.go, args.out, args.worker_version,
                         refresh_sdk_checksum=args.refresh_sdk_checksum)
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        parser.exit(1, str(error) + '\n')
    print(json.dumps({'status': report['status'], 'proposals': report['proposals'],
                      'report': str(args.out / 'report.json'), 'release_qualified': False}))


if __name__ == '__main__':
    main()
