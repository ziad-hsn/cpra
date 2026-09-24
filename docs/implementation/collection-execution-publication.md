# Private collection execution results

Retained execution-result publication was introduced in application storage
**format 11**, following format-10 finalization driven by the existing execution
coordinator. The current private candidate adds
[execution and source retirement](collection-execution-retirement.md)
without changing the original finalization or publication identities. The package
owning this work is `internal/persistence`. Authoritative operation projection, protected HTTP
pagination, and dashboard/SDK execution-result reads are also implemented in
the private candidate.

Public collection Activate and end-to-end SDK/CLI/dashboard Apply remain gated.
The registered private `retire` command deletes a bounded execution-only prefix,
pairing accepted decisions with their terminal records and persisting the retired
commitment. Format 13 adds validation/plan/input retirement, header-last deletion
and surviving-prefix snapshot/recovery. Automatic maintenance and retained
operation listing after header removal have scoped integrated qualification.
Result reads do not enable activation.

## Finalization and retained rows

The registered execution coordinator finalizes fully decided applying parents
once their accepted children settle. It also finalizes canceled or
restore-invalidated admitted parents, including cancellation before begin.
Finalization records known facts without requiring a fresh provider credential or
capability profile. A pending command retains its original identity and time
across an uncertain reply and a FIFO reconciliation barrier. Pending item work
is resolved first. A confirmed quota refusal does not starve other parents.

Each retained item joins the original encrypted-input identity, validated plan
row, committed conditional decision and accepted child's terminal disposition.
The input ordinal determines display order; the plan ordinal preserves execution
order. An accepted catalog change remains accepted if its controller projection
fails. Unattempted rows require the original canceled/invalidated suffix proof.
Missing committed evidence cannot become an unattempted or unknown result.

Rows contain only allowlisted metadata: resource key, opaque source coordinates,
original/current incarnation and version, generation, decision time/index, and
child disposition. They cannot carry resource bodies, credentials, ciphertext,
local source paths, executable objects or provider error text. Canonical item
encoding is limited to 16 KiB.

The item builder owns and closes a frozen ledger view. It checks selected source
frames against their original commitments on every page. A disposable cache
retains one immutable audit certificate, charged within a 32 MiB logical budget;
it owns no transaction, plaintext resource or fleet result map. Execution,
finalization and publication relinquish the other original-plan cache before
building theirs. Publication snapshot recovery reconstructs the published prefix
digest and byte count from original rows, including a canceled-before-begin
inventory. After format-12 execution retirement begins, recovery validates the
committed retired prefix with the surviving original execution suffix; it does
not rebuild executable state from retained history.

## Publication and history

The isolated `CollectionExecuteCommand` action `publish` carries the original
binding and expected published offset. It takes no execution authority, prepared
resource, caller-supplied item or child identity. Each commit publishes at most
256 rows and 4 MiB including event framing. Old-offset retries add no duplicate
rows. Publication does not allocate operations or mutate the catalog sequence.

The final seal contains the immutable summary and a canonical item count, byte
count and digest. The original `FinalizedAt` fixes the cohort for the anchor,
items and seal. Memory mode stages only the bounded new-page delta; disk mode
writes primary events and secondary indexes in one bbolt transaction. The
ordinary synced history watermark precedes acknowledgement and snapshotting.
Startup and selected-page reads check primary/index equality and the original
anchor. Missing or inconsistent retained evidence is unavailable.

The descriptor is a replicated fact; result availability also requires retained
history. Historical publication replay must not branch on a process-local
retention cutoff. An old command may therefore reconstruct its original sealed
descriptor after the cohort has expired, while all result readers report expired.
For an already-expired cohort, maintenance observes the monotonic history cutoff
before proposing a replicated time and commits explicit `HistoryExpiredAt` even
if the wall clock moved backward. Still-retained publication uses the supplied
observation without raising it to a future time. The cutoff prevents a backward-clock read from reviving expired data.
Format-12 retirement uses the original committed result/publication or explicit
expiry fence. Replay preserves the original removal decision independently of
later local history expiry. A sealed descriptor alone is insufficient to infer
present read availability; general source cleanup remains gated.

The private history reader uses original input order, a pinned descriptor and
log watermark, a default of 100 rows, maximum of 500, and a 4 MiB page ceiling.
The protected Store view checks a live header's original named owner before
reading history. When that header is absent, a bounded original-anchor lookup
recovers the owner before seal or item access. It rechecks epoch, issued-handle
high-water, store health, history identity, ownership, summary and expiry after
the read.
The HTTP adapter repeats a metadata-only check after waiting for authorization
or cursor admission. A lock-free observation of the committed history cutoff
prevents global retention from expiring a cohort during that wait without being
noticed. Native retention publishes this observation after the catalog save
succeeds; successful recovery initializes it from the retained catalog. Failed
saves and backward clocks cannot advance or regress it incorrectly.
Protected point reads and pages now work after source-header removal,
using the original immutable anchor/seal rather than an inferred expired or empty
result. Existing cursors keep their original receipt and watermark across that
removal. Format-13 retirement can delete the original header, and listing merges
retained anchors with older terminal receipts. The result lifetime is independent of
an earlier cancellation receipt's expiration.

