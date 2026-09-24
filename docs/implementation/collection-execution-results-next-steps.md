# Collection execution results: terminalization, publication and cleanup

Status: **private finalization, coordinator integration, joined publication,
authoritative operation projection, protected HTTP pagination and SDK/CLI/dashboard
result reads are implemented. Format 12 adds registered private execution-prefix
retirement. Format 13 adds source retirement, partial-source snapshot/recovery,
automatic maintenance and retained operation listing after header removal.
Scoped qualification and bounded warm-audit verification are implemented. Public
Activate and connected Apply are now implemented in the private candidate; their
[integration record](collection-public-activation.md) distinguishes current
verification from remaining cross-process reselection and shipping gates.** The private `retire_sources`
command deletes validation, plan and encrypted input after execution retirement,
then removes the header. Original results remain in retained history.

See the [implemented publication boundary](collection-execution-publication.md)
and the [interface implementation record](collection-execution-interfaces.md).
The [retirement contract](collection-execution-retirement.md) describes
the current retirement and retained-read checkpoint. The earlier
[interface verification record](../../bin/verification/collection-execution-interfaces-2026-09-23/result.json)
keeps its original scope. The sections below preserve the full
agreed contract; implemented result reads do not enable the remaining mutation
or cleanup paths.
Candidates remain private. This note supplements the
[activation contract](collection-activation.md) and the
[format-9 item-execution boundary](collection-execution-integration.md). It does
not change the approved path-only `POST /api/v2/operations/{id}/activate` request.

## Foundations that can be reused

| Existing source | Implemented contract | Remaining extension |
| --- | --- | --- |
| [Execution records](../../internal/persistence/collection_execution_records.go), [progress](../../internal/persistence/collection_execution_progress.go), [publication](../../internal/persistence/collection_execution_publication.go) | Immutable original conditional decisions and child terminals; summary/anchor in format 10, joined items/seal in format 11, execution retirement in format 12, source retirement in format 13 | Qualify the connected lifecycle before public activation |
| [Child commit hooks](../../internal/persistence/collection_child_commit.go), [paired retirement](../../internal/persistence/collection_execution_ledger_retire.go) | Persist terminal disposition before removing a pending child; reserve 4,096 bytes per accepted child and refund it only with paired outcome/terminal retirement | Connected lifecycle qualification |
| [Execution ledger](../../internal/persistence/collection_execution_ledger.go), [snapshot audit](../../internal/persistence/collection_execution_snapshot.go) | Atomic prepared-slot consumption, bounded warm execution/source deletion and verified partial-prefix recovery | Maximum-size cold-audit latency and connected Apply qualification |
| [Execution history reader](../../internal/persistence/collection_execution_publication_read.go), [retained lookup](../../internal/persistence/collection_execution_retained_read.go) | Original anchor/seal owner authority, exact identities, one retention cohort and primary/index validation; points/pages/lists survive header removal | Connected Apply acceptance |
| [Protected result view](../../internal/persistence/collection_execution_result_view.go), [HTTP result reads](../../internal/httpserver/management_collection_execution.go) | Owner check before rows; immutable watermark; bounded detail cursors; fresh authorization, epoch/high-water, health and retention checks after I/O and response-admission waits | Qualify connected Apply alongside reads |
| [Cancellation](../../internal/persistence/collection_cancel.go), [cleanup](../../internal/persistence/collection_cleanup.go), [retirement checkpoint](../../internal/persistence/collection_execution_retirement_checkpoint.go) | Separate decision-stop/result-finalization milestones and automatic retirement; distinct source-prefix commitments before header removal | Public activation and connected application |

The collection projection in
[collectionOperationView](../../internal/management/collection_admission.go)
now reports authoritative `committed = accepted` and `applied = childApplied`
counts. Protected point/list observations retain original identity and parent
state, including canceled parents whose earlier cancellation receipt expired.
Point reads and result pages also recover original authority after header removal;
retained operation listing after that removal has scoped qualification.
They expose live counts before finalization and an immutable public summary only
after a verified seal supplies its descriptor. An admission-only receipt without
execution observations omits unavailable counters instead of inventing zeroes.

## Preserve three different facts

