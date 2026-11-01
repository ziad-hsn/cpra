from contextlib import ExitStack
import hashlib
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import browser_campaign


class BrowserCampaignTests(unittest.TestCase):
    def results(self):
        return [{"Package": package, "Test": name, "Action": "pass"}
                for package, names in browser_campaign.TESTS.items() for name in names] + [
                    {"Package": package, "Action": "pass"} for package in browser_campaign.TESTS]

    def test_requires_every_test_to_pass(self):
        browser_campaign.verify_results(self.results())
        for action in ("skip", "fail"):
            with self.subTest(action=action):
                results = self.results()
                results[0]["Action"] = action
                with self.assertRaisesRegex(ValueError, "did not pass"):
                    browser_campaign.verify_results(results)
        with self.assertRaises(ValueError):
            browser_campaign.verify_results([])

    def test_requires_both_package_completions(self):
        for missing in browser_campaign.TESTS:
            events = [event for event in self.results() if not (event["Package"] == missing and "Test" not in event)]
            with self.subTest(package=missing), self.assertRaisesRegex(ValueError, "package completion"):
                browser_campaign.verify_results(events)

    def test_rejects_duplicate_or_contradictory_terminal_events(self):
        for action in ("pass", "fail", "skip"):
            events = self.results() + [dict(self.results()[0], Action=action)]
            with self.subTest(action=action), self.assertRaises(ValueError):
                browser_campaign.verify_results(events)

    def test_failed_child_cannot_be_hidden_by_parent_pass(self):
        events = self.results() + [dict(self.results()[0], Test=self.results()[0]["Test"] + "/child", Action="fail")]
        with self.assertRaisesRegex(ValueError, "did not pass"):
            browser_campaign.verify_results(events)

    def test_rejects_malformed_terminal_identity(self):
        for value in ([], {}, 1):
            with self.subTest(value=value), self.assertRaises(ValueError):
                browser_campaign.verify_results([{"Package": value, "Action": "pass"}])

    def test_strict_event_objects(self):
        for line in ('"log"', 'null', '[]', '{"Action":"pass","Action":"fail"}',
                     '{"Action":"pass","Elapsed":NaN}', '{"Action":"pass","Package":[]}',
                     '{"Action":"output","Output":false}'):
            with self.subTest(line=line), self.assertRaises(ValueError):
                browser_campaign.parse_event(line)
        self.assertEqual(browser_campaign.parse_event('{"Action":"output","Output":"\\\"log\\\""}')['Output'], '"log"')

    def test_environment_excludes_ambient_build_options_and_credentials(self):
        with patch.dict(os.environ, {"GOFLAGS": "-tags=externaljobs", "GOEXPERIMENT": "evil", "GOTOOLCHAIN": "auto",
                                    "NODE_OPTIONS": "--require=evil.js", "CPRA_TOKEN": "secret", "GH_TOKEN": "secret",
                                    "GOWORK": "off"}):
            env = browser_campaign.build_environment(Path('/module'), '/node', Path('/chrome'), Path('/screens'))
        self.assertEqual(env['GOFLAGS'], '-mod=readonly -p=1')
        self.assertEqual(env['GOTOOLCHAIN'], 'local')
        self.assertEqual(env['GOEXPERIMENT'], '')
        self.assertEqual(env['CPRA_BROWSER_REQUIRED'], '1')
        for name in ('NODE_OPTIONS', 'CPRA_TOKEN', 'GH_TOKEN'):
            self.assertNotIn(name, env)

    def test_failure_location_excludes_fixture_content_and_unregistered_sources(self):
        event = {'Action': 'output', 'Package': 'github.com/ziad-hsn/cpra',
                 'Test': 'TestMainManagementRecoveryReviewBrowser'}
        for output, source, line in (
            ('CPRA_BROWSER_FAILURE verify_actions_browser.cjs:314\n', 'verify_actions_browser.cjs', 314),
            ('    main_actions_browser_test.go:152: private-fixture-marker\n', 'main_actions_browser_test.go', 152),
        ):
            result = browser_campaign.failure_location(dict(event, Output=output))
            self.assertEqual(result, {'test': event['Test'], 'file': source, 'line': line})
            self.assertNotIn('private-fixture-marker', json.dumps(result))
        for output in ('CPRA_BROWSER_FAILURE secret-marker.cjs:12',
                       'CPRA_BROWSER_FAILURE verify_actions_browser.cjs:0',
                       'CPRA_BROWSER_FAILURE verify_actions_browser.cjs:123456',
                       'CPRA_BROWSER_FAILURE verify_actions_browser.cjs:314 private-fixture-marker',
                       '/private/path/main_actions_browser_test.go:152: private-fixture-marker'):
            self.assertIsNone(browser_campaign.failure_location(dict(event, Output=output)))
        for replacement in ({'Test': 'unregistered'}, {'Package': 'unregistered'}, {'Action': 'fail'}):
            self.assertIsNone(browser_campaign.failure_location(dict(event, Output='CPRA_BROWSER_FAILURE verify_actions_browser.cjs:314', **replacement)))

    def test_explicit_frozen_environment_does_not_inherit_developer_workspace(self):
        with patch.dict(os.environ, {'GOWORK': '/developer/work', 'GOMODCACHE': '/developer/cache'}):
            env = browser_campaign.build_environment(Path('/module'), '/node', Path('/chrome'), Path('/screens'),
                {'GOWORK': 'off', 'GOMODCACHE': '/private/cache', 'GOPATH': '/private/go-path'})
        self.assertEqual(env['GOWORK'], 'off')
        self.assertEqual(env['GOMODCACHE'], '/private/cache')
        self.assertEqual(env['GOPATH'], '/private/go-path')

    def test_source_archive_inventory_uses_recorded_inputs_without_git(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'main.go').write_bytes(b'package main')
            (root / 'go.mod').write_bytes(b'module fixture')
            with patch.object(browser_campaign.asset_manifest, 'snapshot', return_value={'inputs': {}, 'outputs': {}}), \
                    patch.object(browser_campaign.subprocess, 'check_output', side_effect=AssertionError('Git invoked')):
                values = browser_campaign.source_inventory(root, {'main.go': {}, 'go.mod': {}})
        self.assertEqual(values['main.go'], hashlib.sha256(b'package main').hexdigest())


