# Encrypted collection persistence

Status: private implementation, 24 September 2026. The persistence layer retains
inactive encrypted input. [Public admission](implementation/collection-admission.md)
now supports preparation, upload and cancellation of inactive collections, with
[retained terminal receipts](implementation/collection-terminal-receipts.md).
Public validation, [activation and connected Apply](implementation/collection-public-activation.md),
and [original-file upload recovery](implementation/collection-reselection-runtime.md)
are now integrated. Uploading input alone never makes it executable. The complete
management and distribution qualification remains open.

## What is committed

Each collection has a bounded header and an ordered sequence of encrypted rows.
The header contains the original operation handle, upload identity, declared
inventory, actor, inactivity deadline, and committed progress. The inventory key
and source fingerprint are encrypted. A row encrypts the exact transmitted
resource JSON and records an opaque source token and document/item coordinates.
Local filenames, source URLs, provider clients, and executable jobs are absent.

Creating an upload and appending an item do not alter the active catalog or
invoke a provider. An exact internal retry returns its existing progress. A
changed item, duplicate resource identity, skipped ordinal, changed upload
identity, or expired handle is rejected. Accepted appends and identical retries
renew the 24-hour inactivity deadline. Background maintenance retires expired,
unactivated uploads and removes at most 256 rows or 4 MiB of encoded input in
each committed cleanup step. An activity/progress fence prevents a stale cleanup
observation from deleting a renewed upload. Expiration records one retained audit
event and prohibits further upload or activation. Physical file shrinkage is
separate from this logical reclamation.

An HTTP upload accepts at most 256 resources and 4 MiB of resource input.
Owner-bound uploads prepare encrypted rows against one captured prefix, then
submit bounded groups of the existing commands. Store command-count and encoded
byte limits may split one HTTP chunk into several Raft entries; ciphertext JSON
can be larger than its original resource input. Synchronous log and ledger writes
remain enabled.

Every new row carries the predicted exact predecessor digest, byte count and
committed operator authority. A competing append or rejected predecessor prevents
dependent rows from appending against a different prefix. Identical retries reuse
the original stored ciphertext, including after key rotation. Historical ownerless
operations retain serial admission. These are per-row conditional commands, not
chunk-wide transactions: accepted prefixes survive a later rejection or lost
response. Read the original operation to reconcile progress before continuing.

Raft logs and self-contained snapshots are authoritative. A bbolt ledger is a
derived, encrypted materialization of the committed rows. Every restart builds
a fresh generation from the selected snapshot and subsequent committed log.
Rows left by a later run cannot fill a missing prefix in an older replay. A
missing derived database can be rebuilt; missing or corrupt authoritative state
must fail startup. Replay must also reach the retained history watermark before
new recovery commands can be committed.

## Snapshot and restore boundary

