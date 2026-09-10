---
title: "CPRa documentation"
description: "Run CPRa to check services, send alerts and perform configured recovery actions. Start with the current Go application, dashboard and CLI."
---

# Check services. Understand failures. Recover deliberately.

CPRa (Continuous Pulse and Recovery Agent) checks your services, sends incident notifications, and runs the recovery actions you configure. It is free, self-hosted software under the MIT license.

The current application includes a read-only dashboard, an HTTP API, and the `cpractl` command-line client. These guides describe the durable implementation candidate. See [release notes](release-notes.md) for its source revision and outstanding evidence gates.

[Run your first monitor](tutorials/quickstart.md){ .md-button .md-button--primary }
[Explore the configuration](reference/config-schema.md){ .md-button }

## What you can do

<div class="grid cards" markdown>

- **Check a service**

    Monitor HTTP, TCP, DNS, TLS certificates, containers, and more. Add database and broker checks with optional build tags.

    [Check types and fields](reference/jobs-reference.md)

- **Follow an incident**

    Set failure and healthy-check thresholds. Send notifications to individual destinations or reusable groups.

    [Incident behavior](explanation/incident-lifecycle.md)

- **Configure recovery**

    Trigger a Docker or webhook action, or build in Kubernetes, AWS, and systemd support.

    [Recovery actions](reference/jobs-reference.md#recovery-actions)

- **Inspect the process**

    Use the dashboard, CLI, API, and Prometheus metrics to understand monitor and worker-pool state.

    [Dashboard and API](reference/api-reference.md)

</div>

## Start with a small deployment

Run one process for each monitor configuration. Single-node Raft retains committed incident state by default. Separate CPRa instances do not coordinate ownership. Retain the complete data directory across restarts.

Worker sizing uses Erlang C with an Allen–Cunneen variability adjustment. It estimates mean latency and is augmented by observed percentile feedback; actual capacity depends on your targets, intervals, host, and workload. This preview does not establish a million-monitor benchmark, a percentile latency guarantee, or high availability.

[Deployment guide](how-to/deploy-to-production.md) · [Queueing model](explanation/queueing-theory.md) · [Current changes](release-notes.md)
