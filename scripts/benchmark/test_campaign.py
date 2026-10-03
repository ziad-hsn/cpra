import json
import io
import hashlib
import pathlib
import sys
import tempfile
import unittest
from unittest.mock import patch

import campaign
import preflight
import prepare
from test_evidence import manifest_fixture


class CampaignContracts(unittest.TestCase):
    def test_missing_physical_host_evidence_blocks_campaign(self):
        with patch.object(pathlib.Path, 'exists', return_value=False):
            result = preflight.inspect()
        self.assertFalse(result['ready'])
        self.assertGreaterEqual(result['required_host_free_bytes'], 30 * 1000 ** 3)

    def test_manifest_has_distinct_ids_and_fixed_cadence(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'monitors.json'
            campaign.manifest(path, 103, 'http://127.0.0.1:12345', 7)
            monitors = json.loads(path.read_text())['monitors']
        self.assertEqual(103, len(monitors))
        self.assertEqual(103, len({m['id'] for m in monitors}))
        self.assertEqual(103, len({m['pulse_check']['config']['url'] for m in monitors}))
        self.assertTrue(all(m['pulse_check']['interval'] == '60s' for m in monitors))
        self.assertEqual(100, sum('intervention' in m for m in monitors))

    def test_invalid_fixture_headroom_cannot_reduce_the_host_requirement(self):
        for value in (-1, -100 * 1024 ** 3, 1.5, None, True, float('inf'), float('nan')):
            with self.subTest(value=value), \
                    patch.object(preflight.subprocess, 'run', side_effect=AssertionError('host probe reached')):
                result = preflight.inspect(value)
                self.assertFalse(result['ready'])
                self.assertEqual(result['required_host_free_bytes'], 30 * 1024 ** 3)
                self.assertIsNone(result['required_fixture_headroom_bytes'])
                self.assertIn('non-negative', result['reason'])
                json.dumps(result, allow_nan=False)

    def test_cli_rejects_invalid_headroom_before_creating_an_output_directory(self):
        for value in ('-100', 'nan', 'inf'):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as directory:
                output = pathlib.Path(directory) / 'must-not-be-created'
                argv = ['campaign.py', '--mode', 'compare', '--out', str(output), '--fixture-headroom-gib', value]
                with patch.object(sys, 'argv', argv), patch('sys.stderr', new_callable=io.StringIO), \
                        patch.object(campaign, 'inspect', side_effect=AssertionError('host probe reached')):
                    with self.assertRaises(SystemExit) as failure:
                        campaign.main()
                self.assertEqual(failure.exception.code, 2)
                self.assertFalse(output.exists())

    def test_reusing_an_output_directory_cannot_overwrite_an_earlier_campaign(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            report = root / 'campaign.json'
            original = b'{"status":"incomplete","retained":"earlier evidence"}\n'
            report.write_bytes(original)
            with patch.object(sys, 'argv', ['campaign.py', '--mode', 'preflight', '--out', str(root)]), \
                    patch('sys.stderr', new_callable=io.StringIO), \
                    patch.object(campaign, 'inspect', side_effect=AssertionError('host probe reached')):
                with self.assertRaises(SystemExit) as failure:
                    campaign.main()
            self.assertEqual(failure.exception.code, 2)
            self.assertEqual(report.read_bytes(), original)

    def test_incomplete_comparison_stops_before_starting_another_trial(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            candidate, baseline, target = (root / name for name in ('candidate', 'baseline', 'target'))
            for path in (candidate, baseline, target):
                path.write_bytes(path.name.encode())
            manifest = manifest_fixture()
            manifest.update(source_date_epoch=1, source_date='1970-01-01T00:00:01Z')
            metadata = {'schema_version': 3, 'baseline_commit': campaign.BASELINE,
                        'release': manifest, 'release_identity': prepare.release_identity(manifest),
                        'recipe': prepare.RECIPE, 'tags': [],
                        'toolchain': {'version': prepare.RECIPE['go_version'], 'target': 'linux_amd64',
                                      'binary_sha256': 'd' * 64,
                                      'archive_sha256': prepare.TOOLCHAINS[prepare.RECIPE['go_version']]['linux_amd64']},
                        'builds': [
                {'name': name, 'commit': campaign.BASELINE if name == 'baseline' else 'a' * 40,
                 'dirty': False, 'go_version': prepare.RECIPE['go_version'],
                 'flags': prepare.build_flags([], manifest if name != 'baseline' else None),
                 'environment': prepare.recorded_environment('linux_amd64'),
                 'source_sha256': 'b' * 64, 'source_tree_sha256': 'c' * 64, 'source_date_epoch': 1,
                 'sha256': hashlib.sha256(path.read_bytes()).hexdigest()}
                for name, path in (('baseline', baseline), ('candidate', candidate), ('target', target))]}
            builds = root / 'builds.json'
            builds.write_text(json.dumps(metadata))
            output = root / 'campaign'
            argv = ['campaign.py', '--mode', 'compare', '--out', str(output), '--candidate', str(candidate),
                    '--baseline', str(baseline), '--target', str(target), '--builds', str(builds)]
            with patch.object(sys, 'argv', argv), patch.object(campaign, 'inspect', return_value={'ready': True}), \
                    patch.object(campaign, 'run_one', return_value={'status': 'incomplete'}) as run:
                self.assertEqual(campaign.main(), 1)
            self.assertEqual(run.call_count, 1)
            report = json.loads((output / 'campaign.json').read_text())
            self.assertEqual(report['status'], 'incomplete')
            self.assertEqual(report['planned_runs'], 18)
            self.assertEqual(len(report['runs']), 1)
            self.assertFalse(report['release_gate_passed'])

            target.write_bytes(b'unrecorded target binary')
            argv[argv.index('--out') + 1] = str(root / 'rejected')
            with patch.object(sys, 'argv', argv), patch('sys.stderr', new_callable=io.StringIO), \
                    patch.object(campaign, 'inspect', return_value={'ready': True}), \
                    patch.object(campaign, 'run_one', side_effect=AssertionError('workload reached')):
                with self.assertRaises(SystemExit) as failure:
                    campaign.main()
            self.assertEqual(failure.exception.code, 2)


if __name__ == '__main__':
    unittest.main()
