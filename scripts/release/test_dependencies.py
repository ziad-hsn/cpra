"""Cache orchestration tests with Go/compiler fixtures, not Go download evidence."""
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import dependencies
import release
import source_rebuild
from test_evidence import manifest_fixture

CORE, WORKER = dependencies.CORE, dependencies.WORKER
SDK_VERSION = 'v0.2.0'
WORKER_VERSION = 'v0.3.0'
FIXTURE_SUM = 'h1:' + 'A' * 43 + '='


class DependencyTests(unittest.TestCase):
    def bundle(self, path):
        manifest = manifest_fixture()
        definitions = {
            'go.mod': f'module {release.RECIPE["module"]}\n\ngo 1.26.0\nrequire {CORE} {SDK_VERSION}\n',
            'sdk/go/go.mod': f'module {CORE}\n\ngo 1.26.0\n',
            'sdk/go/worker/go.mod': f'module {WORKER}\n\ngo 1.26.0\nrequire {CORE} {SDK_VERSION}\n',
        }
        files = {name: definitions.get(name, 'fixture ' + name).encode() for name in manifest['source_files']}
        manifest['source_files'] = {name: {'mode': '100644', 'sha256': hashlib.sha256(data).hexdigest()}
                                    for name, data in files.items()}
        with tarfile.open(path, 'w') as archive:
            for name, data in {'RELEASE.json': json.dumps(manifest).encode(), **files}.items():
                member = tarfile.TarInfo(name); member.size = len(data); member.mode = 0o644
                archive.addfile(member, io.BytesIO(data))
        return manifest

    def test_private_exceptions_do_not_reach_verification_or_release_builds(self):
        manifest = manifest_fixture()
        with patch.dict(os.environ, {'GOWORK': '/developer/workspace', 'GOPATH': '/developer/path',
                                     'GOMODCACHE': '/developer/modules', 'GOSUMDB': 'off',
                                     'GONOSUMDB': '*', 'GOPROXY': 'https://untrusted.invalid',
                                     'CPRA_API_TOKEN': 'fixture-secret'}):
            public = dependencies.compiler_environment('/compiler/go', Path('/private/cache'), manifest)
            private = dependencies.compiler_environment('/compiler/go', Path('/private/cache'), manifest, private=True)
            build = release.environment('/compiler/go', manifest, 'linux_amd64',
                                        {'GOMODCACHE': '/private/cache/modules', 'GOPATH': '/private/cache/go-path'})
        for key, value in release.RECIPE['environment'].items():
            self.assertEqual(public[key], value, key)
            self.assertEqual(build[key], value, key)
        self.assertEqual(private['GONOSUMDB'], CORE + ',' + WORKER)
        self.assertEqual(private['GOPROXY'], 'file:///private/cache/proxy,https://proxy.golang.org')
        self.assertEqual(public['GOMODCACHE'], '/private/cache/modules')
        self.assertEqual(public['GOPATH'], '/private/cache/go-path')
        self.assertEqual(build['GOMODCACHE'], '/private/cache/modules')
        self.assertNotIn('CPRA_API_TOKEN', private)

    def test_go_json_stream_rejects_errors_and_non_object_records(self):
        self.assertEqual(dependencies.json_records(' {"Path":"a"}\n {"Path":"b"}\n'),
                         [{'Path': 'a'}, {'Path': 'b'}])
        for value in ('[]', '{"Error":"failed"}', '{"Path":"a"} invalid'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                dependencies.json_records(value)

    def test_declarations_and_graphs_reject_replacements_and_other_private_versions(self):
        value = {'Module': {'Path': CORE}, 'Require': []}
        for changed in ({**value, 'Replace': [{}]}, {**value, 'Exclude': [{}]},
                        {**value, 'Module': {'Path': 'example.com/wrong'}},
                        {**value, 'Require': [{'Path': CORE + '/unexpected', 'Version': SDK_VERSION}]}):
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                dependencies.module_requirements(changed, CORE)
        record = {'Path': CORE, 'Version': SDK_VERSION, 'Sum': FIXTURE_SUM, 'GoModSum': FIXTURE_SUM}
        archives = {CORE: record}
        for changed in ({**record, 'Version': 'v0.9.0'}, {**record, 'Replace': {}},
                        {**record, 'Path': CORE + '/unexpected'}):
            # An empty replacement object still indicates the field was supplied.
            if 'Replace' in changed:
                changed['Replace'] = {'Path': 'example.com/other'}
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                dependencies.dependency_identity(changed, archives)
        for rows in ([record], [{'Main': True, 'Path': 'example.com/wrong'}],
                     [{'Main': True, 'Path': WORKER}, {**record, 'Version': 'v0.9.0'}]):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                dependencies.dependency_graph(rows, WORKER, archives)

    def campaign(self, root, failure=None):
        bundle = root / 'source.tar'; manifest = self.bundle(bundle)
        compiler = root / 'go'; compiler.write_bytes(b'compiler fixture; never executed')
        out = root / 'prepared'; archives = {}; downloads = {}; calls = []

        def archive_fixture(archiver, proxy, module, version, directory, source_time):
            destination = proxy / module / '@v'; destination.mkdir(parents=True)
            archive = destination / (version + '.zip')
            with zipfile.ZipFile(archive, 'w') as stream:
                for name in ('go.mod', 'go.sum'):
                    stream.writestr(module + '@' + version + '/' + name, (directory / name).read_bytes())
            (destination / (version + '.mod')).write_bytes((directory / 'go.mod').read_bytes())
            (destination / (version + '.info')).write_text(json.dumps({'Version': version, 'Time': source_time}))
            record = {'Path': module, 'Version': version, 'Sum': FIXTURE_SUM, 'GoModSum': FIXTURE_SUM,
                      'ArchiveSHA256': release.sha(archive), 'Requires': {CORE: SDK_VERSION} if module == WORKER else {}}
            archives[module] = record
            return record

        def go_fixture(go, arguments, *, cwd, env, out, label):
            calls.append({'arguments': arguments, 'env': dict(env), 'cwd': cwd})
            if arguments[0] == 'env':
                return json.dumps({'GOVERSION': release.RECIPE['go_version'], 'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64'})
            if arguments[:3] == ['mod', 'edit', '-json']:
                module = next(module for module, relative in dependencies.MODULES.items()
                              if (out / 'source' / relative).resolve() == cwd.resolve())
                return json.dumps({'Module': {'Path': module}, 'Go': '1.26.0',
                                   'Require': [] if module == CORE else [{'Path': CORE, 'Version': SDK_VERSION}]})
            if arguments[:3] == ['mod', 'download', '-json'] and arguments[-1] != 'all':
                for module, record in archives.items():
                    version = record['Version']
                    source = out / 'proxy' / module / '@v'
                    target = out / 'modules/cache/download' / module / '@v'; target.mkdir(parents=True)
                    result = {key: record[key] for key in ('Path', 'Version', 'Sum', 'GoModSum')}
                    for key, suffix in (('Info', '.info'), ('GoMod', '.mod'), ('Zip', '.zip')):
                        destination = target / (version + suffix)
                        shutil.copyfile(source / (version + suffix), destination)
                        result[key] = str(destination)
                    if failure == 'archive':
                        Path(result['Zip']).write_bytes(b'wrong downloaded payload')
                    downloads[module] = result
                return '\n'.join(json.dumps(record) for record in downloads.values())
            if arguments[:3] == ['mod', 'download', '-json']:
                if failure == 'source-change':
                    (cwd / 'go.sum').write_text('fixture: checksums were added by the Go command')
                return json.dumps(downloads[CORE]) if cwd != out / 'source/sdk/go' else ''
            if arguments == ['mod', 'verify']:
                self.assertEqual(env['GONOSUMDB'], '')
                self.assertEqual(env['GOPROXY'], release.RECIPE['environment']['GOPROXY'])
                if failure == 'verify':
                    raise subprocess.CalledProcessError(1, ['fixture-go', *arguments])
                return 'all modules verified\n'
            if arguments == ['list', '-mod=readonly', '-m', '-json', 'all']:
                module = next(module for module, relative in dependencies.MODULES.items()
                              if (out / 'source' / relative).resolve() == cwd.resolve())
                rows = [{'Path': module, 'Main': True}]
                if module != CORE:
                    record = {key: downloads[CORE][key] for key in ('Path', 'Version', 'Sum', 'GoModSum')}
                    if failure == 'graph':
                        record['Version'] = 'v0.9.0'
                    rows.append(record)
                return '\n'.join(json.dumps(row) for row in rows)
            self.fail('Unexpected Go command: ' + str(arguments))

        with patch.object(dependencies, 'build_archiver', return_value=Path('/fixture/modulezip')), \
                patch.object(dependencies, 'stage_module', side_effect=archive_fixture), \
                patch.object(dependencies, 'go_output', side_effect=go_fixture):
            if failure:
                with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                    dependencies.bootstrap(bundle, compiler, WORKER_VERSION, out)
            else:
                dependencies.bootstrap(bundle, compiler, WORKER_VERSION, out)
        return manifest, out, json.loads((out / dependencies.REPORT).read_text()), calls

    def test_bootstrap_records_private_identity_and_returns_to_release_checksum_policy(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest, out, report, calls = self.campaign(Path(directory))
            self.assertEqual(report['status'], 'pass')
            self.assertFalse(report['public_download_qualified'])
            self.assertFalse(report['release_qualified'])
            self.assertEqual(set(report['graphs']), set(dependencies.MODULES))
            self.assertEqual(report['private_modules'][CORE]['Version'], SDK_VERSION)
            self.assertEqual(report['private_modules'][WORKER]['Version'], WORKER_VERSION)
            self.assertEqual(report['requirements'][release.RECIPE['module']][CORE], SDK_VERSION)
            self.assertEqual(report['requirements'][WORKER][CORE], SDK_VERSION)
            self.assertEqual(report['requirements'][CORE], {})
            self.assertEqual(report['release_identity'], release.evidence_identity(manifest))
            selected = dependencies.cache_environment(out, manifest)
            self.assertEqual(selected['GOMODCACHE'], str(out / 'modules'))
            self.assertTrue(all(call['env']['GOWORK'] == 'off' for call in calls))
            self.assertEqual(dependencies.source_changes(out / 'source', manifest['source_files']), [])
            with patch.object(dependencies, 'go_output', side_effect=AssertionError('Go reached')):
                with self.assertRaises(FileExistsError):
                    dependencies.bootstrap(Path(directory) / 'source.tar', Path(directory) / 'go', WORKER_VERSION, out)

    def test_changed_inputs_failed_verification_and_wrong_downloads_cannot_pass(self):
        for failure in ('source-change', 'archive', 'verify', 'graph'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                manifest, out, report, calls = self.campaign(Path(directory), failure)
                self.assertEqual(report['status'], 'input_changes' if failure == 'source-change' else 'failed')
                with self.assertRaises(ValueError):
                    dependencies.cache_environment(out, manifest)
                if failure == 'source-change':
                    self.assertIn('go.sum', report['changed_inputs'])
                    self.assertFalse(any(call['arguments'] == ['mod', 'verify'] for call in calls))

    def test_reuse_rejects_changed_source_policy_inventory_and_cache_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest, out, report, _ = self.campaign(Path(directory))
            changed = copy.deepcopy(manifest)
            changed['source_files']['main.go']['sha256'] = 'f' * 64
            with self.assertRaisesRegex(ValueError, 'frozen release inputs'):
                dependencies.cache_environment(out, changed)
            mutations = [lambda r: r.update(status='failed'),
                         lambda r: r['verification_environment'].update(GONOSUMDB='*'),
                         lambda r: r.update(private_cache_files={}),
                         lambda r: r['private_cache_files'].pop(next(iter(r['private_cache_files']))),
                         lambda r: r['private_cache_files'].update({'../escape': 'f' * 64})]
            for mutate in mutations:
                with self.subTest(mutate=mutate):
                    altered = copy.deepcopy(report); mutate(altered)
                    (out / dependencies.REPORT).write_bytes(release.encoded(altered))
                    with self.assertRaises(ValueError):
                        dependencies.cache_environment(out, manifest)
            (out / dependencies.REPORT).write_bytes(release.encoded(report))
            cached = out / 'modules' / next(iter(report['private_cache_files']))
            cached.write_bytes(b'changed after preparation')
            with self.assertRaisesRegex(ValueError, 'cache bytes have changed'):
                dependencies.cache_environment(out, manifest)

    def test_invalid_source_fails_before_compiler_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); bundle = root / 'source.tar'
            with tarfile.open(bundle, 'w'):
                pass
            with patch.object(dependencies, 'go_output', side_effect=AssertionError('Go reached')):
                with self.assertRaises(KeyError):
                    dependencies.bootstrap(bundle, root / 'go', WORKER_VERSION, root / 'out')
            self.assertEqual(json.loads((root / 'out' / dependencies.REPORT).read_text())['status'], 'failed')

    def test_missing_source_still_records_preparation_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with patch.object(dependencies, 'go_output', side_effect=AssertionError('Go reached')):
                with self.assertRaises(FileNotFoundError):
                    dependencies.bootstrap(root / 'missing.tar', root / 'go', WORKER_VERSION, root / 'out')
            report = json.loads((root / 'out' / dependencies.REPORT).read_text())
            self.assertEqual(report['status'], 'failed')
            self.assertFalse(report['release_qualified'])

    @unittest.skipIf(os.name == 'nt', 'Windows does not expose Unix executable permissions')
    def test_source_freeze_detects_executable_permission_changes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); source = root / 'install.sh'
            source.write_bytes(b'#!/bin/sh\nexit 0\n'); source.chmod(0o755)
            inventory = {'install.sh': {'mode': '100755', 'sha256': release.sha(source)}}
            self.assertEqual(dependencies.source_changes(root, inventory), [])
            source.chmod(0o644)
            self.assertEqual(dependencies.source_changes(root, inventory), ['install.sh'])

    def test_release_build_selects_prepared_cache_without_private_policy(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest, prepared, _, _ = self.campaign(Path(directory))
            source = prepared / 'source'
            output = Path(directory) / 'artifacts'
            with patch.object(release, 'ROOT', source), \
                    patch.object(release, 'check_manifest'), \
                    patch.object(release, 'validate_toolchain'), \
                    patch.object(release.subprocess, 'run') as commands, \
                    patch.object(release, 'stage') as stage:
                release.build('/fixture/go', manifest, output, ['linux_amd64'], dependency_dir=prepared)
            self.assertTrue(any(call.args[0][1] == 'build' for call in commands.call_args_list))
            for call in commands.call_args_list:
                env = call.kwargs['env']
                self.assertEqual(env['GOMODCACHE'], str(prepared / 'modules'))
                self.assertEqual(env['GOPATH'], str(prepared / 'go-path'))
                self.assertEqual(env['GONOSUMDB'], '')
                self.assertEqual(env['GOPROXY'], release.RECIPE['environment']['GOPROXY'])
            paths = stage.call_args.args[-1]
            self.assertEqual(paths['GOMODCACHE'], str(prepared / 'modules'))
            # Wrong source identity is rejected before a compiler is invoked.
            changed = copy.deepcopy(manifest); changed['version'] = 'v0.9.0'
            with patch.object(release, 'check_manifest'), \
                    patch.object(release, 'validate_toolchain', side_effect=AssertionError('compiler reached')):
                with self.assertRaises(ValueError):
                    release.build('/fixture/go', changed, output, ['linux_amd64'], dependency_dir=prepared)

    def test_source_rebuilds_forward_cache_and_keep_separate_build_caches(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); bundle = root / 'source.tar'; manifest = self.bundle(bundle)
            stage = root / 'stage'; stage.mkdir()
            (stage / 'RELEASE.json').write_bytes(release.encoded(manifest))
            prepared = root / 'dependencies'; report = root / 'result.json'
            arguments = ['source_rebuild.py', '--source', str(bundle), '--stage', str(stage),
                         '--go', '/fixture/go', '--dependencies', str(prepared), '--targets', 'linux_amd64',
                         '--out', str(report)]
            with patch.object(sys, 'argv', arguments), patch('sys.stdout', new_callable=io.StringIO), \
                    patch.object(source_rebuild.subprocess, 'run') as commands, \
                    patch.object(source_rebuild, 'compare', return_value={'fixture': 'not a binary comparison'}):
                source_rebuild.main()
            builds = [call for call in commands.call_args_list if 'build' in call.args[0]]
            self.assertEqual(len(builds), 2)
            caches = set()
            for call in builds:
                command = call.args[0]
                self.assertEqual(command[command.index('--dependencies') + 1], str(prepared))
                caches.add(call.kwargs['env']['GOCACHE'])
            self.assertEqual(len(caches), 2)
            altered = copy.deepcopy(manifest); altered['commit'] = 'f' * 40
            (stage / 'RELEASE.json').write_bytes(release.encoded(altered))
            with patch.object(sys, 'argv', arguments), \
                    patch.object(source_rebuild.subprocess, 'run', side_effect=AssertionError('artifact command reached')):
                with self.assertRaisesRegex(ValueError, 'reference release identity'):
                    source_rebuild.main()

    def test_source_only_command_validates_inputs_without_building(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); manifest_path = root / 'RELEASE.json'
            manifest = manifest_fixture(); manifest_path.write_bytes(release.encoded(manifest))
            output = root / 'artifacts'
            with patch.object(sys, 'argv', ['release.py', 'source', '--manifest', str(manifest_path), '--out', str(output)]), \
                    patch('sys.stdout', new_callable=io.StringIO), \
                    patch.object(release, 'check_manifest') as validate, \
                    patch.object(release, 'source_archive', return_value=output / 'source.tar.gz') as archive, \
                    patch.object(release, 'validate_toolchain', side_effect=AssertionError('compiler reached')):
                release.main()
            validate.assert_called_once_with(manifest)
            archive.assert_called_once_with(manifest, output)


if __name__ == '__main__':
    unittest.main()
