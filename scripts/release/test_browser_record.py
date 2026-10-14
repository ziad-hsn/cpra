"""Recorder orchestration fixtures; no browser/server qualification occurs here."""
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import browser_record
import release
from test_accessibility_evidence import scanner_fixture
from test_evidence import manifest_fixture


class BrowserRecordTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name)
        self.source = self.base / 'source'; self.source.mkdir()
        self.dependencies = self.base / 'dependencies'; self.dependencies.mkdir()
        (self.dependencies / browser_record.dependencies.REPORT).write_bytes(b'{"synthetic":true}')
        self.scanner = scanner_fixture()
        self.manifest = manifest_fixture()
        self.manifest['source_files'] = {}
        for name, data in [('go.mod', b'module fixture\n'), ('main.go', b'package main\n'),
                           ('go.sum', b''), ('sdk/go/go.mod', b'module sdk-fixture\n'), ('sdk/go/go.sum', b''),
                           ('sdk/go/worker/go.mod', b'module worker-fixture\n'), ('sdk/go/worker/go.sum', b''),
                           ('scripts/dashboard/browser/package-lock.json', self.scanner['lock'])]:
            path = self.source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
            self.manifest['source_files'][name] = {'mode': '100644', 'sha256': release.sha(path)}
        self.release_path = self.base / 'RELEASE.json'; self.save_manifest()
        self.module = self.base / 'modules' / 'playwright'; self.module.mkdir(parents=True)
        scanner = self.module.parent / 'axe-core'; scanner.mkdir()
        (scanner / 'package.json').write_bytes(self.scanner['package'])
        (scanner / 'axe.min.js').write_bytes(self.scanner['script'])
        self.args = SimpleNamespace(source_dir=self.source, out=self.base / 'browser.json',
            dependencies=self.dependencies, release=self.release_path, go='/fixture/go', node='/fixture/node',
            module=str(self.module), browser='/fixture/chromium', tags='', race=False, timeout='45m')
        self.after_delegate = None
        self.stack = self.enterContext(patch.object(browser_record.build, 'source_inputs',
                                                   side_effect=lambda manifest: manifest['source_files']))
        self.cache = self.enterContext(patch.object(browser_record.dependencies, 'cache_environment',
            return_value={'GOMODCACHE': str(self.dependencies / 'modules'), 'GOPATH': str(self.dependencies / 'go-path')}))
        self.download = self.enterContext(patch.object(browser_record, 'download_scanner', return_value=self.scanner['archive']))
        self.delegate = self.enterContext(patch.object(browser_record.browser_campaign, 'campaign', side_effect=self.execute))

    def save_manifest(self):
        self.release_path.write_bytes(release.encoded(self.manifest))

    def execute(self, args, **kwargs):
        directory = args.out.with_name(args.out.stem + '.artifacts')
        screenshots = directory / 'screenshots'; screenshots.mkdir(parents=True)
        scan = {'checkpoint': 'sign-in', 'scanner': '4.13.0', 'automatedOnly': True,
                'violations': [], 'needsReview': [], 'contrastPasses': [], 'passedRules': ['label']}
        (screenshots / 'accessibility-verify_browser-01-sign-in.json').write_bytes(release.encoded(scan))
        report = {'result': 'passed', 'release_qualified': False, 'workspace': {'path': 'off', 'sha256': None}}
        args.out.write_bytes(release.encoded(report)); args.out.chmod(0o600)
        if self.after_delegate is not None: self.after_delegate()
        return report

    def test_records_original_scanner_and_cache_inputs_with_no_workspace(self):
        with patch.dict(os.environ, {'GOWORK': '/developer/work', 'CPRA_TOKEN': 'private', 'NODE_OPTIONS': 'bad'}):
            report = browser_record.record(self.args)
        self.assertEqual(report['result'], 'passed')
        self.assertFalse(report['release_qualified'])
        call = self.delegate.call_args
        self.assertEqual(call.kwargs['source_files'], self.manifest['source_files'])
        self.assertEqual(call.kwargs['source_commit'], self.manifest['commit'])
        self.assertEqual(call.kwargs['environment']['GOWORK'], 'off')
        for name in ('CPRA_TOKEN', 'NODE_OPTIONS'):
            self.assertNotIn(name, call.kwargs['environment'])
        self.assertEqual(len(report['accessibility']['scanner']), 4)
        for entry in report['accessibility']['scanner'].values():
            path = self.base / entry['file']
            self.assertEqual(release.sha(path), entry['sha256'])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads(self.args.out.read_bytes()), report)

    def test_changed_source_fails_before_any_browser_execution(self):
        (self.source / 'main.go').write_bytes(b'changed source')
        report = browser_record.record(self.args)
        self.assertEqual(report['result'], 'failed')
        self.assertIn('frozen release', report['error'])
        self.delegate.assert_not_called()
        self.download.assert_not_called()

    def test_changed_source_after_execution_preserves_failed_report(self):
        self.after_delegate = lambda: (self.source / 'main.go').write_bytes(b'changed while executing')
        report = browser_record.record(self.args)
        self.assertEqual(report['result'], 'failed')
        self.assertIn('source changed', report['error'])
        self.assertTrue((self.base / 'browser.artifacts/tools/axe-core.tgz').is_file())

    def test_wrong_scanner_archive_cannot_start_browser(self):
        self.download.return_value = b'corrupt archive'
        report = browser_record.record(self.args)
        self.assertEqual(report['result'], 'failed')
        self.assertIn('locked distribution', report['error'])
        self.delegate.assert_not_called()

    def test_modified_executed_scanner_invalidates_recording(self):
        self.after_delegate = lambda: (self.module.parent / 'axe-core/axe.min.js').write_bytes(b'replaced scanner')
        report = browser_record.record(self.args)
        self.assertEqual(report['result'], 'failed')
        self.assertIn('scanner changed', report['error'])

    def test_existing_results_and_concurrent_writers_are_never_overwritten(self):
        self.args.out.write_bytes(b'original record')
        with self.assertRaises(ValueError): browser_record.record(self.args)
        self.assertEqual(self.args.out.read_bytes(), b'original record')
        self.delegate.assert_not_called()
        self.args.out.unlink()
        def competing(*_, **__):
            self.args.out.write_bytes(b'other recorder owns this file')
            raise FileExistsError('another recorder created the output')
        self.delegate.side_effect = competing
        with self.assertRaises(FileExistsError): browser_record.record(self.args)
        self.assertEqual(self.args.out.read_bytes(), b'other recorder owns this file')

    def test_outputs_cannot_be_written_into_source_or_dependency_inputs(self):
        for root in (self.source, self.dependencies):
            self.args.out = root / 'output.json'
            with self.subTest(root=root), self.assertRaisesRegex(ValueError, 'separate'):
                browser_record.record(self.args)
            self.assertFalse(self.args.out.exists())


if __name__ == '__main__':
    unittest.main()
