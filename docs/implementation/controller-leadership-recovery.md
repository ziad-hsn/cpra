# Controller recovery after temporary Raft leadership loss

Status: implemented and independently reviewed locally, 2026-09-23. The controller
now retains one unresolved owner write and a bounded received-result suffix,
reconciles through a FIFO barrier, and retries returned executor markers without
reinvoking providers. Context-aware lock/read boundaries have ordinary, race and
Go 1.25 coverage. The verification checkpoint is
`bin/verification/controller-recovery-2026-09-23/result.json`.
This retains the existing single ECS owner, bounded worker pools, Raft log, and
external-action grants. Startup failure still requires a restart.

## Executed recovery evidence

`owner_recovery_test.go` exercises a real state machine with deliberately lost
submission replies before and after commit. It checks original command bytes,
an oversized received batch's suffix, one SLO observation per confirmed check,
notification-successor identity, frozen SLO checkpoints, stopped draining and
permanent-failure retention. `owner_projection_recovery_test.go` uses the real
encrypted management catalog to check preparation ownership/acknowledgement,
stale configure guards, removal/recreation and snooze-expiry replay. Those tests
drive owner handoffs directly, not background operation-receipt completion.

`controller_recovery_external_test.go` runs an HTTP provider with a live controller
and actual higher-term Raft vote request. The provider returns during follower
state; readiness closes, no target failure is invented, and the same process
recovers one durable check, one SLO sample and an `up` dashboard observation.
The startup variant verifies that an incomplete initialization does not recover
readiness merely because Raft is elected again. `local_executor_recovery_test.go`
separately covers actual provider return during leadership loss, retained claims,
the original completion timestamp, bounded returned-marker retries and no second
invocation. These campaigns pass under the race detector; exact commands and the
final context-aware qualification are recorded in the implementation progress log.

The final review also exercises malformed expiry/configure replies after a real
commit: no local projection, popped-deadline release or preparation acknowledgement
occurs from an empty or unrelated response. All 23 projection/malformed-response
scenarios passed under race detection. The complete affected application,
management, controller and server suites passed after the contextual read changes.
The optional-driver race selection passed (systems 3.350 s, persistence 36.970 s),
including the actual Raft controller pair, local executor recovery, contextual
reads and format-10 result regressions. The 402 recorded production/build input
hashes remained unchanged during that final matrix.

The failure schedules below remain the acceptance matrix. Component tests and a
successful election test do not imply that every combination has been executed.
In particular, there is no injected election exactly between mapper installation
and its observed-generation acknowledgement, nor a full process-kill campaign
combining every controller recovery phase with collection activation.

## Failure path being replaced

The previous `internal/controller/systems/durable_system.go` treated any unsuccessful
`Store.Status().Ready` check as a reason to discard results. `fail` permanently
called `Store.MarkUnavailable`, set `lastError`, and closed admission. Thus a
short follower interval can either lose returned observations or permanently
poison otherwise recoverable storage. Clearing those flags is unsafe: local
batches, popped deadlines, and prepared projections may already have been lost,
and a rejected submission reply does not prove that its command did not commit.

`commitResults` also deleted pending check/action ownership before submission.
`drain` could receive a result slice larger than its remaining command budget;
its unprocessed suffix existed only in a local variable. Both now remain owned
across an uncertain commit before recovery resumes scheduling.

## Submission boundaries and retained ownership

| Boundary | Current source | State that must survive uncertainty |
| --- | --- | --- |
| Pulse results, action results, late evidence | `systems/durable_system.go`: `drain`, `commitResults` | Original result slice and unread suffix; frozen commands with original times, generations, action IDs, guards, control revisions, outcome classification and maintenance decisions; per-result local projection/accounting disposition. |
| Periodic SLO checkpoint | `systems/durable_system.go`: `persistSLO` | The admitted `slo.State` and timestamp until resolved. Do not regenerate the snapshot as a retry or advance `lastSLO` before confirmation. Later checkpoints may follow resolution. |
| Catalog installation | `systems/catalog_runtime.go`: `startCatalogProjection`, `prepareCatalogWrite`, `finishCatalogWrite` | Original projection, `PreparedMonitor`, frozen configure command/guard, and `p.done` acknowledgement. Preparation remains owned while resolution needs it; an installed flag separates mapper installation from its observation acknowledgement. |
| Catalog removal | `systems/catalog_runtime.go`: `removeCatalogProjection` | Original removed catalog record and exact remove command. Remove ECS membership and acknowledge only after committed state is reconciled. |
| Snooze expiry | `systems/control_runtime.go`: `expireSnoozes` | Popped entities/deadlines and the original expiry command IDs, expected revisions, `Until`, and observation time. Recovery must not allocate replacement UUIDs or lose scheduler membership. |
| External-action start | `systems/durable_system.go`: `dispatchActions`; `persistence/local_executor.go`: `BeginLocalAction` | The process-local `LocalExecution` handle and original action/session identity, even when start returns an error. The provider must not run without a confirmed grant. |
| Executor completion | `jobs/execution.go`: `Dispatch.Execute`, `finalizeInvocation`; `persistence/local_executor.go`: `Finish` | Returned-handle ownership, separate from the provider result. `Finish` may be retried for the same handle; provider execution may not be retried. |
| Startup configuration/reconciliation | `DurableSystem.Load`, `LoadCatalog`; `Store.Reconcile` | Startup may remain fail-closed and restart-required in the first bounded implementation. Do not claim runtime recovery also covers partially loaded startup. |
| Operation-receipt completion | `systems/catalog_runtime.go`: `completeReceipts`; `management/operations.go`: `CompleteOperation` | Existing stable receipt ID and CAS. This background reconciliation already observes pending receipts again; it must never acknowledge an uninstalled owner projection. |

