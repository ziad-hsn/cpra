---
title: Versions and availability
description: Choose the documentation for published main, the durable release candidate, or the unpublished Go SDK and approved management API plan.
cpra_scope: docs
---

# Versions and availability

Reviewed on **13 September 2026**. The application on `main`, the release
candidate, and the Go SDK candidate have different capabilities. Publishing
their documentation does not merge or release their code.

| Capability | Current main · `51a835a` | Release candidate · `410fbfb` | SDK and management plan |
| --- | --- | --- | --- |
| Checks, notifications, configured recovery | Implemented | Implemented | Management examples use fixtures |
| Incident state after restart | Resets with the process | Single-node Raft; interrupted started actions held as unknown | Encrypted canonical configuration is planned |
| Incident/action history | Not retained | 30-day retained events | Client contracts include observations |
| HTTP API | Read-only `/api/v1` | Read-only v1 plus state, history and SLO | Draft v2 and worker contracts; server unimplemented |
| CLI | Read-only inspection | Also readiness, local services and stopped backup/restore | Existing reads use a legacy SDK client; management commands remain planned |
| Worker sizing | Erlang C with variability adjustment | Model plus measured latency feedback | External worker protocol is a separate candidate |
| Packaging | Linux archives and source-built container | Multi-platform recipes, native services, DEB/RPM, Compose and Helm | Public SDK tags remain unpublished |
| Dashboard appearance | Neutral light, graphite dark, System preference | Earlier dashboard with durable views; palette parity needs integration | SDK work does not change the dashboard |

## Current main

Use the [main quickstart](tutorials/quickstart.md), [configuration](reference/config-schema.md),
[API](reference/api-reference.md), [CLI](reference/cli.md), and [FAQ](faq.md)
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

## Go SDK and approved management plan

The [SDK guide](sdk/index.md), [operations](sdk/api-reference.md),
[wire types](sdk/wire-types.md), [Go declarations](sdk/go-reference.md), and four
lessons describe the **uncommitted `codex/go-sdk` source snapshot** reviewed on
13 September. That local branch is based on `410fbfb`; the parent commit does not
identify its added SDK files. The public candidate branch does not contain them.
Neither `git checkout codex/go-sdk` nor `go get` is a public installation path
for this snapshot.

Commands under `scripts/sdk`, `sdk/go` and `examples/sdk` require the complete
local SDK source tree. Lesson source downloads here are reference excerpts,
not an independently runnable distribution. The [snapshot inventory](review/source-inventory.json)
records reviewed inputs by SHA-256. [SDK status](implementation/go-sdk-status.md)
and [verification](implementation/go-sdk-verification.md) retain the recorded
local evidence and remaining publication gates.

The [approved management plan](implementation/api-management-plan.md) covers
encrypted durable configuration, named authorization, complete collection
preflight followed by per-resource conditional activation, operator controls,
and optional pull-based workers. It is a plan. SDK methods and fixture tests do
not implement its server contract. Neither main nor `410fbfb` has a v2 management
server or external-worker dispatcher.

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
