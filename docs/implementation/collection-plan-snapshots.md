# Inactive plan snapshots, format 5

Status: private durable prerequisite, 19 September 2026. Format 5 retains an
inactive plan artifact alongside its original encrypted input. It does not
register public Validate/Activate, apply a catalog resource or invoke a provider.

## One committed image, two required namespaces

The snapshot starts with `CPRA-COLLECTION-SNAPSHOT-5`, followed by the state
image, the existing encrypted input-ledger stream, and a separately framed plan
fragment stream. Both streams are mandatory, including when either is empty.
Missing streams, extra trailing streams, reordered or missing fragments,
incorrect counts, byte totals and digests all fail restoration.

The controller's authoritative headers and the shared ledger view are captured
under the FSM lock. The input and plan streams use that one frozen bbolt read
transaction; serialization does not open a newer transaction between namespaces.
Later appends, plan finalization or cleanup cannot change the captured image.
The snapshot owns its read view until release. Holding an unreleased view while
closing its database is invalid; backup and test helpers use the complete
Persist/Release contract.

Plan rows contain only the reviewed metadata artifact, including original
identities, source coordinates and version/dependency guards. The original
resource bytes remain in the encrypted input namespace. Snapshot validation
checks that every plan row's input ordinal, key and source coordinates match its
original input and that the plan header matches its authoritative collection.
This structural check does not replace graph validation or grant execution
authority.

## Bounded recovery and retirement

Recovery imports both namespaces into a fresh, unpublished generation. The plan
stream's canonical records are bounded independently; synchronous import batches
contain at most 256 fragments and 4 MiB of encoded records. Input and plan rows
share the ledger's logical byte quota. A failed import may have written earlier
atomic batches in its unpublished generation, but it cannot replace the running
or restored committed ledger. Physical database allocation is a separate bound.

Every retained plan records its immutable header/descriptor and committed
fragment prefix. An incomplete plan remains inactive and can resume only its
original fragments and identity. A complete verified artifact records a
conditional finalized state; the snapshot contains that state and all of its
fragments. Parser caches are derived state: snapshot installation discards them,
and later committed replay or append can rebuild them from the retained prefix.

Cancellation, expiry and explicit restore prevent an inactive plan from becoming
executable. Terminal cleanup removes a bounded plan tail before reclaiming its
encrypted input, and retires the collection header only after both namespaces
are empty. Original totals remain audit identity while removed counters describe
the retained prefixes. A shortened terminal prefix is never presented as the
original complete artifact. These inactive transitions do not establish
cancellation after partial catalog application or rollback of external actions.

Stopped-backup validation uses the same complete decoder and committed replay in
private scratch storage. It validates older retained snapshots as well as the
latest snapshot and following logs; it neither repairs nor rewrites the source
store. Complete-directory backups remain necessary, including retained history.

## Compatibility and evidence

Formats 1 and 2 retain their previous JSON snapshot behavior. Formats 3 and 4
retain their literal outer framing and exact input-stream encoding. Their
snapshots cannot contain plan fragments. Format 5 has explicit framing and keeps
the [format-4 mutation-token semantics](catalog-mutation-format.md).

A format-5 image may have a zero catalog mutation sequence when no catalog
mutation has succeeded yet. Its first new catalog mutation seeds the sequence
from the preceding image index and increments it. Legacy format-2/3 catalog
mutations are rejected after the image reaches format 5, even while the sequence
is zero. The image version never drops when later ordinary commands use older
envelope capabilities.

Plan begin, append and finalize commands require format-5 envelopes, as does
cleanup carrying plan-specific progress. The decoder rejects those payloads in
format-3/4 envelopes. Cancellation adds no plan payload, so its existing command
shape can retain format 3; the committed format-5 image selects the inactive
plan transition without downgrading state.

Release metadata records 5 as the highest supported application storage format.
Rollback requires a reader that supports every committed snapshot and log
format, including rejected commands retained in the log. There is no automatic
downgrade, state deletion or old-generation fallback.

Scoped tests cover memory and real disk namespaces, frozen views during later
writes, count- and byte-bounded imports, corrupt footers, truncated or missing
streams, quotas, short writes and unchanged format-3/4 framing. A real Raft test
restores a format-3 input snapshot plus format-5 partial-plan logs, validates the
stopped store, resumes the exact artifact, records inactive finalization and
restarts from a format-5 snapshot. Failed restoration leaves the current ledger
and authoritative header intact.

The local evidence is `bin/verification/collection-plan-snapshots/result.json`.
These checks do not execute an historical binary, qualify native Windows/macOS
storage, prove power-loss behavior, publish a release, or complete the activation,
provider-account or million-monitor endurance gates.
