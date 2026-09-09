---
title: "Capacity and diagnostics"
description: "Measure CPRa queue demand, service time, worker limits and target saturation without assuming unverified throughput guarantees."
---

# Capacity and diagnostics

Size CPRa from measurements of your workload. There is no verified universal monitor count, memory-per-monitor figure, or throughput guarantee for this preview.

## Start with the workload

Record monitor count, check intervals, target response times, failure rates, notification traffic, and any recovery actions. Check rate is influenced by the sum of monitor frequencies, not only the number of monitors.

~~~sh
./bin/cpractl get queues -o json
./bin/cpractl get pools -o json
./bin/cpractl get systems -o json
./bin/cpractl metrics
~~~

Compare queue growth with service time, worker capacity, CPU, memory, and target-side rate limits.

## Interpret sizing telemetry

The runtime uses the [queueing model](../explanation/queueing-theory.md), measured variability, default headroom, and worker bounds. `slo_unattainable` means the requested mean-latency or worker-capacity bound cannot currently be met.

A twofold per-adjustment growth limit means capacity changes are not instantaneous. Adding workers can increase pressure on a slow or overloaded target; measure that target before raising limits.

## Know where configuration lives

Worker limits, queue capacity, snapshot interval, and sizing policy are programmatic controller or worker-pool settings. They are not arbitrary fields in the monitor YAML. The server exposes only the [documented flags](../reference/cli.md).

`CPRA_ENTITY_THRESHOLD` is a startup queue-selection input. Live queue migration is unsupported. Dynamic worker pools reject eager worker preallocation because it conflicts with resizing.

## Profile a controlled instance

~~~sh
./bin/cpra -yaml monitors.yaml -pprof -pprof.addr localhost:6060
go tool pprof http://localhost:6060/debug/pprof/profile
~~~

Keep the profiling listener on loopback. Profiling is disabled by default. Use captured evidence to distinguish target I/O, queueing, snapshot scans, and controller work.

Hybrid queues provide overflow handling; the segmented Workiva implementation serializes producers while publishing or sealing a segment. It should not be described as wholly lock-free.

The release review covered correctness and process behavior. It did not include a comparative performance benchmark or a long-duration soak test.
