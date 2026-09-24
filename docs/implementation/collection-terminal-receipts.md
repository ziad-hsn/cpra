# Retained terminal collection receipts

The internal durable store now provides `CollectionReceipt(ctx, id, at)`. It
returns a safe active-upload observation or a retained terminal receipt after
the encrypted upload rows and header have been removed. The initial terminal
outcomes are `canceled`, `expired`, and `invalidated` by explicit restore. They
do not imply that any resource was activated or applied.

The receipt contains the original operation and upload identities, uploader,
inventory format and content commitment, declared and uploaded counts, lifecycle
timestamps, and applicable cancellation or restore identity. It contains no
secret envelope, configuration payload, inventory key, source path or source
fingerprint. It is distinct from the protected `CollectionState` used by internal
upload preparation.

Cancellation, the first committed expiration cleanup step, and restore
invalidation emit a typed receipt inside their audit event. History writes the
event and its `collection_receipts` point index in the same synchronous bbolt
transaction, before advancing the synced history watermark. Exact log replay
uses the same event key and receipt bytes. A contradictory outcome cannot replace
the original indexed receipt within a segment. Querying contradictory retained
outcomes across segments reports unavailable.

Headers remain in the bounded upload map only while ciphertext cleanup requires
them. Terminal receipt lifetime uses the existing 30-day history retention,
independently of that cleanup. The history index and authoritative events travel
together in complete-directory backups. Snapshot/log recovery and stopped backup
validation require their indexes to agree in both directions.

Lookup performs one receipt-index read and, where needed, one primary-prefix
seek per retained daily segment. It does not scan unrelated events or resources.
There is a 64-segment safety bound and an 8 KiB encoded receipt-event bound.
Memory-only history uses point maps and the same retention boundary. Calls honor
context cancellation while acquiring locks and between segment reads; an
individual operating-system filesystem read is cooperative and cannot be forcibly
interrupted by a Go context.

The current operation epoch and allocation high-water mark distinguish an issued
expired handle from an unissued handle. Missing or unreadable history, damaged
indexes, and incomplete history progress report unavailable. A retained terminal
header also provides an independent expected receipt identity. Neither lookup
nor replay creates a replacement operation, renews upload activity, or invokes
providers. Explicit restore makes old handles non-resumable under the new epoch;
their historical receipt events remain evidence.

Older private format-3 terminal audit events remain readable in the timeline.
They lack the full receipt inventory and cannot be truthfully reconstructed after
the old header was removed. A bounded primary-prefix lookup detects such retained
evidence and reports receipt unavailable, rather than fabricating counts or
calling it expired. No repair deletes those events. Older terminal headers without
receipt timestamps remain valid for cleanup.

This completes the internal terminal point-lookup boundary for the three inactive
outcomes. HTTP/SDK operation projections, listing and pagination, the complete
public cancellation contract, activation progress/results, and browser operation
workflows remain separate implementation work. Receipt retention alone is not a
create-request deduplication mechanism after retention or explicit restore;
creation admission requires its own durable identity contract.

Focused verification includes actual Raft cleanup/restart and explicit restore,
retention and segment deletion, malformed or missing indexes, retained legacy
events, partial history-watermark recovery, copied observations, cancellation,
metadata bounds, and ordinary operation/history/restore regression selections.
Commands and source hashes are recorded in the private artifact
`bin/verification/collection-terminal-receipts/result.json`.
