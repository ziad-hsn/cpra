---
title: 'Plan and status · 07-scale-evidence: Validate million-monitor operation'
description: 'Plan and status · 07-scale-evidence: Validate million-monitor operation for the reviewed CPRa source; see the version and availability notice.'
cpra_scope: plan
---

> **Candidate design and evidence:** this record describes unreleased implementation work or an approved plan. Its checklists do not establish availability in `main`. [Version and availability](../versions.md) identifies the earlier 13 September reviewed snapshots; [current implementation status](STATUS.md) records the later branch checkpoints.

# 07-scale-evidence: Validate million-monitor operation

## Context

Implementation ticket from the approved Durable CPRa plan.

## What to implement

Compare published baseline `a370969b041b399c0778318d8915ce059fd74294` with the
frozen candidate on the current WSL machine at 10,000, 100,000 and 1,000,000
monitors, each at a 60-second cadence. Each build receives a five-minute warmup,
fifteen-minute measurement and three repetitions at each fleet size. Record
separate burst, slow-target, outage, recovery and process-crash scenarios.

The user's 1 October 2026 scope correction removes the 24-hour endurance run
from this task and its completion gates. Comparative measurements do not
establish endurance qualification.

## Where

scripts/benchmark/; evidence/

## Current harness verification

The 29 September local tooling change uses the recorded unstripped release recipe,
committed-source archives and an explicit module cache with workspace overrides
disabled. Preparation checks the physical-space prerequisite before allocating
archives or compiling. Campaign metadata now binds the target-server executable
to the candidate source as well as checking baseline and candidate binary digests.
A regression demonstrated that changing the target previously reached workload
execution; the new admission check rejects it before starting any run.

The HTTP harness uses separate authenticated CPRa and unauthenticated target
clients, private per-run token files, bounded responses, no redirect/proxy
inheritance, and bounded target startup. Incomplete trials stop the entire
campaign. Negative or non-finite fixture allowances cannot reduce the disk gate.

The [scoped report](../review/local-evidence.md#record-38b3f0bd29a3d9053bd519db)
records Python contract tests with actual loopback HTTP listeners, small Git
repositories, archive extraction and target-startup subprocesses. Compilation and
CPRa process orchestration are controlled fixtures; they establish no actual
candidate build, native CPRa execution, measured monitor capacity or soak result.
Independent review is pending. The normal CI benchmark-test discovery includes
these contracts and needs no provider credentials.

Remaining prerequisites include a clean frozen candidate, qualified private SDK
module archives/cache, checksum-verified compiler installation and adequate
physical host space. Actual matched compilation, comparative measurements and
fault scenarios remain open. On 1 October 2026 the user removed the 24-hour run
from the task and mandatory completion checks; no endurance claim is established.
See the [operator procedure](../validation.md#scale-campaign).

### Retained campaign verification

The 1 October preparation correction requires `--manifest RELEASE.json` and
records schema-3 build metadata. Candidate and target builds inject the official
version, commit and source date; the published baseline keeps its own default
identity fields. Frozen source bytes and modes are verified before compilation,
and the final private assessment matches the controller digest to its native
archive. Earlier schema-2 diagnostics cannot establish this release binding.
The reader checks retained comparisons and faults without requiring endurance
evidence. These tool changes do not establish a completed performance run.

The 30 September evidence reader verifies prepared source archives and binary
identities against all required retained campaigns. It streams the original
samples, recomputes threshold attainment and throughput, requires completed fault
and recovery intervals, and rejects shortened runs, missing samples, target
saturation, incomplete per-monitor reconciliation or changed inputs. It retains
the source/compiler and resource-stability review boundary; a consistent report
does not open publication. The runner now preserves prior campaign directories,
records bounded dashboard reads and reconciles intervention effects per monitor.

The [local verification record](../review/local-evidence.md#record-70864d2e243d442e512faed2)
contains report-reader and harness contract tests. Synthetic campaign records
exercise the complete expected matrix without representing elapsed execution.
Actual source preparation, native target execution, comparative measurements,
fault scenarios, final artifact binding and independent review remain
open. Use the [readback procedure](../validation.md#verify-retained-campaign-evidence)
after real campaigns complete.

## Acceptance criteria

- [ ] Physical host free-space gate >=30 GiB plus measured fixture headroom; target accounting, latency/resource trends, interference and interruption detection.
- [ ] All 18 matched comparison runs and five fault scenarios retain the selected source, artifact identities, fleet sizes, cadence and complete measurement windows.
- [ ] Healthy candidate windows sustain the required rate and five-minute p99 scheduling-plus-queue delay <=250 ms and scheduled-to-committed-result latency <=5 s, with misses, timeouts and coverage gaps counted explicitly.
- [ ] Accepted actions reconcile with observed outcomes; unintended duplicate interventions and unexplained losses remain zero, and queues drain after declared fault periods.
- [ ] No 24-hour run is required or started by this task; incomplete or failed comparisons retain their measured limits and cannot support a passing release claim.
- [ ] Preserve existing manifests, read-only interfaces, Go 1.25 and optional driver tags.

## Out of scope

Multiple voters, hosted operations, subscriptions, raw check history, automatic unknown-action replay, infrastructure purchases, a 24-hour endurance run and unverified release claims.

## Depends on

05-interfaces,06-live-providers

## Technical notes

The approved user plan is the requirements authority. Existing source artifacts and the separate documentation checkout must be preserved. File mapping may be refined within that scope during implementation.

## Definition of done

- [ ] Implementation and acceptance checks complete.
- [ ] Independent review complete.
- [ ] Relevant standard checks pass.
- [ ] Configuration, behavior and evidence documented.
