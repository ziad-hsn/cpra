# Automatic worker execution finalization

Updated: 2026-09-28. This private Ticket 8 checkpoint closes missing executions
without waiting for another worker request. It extends
[result receipts](worker-result-receipts.md) and
[heartbeats and late evidence](worker-heartbeats-and-evidence.md).
Production dispatch remains unfinished. The subsequent
[worker HTTP integration](worker-http.md) connects opted-in routes and shutdown.

## Committed outcomes

The server records a separate finalization when the original execution deadline
has elapsed, the process owning that execution has been replaced, or Start has
already rejected an unstarted offer. This is a server decision about missing work;
it contains no fabricated worker payload or provider completion evidence.

| Execution | Committed effect |
| --- | --- |
| Check | Record `noData`, advance the applicable generation watermark and next scheduled time, and preserve observed health, incident state, counters and recovery verification. |
| Recovery or notification that never received Start permission | Cancel the original queued action with `worker_not_started`. Do not increment attempts or claim an external effect. |
| Recovery or notification that received Start permission | Hold the original action as unknown. Do not issue another grant, mark the executor stopped, reset cooldowns or resolve an operator review. |

Missing-check finalizations distinguish elapsed deadlines from owner loss. Only
an elapsed check deadline is classified as timed out. A changed/deleted monitor
or a newer generation cannot receive an old health transition. Finalization is
recorded against the original execution even when it can no longer update the
current monitor.

The original result and server finalization are mutually exclusive. Their Raft
commit order decides which wins. If an ordinary result wins, a later finalizer
does nothing. If finalization wins, result retries return its original receipt
without storing a replacement outcome or changing health. The result method
still authenticates the original worker and validates input against the original
contract. A separate late-evidence request can retain eligible supplemental
information against the finalization receipt.

Start/reconciliation returns a terminal disposition for finalized checks and
unstarted offers, or unknown for started actions. It includes the retained receipt
and never returns another executable grant. Unassigned records have no worker or
session identity; finalization does not invent one.

## Maintenance ownership and bounds

The existing Store maintenance loop runs the finalizer once per second in a
tagged build with `external_jobs.enabled`. Stopped administrative opens do not
run maintenance. Default builds omit the implementation, types and public methods.

Each pass selects at most 128 due executions, further bounded by the configured
commit batch size. Selection reads metadata from the existing bounded 4,096-record
execution namespace, then releases read locks before submission. It copies no
provider parameters during selection and creates no goroutine per job. Every
command rechecks the current owner epoch and original execution revision when
applied. Time is supplied in the command rather than generated during replay.

The pass has a one-second context budget. Opening, configuration migration,
explicit restore and unavailable leadership prevent admission without permanently
poisoning storage. A later pass observes committed state again. Nontransient write
failures keep storage unavailable through the existing health boundary. Shutdown
joins this maintenance loop before closing storage.

Finalization uses the execution's reserved result capacity. It is limited to
8 KiB and cannot coexist with a stored worker result, so filling the intent
namespace cannot prevent it. Recovery pages include its actual encoded size
within the existing 4 MiB page bound. This is an accounting limit, not a measured
memory or filesystem-allocation limit.

## Controller projection

Committed worker Start, Result and finalization changes now publish scalar hints
through the existing bounded owner change stream. Healthy check results and
missing-check watermarks are included even when the control and incident versions
stay unchanged. A transition that already publishes an incident/control change
does not publish a second hint, and a repeated terminal request publishes none.

The controller reads the current committed monitor before updating Ark components,
action scheduling and the dashboard index. Existing incarnation/revision guards
discard obsolete hints. Worker request handlers and Raft application do not mutate
ECS state. Projection retries do not apply health transitions or create SLO samples.

A fresh projection snapshot includes monitors referenced by retained worker
executions, including healthy monitors with their original control versions.
Rebuilding this additional membership visits at most 4,096 execution records and
copies no protected execution payloads. Existing cursor-gap recovery, immutable
views, page limits and contextual reads remain in use. This connects committed
monitor state to the owner; remote admission and pending-execution tracking still
belong to the unfinished dispatcher.

Aggregate external SLO accounting remains unimplemented. In particular,
`WorkerResultRecord.RecordedAt` is supplied before Raft submission and cannot be
used as the measured result-commit time. Integration must capture confirmation
after commit and preserve missing measurements across interruption. Grant time
is also not a measured remote handler start; queue and execution percentiles
must remain unavailable without the separately defined evidence.

## Persistence and verification

Tagged format 24 adds the finalization command and retained server decision.
Earlier tagged formats 15–23 remain readable. Default builds remain at format 14
and reject external state. An older unsupported binary must not open a store
written by this format; preserve a complete stopped backup before upgrading.

Snapshots freeze finalization records and validate their original identities,
receipt hashes, reason, timestamp, log position, timeout classification and
lifecycle relationship. Log replay and snapshot restoration perform no provider
operation. Action finalization is visible in the incident timeline; missing checks
do not generate a raw-check history stream.

The scoped tests cover queued/offered/started work in all three categories,
owner fencing, duplicate and racing commits, bounded passes, snapshot corruption,
unchanged health/reviews, late evidence and unavailable default-build methods.
Actual SDK/TLS/Raft cases wait for the real maintenance timer, retain late evidence,
then snapshot/restart and reconcile the original grant. Nine additional native
process-kill cases cover before finalization, after its commit and after snapshot,
across checks, recovery and notifications. These tests do not invoke provider
handlers through the complete worker runner.

The [verification record](../review/local-evidence.md#record-98eeb241be203167d57a10ec)
records the exact source inputs and selected commands. Independent review remains
pending. The missing/timeout classification is durable. Monitor-state projection
is now connected as described above; aggregate SLO counters, remote admission and
pending-execution tracking remain part of unfinished dispatch integration.
Terminal retirement, bounded long polling, worker observations, public external configuration
and complete separate-process runner/server qualification also remain open.

The subsequent [external SLO checkpoint](worker-slo.md) adds persisted admitted-check
accounting, missing/timeout counts and receipt-bound post-commit confirmation.
Dispatcher schedule accounting and remote execution timing remain separate gaps.

The later [scheduling checkpoint](worker-scheduling.md) adds requeue of eligible
unstarted work before finalization. It preserves rejection of retired leases and
seals exhausted offers as `offer_limit`; it never requeues a started action.
