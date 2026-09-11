# 08-release: Publish evidence-backed release documentation

## Context

Implementation ticket from the approved Durable CPRa plan.

## What to implement

Document measured implemented behavior, backups, unknown outcomes, provider matrix and exact candidate evidence; publish through reviewed workflows.

## Where

README.md; deployment examples; .github/workflows/; dependency notices; separate gh-pages _sources/docs/

## Acceptance criteria

- [ ] Go 1.25/default/all-tags/race/vulnerability, frontend, packaging, restore and documentation checks; release evidence gates.
- [ ] Preserve existing manifests, read-only interfaces, Go 1.25 and optional driver tags.

## Out of scope

Multiple voters, hosted operations, subscriptions, raw check history, automatic unknown-action replay, infrastructure purchases and unverified release claims.

## Depends on

01 through 07

## Technical notes

The approved user plan is the requirements authority. Existing source artifacts and the separate documentation checkout must be preserved. File mapping may be refined within that scope during implementation.

## Definition of done

- [ ] Implementation and acceptance checks complete.
- [ ] Independent review complete.
- [ ] Relevant standard checks pass.
- [ ] Configuration, behavior and evidence documented.
