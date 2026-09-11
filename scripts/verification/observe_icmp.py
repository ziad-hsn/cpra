#!/usr/bin/env python3
"""Observe kernel ICMP loopback exchanges inside the designated test namespace."""
import json
import os
import pathlib
import sys


def read_counts(text):
    lines = [line.split()[1:] for line in text.splitlines() if line.startswith('Icmp:')]
    if len(lines) != 2 or len(lines[0]) != len(lines[1]):
        raise ValueError('ICMP counters unavailable')
    values = dict(zip(lines[0], map(int, lines[1])))
    return {key: values[key] for key in ('InEchos', 'InEchoReps')}


def result(before, after, run_id):
    matched = before.get('run_id') == run_id
    observed = matched and all(after[key] > before[key] for key in ('InEchos', 'InEchoReps'))
    return dict(after, run_id=run_id, observed=observed,
                request_count=after['InEchos'], reply_count=after['InEchoReps'],
                count=max(0, after['InEchoReps'] - before['InEchoReps']))


def main():
    counts = read_counts(pathlib.Path('/proc/net/snmp').read_text())
    run_id = os.environ['CPRA_VERIFY_RUN_ID']
    if os.environ['CPRA_VERIFY_PHASE'] == 'before':
        print(json.dumps(dict(counts, run_id=run_id, request_count=counts['InEchos'],
                              reply_count=counts['InEchoReps'])))
    else:
        print(json.dumps(result(json.load(sys.stdin), counts, run_id)))


if __name__ == '__main__':
    main()
