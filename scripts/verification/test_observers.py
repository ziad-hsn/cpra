import json
import os
import pathlib
import subprocess
import sys
import tempfile
import time
import unittest

class ReceiptContracts(unittest.TestCase):
    def test_correlated_destination_receipt(self):
        observer=pathlib.Path(__file__).with_name('observe_receipt.py')
        env=dict(os.environ,CPRA_VERIFY_RUN_ID='fixture-run',CPRA_VERIFY_PHASE='before')
        with tempfile.TemporaryDirectory() as directory:
            receipts=pathlib.Path(directory)/'receipts.jsonl'
            command=[sys.executable,str(observer),'slack',str(receipts)]
            before=subprocess.check_output(command,env=env,text=True)
            receipts.write_text(json.dumps({'run_id':'fixture-run','driver':'slack','provider_id':'message-123','received_at':time.time(),'delivered':True})+'\n')
            after=subprocess.check_output(command,env=dict(env,CPRA_VERIFY_PHASE='after'),input=before,text=True)
        self.assertTrue(json.loads(after)['observed'])
        self.assertNotIn('message-123',after)

class AgentEffects(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = pathlib.Path(self.directory.name) / 'agent.jsonl'
        self.command = [sys.executable, str(pathlib.Path(__file__).with_name('observe_agent.py')),
                        'intervention', str(self.path), '--timeout', '0']
        self.env = dict(os.environ, CPRA_VERIFY_RUN_ID='run-original', CPRA_VERIFY_MONITOR_ID='monitor-original',
                        CPRA_VERIFY_ACTION_ID='action-original', CPRA_VERIFY_PHASE='before')
        self.before = subprocess.check_output(self.command, env=self.env, text=True)
        self.env['CPRA_VERIFY_PHASE'] = 'after'
        self.receipt = dict(run_id='run-original', monitor_id='monitor-original', action_id='action-original',
                            role='intervention', effect='recovery_completed', observed=True,
                            observed_at=time.time(), provider_id='provider-private-reference')

    def after(self, before=None):
        return subprocess.run(self.command, env=self.env, input=before or self.before,
                              text=True, capture_output=True, check=False)

    def test_roles_require_their_independently_observed_effect(self):
        for role, effect in [('intervention', 'recovery_completed'), ('notification', 'incident_received')]:
            self.command[2] = role
            self.path.write_text(json.dumps(dict(self.receipt, role=role, effect=effect)) + '\n')
            result = self.after()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue(json.loads(result.stdout)['observed'])
            self.assertNotIn('provider-private-reference', result.stdout)

    def test_wrong_identity_stale_receipt_and_task_acceptance_do_not_pass(self):
        for change in [{'run_id': 'different'}, {'action_id': 'different'}, {'monitor_id': 'different'},
                       {'role': 'notification'}, {'effect': 'task_completed'}, {'observed': False},
                       {'observed_at': 0}, {'provider_id': ''}]:
            self.path.write_text(json.dumps(dict(self.receipt, **change)) + '\n')
            with self.subTest(change=change):
                result = self.after()
                self.assertEqual(result.returncode, 0)
                self.assertFalse(json.loads(result.stdout)['observed'])

    def test_multiple_external_effects_do_not_pass(self):
        self.path.write_text(json.dumps(self.receipt) + '\n' +
                             json.dumps(dict(self.receipt, provider_id='another-effect')) + '\n')
        result = self.after()
        self.assertEqual(result.returncode, 0)
        self.assertEqual(json.loads(result.stdout)['count'], 2)
        self.assertFalse(json.loads(result.stdout)['observed'])

    def test_replaced_baseline_ambiguous_and_oversized_receipts_fail(self):
        self.path.write_text(json.dumps(self.receipt) + '\n')
        replaced = json.loads(self.before)
        replaced['action_id'] = 'private-stale-action'
        self.assertEqual(self.after(json.dumps(replaced)).returncode, 1)
        for contents in ['{"observed":false,"observed":true}', 'x' * 65537]:
            self.path.write_text(contents)
            result = self.after()
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stdout, '')
            self.assertNotIn(contents, result.stderr)


if __name__=='__main__':unittest.main()
