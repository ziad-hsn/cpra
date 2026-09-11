# 05-interfaces: Update operator interfaces

## Context

Implementation ticket from the approved Durable CPRa plan.

## What to implement

Read-only history/state/SLO API, CLI and dashboard, stable IDs and incremental fleet projection with bounded pages.

## Where

internal/web/; internal/client/; internal/cpractl/; dashboard/src/

## Acceptance criteria

- [ ] API authentication/contracts, CLI, frontend build/type/lint/tests and browser scenarios.
- [ ] Preserve existing manifests, read-only interfaces, Go 1.25 and optional driver tags.

## Out of scope

Multiple voters, hosted operations, subscriptions, raw check history, automatic unknown-action replay, infrastructure purchases and unverified release claims.

## Depends on

03-history,04-slo

## Technical notes

The approved user plan is the requirements authority. Existing source artifacts and the separate documentation checkout must be preserved. File mapping may be refined within that scope during implementation.

## Definition of done

- [ ] Implementation and acceptance checks complete.
- [ ] Independent review complete.
- [ ] Relevant standard checks pass.
- [ ] Configuration, behavior and evidence documented.
