#!/usr/bin/env python3
"""Observe bounded counters from an isolated local socket fixture.

The receiver records operations independently of cpra-verify. It never receives
the runner's result. A baseline without a subsequent operation cannot pass.
"""
import json
import os
import sys
import urllib.request


def observed(before, after, run_id, exact=True):
    delta = after['count'] - before['count']
    return (before['run_id'] == run_id and
            (delta == 1 if exact else delta >= 1) and
            after['invalid'] == before['invalid'] and
            (not after.get('run_id') or after['run_id'] == run_id))


def main():
    url = sys.argv[1]
    run_id = os.environ['CPRA_VERIFY_RUN_ID']
    with urllib.request.urlopen(url, timeout=2) as response:
        state = json.load(response)
    if os.environ['CPRA_VERIFY_PHASE'] == 'before':
        state['run_id'] = run_id
        print(json.dumps(state))
    else:
        before = json.load(sys.stdin)
        print(json.dumps({'observed': observed(before, state, run_id, state.get('exact', True)),
                          'before': before['count'], 'after': state['count'],
                          'invalid': state['invalid'], 'run_id': run_id,
                          'digest': state.get('digest', '')}))


if __name__ == '__main__':
    main()