`MarkMonitorObservedContext` is process-local acknowledgement, not a durable write.
Its success must still follow actual installation of the exact prepared jobs and
schedules. The contextual control snapshot, change cursor, catalog guard, action
and monitor reads acquire cancellable Store-to-FSM locks. They check permanent
storage health before classifying leadership under those same locks. Check
admission also checks leadership immediately before invocation. A withheld check
retains its original scheduled obligation without inventing a target failure or
a second SLO sample.

## Smallest owner state change

The separate atomic admission state represents temporary recovery. Shutdown and
permanent failure remain irreversible; recovery never clears `stopping` or `lastError`.
The effective states are running, recovering, stopping, and permanently failed.

The owner retains at most one unresolved write envelope, plus its already
received result suffix. A typed pending record carries its source, immutable
commands, original result metadata or prepared projection, and completion stage.
It must hold no borrowed Ark component pointers. No second owner write starts
until that envelope is resolved. Bound retained results by the existing receive
batch size plus one producer batch; do not accumulate an outage-length list.
Leave further results in the existing bounded channels and worker queues so
backpressure remains explicit.

On a classified leadership error, mark recovery admission unavailable without
calling `Store.MarkUnavailable`. On readiness loss observed before a submission,
obtain the recorded availability cause rather than treating the boolean as an
error classifier. Permanent store/FSM/catalog integrity failures take precedence
over follower state. Generic unavailable, shutdown, malformed response, and
unclassified commit uncertainty remain fail-closed in this first implementation.

A recovery tick performs bounded work:

1. Check permanent health and shutdown intent; wait at a bounded retry cadence
   while leadership is unavailable. Do not sleep indefinitely inside an ECS
   update or create additional outstanding submission goroutines.
2. Successfully `Store.Flush` before interpreting an uncertain write's absence.
   The barrier must remain unresolved if its own reply is uncertain. Use an
   explicit recovery deadline per attempt; preserve the pending record on expiry.
3. Reconcile the original command identities against committed state. Retry only
   the exact frozen commands whose semantics permit it. No refreshed CAS,
   regenerated UUID, new provider grant, or replacement preparation is a retry.
4. Finish the pending envelope's local projection/accounting exactly once, then
   clear its ownership. Continue bounded result draining, reconcile current
   catalog/control views, and resolve returned-executor markers before reopening
   admission. A newer catalog/control fence may legitimately reject old work.

`AdmissionReady`, `Controller.Ready`, and both dispatch closures must include the
recovery state. Check current store leadership immediately before provider
admission as well: the owner flag alone cannot close the race before its next
tick. Already-started providers retain their original ownership. A storage
interruption must not create a synthetic unhealthy target observation.

## Replay and observation rules

`Store.Flush` (`internal/persistence/flush.go`) joins the FIFO submission prefix.
It does not stop later HTTP writes or establish whether an external action ran.
Every follow-up decision therefore retains its original guard and identity.

Existing lifecycle transitions make repeated pulse generations and terminal
action results no-ops (`internal/persistence/transitions.go`). Replaying a command
that already committed can return an empty `persistence.Result`; absence of a
returned monitor is not evidence that the original result was rejected.

For pulse results, reconcile the original monitor incarnation, execution
revision, generation, and retained outcome/timestamp before projecting a duplicate
commit. With this owner paused, another pulse for that monitor cannot advance its
generation. If identity or evidence no longer matches, classify the result as
superseded or unresolved; never project it onto the replacement monitor. An
accepted frozen pulse gets one local SLO observation, using confirmed response
or reconciliation time conservatively for scheduled-to-result latency. A rejected
or unresolved obligation stays in the denominator. Never manufacture a low
latency from the command's pre-submit timestamp.

