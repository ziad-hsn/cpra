"""Private staging contracts; compiler execution is deliberately a fixture here."""
import base64
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import module_archive


class ModuleStagingTests(unittest.TestCase):
    def test_compiler_environment_has_no_workspace_or_developer_policy(self):
        with patch.dict(os.environ, {'GOFLAGS': '-race', 'GOTOOLCHAIN': 'auto',
                                     'GOWORK': '/developer/workspace', 'GOOS': 'plan9',
                                     'GONOSUMDB': '*', 'CPRA_AUTH_TOKEN': 'fixture-secret'}):
            env = module_archive.tool_environment('/compiler/go', Path('/scratch'))
        self.assertEqual(env['GOWORK'], 'off')
        self.assertEqual(env['GOTOOLCHAIN'], 'local')
        self.assertEqual(env['GONOSUMDB'], '')
        self.assertEqual(env['GOSUMDB'], 'sum.golang.org')
        self.assertEqual(env['GOMODCACHE'], '/scratch/module-cache')
        self.assertEqual(env['GOPATH'], '/scratch/go-path')
        self.assertNotIn('GOOS', env)
        self.assertNotIn('CPRA_AUTH_TOKEN', env)

    def test_staging_records_tool_hashes_and_refuses_existing_version(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); source = root / 'source'; source.mkdir()
            module = module_archive.CORE; version = 'v0.2.0-rc.1'
            content = ('module ' + module + '\n').encode()
            (source / 'go.mod').write_bytes(content)
            proxy = root / 'proxy'
            h1 = 'h1:' + base64.b64encode(bytes(32)).decode()

            def archiver_fixture(args, **kwargs):
                self.assertEqual(args[args.index('-module') + 1], module)
                self.assertEqual(args[args.index('-version') + 1], version)
                path = Path(args[args.index('-out') + 1])
                with zipfile.ZipFile(path, 'w') as archive:
                    archive.writestr(module + '@' + version + '/go.mod', content)
                return json.dumps({'Path': module, 'Version': version, 'Sum': h1, 'GoModSum': h1,
                                   'ArchiveSHA256': module_archive.sha256(path), 'Requires': {}})

            with patch.object(module_archive.subprocess, 'check_output', side_effect=archiver_fixture):
                report = module_archive.stage_module('/fixture/modulezip', proxy, module, version, source, '2026-09-29T00:00:00Z')
            destination = proxy / module / '@v'
            self.assertEqual((destination / (version + '.mod')).read_bytes(), content)
            self.assertEqual(report['ArchiveSHA256'], module_archive.sha256(destination / (version + '.zip')))
            self.assertEqual(json.loads((destination / (version + '.info')).read_text())['Time'], '2026-09-29T00:00:00Z')
            with patch.object(module_archive.subprocess, 'check_output', side_effect=AssertionError('archiver reached')):
                with self.assertRaisesRegex(ValueError, 'replace a staged'):
                    module_archive.stage_module('/fixture/modulezip', proxy, module, version, source, '2026-09-29T00:00:00Z')

    def test_mismatched_tool_identity_does_not_publish_proxy_files(self):
        for changed in ('Path', 'Version', 'ArchiveSHA256', 'Sum', 'GoModSum', 'Requires'):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary); source = root / 'source'; source.mkdir()
                module = module_archive.WORKER; version = 'v0.3.0'
                (source / 'go.mod').write_text('module ' + module + '\n')

                def archiver_fixture(args, **kwargs):
                    output = Path(args[args.index('-out') + 1]); output.write_bytes(b'fixture')
                    h1 = 'h1:' + base64.b64encode(bytes(32)).decode()
                    report = {'Path': module, 'Version': version, 'Sum': h1, 'GoModSum': h1,
                              'ArchiveSHA256': module_archive.sha256(output), 'Requires': {}}
                    report[changed] = None
                    return json.dumps(report)

                proxy = root / 'proxy'
                with patch.object(module_archive.subprocess, 'check_output', side_effect=archiver_fixture):
                    with self.assertRaises(ValueError):
                        module_archive.stage_module('/fixture/modulezip', proxy, module, version, source, '2026-09-29T00:00:00Z')
                self.assertEqual(list(proxy.rglob('*.zip')), [])
                self.assertEqual(list(proxy.rglob('*.mod')), [])

    def test_go_mod_change_during_archive_creation_cannot_publish_inconsistent_metadata(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); source = root / 'source'; source.mkdir()
            module = module_archive.CORE; version = 'v0.2.0'
            original = ('module ' + module + '\n\ngo 1.26.10\n').encode()
            (source / 'go.mod').write_bytes(original)

            def archiver_fixture(args, **kwargs):
                output = Path(args[args.index('-out') + 1])
                with zipfile.ZipFile(output, 'w') as archive:
                    archive.writestr(module + '@' + version + '/go.mod', original)
                # A writer changes the source after the tool has captured it.
                # The proxy must never combine that new .mod with the old ZIP.
                (source / 'go.mod').write_bytes(original + b'require example.com/changed v1.0.0\n')
                h1 = 'h1:' + base64.b64encode(bytes(32)).decode()
                return json.dumps({'Path': module, 'Version': version, 'Sum': h1, 'GoModSum': h1,
                                   'ArchiveSHA256': module_archive.sha256(output), 'Requires': {}})

            proxy = root / 'proxy'
            with patch.object(module_archive.subprocess, 'check_output', side_effect=archiver_fixture):
                with self.assertRaisesRegex(ValueError, 'go.mod changed'):
                    module_archive.stage_module('/fixture/modulezip', proxy, module, version, source, '2026-09-29T00:00:00Z')
            self.assertEqual(list(proxy.rglob('*.zip')), [])
            self.assertEqual(list(proxy.rglob('*.mod')), [])

    def test_committed_source_excludes_ignored_files_and_rejects_dirty_input(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); repo = root / 'repo'; repo.mkdir()
            subprocess.run(['git', 'init', '-q', str(repo)], check=True)
            (repo / '.gitignore').write_text('.env.local\nprivate.pem\n')
            (repo / 'embedded.bin').write_bytes(b'committed fixture bytes')
            subprocess.run(['git', '-C', str(repo), 'add', '.'], check=True)
            subprocess.run(['git', '-C', str(repo), '-c', 'user.name=CPRa test',
                            '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'archive fixture'], check=True)
            (repo / '.env.local').write_text('TOKEN=fixture')
            (repo / 'private.pem').write_text('ignored credential fixture')
            source = root / 'source'
            commit, epoch = module_archive.committed_source(repo, source)
            self.assertEqual(len(commit), 40)
            self.assertGreater(epoch, 0)
            self.assertEqual((source / 'embedded.bin').read_bytes(), b'committed fixture bytes')
            self.assertFalse((source / '.env.local').exists())
            self.assertFalse((source / 'private.pem').exists())
            (repo / 'new.go').write_text('package fixture\n')
            with self.assertRaisesRegex(ValueError, 'clean committed'):
                module_archive.committed_source(repo, root / 'dirty-source')
            self.assertFalse((root / 'dirty-source').exists())

    def test_archived_go_mod_must_match_source_even_when_tool_identity_matches(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); source = root / 'source'; source.mkdir()
            module = module_archive.CORE; version = 'v0.2.0'
            (source / 'go.mod').write_text('module ' + module + '\n')

            def archiver_fixture(args, **kwargs):
                output = Path(args[args.index('-out') + 1])
                with zipfile.ZipFile(output, 'w') as archive:
                    archive.writestr(module + '@' + version + '/go.mod', 'module example.com/different\n')
                h1 = 'h1:' + base64.b64encode(bytes(32)).decode()
                return json.dumps({'Path': module, 'Version': version, 'Sum': h1, 'GoModSum': h1,
                                   'ArchiveSHA256': module_archive.sha256(output), 'Requires': {}})

            proxy = root / 'proxy'
            with patch.object(module_archive.subprocess, 'check_output', side_effect=archiver_fixture):
                with self.assertRaisesRegex(ValueError, 'Archived go.mod differs'):
                    module_archive.stage_module('/fixture/modulezip', proxy, module, version, source, '2026-09-29T00:00:00Z')
            self.assertEqual(list(proxy.rglob('*.zip')), [])
            self.assertEqual(list(proxy.rglob('*.mod')), [])


if __name__ == '__main__':
    unittest.main()
