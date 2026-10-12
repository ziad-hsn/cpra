---
title: Deploy a CPRa instance
description: Run a single CPRa owner with authenticated access, private manifests, target permissions and clear restart semantics.
cpra_scope: development
---

See [versions and availability](../versions.md) for current support and
qualification boundaries.


# Deploy a CPRa instance

Begin with a small, observed deployment using the monitor intervals and target types you actually need. The current preview is a single-process application; it does not establish a hosted service or high-availability cluster.

## Assign ownership and permissions

Run one process for a monitor configuration. Independent instances do not coordinate recovery or alert ownership. Give the process account only the access needed for its configured checks and actions.

Keep manifests containing credentials outside version control. A manifest authorizes outbound requests and local or remote recovery actions. `-ssrf-protect` restricts HTTP(S) destinations; it does not sandbox other protocols or local actions.

## Protect remote access

Initialize named management and TLS with the [tested setup](../management-startup.md).
System installations require a protected operator-token output outside service
configuration/state. Give the service access to its policy, wrapping key and TLS
files, while keeping the operator bearer separate. Services use absolute paths:

~~~sh
cpra -yaml /etc/cpra/monitors.yaml \
  -runtime-config /etc/cpra/runtime.yaml \
  -data-dir /var/lib/cpra -web.addr 0.0.0.0:8060
~~~

Configure the runtime TLS certificate and exact permitted HTTPS origin for the
published name. The generated localhost certificate is for local use; it is not
a remote service certificate. An explicitly configured trusted reverse proxy
requires its own documented identity/origin contract.

~~~sh
cpractl --server https://monitor.example.com \
  --token-file /protected/operator.token get state
~~~

The hostname and token path are placeholders for your protected installation.
Supply a CA file when the certificate uses a private authority. Token files are
reread per CLI request. Authentication failure never grants a less privileged
write path. Operator configuration privileges include choosing outbound targets;
CPRa does not enforce per-credential destination isolation or multi-tenancy.

## Observe before enabling recovery

Confirm that checks classify your target correctly and notifications reach a test destination. Then add the intended recovery action and verify its effect.

Recovery observes the configured per-incident attempt limit. Unknown external
outcomes remain held across restart. Verify provider evidence and use audited
review; restarting is not permission to repeat an interrupted action. Keep
committed state and retained history on persistent storage, and use a complete
[stopped backup](../durability.md) before replacing an artifact.

## Containers

~~~sh
docker build -f docker/Dockerfile -t cpra:local .
docker compose -f docker/docker-compose.yml up --build
~~~

Use the [production Compose/Helm guide](../container-helm.md) for a versioned
artifact, authenticated API, stable persistent volume and missing-mount checks.
Container loopback refers to that container, not the host.

The image runs as UID 1001, reads configuration under /etc/cpra and writes state
under /var/lib/cpra. Mount configuration read-only and retain the state volume.
Use bounded writable space/logs. Enable host integrations only with designated
permissions; Docker socket access gives control of the corresponding daemon.

## Health and shutdown

- Liveness reports the process and does not fail because monitored services are down.
- Readiness requires initialized admission, usable storage and controller progress;
  intentionally empty configurations are supported.
- `/metrics` exposes runtime and pipeline metrics.
- Shutdown stops admission/readiness first, drains bounded work and retains
  diagnostics during draining before closing dependencies.

The initial Unix/container application budget is 45 seconds within a 60-second
supervisor allowance. Confirm shutdown behavior for the actual deployment.
Use the [native service](../native-installation.md) and [single-owner Kubernetes](../container-helm.md)
procedures for upgrades, fencing and restore. Permanent storage failure keeps
admission unavailable until stopped repair or restore; no automatic deletion is
performed.

[Current limits](../release-notes.md#current-boundaries) · [Troubleshooting](common-tasks.md)