Format 3 adds the `CPRA-COLLECTION-SNAPSHOT-3` framing: the ordinary state image,
followed by a streamed, ordered encrypted ledger with counts, byte accounting,
and digests. Snapshot capture freezes the headers and a read transaction before
background serialization. Later appends cannot change that captured view. This
follows the [Raft FSM snapshot contract](https://pkg.go.dev/github.com/hashicorp/raft#FSM)
and bbolt's [read transaction semantics](https://pkg.go.dev/go.etcd.io/bbolt#Tx).

Snapshot validation imports into a fresh, unpublished generation and checks the
entire stream before making it available. Runtime recovery publishes its new
generation only after replay and restore reconciliation finish. A failed ledger
write makes durable readiness and subsequent admission unavailable. It never
switches to memory or treats a stale materialization as committed input.

The reader retains compatibility with formats 1, 2, 3 and 4. New catalog mutations
and bootstrap seeds use format 4, which assigns unique reverse-dependent
mutation tokens even when several changes share one Raft entry. Its explicit
`CPRA-COLLECTION-SNAPSHOT-4` outer framing retains the same inner encrypted input
ledger stream; it does not add executable collection plans or item outcomes.
Historical format-2/3 catalog commands still replay with their original log-index
semantics. Frozen format-3 snapshots retain their original bytes. Subsequent
collection and lifecycle commands cannot downgrade an upgraded image. See the
[format migration boundary](implementation/catalog-mutation-format.md).

[Format 5](implementation/collection-plan-snapshots.md) adds the mandatory
inactive plan-fragment namespace to the same frozen snapshot and fresh recovery
generation. Its metadata and encrypted input remain tied to their original
operation, upload identity and committed progress. Retaining and structurally
verifying an inactive artifact does not register public validation or activate
its resources.

Release metadata's
`storage_format_version` is the highest format the binary can write and read;
it does not mean every existing store has already been upgraded. Rollback
requires an older binary capable of reading all data written by the candidate.

Subsequent [format 9](implementation/collection-execution-integration.md) added
isolated conditional-item commands and an execution snapshot namespace.
The current private candidate writes through format 14, which records the
normalization profile and conditional upload prefix. Formats 12 and 13 added
[execution and source retirement](implementation/collection-execution-retirement.md).
Public activation is now connected through the separate explicit operation flow.
A complete
stopped backup must preserve all snapshot/log/history data; execution evidence
is never reconstructed by repeating a provider operation.

Explicit restore changes the operation epoch. Previous collection handles
expire, and retained uploading, validating and validated inactive headers become
invalidated. Existing terminal
outcomes are preserved; restore does not resume or activate the old collection.
Bounded cleanup can retire invalidated rows after
authentication has been reprovisioned. Stopped backup validation reconstructs the
ledger in a private temporary directory, without opening a second active owner
or modifying the backed-up state. Preserve the complete data directory and its
matching configuration/artifact identity. Manage wrapping keys separately.

## Bounds and remaining integration

The initial internal limits are 64 retained collection headers, 10 million
declared items per collection, and 1 GiB of logically encoded input and plan data
combined.
Ciphertext pages are bounded by count and encoded bytes. Snapshot import uses
bounded transactions and never constructs a slice containing the entire input.
These limits are safety boundaries, not a demonstrated fleet capacity or an
increase to the public synchronous preflight limit.

Logical accounting does not bound allocated bbolt space, Raft logs, retained
snapshots, old ledger generations, or filesystem overhead. Physical accounting,
automatic generation cleanup and maximum-input resource measurements remain
separate qualification work. Public validation, conditional activation,
retained per-item outcomes and cancellation during activation have scoped
evidence in the linked lifecycle contracts. The separate
[Linux retirement primitive](implementation/collection-generation-cleanup.md)
is not yet connected to Store maintenance. Inactive cancellation and its
30-day terminal receipt retention are distinct from cancellation after partial
application, which cannot undo committed changes or external actions.
Windows mapping/allocation behavior still requires native qualification.

Partial cleanup leaves a contiguous encrypted prefix. Its header retains the
original inventory identity and separate removed-row/byte counters; the original
final digest is not claimed to be the shortened prefix's digest. Snapshot framing
and the Raft checksum cover the retained rows, and the terminal phase cannot
return to uploading. Deleting the final row retires the header while preserving
the operation high-water mark, so an old handle cannot create a new operation.

The current [pure preflight](collection-preflight.md) separately bounds retained
validation data to 32 MiB. The browser's larger source limit does not bypass that
bound. The incremental activation implementation must commit each catalog CAS,
item result, and parent-operation progress in one deterministic transition,
distinguish controller application from catalog commitment, and preserve
partial outcomes. Reusing a parent collection handle for unrelated child
mutations is not a safe substitute.

Current tests cover exact retries, invalid ordering and identity, a frozen
snapshot followed by later writes, forced process termination, restart replay,
truncated streams, failed ledger writes, missing committed Raft data despite a
newer derived copy, explicit restore invalidation, and stopped backup validation.
These checks qualify this storage foundation; they do not establish completed
collection activation, a production-account provider result, or endurance at a
million monitors.
