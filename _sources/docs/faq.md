---
title: "Frequently asked questions"
description: "Short, direct answers about what CPRa is, what it checks, what it does when a service fails, and what it deliberately does not do."
---

# Frequently asked questions

## What is CPRa?

CPRa (Continuous Pulse and Recovery Agent) is a self-hosted monitoring and recovery agent written in Go. It runs health checks against services on a schedule, tracks incidents against failure and recovery thresholds, sends notifications, and runs a configured recovery action when a service fails. It ships as one static binary with an embedded read-only dashboard, an HTTP API, and the `cpractl` command-line client.

## Is CPRa free and open source?

Yes. CPRa is licensed under the [MIT license](https://github.com/ziad-hsn/cpra/blob/main/LICENSE). The source is at [github.com/ziad-hsn/cpra](https://github.com/ziad-hsn/cpra). There is no hosted service or paid tier.

## What can CPRa check?

The default build checks HTTP, TCP, ICMP, DNS, UDP, TLS certificate expiry, Docker container state, and gRPC port reachability. Optional build tags add Redis, PostgreSQL, MySQL, MongoDB, RabbitMQ, and Kafka checks. See [drivers and fields](reference/jobs-reference.md).

## What does CPRa do when a check fails?

After the configured number of consecutive failures, CPRa opens an incident, notifies the configured destinations, and runs the monitor's recovery action once. The default build can restart a Docker container or call an HTTP webhook; optional build tags add Kubernetes rollout restart or scaling, EC2 instance reboot, and systemd unit restart. The incident closes after the configured number of consecutive healthy checks. See the [incident lifecycle](explanation/incident-lifecycle.md).

## Where can CPRa send alerts?

Log, Slack, PagerDuty, email (SMTP with STARTTLS), generic webhook, Telegram, Discord, Opsgenie, Mattermost, VictorOps, Pushover, and Datadog in the default build; Microsoft Teams and Twilio with build tags. Destinations can be grouped; a group succeeds when at least one destination accepts delivery.

## How does CPRa differ from a metrics stack like Prometheus?

CPRa is not a time-series database and does not store metric history. It performs active checks, decides whether a service is up, and acts on the answer. It exposes its own process and worker-pool state on a Prometheus-compatible `/metrics` endpoint, so it can sit alongside a metrics stack rather than replace one.

## How does CPRa differ from an uptime monitor?

Uptime monitors typically stop at notification. CPRa also runs a recovery action you configure, with one attempt per incident and thresholds that must be met before the incident closes. The trade-off is that it is designed for a single process per configuration, not for a hosted multi-region service.

## Does CPRa support high availability or clustering?

No. Run one process for a monitor configuration. Incident and delivery state is held in memory, resets on restart, and separate processes do not coordinate ownership. See [behavior and limits](https://github.com/ziad-hsn/cpra#behavior-and-limits).

## Does CPRa keep history?

Not per monitor. The dashboard reports a healthy-sample percentage for the current process run and incident transitions are written to the configured log destinations, but there is no retained time series.

## How is the dashboard secured?

The server listens on loopback by default. Binding to any other address requires an authentication token read from a file or environment variable; the browser login uses username `cpra` with the token as the password and the API accepts a Bearer token. The built-in listener serves HTTP, so remote access needs an HTTPS reverse proxy. See [deployment](how-to/deploy-to-production.md).

## What platforms does CPRa run on?

Release builds produce Linux amd64 and arm64 archives for the server and CLI, and the repository includes a Dockerfile for a container that runs as UID 1001. Building from source needs Go 1.25 or later; rebuilding the dashboard needs Node.js 24 and pnpm.

## How do I install it?

Follow the [quickstart](tutorials/quickstart.md): build, copy the example manifest, set a service address, run `./bin/cpra -yaml monitors.yaml`, and open `http://localhost:8060`.

## Where does the name come from?

"CPR" is the pulse line: the agent keeps taking the pulse of a service and resuscitates it when it flatlines. The lowercase "a" is "agent". The gold loop in the mark is a nod to Ra, the sun that makes the same circuit every day.
