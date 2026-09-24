# Check execution and admission

A pulse check's configured `timeout` bounds the entire operation, including
application retries and their delays. Each attempt receives a separate context
limited by the remaining operation budget and the worker's cancellation context.
The first attempt retains the full remaining timeout. A timeout that consumes the
whole budget ends the check; retries do not multiply the configured timeout.

HTTP checks retry transport failures, HTTP 429, and HTTP 5xx responses only for
GET, HEAD, and OPTIONS. Other HTTP methods execute once. This retry policy applies
to pulse checks; recovery and notification actions keep their separate admission
and replay rules. Application attempt counts do not count transport-internal
connection recovery.

Worker pool statistics retain whole-operation service time for capacity sizing:
the worker remains occupied during retry delays. Separate cumulative `attempts`,
`attempt_timeouts`, `attempt_duration`, and `retry_delay` fields expose the retry
cost. They do not replace service time with a smaller successful-attempt latency.
These fields are available in the existing runtime worker-pool JSON statistics;
the typed management Pool response continues to expose its existing contract.

The controller maintains pulse demand as the sum of `1 / interval` for enabled,
unsnoozed monitors. Catalog creates, edits, removals, and control projections
adjust this value on the owner loop. The pool receives this current demand even
when the queue is empty. An explicitly empty workload can shrink to its minimum
capacity after queued work drains. The mean queueing model and percentile feedback
continue to determine capacity within the configured worker limits.

Capacity exhaustion leaves a check waiting for admission with its original
scheduled timestamp. It is not a failed provider observation. Monitor snapshot
JSON reports `check_admission: awaiting_capacity` and a warning while admission is
pending; `pending` means accepted by the queue, without claiming the worker has
started. `queue_rejections` counts failed enqueue attempts, and `missed_checks`
counts subsequent cadence slots that could not start. Both counters are scoped
to the current runtime monitor incarnation and restart from zero on process
restart or monitor recreation. Bounded queue `dropped` statistics count rejected
admissions, including attempts retried later; they are not a count of unique lost
checks.

Missed and overdue scheduled obligations remain in the SLO denominator. Admission
retries preserve the original obligation rather than creating a new one, and
capacity recovery does not erase previously missed cadence slots. Paused monitors
remain covered by the separate intentional-pause measurements.