1. **Catalog decision:** `accepted`, `unchanged`, `conflict`, or
   `dependencyBlocked`, by original plan ordinal. A successful conditional
   catalog decision remains successful even if its controller child later fails.
2. **Controller disposition:** pending until the original accepted child's
   retained terminal record says `completed/applied`,
   `failed/projection_failed`, or `partial/superseded`. Explicit restore retains
   its marker separately. No child is invented for unchanged or rejected rows.
3. **Parent outcome and result availability:** cancellation can stop decisions
   immediately while accepted children remain pending. Result publication and
   availability therefore cannot be inferred from a terminal-looking parent
   state alone.

`Operation.state` and `ApplyResult.outcome` are currently unrestricted strings in
the [authoritative schema](../../api/openapi/models.base.json), not schema enums.
The [SDK result wait helper](../../sdk/go/operation_execution.go) and
[dashboard decoder](../../dashboard/src/api/collectionOperations.ts) nevertheless
recognize explicit sets. Extend the schema documentation, generated models and
both consumers together; do not infer compatibility from Go's string type.

Implemented parent projection, once all accepted children have terminal evidence:

| State | Exact meaning |
| --- | --- |
| `completed` | Every input was accepted or conditionally unchanged, and every accepted child was applied |
| `partial` | At least one input was accepted or unchanged, but some catalog decision or controller disposition prevented complete success; committed changes remain committed |
| `failed` | No input was accepted or conditionally unchanged; all attempted decisions were conflicts or dependency failures |
| `canceled` | The original cancellation stopped remaining decisions; later result finalization does not replace its ID, time or parent outcome |
| `invalidated` | Explicit restore fenced the original epoch; retained facts cannot restart that parent |
| `applying` | More original decisions remain, or accepted children have not all settled; a transport timeout or unavailable provider does not create a terminal parent outcome |

Thus all accepted children failing projection yields `partial`, with nonzero
committed count and zero applied count. It must not appear to have rolled back.
An all-unchanged collection is `completed`, with zero newly committed mutations
and zero newly applied children, plus its explicit unchanged count.

There is no `unknown` conditional decision or configuration-child terminal in
the current durable model. A lost HTTP/commit reply is an unconfirmed observation
to reconcile by original identity. A pending child is pending; missing expected
records are unavailable/corrupt, not unknown, failed or unattempted. Unknown
external interventions remain in their existing action-review domain. Unknown
future wire values may be displayed read-only without being rewritten into a
known current outcome.

The private coordinator already exposes bounded scalar
[status observations](../../internal/management/collection_execution_coordinator.go):
for example `capabilitiesChanged`, `authorityUnavailable`, `clockBeforeProgress`,
`backpressure`, `commitUnconfirmed`, `sequenceExhausted` and `awaitingCompletion`.
They are ephemeral execution/admission observations and are not yet wired to the
public API. Surface them through the existing condition-style discovery and
observation contract when integrating the API; keep them separate from durable
catalog decisions, child dispositions and the retained result seal. In
particular, `awaitingCompletion` does not certify parent completion or result
availability. An applying operation should explain why work is waiting without
persisting runtime error strings or inventing a terminal result.

## Small immutable terminalization boundary

The boundary in this section is now implemented by the private format-10
`CollectionExecuteCommand` action `finalize`, with an immutable summary and
anchor. Format 11 introduced publication/seals; format 12 now adds bounded paired
execution retirement. See
[implementation and executed verification scope](collection-execution-finalization.md).
The requirements below describe the implemented terminalization contract and
constraints that later cleanup integration must preserve.

The bounded `CollectionExecutionResultState` holds an immutable summary and
separate publication progress. `CollectionExecutionRetirementState` now carries
the exact result fence and retired execution-prefix checkpoint separately. The
immutable summary binds:

- Original operation/upload/activation/plan identities, plan digest, original
  public inventory identity and item count, permanent actor and original epoch.
- Processed count, accepted/unchanged/conflict/dependency-blocked counts, all child
  disposition counts, original outcome digest and final child-terminal root.
- Any abandoned prepared identity/digest, original cancellation or restore
  disposition, and one `FinalizedAt` observation for the result's retention.
- A versioned result-projection contract. Use the original activation ID as the
  stable result identity within that contract; do not generate an ID on replay.

