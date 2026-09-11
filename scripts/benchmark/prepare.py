#!/usr/bin/env python3
"""Build the pinned baseline and candidate with matching compiler settings."""
import argparse
import hashlib
import json
import pathlib
import subprocess
import tempfile

BASELINE = 'a370969b041b399c0778318d8915ce059fd74294'


def command(args, cwd):
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--go', default='go')
    parser.add_argument('--tags', default='')
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    root = pathlib.Path(__file__).resolve().parents[2]
    out = pathlib.Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    compiler = command([args.go, 'version'], root)
    flags = ['-trimpath', '-mod=readonly', '-tags', args.tags, '-ldflags=-s -w']
    base = command(['git', 'rev-parse', BASELINE + '^{commit}'], root)
    if base != BASELINE:
        raise SystemExit('Published baseline commit could not be verified')
    with tempfile.TemporaryDirectory(prefix='cpra-baseline-') as directory:
        checkout = pathlib.Path(directory) / 'source'
        subprocess.run(['git', 'worktree', 'add', '--detach', str(checkout), BASELINE], cwd=root, check=True)
        try:
            records = []
            for name, source in [('baseline', checkout), ('candidate', root)]:
                binary = out / name
                subprocess.run([args.go, 'build', *flags, '-o', str(binary), '.'], cwd=source, check=True)
                status = command(['git', 'status', '--porcelain'], source)
                records.append({'name': name, 'commit': command(['git', 'rev-parse', 'HEAD'], source),
                                'dirty': bool(status), 'go_version': compiler, 'flags': flags,
                                'binary': str(binary), 'sha256': hashlib.sha256(binary.read_bytes()).hexdigest()})
            subprocess.run([args.go, 'build', *flags, '-o', str(out / 'target'), './cmd/cpra-target'], cwd=root, check=True)
            (out / 'builds.json').write_text(json.dumps({'baseline_commit': BASELINE, 'builds': records}, indent=2) + '\n')
        finally:
            subprocess.run(['git', 'worktree', 'remove', str(checkout)], cwd=root, check=True)


if __name__ == '__main__':
    main()
