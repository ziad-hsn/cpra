# 07-scale-evidence: Validate million-monitor operation

## Context

Implementation ticket from the approved Durable CPRa plan.

## What to implement

Matched baseline a370969b041b399c0778318d8915ce059fd74294 comparisons and 24 uninterrupted hours at one million monitors every 60 seconds on current WSL.

## Where

scripts/benchmark/; evidence/

## Acceptance criteria

- [ ] Physical host free-space gate >=30 GB; target accounting, latency/resource trends, interference and interruption detection.
- [ ] Preserve existing manifests, read-only interfaces, Go 1.25 and optional driver tags.

## Out of scope

Multiple voters, hosted operations, subscriptions, raw check history, automatic unknown-action replay, infrastructure purchases and unverified release claims.

## Depends on

05-interfaces,06-live-providers

## Technical notes

The approved user plan is the requirements authority. Existing source artifacts and the separate documentation checkout must be preserved. File mapping may be refined within that scope during implementation.

## Definition of done

- [ ] Implementation and acceptance checks complete.
- [ ] Independent review complete.
- [ ] Relevant standard checks pass.
- [ ] Configuration, behavior and evidence documented.
