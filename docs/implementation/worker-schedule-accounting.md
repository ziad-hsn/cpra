# External-check scheduling accounting

Private implementation checkpoint: 29 September 2026. Tagged storage format 30
counts external health checks from the time the controller schedules them,
including work waiting in the ready queue or preparing an assignment. It does
not enable public external configuration or qualify complete worker execution.

## One obligation across admission

The owner issues a schedule ticket containing its incarnation, the original
five-second bucket and a monotonically increasing sequence within that bucket.
The ready entry retains this ticket, and preparation copies it into the sealed
assignment. A confirmed retry keeps the original ticket and scheduled time.

Each bucket keeps a cumulative issued count. Admission advances that same count
to at least the ticket's sequence; it does not add a second expectation. A later
aggregate checkpoint takes the maximum of the counters it captured and those
already committed. This preserves the same denominator when preparations finish
out of order, a checkpoint is replayed, or an admission reply is delayed or lost.
Existing retained assignments reject reuse of their schedule ticket.

The authenticated assignment identity and retained execution digest include the
ticket. Changing or removing it invalidates authentication. Commands and snapshots
that contain scheduling counters require format 30. Earlier supported records
retain their original digest and format rules; default builds remain at format 14
and contain none of the scheduling extension's exported APIs.

## Waiting, cancellation and shutdown

API reads merge a detached copy of the live issued counters with the committed
window. Ready and preparing checks are immediately visible as pending. Once a
whole bucket is older than the result target, unfinished obligations count as
overdue and reduce attainment. This merge performs no storage write or provider
operation and uses 60 aggregate buckets rather than one histogram per monitor.

A definitively unadmitted check becomes missed when its execution window expires,
configuration or controls cancel its ready entry, or shutdown abandons it. This
changes measurement accounting, not monitor health. It creates no failed provider
result, intervention attempt, notification or fabricated latency sample.

Uncertain submissions retain their original identity until the existing FIFO
barrier and authoritative read settle the outcome. Shutdown joins the dispatcher
before accounting for unprocessed requests and ready work. It preserves committed
assignments for the existing worker lifecycle. Repeated finalization cannot add
another missed obligation.

If reconciliation adopts an older assignment, the newly due slot is missed; it
does not replace or duplicate the older assignment's measurement. Deliberate pause
exposure and other missed cadence slots remain separate observations in the same
external report. Recovery and notification actions never enter this health-check
latency window.

## Persistence and recovery coverage

The controller checkpoints cumulative schedule counters alongside its normal SLO
aggregate using its existing uncertain-write reconciliation. A committed
assignment can advance the durable counters between checkpoints. Frozen snapshots
retain the resulting aggregate independently of later updates or receipt pruning.

A new process issues counters under a new owner identity. Previously committed
obligations remain in their original buckets; old-owner checkpoints are rejected.
The normal recovery coverage gap remains explicit. Schedules issued after the
last committed checkpoint and never included by an admission can be lost in a
process crash; the API must not claim complete measurement of that interval.
There is no inferred successful check or invented count for unobserved work.

Remote handler-start and execution-duration timing remain unavailable in the
current worker protocol. The available end-to-end distribution measures original
scheduling through committed-result confirmation. Its percentiles remain
estimates, while threshold attainment uses the counted obligations.

## Verification boundary

The checkpoint covers out-of-order admissions and checkpoints, lost replies,
queue saturation, original-input retry, cancellation, shutdown, authenticated
ticket binding, snapshot freezing and corruption rejection. Storage-process tests
kill the owner before a checkpoint, after commit and after snapshot, then reopen
the same directory. They do not run the complete server and worker as separate
production processes.

The [scoped verification report](../review/local-evidence.md#record-2b3404e604e1337af5d0108a)
records 281 selected release-compiler race tests, 71 Go 1.25 checks, 93 default
persistence/controller/SLO tests, default/tagged vet and 260 negative compilation
checks covering 130 excluded symbols. Each compiler runs the three new native
storage-process kill cases. The report retains failed attempts and source hashes.
Sustained admission fairness,
the production server/worker failure campaign, public configuration admission,
independent review and the other [release gates](STATUS.md) remain open.
