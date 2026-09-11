---
title: "Durable CPRa candidate"
description: "Implemented durability and operator interfaces, completed checks, and outstanding release evidence."
---

# Durable CPRa candidate

This documentation describes the `codex/release-engineering` implementation candidate.
The source revision appears in the page footer and `site-version.json`.
Publication to `main` and the public documentation site is gated on review and
agreed release evidence. The previous published baseline is
[a370969](https://github.com/ziad-hsn/cpra/commit/a370969b041b399c0778318d8915ce059fd74294).

## Implemented behavior

- Single-node Raft storage defaults to the platform user state directory; system
  services use explicit native paths. Legacy `./cpra-data` requires an explicit
  selection or stopped migration. Memory-only mode remains explicit.
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

## Release engineering changes

The candidate adds canonical Go installation, recorded unstripped release builds,
standard platform paths, native supervisor integration, stopped backup/restore,
format-specific DEB/RPM lifecycle handling, exact-binary OCI images, production
Compose and a validated single-owner Helm chart. The release workflow must pass
its gates before exposing a stable Git tag to Go installation.

See [native operations](native-installation.md), [container and Helm operations](container-helm.md),
and [release engineering](release-engineering.md) for the implemented contracts.

## Verification status

Implementation tests and candidate evidence are recorded separately from final
release qualification. Default Go 1.27.1 tests and dashboard type/lint/component
checks passed during this work. Native Windows tests exercise metadata replacement,
real process-crash boundaries, history, locking and stopped backup/restore; native
SCM installation is a separate gate. Full final-source reports must identify the
exact source and artifact digest before a released platform is claimed.

Historical provider fixtures and prior candidate checks do not automatically
qualify a new artifact. The final release record must include reproducibility,
native platform/service/package lifecycle, image/chart operations, vulnerability
scans and documentation checks.

## Outstanding release gates

- Full-account verification of all 33 driver types remains incomplete. User-owned
  accounts, credentials, designated targets and independent receipt observers
  are required. Local fixtures cannot certify cloud production accounts.
- The large campaign must recheck physical C: free space before starting: at
  least 30 GiB plus measured fixture headroom. The earlier low-disk result is
  historical, and freeing space does not itself complete a campaign.
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
