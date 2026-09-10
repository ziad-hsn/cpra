# 01-storage: Establish durable Raft storage

## Context

Implementation ticket from the approved Durable CPRa plan.

## What to implement

Default single-node Raft, stable monitor identities, versioned records, synchronous batched commits and frozen snapshots.

## Where

internal/durable/{model,store,fsm,files}.go; internal/runtimeconfig/config.go; internal/loader/schema/{manifest,identity}.go; internal/controller/entities/mapper.go; main.go

## Acceptance criteria

- [ ] Forced termination, lock, corruption, format, duplicate identity and secret exclusion tests.
- [ ] Preserve existing manifests, read-only interfaces, Go 1.25 and optional driver tags.

## Out of scope

Multiple voters, hosted operations, subscriptions, raw check history, automatic unknown-action replay, infrastructure purchases and unverified release claims.

## Depends on

None

## Technical notes

The approved user plan is the requirements authority. Existing source artifacts and the separate documentation checkout must be preserved. File mapping may be refined within that scope during implementation.

## Definition of done

- [ ] Implementation and acceptance checks complete.
- [ ] Independent review complete.
- [ ] Relevant standard checks pass.
- [ ] Configuration, behavior and evidence documented.
