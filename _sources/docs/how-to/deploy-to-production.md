---
title: "Deploy a CPRa instance"
description: "Run a single CPRa owner with authenticated access, private manifests, target permissions and clear restart semantics."
---

# Deploy a CPRa instance

Begin with a small, observed deployment using the monitor intervals and target types you actually need. The current preview is a single-process application; it does not establish a hosted service or high-availability cluster.

## Assign ownership and permissions

Run one process for a monitor configuration. Independent instances do not coordinate recovery or alert ownership. Give the process account only the access needed for its configured checks and actions.

Keep manifests containing credentials outside version control. A manifest authorizes outbound requests and local or remote recovery actions. `-ssrf-protect` restricts HTTP(S) destinations; it does not sandbox other protocols or local actions.

## Protect remote access

Create a token file readable by the service account, then start the server:

~~~sh
./bin/cpra -yaml /etc/cpra/monitors.yaml \
  -web.addr 0.0.0.0:8060 \
  -web.auth-file /run/secrets/cpra-token
~~~

Put an HTTPS reverse proxy in front of this HTTP listener. Browser login is username `cpra` with the token as password.

~~~sh
./bin/cpractl --server https://monitor.example.com \
  --token-file /run/secrets/cpra-token get overview
~~~

`monitor.example.com` is a placeholder for your protected endpoint. A token file overrides `CPRA_AUTH_TOKEN`. Liveness, readiness, and metrics requests also need the token when authentication is enabled.

## Observe before enabling recovery

Confirm that checks classify your target correctly and notifications reach a test destination. Then add the intended recovery action and verify its effect.

Each incident admits one operation. If a response is lost, inspect the target's actual state before repeating the action. Restarting CPRa resets in-memory incident state and can change what is admitted next.

## Containers

~~~sh
docker build -f docker/Dockerfile -t cpra:local .
docker compose -f docker/docker-compose.yml up --build
~~~

The supplied Compose example disables the web listener. Edit the manifest for addresses reachable from the container; its loopback address refers to the container itself.

The image runs as UID 1001 and expects `/etc/cpra/monitors.yaml`. Mount configuration read-only and provide writable storage for file-based notifications. Docker actions need access to the intended Docker daemon; mounting its socket grants substantial control over that daemon.

## Health and shutdown

- `/api/v1/healthz` reports process liveness.
- `/api/v1/readyz` requires a recent nonempty snapshot.
- `/metrics` exposes runtime and pipeline metrics.
- SIGINT or SIGTERM starts graceful shutdown; the web server stops before the controller.

Choose service-manager stop timeouts with enough room for your configured operation deadlines. Use an external supervisor to restart a failed process and an external observer for CPRa itself.

[Current limits](../release-notes.md#current-boundaries) · [Troubleshooting](common-tasks.md)
