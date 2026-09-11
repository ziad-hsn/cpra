#!/bin/bash
set -euo pipefail
# Dedicated CI container. The pinned official image is the fixture, never an
# existing host service. Installing packages inside it does not mutate the host.
image=fedora:44@sha256:43b29f65a41eb9c35e1cd5323e3bdf3b655c2357a9f4f1ff2f9c2798e5045d80
name="cpra-rpm-ci-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-0}"
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
docker run -d -t -e container=docker --name "$name" --privileged --cgroupns=private --tmpfs /run --tmpfs /tmp \
  -v "$PWD:/workspace" -w /workspace "$image" \
  bash -c 'dnf -y install systemd python3 shadow-utils ca-certificates && exec /sbin/init'
for attempt in $(seq 1 120); do
  if docker exec "$name" test -d /run/systemd/system; then break; fi
  sleep 2
done
docker exec "$name" test -d /run/systemd/system
docker exec "$name" python3 scripts/release/package_lifecycle.py --isolated-ci --format rpm \
  --nfpm /workspace/bin/release-tools/nfpm --evidence "/workspace/evidence/package-rpm-${CPRA_PACKAGE_ARCH:-amd64}.json"
