"""Preparation boundaries using real Git selection and filesystem changes."""
import json
import base64
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import prepare_dependencies as prepare


class PreparationTests(unittest.TestCase):
    def repository(self, directory):
        root = Path(directory) / 'checkout'
        root.mkdir()
        subprocess.run(['git', 'init', '-q', str(root)], check=True)
        (root / '.gitignore').write_text('ignored/\n')
        (root / 'go.mod').write_text('module cpra.invalid/fixture\n\ngo 1.25.0\n')
        (root / 'removed.go').write_text('package fixture\n')
        (root / 'run.sh').write_text('#!/bin/sh\n')
        (root / 'run.sh').chmod(0o755)
        subprocess.run(['git', 'add', '.'], cwd=root, check=True)
        return root

    def test_captures_current_edits_new_files_deletions_and_modes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.repository(directory)
            (root / 'go.mod').write_text('module cpra.invalid/edited\n\ngo 1.25.0\n')
            (root / 'removed.go').unlink()
            (root / 'new.go').write_text('package edited\n')
            (root / 'ignored').mkdir()
            (root / 'ignored' / 'private').write_text('must not copy')
            destination = Path(directory) / 'copy'
            before = prepare.source_inventory(root, destination)
            self.assertEqual(before, prepare.source_inventory(root))
            self.assertNotIn('removed.go', before)
            self.assertNotIn('ignored/private', before)
            self.assertEqual((destination / 'go.mod').read_bytes(), (root / 'go.mod').read_bytes())
            self.assertEqual((destination / 'new.go').read_bytes(), (root / 'new.go').read_bytes())
            self.assertTrue(before['run.sh']['executable'])
            self.assertEqual((destination / 'run.sh').stat().st_mode & 0o777, 0o700)

    def test_rejects_link_and_private_dotenv_inputs_without_reading_contents(self):
        for name in ('link', '.env.local'):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = self.repository(directory)
                if name == 'link':
                    (root / name).symlink_to(root / 'go.mod')
                else:
                    (root / name).write_text('TOKEN=must-not-log\n')
                with self.assertRaises(ValueError) as raised:
                    prepare.source_inventory(root)
                self.assertNotIn('must-not-log', str(raised.exception))

    def test_source_quota_and_changed_inputs_are_detected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.repository(directory)
            before = prepare.source_inventory(root)
            (root / 'go.mod').write_text('changed')
            self.assertNotEqual(before, prepare.source_inventory(root))
            with patch.object(prepare, 'MAX_SOURCE_BYTES', 1):
                with self.assertRaisesRegex(ValueError, 'byte quota'):
                    prepare.source_inventory(root)

    def test_only_module_file_proposals_are_allowed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.repository(directory)
            source = Path(directory) / 'copy'
            before = prepare.source_inventory(root, source)
            (source / 'go.sum').write_text('Go-generated contents supplied by caller\n')
            changes = prepare.proposals(source, before)
            self.assertEqual([item['path'] for item in changes], ['go.sum'])
            self.assertIsNone(changes[0]['before_sha256'])
            (source / 'removed.go').write_text('changed')
            with self.assertRaisesRegex(ValueError, 'non-module'):
                prepare.proposals(source, before)

    def test_module_contract_rejects_version_replacement_and_dependency_cycles(self):
        base = {'Module': {'Path': prepare.WORKER}, 'Go': '1.25.0',
                'Require': [{'Path': prepare.CORE, 'Version': 'v0.1.0-rc.1'}]}
        self.assertEqual(prepare.module_contract(base, prepare.WORKER, 'v0.1.0-rc.1'),
                         {prepare.CORE: 'v0.1.0-rc.1'})
        invalid = [dict(base, Go='1.27.1'), dict(base, Replace=[{}]),
                   dict(base, Exclude=[{}]), dict(base, Toolchain='go1.27.1'),
                   dict(base, Require=base['Require'] + [{'Path': prepare.MODULE, 'Version': 'v0.1.0'}])]
        for value in invalid:
            with self.subTest(value=value), self.assertRaises(ValueError):
                prepare.module_contract(value, prepare.WORKER, 'v0.1.0-rc.1')
        with self.assertRaises(ValueError):
            prepare.module_contract(base, prepare.WORKER, 'v0.2.0')

    def test_existing_output_is_never_overwritten_even_on_error(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.repository(directory)
            out = Path(directory) / 'retained'
            out.mkdir()
            original = b'{"status":"original"}\n'
            (out / 'report.json').write_bytes(original)
            with self.assertRaises(FileExistsError):
                prepare.prepare(sys.executable, out, 'v0.1.0-rc.1', root)
            self.assertEqual((out / 'report.json').read_bytes(), original)

    def retained_preparation(self, directory):
        root = self.repository(directory)
        retained = Path(directory) / 'retained'
        retained.mkdir()
        inputs = prepare.source_inventory(root, retained / 'source')
        prepare.write_json(retained / 'source-inputs.json', inputs)
        report = {'schema_version': 1, 'status': 'prepared', 'source_unchanged': True,
                  'evidence_class': 'working_tree_dependency_preparation',
                  'release_qualified': False, 'public_download_qualified': False,
                  'source_inputs_sha256': prepare.sha256(retained / 'source-inputs.json'),
                  'proposals': []}
        prepare.write_json(retained / 'report.json', report)
        return retained, inputs, report

    def test_verified_copy_contains_exact_original_and_proposed_module_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            retained, inputs, report = self.retained_preparation(directory)
            path = retained / 'source' / 'go.sum'
            path.write_text('fixture proposed module bytes\n')
            path.chmod(0o600)
            report['proposals'] = [{'path': 'go.sum', 'before_sha256': None,
                                    'after_sha256': prepare.sha256(path)}]
            prepare.write_json(retained / 'report.json', report)
            destination = Path(directory) / 'consumer-source'
            actual = prepare.copy_prepared_source(retained, destination)
            self.assertEqual(actual, report)
            self.assertEqual((destination / 'go.sum').read_bytes(), path.read_bytes())
            self.assertEqual((destination / 'go.mod').read_bytes(), (retained / 'source/go.mod').read_bytes())
            self.assertFalse((destination / '.git').exists())

    def test_retained_copy_rejects_content_inventory_modes_and_evidence_drift(self):
        mutations = ('content', 'extra', 'link', 'mode', 'report', 'inputs', 'proposal')
        for mutation in mutations:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                retained, inputs, report = self.retained_preparation(directory)
                source = retained / 'source'
                if mutation == 'content':
                    (source / 'go.mod').write_text('unexpected')
                elif mutation == 'extra':
                    (source / 'extra.go').write_text('package unexpected\n')
                elif mutation == 'link':
                    (source / 'linked.go').symlink_to(source / 'go.mod')
                elif mutation == 'mode':
                    (source / 'run.sh').chmod(0o600)
                elif mutation == 'report':
                    report['release_qualified'] = True
                    prepare.write_json(retained / 'report.json', report)
                elif mutation == 'inputs':
                    inputs['go.mod']['sha256'] = '0' * 64
                    prepare.write_json(retained / 'source-inputs.json', inputs)
                else:
                    report['proposals'] = [{'path': 'go.mod', 'before_sha256': '0' * 64,
                                            'after_sha256': inputs['go.mod']['sha256']}]
                    prepare.write_json(retained / 'report.json', report)
                with self.assertRaises(ValueError):
                    prepare.copy_prepared_source(retained, Path(directory) / 'consumer-source')

    def test_output_must_be_outside_source_and_version_is_explicit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.repository(directory)
            with self.assertRaisesRegex(ValueError, 'outside'):
                prepare.prepare(sys.executable, root / 'output', 'v0.1.0-rc.1', root)
            with self.assertRaisesRegex(ValueError, 'canonical'):
                prepare.prepare(sys.executable, Path(directory) / 'output', 'latest', root)

    def test_archives_cover_final_graph_and_checksum_refresh_is_explicit(self):
        for refresh in (False, True):
            with self.subTest(refresh=refresh), tempfile.TemporaryDirectory() as directory:
                root = self.repository(directory)
                version = 'v0.1.0-rc.1'
                stale = prepare.CORE + ' ' + version + ' h1:old-fixture-content\n'
                definition = prepare.CORE + ' ' + version + '/go.mod h1:fixture-definition\n'
                for module, relative in prepare.MODULES.items():
                    path = root / relative
                    path.mkdir(parents=True, exist_ok=True)
                    (path / 'go.mod').write_text('module ' + module + '\n\ngo 1.25.0\n')
                    (path / 'go.sum').write_text('leaf fixture\n' if module == prepare.CORE else stale + definition)
                subprocess.run(['git', 'add', '.'], cwd=root, check=True)
                subprocess.run(['git', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                                '-c', 'commit.gpgSign=false', 'commit', '-qm', 'Fixture'], cwd=root, check=True)
                original = prepare.source_inventory(root)
                out = Path(directory) / 'prepared'
                archives = {}

                def stage(archiver, proxy, module, selected_version, path, source_time):
                    content = (path / 'go.sum').read_bytes()
                    digest = 'h1:' + base64.b64encode(hashlib.sha256(content).digest()).decode()
                    archive = out / 'module-cache' / ('sdk.fixture' if module == prepare.CORE else 'worker.fixture')
                    archive.parent.mkdir(exist_ok=True)
                    archive.write_bytes(content)
                    record = {'Path': module, 'Version': selected_version, 'Sum': digest,
                              'GoModSum': 'h1:fixture-definition', 'ArchiveSHA256': prepare.sha256(archive),
                              'Requires': {} if module == prepare.CORE else {prepare.CORE: version},
                              'Zip': str(archive)}
                    archives[module] = record
                    return record

                def go_output(go, path, args, env, destination, name, commands):
                    if args == ['version']:
                        return 'go version go1.27.1 linux/amd64\n'
                    if args[:2] == ['mod', 'edit']:
                        module = next(module for module, relative in prepare.MODULES.items()
                                      if path == out / 'source' / relative)
                        value = {'Module': {'Path': module}, 'Go': '1.25.0',
                                 'Require': [] if module == prepare.CORE else [{'Path': prepare.CORE, 'Version': version}]}
                        result = json.dumps(value)
                        (destination / (name + '.stdout')).write_text(result)
                        return result
                    if name == 'sdk-tidy':
                        (path / 'go.sum').write_text('leaf fixture\n')
                    elif name in ('application-tidy', 'worker-tidy'):
                        contents = (path / 'go.sum').read_text()
                        if stale in contents:
                            raise ValueError('checksum mismatch in copied dependent module')
                        (path / 'go.sum').write_text(contents + prepare.CORE + ' ' + version + ' ' + archives[prepare.CORE]['Sum'] + '\n')
                    elif args == ['mod', 'download', '-json', 'all']:
                        with (path / 'go.sum').open('a') as stream:
                            stream.write('complete graph fixture\n')
                    elif name == 'private-module-download':
                        return '\n'.join(json.dumps(record) for record in archives.values())
                    return ''

                with patch.object(prepare, 'run_go', side_effect=go_output), \
                        patch.object(prepare, 'build_archiver', return_value=Path(sys.executable)), \
                        patch.object(prepare, 'stage_module', side_effect=stage):
                    if not refresh:
                        with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
                            prepare.prepare(sys.executable, out, version, root)
                    else:
                        report = prepare.prepare(sys.executable, out, version, root, refresh_sdk_checksum=True)
                        self.assertEqual(report['status'], 'prepared')
                        self.assertEqual(len(report['sdk_checksum_refresh']), 2)
                        for module, relative in prepare.MODULES.items():
                            self.assertIn('complete graph fixture\n', (out / 'source' / relative / 'go.sum').read_text())
                        sdk = out / 'source/sdk/go/go.sum'
                        self.assertEqual(Path(archives[prepare.CORE]['Zip']).read_bytes(), sdk.read_bytes())
                        worker = out / 'source/sdk/go/worker/go.sum'
                        self.assertEqual(Path(archives[prepare.WORKER]['Zip']).read_bytes(), worker.read_bytes())
                        self.assertIn(definition, worker.read_text())
                self.assertEqual(prepare.source_inventory(root), original)

    @unittest.skipIf(os.name == 'nt', 'process group boundary uses the WSL release host')
    def test_timed_out_command_is_terminal_and_retains_failed_logs(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory)
            records = []
            with self.assertRaisesRegex(ValueError, 'time budget'):
                prepare.run_go(sys.executable, out, ['-c', 'import time; time.sleep(10)'],
                               dict(os.environ), out, 'timeout', records, timeout=0.05)
            self.assertEqual(records[0]['status'], 'fail')
            self.assertIsInstance(records[0]['exit_code'], int)
            self.assertNotEqual(records[0]['exit_code'], 0)
            self.assertTrue((out / 'timeout.stdout').is_file())
            self.assertTrue((out / 'timeout.stderr').is_file())


if __name__ == '__main__':
    unittest.main()
