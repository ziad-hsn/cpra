"""Preparation boundaries using real Git selection and filesystem changes."""
import json
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
