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

Pulse work uses bounded queues and concurrency reservations per driver within
one global worker budget. Fair dispatch also rotates active monitor flows within
a driver; it does not allocate a permanent queue for every configured monitor.
Checks already selected but waiting for execution remain in pending accounting.

Sizing starts with Erlang C/Allen–Cunneen. Active execution ages, oldest queued
work and configured arrival demand can request bounded additional capacity before
hanging checks finish. A downstream-limited driver does not prevent useful
capacity changes for other checks. Five-second evaluations, the worker bounds,
twofold growth limit and healthy reduction hold still apply. These mechanisms
reduce starvation; they do not guarantee that an overloaded host can meet cadence.

Queue and pool observations expose driver activity, oldest pending age,
saturation, timeouts, missed checks and limiting conditions. Use these alongside
SLO coverage and sample counts. A short regression's maximum cadence gap is not
a five-minute latency percentile or a production capacity claim.

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

## Safe failure diagnostics

The latest failed check reports a `lastFailure` cause code and fixed description
in the management API. A successful observation clears that current failure.
Actions report their own `failure` separately from the original notification
`cause`; incident and action events retain those safe descriptions for the
existing history period. The SDK, CLI descriptions and dashboard expose the
same fields. No raw successful-check history is added.

Diagnostics classify typed deadline, cancellation, DNS, TLS, connection,
permission, HTTP-status and explicit-rejection errors. Other failures receive an
explicit generic description. Provider response bodies, target addresses,
filesystem paths and arbitrary error strings are not diagnostic inputs. Messages
are reviewed constants bounded to 256 bytes, rather than provider text with a
best-effort redaction pattern.

Notifications include the cause committed with their intent. Reusable job
prototypes do not acquire a previous delivery's diagnostic. A2A notifications
receive only the same code and description in their structured context.
External-worker outcomes receive a generic safe cause or confirmed rejection;
the worker's private diagnostic payload is not copied into public state.

A failure explanation does not prove whether an external effect occurred. The
recorded action outcome and investigation hold remain authoritative, including
unknown outcomes after transport interruption or process restart.

## Notification rejection and retry delays

The controller owns notification retries. An HTTP notification is retried only
when the selected provider's response proves a retryable rejection. Generic
webhooks, unrecognized errors, malformed acknowledgements, HTTP 408 and HTTP 5xx
leave the action unknown; they do not authorize another invocation. Acceptance
by a provider remains distinct from observed delivery.

Provider-specific handling follows the
[provider response verification](provider-testing.md).
Documented rate-limit delays are committed with the result and survive restart.
The next attempt preserves the original logical delivery identity and overall
deadline. Its delay is the larger of the provider delay and CPRa's existing
exponential delay; a delay that cannot fit the remaining deadline ends delivery
without scheduling another attempt. New ordinary built-in notifications capture
a 45-second budget at admission and start it at the first committed execution
start. Waiting for a worker or a serialized destination does not consume that
budget. At most three attempts share the resulting absolute deadline. Each HTTP
call is bounded by ten seconds or the remaining budget. An older action keeps
its already recorded deadline; restart, retry and re-offer never renew it.

A pending condition remains visible while delivery is eligible. Recovery cancels
an unsent outage opening. An eligible closure for an actually attempted opening
survives maintenance and keeps the original destination and provider identity.
If another outage is active when that earlier closure is sent, its message
distinguishes the earlier recovery from current health. Exhaustion, permanent
rejection and uncertain outcomes remain visible and do not start another retry
cycle. No driver-level replay layer is added.

## Result batching while idle

An empty result shard waits for its producer notification or cancellation.
A partial batch starts one flush deadline with its first result; later results
do not postpone that deadline. A full batch routes immediately and stops its
timer. Cancellation still drains queued results and flushes the remaining batch.
The configured batch timeout remains the bound for an otherwise idle partial
batch, rather than a periodic wakeup for every empty shard.

The controller's scheduling cadence and worker-demand refresh are unchanged by
this result-path repair. The [verification boundaries](versions.md)
records measured CPU changes and their limited workload scope.
