#!/usr/bin/env python3
"""Check independently exported agent effects against the original action.

The exporter belongs to the designated destination or recovery target. It must
not turn a CPRa response or A2A task status alone into an observed effect.
"""
import argparse
import hashlib
import json
import math
import os
import pathlib
import sys
import time


def unique_fields(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate observation field')
        result[key] = value
    return result


def identity(environment):
    values = {key: environment.get(variable, '') for key, variable in (
        ('run_id', 'CPRA_VERIFY_RUN_ID'), ('monitor_id', 'CPRA_VERIFY_MONITOR_ID'),
        ('action_id', 'CPRA_VERIFY_ACTION_ID'))}
    if any(not value or len(value.encode()) > 256 or any(ord(c) < 32 for c in value)
           for value in values.values()):
        raise ValueError('missing or invalid committed verification identity')
    return values


def inspect(path, expected, role, started):
    if not path.exists():
        return set()
    with path.open('rb') as stream:
        data = stream.read(16 * 1024 * 1024 + 1)
    if len(data) > 16 * 1024 * 1024:
        raise ValueError('agent observation file exceeds 16 MiB')
    identifiers = set()
    effect = 'recovery_completed' if role == 'intervention' else 'incident_received'
    for line in data.splitlines():
        if len(line) > 64 * 1024:
            raise ValueError('agent observation record exceeds 64 KiB')
        row = json.loads(line, object_pairs_hook=unique_fields)
        if not isinstance(row, dict):
            raise ValueError('agent observation record must be an object')
        at = row.get('observed_at')
        provider_id = row.get('provider_id')
        if (all(row.get(key) == value for key, value in expected.items())
                and row.get('role') == role and row.get('effect') == effect
                and row.get('observed') is True and type(at) in (int, float)
                and math.isfinite(at) and started <= at <= time.time()
                and isinstance(provider_id, str) and 0 < len(provider_id.encode()) <= 256):
            identifiers.add(provider_id)
    return identifiers


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('role', choices=('intervention', 'notification'))
    parser.add_argument('receipt_file', type=pathlib.Path)
    parser.add_argument('--timeout', type=float, default=60)
    args = parser.parse_args()
    try:
        if not math.isfinite(args.timeout) or not 0 <= args.timeout <= 120:
            raise ValueError('observer timeout must be between zero and 120 seconds')
        expected = identity(os.environ)
        phase = os.environ.get('CPRA_VERIFY_PHASE')
        if phase == 'before':
            print(json.dumps(dict(expected, started=time.time())))
            return 0
        if phase != 'after':
            raise ValueError('invalid observation phase')
        raw = sys.stdin.buffer.read(65537)
        if len(raw) > 65536:
            raise ValueError('baseline exceeds 64 KiB')
        before = json.loads(raw, object_pairs_hook=unique_fields)
        if (not isinstance(before, dict) or any(before.get(key) != value for key, value in expected.items())
                or type(before.get('started')) not in (int, float) or not math.isfinite(before['started'])
                or not 0 < before['started'] <= time.time()):
            raise ValueError('baseline belongs to a different action or time')
        deadline = time.monotonic() + args.timeout
        while True:
            matches = inspect(args.receipt_file, expected, args.role, before['started'])
            if matches or time.monotonic() >= deadline:
                encoded = json.dumps(sorted(matches), separators=(',', ':')).encode()
                print(json.dumps({'observed': len(matches) == 1, 'count': len(matches),
                                  'resource_digest': hashlib.sha256(encoded).hexdigest()}))
                return 0
            time.sleep(min(1, max(0, deadline - time.monotonic())))
    except (OSError, ValueError, TypeError, KeyError, OverflowError):
        print('Agent effect evidence is unavailable or invalid.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
