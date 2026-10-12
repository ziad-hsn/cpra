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

## Know where configuration lives

Worker limits, queue capacity, snapshot interval, and sizing policy are programmatic controller or worker-pool settings. They are not arbitrary fields in the monitor YAML. The server exposes only the [documented flags](../reference/cli.md).

`CPRA_ENTITY_THRESHOLD` is a startup queue-selection input. Live queue migration is unsupported. Dynamic worker pools reject eager worker preallocation because it conflicts with resizing.

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

Hybrid queues provide overflow handling; the segmented Workiva implementation serializes producers while publishing or sealing a segment. It should not be described as wholly lock-free.

The comparative 10,000/100,000/1,000,000-monitor campaign remains outstanding.
Its physical-host disk and measurement requirements are described in
[verification](../validation.md). The 24-hour run is canceled.