The isolated internal `CollectionExecuteCommand` action `finalize` carries the original
binding, an exact expected execution-progress fence and observation time. It
derives the summary from committed facts; it accepts neither arbitrary item
results nor refreshed guards. An applying parent qualifies only when
`Processed == ItemCount`, no prepared slot remains, and
`Accepted == ChildTerminals`. A canceled or restore-invalidated parent qualifies
after `Accepted == ChildTerminals`; its untouched suffix is explicitly
unattempted. An existing prepared candidate in that suffix was never accepted
and has no child. Keep its identity until publication/cleanup records that fact.

An activated parent canceled before `begin` receives a zero-decision result
without fabricating an execution command. Unactivated canceled uploads
continue to use their existing no-application-result behavior.

Finalization compares the complete progress/stop fence under FSM ownership and
requires `at >= Activation.At`, `at >= Execution.LastAt` when present, and
`at >=` the original stop observation. A stale proposal conflicts; an exact retry
returns the original summary and time without another event. It never changes
catalog resources, children, original item decisions or staging `ActivityAt`.
Counters must be backed by the existing certified namespace/cache invariants;
matching counts alone are insufficient. Any needed cold verification follows
the existing isolated-command prepass and closes its frozen reader before writes.

Finalization and copying settled facts require store health and original binding,
not a still-valid execution credential. Revocation must stop new catalog writes
without preventing already-known completion evidence from being retained.
Do not terminalize paused work merely because its current owner is revoked or
the compiled capability profile changed. Public reads still require current
authentication, owner identity and permissions.

## Retained anchor, bounded rows and final seal

The separate `collection-execution/<operationID>` history family has three
allowlisted event shapes: immutable summary anchor, original-input-order item,
and final publication seal. All use `Event.At == FinalizedAt`, while
retaining original decision, cancellation and child timestamps inside their
typed fields. Event IDs continue to come from the enclosing log index/ordinal.
The finalization command emits the compact anchor through the existing history
barrier before acknowledgement or snapshot/log compaction.

The anchor is necessary because cancellation receipts are immutable and dated at
cancellation. A child may settle more than 30 days later. Its final execution
results must have a new, complete 30-day cohort, independently of the expired
cancellation receipt; never patch that old receipt or date each copied item by
its publication wall clock. The result index needs owner/binding/anchor lookup
before item access. Operation point/list projection uses this retained companion
with the original header when the earlier parent receipt has expired. Point reads
and result pages additionally use the original anchor/seal when the header is
absent, without duplicating the parent ID or changing its canceled/invalidated
outcome. Listing after header removal has scoped qualification. Old-epoch API handles
remain fenced after explicit restore.

A subsequent `CollectionExecuteCommand` action `publish` carries only the
original binding and expected published-item ordinal. It derives at most 256
rows and 4 MiB from
the original input, plan, outcome and terminal namespaces. Original input order
is public order; plan ordinal remains a separate field. Reuse the bounded
verified key-to-plan-ordinal index and selected-row checks rather than retaining
a fleet-sized plaintext result map. Validate the canonical accepted-row hash and
terminal binding/proof before joining a child disposition. A row beyond the
processed prefix is unattempted only when the immutable stop summary proves it;
a missing row inside that prefix is corruption.

Each joined item is metadata-only: kind/resource ID, input and plan ordinals,
opaque source token/document/item, original old/new versions and incarnation,
catalog decision/index/time, optional distinct child handle and child
state/outcome/time/restore marker. No resource body, ciphertext, provider error
text, source path, URL, key or source fingerprint is publishable. The canonical
per-item bound is 16 KiB; an oversized first row fails without returning an empty
advancing page.

Publication progress accumulates canonical item count/bytes/digest. The final
page emits a seal binding that descriptor to the original anchor and marks
`HistorySealed` in the parent. Treat pages before the seal as provisional.
Exact command retries emit no duplicate pages; exact history replay accepts only
identical primary/index bytes. A history write failure blocks acknowledgement
and safe cleanup. Missing expected unexpired anchor, items, indexes or seal is
unavailable, not a new result or expired result. The existing limitation remains:
coherent deletion of all authoritative history evidence cannot be diagnosed from
absence alone; this is not tamper-proof storage.

