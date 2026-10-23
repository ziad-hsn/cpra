# Management API observations

CPRa provides the v2 read operations below. These
are observations of the running CPRa process and its durable store. Reading them
never queues a health check, performs recovery, opens a second store, or invokes
a notification provider. Discovery and the authenticated access response list
these operations using the same names as the public Go SDK.

| Observation | HTTP route | Go SDK method |
| --- | --- | --- |
| Readiness, controller progress, projection age, storage | `GET /api/v2/state` | `Client.State(ctx)` |
| Shared queue saturation and measured wait | `GET /api/v2/queues` | `Client.Queues.List(ctx, options)` |
| Worker bounds, capacity, throughput totals, sizing condition | `GET /api/v2/pools` | `Client.Pools.List(ctx, options)` |
| Controller system progress and aggregate update durations | `GET /api/v2/systems` | `Client.Systems.List(ctx, options)` |
| Rolling health-check latency and target attainment | `GET /api/v2/slo` | `Client.SLO.Get(ctx)` |
| Safe active runtime settings | `GET /api/v2/config` | `Client.RuntimeConfig(ctx)` |
| Admission readiness | `GET /api/v2/readyz` | `Client.Ready(ctx)` |
| Process liveness | `GET /api/v2/healthz` | `Client.Live(ctx)` |
| Combined typed queue, pool, system, and SLO observations | `GET /api/v2/metrics` | `Client.Metrics(ctx)` |
| Prometheus exposition text | `GET /metrics` | `Client.Prometheus(ctx, writer)` |

Named readers and operators can inspect these observations over the configured
management TLS and origin policy. Observation mode retains its configured
authentication policy. A token that cannot inspect a mutation cannot acquire
write permission by using an observation endpoint. The v1 API remains available
for existing clients, including existing CLI read commands.

## Availability and units

A measurement is meaningful only when its `available` field is true. Known zero
values are emitted explicitly. An unavailable measurement includes a safe
reason; its numeric zero is not a measured result. New fields remain optional
where the contract permits older servers to omit them. The SDK checks actual
schema-required fields independently of Go JSON omission settings.

JSON duration measurements and percentile fields use **milliseconds**. Runtime
settings and rate sample windows use Go duration strings such as `250ms`, `5s`,
and `5m0s`. Rates use jobs per second; utilization and attainment are fractions
from zero to one. Prometheus durations use **seconds** and counts retain their
stated units.

`State.ready` reports initialized, admission-capable controller progress and
usable durable storage. A stale dashboard projection has its own availability,
freshness, and age fields. `controllerAvailable` distinguishes a real owner
progress callback from the compatibility fallback. The fallback requires a fresh
projection and a nonempty fleet unless an empty configuration was explicitly
allowed. Provider outages, unknown action outcomes, and failed target checks do
not on their own make the CPRa process unready or dead.

When admission stops during draining, readiness returns HTTP 503 with a typed
`notReady` problem; state, liveness, and other diagnostic reads remain available.
Storage failures similarly make readiness unavailable while preserving
liveness. Storage error text excludes private backend error values.

`StorageState.appliedIndex` is the state machine's applied log position.
`commitIndex` is retained as a compatibility alias for that same value. Neither
field is independent evidence of a Raft quorum commit, distributed replication,
or failover. `formatVersion` identifies the application snapshot format, not the
node identity or authentication format. Last commit/snapshot duration is
available only after the source records a positive elapsed duration. Filesystem
bytes and a fleet-wide unknown-action count are currently marked unavailable;
use indexed action observations for investigation rather than inferring a zero.

`storage.actions` is a cached action-ledger sample. `observedAt` identifies the
sample time; absence of the object means no sample is available. Active,
ordinary retained, protected and tombstone counts describe current ledger rows.
Their byte fields measure encoded records, not allocated database pages or
reclaimed disk space. Ordinary retained records can still have dependencies that
prevent retirement. `retired` and `retiredBytes` are cumulative logical retirement
totals. `cleanupBlocked` and `cleanupBlockedBytes` cover only the last bounded
selection page, as stated by `cleanupCoverage: last_selection_page`; they are not
a scan of every blocked record. API, CLI and dashboard reads use this cached
sample without scanning the ledger or filesystem.

