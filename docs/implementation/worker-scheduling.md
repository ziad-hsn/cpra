# External check generations and unstarted assignment recovery

Updated: 2026-09-28. This private implementation extends the
[offer and Start contract](worker-offers-and-start.md) and
[result receipts](worker-result-receipts.md). Production external configuration,
controller dispatch and worker route registration remain incomplete.

## Admission and retained receipts

A confirmed terminal check releases its monitor's active check slot and its
JobType's active execution slot. The next generation can be admitted without
discarding the previous execution or changing its receipt. At most one active
check exists per monitor incarnation. Recovery and notification outcomes reported
as unknown continue to consume active capacity and cannot be assigned again.

The existing 256-active-execution bound applies per JobType incarnation across
versions. The 4,096-record global bound and encoded-byte limit still count retained
terminal records. This change does **not** implement completed-record expiration:
reaching those limits stops admission. Production dispatch must remain unavailable
until bounded recent check receipts and durable generation fences support safe
retirement. Action identities and unknown outcomes require separate retention.

## Requeue without another execution identity

The owner can requeue a queued record from a previous process, an expired offer,
or a rejected unstarted offer. Requeue preserves the original execution ID,
configuration and endpoint pins, encrypted parameters, generation/action identity,
schedule, overall deadline and SLO admission. The state machine checks current
controls, dependencies, source revision and action eligibility again.

Started work, terminal records and unknown actions are ineligible. Requeue grants
no permission to run a handler. A later offer receives a new lease, and Start must
commit authorization before an external operation can begin.

Retired leases remain attached to the original execution. The old worker can
reconcile its own lease and receives rejection without the replacement worker's
grant or receipt. Repeating the original retirement command cannot clear a later
offer or Start. Allocation rejects reuse of a retired lease ID. Parameter reads
also reject retired poll responses rather than returning a stale assignment.

Concurrent Start callers may record timestamps in a different order from their
Raft commits. An earlier-timestamped duplicate reads the committed disposition
and grant identity without receiving `granted` again. It cannot backdate a state
change. Requests predating an unstarted offer remain ineligible.

Recovery retains at most eight rejected leases per execution. After the ninth
offer expires, the existing finalizer records `offer_limit`. An unstarted action
is cancelled; a check contributes no data. This does not invent a provider effect
or classify offer exhaustion as a target timeout. Original overall deadlines
continue to apply throughout. A backwards clock cannot transfer work before its
last recorded retirement or issue a replacement offer before that time.

Maintenance submits at most 128 requeues per tick, further bounded by the store's
batch setting. It runs before the existing finalization pass. Retired leases fit
inside the original 32 KiB lifecycle reservation; recovery requires no new
execution slot or larger reservation. Snapshot serialization freezes the records,
and validation checks retired lease identities, ordering, bounds and absence of
an executable grant.

## Storage format and verification boundary

Tagged storage format 26 adds retired leases and the new scheduling rules.
Admission commands explicitly identify the new rules; replay of older commands
preserves their original admission decisions. Earlier snapshots retain their
original receipt and byte-accounting contracts. Default builds remain format 14
and omit the scheduling commands, methods and types.

The [scoped verification record](../review/local-evidence.md#record-ade7086c530331bcbf0ee95b)
identifies the source and executed checks. Regression coverage includes successive
check generations, more completed checks than the active per-type quota, unchanged
receipt replay, clock rollback, reused leases, replacement workers, unknown
actions, concurrent retirement commands and exhausted offer limits. SDK/TLS/Raft
tests complete two checks and recover both original receipts through log replay
and snapshots. Nine additional real process-kill cases cover before retirement,
after its commit and after snapshot for checks, recovery and notifications.

These tests qualify the storage and protocol adapters. They do not invoke a
provider handler through the complete worker runner or production dispatcher.
Independent review, schedule/pending/missed-work accounting and full
separate-process interoperability remain required. Subsequent checkpoints
implement [terminal retirement](worker-check-retention.md),
[bounded polling](worker-polling.md) and [worker observations](worker-status.md).

The subsequent retirement work preserves generation/finalization fences and
healthy monitor projection independently of old encrypted execution records. It
updates retained session references and lets the worker retire its local check
outbox after authoritative finalization when the detailed receipt has expired.
Removing server records alone would leave workers retrying missing receipts.
Unknown actions and their late evidence remain retained.

### Monitor projection before receipt retirement

The monitor-index rebuild now recognizes a finalized external check from the
monitor's generation and its current catalog incarnation's authenticated JobType
references. It does not need an encrypted execution record to recover a healthy
or no-data monitor in the controller view. Missing, removed, replaced or built-in
catalog entries and unobserved generations do not acquire this projection entry.
The existing bounded execution-index pass still covers other retained worker
state. Default builds use an unexported false predicate and expose no worker API.

The [projection verification report](../review/local-evidence.md#record-91c5577d1b75267b89800f2c)
records four tagged projection tests passing on Go 1.27.1 with race detection
and on Go 1.25.0, plus four related default-build race tests. All 1,075 recorded
Go/module inputs stayed unchanged during each run. These are memory-store and
index-reconstruction tests. They do not implement or qualify record deletion,
receipt expiry, or a Raft restore after such deletion.

Before deleting completed check records, the protocol must distinguish an expired
original execution from an unissued identity, an authentication failure and an
unavailable store. A worker must not discard an outcome on a generic 404/409 or
because its local clock reached the deadline. Retained poll replies must also
stop referencing deleted assignments without silently changing a replayed reply.
The [retention contract](worker-check-retention.md) now implements those expiry
and replay distinctions. Complete production dispatch remains unfinished.
