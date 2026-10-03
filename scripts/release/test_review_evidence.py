"""Real offline signature regressions over synthetic release assessments.

Disposable identities below are test inputs, never independent review evidence.
"""
import copy
from datetime import datetime, timedelta, timezone
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import qualification
import release
import review_evidence
from test_evidence import manifest_fixture


class ReviewEvidenceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.ssh = shutil.which('ssh-keygen')
        if not self.ssh:
            self.fail('OpenSSH is required for the real offline signature regression')
        self.keys = {}
        for name in ('author', 'reviewer', 'second'):
            key = self.root / name
            subprocess.run([self.ssh, '-q', '-t', 'ed25519', '-N', '', '-C', 'disposable-test', '-f', str(key)],
                           check=True, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            self.keys[name] = {'principal': name + '@example.invalid',
                               'public_key': ' '.join(key.with_suffix('.pub').read_text().split()[:2])}
        self.manifest = manifest_fixture()
        self.identity = release.evidence_identity(self.manifest)
        self.proofs = self.root / 'reviews'
        self.proofs.mkdir(mode=0o700)
        self.original = self.proofs / 'execution.log'
        self.original.write_bytes(b'synthetic original execution record; no application ran\n')
        self.components = {name: {'status': 'consistent_pending_review'} for name in qualification.COMPONENTS
                           if name != 'independent_review'}
        self.assessment_path = self.proofs / 'assessment.json'
        self.assessment = {'schema_version': 1, 'status': 'incomplete', 'release_identity': self.identity,
                           'scope': 'private frozen-release evidence assessment', 'release_gate_passed': False,
                           'publication_locked': True, 'components': {**self.components, 'independent_review': {'status': 'missing'}},
                           'input_sha256': {str(self.original): release.sha(self.original)}}
        self.trust_path = self.root / 'operator-trust.json'
        self.trust = {'schema_version': 1, 'repository': 'ziad-hsn/cpra', 'authors': [self.keys['author']],
                      'reviewers': [self.keys['reviewer'], self.keys['second']], 'minimum_reviewers': 1}
        self.paths = [self.proofs / 'review.json']
        self.records = [{'schema_version': 1, 'release_identity': self.identity,
                         'assessment_sha256': '', 'principal': self.keys['reviewer']['principal'],
                         'reviewed_at': datetime.now(timezone.utc).isoformat(),
                         'components': sorted(self.components), 'conclusion': 'approve',
                         'notes': 'Disposable test statement. No genuine candidate or review is approved.', 'findings': []}]
        self.save()

    def save(self, *, signer='reviewer', namespace=review_evidence.NAMESPACE):
        self.assessment_path.write_bytes(release.encoded(self.assessment))
        self.trust_path.write_bytes(release.encoded(self.trust))
        self.trust_path.chmod(0o600)
        for path, record in zip(self.paths, self.records):
            record['assessment_sha256'] = release.sha(self.assessment_path)
            path.write_bytes(release.encoded(record))
            path.with_name(path.name + '.sig').unlink(missing_ok=True)
            subprocess.run([self.ssh, '-Y', 'sign', '-f', str(self.root / signer), '-n', namespace, str(path)],
                           check=True, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def verify(self):
        return review_evidence.verify_reviews(self.manifest, self.assessment_path, self.trust_path,
                                              self.paths, self.components, qualification.Inputs(), ssh_keygen=self.ssh)

    def test_real_trusted_signature_binds_every_original_input_without_authorizing_publication(self):
        result = self.verify()
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertEqual(result['approved_reviewers'], ['reviewer@example.invalid'])
        self.assertIn(str(self.original), result['input_sha256'])
        self.assertIn(str(self.trust_path), result['input_sha256'])
        self.assertIn(str(self.paths[0]) + '.sig', result['input_sha256'])
        self.assertFalse(result['release_gate_passed'])
        self.assertTrue(result['publication_locked'])

    def test_signature_rejects_another_key_and_another_namespace(self):
        for signer, namespace in [('author', review_evidence.NAMESPACE), ('reviewer', 'file')]:
            with self.subTest(signer=signer, namespace=namespace):
                self.save(signer=signer, namespace=namespace)
                with self.assertRaisesRegex(ValueError, 'signature'):
                    self.verify()

    def test_rewriting_signed_notes_or_signature_is_rejected(self):
        for signature in (False, True):
            with self.subTest(signature=signature):
                self.save()
                path = self.paths[0].with_name(self.paths[0].name + '.sig') if signature else self.paths[0]
                raw = path.read_bytes()
                path.write_bytes(raw.replace(b'Disposable', b'Unreviewed') if not signature else b'invalid SSH signature\n')
                with self.assertRaisesRegex(ValueError, 'signature'):
                    self.verify()

    def test_changed_original_execution_bytes_cannot_reuse_approval(self):
        self.original.write_bytes(b'changed original execution')
        with self.assertRaisesRegex(ValueError, 'retained input changed'):
            self.verify()

    def test_same_source_but_different_assessment_digest_is_rejected(self):
        self.assessment['status'] = 'another assessment'
        self.assessment_path.write_bytes(release.encoded(self.assessment))
        with self.assertRaisesRegex(ValueError, 'another source or assessment'):
            self.verify()

    def test_current_results_must_equal_reviewed_results_in_full(self):
        self.components['security'] = {'status': 'consistent_pending_review', 'different_run': True}
        with self.assertRaisesRegex(ValueError, 'outcomes differ'):
            self.verify()

    def test_incomplete_component_is_not_qualified_by_a_real_approval_signature(self):
        self.components['providers'] = {'status': 'missing'}
        self.assessment['components']['providers'] = self.components['providers']
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertFalse(result['release_gate_passed'])

    def test_author_cannot_approve_under_own_identity_or_another_identity_with_same_key(self):
        for principal in ('author@example.invalid', 'someone@example.invalid'):
            with self.subTest(principal=principal):
                self.trust['reviewers'][0] = {**self.keys['author'], 'principal': principal}
                self.save()
                with self.assertRaisesRegex(ValueError, 'authors cannot review'):
                    self.verify()

    def test_duplicate_key_cannot_satisfy_two_reviewers(self):
        self.trust['minimum_reviewers'] = 2
        self.trust['reviewers'][1]['public_key'] = self.trust['reviewers'][0]['public_key']
        self.save()
        with self.assertRaisesRegex(ValueError, 'duplicate trusted identity or key'):
            self.verify()

    def test_revoked_identity_and_insufficient_independent_reviewers_remain_unavailable(self):
        self.trust['minimum_reviewers'] = 2
        self.save()
        self.assertEqual(self.verify()['status'], 'incomplete')
        self.trust['reviewers'] = [self.keys['second']]
        self.trust['minimum_reviewers'] = 1
        self.save()
        with self.assertRaisesRegex(ValueError, 'untrusted'):
            self.verify()

    def test_reviewer_policy_is_supplied_outside_artifacts_and_not_by_an_assessment(self):
        self.trust_path = self.proofs / 'invented-trust.json'
        self.save()
        with self.assertRaisesRegex(ValueError, 'artifact directory'):
            self.verify()
        self.trust_path = self.root / 'operator-trust.json'
        self.assessment['input_sha256'][str(self.trust_path)] = release.sha(self.trust_path)
        self.save()
        with self.assertRaisesRegex(ValueError, 'own reviewer trust'):
            self.verify()

    def test_policy_permissions_and_linked_inputs_fail_explicitly(self):
        self.trust_path.chmod(0o666)
        with self.assertRaisesRegex(ValueError, 'writable by group'):
            self.verify()
        self.trust_path.chmod(0o600)
        original = self.paths[0]
        link = self.proofs / 'linked-review.json'
        link.symlink_to(original)
        self.paths = [link]
        with self.assertRaisesRegex(ValueError, 'link or alias'):
            self.verify()

    def test_signed_review_cannot_omit_scopes_or_claim_published_authority(self):
        for mutate in (lambda: self.records[0]['components'].pop(),
                       lambda: self.assessment.update(publication_locked=False),
                       lambda: self.assessment.update(release_gate_passed=True),
                       lambda: self.assessment['components'].pop('benchmark')):
            original_record, original_assessment = copy.deepcopy(self.records), copy.deepcopy(self.assessment)
            with self.subTest(mutate=mutate):
                mutate(); self.save()
                with self.assertRaises(ValueError):
                    self.verify()
            self.records, self.assessment = original_record, original_assessment

    def test_open_findings_require_changes_and_cannot_be_hidden_by_other_approvals(self):
        self.records[0]['findings'] = [{'id': 'R1', 'component': 'security', 'status': 'open',
                                      'notes': 'Synthetic unresolved finding',
                                      'evidence': {str(self.original): release.sha(self.original)}}]
        self.save()
        with self.assertRaisesRegex(ValueError, 'unresolved findings'):
            self.verify()
        self.records[0]['conclusion'] = 'changes_requested'
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'incomplete')
        self.assertEqual(result['changes_requested_by'], ['reviewer@example.invalid'])

    def test_resolved_finding_requires_original_evidence_bytes(self):
        self.records[0]['findings'] = [{'id': 'R1', 'component': 'management', 'status': 'resolved',
                                      'notes': 'Synthetic resolution', 'evidence': {str(self.original): release.sha(self.original)}}]
        self.save()
        self.assertEqual(self.verify()['status'], 'consistent_pending_review')
        self.records[0]['findings'][0]['evidence'][str(self.original)] = 'a' * 64
        self.save()
        with self.assertRaisesRegex(ValueError, 'original reviewed evidence'):
            self.verify()

    def test_future_and_pre_source_timestamps_are_rejected(self):
        for at in ('1970-01-01T00:00:00Z', '2026-10-01T00:00:00',
                   (datetime.now(timezone.utc) + timedelta(days=1)).isoformat()):
            with self.subTest(timestamp=at):
                self.records[0]['reviewed_at'] = at
                self.save()
                with self.assertRaisesRegex(ValueError, 'timestamp'):
                    self.verify()

    def test_missing_excessive_duplicate_and_unsigned_records_fail(self):
        for records in ([], [self.paths[0]] * 2, [self.paths[0]] * 17):
            with self.subTest(records=len(records)), self.assertRaises(ValueError):
                review_evidence.verify_reviews(self.manifest, self.assessment_path, self.trust_path,
                                              records, self.components, qualification.Inputs(), ssh_keygen=self.ssh)
        self.paths[0].with_name(self.paths[0].name + '.sig').unlink()
        with self.assertRaises(FileNotFoundError):
            self.verify()

    def test_duplicate_json_and_bounded_record_are_rejected(self):
        self.paths[0].write_bytes(b'{"schema_version":1,"schema_version":1}')
        with self.assertRaisesRegex(ValueError, 'duplicate JSON'):
            self.verify()
        self.paths[0].write_bytes(b'x' * (review_evidence.MAX_RECORD_BYTES + 1))
        with self.assertRaisesRegex(ValueError, 'bounded regular'):
            self.verify()

    def test_verifier_timeout_and_missing_executable_cannot_pass(self):
        with patch.object(review_evidence.subprocess, 'run', side_effect=subprocess.TimeoutExpired('ssh-keygen', 10)):
            with self.assertRaisesRegex(ValueError, 'timed out'):
                self.verify()
        with self.assertRaisesRegex(ValueError, 'installed OpenSSH verifier'):
            review_evidence.verify_reviews(self.manifest, self.assessment_path, self.trust_path,
                                          self.paths, self.components, qualification.Inputs(), ssh_keygen='/missing/ssh-keygen')
