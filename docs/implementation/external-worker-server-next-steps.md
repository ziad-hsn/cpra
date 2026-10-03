# External-worker server: next implementation boundary

Source audit and subsequent implementation: 2026-09-24. This is a private
implementation handoff, not Ticket 8 completion evidence. The
[JobType persistence and schema foundation](job-types.md) now implements tagged
conditional registration/preparation, encrypted immutable versions, five gated
HTTP operations and terminal configuration receipts. Scoped committed worker
authority, stopped local provisioning and the tagged request-authentication
adapter are also implemented. Raft session ownership and the tagged SDK/worker
session contract are implemented. Tagged format-20
[queued execution admission and preparation](worker-execution-admission.md) now
pin encrypted work to its original configuration and retained contract. A later [format-21 checkpoint](worker-offers-and-start.md) adds bounded offers,
Start/reconciliation and authenticated startup recovery. The later private
[format-22 result checkpoint](worker-result-receipts.md) adds encrypted result
commits, original receipts, conservative lifecycle application and recovery
verification. Private [format 23](worker-heartbeats-and-evidence.md) adds committed
heartbeat decisions and encrypted append-only late evidence. Private
[format 24](worker-finalization.md) adds automatic missing-execution finalization
and preserves its receipt against late reports. Committed worker monitor changes
now use the existing bounded controller projection stream, with healthy-monitor
snapshot membership restored from retained executions. Private [format 25](worker-slo.md)
adds receipt-bound post-commit timing and admitted-check SLO accounting.
Private [format 26](worker-scheduling.md) adds successive check admission and
recovery of eligible unstarted assignments with retained lease fences. Private
[format 27](worker-check-retention.md) adds committed check identities, bounded
terminal retention and explicit expiry responses. It has scoped passing verification;
full execution remains open. The subsequent [polling adapter](worker-polling.md)
honors bounded waits, cancellation and exact replay. Tagged
[runtime preparation](worker-runtime.md) now produces detached authenticated
bindings and inert markers for the entity preparer.

The subsequent [format-29 expiry transition](worker-expiry.md) commits cancellation
of original recovery/notification actions whose deadline passes before assignment
admission. It preserves admitted work and uses no execution capacity. The
subsequent [format-30 scheduling checkpoint](worker-schedule-accounting.md)
counts ready and preparing obligations, including cancellation, shutdown and
recovery coverage. Full separate-process worker/server qualification remains open.

