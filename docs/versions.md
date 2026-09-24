---
title: Versions and availability
description: Distinguish current management development from historical source guides and release qualification.
cpra_scope: docs
---

# Versions and availability

## Current development source

Updated **24 September 2026**. The `codex/dashboard-finalization` checkpoint
[`4c6baf5`](https://github.com/ziad-hsn/cpra/commit/4c6baf58df8bf7fd4e02e0399fbe092ba867b20f)
includes persistence, encrypted management CRUD, operator controls, dashboard
forms, and SDK/CLI collection validation, Apply and upload recovery. The merge
with `fc4edb4` brings in the shared light/dark theme and documentation sources.
Current [API](reference/api-reference.md), [CLI](reference/cli.md),
[driver](reference/jobs-reference.md) and [SDK](sdk/index.md) references describe
this development source.

The unreleased SDK compatibility client has been removed. External-worker offers
and Start have scoped tests, while normal-startup worker routes, controller
dispatch, result processing and full interoperability remain incomplete.
[Implementation progress](implementation/dashboard-implementation-progress.md)
records the precise boundary. [Release gates](implementation/dashboard-shipping-plan.md)
remain open; a pushed development branch is not a qualified release.

## Historical documentation review: 13 September 2026

The comparison below describes those exact older revisions, not the current
development checkout. Pinned historical tutorials and candidate guides remain
available so their evidence is not confused with current implementation.

| Capability | Historical main · `51a835a` | Earlier candidate · `410fbfb` | SDK and plan at that review |
| --- | --- | --- | --- |
| Checks, notifications, configured recovery | Implemented | Implemented | Management examples use fixtures |
| Incident state after restart | Resets with the process | Single-node Raft; interrupted started actions held as unknown | Encrypted canonical configuration is planned |
| Incident/action history | Not retained | 30-day retained events | Client contracts include observations |
| HTTP API | Read-only `/api/v1` | Read-only v1 plus state, history and SLO | Draft v2 and worker contracts; server unimplemented |
| CLI | Read-only inspection | Also readiness, local services and stopped backup/restore | Existing reads use a legacy SDK client; management commands remain planned |
| Worker sizing | Erlang C with variability adjustment | Model plus measured latency feedback | External worker protocol is a separate candidate |
| Packaging | Linux archives and source-built container | Multi-platform recipes, native services, DEB/RPM, Compose and Helm | Public SDK tags remain unpublished |
| Dashboard appearance | Neutral light, graphite dark, System preference | Earlier dashboard with durable views; palette parity needs integration | SDK work does not change the dashboard |

## Historical main: `51a835a`

Use the historical [quickstart](tutorials/quickstart.md),
[configuration](reference/config-schema.md), and [FAQ](faq.md)
for [`51a835a29f2fb7af2e0301910042a52e308cbb24`](https://github.com/ziad-hsn/cpra/commit/51a835a29f2fb7af2e0301910042a52e308cbb24).
That commit includes the September branding and theme changes. Documentation
can advance on main without changing this runtime revision.

The root module at this revision is named `cpra`; build it from a checkout with
`make`. Canonical-path `go install`, local service commands, `-runtime-config`,
`-data-dir`, `-validate`, and durable APIs belong to the candidate below.

## Durable release candidate

The [candidate guides](candidate/index.md) describe
[`410fbfb0092d01277b3884cd04151c27443a4226`](https://github.com/ziad-hsn/cpra/commit/410fbfb0092d01277b3884cd04151c27443a4226)
on `codex/release-engineering`. Its history contains the durability,
provider-fixture, native-operation and release-packaging changes.

The [candidate quickstart](candidate/tutorials/quickstart.md) checks out that exact
revision. It builds independently of unpublished SDK work. Distribution guides
describe implemented recipes; their `VERSION` examples require a verified
release. No application or SDK version tags were present in the remote repository
when this review checked it.

Provider-account qualification, native platform and publication gates, matched
capacity comparisons and the million-monitor 24-hour campaign remain separate
from documentation verification. See [candidate changes](candidate/release-notes.md)
and [verification requirements](candidate/validation.md).

## Historical SDK and management review

The 13 September review covered an **uncommitted `codex/go-sdk` source snapshot**
based on `410fbfb`; the parent commit did not identify the added SDK files. The
source inventory below preserves that historical review. Current SDK guides and
generated references have since advanced with the development branch described
above. Public versioned module installation remains a separate release gate.

Commands under `scripts/sdk`, `sdk/go` and `examples/sdk` require the complete
local SDK source tree. Lesson source downloads here are reference excerpts,
not an independently runnable distribution. The [snapshot inventory](review/source-inventory.json)
records reviewed inputs by SHA-256. [SDK status](implementation/go-sdk-status.md)
and [verification](implementation/go-sdk-verification.md) retain the recorded
local evidence and remaining publication gates.

The [approved management plan](implementation/api-management-plan.md) covers
encrypted durable configuration, named authorization, complete collection
preflight followed by per-resource conditional activation, operator controls,
and optional pull-based workers. SDK methods and fixture tests alone do not
establish its server contract. The historical `51a835a` and `410fbfb` revisions
have no v2 management server or external-worker dispatcher; consult current
implementation progress for the later implemented management paths.

## Evidence and maintenance

Historical reports identify what their authors ran at that time. Local protocol
tests, mock contracts, provider sandboxes and actual account effects are separate
evidence classes. Cross-compilation is distinct from native execution; a smoke
test is distinct from an endurance campaign.

The [documentation review](review/latest-changes.md) records the sources and
corrections behind this refresh. Pages are canonical in `docs/` on main and
synchronized to `_sources/docs/` on gh-pages. Use the
[maintenance procedure](maintaining-docs.md) to update both branches and rebuild
the generated site together.
