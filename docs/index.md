---
title: CPRa documentation
description: Run CPRa to check services, send alerts and perform configured recovery actions. Start with the current Go application, dashboard and CLI.
cpra_scope: main
---

> **Current development:** includes durable management and the writable dashboard.
> Start with the [protected management quickstart](tutorials/quickstart.md).
> [Versions and availability](versions.md) distinguishes implemented behavior
> from completed qualification and published artifacts.

<div class="cpra-hero" markdown>

![CPRa](images/cpra-horizontal-color.svg#only-light){ width="464" height="128" }
![CPRa](images/cpra-horizontal-dark.svg#only-dark){ width="464" height="128" }

# Continuous Pulse and Recovery Agent { #check-services-understand-failures-recover-deliberately }

Checks services, sends alerts, and runs the recovery you configure.
{ .cpra-hero__description }

Go · MIT · self-hosted
{ .cpra-hero__meta }

[Run your first monitor](tutorials/quickstart.md){ .md-button .md-button--primary }
[Explore the configuration](reference/config-schema.md){ .md-button }

</div>

CPRa (Continuous Pulse and Recovery Agent) is a self-hosted monitoring and recovery agent written in Go. It runs health checks against your services on a schedule, opens and closes incidents against thresholds you set, sends notifications, and executes a configured recovery action when a service fails. It is free software under the MIT license, distributed as a single static binary.

The current source includes a management dashboard, an HTTP API and the
`cpractl` command-line client. Named operators manage configuration and controls;
read-only identities observe their granted resources. Dated engineering records
retain their historical scope in the source repository and are excluded from the public site.

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

Run one owner for each state directory. This development source defaults to
single-node Raft and restores committed incident state after restart. An explicit
memory-only mode is available for disposable runs. Separate CPRa instances do not
coordinate monitor ownership. See [durability](durability.md) for recovery behavior.

Worker sizing uses Erlang C with an Allen–Cunneen variability adjustment and
observed latency feedback. Percentile SLO measurements remain distinct from an
SLA guarantee. Comparative fleet measurements and provider-account qualification
remain open [release gates](versions.md). No 24-hour run is part of this work.

[Deployment guide](how-to/deploy-to-production.md) · [Queueing model](explanation/queueing-theory.md) · [Current changes](release-notes.md) · [FAQ](faq.md)

## Follow the latest development

Use [versions and availability](versions.md), the [Go SDK guide](sdk/index.md), and
the [management guide](cpractl-management.md) for current behavior. Implementation,
local verification, qualification and publication are recorded separately.
