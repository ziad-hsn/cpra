# Collection execution records and completion capacity

Status: private conditional execution is registered in storage format 9.
The record model, ledger and framed stream have scoped qualification; the
integrated command/snapshot boundary passed its scoped matrix and full durable
race suite (449.031 s). Command, snapshot and real-process termination checks
pass at that recorded source boundary. Later coordinator/cache changes have
separate qualification.
No public activation HTTP route or background collection executor is enabled by
this work. Formats 3–8 still reject a nonempty execution namespace instead of
silently omitting it from a snapshot. See the
[format-9 integration boundary](collection-execution-integration.md).

The [activation contract](collection-activation.md) remains authoritative. Its
original owner, complete input, finalized plan and resource conditions must be
rechecked at actual item acceptance. A disk materialization, derived index or
caller-supplied predecessor assertion cannot authorize a write.

## Records

All records bind the original parent operation, upload, activation, plan identity
and plan digest. The plan ordinal and original input ordinal remain distinct.

| Record | Contents | Lifetime |
| --- | --- | --- |
| Prepared candidate | Exact selected incarnation/revision, encrypted resource, row identity and observation time | At most one current slot per parent; exact acceptance consumes it |
| Item outcome | Original source coordinates, conditional decision, accepted tuple/token and optional distinct child receipt | Immutable by original plan ordinal |
| Child terminal | Original outcome binding, child ID, finite disposition, time and optional restore marker | Immutable, separately from catalog acceptance |

An accepted create/update has an ordinary configuration child receipt. An
unchanged, conflicting or dependency-blocked item has no invented child. Only
ordinary catalog children are eligible; incident, control, recovery and review
receipts require their own existing lifecycle handling.

The terminal record is compact. Its immutable resource identity and admission
metadata come from the original acceptance. Reconstruction must match that
acceptance exactly; a different actor, resource version, incarnation or child
handle is not a terminal update of the same work. Provider diagnostics, source
paths, credentials and executable objects never enter these records.

## Quota and atomicity

One accepted child permanently charges its canonical acceptance bytes plus a
4,096-byte terminal allowance. The later bounded terminal uses this prepaid
capacity and neither adds to nor refunds charged usage. The allowance stays
charged until the parent's retained results can be safely reclaimed. Encoded
bytes, charged bytes and unfilled terminal capacity are different measurements.
This prevents logical quota exhaustion from discarding a known outcome; a real
filesystem write failure still makes durable storage unavailable.

One synchronous ledger transaction may consume the exact prepared candidate,
append its immutable outcome and record a superseded earlier child, including a
child belonging to another parent. A later failure rejects the whole transaction.
Memory-only mode provides the same logical atomicity without disk durability.
Transactions contain at most 256 records and 4 MiB. Reads return at most 500
records and 4 MiB; sparse terminal rows remain deterministically ordered.

An exact outcome retry compares canonical stored bytes and consumes no extra
capacity. Once preparation has been consumed, callers reconcile its original
outcome. The removed ciphertext cannot be certified by reusing only a preparation
ID. No retry may re-create the consumed slot or silently substitute ciphertext.

## Snapshot boundary

The execution stream uses the same frozen ledger generation as input, plan and
validation data. It records strict canonical frames, ordered parent/slot keys,
record count, encoded and charged totals, and a digest. Recovery imports bounded
batches into a fresh unpublished generation. It rejects missing or mismatched
outcomes, orphan terminals, incompatible payloads, reordering and corruption.
It never merges a snapshot into an existing execution namespace.

Snapshots naturally omit already-consumed preparation. Their importer therefore
has a distinct internal mode that reconstructs accepted records without requiring
historical preparation. This is not available as normal execution or admission.
A malformed later batch or footer leaves only an unpublished prefix, which the
caller must discard. The importer closes its inspection view before writing;
the executor closes its verification view before synchronous acceptance.
Candidate preparation detaches one next item and closes its database reader
before encryption or further current-state checks, preventing a concurrent
writer/remap cycle. Snapshot persistence can overlap later updates.

The parent progress model and pending-child helpers are implemented. Existing
completion, catalog supersession and explicit-restore paths now persist linked
terminal evidence before deleting the ordinary receipt. Their native write-failure
tests require the image, pending receipt, reservation and restore cursor to remain
unchanged. The existing history barrier remains before acknowledgement. A canceled
parent retains unresolved children; an unbuilt link cache cannot bypass persistence.

Original-item acceptance, complete inventory reconstruction, replay and stopped
validation are now integrated in the private format-9 path. Public activation
still requires retained-result publication and bounded cleanup, as well as the
management runner and interface contracts. Execution-bearing parents currently refuse
cleanup. This conservative intermediate guard prevents evidence loss; it is not
the final retention implementation. Every outstanding child must match one
pending ordinary configuration receipt on recovery.

Current verification and its limits are recorded in the
[implementation progress log](dashboard-implementation-progress.md). These
primitives alone establish no dashboard Apply, controller-completion, provider,
performance or shipping claim. All candidates remain private until the agreed
gates pass.