For action results, exact replay is safe at the state-machine boundary but the
returned monitor may again be absent. A retryable notification failure can even
remove the original action and create its deterministic successor. Reconcile
committed action/history evidence or refresh the current matching monitor after
the confirmed replay; do not infer an uncommitted action from missing membership.
Late evidence remains attached to the original held action. It must not clear
`Unknown`, transfer incident ownership, or authorize another invocation.

Configure/remove retries retain their original dependency guards. Catalog writes
from other callers can supersede them during recovery. A stale guard must lead to
fresh catalog reconciliation after the original outcome is settled, not to
rebasing the frozen configure command. Owner-observation acknowledgements follow
installation, including when the first configure committed but its reply was
lost.

Expiry controls are CAS operations, not success-returning idempotent writes.
An exact duplicate can return `ErrControlConflict` after its first commit.
Reconcile the original operation receipt and control revision before deciding
whether to project, requeue the unchanged original deadline, or accept a newer
operator control. Preserve the first expiry UUID across all ambiguous replies.

The recovery attempt and each contextual owner read stage use 250 ms deadlines;
initial write submission uses five seconds. These bound waiting for locks and
storage acknowledgement, not every CPU instruction in an owner tick. Record
cloning is not preemptible. Result and expiry completion retain a per-item cursor,
so cancellation on a later read cannot account an earlier item twice. A catalog
retry after installation only retries the observation acknowledgement.

## External-action completion and shutdown

A `BeginLocalAction` error may accompany a committed `Started` marker. Its handle
already fences the local invocation, and `Dispatch` correctly withholds provider
execution when authorization fails. Resolve the submission prefix first. A
proven ungranted, still-queued original action may later receive ordinary guarded
admission; an ambiguous or committed start must not cause automatic provider
replay. Retain conservative unknown disposition where appropriate.

`LocalExecution.Finish` is explicitly retryable and records only that the handler
returned. Preserve `Result.Err` as the provider outcome and treat
`FinalizationErr` as separate storage evidence. Classified temporary completion-marker
failure enters recovery without permanently canceling catalog reconciliation.
The retained handles retry completion without executing the provider again.
`RetryReturnedExecutions` processes at most 128 returned handles per call and
reports active handlers separately from returned markers awaiting confirmation.
Running handlers do not alone prevent readiness recovery; shutdown still joins
both categories. Cancellation is not proof that a handler returned.

`Finalize` participates in the same retained-write recovery and preserves
buffered results while storage is unavailable. `BeginStop` still closes
new admission permanently. Continue resolving already-admitted results and
returned markers while worker pools drain. Bounded buffers can apply backpressure
during an outage, so shutdown cannot wait for workers while abandoning their
result consumers. If the caller's shutdown deadline expires, report incomplete
drain and retain dependency ownership; do not report success or unlock storage.
Existing `Store.Close` rejection of outstanding local executors remains required.
An explicit process exit/restart may preserve unknown started actions and an
observation coverage gap, but cannot be described as a clean drain.

## Required regressions

Use both deterministic lost-reply fixtures and an actual single-node higher-term
`RequestVote` follower/re-election cycle. Run the controller with live workers;
store-only and collection-coordinator tests do not prove this behavior.

- Follower before pulse submission, and pulse committed with reply lost: one
  provider invocation, one durable check/history outcome, one SLO sample, original
  schedule retained, readiness false during recovery and true afterward.
- Failure in the first command slice of an oversized received result batch:
  preserve the suffix and fairness across pulse/intervention/code channels.
  Sustained outage retains bounded memory and applies backpressure.
- Configure/remove committed with reply lost: retain/close `PreparedMonitor`
  exactly once, install/acknowledge the matching projection exactly once, and
  never acknowledge a superseded dependency guard.
- Snooze expiry committed with reply lost: one original operation UUID and expiry
  event; concurrent newer snooze/disable/recreation wins; no popped deadline lost.
- Start committed with reply lost: zero provider invocations without a confirmed
  grant; retain and finish its handle; never issue a replacement external action.
- Provider success or definite rejection followed by completion-marker leadership
  loss: provider executes once, its real outcome survives, and marker retry cannot
  rerun it. Add a genuinely ambiguous transport outcome and preserve `Unknown`.
- Retryable notification result committed with reply lost: no second successor
  action or duplicate notification invocation. Include late results after monitor
  removal/recreation and action review.
- Shutdown during each recovery phase, including a full result queue and an
  uncooperative provider: bounded caller wait, no premature dependency close,
  clean completion only after results and executor markers resolve.
- Real FSM/history corruption, failed catalog verification, authentication reset,
  generic uncertain barriers, and stale guards: remain unavailable; temporary
  recovery must never clear a permanent failure.

The pending-write state, contextual owner reads, catalog/control recovery and
returned-executor retry are implemented together. They introduce no public write
contract or additional Raft command format. The independent format-10 collection
result work has its own compatibility and verification boundary. The scenarios
listed here remain a qualification matrix, not a claim that every combined crash
schedule, platform or shipping gate has passed.