class RecordedCampaignTests(unittest.TestCase):
    results = BrowserCampaignTests.results

    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.root = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        for name, value in (
            ('scripts/dashboard/browser/package.json', {'dependencies': {'playwright': '1.56.1'}}),
            ('scripts/release/recipe.json', {'minimum_go_version': 'go1.26.10', 'go_version': 'go1.27.3',
                                            'frontend': {'node_version': 'v24.21.0'}}),
            ('playwright/package.json', {'version': '1.56.1'}),
        ):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(json.dumps(value))
        for name in ('go', 'node', 'chrome'):
            (self.root / name).write_bytes(name.encode())
        self.args = SimpleNamespace(out=self.root/'report.json', module=str(self.root/'playwright'),
                                    node=str(self.root/'node'), go=str(self.root/'go'), browser=str(self.root/'chrome'),
                                    race=True, tags='', timeout='45m')
        self.events = self.results()
        self.go_version = 'go1.27.3'
        self.raw = None
        self.exit_code = 0
        self.images = True
        self.terminated = False
        self.source = self.stack.enter_context(patch.object(browser_campaign, 'source_inventory', return_value={'main.go': 'a'*64}))
        self.stack.enter_context(patch.object(browser_campaign.asset_manifest, 'verify'))
        self.stack.enter_context(patch.object(browser_campaign.shutil, 'which', side_effect=lambda value: value))
        self.stack.enter_context(patch.object(browser_campaign.subprocess, 'check_output', side_effect=self.tool_output))
        self.process = self.stack.enter_context(patch.object(browser_campaign.subprocess, 'Popen', side_effect=self.execute))
        self.stack.enter_context(patch.dict(os.environ, {'GOWORK': 'off'}))

    def tool_output(self, command, **_):
        if command[0] == 'git':
            return 'a'*40 + '\n'
        if 'GOVERSION' in command:
            return self.go_version + '\n'
        if command[0] == self.args.node:
            return 'v24.21.0\n'
        return 'Chromium fixture\n'

    def execute(self, command, **kwargs):
        if self.images:
            for name in browser_campaign.SCREENSHOTS:
                (Path(kwargs['env']['CPRA_BROWSER_EVIDENCE_DIR']) / name).write_bytes(b'\x89PNG\r\n\x1a\nfixture')
        raw = self.raw if self.raw is not None else b''.join(json.dumps(event).encode()+b'\n' for event in self.events)
        owner = self

        class Process:
            stdout = io.BytesIO(raw)

            def __enter__(self):
                return self

            def __exit__(self, *_):
                self.stdout.close()

            def wait(self, **_):
                return owner.exit_code

            def terminate(self):
                owner.terminated = True

        return Process()

    def run_campaign(self):
        report = browser_campaign.campaign(self.args, root=self.root)
        self.assertEqual(json.loads(self.args.out.read_text()), report)
        return report

    def test_retains_raw_logs_source_and_screenshots(self):
        report = self.run_campaign()
        self.assertEqual(report['result'], 'passed')
        self.assertFalse(report['release_qualified'])
        self.assertEqual(report['source_changed'], [])
        self.assertEqual(len(report['screenshots']), len(browser_campaign.SCREENSHOTS))
        self.assertEqual({Path(item).name for item in report['screenshots']}, set(browser_campaign.SCREENSHOTS))
        for name, entry in {**report['logs'], **report['screenshots']}.items():
            self.assertEqual(hashlib.sha256((self.root/name).read_bytes()).hexdigest(), entry['sha256'])
        if os.name != 'nt':
            self.assertEqual(self.args.out.stat().st_mode & 0o777, 0o600)
            self.assertEqual((self.root/'report.artifacts').stat().st_mode & 0o777, 0o700)

    def test_recorded_minimum_compiler_is_accepted(self):
        self.go_version = 'go1.26.10'
        self.args.race = False
        self.assertEqual(self.run_campaign()['result'], 'passed')
        self.process.assert_called_once()

    def test_unselected_compiler_fails_before_browser_execution(self):
        self.go_version = 'go1.25.0'
        report = self.run_campaign()
        self.assertEqual(report['result'], 'failed')
        self.assertIn('compiler', report['error'])
        self.process.assert_not_called()

    def test_existing_report_is_never_replaced(self):
        self.args.out.write_bytes(b'original evidence')
        with self.assertRaises(FileExistsError):
            self.run_campaign()
        self.assertEqual(self.args.out.read_bytes(), b'original evidence')
        self.process.assert_not_called()

    def test_existing_artifacts_are_never_adopted(self):
        (self.root/'report.artifacts').mkdir()
        with self.assertRaises(FileExistsError):
            self.run_campaign()
        self.assertFalse(self.args.out.exists())
        self.process.assert_not_called()

    def test_failed_command_is_retained(self):
        self.exit_code = 1
        report = self.run_campaign()
        self.assertEqual(report['result'], 'failed')
        self.assertEqual(report['exit_code'], 1)
        self.assertEqual(len(report['logs']), 2)
        self.assertTrue((self.root/'report.artifacts/go.jsonl').is_file())

    def test_public_failure_summary_uses_only_registered_test_identities(self):
        self.exit_code = 1
        self.events[0]['Action'] = 'fail'
        report = self.run_campaign()
        report['test_results'] += [
            {'Package': 'github.com/ziad-hsn/cpra', 'Test': 'secret-marker', 'Action': 'fail'},
            {'Package': 'secret-marker', 'Action': 'fail'},
            {'Package': 'github.com/ziad-hsn/cpra', 'Action': 'fail', 'Output': 'secret-marker'},
        ]
        report['logs']['secret-marker'] = {'sha256': 'secret-marker'}
        summary = browser_campaign.public_summary(report)
        self.assertEqual(summary['failed_tests'], [browser_campaign.TESTS['github.com/ziad-hsn/cpra'][0]])
        self.assertEqual(summary['failed_packages'], ['github.com/ziad-hsn/cpra'])
        self.assertNotIn('secret-marker', json.dumps(summary))

    def test_skipped_required_test_is_retained_as_failure(self):
        self.events[0]['Action'] = 'skip'
        report = self.run_campaign()
        self.assertEqual(report['result'], 'failed')
        self.assertIn('did not pass', report['error'])

    def test_failure_locations_are_bounded_and_only_published_for_failed_tests(self):
        name = 'TestMainManagementRecoveryReviewBrowser'
        output = {'Action': 'output', 'Package': 'github.com/ziad-hsn/cpra', 'Test': name}
        self.events = [dict(output, Output=f'CPRA_BROWSER_FAILURE verify_actions_browser.cjs:{line}\n')
                       for line in range(10, 30)] + self.events
        next(event for event in self.events if event.get('Test') == name and event['Action'] == 'pass')['Action'] = 'fail'
        self.exit_code = 1
        report = self.run_campaign()
        self.assertEqual(len(report['failure_locations']), 10)
        summary = browser_campaign.public_summary(report)
        self.assertEqual(summary['failure_locations'], report['failure_locations'])
        report['failure_locations'] = [dict(report['failure_locations'][0], file='private-marker'),
                                       dict(report['failure_locations'][0], line='private-marker'),
                                       dict(report['failure_locations'][0], output='private-marker')]
        self.assertEqual(browser_campaign.public_summary(report)['failure_locations'], [])
        report['failure_locations'] = summary['failure_locations']
        for event in report['test_results']:
            event['Action'] = 'pass'
        self.assertEqual(browser_campaign.public_summary(report)['failure_locations'], [])

    def test_invalid_json_line_cannot_disappear(self):
        self.raw = b'"logger output"\n'+b''.join(json.dumps(event).encode()+b'\n' for event in self.events)
        report = self.run_campaign()
        self.assertEqual(report['result'], 'failed')
        self.assertIn('invalid event', report['error'])
        self.assertTrue((self.root/'report.artifacts/go.jsonl').read_bytes().startswith(b'"logger output"'))

    def test_input_changes_fail_the_campaign(self):
        self.source.side_effect = [{'main.go': 'a'*64}, {'main.go': 'b'*64}]
        report = self.run_campaign()
        self.assertEqual(report['result'], 'failed')
        self.assertEqual(report['source_changed'], ['main.go'])

    def test_missing_screenshots_fail_the_campaign(self):
        self.images = False
        report = self.run_campaign()
        self.assertEqual(report['result'], 'failed')
        self.assertIn('screenshot', report['error'])

    def test_preflight_failure_is_retained(self):
        (self.root/'playwright/package.json').write_text('{"version":"wrong"}')
        report = self.run_campaign()
        self.assertEqual(report['result'], 'failed')
        self.assertIn('Playwright', report['error'])
        self.process.assert_not_called()

    def test_output_quota_stops_recording_and_fails(self):
        with patch.object(browser_campaign, 'MAX_LOG_BYTES', 8):
            report = self.run_campaign()
        self.assertEqual(report['result'], 'failed')
        self.assertTrue(self.terminated)
        self.assertIn('recording limit', report['error'])


if __name__ == "__main__":
    unittest.main()
