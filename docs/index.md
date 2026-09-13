---
title: CPRa documentation
description: Run CPRa to check services, send alerts and perform configured recovery actions. Start with the current Go application, dashboard and CLI.
cpra_scope: main
---

> **Current main:** applies to the application at [`51a835a`](https://github.com/ziad-hsn/cpra/commit/51a835a29f2fb7af2e0301910042a52e308cbb24). See [version and availability](versions.md) for newer candidate work.


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

The current application includes a read-only dashboard, an HTTP API, and the `cpractl` command-line client. These guides describe the source at `51a835a`, published to `main` on **11 September 2026**.

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

Run one process for each monitor configuration. Incident state lives in memory and resets when the process restarts. Separate CPRa instances do not coordinate ownership.

Worker sizing uses Erlang C with an Allen–Cunneen variability adjustment. It estimates mean latency; actual capacity depends on your targets, intervals, host, and workload. This preview does not establish a million-monitor benchmark, a percentile latency guarantee, or high availability.

[Deployment guide](how-to/deploy-to-production.md) · [Queueing model](explanation/queueing-theory.md) · [Current changes](release-notes.md) · [FAQ](faq.md)

## Follow the latest development

Use [versions and availability](versions.md) to choose between these main guides, the [durable release candidate](candidate/index.md), the [Go SDK candidate](sdk/index.md), and the [approved management plan](implementation/api-management-plan.md). Their implementation and publication states are recorded separately.
