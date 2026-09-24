#!/usr/bin/env python3
import json
import os
import sys
import urllib.request
with urllib.request.urlopen(sys.argv[1],timeout=5) as response:counts=json.load(response)
run_id=os.environ['CPRA_VERIFY_RUN_ID']
if os.environ['CPRA_VERIFY_PHASE']=='before':
    print(json.dumps(dict(counts,run_id=run_id)))
else:
    before=json.load(sys.stdin)
    observed=before['run_id']==run_id and all(counts[k]>before[k] for k in ('connections','request_bytes','response_bytes'))
    print(json.dumps({'observed':observed,'count':counts['connections']-before['connections'],'before':before['response_bytes'],'after':counts['response_bytes'],'run_id':run_id}))
