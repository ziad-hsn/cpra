"""Execute the workflow's private-staging guard without contacting GitHub."""
import os
from pathlib import Path
import subprocess
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]


class PrivateReleasePolicyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load((ROOT / '.github/workflows/release.yml').read_text())

    def test_actual_guard_rejects_public_internal_unknown_and_publication(self):
        guard = self.workflow['jobs']['private-staging']['steps'][0]
        expected = {
            'CPRA_REPOSITORY_PRIVATE': '${{ github.event.repository.private }}',
            'CPRA_REPOSITORY_VISIBILITY': '${{ github.event.repository.visibility }}',
            'CPRA_PUBLISH_REQUESTED': '${{ inputs.publish }}',
        }
        self.assertEqual(guard['env'], expected)
        cases = [
            ('true', 'private', 'false', True),
            ('false', 'public', 'false', False),
            ('true', 'internal', 'false', False),
            ('true', 'public', 'false', False),
            ('false', 'private', 'false', False),
            ('true', 'private', 'true', False),
            ('true', 'private', '', False),
            ('', 'private', 'false', False),
            ('true', '', 'false', False),
            ('', '', '', False),
            ('True', 'private', 'false', False),
        ]
        for private, visibility, publish, allowed in cases:
            with self.subTest(private=private, visibility=visibility, publish=publish):
                env = dict(zip(expected, (private, visibility, publish)))
                env['PATH'] = os.defpath
                result = subprocess.run(['bash', '-euo', 'pipefail', '-c', guard['run']],
                                        env=env, capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)
                if not allowed:
                    self.assertTrue(result.stderr)

    def test_every_release_job_waits_for_the_policy(self):
        jobs = self.workflow['jobs']
        policy = jobs['private-staging']
        self.assertEqual(policy['permissions'], {})
        self.assertEqual(len(policy['steps']), 1)
        self.assertNotIn('uses', policy['steps'][0])
        self.assertNotIn('if', policy)

        def requires_policy(name, seen):
            if name == 'private-staging':
                return True
            self.assertNotIn(name, seen, 'cyclic workflow dependencies')
            job = jobs[name]
            self.assertNotIn('always()', job.get('if', ''))
            self.assertNotIn('continue-on-error', job)
            needs = job.get('needs', [])
            if isinstance(needs, str):
                needs = [needs]
            return any(requires_policy(parent, seen | {name}) for parent in needs)

        for name in jobs:
            with self.subTest(job=name):
                self.assertTrue(requires_policy(name, set()))
        # The only admitted dispatch is publish=false. This job requires true
        # and cannot bypass the failed policy through always() or dependencies.
        self.assertEqual(jobs['publish']['if'], "inputs.publish && github.ref == 'refs/heads/main'")

    def test_ci_reports_only_upload_to_private_storage(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_text())
        uploads = [step for job in workflow['jobs'].values() for step in job['steps']
                   if step.get('uses', '').startswith('actions/upload-artifact@')]
        self.assertTrue(uploads)
        for step in uploads:
            self.assertEqual(step['if'], "always() && github.event.repository.private == true && github.event.repository.visibility == 'private'")

    def test_live_campaign_requires_private_repository_before_runner_allocation(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/live-verification.yml').read_text())
        self.assertEqual(workflow['jobs']['live']['if'],
                         "github.event.repository.private == true && github.event.repository.visibility == 'private'")


if __name__ == '__main__':
    unittest.main()
