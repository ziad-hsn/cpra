---
title: Worker sizing and queueing
description: The runtime Erlang C and Allen–Cunneen model, observed variability, headroom, fallback and capacity limits in CPRa.
cpra_scope: development
---

See [versions and availability](../versions.md) for implementation and
qualification boundaries.


# Worker sizing and queueing

The runtime `desiredCapacity` path calls `FindCForSLO`, using Erlang C with an Allen–Cunneen variability adjustment. Queueing theory is part of active worker scaling.

## Inputs and observations

| Symbol | Meaning |
| --- | --- |
| `λ` | Arrival demand in jobs per second. |
| `τ` | Mean execution time per job in seconds. |
| `μ = 1 / τ` | Mean service rate of one worker. |
| `cₐ` | Coefficient of variation of interarrival times. |
| `cₛ` | Coefficient of variation of service times. |
| `c` | Candidate number of workers. |

The queue retains the latest 256 arrival-interval observations; the pool retains the latest 256 execution-time observations. Missing variability uses the M/M/c baseline of one. A measured zero remains a valid observation.

The pool prefers an external demand estimate when available, then queue enqueue
and dequeue rates. The controller refreshes configured check demand after
management changes, disable/enable and snooze/unsnooze. It uses observed service
time when available and startup estimates otherwise.

## Mean waiting time

For a stable candidate, `c > λτ`, the implementation computes Erlang C's waiting probability `P(wait)`. It uses an Erlang-B recurrence to avoid the overflow of a direct factorial calculation.

~~~text
base queue wait = P(wait) / (cμ − λ)
variability factor = max(1, (cₐ² + cₛ²) / 2)
estimated mean total latency = τ + variability factor × base queue wait
~~~

The variability adjustment is floored at the M/M/c baseline: low measured variance does not reduce this estimate below that baseline.

`FindCForSLO` searches for the smallest stable worker count whose estimated mean total latency meets the target, within the configured maximum. The default runtime target adds the queue-latency budget to service time; an explicit sizing policy can provide a total latency target.

## Headroom and actuation

A successful model recommendation receives 15% default headroom. Startup sizing can provide a different policy. Backlog relief can raise the requested capacity for pools without an external demand estimate.

The result is then bounded by minimum and maximum workers, limited to twofold growth per adjustment, and rounded up. These actuation limits can delay convergence during a burst.

## Model states

| `sizing_model` | Meaning |
| --- | --- |
| `awaiting_observations` | Arrival or service estimates are insufficient. |
| `erlang_c_allen_cunneen` | The queueing model produced a feasible recommendation. |
| `little_law_fallback` | Invalid model inputs required an offered-load fallback. |
| `slo_unattainable` | The latency or capacity bound could not be met. |

The fallback uses offered load `λτ` with headroom. When the maximum worker count is the constraint, the pool requests that maximum, still subject to growth limits. When the latency target cannot accommodate service time itself, additional workers cannot make that target feasible.

## Percentile observations and feedback

The mean model provides an initial recommendation. Separately, bounded rolling
histograms and exact threshold counters observe scheduling-plus-queue delay,
execution time and scheduled-to-committed-result latency. Five-minute windows
report p50/p95/p99, sample coverage, missed work, timeouts and threshold
attainment. The default health-check targets are 250 ms queue delay and five
seconds to committed result. Missing coverage is not success.

Feedback evaluates recent observations every five seconds over a 30-second
control window, requiring at least 1,000 observations. Queue delay with queued
work can raise capacity within the existing bounds and twofold growth limit.
Reduction requires a 60-second healthy hold, is limited to 10% per adjustment,
and stays above the model recommendation. Downstream/controller limitations
are reported where adding workers cannot address them. See [SLO definitions](../slo.md).

## What the model does not prove

The analytical recommendation remains a mean-latency approximation. Observed
percentiles are measurements of covered work, not future guarantees or a
contractual SLA. Network failures, bursts, slow targets, scheduling and resource
contention can differ from the model's assumptions. Measure the workload and
inspect target saturation before increasing worker limits.

Focused runtime tests and short idle observations are development evidence.
The agreed comparative fleet campaign remains outstanding; the 24-hour run is
canceled. Neither source design nor helper benchmarks qualify fleet capacity.

[Capacity and diagnostics](../how-to/performance-tuning.md) · [SLO measurements](../slo.md)
