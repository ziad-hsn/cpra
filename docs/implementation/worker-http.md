# Worker HTTP routes and shutdown

The private tagged server registers Poll, Start, Heartbeat, Result and
LateEvidence when `external_jobs.enabled` is true. The build tag, explicit runtime
setting, authenticated management and Raft remain separate requirements. Default
and all-built-in-driver builds contain no worker routes or implementation.

This connects the existing committed protocol to normal HTTP startup. Public
external monitor configuration, controller dispatch, worker observations and
complete separate-process runner qualification remain unfinished. These routes
do not make a newly configured external monitor executable yet.

## Authentication and admission

Each protocol route authenticates a current worker credential against committed
worker policy. Management operator, reader and bootstrap credentials cannot act
as workers. Existing exact-path, HTTPS/origin, content-type, body-size and
per-worker/global concurrency checks remain in the handlers.

Poll and Start `begin` use the server's new-work admission barrier and current
controller/storage readiness. A committed offer still requires a separate Start
grant before invocation. Closing new-work admission cancels waiting polls without
waiting for their full 25-second interval.

Result, late evidence, heartbeat and Start `reconcile` use a separate completion
barrier. They require usable committed storage and current worker authority, while
allowing the controller to be draining. Reconciliation cannot produce a new
executable grant. Results and evidence retain their original execution identity,
receipt and conservative unknown-action semantics.

## Shutdown ownership

1. `StopAdmission` rejects new management mutations and worker starts, cancels
   waiting polls, and joins accepted new-work callbacks.
2. The application joins collection work and drains the controller. Original
   worker completion requests can still commit during this period.
3. `StopContext` closes the listener and joins HTTP requests, then fences and joins
   complete worker request lifetimes, including authentication and preparation
   before commit. On deadline expiry it closes connections, but does not treat
   that as completion of a handler still using storage or keys.
4. The application flushes admitted writes after the HTTP join, then closes the
   store and encryption owner. An unsuccessful join/flush retains ownership until
   process exit and reports an error; it does not report a clean shutdown.

The completion barrier counts worker HTTP requests, not remote handler
lifetimes. The supervisor still supplies the hard process deadline. Worker-local
journals retain undelivered outcomes; a response lost during shutdown can be
reconciled using the original receipt after restart.

## Request deadlines

The tagged, enabled server defaults to a 40-second HTTP write timeout and rejects
an explicitly shorter setting. This allows the five-second authentication/body
budget, up to 25 seconds of waiting, bounded commit time and response transmission.
Every operation continues to honor its request context. Ordinary server builds
retain their existing 30-second default. The SDK independently bounds polling by
its requested wait plus five seconds.

## Verification boundary

Production-listener tests use the real TLS socket, middleware, SDK and Raft store.
They cover opt-in, credential separation, the maximum poll wait, cancellation on
drain, known/unknown results for all three categories, receipt replay, late
evidence and a callback surviving a shutdown deadline. Configuration/execution
records are installed through authenticated internal fixture paths; these tests
do not establish controller dispatch or complete `worker.Run` interoperability.
Normal application tests also exercise the runtime route gate and shutdown.

Normal application coverage also provisions a worker against a stopped store,
restarts the same application, authenticates through its production TLS listener,
and reconciles the original committed empty poll response. It does not use an
operator credential as a worker or bypass worker policy in the request path.

Review found that a second shutdown could return successfully while
a request was still preparing and had not reached its commit callback. A real
TLS regression reproduced that premature join. The barrier now covers every
registered worker request from entry through return. Connection closure and a
repeated shutdown cannot release its storage/key ownership early. The interrupted
exclusion run and failing regression are retained separately.

The corrected selected suite passed all 53 top-level tests on Go 1.27.1 with
race detection and on Go 1.25.0, without skips. Tagged vet and all 340
build-exclusion checks passed. All 1,087 recorded Go/module inputs stayed unchanged
during final verification. Earlier related default-build checks passed 17 tests
on each compiler; only the two tagged HTTP files changed afterward. The final
exclusion run verifies ordinary and all-built-in source selection and routes.
Commands, source hashes and raw results are retained in the
[scoped report](../review/local-evidence.md#record-87b68dbb023c8247d2d76eec).

CI now selects the complete worker HTTP test family, runtime preparation and
normal application startup, including result, retention and shutdown cases.
The persistence selection has a ten-minute package budget: the earlier selected
scheduling race run exceeded five minutes. This changes neither its assertions
nor the full-suite completion requirement. Workflow syntax validation passed.

Independent review and the complete release gates remain open. This change does
not alter the wire schema or durable format.