## Implemented result reads and retention

The approved operation-detail endpoint now dispatches protected collection
execution-result pagination through the
[execution read adapter](../../internal/httpserver/management_collection_execution.go).
Ordinary operation visibility, cursor-type separation and existing
`GetOperationValidation` remain intact. This read interface adds no activation
request body and does not enable public Activate.

Typed fields carry catalog decision and child disposition separately in
`ApplyResult`. Existing `committed`/`applied`
booleans retain false versus omitted availability. `committed=true` means a new
catalog mutation was accepted; unchanged is separately successful but creates
none. `applied` is absent while its accepted child is pending or when no child
exists. Only a recorded child disposition supplies the final boolean. The
execution result availability object (`pending`, `ready`, `expired`) carries
explicit counts and the sealed summary; errors, including unavailable history,
remain typed errors. Metadata-only operation lists carry no execution item rows.
Waiting for a canceled operation and waiting for its final retained result are
distinct SDK/UI operations; cancellation of either wait does not cancel work.

The implementation exposes bounded live counts before finalization and
immutable item pages only after the seal. If live item pages are added,
capture a finite observed progress/root and watermark and label them provisional;
never join later child terminals into an earlier retained cursor.

The protected `CollectionExecutionResultView` and HTTP adapter enforce current
authentication/permissions before existence, original actor checks
before rows, one immutable anchor/seal plus history watermark, then bounded
100-default/500-maximum pages within 4 MiB. Cursors bind operation, original
result, principal, current access generation, page limit and watermark. They
expire at the earlier of five minutes or the result's 30-day deadline. Fresh
store/epoch/health, policy and monotonic time checks run after disk reads. Do not
hold authentication or cursor mutexes across I/O; preserve the existing concurrent
read and retained-byte quotas. Rotation permits a new same-owner query but
invalidates old access-generation cursors. Explicit restore keeps old-epoch API
handles fenced even when their historical evidence is retained locally. Final
response admission also rechecks protected metadata after waiting for policy or
cursor locks. A lock-free observation of the committed monotonic history cutoff
prevents retention advancement during those waits from returning stale rows.
Native cutoff observations publish only after the catalog save succeeds and are
initialized from successful recovery.

The [SDK result methods](../../sdk/go/operation_execution.go) provide a bounded
page read, lazy iterator and separate wait for execution-result readiness. The
[dashboard reader](../../dashboard/src/components/CollectionExecution.tsx)
provides explicit read, wait and page navigation while preserving one detached
page and original identity/summary. Failed or invalidated reads cannot revive an
older ready observation. CLI `get operation ID --results` reads one SDK page;
its completion evidence belongs in the interface implementation record.

Protected point reads and result pages now support an absent source header.
Bounded index lookups verify canonical original anchor/primary evidence, recover
the original actor before seal or item access, and retain the immutable receipt
and watermark across pages. Missing unexpired anchor/seal evidence is unavailable;
absence alone cannot establish expiry or an empty success. The detached parent
observation contains only proven metadata, with no reconstructed source timestamps
or activation grant. Tests remove the header after committed finalization and
publication; the private retirement command does not remove it. Retained operation
listing after header removal still needs implementation and qualification.

Retention starts at immutable `FinalizedAt`, not activation, cancellation or
upload expiry. A long-running parent retains authoritative execution facts and
pending children regardless of ordinary history's deadline. At or after the
result deadline, persist explicit `HistoryExpiredAt` and the monotonic history
cutoff before permitting cleanup, following the existing validation expiry fix.
A backward caller clock must not resurrect expired data after header deletion.
Do not claim physical space reclamation from logical expiry.

## Cleanup requires its own verifiable progress

The format-12 private `retire` transition now persists the
[retirement checkpoint](../../internal/persistence/collection_execution_retirement_checkpoint.go)
and uses its bounded
[prefix tree](../../internal/persistence/collection_execution_retirement_tree.go).
It pairs each accepted outcome with its terminal, retains cumulative retired
commitments and a bounded terminal frontier, and rejects proofs or insertions
inside the retired prefix. Partial-prefix snapshots and recovery validate those
commitments with the surviving original execution suffix. See the
[retirement contract](collection-execution-retirement.md) for the registered
command and scoped evidence. Format 13 adds automatic selection and source/header
cleanup; its scoped integrated checks are recorded separately from this plan.

