---
title: "Worker sizing and queueing"
description: "The runtime Erlang C and Allen\u2013Cunneen model, observed variability, headroom, fallback and capacity limits in CPRa."
---

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

The pool prefers an external demand estimate when available, then queue enqueue and dequeue rates. It uses observed service time when available and startup estimates otherwise.

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

## What the model does not prove

This is a mean-latency approximation, not a p95 or p99 guarantee. Network failures, correlated bursts, slow targets, scheduling work, and resource contention can differ from its assumptions. Measure your workload and inspect target saturation before increasing worker limits.

The current release review observed the model in a running process. It did not establish a million-monitor capacity benchmark or a long-duration production soak result.

[Runtime implementation](https://github.com/ziad-hsn/cpra/blob/5995427cb0e2ef7276f74747afd639da1f03f84c/internal/queue/dynamic_worker_pool.go) · [Sizing mathematics](https://github.com/ziad-hsn/cpra/blob/5995427cb0e2ef7276f74747afd639da1f03f84c/internal/queue/sizing.go)
