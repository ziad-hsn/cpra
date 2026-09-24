---
title: 'Plan and status · 02-incidents: Integrate crash-safe incident processing'
description: 'Plan and status · 02-incidents: Integrate crash-safe incident processing for the reviewed CPRa source; see the version and availability notice.'
cpra_scope: plan
---

> **Candidate design and evidence:** this record describes unreleased implementation work or an approved plan. Its checklists do not establish availability in `main`. [Version and availability](../versions.md) identifies the earlier 13 September reviewed snapshots; [current implementation status](STATUS.md) records the later branch checkpoints.

# 02-incidents: Integrate crash-safe incident processing

## Context

Implementation ticket from the approved Durable CPRa plan.

## What to implement

Commit intent and started markers before external operations; restore state and hold unknown outcomes; reconcile target revisions.

## Where

internal/persistence/transitions.go; internal/controller/systems/durable_system.go; internal/controller/controller.go; internal/jobs/{execution,results}.go

## Acceptance criteria

- [ ] Real process crash boundaries, per-endpoint recovery, cooldown and incident lifecycle regressions.
- [ ] Preserve existing manifests, read-only interfaces, Go 1.25 and optional driver tags.

## Out of scope

Multiple voters, hosted operations, subscriptions, raw check history, automatic unknown-action replay, infrastructure purchases and unverified release claims.

## Depends on

01-storage

## Technical notes

The approved user plan is the requirements authority. Existing source artifacts and the separate documentation checkout must be preserved. File mapping may be refined within that scope during implementation.

## Definition of done

- [ ] Implementation and acceptance checks complete.
- [ ] Independent review complete.
- [ ] Relevant standard checks pass.
- [ ] Configuration, behavior and evidence documented.
