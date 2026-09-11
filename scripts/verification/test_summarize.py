"""Evidence aggregation must fail closed on corrupt, stale or mislabelled proof."""
import hashlib
import json
import pathlib
import re
import tempfile
import unittest

import summarize


def fixture_report(directory, name, passes, category='local_integration', boundary='effect'):
    path = pathlib.Path(directory) / (name + '.json')
    evidence_dir = pathlib.Path(str(path) + '.evidence')
    evidence_dir.mkdir()
    run_id = 'run-' + name
    records = []
    for kind, driver in summarize.INVENTORY:
        row = {'kind': kind, 'driver': driver, 'status': 'not_configured', 'configured': False}
        if (kind, driver) in passes:
            reference = kind + '-' + driver + '.jsonl'
            evidence = {'kind': kind, 'driver': driver, 'run_id': run_id, 'evidence_type': category,
                        'observation_boundary': boundary, 'accepted': True, 'observed': boundary == 'effect'}
            if boundary == 'effect':
                evidence.update(before={'count': 0}, after={'count': 1, 'observed': True})
            raw = json.dumps(evidence).encode()
            (evidence_dir / reference).write_bytes(raw)
            row.update(status='pass', configured=True, operation_invoked=True, accepted=True,
                       observed=boundary == 'effect', evidence_type=category, observation_boundary=boundary,
                       evidence_ref=reference, evidence_sha256=hashlib.sha256(raw).hexdigest(), duration_ms=1)
        records.append(row)
    path.write_text(json.dumps({'run_id': run_id, 'version': 'test-version', 'go_version': 'go1.27.0', 'records': records}))
    return path


def change_record(path, change):
    report = json.loads(path.read_text())
    row = next(row for row in report['records'] if row['status'] == 'pass')
    change(row)
    path.write_text(json.dumps(report))


class EvidenceSummaryTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)

    def report(self, name='one', passes=(('pulse', 'http'),), **kw):
        return fixture_report(self.directory.name, name, passes, **kw)

    def test_inventory_matches_production_runner_and_documented_counts(self):
        source = pathlib.Path(__file__).resolve().parents[2] / 'internal/verification/runner.go'
        inventory = source.read_text().split('func Inventory()', 1)[1].split('type Case', 1)[0]
        rows = re.findall(r'\{"(pulse|intervention|code)", "([^"]+)"\}', inventory)
        expected = [(kind, driver) for kind, names in rows for driver in names.split()]
        self.assertEqual(tuple(expected), summarize.INVENTORY)
        self.assertEqual(len(expected), 33)
        self.assertEqual([sum(kind == group for kind, _ in expected) for group in ('pulse', 'intervention', 'code')], [14, 5, 14])

    def test_partial_coverage_retains_all_unknown_drivers(self):
        path = self.report()
        summary = summarize.summarize([path])
        self.assertEqual(summary['covered_driver_count'], 1)
        self.assertEqual(len(summary['records']), 33)
        self.assertFalse(summary['all_drivers_covered'])
        self.assertFalse(summary['all_providers_verified'])
        self.assertEqual(summary['reports_without_binary_fingerprint'], [str(path)])

    def test_full_mock_coverage_does_not_establish_live_verification(self):
        path = self.report(passes=summarize.INVENTORY, category='mock_contract')
        summary = summarize.summarize([path])
        self.assertTrue(summary['all_drivers_covered'])
        self.assertFalse(summary['all_providers_verified'])
        self.assertFalse(summary['formal_vendor_certification'])
        self.assertEqual(summary['strongest_evidence_counts']['mock_contract'], 33)

    def test_only_full_live_effect_coverage_satisfies_live_account_gate(self):
        path = self.report(passes=summarize.INVENTORY, category='live_account')
        summary = summarize.summarize([path])
        self.assertTrue(summary['all_providers_verified'])
        self.assertFalse(summary['formal_vendor_certification'])

    def test_retains_both_mock_and_local_evidence_selects_local(self):
        mock = self.report(name='mock', category='mock_contract')
        local = self.report(name='local')
        summary = summarize.summarize([mock, local])
        first = summary['records'][0]
        self.assertEqual(first['strongest_evidence_type'], 'local_integration')
        self.assertEqual(len(first['evidence']), 2)
        self.assertEqual(summary['covered_driver_count'], 1)

    def test_tampered_missing_or_relocated_evidence_cannot_pass(self):
        for mutation in ('tamper', 'remove', 'traversal'):
            path = self.report(name=mutation)
            evidence = pathlib.Path(str(path) + '.evidence') / 'pulse-http.jsonl'
            if mutation == 'tamper':
                evidence.write_text('{}')
            elif mutation == 'remove':
                evidence.unlink()
            else:
                change_record(path, lambda row: row.update(evidence_ref='../other.json'))
            with self.subTest(mutation=mutation), self.assertRaises((ValueError, OSError)):
                summarize.summarize([path])

    def test_matching_hash_does_not_hide_wrong_run_identity_or_missing_observation(self):
        for mutation in ({'run_id': 'stale-run'}, {'driver': 'tcp'}, {'after': {'observed': False}}, {'before': None}, {'after': []}):
            path = self.report(name='mutation-' + str(len(list(pathlib.Path(self.directory.name).glob('*.json')))))
            evidence = pathlib.Path(str(path) + '.evidence') / 'pulse-http.jsonl'
            data = json.loads(evidence.read_text())
            data.update(mutation)
            evidence.write_text(json.dumps(data))
            change_record(path, lambda row: row.update(evidence_sha256=summarize.digest(evidence)))
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                summarize.summarize([path])

    def test_passing_label_cannot_hide_noninvocation_or_unobserved_effect(self):
        for patch in ({'operation_invoked': False}, {'accepted': False}, {'observed': False}, {'configured': False}):
            path = self.report(name=next(iter(patch)))
            change_record(path, lambda row: row.update(patch))
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                summarize.summarize([path])

    def test_explicit_twilio_api_acceptance_keeps_observed_false(self):
        path = self.report(passes=(('code', 'twilio'),), category='provider_sandbox', boundary='api_acceptance')
        summary = summarize.summarize([path])
        row = next(row for row in summary['records'] if row['status'] == 'pass')
        self.assertEqual(row['observation_boundary'], 'api_acceptance')
        self.assertFalse(summary['all_providers_verified'])
        other = self.report(name='invalid-boundary', category='live_account', boundary='api_acceptance')
        with self.assertRaises(ValueError):
            summarize.summarize([other])

    def test_wrong_inventory_duplicate_report_or_failed_case_rejected(self):
        path = self.report()
        with self.assertRaises(ValueError):
            summarize.summarize([path, path])
        report = json.loads(path.read_text())
        report['records'].pop()
        path.write_text(json.dumps(report))
        with self.assertRaises(ValueError):
            summarize.summarize([path])
        other = self.report(name='failed')
        change_record(other, lambda row: row.update(status='fail'))
        with self.assertRaises(ValueError):
            summarize.summarize([other])

    def test_suite_failure_and_different_binaries_or_versions_rejected(self):
        one, two = self.report(), self.report(name='two')
        one.with_suffix('.suite.json').write_text(json.dumps({'passed': False}))
        with self.assertRaises(ValueError):
            summarize.summarize([one])
        for path, fingerprint in ((one, 'a' * 64), (two, 'b' * 64)):
            path.with_suffix('.suite.json').write_text(json.dumps({'passed': True, 'binary_sha256': fingerprint}))
        with self.assertRaises(ValueError):
            summarize.summarize([one, two])
        two.with_suffix('.suite.json').unlink()
        report = json.loads(two.read_text())
        report['version'] = 'different-source-version'
        two.write_text(json.dumps(report))
        with self.assertRaises(ValueError):
            summarize.summarize([one, two])

    def test_successful_suite_with_failed_inner_scenario_is_rejected(self):
        path = self.report()
        path.with_suffix('.suite.json').write_text(json.dumps({'passed': True, 'scenarios': [{'passed': False}]}))
        with self.assertRaises(ValueError):
            summarize.summarize([path])

    def test_fixture_fingerprint_is_retained_and_cannot_conflict_with_suite(self):
        path = self.report()
        pathlib.Path(str(path) + '.fixture.json').write_text(json.dumps({'status': 'pass', 'cleanup_failures': [], 'binary_sha256': 'a' * 64}))
        summary = summarize.summarize([path])
        self.assertEqual(summary['binary_sha256'], 'a' * 64)
        self.assertEqual(summary['reports_without_binary_fingerprint'], [])
        path.with_suffix('.suite.json').write_text(json.dumps({'passed': True, 'binary_sha256': 'b' * 64}))
        with self.assertRaises(ValueError):
            summarize.summarize([path])

    def test_fixture_cannot_attach_a_binary_hash_to_a_different_report(self):
        path = self.report()
        fixture = pathlib.Path(str(path) + '.fixture.json')
        fixture.write_text(json.dumps({'status': 'pass', 'binary_sha256': 'a' * 64, 'report_sha256': 'b' * 64}))
        with self.assertRaises(ValueError):
            summarize.summarize([path])
        fixture.write_text(json.dumps({'status': 'pass', 'binary_sha256': 'a' * 64, 'report_sha256': summarize.digest(path)}))
        self.assertEqual(summarize.summarize([path])['binary_sha256'], 'a' * 64)
        path.with_suffix('.suite.json').write_text(json.dumps({'passed': False, 'status': 'pass'}))
        with self.assertRaises(ValueError):
            summarize.summarize([path])


if __name__ == '__main__':
    unittest.main()
