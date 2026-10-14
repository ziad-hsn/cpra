"""Build preparation contracts; these tests do not compile or benchmark CPRa."""
import copy
from datetime import datetime, timezone
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import prepare
from test_evidence import manifest_fixture


class BuildContracts(unittest.TestCase):
    def test_preflight_prevents_compiler_or_archive_work_when_space_is_insufficient(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / 'out'
            argv = ['prepare.py', '--out', str(out), '--module-cache', directory, '--manifest', str(out / 'RELEASE.json')]
            with patch.object(sys, 'argv', argv), patch('sys.stdout', new_callable=io.StringIO), \
                    patch.object(prepare, 'inspect', return_value={'ready': False, 'reason': 'disk fixture'}) as inspect, \
                    patch.object(prepare, 'prepare_builds', side_effect=AssertionError('build reached')):
                self.assertEqual(prepare.main(), 2)
            inspect.assert_called_once_with(5 * 1024 ** 3)
            self.assertFalse(out.exists())

    def test_compiler_must_match_the_recipe_and_native_linux_target(self):
        info = {'GOVERSION': prepare.RECIPE['go_version'], 'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64'}
        for field, value in (('GOVERSION', 'go1.26.0'), ('GOHOSTOS', 'windows'), ('GOHOSTARCH', '386')):
            with self.subTest(field=field), \
                    patch.object(prepare.subprocess, 'check_output', return_value=json.dumps({**info, field: value})):
                with self.assertRaises(ValueError):
                    prepare.inspect_compiler(Path('/compiler/bin/go'), {})
        with patch.object(prepare.subprocess, 'check_output', return_value=json.dumps(info)):
            self.assertEqual(prepare.inspect_compiler(Path('/compiler/bin/go'), {}), info)

    def test_build_environment_discards_developer_overrides(self):
        with patch.dict(os.environ, {'GOFLAGS': '-race -ldflags=-s', 'GOEXPERIMENT': 'bad',
                                     'GOWORK': '/developer/go.work', 'GOOS': 'plan9',
                                     'CGO_ENABLED': '1', 'GOMODCACHE': '/developer/cache',
                                     'GOPROXY': 'https://example.invalid', 'CPRA_AUTH_TOKEN': 'secret'}):
            env = prepare.build_environment(Path('/compiler/bin/go'), 123, Path('/modules'), Path('/build'))
        for key, value in prepare.RECIPE['environment'].items():
            self.assertEqual(env[key], value, key)
        self.assertNotIn('GOOS', env)
        self.assertNotIn('CPRA_AUTH_TOKEN', env)
        self.assertEqual(env['GOMODCACHE'], '/modules')
        self.assertEqual(env['GOCACHE'], '/build')
        self.assertEqual(env['SOURCE_DATE_EPOCH'], '123')
        self.assertEqual(prepare.build_flags(['redis', 'aws']),
                         [*prepare.RECIPE['build_flags'], '-tags', 'redis aws'])
        self.assertNotIn('-s', ' '.join(prepare.build_flags([])))
        self.assertNotIn('-w', ' '.join(prepare.build_flags([])))

    def repository(self, root):
        root.mkdir()
        subprocess.run(['git', 'init', '-q', str(root)], check=True)
        (root / 'go.mod').write_text('module example.com/benchmark-fixture\n\ngo 1.26.0\n')
        (root / 'go.sum').write_text('')
        (root / 'main.go').write_text('package main\nfunc main() {}\n')
        (root / '.gitignore').write_text('ignored.go\n.env.local\n')
        for suffix in ('sdk/go', 'sdk/go/worker'):
            path = root / suffix; path.mkdir(parents=True, exist_ok=True)
            (path / 'go.mod').write_text('module ' + prepare.RECIPE['module'] + '/' + suffix + '\n\ngo 1.26.0\n')
        return self.commit(root, 'baseline fixture')

    def frozen(self, root, commit):
        manifest = manifest_fixture()
        epoch = int(prepare.command(['git', 'show', '-s', '--format=%ct', commit], root))
        manifest.update(commit=commit, source_date_epoch=epoch,
                        source_date=datetime.fromtimestamp(epoch, timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'))
        names = subprocess.check_output(['git', '-C', str(root), 'ls-files', '-z']).decode().split('\x00')
        manifest['source_files'] = {name: {'mode': '100755' if (root / name).stat().st_mode & 0o111 else '100644',
                                         'sha256': prepare.sha256(root / name)} for name in names if name}
        return manifest

    def commit(self, root, message):
        subprocess.run(['git', '-C', str(root), 'add', '.'], check=True)
        subprocess.run(['git', '-C', str(root), '-c', 'user.name=CPRa test',
                        '-c', 'user.email=test@example.invalid', 'commit', '-qm', message], check=True)
        return subprocess.check_output(['git', '-C', str(root), 'rev-parse', 'HEAD'], text=True).strip()

    def test_source_archives_exclude_ignored_inputs_and_preserve_modes(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory); root = parent / 'repository'
            self.repository(root)
            script = root / 'tool.sh'; script.write_text('#!/bin/sh\nexit 0\n'); script.chmod(0o755)
            commit = self.commit(root, 'executable fixture')
            (root / 'ignored.go').write_text('not a committed compilation input')
            (root / '.env.local').write_text('NOT_A_REAL_TOKEN=fixture')
            source = parent / 'source'; archive = parent / 'source.tar'
            prepare.extract_source(root, commit, archive, source)
            self.assertFalse((source / '.git').exists())
            self.assertFalse((source / 'ignored.go').exists())
            self.assertFalse((source / '.env.local').exists())
            self.assertEqual((source / 'tool.sh').stat().st_mode & 0o777, 0o755)
            self.assertEqual((source / 'go.mod').read_bytes(), (root / 'go.mod').read_bytes())

    def test_preparation_records_all_three_binaries_and_rejects_modified_inputs(self):
        for mutate in (False, True):
            with self.subTest(mutate=mutate), tempfile.TemporaryDirectory() as directory:
                parent = Path(directory); root = parent / 'repository'
                base = self.repository(root)
                (root / 'main.go').write_text('package main\nfunc main() { println("candidate fixture") }\n')
                candidate = self.commit(root, 'candidate fixture')
                manifest = self.frozen(root, candidate)
                compiler = parent / 'go'; compiler.write_bytes(b'compiler identity fixture')
                modules = parent / 'modules'; modules.mkdir()
                out = parent / 'out'; invocations = []
                toolchain = {'GOVERSION': prepare.RECIPE['go_version'], 'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64'}

                def compile_fixture(command, *, cwd, env, log):
                    invocations.append((command, env.copy(), cwd))
                    if command[1] == 'build':
                        dest = Path(command[command.index('-o') + 1])
                        dest.write_bytes((Path(cwd) / 'main.go').read_bytes() + command[-1].encode())
                        if mutate:
                            (Path(cwd) / 'go.sum').write_text('unexpected build mutation')

                with patch.object(prepare, 'BASELINE', base), \
                        patch.object(prepare, 'inspect_compiler', return_value=toolchain), \
                        patch.object(prepare, 'run_go', side_effect=compile_fixture):
                    if mutate:
                        with self.assertRaisesRegex(ValueError, 'source inputs changed'):
                            prepare.prepare_builds(root, compiler, out, modules, ['redis'], manifest)
                        self.assertFalse((out / 'builds.json').exists())
                        continue
                    report = prepare.prepare_builds(root, compiler, out, modules, ['redis'], manifest)
                self.assertEqual(report['schema_version'], 3)
                self.assertEqual(report['release_identity'], prepare.release_identity(manifest))
                self.assertEqual(json.loads((out / 'RELEASE.json').read_text()), manifest)
                self.assertEqual({r['name'] for r in report['builds']}, {'baseline', 'candidate', 'target'})
                builds = {r['name']: r for r in report['builds']}
                self.assertEqual(builds['baseline']['commit'], base)
                self.assertEqual(builds['candidate']['commit'], candidate)
                self.assertEqual(builds['target']['commit'], candidate)
                self.assertEqual(builds['candidate']['source_sha256'], builds['target']['source_sha256'])
                self.assertTrue(all(r['dirty'] is False for r in builds.values()))
                for row in builds.values():
                    self.assertEqual(row['sha256'], prepare.sha256(out / row['name']))
                    self.assertEqual(row['flags'], prepare.build_flags(['redis'], manifest if row['name'] != 'baseline' else None))
                self.assertEqual(len([i for i in invocations if i[0][1] == 'build']), 3)
                self.assertTrue(all(i[1]['GOWORK'] == 'off' for i in invocations))
                self.assertTrue(all(i[1]['GOMODCACHE'] == str(modules) for i in invocations))

    def test_dirty_source_is_rejected_before_compiler_or_output(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory); root = parent / 'repository'
            base = self.repository(root)
            (root / 'main.go').write_text('uncommitted change')
            out = parent / 'out'
            with patch.object(prepare, 'BASELINE', base), \
                    patch.object(prepare, 'inspect_compiler', side_effect=AssertionError('compiler reached')):
                with self.assertRaisesRegex(ValueError, 'clean committed'):
                    prepare.prepare_builds(root, parent / 'go', out, parent / 'modules', [], self.frozen(root, base))
            self.assertFalse(out.exists())

    def metadata(self, parent):
        manifest = manifest_fixture()
        manifest.update(source_date_epoch=1, source_date='1970-01-01T00:00:01Z')
        records = []
        for name in ('baseline', 'candidate', 'target'):
            binary = parent / name; binary.write_bytes(name.encode())
            records.append({'name': name, 'commit': prepare.BASELINE if name == 'baseline' else 'a' * 40,
                            'dirty': False, 'go_version': prepare.RECIPE['go_version'],
                            'flags': prepare.build_flags([], manifest if name != 'baseline' else None), 'environment': prepare.recorded_environment('linux_amd64'),
                            'source_sha256': 'b' * 64, 'source_tree_sha256': 'c' * 64,
                            'source_date_epoch': 1, 'sha256': prepare.sha256(binary)})
        return {'schema_version': 3, 'baseline_commit': prepare.BASELINE, 'recipe': prepare.RECIPE,
                'release': manifest, 'release_identity': prepare.release_identity(manifest),
                'tags': [], 'toolchain': {'version': prepare.RECIPE['go_version'], 'target': 'linux_amd64',
                                       'binary_sha256': 'd' * 64,
                                       'archive_sha256': prepare.TOOLCHAINS[prepare.RECIPE['go_version']]['linux_amd64']},
                'builds': records}

    def test_campaign_binds_target_and_rejects_inconsistent_build_settings(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); valid = self.metadata(root)
            prepare.verify_builds(valid, root / 'candidate', root / 'target', root / 'baseline')
            mutations = (
                lambda d: d.update(schema_version=1),
                lambda d: d.update(schema_version=2),
                lambda d: d['builds'].pop(),
                lambda d: d['builds'].append(copy.deepcopy(d['builds'][1])),
                lambda d: d['builds'][0].update(dirty=True),
                lambda d: d['builds'][1].update(flags=['-race']),
                lambda d: d['builds'][0].update(go_version='go1.26.0'),
                lambda d: d['builds'][1]['environment'].update(GOWORK='/developer/go.work'),
                lambda d: d['builds'][2].update(commit='e' * 40),
                lambda d: d['builds'][2].update(source_sha256='f' * 64),
                lambda d: d['toolchain'].update(archive_sha256='e' * 64),
                lambda d: d.update(recipe={}),
                lambda d: d['release'].update(version='v0.2.0'),
                lambda d: d['builds'][1].update(flags=prepare.build_flags([])),
                lambda d: d['release'].update(source_date='1970-01-01T00:00:01Z -s -w'),
            )
            for index, mutate in enumerate(mutations):
                altered = copy.deepcopy(valid); mutate(altered)
                with self.subTest(index=index), self.assertRaises(ValueError):
                    prepare.verify_builds(altered, root / 'candidate', root / 'target', root / 'baseline')
            (root / 'target').write_bytes(b'different target server')
            with self.assertRaisesRegex(ValueError, 'target.*digest'):
                prepare.verify_builds(valid, root / 'candidate', root / 'target')

    def test_changed_release_version_cannot_reuse_flags_after_recomputing_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            metadata = self.metadata(root)
            metadata['release']['version'] = 'v0.2.0'
            metadata['release_identity'] = prepare.release_identity(metadata['release'])
            with self.assertRaisesRegex(ValueError, 'build settings'):
                prepare.verify_builds(metadata, root / 'candidate', root / 'target', root / 'baseline')

    def test_unsafe_or_inconsistent_source_dates_are_rejected(self):
        for change in ({'source_date_epoch': True}, {'source_date_epoch': -1}, {'source_date_epoch': 10 ** 100},
                       {'source_date': '1970-01-01T00:02:03Z -s -w'},
                       {'source_date': '1970-01-01T00:02:04Z'}):
            with self.subTest(change=change):
                manifest = manifest_fixture(); manifest.update(change)
                with self.assertRaises(ValueError):
                    prepare.release_identity(manifest)

    def test_frozen_source_mismatch_is_rejected_before_any_compiler_command(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory); root = parent / 'repository'
            base = self.repository(root)
            manifest = self.frozen(root, base)
            manifest['source_files']['main.go']['sha256'] = 'a' * 64
            compiler = parent / 'go'; compiler.write_bytes(b'compiler fixture')
            modules = parent / 'modules'; modules.mkdir()
            info = {'GOVERSION': prepare.RECIPE['go_version'], 'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64'}
            with patch.object(prepare, 'BASELINE', base), patch.object(prepare, 'inspect_compiler', return_value=info), \
                    patch.object(prepare, 'run_go', side_effect=AssertionError('compiler command reached')):
                with self.assertRaisesRegex(ValueError, 'source file differs'):
                    prepare.prepare_builds(root, compiler, parent / 'out', modules, [], manifest)
            self.assertFalse((parent / 'out' / 'builds.json').exists())

    def test_source_validation_preserves_symlink_identity_and_executable_modes(self):
        manifest = manifest_fixture()
        inventory = {name: {'kind': 'file', 'mode': int(record['mode'][-3:], 8), 'sha256': record['sha256']}
                     for name, record in manifest['source_files'].items()}
        manifest['source_files']['tool.sh'] = {'mode': '100755', 'sha256': 'd' * 64}
        inventory['tool.sh'] = {'kind': 'file', 'mode': 0o755, 'sha256': 'd' * 64}
        target = 'main.go'
        manifest['source_files']['linked.go'] = {'mode': '120000', 'sha256': hashlib.sha256(target.encode()).hexdigest()}
        inventory['linked.go'] = {'kind': 'symlink', 'target': target}
        prepare.validate_source(manifest, inventory)
        for mutate in (lambda d: d['tool.sh'].update(mode=0o644),
                       lambda d: d['linked.go'].update(target='another.go'),
                       lambda d: d.pop('main.go'),
                       lambda d: d.update(unrecorded={'kind': 'file', 'mode': 0o644, 'sha256': 'a' * 64})):
            changed = copy.deepcopy(inventory); mutate(changed)
            with self.assertRaises(ValueError):
                prepare.validate_source(manifest, changed)


if __name__ == '__main__':
    unittest.main()
