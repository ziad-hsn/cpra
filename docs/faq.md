---
title: Frequently asked questions
description: Short, direct answers about what CPRa is, what it checks, what it does when a service fails, and what it deliberately does not do.
cpra_scope: development
---

See [versions and availability](versions.md) for current implementation and
qualification boundaries.


# Frequently asked questions

## What is CPRa?

CPRa (Continuous Pulse and Recovery Agent) is a self-hosted monitoring and
recovery agent written in Go. It checks services, tracks incidents, notifies
configured destinations and runs eligible recovery actions. Its embedded
dashboard and CLI can manage configuration with named operator access;
observation access remains read-only.

## Is CPRa free and open source?

Yes. CPRa is licensed under the [MIT license](https://github.com/ziad-hsn/cpra/blob/main/LICENSE). The source is at [github.com/ziad-hsn/cpra](https://github.com/ziad-hsn/cpra). There is no hosted service or paid tier.

## What can CPRa check?

The default build checks HTTP, TCP, ICMP, DNS, UDP, TLS certificate expiry,
Docker container state and gRPC. Omitted gRPC mode checks TCP reachability;
explicit health mode checks the configured unary health service. Optional tags
add Redis, PostgreSQL, MySQL, MongoDB, RabbitMQ and Kafka. See
[drivers and fields](reference/jobs-reference.md).

## What does CPRa do when a check fails?

An initial failure can send a configured yellow notification. At the failure threshold, CPRa admits one eligible recovery operation. If no recovery is available, recovery fails, or healthy-check verification fails, it opens an incident and sends the configured red notification. The default build can restart a Docker container or call an HTTP webhook; optional build tags add Kubernetes rollout restart or scaling, EC2 instance reboot, and systemd unit restart. The incident closes after the configured number of consecutive healthy checks. See the [incident lifecycle](explanation/incident-lifecycle.md).

## Where can CPRa send alerts?

Log, Slack, PagerDuty, email (SMTP with STARTTLS), generic webhook, Telegram,
Discord, Opsgenie, Mattermost, VictorOps, Pushover, Datadog and A2A are in the
default build; Microsoft Teams and Twilio use build tags. Receivers and groups
reuse destinations. Each endpoint retains its delivery outcome, including
unknown effects; provider acceptance does not prove a human read the alert.

## How does CPRa differ from a metrics stack like Prometheus?

CPRa is not a time-series database and does not store metric history. It performs active checks, decides whether a service is up, and acts on the answer. It exposes its own process and worker-pool state on a Prometheus-compatible `/metrics` endpoint, so it can sit alongside a metrics stack rather than replace one.

## How does CPRa differ from an uptime monitor?

CPRa also runs a configured recovery action, subject to its per-incident attempt
limit and subsequent healthy-check verification. It operates as a single owner
for a configuration. It does not provide multi-region failover.

## Does CPRa support high availability or clustering?

No. One process owns a monitor configuration and exclusively locks its data
directory. Single-node Raft preserves committed state across restart; it does
not provide distributed failover or cross-instance monitor coordination. Use
[persistent deployment storage](durability.md).

## Does CPRa keep history?

Each monitor has a retained incident/action/notification timeline, normally
30 days. Successful raw checks and per-monitor percentile histories are not
retained. Rolling SLO distributions and threshold counters describe current
measurement windows, with availability and restart coverage reported explicitly.

## How is the dashboard secured?

The recommended first run initializes named management access and localhost
TLS. Sign in with the protected operator bearer after manually trusting the
generated local CA. Management writes require granted permissions and approved
HTTPS origin; authentication failure never downgrades a write to observation
mode. The API also supports separately configured read-only access. See
[management startup](management-startup.md) and [deployment](how-to/deploy-to-production.md).

## What platforms does CPRa run on?

The planned matrix includes Linux/macOS/Windows amd64 and arm64 archives, Linux
DEB/RPM packages, two Linux OCI architectures, Compose and Helm. Native and
deployment execution gates remain open. Source supports Go 1.25; dashboard
rebuilds use pinned Node and pnpm. [Availability](versions.md) distinguishes a
build recipe from a qualified public artifact.

## How do I install it?

Follow the [quickstart](tutorials/quickstart.md): build matching source, initialize
protected management, start with its printed command, connect with its CA/token
files, then apply the example monitor. It includes the explicit browser trust
step and durable restart behavior.

## Where does the name come from?

"CPR" is the pulse line: the agent keeps taking the pulse of a service and resuscitates it when it flatlines. The lowercase "a" is "agent". The gold loop in the mark is a nod to Ra, the sun that makes the same circuit every day.

## Are the implementation and public releases the same thing?

Current source implements persistence, management and SDK/worker contracts.
Public module tags and release artifacts require their separate qualification
and downloaded-asset checks. Candidates remain private while gates are open.
Local provider fixtures are contract evidence; actual account delivery/effects
require designated resources and credentials. The 24-hour test is cancelled.

## How do I change the appearance?

Use the dashboard Settings page to choose System, Light or Dark. System follows your operating system. The documentation header cycles the same three choices. Both preserve explicit preferences across reloads.
