# External-worker admission fairness and quota backoff

Private implementation checkpoint: 30 September 2026. This extends the bounded
[controller dispatcher](worker-dispatch.md). It does not enable public external
configuration or qualify the complete production server/worker execution path.

## Selecting committed assignments

New worker polls select the oldest eligible committed execution across all
advertised JobTypes. The committed log position determines age; execution IDs
break ties when several intents share one log entry. A stream of newer checks
cannot overtake a queued recovery or notification merely because its type or ID
sorts first. The same ordering applies within a type.

Selection still requires current worker authority, the original JobType version,
the current server owner, and unexpired configuration/control guards. An
ineligible record does not block eligible records. Per-worker capacity, request
limits, the response byte budget and storage limits remain enforced. A batch
that cannot fit all queued payloads leaves the rest queued; a later poll starts
again from the oldest eligible record. Unknown started actions remain held and
consume their existing capacity.

Private tagged storage format 37 records the selection rule in the poll command.
Already committed commands without that field replay their original type/ID
ordering. Repeating an earlier poll returns its retained answer, including after
upgrading the selection rule. Snapshot and log replay never run handlers.
Default builds reject the optional format.

The selection pass copies only IDs and log positions, bounded by the existing
4,096-record execution limit. It sorts those small entries without cloning
payloads. This guarantees ordering among eligible committed work; it does not
promise equal throughput per type or progress when worker/store capacity is full.

The [queue checkpoint](../review/local-evidence.md#record-e6f9d5f209b055d893d0668b)
records the reproduced starvation, 64-poll continuous-admission regression,
replay tests and forced-process-restart cases. The later
[process capacity campaign](worker-dispatch.md#journal-capacity-through-process-restart)
covers configured journal byte limits and retained unknown actions. Sustained
server/runner saturation at deployment limits, independent review and public
configuration qualification remain separate requirements.

## Sharing preparation capacity

Health checks and incident actions share four preparation goroutines and at most
16 accepted preparation requests. The owner alternates which pipeline gets first
access on successive dispatch ticks. A continuous check backlog can no longer
take every newly freed slot before recovery and notifications are considered.
Continuous action demand likewise cannot take first access on every tick.

This is admission fairness, not a promise that a full store, unavailable worker or
slow provider can make progress. It preserves the existing FIFO ready queues,
original action identities, scheduled times, control guards and execution
deadlines. The owner stops the second pipeline when the first closes admission.
Normal builds keep their existing independent local-pipeline order.

## Confirmed quota rejection

The preparation path now checks committed record, byte and per-type limits before
reserving a check identity or sealing an assignment. This read is a capacity
hint, not execution authorization or a reservation. The Raft commit still checks
every limit atomically, including when multiple preparations pass the read at
the same time. Expired actions use their existing expiry path before this hint.
Held unknown actions continue to occupy per-type capacity; a confirmed terminal
result releases active capacity according to the existing retained-record rules.

The owner delays retries after a confirmed quota rejection. Delays start at
250 milliseconds, double up to five seconds, and apply to the JobType identity
and incarnation across its versions. Rejections from the same outstanding batch
do not multiply the delay. Successful admission clears that type's delay; a
different incarnation or type has separate state. An uncertain commit stays with
the dispatcher's original-identity reconciliation and never enters this retry
policy.

Delay state is owned by the ECS loop and capped at 4,096 entries. Expired entries
are reclaimed when that bound is reached. If every entry is still delayed, a
250-millisecond shared pause prevents rapid type/incarnation churn from growing
the map. These are temporary scheduling delays, not durable grants or provider
retries. Restart does not repeat a started or unknown external operation.

Ready checks retain their original schedule ticket and remain pending/overdue in
the [SLO accounting](worker-schedule-accounting.md). Backoff creates neither a
new observation nor a new latency window.

## Preparation and observation capacity

Preparation and execution observation have separate budgets. The owner retains
at most 4,096 execution observations and 16 accepted preparation requests. A
preparation keeps its slot until its result has either been projected or moved
into an observation slot. Completed preparations waiting for a slot remain in
that same bounded budget; they do not permit another request to enter.

Full observation capacity therefore still permits an expired action to reach
the existing committed cancellation path. Cancellation and confirmed rejection
are processed before the observation rotation. They need no execution record
and cannot wait behind every slow handler. A successful admission waits for an
observation slot without losing its original execution or scheduling identity.
The rotation releases retired or completed observations and then hands those
slots to waiting admissions.

The maximum combined owner tracking is 4,112 entries. Preparation concurrency,
guard-byte accounting, persistent quotas, and the shared 50 ms read budget remain
bounded. Shutdown still joins preparation and accounts for unadmitted schedules;
no extra goroutine, execution grant or provider retry is introduced.

## Evidence boundary

The first Raft-backed regression admitted 32 checks over two ticks while neither
incident action progressed. The corrected test admits both recovery and
notification work while retaining a check backlog. Its reverse case keeps a
check progressing under a continuous action backlog. A separate regression
reproduced immediate retry after quota rejection.

Additional checks cover bounded delays, type isolation, memory bounds, retained
scheduling obligations, capacity rejection before check-ID allocation, and actual
committed per-type saturation. The capacity test fills a type with 256 committed
assignments and distinguishes a confirmed completion from a held unknown action.
The scoped report is
`bin/verification/worker-admission-2026-09-29/result.json`.

The subsequent observation-capacity correction has a separate report at
`bin/verification/worker-observation-2026-09-29/result.json`. Its two initial
regressions failed because full tracking blocked expiry and all new preparation.
Raft-backed owner tests now exercise expiry, quota rejection, committed-check
handoff, and cancellation during shutdown with 4,096 retained scalar observations.
Those observations model already-retired checks; this is not a campaign with
4,096 concurrently executing handlers.

A separate memory-state-machine test commits 4,096 executions across 16 types.
It verifies both capacity preflight and atomic commit rejection for a new type,
then expires recovery, shared-endpoint notification and inline notification
actions. Repeated expiry produces one event per action, preserves the other
monitor fields and leaves all retained execution records unchanged. This is
state-machine capacity evidence, not full-store native crash or storage-load
evidence.

The later reconfiguration regression reproduced duplicate observations when a
metadata-only monitor edit adopted an existing committed check. A delayed older
preparation reply could also retain an obsolete correlation token. The owner now
indexes observations by execution identity and transfers the existing slot to
the current monitor token. Completed observations update that index when their
slot is removed. Reconciliation creates no new execution and invokes no handler.

Raft-backed tests cover repeated renames, reversed reply order, reuse of a moved
observation slot, and 20 edits starting at full scalar-observation capacity.
Every skipped scheduling obligation remains counted, while the original check
stays pending exactly once. Their scoped evidence is recorded in
`bin/verification/worker-reconfiguration-2026-09-29/result.json`.

Sustained global saturation, the full separate-process server/worker failure
campaign and independent review remain open. These tests invoke no provider handler and establish no
throughput or endurance claim. The [remaining release gates](STATUS.md) still
apply.
