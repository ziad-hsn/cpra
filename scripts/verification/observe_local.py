#!/usr/bin/env python3
"""Independent observations for the isolated local verification target."""
import json
import os
import pathlib
import sys
import urllib.request

mode, target = sys.argv[1:3]
phase = os.environ['CPRA_VERIFY_PHASE']
run_id = os.environ['CPRA_VERIFY_RUN_ID']
if mode == 'log':
    path = pathlib.Path(target)
    count = sum(run_id in json.loads(line).get('monitor', '') for line in path.read_text().splitlines()) if path.exists() else 0
else:
    with urllib.request.urlopen(target + '/audit', timeout=5) as response:
        audit = json.load(response)
    count = audit.get(mode, 0)
if phase == 'before':
    print(json.dumps({'count': count, 'run_id': run_id}))
else:
    before = json.load(sys.stdin)
    print(json.dumps({'observed': before['run_id'] == run_id and count == before['count'] + 1,
                      'before': before['count'], 'after': count, 'run_id': run_id}))
