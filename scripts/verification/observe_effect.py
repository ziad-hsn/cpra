#!/usr/bin/env python3
"""Independent recovery observations. Commands use configured SDK/CLI identity.

No restart command is executed here: only the production CPRa driver performs
that operation. JSON output contains hashes and counters, never credentials.
"""
import argparse
import hashlib
import json
import os
import subprocess
import sys
import time


def output(args):
    return subprocess.check_output(args, text=True, stderr=subprocess.DEVNULL, timeout=20).strip()


def kubernetes_state(row):
    status = row.get('status', {})
    generation = row['metadata']['generation']
    replicas = row['spec'].get('replicas', 1)
    # Updated replicas and available replicas alone can describe a new unready
    # pod alongside the old available pod. Require the rollout to converge.
    ready = (status.get('observedGeneration', 0) >= generation and
             all(status.get(key, 0) == replicas for key in
                 ('replicas', 'updatedReplicas', 'readyReplicas', 'availableReplicas')) and
             status.get('unavailableReplicas', 0) == 0)
    return {'identity': row['metadata']['uid'], 'generation': generation, 'ready': ready}


def main():
    p = argparse.ArgumentParser()
    p.add_argument('kind', choices=['docker', 'kubernetes', 'systemd', 'boot-id'])
    p.add_argument('target')
    p.add_argument('--namespace', default='cpra-verification')
    p.add_argument('--context')
    args = p.parse_args()
    run_id = os.environ['CPRA_VERIFY_RUN_ID']

    def read():
        if args.kind == 'docker':
            row = json.loads(output(['docker', 'inspect', args.target]))[0]
            state = row['State']
            return {'identity': row['Id'], 'generation': state['StartedAt'], 'ready': state['Running']}
        if args.kind == 'systemd':
            fields = dict(x.split('=', 1) for x in output(['systemctl', 'show', args.target, '--property=InvocationID,ActiveState']).splitlines())
            return {'identity': args.target, 'generation': fields['InvocationID'], 'ready': fields['ActiveState'] == 'active'}
        if args.kind == 'boot-id':
            # Target is an SSH host alias from user configuration. A changed
            # kernel boot UUID proves reboot completion rather than API acceptance.
            boot = output(['ssh', '-oBatchMode=yes', '-oConnectTimeout=10', args.target, 'cat /proc/sys/kernel/random/boot_id'])
            return {'identity': args.target, 'generation': boot, 'ready': bool(boot)}
        command = ['kubectl'] + (['--context', args.context] if args.context else []) + ['-n', args.namespace]
        row = json.loads(output(command + ['get', args.target, '-o', 'json']))
        return kubernetes_state(row)

    def sanitized(row):
        return {'resource_digest': hashlib.sha256(str(row['identity']).encode()).hexdigest(),
                'generation_digest': hashlib.sha256(str(row['generation']).encode()).hexdigest(), 'ready': row['ready'], 'run_id': run_id}

    if os.environ['CPRA_VERIFY_PHASE'] == 'before':
        print(json.dumps(sanitized(read())))
        return
    before = json.load(sys.stdin)
    deadline = time.monotonic() + 100
    while True:
        try:
            after = sanitized(read())
            observed = before['run_id'] == run_id and before['resource_digest'] == after['resource_digest'] and before['generation_digest'] != after['generation_digest'] and after['ready']
            if observed or time.monotonic() >= deadline:
                print(json.dumps(dict(after, observed=observed, previous_generation_digest=before['generation_digest'])))
                return
        except subprocess.SubprocessError:
            if time.monotonic() >= deadline:
                raise
        time.sleep(1)


if __name__ == '__main__':
    main()
