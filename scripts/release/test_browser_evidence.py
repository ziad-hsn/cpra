"""Retained browser-reader fixtures, not real browser or release qualification."""
import copy
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

import browser_evidence
import browser_record
import release
from test_evidence import manifest_fixture


def sha(data):
    return hashlib.sha256(data).hexdigest()


def event_lines(events):
    return b''.join((json.dumps(event, separators=(',', ':')) + '\n').encode() for event in events)


class BrowserEvidenceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.manifest = manifest_fixture()
        names = ('main_actions_browser_test.go', 'internal/httpserver/management_browser_test.go',
                 '.github/workflows/ci.yml', 'dashboard/build-manifest.json',
                 'dashboard/src/App.tsx', 'dashboard/public/favicon.svg', 'dashboard/package.json',
                 'dashboard/pnpm-lock.yaml', 'brand/palette.json', 'brand/dist/palette.css',
                 'LICENSES/dashboard.json', 'LICENSES/dashboard.txt',
                 'internal/httpserver/assets/index.html', 'scripts/dashboard/browser_campaign.py',
                 'scripts/release/browser_evidence.py', 'scripts/dashboard/browser/package.json')
        for name in names:
            self.manifest['source_files'][name] = {'mode': '100644', 'sha256': sha(name.encode())}
        self.paths, self.reports = [], []
        self.events = [{'Action': 'pass', 'Package': package, 'Test': name, 'Elapsed': 1.0}
                       for package, names in browser_evidence.TESTS.items() for name in names]
        self.events += [{'Action': 'pass', 'Package': package, 'Elapsed': 1.0}
                        for package in browser_evidence.TESTS]
        for compiler in (browser_evidence.MINIMUM_COMPILER, release.RECIPE['go_version']):
            for tag in ('', 'externaljobs'):
                path = self.root / (compiler + '-' + (tag or 'default') + '.json')
                directory = path.with_suffix('.artifacts')
                directory.mkdir()
                (directory / 'screenshots').mkdir()
                source = directory / 'source.json'
                source.write_bytes(release.encoded(browser_evidence.source_inputs(self.manifest)))
                log = directory / 'go.jsonl'
                served = {'Action': 'output', 'Package': 'github.com/ziad-hsn/cpra/internal/httpserver',
                          'Test': 'TestManagementBrowser',
                          'Output': 'dashboard assets index sha256=' +
                                    self.manifest['source_files']['internal/httpserver/assets/index.html']['sha256'] +
                                    '; compiler=' + compiler + '; platform=linux/amd64\n'}
                log.write_bytes(event_lines([served] + self.events))
                errors = directory / 'stderr.log'
                errors.write_bytes(b'')
                images = {}
                for name in browser_evidence.SCREENSHOTS:
                    image = directory / 'screenshots' / name
                    image.write_bytes(b'\x89PNG\r\n\x1a\nfixture screenshot')
                    images[image.relative_to(self.root).as_posix()] = self.record(image)
                race = compiler != browser_evidence.MINIMUM_COMPILER
                pattern = '^(' + '|'.join(name for names in browser_evidence.TESTS.values() for name in names) + ')$'
                command = ['/fixture/go', 'test', '-count=1', '-timeout=45m', '-json', '-tags=' + tag, '-run', pattern]
                command += (['-race'] if race else []) + ['.', './internal/httpserver']
                self.paths.append(path)
                self.reports.append({
                    'result': 'passed', 'release_qualified': False, 'exit_code': 0, 'source_changed': [],
                    'source_commit': self.manifest['commit'], 'workspace': {'path': 'off', 'sha256': None},
                    'required_tests': {package: list(names) for package, names in browser_evidence.TESTS.items()},
                    'go': compiler, 'tags': tag, 'race': race, 'command': command,
                    'node': release.RECIPE['frontend']['node_version'], 'playwright': '1.56.1',
                    'chromium': 'fixture Chromium 1', 'tool_sha256': {'go': sha(compiler.encode()),
                                                                    'node': '2' * 64, 'chromium': '3' * 64},
                    'recorded_at': '2026-09-30T10:00:00+00:00', 'seconds': 10.0,
                    'source_inventory': source.relative_to(self.root).as_posix(),
                    'source_inventory_sha256': release.sha(source),
                    'logs': {item.relative_to(self.root).as_posix(): self.record(item) for item in (log, errors)},
                    'test_results': copy.deepcopy(self.events), 'screenshots': images,
                })
        self.save()

    @staticmethod
    def record(path):
        return {'bytes': path.stat().st_size, 'sha256': release.sha(path)}

    def save(self):
        for path, report in zip(self.paths, self.reports):
            path.write_bytes(release.encoded(report))

    def verify(self):
        return browser_evidence.verify_browser_campaigns(self.manifest, self.paths)

    def rewrite_log(self, data, served=True):
        path = self.root / next(name for name in self.reports[0]['logs'] if name.endswith('go.jsonl'))
        output = {'Action': 'output', 'Package': 'github.com/ziad-hsn/cpra/internal/httpserver',
                  'Test': 'TestManagementBrowser',
                  'Output': 'dashboard assets index sha256=' +
                            self.manifest['source_files']['internal/httpserver/assets/index.html']['sha256'] +
                            '; compiler=' + self.reports[0]['go'] + '; platform=linux/amd64\n'}
        path.write_bytes((event_lines([output]) if served else b'') + data)
        self.reports[0]['logs'][path.relative_to(self.root).as_posix()] = self.record(path)
        self.save()

    def reject(self, mutation, message=None):
        original = copy.deepcopy(self.reports[0])
        mutation(self.reports[0])
        self.save()
        try:
            with self.assertRaisesRegex(ValueError, message or '[Bb]rowser'):
                self.verify()
        finally:
            self.reports[0] = original
            self.save()

    def test_both_compilers_and_tags_match_retained_inputs_but_do_not_authorize_release(self):
        report = self.verify()
        self.assertEqual(report['status'], 'consistent_pending_review')
        self.assertFalse(report['release_gate_passed'])
        self.assertEqual(report['release_identity'], release.evidence_identity(self.manifest))
        self.assertEqual(len(report['reports']), 4)
        self.assertEqual(len(report['input_sha256']), 44)

    def test_archive_producer_inventory_matches_reader_and_tracks_dashboard_notices(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            manifest = copy.deepcopy(self.manifest)
            assets = json.loads((release.ROOT / 'dashboard/build-manifest.json').read_text())
            for section in ('inputs', 'outputs'):
                for name in assets[section]:
                    manifest['source_files'][name] = {'mode': '100644', 'sha256': sha(name.encode())}
            for name in ('brand/README.md', 'brand/dist/svg/ignored.txt', 'brand/fonts/archive/old.ttf'):
                manifest['source_files'][name] = {'mode': '100644', 'sha256': sha(name.encode())}
            for name, record in manifest['source_files'].items():
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                data = name.encode()
                path.write_bytes(data)
                record['sha256'] = sha(data)
            with patch.object(browser_record.browser_campaign.subprocess, 'check_output',
                              side_effect=AssertionError('source archive invoked Git')):
                observed = browser_record.browser_campaign.source_inventory(root, manifest['source_files'])
                self.assertEqual(observed, browser_evidence.source_inputs(manifest))
                for name in ('brand/LICENSE', 'brand/ATTRIBUTION.md', 'brand/fonts/LICENSE.txt',
                             'brand/fonts/RobotoSlab-Bold.ttf'):
                    self.assertIn(name, observed)
                    manifest['source_files'][name]['sha256'] = '0' * 64
                    self.assertNotEqual(observed, browser_evidence.source_inputs(manifest))
                    manifest['source_files'][name]['sha256'] = observed[name]
                self.assertNotIn('brand/README.md', observed)
                self.assertNotIn('brand/dist/svg/ignored.txt', observed)
                self.assertNotIn('brand/fonts/archive/old.ttf', observed)
                (root / 'LICENSES/dashboard.txt').write_bytes(b'updated dashboard notices')
                changed = browser_record.browser_campaign.source_inventory(root, manifest['source_files'])
            self.assertNotEqual(observed['LICENSES/dashboard.txt'], changed['LICENSES/dashboard.txt'])

    def test_each_matrix_identity_must_be_distinct_and_supported(self):
        for paths in (self.paths[:3], self.paths + [self.paths[0]], [self.paths[0]] * 4):
            with self.subTest(paths=paths), self.assertRaisesRegex(ValueError, 'four distinct'):
                browser_evidence.verify_browser_campaigns(self.manifest, paths)
        for mutation in (
            lambda r: r.update(go='go1.27.2'), lambda r: r.update(tags='redis'),
            lambda r: r.update(race=True), lambda r: r.update(race=0),
        ):
            with self.subTest(mutation=mutation):
                self.reject(mutation, 'coverage|identity')

    def test_same_commit_with_changed_source_is_rejected(self):
        self.manifest['source_files']['main.go']['sha256'] = 'f' * 64
        with self.assertRaisesRegex(ValueError, 'complete frozen source'):
            self.verify()

    def test_complete_source_inventory_cannot_drop_or_add_inputs(self):
        path = self.root / self.reports[0]['source_inventory']
        original = path.read_bytes()
        for mutate in (lambda values: values.pop('main.go'),
                       lambda values: values.update({'unexpected.go': '4' * 64}),
                       lambda values: values.update({'main.go': '5' * 64})):
            with self.subTest(mutation=mutate):
                values = json.loads(original)
                mutate(values)
                path.write_bytes(release.encoded(values))
                self.reports[0]['source_inventory_sha256'] = release.sha(path)
                self.save()
                with self.assertRaisesRegex(ValueError, 'complete frozen source'):
                    self.verify()
        path.write_bytes(original)

    def test_source_selection_covers_real_frontend_and_browser_contract_inputs(self):
        required = browser_evidence.source_inputs(self.manifest)
        self.assertIn('dashboard/pnpm-lock.yaml', required)
        self.assertIn('dashboard/src/App.tsx', required)
        self.assertIn('dashboard/public/favicon.svg', required)
        self.assertIn('scripts/release/browser_evidence.py', required)
        self.manifest['source_files']['docs/unrelated.md'] = {'mode': '100644', 'sha256': '6' * 64}
        self.manifest['source_files']['dashboard/build-manifest.json'] = {'mode': '120000', 'sha256': '7' * 64}
        with self.assertRaisesRegex(ValueError, 'missing browser or dashboard inputs'):
            browser_evidence.source_inputs(self.manifest)

    def test_summary_flags_and_tooling_cannot_substitute_for_evidence(self):
        mutations = (
            lambda r: r.update(result='failed'), lambda r: r.update(result='running'),
            lambda r: r.update(release_qualified=True), lambda r: r.update(exit_code=True),
            lambda r: r.update(exit_code=1), lambda r: r.update(source_changed=['main.go']),
            lambda r: r.update(source_commit='b' * 40), lambda r: r.pop('workspace'),
            lambda r: r.update(workspace={'path': '/dev/go.work', 'sha256': '8' * 64}),
            lambda r: r.update(required_tests={}), lambda r: r.update(node='v1.0.0'),
            lambda r: r.update(playwright='1.0.0'), lambda r: r.update(chromium=''),
            lambda r: r.update(tool_sha256={}), lambda r: r['tool_sha256'].update(go='invalid'),
            lambda r: r.update(recorded_at='2026-09-30'), lambda r: r.update(recorded_at=True),
            lambda r: r.update(seconds=0), lambda r: r.update(seconds=True),
        )
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                self.reject(mutate)

    def test_recorded_command_must_cover_exact_test_selection_and_race(self):
        for mutate in (lambda r: r['command'].remove('-count=1'),
                       lambda r: r['command'].__setitem__(7, '^TestManagementBrowser$'),
                       lambda r: r['command'].append('-short'),
                       lambda r: r['command'].__setitem__(0, 'go')):
            with self.subTest(mutation=mutate):
                self.reject(mutate, 'invocation')

    def test_compiler_and_browser_bytes_must_remain_consistent_across_entries(self):
        self.reports[1]['tool_sha256']['go'] = 'a' * 64
        self.save()
        with self.assertRaisesRegex(ValueError, 'different tool bytes'):
            self.verify()
        self.reports[1]['tool_sha256']['go'] = self.reports[0]['tool_sha256']['go']
        self.reports[1]['tool_sha256']['chromium'] = 'b' * 64
        self.save()
        with self.assertRaisesRegex(ValueError, 'changed between matrix'):
            self.verify()

    def test_lost_or_tampered_raw_payload_cannot_pass(self):
        path = self.root / next(name for name in self.reports[0]['logs'] if name.endswith('go.jsonl'))
        path.write_bytes(path.read_bytes() + b'\n')
        with self.assertRaisesRegex(ValueError, 'raw browser log bytes changed'):
            self.verify()

    def test_missing_raw_logs_and_summary_only_results_are_rejected(self):
        self.reject(lambda r: r.update(logs={}), 'not retained')
        self.reject(lambda r: r['test_results'].pop(), 'contradicts')

    def test_failed_or_skipped_go_events_defeat_a_passing_summary(self):
        for action in ('fail', 'skip', 'build-fail'):
            with self.subTest(action=action):
                events = copy.deepcopy(self.events)
                events[0]['Action'] = action
                self.rewrite_log(event_lines(events))
                with self.assertRaisesRegex(ValueError, 'failed or skipped|did not compile'):
                    self.verify()

    def test_duplicate_or_missing_terminal_completion_is_rejected(self):
        for events in (self.events + [self.events[0]], self.events[:-1], self.events[1:]):
            with self.subTest(events=events):
                self.rewrite_log(event_lines(events))
                with self.assertRaisesRegex(ValueError, 'duplicate terminal|required browser'):
                    self.verify()

    def test_unexpected_passed_test_cannot_expand_coverage_silently(self):
        events = self.events + [{'Action': 'pass', 'Package': release.RECIPE['module'],
                                 'Test': 'TestOther', 'Elapsed': 1.0}]
        self.rewrite_log(event_lines(events))
        with self.assertRaisesRegex(ValueError, 'unexpected test coverage'):
            self.verify()

    def test_served_dashboard_observation_and_compiler_must_match_frozen_inputs(self):
        self.rewrite_log(event_lines(self.events), served=False)
        with self.assertRaisesRegex(ValueError, 'served dashboard'):
            self.verify()
        for output in ('dashboard assets index sha256=' + 'a' * 64 + '; compiler=go1.26.0; platform=linux/amd64',
                       'dashboard assets index sha256=invalid; compiler=go1.26.0; platform=linux/amd64',
                       'dashboard assets index sha256=' +
                       self.manifest['source_files']['internal/httpserver/assets/index.html']['sha256'] +
                       '; compiler=go9.0.0; platform=linux/amd64'):
            with self.subTest(output=output):
                observed = {'Action': 'output', 'Package': 'github.com/ziad-hsn/cpra/internal/httpserver',
                            'Test': 'TestManagementBrowser', 'Output': output}
                self.rewrite_log(event_lines([observed] + self.events), served=False)
                with self.assertRaisesRegex(ValueError, 'served dashboard'):
                    self.verify()

    def test_subtests_still_require_their_parent_and_package_completion(self):
        events = self.events + [dict(self.events[0], Test=self.events[0]['Test'] + '/subtest')]
        self.rewrite_log(event_lines(events))
        self.reports[0]['test_results'] = events
        self.save()
        self.assertEqual(self.verify()['status'], 'consistent_pending_review')

    def test_invalid_raw_json_event_and_nonfinite_numbers_are_rejected(self):
        for data in (b'"output"\n', b'{"Action":"pass","Action":"skip"}\n',
                     b'{"Action":"output","Elapsed":NaN}\n', b'{"Action":"output","Elapsed":1e999}\n',
                     b'{"Action":"output","Package":null}\n', b'{"Action":"unknown"}\n'):
            with self.subTest(data=data):
                self.rewrite_log(data)
                with self.assertRaises(ValueError):
                    self.verify()

    def test_per_line_and_total_raw_output_limits_are_enforced(self):
        data = event_lines(self.events)
        self.rewrite_log(data)
        with patch.object(browser_evidence, 'MAX_LINE_BYTES', 10), self.assertRaisesRegex(ValueError, 'byte limit'):
            self.verify()
        with patch.object(browser_evidence, 'MAX_LOG_BYTES', 20), self.assertRaisesRegex(ValueError, 'byte limit'):
            self.verify()

    def test_terminal_event_count_is_bounded(self):
        with patch.object(browser_evidence, 'MAX_TERMINAL_EVENTS', 2), self.assertRaisesRegex(ValueError, 'too many'):
            self.verify()

    def test_missing_tampered_or_mislabeled_screenshots_are_rejected(self):
        self.reject(lambda r: r['screenshots'].pop(next(iter(r['screenshots']))), 'not retained')
        name = next(iter(self.reports[0]['screenshots']))
        path = self.root / name
        path.write_bytes(b'not a PNG screenshot')
        with self.assertRaisesRegex(ValueError, 'screenshot bytes changed'):
            self.verify()
        self.reports[0]['screenshots'][name] = self.record(path)
        self.save()
        with self.assertRaisesRegex(ValueError, 'not PNG'):
            self.verify()

    def test_retained_paths_cannot_escape_or_follow_symlinks(self):
        self.reject(lambda r: r.update(source_inventory='../source.json'), 'retained layout')
        path = self.root / self.reports[0]['source_inventory']
        outside = self.root / 'original-source.json'
        path.rename(outside)
        path.symlink_to(outside)
        with self.assertRaisesRegex(ValueError, 'symlink component'):
            self.verify()

    def test_report_and_artifact_directory_symlinks_are_rejected(self):
        path = self.paths[0]
        original = path.with_suffix('.original')
        path.rename(original)
        path.symlink_to(original)
        with self.assertRaisesRegex(ValueError, 'not a regular file'):
            self.verify()
        path.unlink()
        original.rename(path)
        directory = path.with_suffix('.artifacts')
        original_directory = directory.with_suffix('.original')
        directory.rename(original_directory)
        directory.symlink_to(original_directory, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, 'symlink component'):
            self.verify()

    def test_inputs_cannot_change_during_reader_execution(self):
        original = browser_evidence.hash_file
        calls = {}
        def changing(path, limit):
            calls[path] = calls.get(path, 0) + 1
            if path == self.paths[0] and calls[path] == 2:
                path.write_bytes(path.read_bytes() + b'\n')
            return original(path, limit)
        with patch.object(browser_evidence, 'hash_file', side_effect=changing), self.assertRaisesRegex(ValueError, 'changed during'):
            self.verify()

    def test_cli_preserves_exclusive_private_reports_and_invalid_attempts(self):
        manifest_path = self.root / 'RELEASE.json'
        manifest_path.write_bytes(release.encoded(self.manifest))
        output = self.root / 'checked.json'
        args = ['browser_evidence.py', '--release', str(manifest_path), '--out', str(output)]
        for path in self.paths:
            args += ['--campaign', str(path)]
        with patch.object(sys, 'argv', args):
            self.assertEqual(browser_evidence.main(), 0)
        original = output.read_bytes()
        self.assertEqual(output.stat().st_mode & 0o777, 0o600)
        with patch.object(sys, 'argv', args), self.assertRaises(FileExistsError):
            browser_evidence.main()
        self.assertEqual(output.read_bytes(), original)
        invalid_output = self.root / 'invalid.json'
        self.reports[0]['workspace']['path'] = '/developer/go.work'
        self.save()
        args[args.index(str(output))] = str(invalid_output)
        with patch.object(sys, 'argv', args):
            self.assertEqual(browser_evidence.main(), 1)
        invalid = json.loads(invalid_output.read_text())
        self.assertEqual(invalid['status'], 'invalid_evidence')
        self.assertFalse(invalid['release_gate_passed'])


if __name__ == '__main__':
    unittest.main()
