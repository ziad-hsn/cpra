---
title: Candidate · Frequently asked questions
description: Candidate · Short, direct answers about what CPRa is, what it checks, what it does when a service fails, and what it deliberately does not do.
cpra_scope: release_candidate
---

> **Release candidate:** this page describes [`410fbfb`](https://github.com/ziad-hsn/cpra/commit/410fbfb0092d01277b3884cd04151c27443a4226), which is separate from `main`. See [version and availability](../versions.md).


# Frequently asked questions

## What is CPRa?

CPRa (Continuous Pulse and Recovery Agent) is a self-hosted monitoring and recovery agent written in Go. It runs health checks against services on a schedule, tracks incidents against failure and recovery thresholds, sends notifications, and runs a configured recovery action when a service fails. The server binary embeds a read-only dashboard and HTTP API. The separate `cpractl` command-line client provides API inspection and local installation, service, and backup operations.

## Is CPRa free and open source?

Yes. CPRa is licensed under the [MIT license](https://github.com/ziad-hsn/cpra/blob/main/LICENSE). The source is at [github.com/ziad-hsn/cpra](https://github.com/ziad-hsn/cpra). There is no hosted service or paid tier.

## What can CPRa check?

The default build checks HTTP, TCP, ICMP, DNS, UDP, TLS certificate expiry, Docker container state, and gRPC port reachability. Optional build tags add Redis, PostgreSQL, MySQL, MongoDB, RabbitMQ, and Kafka checks. See [drivers and fields](reference/jobs-reference.md).

## What does CPRa do when a check fails?

An initial failure can send a configured yellow notification. At the failure threshold, CPRa admits one eligible recovery operation. If no recovery is available, recovery fails, or healthy-check verification fails, it opens an incident and sends the configured red notification. The default build can restart a Docker container or call an HTTP webhook; optional build tags add Kubernetes rollout restart or scaling, EC2 instance reboot, and systemd unit restart. The incident closes after the configured number of consecutive healthy checks. See the [incident lifecycle](explanation/incident-lifecycle.md).

## Where can CPRa send alerts?

Log, Slack, PagerDuty, email (SMTP with STARTTLS), generic webhook, Telegram, Discord, Opsgenie, Mattermost, VictorOps, Pushover, and Datadog in the default build; Microsoft Teams and Twilio with build tags. Destinations can be grouped. Committed delivery state is retained per endpoint, so a successful endpoint is not repeated merely because another endpoint is unfinished. An ambiguous external outcome is held for operator investigation.

## How does CPRa differ from a metrics stack like Prometheus?

CPRa is not a time-series database and does not store metric history. It performs active checks, decides whether a service is up, and acts on the answer. It exposes its own process and worker-pool state on a Prometheus-compatible `/metrics` endpoint, so it can sit alongside a metrics stack rather than replace one.

## How does CPRa differ from an uptime monitor?

Uptime monitors typically stop at notification. CPRa also runs a recovery action you configure, with one attempt per incident and thresholds that must be met before the incident closes. The trade-off is that it is designed for a single process per configuration, not for a hosted multi-region service.

## Does CPRa support high availability or clustering?

No. Run one process for a monitor configuration. Single-node Raft preserves committed incident and delivery state across restarts; separate processes do not coordinate monitor ownership or provide distributed failover. An explicit memory mode is available for disposable runs. See [deployment and recovery](how-to/deploy-to-production.md).

## Does CPRa keep history?

CPRa retains 30 days of incident, intervention, and per-endpoint notification events, queryable by stable monitor ID through the API, CLI, and dashboard. It does not retain raw successful checks or a per-monitor latency time series. Backups must include the complete state directory, including the history catalog and daily segments.

## How is the dashboard secured?

The server listens on loopback by default. Binding to any other address requires an authentication token read from a file or environment variable; the browser login uses username `cpra` with the token as the password and the API accepts a Bearer token. The built-in listener serves HTTP, so remote access needs an HTTPS reverse proxy. See [deployment](how-to/deploy-to-production.md).

## What platforms does CPRa run on?

The release recipe targets Linux, macOS, and Windows on amd64 and arm64, Linux DEB/RPM packages, and Linux OCI images on both architectures. Compose and Helm use the same image. Cross-build success does not establish native execution support: see the [release engineering guide](release-engineering.md) for required native gates and recorded evidence. Source compatibility remains Go 1.25; the official recipe pins Go 1.27.1 and dashboard builds pin Node.js 24.21.0 and pnpm.

## How do I install it?

Use the [native installation guide](native-installation.md) for versioned Go installation, archives, packages, platform paths, and service setup. The [quickstart](tutorials/quickstart.md) also covers a foreground source build. Use [Compose or Helm](container-helm.md) for container deployments.

## Where does the name come from?

"CPR" is the pulse line: the agent keeps taking the pulse of a service and resuscitates it when it flatlines. The lowercase "a" is "agent". The gold loop in the mark is a nod to Ra, the sun that makes the same circuit every day.