### Collection file accounting

`storage.collectionGenerations` reports active, retained, pinned and retired
generation counts, the cleanup condition and cached file measurements. The
maintenance worker samples at most one completed generation per step. Reading
state through the API, SDK, CLI or dashboard performs no filesystem scan.

| Field | Meaning |
| --- | --- |
| `usageGenerations` / `usageExpected` | Measured generations versus the current completed-generation catalog. |
| `usageCoverage` | `pending`, `partial`, `complete` or `unavailable` coverage of that catalog. |
| `usageOldestAt` | Oldest file sample contributing to the cached subtotal. |
| `knownApparentBytes` | Sampled file lengths, omitted when unavailable. |
| `knownAllocatedBytes` | Filesystem-reported allocation, omitted when unavailable. |

The subtotal covers `ledger.db` and `generation.json` in catalogued completed
generations. It excludes incomplete generations, unknown files, directory
metadata, Raft, snapshots and event history. `complete` describes coverage of
this catalog; it does not describe all data-directory or host disk usage.
Measurements are incremental and can have different sample times. A failed
refresh removes that generation's cached sample, so partial/unavailable data
cannot silently become a measured zero. Retired samples leave the subtotal.

Linux/macOS read allocated block counts from open file descriptors. Windows
reads `FILE_STANDARD_INFO.AllocationSize` from verified local NTFS handles.
These numbers differ from apparent length and from physical host allocation,
especially with sparse files or virtual disks. Unlinked bytes likewise do not
prove reclaimed space. Cleanup/accounting conditions remain separate from Raft
admission health.

Use `cpractl get state -o json`, `Client.State(ctx)` or the dashboard's runtime
diagnostics to inspect the same measurements. The dashboard preserves measured
zero and labels missing measurements unavailable. The complete native-platform
and storage qualification matrix remains open.

## Shared queue and worker diagnostics

The three pipeline names are `pulse`, `intervention`, and `code`. Queue depth,
capacity, drops, and saturation come directly from each queue. Saturation means
depth reached capacity. Enqueue and dequeue rates describe the recorded sampling
window. A dequeue is not an execution completion, so `completedRateAvailable`
remains false. Average and maximum queue wait are available only after a dequeue
was measured. Oldest pending age is explicit when the queue supplies it. Per-driver
observations include pending and active work, reservations, requested capacity,
oldest pending/execution ages and timeout counts with their availability.

Pools expose actual capacity, target, minimum/maximum, running worker goroutines,
waiting tasks, pending results, submitted/completed totals, scaling-event total,
and the active sizing model and SLO condition. Active executions are counted
separately from running worker goroutines, which can be idle. Requested and
reserved capacity, draining workers and the oldest execution/submission age
remain distinct from queue depth. A numeric model recommendation and explanatory
last-adjustment reason are unavailable where the source does not record them.
Service time requires measured samples, and a last
scale timestamp is emitted only after an observed scaling event.

System observations expose cumulative updates/entities, the latest recorded
progress time, and measured aggregate average/minimum/maximum update duration.
There is no measurement of the last individual update duration. At most 256
registered systems are copied under the metrics lock; sorting and serialization
occur outside it. An overflow or invalid source name produces an unavailable
whole snapshot rather than a silently truncated success. This work is independent
of monitor count and does not query the mutable ECS world.

Queue, pool, and system pages default to 100 items and permit at most 500. Their
continuation cursors retain a frozen bounded observation, tied to the principal,
authority generation, page size, and observation kind. They share the maximum
five-minute retention and 64-global/16-per-principal limits with other management
reads. Under allocation pressure, expired views are reclaimed first, then
completed views, then the least recently used views that have been idle for at
least 30 seconds. In-flight reads are protected. An evicted continuation returns
`410 cursorExpired`; start a new query explicitly instead of combining pages from
different observations. One-page results do not retain a view. A cursor
cannot switch pipelines, principals, or page size. These aggregate observations
do not accept nonempty per-monitor or label filters. Single-object aggregate
routes reject query parameters.

## SLO windows and intentional pauses

