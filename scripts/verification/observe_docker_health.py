#!/usr/bin/env python3
import hashlib
import json
import os
import subprocess
import sys
row = json.loads(subprocess.check_output(['docker', 'inspect', sys.argv[1]], text=True))[0]
evidence = {'run_id': os.environ['CPRA_VERIFY_RUN_ID'], 'resource_digest': hashlib.sha256(row['Id'].encode()).hexdigest(), 'running': row['State']['Running']}
if os.environ['CPRA_VERIFY_PHASE'] == 'after':
    before = json.load(sys.stdin)
    evidence['observed'] = evidence['running'] and before['resource_digest'] == evidence['resource_digest'] and before['run_id'] == evidence['run_id']
print(json.dumps(evidence))
