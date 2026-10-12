# Idle result-processing observation

This is private development evidence for Linux/WSL, not the release's matched
10,000/100,000/1,000,000-monitor benchmark. No endurance test ran.

The first instrumented observations used a normal Go 1.27.1 build with trimpath,
VCS stamping disabled and PGO off. Five-second warmup and ten-second CPU-profile
intervals measured an empty Raft-backed configuration and one HTTP monitor at
60-second cadence. Empty CPU use was 19.0% of one core; one-monitor use was 15.2%.
The CPU sample attributed much of the work to scheduler wakeups and periodically
polled result shards, with the resultProcessor path visible in the profile.

The repair replaces each shard's unconditional ten-millisecond ticker with a
timer active only for a partial batch. Producers still notify the processor;
full batches and cancellation retain their routing behavior. It changes no
controller tick rate, worker bound, capacity model or durable-write setting.

A subsequent comparison used the same compiler/settings and machine, with
three interleaved before/after runs for each workload. Each process initialized
its own Raft directory, warmed for five seconds and measured for ten seconds.
CPU came from process counters without active CPU profiling; a separate local
receiver counted target requests. The build records bind the executable digests
and source inventories. Unrelated host activity was present and load averages
were recorded.

| Workload | Before, mean (range), % of one core | After, mean (range), % of one core |
| --- | --- | --- |
| No monitors | 13.4 (12.4–14.0) | 8.9 (8.6–9.3) |
| One HTTP monitor / 60 seconds | 14.6 (13.5–15.3) | 9.5 (9.1–10.0) |

The roughly one-third reduction applies to these short local observations.
There is no claimed memory improvement: RSS varied between runs. Each measured
one-monitor interval received one request; empty intervals received none. All
owned processes stopped cleanly. This does not establish fleet throughput,
percentile SLO attainment, native-platform portability or long-run stability.

Deterministic tests cover full-batch immediate routing, a partial deadline that
later results cannot extend, a fresh deadline after idle, and cancellation
flushing the pending result. The complete queue suite, focused Go 1.25 checks,
and queue/controller race, drain and demand-refresh checks pass. Independent
source review of this finite repair remains required.

Privately retained evidence consists of the pilot profiles, executable build
records, paired progress report and measurement script. The current 200 Hz owner
loop and dispatch polling remain unchanged; further idle-loop changes require
their own wakeup/deadline correctness work and before/after measurements.