SLO reports describe the health-check (`pulse`) pipeline by driver. Each report
keeps scheduling-plus-queue delay, execution duration, and scheduled-to-committed-
result latency separate. The current configuration supplies the measurement
window and thresholds; the defaults remain five minutes, 250 ms, and five seconds.

Percentiles are bounded histogram estimates. Exact threshold counters and
attainment include missed and overdue obligations, and timeouts cannot improve
attainment. Queue and result attainment are distinct. The older `attainment`
field is a compatibility alias for result attainment. Coverage gaps, gap bounds,
insufficient observations, and limiting conditions remain visible. This API
reports measurements and targets; it does not establish an unconditional SLA.

`pausedMonitors` reports the current owner-observed intentionally paused count.
`pausedMonitorSeconds` reports pause exposure within the rolling window. A
committed configuration change is not counted as installed until the owner
projects it. Pause reporting does not rewrite previously missed checks or
obligations. These fields distinguish deliberate disable/snooze time from a
healthy measurement claim.

Prometheus exports bounded labels for pipeline and driver. Rolling samples,
expected/missed/overdue/pending/timeouts, intentional pause exposure, and quantile
estimates are **gauges**, because they may fall as the window advances. Cumulative
system updates/entities and existing pool task totals remain **counters**. The
scrape uses the official [Prometheus text-format escaping and grouping
rules](https://prometheus.io/docs/instrumenting/exposition_formats/), including
literal UTF-8 label values and only the permitted backslash, quote, and line-feed
escapes. Missing percentile/attainment measurements are omitted, while the
availability and coverage series explain the missing values.

Fleet gauges distinguish `cpra_monitors_unavailable`, `cpra_monitors_paused`,
and `cpra_monitors_pending`. Unavailable means local check preparation failed;
paused means a committed control prevents checks; pending means there is no
target observation yet. A previous successful observation does not make an
unavailable monitor healthy. Monitor responses retain that observation as
`lastObservedHealth` and `lastObservedAt`, separately from current `health` and
`availability` (the v1 observation names use underscores).

`cpra_monitors_in_maintenance` uses the controller's current maintenance
assessment. `cpra_monitors_unhealthy_suppressed` and
`cpra_monitors_unhealthy_unsuppressed` distinguish observed outages with and
without ordinary notification suppression. These are current counts, not
cumulative counters; maintenance can overlap an availability category. They
have no monitor, destination or secret labels and do not reconstruct control
state from the scraping client's clock.

## Notification condition details

Monitor details include a bounded `notificationConditions` view. It can show an
ordinary pending notice, an earlier outage's resolution, or a blue review notice
whose closure has no execution attempt yet. A locally closed blue condition does
not prove provider resolution or release the original intervention hold.

Detail reads return `notificationConditionsObservedAt` and
`notificationConditionsSequence` when a matching retained-condition sample is
available. The sequence is a decimal string, not a write revision or pagination
cursor. These reads include at most 100 conditions and 1 MiB of ledger input;
they do not scan lifetime action history. List pages keep their controller
summaries and omit these sample fields. `notificationConditionsTruncated` means
conditions may be omitted or detail coverage is unavailable, including when a
monitor changes during the bounded read. Inspect action records and history for
retained delivery outcomes; do not interpret incomplete coverage as no pending
work.

## Go consumer example

```go
view, err := client.SLO.Get(ctx)
if err != nil {
    return err
}
for _, report := range view.Data.Reports {
    if !report.QueueDelay.Available || !report.QueueAttainment.Available {
        continue // Report unavailable or insufficient observations to the caller.
    }
    fmt.Printf("%s p99 queue delay %.3f ms; attainment %.2f%%\n",
        report.Driver, report.QueueDelay.P99MS,
        100*report.QueueAttainment.Value)
}
```

Use the caller's context and inspect availability before comparing thresholds.
Keep credentials out of logged requests and output. The complete SDK method and
wire-field definitions are generated in [the operation reference](sdk/api-reference.md)
and [wire types](sdk/wire-types.md). Provider-account verification and comparative
capacity qualification remain separate release gates. These observation
interfaces do not establish that either gate has passed.
