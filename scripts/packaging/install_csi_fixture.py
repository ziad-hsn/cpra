#!/usr/bin/env python3
"""Install the pinned, non-production CSI hostpath fixture in an isolated kind cluster."""
import argparse
import json
from pathlib import Path
import subprocess


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--kubeconfig', required=True)
    p.add_argument('--context', required=True)
    args = p.parse_args()
    if not args.context.startswith('kind-cpra-'):
        p.error('this privileged test fixture requires a dedicated kind-cpra-* cluster')
    k = ['kubectl', '--kubeconfig', args.kubeconfig, '--context', args.context]
    nodes = json.loads(subprocess.check_output(k + ['get', 'nodes', '-o', 'json']))['items']
    if len(nodes) != 1 or not nodes[0]['metadata']['name'].startswith('cpra-'):
        p.error('CSI hostpath fixture supports only a single dedicated cpra-* kind node')
    manifest = Path(__file__).parent / 'fixtures/csi/fixture.yaml'
    subprocess.run(k + ['apply', '-f', str(manifest)], check=True, timeout=60)
    subprocess.run(k + ['-n', 'cpra-csi-fixture', 'rollout', 'status',
                        'statefulset/csi-hostpathplugin', '--timeout=240s'], check=True, timeout=270)


if __name__ == '__main__':
    main()