Pinned encrypted configuration references have storage and management validation
support under tagged format 18. The [30 September configuration admission](external-configuration.md)
connects ordinary API, bootstrap and collection validation to retained JobType
contracts when the tagged runtime is enabled. Tagged controller preparation
preserves their inert bindings under the explicit runtime opt-in; bounded dispatch
is connected, with complete candidate qualification and review pending.
See [configuration references](job-types.md#configuration-references) for the
implemented boundary and [session protocol](worker-session-protocol.md) for the
private wire contract and format-19 session implementation. Its HTTP adapter is
originally qualified through a dedicated test mux. The later
[worker HTTP integration](worker-http.md) registers explicitly enabled production
routes and separates new-work shutdown from completion delivery.

The server work belongs to [shipping Ticket 8](dashboard-shipping-plan.md#ticket-8-qualify-the-external-worker-server).
[Ticket 9](dashboard-shipping-plan.md#ticket-9-complete-the-go-management-consumers)
depends on it for tagged SDK completion. The approved contract is the
[optional external-worker protocol](api-management-plan.md#optional-external-worker-protocol)
and [API plan Ticket 8](api-management-plan.md#ticket-8-introduce-the-optional-external-worker-boundary).
The implemented private foundation is one prerequisite; it does not complete
either ticket.

## Observed implementation

| Boundary | Current source and status |
| --- | --- |
| Draft wire contract | [externaljobs.json](../review/source/api/openapi/externaljobs.json.txt) and [operations.json](../review/source/api/openapi/operations.json.txt) define tagged JobType CRUD, worker observations, Poll, Start, Heartbeat, Result and LateEvidence. There is no enrollment operation. |
| Go clients | [services_external.go](../review/source/sdk/go/services_external.go.txt) implements JobType/worker observations; [worker_client_external.go](../review/source/sdk/go/worker_client_external.go.txt) implements the execution protocol. Both require `externaljobs`. |
| Separate worker | [runner.go](../review/source/sdk/go/worker/runner.go.txt) and [journal.go](../review/source/sdk/go/worker/journal.go.txt) implement reservation, committed local journal/outbox state, conservative interrupted-start recovery and joined shutdown. The [worker README](../review/source/sdk/go/worker/README.md.txt) explicitly leaves real-server interoperability unqualified. |
| Private JobType foundation | [Tagged persistence](../review/source/internal/persistence/job_type_external.go.txt), [management preparation](../review/source/internal/management/job_types_external.go.txt) and the [bounded schema evaluator](../review/source/internal/management/job_schema_external.go.txt) implement conditional registration/replacement/deletion, encrypted immutable versions, current-revision CAS, committed operator-policy checks and startup verification. The [foundation contract](job-types.md) records the implemented bounds and focused evidence. The HTTP adapter uses authenticated reservation and commit; these methods grant no execution permission. |
| Production HTTP | [Tagged registration](../review/source/internal/httpserver/extensions_external.go.txt) exposes JobType CRUD and the [worker protocol routes](../review/source/internal/httpserver/worker_routes_external.go.txt) only when explicitly enabled. Worker credentials remain separate from operator permissions. [Completion-aware shutdown](worker-http.md) keeps original results deliverable during controller drain. Bounded controller dispatch is connected; full runner integration remains unqualified. |
| Worker authority | [Tagged worker policy](../review/source/internal/persistence/worker_policy_external.go.txt) commits separate identities, credentials and scoped grants. [Local provisioning](../worker-authentication.md) requires the stopped store and current named operator. The [request adapter](../review/source/internal/httpauth/worker_external.go.txt) authenticates exact worker operations without reading bodies or granting execution. Management reader/operator credentials cannot authenticate as workers. |
| Durable execution | [Queued admission](worker-execution-admission.md) implements bounded encrypted execution intents, exact retry, original endpoint pins and current-owner ready reads. [model.go](../review/source/internal/persistence/model.go.txt) and [local_executor.go](../review/source/internal/persistence/local_executor.go.txt) retain the incident/action and local ownership contracts. Offers and starts are implemented by [format 21](worker-offers-and-start.md); private [format 22](worker-result-receipts.md) commits outcomes and original receipts; [format 23](worker-heartbeats-and-evidence.md) adds heartbeat decisions and append-only late evidence; [format 24](worker-finalization.md) adds automatic finalization. Committed monitor changes now reach the existing owner projection stream. The [format-25 SLO window](worker-slo.md) now records admitted checks and post-commit result confirmation. The [format-26 scheduling contract](worker-scheduling.md) adds safe unstarted requeue and successive check generations. Bounded check retention has scoped passing verification. Bounded remote dispatch, [unadmitted-action expiry](worker-expiry.md) and [pre-admission check accounting](worker-schedule-accounting.md) are connected; sustained fairness and full process integration remain unqualified. |
| Runtime and driver | [Tagged runtime configuration](../review/source/internal/runtimeconfig/extensions_external.go.txt) and [normal startup](../review/source/main_externaljobs.go.txt) carry explicit `external_jobs.enabled` through to the server, requiring authenticated management and Raft. The [driver registry](../review/source/internal/jobs/capabilities.go.txt) still contains no external execution adapter. |

The [public external-configuration boundary](external-configuration.md) now has
24 focused passing tests on each compiler, including Go 1.27.1 race detection.
It covers conditional API changes, file/URL collections, retained-contract pins
and interrupted bootstrap. All 37 refreshed process/crash scenarios pass on both
compilers after changing the 27 ordinary cases to create configuration through the
public SDK. Configured-capacity and sustained-load fixtures retain stopped setup;
their evidence does not establish public configuration admission.
The [five-minute mixed-load test](worker-dispatch.md#sustained-mixed-worker-load)
now passes with complete captured reports on both compilers, including the
Go 1.27.1 race build, after the
[journal-accounting correction](worker-dispatch.md#journal-accounting-under-load).
Both runs preserve original outcome identities and drain without unknown actions,
repeated invocations or unreconciled outcomes. Earlier failures remain recorded.
Final deployment/candidate qualification and independent review remain required.
The current 37-case
[separate-process campaign](worker-dispatch.md#separate-process-verification)
passes on Go 1.27.1 with race detection and Go 1.25.0. It covers normal execution,
interrupted Start/handler/result boundaries, both-process crashes, shutdown,
late evidence, revocation, reserved/live/allocated byte limits and unknown-action
retention at configured byte/record thresholds. The harness uses
the real SDK runner and normal application startup with stopped configuration
fixtures. Those earlier reports precede the public-configuration change; complete
candidate qualification remains open. Outcome
delivery, heartbeats, late evidence and result SLO observations have private
SDK/TLS/Raft coverage. Completed-check retirement now has a private implementation
with scoped passing verification. Full worker-runner integration remains unqualified.

The tagged [worker observation endpoint](worker-status.md) now reads bounded
operator snapshots through normal TLS/Raft startup. It reports committed grants,
original unknown outcomes and last-poll availability without implying remote
process liveness. The SDK exposes pages and a lazy iterator. This observation
path does not open configuration admission or execute handlers.

The [external-check SLO contract](worker-slo.md) captures confirmation after the
original result commit, retains an idempotent measurement identity and counts
missing/timeout outcomes without inventing latency samples. An interrupted
measurement stays missing even when the worker retries the original outcome.
Remote queue and execution timing remain explicitly unavailable. The dispatcher
and schedule-ticket accounting now retain work that never reaches admission.
Adding route wrappers alone would not establish safe execution. Existing
[SDK inventory tests](../review/source/sdk/go/external_test.go.txt) use HTTP fixtures;
[worker tests](../review/source/sdk/go/worker/worker_test.go.txt) exercise the worker library and
process/journal failures. Neither substitutes for an actual worker communicating
with the production server's committed state machine. The separate-process
campaign provides that connection for its verified cases, including record-limit,
revocation, delayed-evidence and uncooperative-shutdown scenarios.
The [capacity campaign](worker-dispatch.md#journal-capacity-through-process-restart)
uses small explicit limits and positive controls against real processes.
Sustained saturation at deployment limits still needs complete-path coverage.
Passing evidence and independent review remain required before qualification.

## Next boundary — worker authority and protocol integration

Integrate the implemented worker authority with configuration/version references,
session ownership and the execution adapter. Keep worker execution unavailable
until the complete protocol passes its acceptance tests. The
[authorization checkpoint](../review/local-evidence.md#record-ca34842fb9b20113939b486f)
covers policy, local administration, authentication and build exclusion only.

Provisioning and session binding are implemented. Assignments and start grants
must now compose with those committed identities:

1. **Provisioned grants — implemented.** Stopped local administration binds a
   stable worker ID/UID to immutable JobType versions, categories and exact
   resource IDs or an explicit whole-kind wildcard. Credential and grant revisions
   are distinct. Revoked IDs remain terminal; restore clears effective grants and
   requires fresh credentials for each retained worker. Scope by stable resource
   ID can cover a future incarnation; execution must separately pin its target UID.
   Current credential authentication is independent of new-work permission:
   future result ingress must accept eligible rotated credentials for the original
   worker UID without requiring its old credential/grant revision. It must still
   validate the original execution and server/restore identity. Polling never
   enrolls a worker or enlarges grants.
2. **Session binding — implemented foundation.** `PollRequest` now includes
   expected server identity, client nonce, session ID and sequence. The
   [session contract](worker-session-protocol.md) commits bounded session ownership
   and exact empty-response replay; the tagged worker journal stores original
   bindings and reconciliation-only requests. Assignment allocation and
   authoritative Start/result dispositions now have adapters registered through
   the explicit runtime opt-in. Bounded controller dispatch is now connected;
   complete worker-runner qualification remains pending.
   `StartRequest` also omits `workerID`: ownership must come from authenticated
   authority and the original stored assignment, never a caller-selected target.

Version retention is selected and implemented. Each JobType ID has a current
descriptor and separate immutable encrypted versions in a dedicated persistence
namespace. Ordinary catalog, bootstrap and controller-recovery paths cannot
insert JobTypes. Metadata-only replacement of the current version authenticates
and compares its original spec, preserving the original version ciphertext.
Changed handler/schema/timeout/protocol/rejection contracts require a new version.
Deletion retains a tombstone and every version; recreation uses a new incarnation,
preserves the category and cannot reuse a retained version name. Historical
versions are not reactivated or evicted to make room. Future configuration and
execution integration must use the new configuration pins and extend
referenced-delete checks to retained executions. Worker grants already prevent
deletion of their pinned current JobType incarnation until explicit grant removal
or worker revocation, even when the worker credential has expired.

The implemented `cpra.schema.v1` evaluator accepts a documented bounded vocabulary
with local references, explicit work limits and cancellation. It rejects remote
references, unsupported keywords and cycles. Management preparation also rejects
built-in driver name collisions.
Schemas and editable metadata are encrypted before submission; the FSM performs
no schema evaluation, key wrapping or handler invocation. See the
[storage and schema contract](job-types.md) for limits and compatibility details.
Custom parameters remain encrypted in configuration records, and retained
reference authentication checks their original schemas. Public configuration
activation and execution admission remain pending; committed monitor-state projection is connected.
Runtime conversion now preserves source/slot pins, mixed notification ordering and
configuration-content identity. The controller selects its external entity
preparer under the explicit runtime opt-in, commits notification source pins, and
excludes remote checks from local pool demand. This projection connection does
not complete dispatch or enable public external configuration.

Next implementation seams are public configuration admission, complete
schedule/pending-work accounting and complete runner/server
tests. Bounded dispatch has the scoped checkpoint linked above.
Wire them through the existing server registration/configuration and
authentication boundaries linked above. Tagged JobType storage uses format 16 for
audited registration; separate worker policy uses format 17, and configuration
references use format 18, sessions use format 19, and queued execution admission
uses format 20. Offers and Start use format 21; committed results and receipts use
format 22; heartbeats and late evidence use format 23; automatic finalization uses
format 24; receipt-bound SLO observations use format 25; safe unstarted requeue and
active check/type admission use format 26; check identity issuance and bounded
receipt retention use format 27. [Explicit notification sources and grants](worker-notifications.md)
use format 28. [Unadmitted-action expiry](worker-expiry.md) uses format 29. Earlier format-15–28 replay remains supported; default builds retain format 14
and reject external state.
Further persisted additions require an explicit
compatibility gate in [catalog_format.go](../review/source/internal/persistence/catalog_format.go.txt),
with historical digest bytes preserved through the frozen projections in
[command_digest_v1.go](../review/source/internal/persistence/command_digest_v1.go.txt). Do not
silently drop stored external resources when an untagged binary opens them.

The [controller dispatch checkpoint](worker-dispatch.md) connects catalog
preparation, original notification pins, local-pool exclusion and bounded
asynchronous assignment admission. Remote checks have their own ready queue.
An uncertain submission retains its identity and ciphertext; scalar completion
observations return to the ECS owner. Definite missed slots and pauses merge into
one external SLO report without inventing remote timing.

Ready/preparing-work accounting now uses original scheduling tickets and
cumulative checkpoints. Configured byte/unknown-action capacity cases now pass
through the real server and worker. Sustained saturation remains required.
Public configuration admission stays closed until those boundaries and
independent review pass.

Acceptance cases for the pending integration:

- Extend the passing SDK/TLS/Raft JobType CRUD, CAS, restart, immutable-version
  retention and operation-receipt cases with pinned configuration references
  and referenced-delete rejection.
- Management credentials cannot execute worker calls; worker credentials cannot
  edit fleet configuration or read CPRa-held provider secrets. Revocation and
  renewal preserve the intended stable-principal distinction.
- Advertised versions/capacity cannot expand committed grants. Malformed,
  oversized or remote-reference schemas fail before a durable mutation; secret
  canaries do not appear in errors or history.
- Untagged builds cannot enable the feature. Tagged-but-disabled builds expose
  no execution capability. The ordinary [all-driver build](../review/source/Makefile.txt) stays
  separate from `externaljobs`; default schemas/types/routes remain excluded.
- Extend the recorded foundation checks with Go 1.25 and release-compiler
  ordinary/race checks, native replay/snapshot evidence and independent review
  for each integrated boundary. Existing JobType HTTP and authorization evidence
  does not establish worker execution interoperability.

## Later execution integration — still required

Runtime preparation and bounded controller dispatch are now connected. The
[dispatch checkpoint](worker-dispatch.md) identifies their tested boundary and
remaining accounting and process-integration requirements. Action expiry now has
the separate [format-29 checkpoint](worker-expiry.md). Earlier
integration requirements below are retained as acceptance obligations:

- `catalogRuntime.prepare` now selects `PrepareMonitorExternal` under explicit
  runtime enablement. Continue verifying that every accepted external slot has
  a matching inert binding and dispatch path. Keep Ark world access on the existing controller owner.
- `DurableSystem.dispatchChecks` and `dispatchActions` now select the aligned
  external slots before local prototypes. The bounded immutable handoff runs
  preparation/commit work outside the owner loop. Preserve local-pool independence
  while completing saturation and separate-process qualification.
- The [schedule-accounting checkpoint](worker-schedule-accounting.md) reconciles
  scheduler expectations with admitted-check SLO accounting. Preserve its original
  obligation through sustained saturation, pause, timeout and process-failure cases.
- [Preparation and observation capacity](worker-admission.md#preparation-and-observation-capacity)
  now have separate bounds so queued actions can expire at full observation
  capacity. Carry that behavior through repeated configuration changes, sustained
  pressure, and the actual server/worker process campaign. The full-store fixture
  is memory-state-machine evidence, not native storage or remote execution proof.
- The [notification source contract](worker-notifications.md) now distinguishes
  endpoint-owned sources from monitor-owned inline slots. Controller preparation
  must preserve that kind and the original color/ordinal in the durable policy.
  Define how configured retry settings map to check and confirmed rejection
  behavior; uncertain side effects still cannot be retried automatically.
- Public graph validation and the frozen collection validation/file profiles
  reject external drivers. Introduce the supported tagged profile explicitly;
  do not change the meaning of retained base-profile uploads or validation claims.
- The [production HTTP integration](worker-http.md) now registers protocol routes,
  cancels polls on admission stop and retains a separate bounded completion
  path. Carry those shutdown boundaries into controller dispatch and actual
  runner tests; registration alone does not establish safe remote execution.

These are production integration requirements, not claims covered by the
runtime-preparation or dedicated worker-handler fixtures.

After the implemented authority and queued-intent foundations, implement a bounded external dispatcher and the
actual assignment → committed start → result receipt chain against the existing
worker module. Remote waiting must not occupy a local worker or create one
blocked goroutine per remote job. Admission must bound assignments, pollers,
queues and bytes globally and per worker/type before allocation.

The approved rules remain mandatory: assignment is not execution permission;
start checks original ownership, expiry, current controls/dependencies and limits;
side-effect starts commit before a grant; ingress derives job identity from the
stored record and validates the original type version. Identical outcomes return
the original retained receipt; conflicting outcomes fail. Started recovery or
notification work becomes held unknown after uncertain loss, never automatic
lease-based redelivery. Missing checks finalize as noData with coverage loss and
watermarks, preserving observed health. Late evidence is append-only and cannot
replace an outcome or operator review. Heartbeats cannot extend the overall
deadline indefinitely or fence an uncooperative remote process.

Real separate-process server/worker tests must cover all three categories and
assert actual handler invocation counts: lost start replies, commit-before-receipt
loss, worker/server restart, expired unstarted offers, stale token/revision or
incarnation, revocation, pause/deletion races, and late evidence after review.
Reuse the scenarios in `TestLostStartNeverInvokesHandlerAndPreservesUnknown`,
`TestLostReceiptRetriesIdenticalOutcome`,
`TestOutboxFullStopsStartAndPreservesPending` and
`TestForcedProcessTerminationRecoversCommittedBoundaries` from the
[worker suite](../review/source/sdk/go/worker/worker_test.go.txt), with the production server
replacing fixture protocol behavior. Verify journal/outbox saturation preserves
pending outcomes and unknown actions, and uncooperative shutdown retains owned
state until the process is stopped.

Dashboard JobType/worker management, executable upload, in-process plugins and
hostile-code sandbox claims remain outside this ticket. SDK publication remains
gated on the full server interoperability and distribution evidence; this
handoff does not relax those gates.
