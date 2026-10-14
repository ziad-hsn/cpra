"""Synthetic scan/review records test boundaries, never WCAG qualification."""
import base64
import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import tarfile
import unittest
from unittest.mock import patch

import accessibility_evidence as accessibility
import browser_evidence as browser
import release
import test_browser_evidence as browser_fixtures


def contrast_finding():
    location = 'html:nth-of-type(1) > body:nth-of-type(1)'
    return {'rule': 'color-contrast', 'impact': 'serious', 'nodeCount': 1, 'locations': [location],
            'contrast': [{'location': location, 'checks': [{'group': 'any', 'reason': 'bgOverlap',
                'foreground': None, 'background': None, 'shadow': None, 'ratio': None, 'expectedRatio': 4.5,
                'fontSizePt': 9.8, 'fontSizePx': 13, 'fontWeight': 'normal'}]}]}


def scanner_fixture():
    package = release.encoded({'version': accessibility.SCANNER_VERSION})
    script = b'synthetic scanner fixture; this is not executable axe-core'
    archive = io.BytesIO()
    with tarfile.open(fileobj=archive, mode='w:gz') as target:
        for name, data in (('package/package.json', package), ('package/axe.min.js', script)):
            member = tarfile.TarInfo(name)
            member.size = len(data)
            target.addfile(member, io.BytesIO(data))
    archive = archive.getvalue()
    lock = release.encoded({'packages': {'node_modules/axe-core': {
        'version': accessibility.SCANNER_VERSION,
        'resolved': 'https://registry.npmjs.org/axe-core/-/axe-core-' + accessibility.SCANNER_VERSION + '.tgz',
        'integrity': 'sha512-' + base64.b64encode(hashlib.sha512(archive).digest()).decode(),
    }}})
    return {'lock': lock, 'archive': archive, 'package': package, 'script': script}


