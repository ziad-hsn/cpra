#!/usr/bin/env python3
"""Record an actual fixture invocation and its candidate/image fingerprints.

Usage: record_fixture.py --binary B --out R [--image cached:tag] --
       python3 local.py --binary B --out R

The existing fixture owns cleanup. Only its successful exit plus passing
invoked report records establishes a passing sidecar. No image is pulled.
"""
import argparse
import hashlib
import json
import pathlib
import subprocess
import time


def passing_report(path):
    report = json.loads(pathlib.Path(path).read_text())
    enabled = [row for row in report['records'] if row.get('configured')]
    return bool(enabled) and all(row['status'] == 'pass' and row.get('operation_invoked') is True
                                 and row.get('accepted') is True and row.get('observed') is True for row in enabled)


def validate_command(command, binary, report):
    if not command:
        raise ValueError('a fixture command is required after --')
    for flag, expected in (('--binary', binary), ('--out', report)):
        indexes = []
        for index, argument in enumerate(command):
            option = argument.split('=', 1)[0]
            if option.startswith('--') and option != '--' and flag.startswith(option):
                if option != flag:
                    raise ValueError('fixture binding must spell out ' + flag)
                indexes.append(index)
        if len(indexes) != 1:
            raise ValueError('fixture command must declare ' + flag + ' exactly once')
        index = indexes[0]
        if '=' in command[index]:
            value = command[index].split('=', 1)[1]
        elif index + 1 < len(command):
            value = command[index + 1]
        else:
            raise ValueError('missing fixture value for ' + flag)
        if pathlib.Path(value).resolve() != expected:
            raise ValueError('fixture command must use the declared ' + flag)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--out', required=True)
    parser.add_argument('--image', action='append', default=[])
    parser.add_argument('command', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command and args.command[0] == '--' else args.command
    binary, report = pathlib.Path(args.binary).resolve(strict=True), pathlib.Path(args.out).resolve()
    try:
        validate_command(command, binary, report)
    except ValueError as error:
        parser.error(str(error))
    report.parent.mkdir(parents=True, exist_ok=True)
    sidecar = pathlib.Path(str(report) + '.fixture.json')
    if sidecar.exists() or report.exists():
        parser.error('choose a new output or archive earlier evidence before running')
    state = {'status': 'running', 'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
             'command': command, 'working_directory': str(pathlib.Path.cwd()), 'cached_images': [],
             'cleanup_failures': [], 'cleanup_status': 'pending', 'started_unix': time.time()}

    def save():
        temporary = sidecar.with_suffix(sidecar.suffix + '.tmp')
        temporary.write_text(json.dumps(state, indent=2) + '\n')
        temporary.replace(sidecar)

    save()
    try:
        for name in args.image:
            image = json.loads(subprocess.check_output(['docker', 'image', 'inspect', name], text=True))[0]
            state['cached_images'].append({'name': name, 'image_id': image['Id'], 'repo_digests': image.get('RepoDigests', [])})
        save()
        completed = subprocess.run(command, check=False)
        state['exit_code'] = completed.returncode
        state['binary_sha256_after'] = hashlib.sha256(binary.read_bytes()).hexdigest()
        if state['binary_sha256_after'] != state['binary_sha256']:
            raise ValueError('candidate binary changed during fixture execution')
        # These fixture entrypoints complete their checked cleanup before exit.
        state['cleanup_status'] = 'confirmed_by_fixture_exit' if completed.returncode == 0 else 'unconfirmed'
        state['status'] = 'pass' if completed.returncode == 0 and passing_report(report) else 'fail'
        if report.exists():
            state['report_sha256'] = hashlib.sha256(report.read_bytes()).hexdigest()
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        state.update(status='fail', reason=type(error).__name__ + ': fixture invocation or report validation failed', cleanup_status='unconfirmed')
    finally:
        state['completed_unix'] = time.time()
        save()
    return 0 if state['status'] == 'pass' else 1


if __name__ == '__main__':
    raise SystemExit(main())
