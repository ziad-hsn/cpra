#!/usr/bin/env python3
"""Validate run-correlated receipts exported by a configured independent reader.

A reader on the actual destination/account must append a JSON line after it
observes the message, with run_id, driver, delivered=true, received_at (Unix UTC)
and provider_id. CPRa's API response alone must never create such a receipt.
"""
import argparse
import hashlib
import json
import os
import pathlib
import sys
import time

p=argparse.ArgumentParser()
p.add_argument('driver')
p.add_argument('receipt_file')
a=p.parse_args()
run_id=os.environ['CPRA_VERIFY_RUN_ID']
if os.environ['CPRA_VERIFY_PHASE']=='before':
    print(json.dumps({'run_id':run_id,'started':time.time()}))
else:
    before=json.load(sys.stdin)
    path=pathlib.Path(a.receipt_file)
    deadline=time.monotonic()+100
    while True:
        matches=[]
        if path.exists():
            if path.stat().st_size>16*1024**2: raise SystemExit('receipt file exceeds bounded verification size')
            for line in path.read_text().splitlines():
                row=json.loads(line)
                if row.get('run_id')==run_id and row.get('driver')==a.driver and row.get('delivered') is True and row.get('received_at',0)>=before['started'] and row.get('provider_id'):
                    matches.append(row)
        if matches or time.monotonic()>=deadline:
            provider_ids=sorted({str(r['provider_id']) for r in matches})
            print(json.dumps({'observed':before['run_id']==run_id and len(provider_ids)==1,'count':len(provider_ids),'resource_digest':hashlib.sha256(json.dumps(provider_ids).encode()).hexdigest(),'run_id':run_id}))
            break
        time.sleep(1)
