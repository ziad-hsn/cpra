# Measured latency targets

The health-check targets are p99 scheduling-plus-queue delay at most 250 ms and
scheduled-to-committed-result latency at most five seconds, reported over a
rolling five-minute window. A separate execution distribution distinguishes
worker delay from a slow target and delayed result application.

`GET /api/v1/slo`, `cpractl get slo`, and the dashboard System Health page expose
p50, p95, p99, sample counts, exact threshold counts, timeouts, missed cadence
slots and coverage. Histograms use bounded fixed storage by driver, not by
monitor, and five-second time buckets. Report boundaries consequently have
five-second resolution. A histogram percentile is an upper bucket estimate;
threshold attainment is counted directly. Unavailable percentile samples are
null rather than zero. The last per-monitor latency is execution duration and
has a separate `latency_available` flag.

Timeouts and skipped cadence slots remain in the attainment denominator. During
startup, low sample counts or recovery coverage gaps, CPRa cannot declare that
the full window met its targets. Aggregate window state is committed every five
seconds; the interval after its last persisted watermark is identified as a
recovery gap. It is not reconstructed by inventing observations. Per-monitor
historical percentiles and raw successful check records are not retained.

Erlang C with the Allen–Cunneen variability adjustment remains the initial worker
capacity recommendation. Feedback uses a 30-second window evaluated every five
seconds, at least 1,000 observations, and real queued work before reacting to a
queue-delay breach. Growth remains bounded by worker limits and twofold per
adjustment. Shrinking requires 60 healthy seconds, is limited to 10% per
adjustment and cannot cross below the model recommendation. Slow execution and
controller-limited conditions are reported instead of repeatedly increasing
workers in response to delay they cannot remedy.

These are observed operating targets, not a contractual SLA or an unconditional
capacity guarantee. A one-million-monitor or 24-hour claim requires the
corresponding completed campaign evidence described in [validation](validation.md).

Scheduled obligations are recorded before queue admission. Checks still missing a
committed result become overdue once their five-second bucket and five-second
result deadline have elapsed; this introduces at most ten seconds of accounting
lag. Overdue checks count against attainment even if no result ever arrives.
Completed samples stay in the bucket of their scheduled time. A late result
replaces its existing obligation rather than adding a second denominator entry.
Missed cadence slots are counted when the scheduler advances, including while a
previous check is queued or running. The scheduler never dispatches a catch-up
burst of old checks. `pending` identifies obligations still within the accounting
grace; `overdue` identifies unfinished obligations already counted as failures.
