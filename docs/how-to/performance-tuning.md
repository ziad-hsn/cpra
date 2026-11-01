---
title: Capacity and diagnostics
description: Measure CPRa queue demand, service time, worker limits and target saturation without assuming unverified throughput guarantees.
cpra_scope: development
---

See [versions and availability](../versions.md) for implementation and
qualification boundaries.


# Capacity and diagnostics

Size CPRa from measurements of your workload. A configured target is separate
from a completed capacity test; there is no universal monitor-count,
memory-per-monitor or throughput guarantee.

## Start with the workload

Record monitor count, check intervals, target response times, failure rates, notification traffic, and any recovery actions. Check rate is influenced by the sum of monitor frequencies, not only the number of monitors.

~~~sh
./bin/cpractl get queues -o json
./bin/cpractl get pools -o json
./bin/cpractl get systems -o json
./bin/cpractl get slo -o json
./bin/cpractl get state -o json
./bin/cpractl metrics
~~~

Use the connection/TLS settings from [management startup](../management-startup.md).
Compare queue growth with service time, worker capacity, measured SLO coverage,
commit/storage health, CPU, memory and target-side rate limits. An unavailable
measurement is not zero latency or successful attainment.

## Interpret sizing telemetry

The runtime uses the [queueing model](../explanation/queueing-theory.md), measured
variability, headroom and worker bounds, plus observed percentile feedback.
`slo_unattainable` means the requested mean-latency or capacity bound cannot be
met. Separate [SLO observations](../slo.md) distinguish queue, target and
controller limitations and insufficient coverage.

A twofold per-adjustment growth limit means capacity changes are not instantaneous. Adding workers can increase pressure on a slow or overloaded target; measure that target before raising limits.

The local check floor sums `min(1, timeout / interval)` for each eligible monitor,
adds 15% headroom per driver and rounds up. Compiled but unused drivers reserve
no workers. Observed fast responses do not remove the floor before a late outage.
The global maximum remains authoritative. `worker_limit` and
`insufficient_driver_capacity` identify requested capacity that cannot be supplied.

Use `get pools -o wide` and `get queues -o wide` to inspect requested capacity,
supplied driver reservations, active work, pending submissions and oldest waiting
work. A queue lease includes submission, invocation and result handoff; actual
executing work is reported separately. Draining worker goroutines may include idle
goroutines after a capacity reduction. A field unavailable on an older server is
not a measured zero.

The check queue preserves driver allowances and fair destination flows.
Intervention queues serialize a private destination. Notification queues permit
independent sequences at the same destination: generic webhooks default to four
concurrent invocations, and other drivers default to one. Opening, update and
resolution for the same sequence remain ordered; historical keys that refer to
the same provider incident also share that ordering. Provider pacing and global
worker limits can impose a lower ceiling. Destination keys never appear in
observations or metric labels. No worker can be forcibly preempted; a reduced
limit waits for existing work to drain. A budget too small to run two operations
cannot provide simultaneous progress for two destinations.

Prometheus exports `cpra_driver_requested_capacity`,
`cpra_driver_reserved_capacity`, `cpra_driver_pending` and
`cpra_driver_inflight` as counts, with bounded `pipeline` and `driver` labels.
`cpra_driver_rejections_total` and `cpra_driver_timeouts_total` are cumulative
counts. `cpra_driver_oldest_pending_seconds` and
`cpra_driver_oldest_inflight_seconds` are durations in seconds and are absent
when the corresponding work is absent. Worker gauges separate requested,
reserved, running, executing, draining and pending-submission counts.

`cpra_driver_destination_concurrency` is the configured destination ceiling.
`cpra_driver_worker_headroom` is the allowance before pacing, ordering and durable
admission; it is not a count of notifications executable now.
`cpra_driver_rate_deferred_total` counts admitted copies observed waiting for
pacing, once per copy, rather than counting dispatcher polls. The queue gauge
`cpra_queue_awaiting_reconciliation` counts completed notification dispatches
that retain reservations until owner reconciliation. These metrics
never include destination, monitor, credential or private limiter identifiers.

`cpra_action_cancellations_total` counts committed cancellations, and
`cpra_action_definite_rejections_total` counts permanent proven-no-effect failures.
`cpra_action_exhaustions_total` distinguishes `attempt_limit` and
`execution_deadline`. Their labels are limited to the `code` and `intervention`
pipelines and these fixed reasons. They count new transitions since the store
opened, exclude historical replay, and reset on restart. They are sampled
counters, not a transactionally consistent snapshot of the retained action ledger.
An expired queued action can count as both canceled and deadline-exhausted; do not
sum these families as disjoint outcomes. Intermediate safe retries and unknown
effects do not count as exhaustion. Use the action and condition views for the
retained outcome, review requirements and original deadline.

## HTTP connection reuse

Status-only HTTP checks close each response body without waiting for an unused
body to arrive. With Go 1.27, the standard transport can drain a small unread
HTTP/1 response asynchronously and reuse its connection. Go 1.26 remains
supported, but these early closes can require a new connection on the next check.
See the [Go connection reuse guidance](https://go.dev/blog/http-connection-reuse).

This applies to checks using the shared HTTP client. Explicit per-monitor CA or
server-name settings currently use an isolated transport. Notifications and
interventions retain their separate transport and replay policy: connection reuse
does not establish that repeating an external effect is safe.

## Know where configuration lives

Worker limits, queue capacity, snapshot interval, and sizing policy are programmatic controller or worker-pool settings. They are not arbitrary fields in the monitor YAML. The server exposes only the [documented flags](../reference/cli.md).

The controller uses fair queues for all fleet sizes. `CPRA_ENTITY_THRESHOLD` is
no longer supported and startup rejects it rather than bypassing admission
fairness. Dynamic worker pools reject eager worker preallocation because it
conflicts with resizing.

## Profile a controlled instance

~~~sh
./bin/cpra -yaml monitors.yaml -runtime-config runtime.yaml -pprof -pprof.addr localhost:6060
go tool pprof http://localhost:6060/debug/pprof/profile
~~~

Use the runtime configuration produced by protected local initialization.
Profiling is disabled by default and restricted to loopback Host identities;
the command-line endpoint is not exposed. Use evidence to distinguish target
I/O, queues, durable writes, projections and controller work. Do not change
worker counts merely because target-side execution is slow.

The durable controller waits for the earliest check/action/control/SLO deadline
when idle, with coalesced background hints and a one-second progress fallback.
Pending work retains the configured update interval. A short empty/one-monitor
CPU observation qualifies only that setup; it does not measure a fleet's rate,
percentile attainment or endurance.

The comparative 10,000/100,000/1,000,000-monitor campaign remains outstanding.
Its physical-host disk and measurement requirements are described in
[verification](../validation.md). The 24-hour run is canceled.
