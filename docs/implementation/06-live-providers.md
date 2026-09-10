# 06-live-providers: Build configuration-driven live verification

## Context

Implementation ticket from the approved Durable CPRa plan.

## What to implement

Production driver paths for 33 configured scenarios with independent effect observations and redacted evidence.

## Where

cmd/cpra-verify/; internal/verification/; examples/verification/; scripts/verification/

## Acceptance criteria

- [ ] Missing configuration is not verified; real configured fixtures and provider evidence remain distinct.
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