class AccessibilityEvidenceTests(unittest.TestCase):
    def setUp(self):
        fixture = browser_fixtures.BrowserEvidenceTests('test_both_compilers_and_tags_match_retained_inputs_but_do_not_authorize_release')
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        self.fixture, self.root, self.manifest = fixture, fixture.root, fixture.manifest
        self.paths, self.reports = fixture.paths, fixture.reports
        scanner = scanner_fixture()
        self.manifest['source_files']['scripts/dashboard/browser/package-lock.json'] = {
            'mode': '100644', 'sha256': hashlib.sha256(scanner['lock']).hexdigest()}
        self.scans = {}
        for path, report in zip(self.paths, self.reports):
            directory = path.with_name(path.stem + '.artifacts')
            source = self.root / report['source_inventory']
            source.write_bytes(release.encoded(browser.source_inputs(self.manifest)))
            report['source_inventory_sha256'] = release.sha(source)
            report['release_identity'] = release.evidence_identity(self.manifest)
            tools = directory / 'tools'; tools.mkdir()
            originals = {}
            for key, name in accessibility.TOOL_FILES.items():
                target = tools / name
                target.write_bytes(scanner[key])
                originals[key] = dict(file=target.relative_to(self.root).as_posix(), **fixture.record(target))
            scans = {}
            for script, labels in accessibility.CHECKPOINTS.items():
                for ordinal, label in enumerate(sorted(labels), 1):
                    target = directory / 'screenshots' / f'accessibility-{script}-{ordinal:02d}-{label}.json'
                    value = {'checkpoint': label, 'scanner': accessibility.SCANNER_VERSION, 'violations': [],
                             'needsReview': [], 'contrastPasses': [], 'passedRules': ['aria-allowed-attr', 'label'], 'automatedOnly': True}
                    if label == 'guarded-recovery':
                        value['needsReview'] = [contrast_finding()]
                    target.write_bytes(release.encoded(value))
                    scans[target.relative_to(self.root).as_posix()] = fixture.record(target)
                    self.scans[target] = value
            report['accessibility'] = {'scanner': originals, 'scans': scans}
        fixture.save()
        self.manual_path = self.root / 'manual-review.json'
        note = self.root / 'manual-notes.txt'
        note.write_bytes(b'Synthetic notes only: no human accessibility review was performed.')
        self.note = dict(file=note.name, **fixture.record(note))
        self.manual = {
            'schema_version': 1, 'release_identity': release.evidence_identity(self.manifest),
            'recorded_at': '2026-10-01T10:00:00+00:00', 'reviewer': 'synthetic reviewer fixture',
            'full_wcag_conformance_claimed': False, 'campaign_sha256': sorted(release.sha(path) for path in self.paths),
            'cases': [{'id': name, 'outcome': 'pass', 'tools': ['synthetic screen reader and keyboard fixture'],
                       'notes': self.note} for name in sorted(accessibility.MANUAL_CASES)],
            'scan_reviews': [],
        }
        for path, report in zip(self.paths, self.reports):
            for name, record in report['accessibility']['scans'].items():
                if self.scans[self.root / name]['needsReview']:
                    self.manual['scan_reviews'].append({'campaign_sha256': release.sha(path),
                        'scan_sha256': record['sha256'], 'rule': 'color-contrast', 'outcome': 'pass',
                        'tools': ['synthetic contrast review'], 'notes': self.note})
        self.save_review()

    def save_review(self):
        self.manual_path.write_bytes(release.encoded(self.manual))

    def verify(self, manual=True):
        return accessibility.verify_accessibility(self.manifest, self.paths, self.manual_path if manual else None)

    def replace_scan(self, change):
        name, record = next(iter(self.reports[0]['accessibility']['scans'].items()))
        path = self.root / name
        value = copy.deepcopy(self.scans[path])
        change(value)
        path.write_bytes(release.encoded(value))
        record.update(self.fixture.record(path))
        self.fixture.save()

    def test_complete_record_consistency_never_authorizes_publication_or_wcag_claim(self):
        result = self.verify()
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertEqual(result['unverified'], [])
        self.assertFalse(result['release_gate_passed'])
        self.assertFalse(result['full_wcag_conformance_claimed'])
        self.assertEqual(len(result['campaigns']), 4)
        self.assertIn(str(self.root / 'manual-notes.txt'), result['input_sha256'])

    def test_automatic_scans_cannot_replace_manual_review(self):
        result = self.verify(manual=False)
        self.assertEqual(result['status'], 'incomplete')
        self.assertEqual(len(result['unverified']), len(accessibility.MANUAL_CASES) + 4)
        self.assertFalse(result['release_gate_passed'])

    def test_missing_or_unreviewed_finding_remains_incomplete(self):
        self.manual['scan_reviews'].pop()
        self.save_review()
        self.assertEqual(self.verify()['status'], 'incomplete')
        self.manual['cases'][0]['outcome'] = 'not_tested'
        self.save_review()
        self.assertIn('not_tested', [item.get('outcome') for item in self.verify()['unverified']])

    def test_failed_manual_cases_and_findings_cannot_be_labeled_pass(self):
        self.manual['cases'][0]['outcome'] = 'fail'
        self.manual['scan_reviews'][0]['outcome'] = 'fail'
        self.save_review()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertEqual(sum(item.get('outcome') == 'fail' for item in result['unverified']), 2)

    def test_confirmed_violations_remain_incomplete_even_with_passing_manual_cases(self):
        self.replace_scan(lambda scan: scan['violations'].append({
            'rule': 'label', 'impact': 'serious', 'nodeCount': 1, 'locations': ['input:nth-of-type(1)'], 'contrast': []}))
        result = self.verify(manual=False)
        self.assertIn('confirmed accessibility violation', [item['requirement'] for item in result['unverified']])

    def test_archive_authenticity_cannot_be_substituted_with_matching_version(self):
        for key in ('archive', 'package', 'script'):
            with self.subTest(key=key):
                original = scanner_fixture()
                original[key] += b' changed'
                with self.assertRaises(ValueError):
                    accessibility.verify_scanner(**original)
        for value in ('http://localhost/private', 'https://example.com/axe-core.tgz'):
            original = scanner_fixture()
            lock = json.loads(original['lock'])
            lock['packages']['node_modules/axe-core']['resolved'] = value
            original['lock'] = release.encoded(lock)
            with self.subTest(origin=value), self.assertRaisesRegex(ValueError, 'origin'):
                accessibility.verify_scanner(**original)

    def test_scan_html_selectors_and_private_field_values_are_rejected(self):
        for change in (lambda s: s.update(html='<input value="private">'),
                       lambda s: s['needsReview'].append({'rule': 'label', 'impact': 'serious', 'nodeCount': 1,
                                                        'locations': ['input[value="private"]'], 'contrast': []})):
            scan = {'checkpoint': 'secret-editor', 'scanner': accessibility.SCANNER_VERSION, 'violations': [],
                    'needsReview': [], 'contrastPasses': [], 'passedRules': ['label'], 'automatedOnly': True}
            change(scan)
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, 'unredacted|private'):
                accessibility.scan_report(scan)

    def test_contrast_measurements_are_bounded_allowlisted_and_not_manual_proof(self):
        scan = {'checkpoint': 'guarded-recovery', 'scanner': accessibility.SCANNER_VERSION, 'violations': [],
                'needsReview': [contrast_finding()], 'contrastPasses': [contrast_finding()],
                'passedRules': ['color-contrast'], 'automatedOnly': True}
        accessibility.scan_report(scan)
        mutations = [lambda c: c.update(html='private-value'), lambda c: c.update(reason='private-value'),
                     lambda c: c.update(foreground='private-value'), lambda c: c.update(fontWeight='private-value'),
                     lambda c: c.update(group='private-value'), lambda c: c.update(ratio=5),
                     lambda c: c.update(expectedRatio=0), lambda c: c.update(expectedRatio=True),
                     lambda c: c.update(expectedRatio=float('inf')), lambda c: c.update(fontSizePx=4097),
                     lambda c: c.update(ratio=22, foreground='#ffffff', background='#000000')]
        for name in ('needsReview', 'contrastPasses'):
            for change in mutations:
                altered = copy.deepcopy(scan)
                change(altered[name][0]['contrast'][0]['checks'][0])
                with self.subTest(section=name, change=change), self.assertRaises(ValueError):
                    accessibility.scan_report(altered)
        for change in (lambda f: f['contrast'].clear(),
                       lambda f: f['contrast'][0].update(location='input[value="private"]'),
                       lambda f: f['contrast'][0]['checks'].append(f['contrast'][0]['checks'][0])):
            altered = copy.deepcopy(scan)
            change(altered['needsReview'][0])
            with self.subTest(change=change), self.assertRaises(ValueError):
                accessibility.scan_report(altered)

    def test_catalog_cannot_omit_relabel_or_repeat_required_checkpoints(self):
        name, record = next(iter(self.reports[0]['accessibility']['scans'].items()))
        del self.reports[0]['accessibility']['scans'][name]
        self.fixture.save()
        with self.assertRaisesRegex(ValueError, 'coverage|ordering'):
            self.verify(manual=False)
        self.reports[0]['accessibility']['scans'][name] = record
        self.fixture.save()
        self.replace_scan(lambda scan: scan.update(checkpoint='not-its-original-label'))
        with self.assertRaisesRegex(ValueError, 'label'):
            self.verify(manual=False)

    def test_manual_review_cannot_be_reused_after_source_or_scan_changes(self):
        self.manual['release_identity']['commit'] = 'b' * 40
        self.save_review()
        with self.assertRaisesRegex(ValueError, 'identity'):
            self.verify()
        self.manual['release_identity'] = release.evidence_identity(self.manifest)
        self.manual['scan_reviews'][0]['scan_sha256'] = 'a' * 64
        self.save_review()
        with self.assertRaisesRegex(ValueError, 'another scan'):
            self.verify()

    def test_unknown_duplicate_and_summary_only_manual_cases_are_rejected(self):
        original = copy.deepcopy(self.manual)
        changes = [lambda r: r['cases'].append(r['cases'][0]),
                   lambda r: r['cases'][0].update(id='invented-case'),
                   lambda r: r['cases'][0].pop('notes'),
                   lambda r: r['cases'][0].update(outcome='success'),
                   lambda r: r['cases'][0].update(tools=[]),
                   lambda r: r.update(full_wcag_conformance_claimed=True),
                   lambda r: r.update(campaign_sha256=['a' * 64]),
                   lambda r: r.update(recorded_at='2026-10-01')]
        for change in changes:
            self.manual = copy.deepcopy(original); change(self.manual); self.save_review()
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.verify()

    def test_raw_payload_fingerprints_and_original_manual_notes_are_required(self):
        original = (self.root / 'manual-notes.txt').read_bytes()
        (self.root / 'manual-notes.txt').write_bytes(original + b' changed')
        with self.assertRaisesRegex(ValueError, 'bytes changed'):
            self.verify()
        (self.root / 'manual-notes.txt').write_bytes(original)
        path = self.paths[0].with_name(self.paths[0].stem + '.artifacts') / 'tools/axe.min.js'
        path.write_bytes(path.read_bytes() + b' changed')
        with self.assertRaisesRegex(ValueError, 'bytes changed'):
            self.verify()

    def test_actual_browser_failures_and_development_workspaces_still_defeat_the_reader(self):
        self.reports[0]['workspace']['path'] = '/developer/go.work'
        self.fixture.save()
        with self.assertRaisesRegex(ValueError, 'development workspace'):
            self.verify()

    def test_cli_preserves_incomplete_and_invalid_results_without_overwrite(self):
        manifest = self.root / 'RELEASE.json'; manifest.write_bytes(release.encoded(self.manifest))
        output = self.root / 'assessment.json'
        args = ['accessibility_evidence.py', '--release', str(manifest), '--out', str(output)]
        for path in self.paths: args.extend(['--campaign', str(path)])
        with patch.object(sys, 'argv', args): self.assertEqual(accessibility.main(), 2)
        self.assertEqual(json.loads(output.read_bytes())['status'], 'incomplete')
        self.assertEqual(output.stat().st_mode & 0o777, 0o600)
        original = output.read_bytes()
        with patch.object(sys, 'argv', args), self.assertRaises(FileExistsError): accessibility.main()
        self.assertEqual(output.read_bytes(), original)


if __name__ == '__main__':
    unittest.main()
