# Private collection executor: next implementation boundary

Status: preserved design for the private executor implementation boundary.
The command/snapshot path, candidate headroom, work selector and private
coordinator have since passed their scoped checks. Cached preparation and
application lifecycle wiring have separate qualification, tracked in the
[implementation progress](dashboard-implementation-progress.md). The discussion
below explains the selected design and the concerns present before those
changes; it is not a claim that public Activate is enabled. Candidates remain
private until the agreed shipping gates pass.

The governing contract remains [collection activation](collection-activation.md)
and the [approved API plan](api-management-plan.md). No new storage format,
replacement validation, or item-outcome enum is proposed here.

## One owner, one item in flight

Add one execution coordinator per opened Store, with a context-aware selector
over at most the existing 64 retained parents. Return detached original identity
and progress metadata, never the protected input key or ciphertext. Ordinary
restart resumes the original applying operation; it must not adopt validation's
policy of interrupting an abandoned compiler claim.

Drive the next original ordinal from committed progress:

| Observed state | Action |
| --- | --- |
| No execution progress | Submit `begin`. |
| Committed prepared slot | Submit `decide` with its exact prepared ID; skip candidate recreation. |
| Original row is unchanged | Submit `decide` without a candidate; let the FSM evaluate the original guards. |
| Unprepared create/update | Prepare from original input, commit `prepare`, then submit `decide`. |
| Actual original-target conflict, with no prepared slot | Submit no-candidate `decide`; the FSM must independently confirm the target conflict. |
| Every ordinal processed | Stop item admission and observe pending children; do not invent parent completion. |

The candidate helper currently checks the target before returning an existing
prepared record or recognizing an unchanged row. The coordinator must therefore
bypass that helper in those two cases. A stale-view `ErrCollectionConflict`, KMS
failure, invalid input, or quota error is not an original-target conflict.

Derive the permanent actor from the admitted parent. Observe current durable
authority and capabilities before each submission, with a fresh command time;
the FSM rechecks them atomically. Credential rotation may refresh the authority
fence, but never rewrites the activation, prepared identity, ciphertext, or
original plan. Crypto runs outside process admission locks. Draining, canceled
parents, revocation, changed capabilities, and old restore epochs prevent new
item admission.

## Reuse verified metadata without weakening reads

The present preparation constructor scans all original input, plan and result
rows, then reconstructs the preceding execution prefix for each item. Across
`N` items and `F` plan fragments this introduces roughly `O(N*(N+F)+N²)` work.
That is a source-derived complexity observation, not a throughput measurement.

Reuse the FSM's one bounded immutable original-plan index. An idempotent `begin`
already populates that cache. A cold cache may require one such reconciliation;
it must not silently trust ledger statistics or decode canonical rows as proof
of an authoritative prefix. Do not add a second full-plan cache in management.

The cache binding must match the current ledger generation and original parent,
upload, activation, plan header/descriptor/progress, input inventory, finalized
result descriptor and capability profile. Compare current execution progress to
its already verified outcome commitment cache, terminal root, prepared
commitment, and ledger statistics. Restore rebuilds those derived caches;
runtime writes install updates only after their atomic transaction succeeds.

**Required addition:** the existing row index does not retain an original
encrypted-input frame hash. During complete original verification, record the
SHA-256 of each exact canonical input ledger frame, including its operation and
item wrapper. Charge the additional 32 bytes per indexed row to the existing
metadata budget before allocation. Every reused-index selected input read must
re-encode/check canonical framing and compare that hash, in addition to original
ordinal/key/source coordinates and the later authenticated plaintext MAC.
Metadata equality alone does not detect replacement of ciphertext or its MAC.

For each step, stream only the selected original row against its verified range
and digest, then detach its input, original target and any prepared record.
Close every pinned ledger transaction before a live-state recheck, KMS work or
write. The holder retains neither a database transaction nor authority to write.

A narrow preparation fence may replace whole-header equality. It must retain
the original epoch/owner and immutable artifact bindings, applying phase,
processed count/outcome digest, and exact prepared-slot identity/commitment.
Terminal-only child progress can advance independently after checking its fresh
authoritative commitments. Current authority and the exact original target
remain separate live checks. Never substitute a new UID/revision because an
incoming reverse-reference token changed.

## Uncertain writes and backpressure

After `ErrCommitUnconfirmed`, keep the original command and candidate identity.
Use the existing FIFO `Store.Flush` barrier with a bounded reconciliation
context before interpreting an absent slot: the earlier admitted command may
still commit after its caller stopped waiting. Reconcile the original prepared
record or certified outcome; retry an unchanged candidate only after confirmed
ordering establishes that neither committed. If the barrier is also uncertain,
retain uncertainty and stop advancing. Completed startup recovery provides the
ordering boundary after a process restart.

`ErrCatalogBusy` and collection quota pressure preserve the prepared slot and
must cause bounded waiting, not replacement identities or item conflicts.
Sequence exhaustion needs an explicit blocked observation rather than a retry
loop. Yield between bounded turns; synchronous isolated writes and large row
verification remain real costs to measure. Canceling a wait does not cancel the
parent. Committed cancellation stops remaining items while accepted changes and
pending child outcomes remain durable.

Shutdown stops new work, requests cooperative cancellation, joins the owner,
then flushes and closes storage and keys. A wait deadline does not release
ownership of an uncooperative attempt.

## Validate candidate encoding headroom first

`stagedValidator.normalized` in `internal/management/staged_validation.go` is the
shared staged compiler seam: after omitted-credential preservation and
`validateDesired`, before the present normalized encoding check and callback.
Validation currently may accept a near-1-MiB input whose later server-owned
metadata makes the prepared resource exceed the encoded resource limit.

Add a pure candidate-size check shared with candidate preparation and
request-local collection validation. For creates, use fixed 36-byte ASCII UUID
placeholders and generation 1 in a size-only metadata copy. For updates, retain
the exact original UID, use a fixed-length new revision placeholder, and compute
the actual generation from the normalized spec comparison; reject a required
increment beyond `MaxInt64`. Marshal and clear that temporary copy, enforcing
`api.MaxResourceBytes`. Unchanged rows need no replacement headroom. This check
mints no identity, encrypts nothing, and refreshes no guard.

Map failure to the original source item's `invalidResource` before sealing a
successful validation. Increment `collectionValidationPolicyVersion`, whose
existing contract covers changes to accepted configurations. Previously sealed
old-profile results must not silently acquire the new execution contract.
Unexpected deterministic preparation failure after a supposedly qualified
verdict must halt explicitly as an invariant failure, not leave a silent retry
loop or become a target conflict. KMS and corruption failures remain distinct.

## Remaining gates and focused evidence

Suggested ownership is a small management step/coordinator, bounded durable
work selection and registration, narrow preparation-view/cache changes, and
the shared candidate-size check. No public route or SDK execution API belongs
in that first slice.

Required tests cover committed preparation followed by target conflict,
unchanged conflict, lost preparation/decision replies and an uncertain barrier,
quota recovery with the same identity, current authority rotation/revocation,
cancellation during crypto, child completion during preparation, corrupted
selected input with unchanged metadata, and counted reads proving whole-prefix
reconstruction is absent from the hot path. Include exact encoded-size and
generation boundaries before claiming validated input is preparable.

Parent terminalization, retained execution-result publication, and safe ledger
cleanup remain separate work. `Processed == ItemCount` does not establish that
accepted children finished or that terminal evidence was published. Keep the
public Activate/Apply gate closed until these lifecycle and interface contracts
are implemented and qualified. This note claims no performance, provider,
endurance, or release qualification.