Keep the generic cleanup command's refusal for execution-bearing parents; source
retirement uses the explicit `retire_sources` action. Private execution-prefix
retirement requires a fixed terminal result, no unresolved accepted children,
no future execution grant, and either the exact
retained seal or durable expiry. The existing validation-result publication guard
still applies independently. Historical format-8/9 canceled admissions whose
input was already reclaimed cannot be assigned fabricated per-item results.

The execution retirement fence contains original binding, immutable result
identity, decision/root commitments, publication state and exact prefix checkpoint.
Each command deletes at most 256 physical records/4 MiB and commits its ledger
removal with the parent checkpoint. It disposes of an unaccepted prepared slot
only after the final result accounts for it and the decided prefix is retired.
A child's permanent 4,096-byte charge is released only when its acceptance and
terminal records are retired together; terminal arrival itself never refunds it.

The implemented transition advances an increasing decision prefix, pairing an
accepted outcome with its terminal. A rolling removed-outcome prefix digest and
bounded sparse Merkle frontier retain the removed terminal commitment. Remaining
records plus these checkpoints reproduce the immutable final outcome digest/root
on recovery. An empty terminal leaf for a nonaccepted decision must remain distinguishable
from a missing required terminal. Committed deletion and partial-prefix recovery
now use these commitments; neither raw counters nor a hash of deleted bytes alone
proves linkage. Terminal parents receive a dedicated recovery audit rather than
being skipped by the complete-inventory auditor.

The next cleanup phase must retire validation rows, plan and input only after
execution retirement completes. It still needs an explicit terminal-only validator
that preserves original descriptors while checking the remaining source prefixes.
Header deletion must come last; it is not enabled by the format-12 execution-only
transition. Readers pin detached receipt/watermark state, not a database
transaction across cleanup. Snapshot,
offline backup/import and restore must accept exactly the committed retired
prefix and reject missing rows outside it. History publication is not a source
for resuming execution or reconstructing an executable plan.

## Ordered implementation and acceptance

1. **Terminal result state/command and anchor — implemented privately.** Normal
   completion, partial/failure classification, canceled/restore settlement and
   exact retries use the original immutable identities and history barrier.
2. **Joined publication and result interfaces — implemented privately.** Codec,
   history/index, protected point/list projection and HTTP pages, SDK result
   methods and dashboard reads are present. Their scoped verification is recorded
   in the interface implementation record; CLI result output and real-server
   pagination checks are also implemented.
3. **Execution-prefix retirement and recovery — implemented privately.** Format
   12 registers bounded paired deletion, exact retirement checkpoints, paired
   child-charge refunds and partial-prefix snapshot/recovery. Retained point/page
   authority also survives header removal. The retirement record states
   the executed qualification scope.
4. **Automatic selection and source cleanup — implemented privately.** Format 13
   adds bounded validation/plan/input reclamation, committed surviving-prefix
   digests, header-last deletion and retained listing. Scoped checks cover
   snapshots, offline/restart, forced termination, failed writes, cursor lifetime,
   original retention and bounded warm verification. Maximum-size cold-audit
   latency remains a measurement boundary.
5. **Public activation and connected Apply — implemented privately.** The existing
   path-only Activate is connected through real API/controller/restart paths,
   SDK Apply/Wait, CLI apply/wait and explicit dashboard activation. See the
   [integration record](collection-public-activation.md). Cross-process reselection
   remains unfinished. Status, result pages and probes must cause no provider operations.

New result/checkpoint fields and commands require an explicitly versioned outer
format and decoder/snapshot/offline hooks. Format 12 introduces execution
retirement and format 13 adds source retirement; formats 10 and 11 retain their original finalization/publication
contracts. Preserve earlier replay, namespace framing and unique mutation-token
behavior. Do not
reinterpret old commands as result publication, retroactively change cancellation
events, or use a numeric `>=` test to recognize unknown future formats. Select a
new format number only when that implementation lands. No successful automatic
rollback, provider-account certification or endurance claim follows from this work.
