---
title: "Durable CPRa candidate"
description: "Implemented durability and operator interfaces, completed checks, and outstanding release evidence."
---

# Durable CPRa candidate

This documentation describes the `codex/durable-cpra` implementation candidate.
The source revision appears in the page footer and `site-version.json`.
Publication to `main` and the public documentation site is gated on review and
agreed release evidence. The previous published baseline is
[a370969](https://github.com/ziad-hsn/cpra/commit/a370969b041b399c0778318d8915ce059fd74294).

## Implemented behavior

- Single-node Raft storage is enabled by default in `./cpra-data`, with explicit
  memory-only configuration for disposable processes.
- Incident transitions and action intent are committed before external work.
  Restart resumes health checks and queued work, while holding interrupted
  started actions as unknown. Successful endpoints are not repeated to complete
  another endpoint.
- Stable monitor IDs and configuration revisions prevent old queued work from
  being redirected to a changed target. Removing a monitor cancels unsent work.
- Incident, intervention and notification events are retained for 30 days in
  daily local segments. Raw check history and hourly aggregates are not retained.
- Five-minute distributions and exact threshold counters measure queue delay,
  execution duration and scheduled-to-committed-result latency. Missed work,
  timeouts, unfinished checks and recovery coverage gaps remain visible.
- Erlang C and Allen–Cunneen remain the initial sizing model. Bounded observed
  latency feedback augments that recommendation.
- Authenticated read-only history, SLO and persistence APIs, matching CLI commands
  and dashboard views expose this state with bounded monitor/history pages.
- Configured live-provider and external benchmark harnesses record actual results
  and distinguish unconfigured cases from passing evidence.

## Completed verification

Go 1.25 compatibility checks, default and all-driver race tests, formatting and
vet checks have passed during implementation. Dashboard build, type checks, lint
and 15 component tests passed. Go vulnerability scans found no affected reachable
symbols or imported packages; four advisories remain only in unused packages of
required modules.

Real subprocess tests cover forced termination before admission, after intent,
after the started marker, after external webhook success and after result commit.
The observed recovery behavior matches the conservative unknown-outcome policy.
Local production-driver runs have passed HTTP checks, Docker checks, webhook and
Docker recovery, webhook notification and file-log delivery. Provider acceptance
is recorded separately from independently observed effects. Failed fixture runs
remain in local evidence; they are not converted into passes.

## Outstanding release gates

- Full-account verification of all 33 driver types remains incomplete. User-owned
  accounts, credentials, designated targets and independent receipt observers
  are required. Local fixtures cannot certify cloud production accounts.
- The physical C: free-space prerequisite has failed (approximately 4 GB free
  during this implementation, below 30 GiB plus fixture headroom). Database
  fixture downloads and large performance campaigns were not started.
- Matched 10k/100k/1m comparisons and the uninterrupted 24-hour million-monitor
  campaign have not completed. A ten-monitor harness smoke run is explicitly
  excluded from scale and endurance evidence.
- Independent review and the final publication gates remain open.

## Operational boundaries {#current-boundaries}

This is one process and one voter with local persistence. It provides no
multi-node failover. The configured health-check targets are p99 queue delay
at most 250 ms and scheduled-to-committed result at most five seconds; measured
attainment is conditional on the observed workload and complete window coverage.
It is not an unconditional SLA. Unknown external actions require operator
investigation and are never automatically replayed. MongoDB SRV discovery and
Kubernetes exec credential plugins remain unsupported.

[Recovery and backups](durability.md) · [Latency accounting](slo.md) ·
[Verification requirements](validation.md)
