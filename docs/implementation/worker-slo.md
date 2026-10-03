# External-check result latency and accounting

Private implementation checkpoint: 28 September 2026. This extends the worker
[result receipt](worker-result-receipts.md),
[finalization](worker-finalization.md) and committed monitor-projection paths.
Production external configuration and dispatch remain unavailable. This record
does not qualify external workers or a release.

## Measurement boundary

New external check admissions freeze the runtime result target in their execution
record. Admission commits one expected check into a bounded rolling aggregate.
Recovery and notification jobs never enter the health-check latency target.

The result command's `RecordedAt` predates submission to Raft. Its value is not a
commit completion measurement. `CommitWorkerResult` instead captures the server
clock immediately after the first result submission confirms success, then commits
a separate observation bound to that execution, configuration revision, owner and
original receipt. The measured duration runs from the original scheduled time to
that confirmation. It includes Raft result-commit delay; it excludes the subsequent
measurement write and return transport to the worker. Confirmation is an observed
upper bound on when the original result became committed, not a remote clock.

Only the first applied result can submit this measurement. Identical result
delivery returns the original receipt without creating another timestamp or
sample. Replaying an observation preserves its first timestamp. A previous owner,
wrong receipt or earlier-than-result timestamp cannot supply a measurement.

The result and measurement are separate commits. A crash, cancellation or failed
measurement write can leave a known outcome with no timing sample. The known
receipt remains valid. A later retry cannot backfill a faster or slower fabricated
confirmation time. This interruption remains visible as missing measurement
coverage and an unmet obligation in the result-attainment denominator.

The protocol does not report measured remote handler start/completion times.
Grant time cannot substitute for handler start. Queue-delay and execution
percentiles, and queue attainment, remain unavailable. Per-monitor execution
latency also remains unavailable. This work does not establish the 250 ms queue
target for external workers.

## Bounded window

The tagged persistence image owns one `external` driver aggregate, reported in the
`pulse` pipeline. It uses sixty five-second buckets and the existing bounded
histogram representation. No per-monitor or per-JobType histogram is allocated.
The local recorder and its worker-pool sizing feedback remain separate.

The scheduled time selects the bucket. The window reports:

- Confirmed timing samples and estimated result p50/p95/p99 in milliseconds.
- An exact count of durations within the frozen result target, independent of
  histogram rounding. The default target remains five seconds.
- Admitted, unfinished checks as pending until the entire scheduled bucket plus
  the result target has elapsed, then overdue. The five-second bucket introduces
  up to five seconds of delay in overdue classification.
- Missing/noData outcomes and completed outcomes without a saved measurement as
  missed obligations. Deadline finalization also increments timeouts.
- Result attainment as target-met checks divided by measured, missed and overdue
  obligations. Pending obligations do not yet enter that denominator.

Missing outcomes never create a latency sample or successful health observation.
A late result cannot overwrite a newer slot. The aggregate persists independently
of the controller's local SLO snapshot. Recovery restores the same counters;
terminal duplicates do not increment them.

A result-target change starts a new window identity. Reverting to an earlier
target still starts another window, including when changes share one committed
Raft batch. Results admitted under an earlier identity cannot contaminate it.
While runtime settings differ from the saved window, the API reports a coverage
gap instead of applying new thresholds to old measurements. Pre-format-25 records
without a frozen target remain explicitly unmeasured.

## Interfaces and storage

Existing SLO/metrics endpoints and the Go SDK expose this report without a new
public HTTP operation. Prometheus exports the actual result counters and available
result quantiles; it omits unavailable queue/remote-execution samples. JSON retains
explicit availability. A missing queue measurement with expected checks has
reason `timing_unavailable`, rather than claiming there were no expected checks.

The report does not become `healthy` while remote timing is unavailable. It uses
the existing insufficient-observation, result-delay and coverage-gap conditions,
or `timing_unavailable` when enough result observations meet their target. Overall
coverage remains incomplete. Existing dashboard consumers receive the same
availability fields; no queue latency or per-monitor execution value is invented.

Tagged storage format 25 adds the aggregate, frozen target/window identity and
receipt-bound observation. Default builds retain format 14 and exclude these
types and command variants. Earlier tagged commands remain readable without
changing their original digest projections. Snapshot creation copies the aggregate
and observation records before background encoding. Validation rejects malformed
counts, histogram overflow, incompatible formats, mismatched identities and
invalid measurement ordering. Confirmation metadata fits within each execution's
reserved result slot and is included in bounded recovery-page accounting.

## Verification and remaining integration

Focused checks cover exact threshold edges, synthetic percentiles, duplicate and
concurrent result delivery, missing measurements, timeouts, action isolation,
target changes within one commit, old slots, pre-format-25 state, corruption and
snapshot isolation. SDK/TLS/Raft cases exercise result reporting through the
authenticated observation API and Prometheus. Native process termination covers
the interval before observation persistence and snapshot recovery; the existing
result/finalization crash scenarios also check retained SLO counters.

Final scoped qualification is recorded in
`bin/verification/worker-slo-2026-09-28/result.json`: 213 selected tests passed on
Go 1.27.1 with race detection and on Go 1.25.0. Each run includes 36 native
process-kill cases and one expected subprocess-helper skip. Default-build checks,
the SLO-window race suite, tagged vet and 286 exclusion checks also passed. The
rebuilt dashboard passed 498 tests and all eight required browser cases with
unchanged tested inputs and assets. Independent review remains required.

This original checkpoint accounts for admitted external checks. Subsequent
[controller dispatch](worker-dispatch.md) and
[schedule accounting](worker-schedule-accounting.md) include ready/preparing
obligations, definite misses and deliberate pauses. Other checkpoints implement
[retirement](worker-check-retention.md), [safe requeue](worker-scheduling.md),
[bounded polling](worker-polling.md), [production worker routes](worker-http.md)
and [operator observations](worker-status.md). Public external configuration and
actual separate worker-runner interoperability remain required. These tests do
not establish fleet capacity, all-platform support or the complete external-worker
SLO contract.
