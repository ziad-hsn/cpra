# 03-history: Retain monitor event history

## Context

Implementation ticket from the approved Durable CPRa plan.

## What to implement

Daily indexed bbolt segments, 30-day event retention, replay identity, history compaction barrier and complete backup validation.

## Where

internal/durable/history.go

## Acceptance criteria

- [ ] Retention, pagination, replay and restore tests; no retained raw check records.
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
