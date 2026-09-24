---
title: Plan and status · CPRa implementation status
description: Plan and status · CPRa implementation status for the reviewed CPRa source; see the version and availability notice.
cpra_scope: plan
---

> **Candidate design and evidence:** this record describes unreleased implementation work or an approved plan. Its checklists do not establish availability in `main`. [Version and availability](../versions.md) identifies the earlier 13 September reviewed snapshots; the current finalization record below preserves the later branch checkpoints.

# CPRa implementation status

## Current finalization plan

**Additional remaining work (2026-09-24):** add an A2A intervention job type and
an A2A notification type. The requested escalation example is a failed Docker
restart intervention notifying an agent to investigate. Protocol, authorization,
task lifecycle and integration design remain open; no A2A implementation is
claimed. See the [A2A backlog and acceptance scope](dashboard-shipping-plan.md#remaining-addition-a2a-agent-intervention-and-notifications).

The [2026-09-23 audit checkpoint](audit-findings-2026-09-23.md) records the
`internal/persistence` rename, security/runtime fixes, scoped verification, and
local controller leadership-recovery implementation. Current affected-package
integration, contextual cancellation, Go 1.25 and optional-driver race checks
passed with independent review. This checkpoint does not close the remaining
collection or shipping gates.

The [dashboard finalization and shipping plan](dashboard-shipping-plan.md) is the
execution roadmap as of 2026-09-14. It includes the missing management server,
SDK/CLI integration, writable dashboard, final distribution checks and provider/
endurance evidence. **Keep release candidates private until all agreed gates
pass.** No public prerelease, SDK tag, image or chart is an early exception.

Implementation has started on the private local branch
`codex/dashboard-finalization`, preserving the existing SDK and documentation
work. The [implementation progress record](dashboard-implementation-progress.md)
distinguishes implemented components, executed checks and incomplete integration.
Normal application startup now integrates the encrypted management catalog,
named authorization and owner reconciliation. Resource editing, controls,
audited reviews and operation reads have local SDK/browser evidence. Collection
preparation, encrypted inactive upload, cancellation and browser file preview now
have scoped SDK, restart and native-browser evidence. Public asynchronous
collection validation and bounded original-result reads are implemented and now
pass a connected normal-main Chrome/Worker/WASM/TLS/Raft campaign, including SDK
reconciliation of the same sealed result after restart. Collection listing,
owner-aware detail and explicit retained-result browsing are now implemented and
have connected browser/restart evidence, passing scoped backend checks and
independent review. [Public activation and connected Apply](collection-public-activation.md)
now connect the original validated operation to SDK/CLI/dashboard execution and
result waiting. Refreshed-browser original-input recovery and explicit operation
continuation are now connected. Restarted CLI recovery now uses the shared
`cpra.file.base.v1` profile and SDK `Reselect` helper, with native process-kill,
lost-reply and original-ciphertext evidence. Bounded upload batching fixes the
reproduced 256-row request timeout while preserving synchronous writes and
per-resource admission. See the [current recovery checkpoint](../../bin/verification/collection-cli-reselection-2026-09-24/result.json)
and [runtime contract](collection-reselection-runtime.md#cli-and-sdk-continuation).
The original full management race run exceeded its ten-minute timeout in
`TestStagedValidationLargeNotificationClosureUsesLookups`. Bounded authenticated
dependency metadata now avoids repeated full-resource decoding during graph
lookups. The unchanged full suite subsequently passed in 557.230 seconds under
the original timeout; its 355 selected repository inputs remained unchanged.
The original failure, focused profiles, independent review and final run are
retained in the [performance checkpoint](../../bin/verification/staged-validation-performance-2026-09-24/comparison.json).
This is validation-cost and correctness evidence, not fleet-capacity evidence.
The optional [JobType implementation](job-types.md) now provides tagged encrypted
immutable versions, bounded schema validation, explicit runtime opt-in and five
authenticated HTTP operations. Mutations retain authority-bound operation handles
and terminal configuration receipts; paginated reads retain bounded encrypted
generations. [Real SDK/TLS/Raft and native-restart checks](../../bin/verification/job-type-http-2026-09-24/result.json)
cover these boundaries.
Scoped committed worker grants, stopped local provisioning and a separate tagged
authentication adapter are now implemented. Credential rotation preserves worker
identity; restore requires explicit reprovisioning; normal builds omit these
interfaces. See [worker administration](../worker-authentication.md) and the
[authorization checkpoint](../../bin/verification/worker-policy-2026-09-24/result.json).
Authenticated immutable JobType references now extend encrypted monitor/endpoint
records, with atomic referenced-delete protection and tagged format-18 checks.
See the [reference contract](job-types.md#configuration-references) and
[scoped verification](../../bin/verification/job-type-references-2026-09-24/result.json).
Tagged format-19 session ownership and exact empty-response replay are now
implemented. The private SDK and journal format 2 bind worker/server/session
identities and distinguish Start from reconciliation. The session HTTP adapter
is tested through a dedicated mux; normal startup still exposes no worker routes.
The [session checkpoint](../../bin/verification/worker-sessions-2026-09-24/result.json)
records exact local test scopes, source identities and the incomplete broader
five-minute persistence run. It is not full-package or release qualification.
Tagged format 20 now adds [encrypted queued execution admission](worker-execution-admission.md),
original endpoint pins, inert runtime bindings and original-contract result
preparation. The [admission checkpoint](../../bin/verification/worker-admission-2026-09-24/result.json)
qualifies these private boundaries. It does not supply Start grants, result
receipts, expiry/noData finalization or active controller dispatch.
The later [format-21 offers and Start checkpoint](worker-offers-and-start.md)
adds guarded allocation, one executable grant, non-executing reconciliation,
protected parameter projection and retained-ciphertext startup verification.
Public external configuration activation, result integration
and full shipping qualification remain unfinished. See the
[external-worker integration boundary](external-worker-server-next-steps.md).
Remeasure physical
C: free space before the large campaign;
the required gate is 30 GiB plus measured fixture headroom, regardless of WSL's
reported virtual capacity.

The private collection plan compiler now records original per-item guards and
dependency outcomes without preparing mutations. Catalog storage format 4 adds
unique reverse-dependent mutation tokens while preserving format-2/3 replay.
These prerequisites have scoped tests; neither registers public Validate/Activate
or makes an uploaded collection executable. See the
[activation boundary](collection-activation.md) and
[format compatibility contract](catalog-mutation-format.md).

The next private prerequisite now retains the original plan descriptor and
bounded fragments with encrypted input in storage format 5. Conditional
finalization, original-input binding, plan-first terminal cleanup and process
restart checks are described in the [staging contract](collection-plan-staging.md).
This internal artifact state is not a public validation verdict or activation
grant. Storage format 6 now retains separate immutable validation results and
enforces permanent named-operator ownership. Finalized results now publish in
bounded pages to 30-day history before temporary staging is reclaimed; scoped
process-kill tests cover partial publication, the final seal and cleanup/restart.
See the [validation result contract](collection-validation-staging.md). Public
validation admission and result endpoints consume those original artifacts;
subsequent checkpoints added conditional execution and public activation.

Storage format 7 now retains one immutable validation request, an exclusive claim
and an audited interruption receipt. A private single-attempt runner binds the
original capability profile, checks current authority before protected reads and
artifact writes, and commits frozen success/rejection results without activation.
Claimed input access ends when either artifact begins. Scoped race, minimum-Go,
tagged, Raft restart and independent-review evidence is recorded in the progress
log. See the [coordinator boundary](collection-validation-coordinator.md).
Normal main now owns one bounded compiler worker, includes it in readiness, and
joins it before closing storage/encryption. Once-per-Store startup retires abandoned
claims without recompiling them; never-claimed requests can begin only after their
original authority/profile checks. Selector, blocked-shutdown, real-process restart
and normal-main tests have scoped evidence in the progress log. Public Validate
now returns a 202 operation; bounded GET validation exposes the original sealed
verdict. The connected release-Go race campaign passed in 11.608 seconds with
exactly five explicit browser writes, no activation, zero target requests and no
active catalog changes; original operation/result/digest survived restart. Its
recorded embedded index is `df03963247b61545e917ebbea80e1bc53d9c46eab32b91e228505069918bc578`.
The same scoped campaign passed Go 1.25 in 10.073 seconds. Parser reproduction
and byte equality between current Vite output and embedded assets also passed.
Evidence is in `bin/verification/collection-validation-browser/result.json`.
Conditional activation and complete collection lifecycle acceptance remain open.
The [collection operation-listing contract](collection-operation-listing.md)
now describes the implementation. Its unified snapshot preserves shared ordinary
receipts and original-owner collection observations with bounded work and no
input decryption. A separate explicit dashboard read retrieves the original
sealed validation result after the local draft is discarded. The new normal-main
Chrome/TLS/Raft campaign passed release-Go race in 12.896 s and Go 1.25 in
11.845 s; list/detail/SDK observations agree across restart, with exactly five
explicit import writes, zero additional browsing writes and zero provider calls.
Its embedded index is
`b4f2223ac4a876a91e65bc3c1369cc6023c63272604e24e0bb1515bc8573129a`.
Evidence: `bin/verification/collection-operation-browser/result.json`. This
supersedes the earlier embedded-asset evidence for this operation-browsing scope.
Earlier durable runs do
not substitute for a complete current shipping matrix; no public release is claimed.

The [private format-8 activation admission](collection-activation-admission.md)
now preserves one original successful plan/result binding and checks current
operator authority separately from that immutable identity. Applying parents
remain live after the old staging deadline; cancellation and explicit restore
retain and fence the original admission. Scoped ordinary, race, Go 1.25, tagged,
snapshot and real-process termination tests pass, with independent source review.
That admission matrix passes, including the complete durable and localadmin race
suites, affected management/server checks, minimum Go, tagged builds, vet and
release-policy tests. The preserved first-run cleanup-fixture failure and its
reviewed correction are described in the progress record. This creates no catalog
mutation, child operation or provider call.

The [private format-9 execution path](collection-execution-integration.md) now
registers isolated conditional-item commands, complete execution snapshots and
recovery of certified child outcomes. First ordinary tests include actual Raft
process kills and stopped replay validation. The private command/snapshot matrix
and full durable race suite pass (449.031 s), with exact source manifests. A
private management runner and cached per-item preparation reads now have their
own qualification in progress. Validation-time encoded-resource headroom and
policy version 2 have passed their scoped matrix. At that format-9 checkpoint,
application lifecycle wiring, retained application-result publication/cleanup,
controller-connected collection acceptance and public API/SDK/dashboard
activation remained unfinished. The later checkpoints below record subsequent
implementation. The current public integration is recorded separately above;
shipping qualification remains incomplete.

The [private format-10 finalization boundary](collection-execution-finalization.md)
now freezes original execution decisions, child dispositions and a result summary.
It distinguishes committed catalog changes from applied children, preserves
cancellation/restore receipts, and emits one immutable history anchor. Exact
retries preserve the original finalization time. Certification reads only the
selected parent's original artifacts and execution namespace outside the FSM
mutex, with one bounded audit index. Admitted input remains retained before begin
and after finalization; historical cleanup entries retain their original replay
behavior. A valid unstarted admission exceeding the audit bound remains healthy,
retained and unfinalized with a quota response.

The final focused format-10 race campaign passed in **35.214 s**, including native
log/snapshot reopen, stopped-store inspection, actual explicit restore with a
pending child, fresh authentication, corruption rejection, historical format-9
and digest compatibility, and the oversized-admission quota case. Independent
source review and a separate execution-result run passed; the reviewer also
checked the final quota correction. The release recipe/reader-format and build
configuration tests passed. These are scoped results: no process-kill campaign
specifically at the new finalization/history boundary has run. The subsequent
[format-11 result checkpoint](collection-execution-publication.md) implements
coordinator-driven finalization, joined publication/seals and protected Store
result reads, with additive generated SDK and browser observation contracts.
The subsequent [result interface checkpoint](collection-execution-interfaces.md)
adds authoritative operation counts, protected HTTP pagination, CLI result reads
and a paged dashboard result view. The real embedded-dashboard/TLS/Raft scenario
passes with Go 1.27 race detection and Go 1.25; restart preserves the original
result. The subsequent
[private retirement contract](collection-execution-retirement.md)
registers format-12 paired execution deletion and format-13 source cleanup, with
committed prefix digests for partial-source snapshots and recovery. Maintenance
deletes validation, plan and input records before the header. Protected points,
pages and operation lists retain original owner/result authority after removal;
history cannot grant execution. Scoped format-13 checks cover native process
termination, retained listing, failure handling and bounded warm verification;
the [evidence record](../../bin/verification/collection-source-retirement-2026-09-24/result.json)
preserves exact commands and limits. The subsequent
[public activation checkpoint](collection-public-activation.md) connects
SDK/CLI/dashboard Apply; cross-process reselection remains open. Its
[normalization prerequisites](collection-browser-reselection.md) now include a
shared versioned base-file parser, fixed compiled-artifact fixtures and
format-14 protected profile/prefix checks. The subsequent
[encrypted source and proof components](collection-reselection-proof.md) verify
original raw input, normalized inventory and uploaded ciphertext without
changing durable progress. Scoped native restart, stopped-policy replacement,
minimum-Go and race checks pass; HTTP attempt routing and suffix transfer remain
open. No public resume capability is advertised by these prerequisites. No release publication is authorized by
these private changes. See the
[remaining result lifecycle work](collection-execution-results-next-steps.md).

[Controller leadership recovery](controller-leadership-recovery.md) is implemented
locally. The initialized owner retains unresolved batches and reconciles their
original identities across transient Raft leadership changes, including projection
and executor finalization. Readiness remains unavailable during recovery; startup
failure and permanent storage/restore/migration failures remain fail-closed.
Focused controller races and actual Raft step-down/re-election tests passed,
including retained in-flight pulse work and startup on a follower. That evidence
precedes the latest context-aware persistence integration. The final root suite,
minimum-Go checks and integrated matrix for that source are pending; local
implementation and earlier passing tests do not establish shipping readiness.

## Historical durability candidate

The baseline and evidence below describe the earlier durability work. They do
not qualify the current integrated candidate; old disk readings are historical.

Base: `a370969b041b399c0778318d8915ce059fd74294`; branch: `codex/durable-cpra`.
The 13 September documentation review also identified release candidate
`410fbfb0092d01277b3884cd04151c27443a4226` on `codex/release-engineering`.
That reviewed snapshot predates the current finalization checkpoints above.

The eight adjacent tickets follow the approved dependency order. This is an
implementation candidate. The complete release gate is **open**.

| Ticket | Implemented | Evidence / outstanding work |
| --- | --- | --- |
| 1 Storage | Default Raft, versioned state, identity, locking, snapshots, explicit memory mode | Restart, forced-kill, corruption, format, snapshot, backup and race checks; Go 1.25 compatibility. |
| 2 Lifecycle | Deterministic commits, per-endpoint intent/start/results, unknown holds, configuration reconciliation | Real subprocess crash boundaries and real controller restart regression checks. |
| 3 History | Daily indexed bbolt segments, 30-day retention, compaction watermark, bounded cursor pages | Replay, retention, concurrent insert pagination, missing-segment and complete backup checks. |
| 4 SLO | Bounded driver histograms, exact counters, persisted coverage, cadence obligations, model plus feedback | Distribution, unfinished/missed work, timeout, restart and control-response tests. Full workload targets remain unverified. |
| 5 Interfaces | Read-only state/history/SLO API and CLI; incremental fleet index; dashboard timeline/status | Auth/route/page tests, real browser and API/CLI agreement; 15 dashboard tests/build/lint/type checks. |
| 6 Providers | 33-case config runner, effect/receipt observers, disposable fixture setup, evidence files, manual workflow | Six local driver scenarios passed. Remaining 27 have no passing live evidence. Actual database images, privileged systemd, designated Kubernetes/cloud accounts and receipt readers remain prerequisites. |
| 7 Scale | Pinned matched-build preparation, physical-disk gate, 10k/100k/1m comparison, fault and 24-hour harness | Ten-monitor harness smoke only. The earlier campaign stopped at its physical-space preflight. That historical free-space reading is not current; rerun preflight for at least 30 GiB plus fixture headroom before any large campaign. No completed full campaign is established here. |
| 8 Release | Persistent deployments, backup docs, workflow entry points, dependency notices and documentation candidate | Local packaging and docs-link checks. Independent review, completed provider/performance/endurance evidence and application publication to main remain outstanding. Candidate documentation is now included in the public documentation; that does not qualify the application. |

No ticket's independent-review checkbox is marked complete. The implementation
was checked and revised by its author; that is not an independent review.

Local evidence is under `evidence/local/` and excluded from source archives and
Git. It includes failed fixture attempts as well as successful runs. Component
checks and short harness runs cannot satisfy the million-monitor or full-provider
gates. No subscriptions, infrastructure purchases, user disk cleanup or unrelated
service changes are part of this work.

## Approved management API work

The [management API, batch configuration, and external-worker plan](api-management-plan.md)
records the approved API, SDK, CLI, and dashboard management scope. Its scope
statement was corrected on 2026-09-13: API-first is dependency order, not permission
to omit the writable dashboard. Its ten server/CLI foundation tickets include
encrypted canonical configuration, multi-file preflight/resumable apply,
acknowledge/dismiss/snooze/disable, Ark projection, and the optional externaljobs
protocol. These are planned capabilities; this link does not mark their
implementation or the separate release gates complete.

The [dashboard management plan](dashboard-management-plan.md) records guided
management forms, file import, notification recipients and groups, encrypted
write-only credentials, controls, and diagnostics. These shared resources also
require API, SDK, and CLI support. Generated contracts, guarded resource
preparation, encrypted catalog storage, named authorization, browser sign-in and
shared-resource screens are implemented locally. Normal startup, owner-loop
reconciliation, single-resource operation reads, controls and selected browser
flows have executed qualification. Resumable collections and final integrated
release gates remain open; neither source presence nor SDK fixtures completes
management delivery.
No check-now operation is planned.

Original-file upload recovery is now connected through normal startup, the API,
SDK and dashboard. Its [runtime contract](collection-reselection-runtime.md)
describes encrypted disposable attempts, exact original-input verification and
conditional suffix transfer. Actual Chrome checks cover refresh/token re-entry
and a lost resume reply without duplicate mutation. The operation page also
provides original validation, retained-result review, explicit activation and
confirmed cancellation. The normal-main Chrome continuation check passes with
Go 1.27 race detection and Go 1.25 and distinguishes upload recovery from the later
explicit activation. Restarted CLI helpers and the remaining dashboard/shipping gates are
tracked separately.

## Go SDK work

The [Go SDK implementation and qualification status](go-sdk-status.md) records the
independent management/worker modules, source verification commands, and the
remaining server prerequisites. SDK contract tests must not be reported as
completion of the planned v2 management server or external-worker dispatcher.
