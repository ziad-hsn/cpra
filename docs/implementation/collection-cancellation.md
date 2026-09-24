# Inactive collection cancellation boundary

Implemented internally in the durable state machine. This document records a
bounded part of the approved collection plan; it does not introduce a public
endpoint, SDK cancellation operation, or collection activation.

An inactive upload can receive a `cancel` command with its original operation
handle, upload identity, original uploader, a cancellation UUID, and an observed
time. The operation handle carries the storage epoch. Admission must separately
authenticate and authorize the caller; supplying the uploader's identifier is
not authorization. The command must arrive through the existing writable store
boundary, which rejects failed storage, unfinished bootstrap, restore, and
authentication reset.

The transition accepts only an unexpired `uploading` header. Its time cannot
precede the most recent committed upload activity. It changes the phase to
`canceled`, stores the original cancellation identity, and emits one
`collection_canceled` history event. Original upload counts, ciphertext,
inventory digests, and inactivity timestamps stay intact. Uploads and both
captured and newly requested validation views immediately reject that input.
No active resource, check, notification, or recovery operation is admitted.

An exact retry while the header remains returns its current terminal metadata
without a second event or another cleanup step. A different cancellation ID,
actor, or observation conflicts. Expired or invalidated outcomes are never
rewritten. After cleanup retires the header, its issued handle returns the
existing explicit expiration result; retry never reconstructs input or allocates
a replacement handle.

Existing maintenance deletes at most 256 rows and 4 MiB of encoded rows per
committed cleanup command, starting at the highest ordinal. It preserves the
remaining contiguous prefix and original inventory counters while advancing
removal counters. Final cleanup deletes the header and releases its slot in the
64-upload bound. A retained snapshot read view remains immutable. Logical byte
reclamation does not imply physical bbolt file shrinkage or deletion of old
materialization generations.

Cancellation metadata and remaining ciphertext are part of the existing
unpublished format-3 snapshot. Snapshot and log replay preserve the transition
without repeating the audit event. Explicit backup restoration changes the
operation epoch and preserves existing terminal outcomes; only still-uploading
input changes to `invalidated`. Old handles cannot resume. Cleanup can later
retire those old terminal inventories without changing active configuration.

The event contains only the collection operation handle, original content
commitment, actor, cancellation ID, observation time, and canceled outcome.
Normal history retention keeps that audit evidence for 30 days. The subsequent
[terminal receipt slice](collection-terminal-receipts.md) adds an internal
queryable receipt index for new cancellation, expiration and restore-invalidation
events after upload cleanup. The public cancellation contract, activation state
machine, and HTTP/SDK operation projections remain unfinished. These internal
boundaries therefore do not complete public collection cancellation acceptance.

Qualification covers memory and bbolt ledgers, exact and conflicting retries,
same-log upload/cancel ordering, copied metadata, bounded cleanup, quota reuse,
read invalidation, storage and restore fences, actual Raft snapshot/log restart,
stopped-directory validation, explicit restore, and audit retention. Executed
commands and source hashes are recorded in the private verification artifact
`bin/verification/collection-cancellation/result.json`.