The legacy `/api/v1/history` monitor route rejects the reserved `collection/`,
`collection-activation/`, `collection-validation/`, and `collection-execution/`
namespaces before accessing storage. This prevents general observation access
from exposing another actor's collection records. Nonreserved monitor history
remains available; collection results require their own protected interface.
Current manifest and v2 resource IDs exclude slashes, but a legacy record created
directly with one of these reserved prefixes loses access through this route.

## Operation projection and result interfaces

The canonical API model has independent parent state and
`executionResult.state`, optional live counts and a sealed summary. The
[management projection](../../internal/management/collection_execution_results.go)
reports committed catalog mutations from `accepted` and applied children from
`childApplied`. A failed child never subtracts an accepted mutation. Unchanged
decisions remain successful with no new mutation or controller child. An
admission-only receipt without execution counters leaves them unavailable
instead of manufacturing zeroes.

Protected operation point/list reads preserve source identity and the original
parent state. A canceled parent's execution result remains pending while its
accepted children settle, even when the original cancellation receipt has expired. Finalized but
unsealed results expose counters without a fabricated public summary descriptor.
Ready observations require verified anchor/seal metadata; explicit expiry or the
monotonic cutoff produces expired availability without replacing the parent
outcome. Operation lists contain metadata only and suppress duplicate older
terminal receipts.

The [HTTP result adapter](../../internal/httpserver/management_collection_execution.go)
uses the existing operation-detail endpoint with bounded execution cursors.
Cursors bind the original operation/result, named principal, current access
generation, page limit and frozen history watermark. They expire at the earlier
of five minutes or the result deadline. Authorization and cursor locks are not
held across disk reads; final response admission rechecks access and protected
storage metadata. Ordinary receipt visibility and other cursor types retain
their existing scope. Point reads and pages support retained lookup after header
removal; operation listing retains its existing source-header requirement for
execution observations.

Per-item catalog decisions and child dispositions remain separate. The
[SDK result methods](../../sdk/go/operation_execution.go) provide bounded result
pages, a lazy iterator and waiting for retained results.
Waiting continues for a canceled parent whose results are pending; canceling the
caller's context does not cancel the operation. Absent/unknown availability and
explicit expiry are distinguishable.

The [dashboard decoder](../../dashboard/src/api/executionResults.ts) retains only
contract metadata, preserves bounded unknown observations, and pins collection
identity, immutable summary and input order across pages. Its
[result reader](../../dashboard/src/components/CollectionExecution.tsx) supports
explicit reading, waiting and page navigation, retaining one detached page.
Failed or invalidated reads cannot restore an older ready observation when a
wait or retry stops. Reading or canceling a read does not mutate the operation.

CLI `get operation ID --results` reads one SDK page with separate configuration
decisions and controller outcomes, including restore invalidation. The interface
implementation record will distinguish its executed completion evidence from
the already implemented read interfaces.

## Remaining qualification

The registered private format-12 retirement command now performs bounded paired
outcome/terminal deletion and refunds the child charge only with that pair. Its
[checkpoint](../../internal/persistence/collection_execution_retirement_checkpoint.go)
persists cumulative commitments and a bounded terminal frontier. The
[prefix tree](../../internal/persistence/collection_execution_retirement_tree.go)
reconstructs terminal proofs from that frontier. Partial-prefix recovery verifies
the original commitments using this progress and the surviving execution records,
rejecting incomplete accepted-outcome/terminal pairs.
Format-13 source retirement and retained operation listing have scoped
qualification, including bounded warm verification. Public
Activate, connected SDK/CLI/dashboard Apply and their connected acceptance
qualification remain unfinished. See the
[retirement contract and current verification scope](collection-execution-retirement.md).
Native release qualification, actual provider-account evidence and the selected
million-monitor 24-hour campaign remain separate outstanding gates. Candidates
remain private.

## Verification

The records below describe the preceding publication/interface checkpoints.
The [retirement record](collection-execution-retirement.md) carries the
current command, recovery and retained-read verification scope.

The new interface implementation and verification records are being assembled at
[collection-execution-interfaces.md](collection-execution-interfaces.md) and
[bin/verification/collection-execution-interfaces-2026-09-23/result.json](../../bin/verification/collection-execution-interfaces-2026-09-23/result.json).
They record the projection, HTTP/SDK/dashboard reads, independent reviews and
post-review retention checks without treating those interfaces as activation or
cleanup qualification. The full package runs preceded the last atomic-cutoff
fix; the focused post-fix race and minimum-Go checks are the evidence for that
change. Refer to those records for exact commands, durations and source scope.

The local checkpoint at
`bin/verification/collection-execution-publication-2026-09-23/result.json`
records executed commands, logs, reviewed fixes, source hashes and limitations.
Focused tests cover bounded publication, original-order joins, child/decision
separation, lost replies, native restart of a partial result, snapshots, quota
classification, explicit expiry, backward clocks and history corruption.
Independent review corrected anchor-primary checking, snapshot prefix validation
and memory publication's full-prefix copying. The cutoff tests preserve replay
determinism while denying expired availability.

The actual main/controller race scenario applies the original child, finalizes
its parent, restarts with authentication, retains the same summary and one
anchor, and keeps public Activate at HTTP 404. Ordinary reopen tests and this
controller scenario do not establish a process-kill campaign at every new
publication boundary or complete shipping readiness.
