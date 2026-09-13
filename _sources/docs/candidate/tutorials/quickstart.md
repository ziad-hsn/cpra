---
title: Candidate · Quickstart
description: Candidate · Build CPRa, run a local HTTP target and inspect your first monitor through the dashboard and CLI.
cpra_scope: release_candidate
---

> **Release candidate:** this page describes [`410fbfb`](https://github.com/ziad-hsn/cpra/commit/410fbfb0092d01277b3884cd04151c27443a4226), which is separate from `main`. See [version and availability](../../versions.md).


# Run your first monitor

This walkthrough uses a local HTTP target and a log notification. You need Git, Go 1.25 or later, Make, and Python 3 for the demo target. The dashboard assets are already included in the Go source.

## 1. Build CPRa

~~~sh
git clone --branch codex/release-engineering https://github.com/ziad-hsn/cpra.git
cd cpra
git checkout --detach 410fbfb0092d01277b3884cd04151c27443a4226
make
./bin/cpra -version
~~~

The build produces `bin/cpra` and `bin/cpractl`. See [installation](getting-started.md) for optional drivers and container builds.

## 2. Start a local target

In another terminal:

~~~sh
mkdir -p /tmp/cpra-demo
printf 'ok\n' > /tmp/cpra-demo/health
python3 -m http.server 8080 --bind 127.0.0.1 --directory /tmp/cpra-demo
~~~

This serves `http://127.0.0.1:8080/health`.

## 3. Start CPRa

In the CPRa checkout:

~~~sh
cp examples/monitors.yaml monitors.yaml
./bin/cpra -yaml monitors.yaml -data-dir ./demo-state
~~~

The example checks the target every 30 seconds, uses a five-second timeout, and writes incident transitions to `alerts.jsonl`. Keep both processes running. This demo explicitly stores durable state under `./demo-state`; retain that directory if you restart the same demo.

## 4. Inspect the monitor

Open [the local dashboard](http://localhost:8060), or use another terminal:

~~~sh
./bin/cpractl health
./bin/cpractl get monitors
./bin/cpractl get overview -o json
~~~

A new monitor can appear as `unknown` before its first check. Allow a check interval and a dashboard snapshot refresh before assessing its state.

## 5. Observe a failure and recovery

Stop the demo HTTP server. After the configured consecutive failures, inspect the dashboard and `alerts.jsonl`. Restart the same demo server and allow the configured consecutive healthy checks for recovery.

This example has no recovery action configured: you restart the target yourself. [Add an action deliberately](../reference/config-schema.md#recovery) after confirming the monitoring and notification behavior.

Use Ctrl+C to stop CPRa and the demo target. The default listener is local; use the [authenticated deployment guide](../how-to/deploy-to-production.md) for remote access.

Use `cpractl get state` to inspect storage and `cpractl get history MONITOR_ID` for the timeline. Obtain the stable ID from the monitor JSON response. Stopping the demo leaves its manifest, alert log and state directory in place; it does not delete retained history.
