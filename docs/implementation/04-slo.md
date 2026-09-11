# 04-slo: Add percentile SLO control

## Context

Implementation ticket from the approved Durable CPRa plan.

## What to implement

Five-minute latency histograms and exact attainment, persistent aggregate windows, queue feedback above the Erlang-C model floor.

## Where

internal/slo/; internal/queue/dynamic_worker_pool.go; internal/jobs/; internal/durable/

## Acceptance criteria

- [ ] Synthetic percentiles, omitted/timeout accounting, bounded control and burst recovery tests.
- [ ] Preserve existing manifests, read-only interfaces, Go 1.25 and optional driver tags.

## Out of scope

Multiple voters, hosted operations, subscriptions, raw check history, automatic unknown-action replay, infrastructure purchases and unverified release claims.

## Depends on

02-incidents

## Technical notes

The approved user plan is the requirements authority. Existing source artifacts and the separate documentation checkout must be preserved. File mapping may be refined within that scope during implementation.

## Definition of done

- [ ] Implementation and acceptance checks complete.
- [ ] Independent review complete.
- [ ] Relevant standard checks pass.
- [ ] Configuration, behavior and evidence documented.
