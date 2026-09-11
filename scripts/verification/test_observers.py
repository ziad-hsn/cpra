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

if __name__=='__main__':unittest.main()
