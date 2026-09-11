#!/usr/bin/env python3
"""Build embedded assets from a pinned frontend toolchain without local env inputs."""
import argparse
import os
from pathlib import Path
import subprocess

import release


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--pnpm', default='pnpm')
    parser.add_argument('--check', action='store_true', help='require regenerated assets and notices to match committed outputs')
    args = parser.parse_args()
    if subprocess.check_output(['node', '--version'], text=True).strip() != release.RECIPE['frontend']['node_version']:
        parser.error('Use the exact Node version recorded in scripts/release/recipe.json')
    if subprocess.check_output([args.pnpm, '--version'], text=True).strip() != release.RECIPE['frontend']['pnpm_version']:
        parser.error('Use the exact pnpm version recorded in scripts/release/recipe.json')
    frontend = release.ROOT / 'dashboard'
    # Refuse dotenv files, even ignored ones. Only Vite-prefixed values reach
    # client code by default, but no ambient frontend build inputs are allowed.
    if any(frontend.glob('.env*')):
        parser.error('Move dashboard/.env* files out of the source tree before a release build')
    env = {key: os.environ[key] for key in ('PATH', 'HOME', 'USERPROFILE', 'SYSTEMROOT', 'SystemRoot', 'TEMP', 'TMP', 'TMPDIR') if key in os.environ}
    env.update(CI='true', TZ='UTC')
    subprocess.run([args.pnpm, 'install', '--frozen-lockfile', '--prod=false'], cwd=frontend, env=env, check=True)
    subprocess.run([args.pnpm, 'build'], cwd=frontend, env=env, check=True)
    subprocess.run([args.pnpm, 'exec', 'tsc', '--noEmit'], cwd=frontend, env=env, check=True)
    subprocess.run([args.pnpm, 'lint'], cwd=frontend, env=env, check=True)
    subprocess.run([args.pnpm, 'test'], cwd=frontend, env=env, check=True)
    targets = ['internal/web/server/assets', 'LICENSES/dashboard.txt', 'LICENSES/dashboard.json']
    before = {}
    if args.check:
        for name in targets:
            path = release.ROOT / name
            before[name] = release.tree_digest(path) if path.is_dir() else release.sha(path)
    subprocess.run(['python3', '-B', 'scripts/release/stage_dashboard.py'], cwd=release.ROOT, env=env, check=True)
    if args.check:
        for name, expected in before.items():
            path = release.ROOT / name
            actual = release.tree_digest(path) if path.is_dir() else release.sha(path)
            if actual != expected:
                raise RuntimeError('Regenerated dashboard output differs: ' + name + '; review and commit generated files')


if __name__ == '__main__':
    main()
