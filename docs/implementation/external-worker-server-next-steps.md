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
Start/reconciliation and authenticated startup recovery. Full execution remains open.

Pinned encrypted configuration references now have storage and management
validation support under tagged format 18. Ordinary API, bootstrap, collection
activation and runtime paths still reject external execution configurations.
See [configuration references](job-types.md#configuration-references) for the
implemented boundary and [session protocol](worker-session-protocol.md) for the
private wire contract and format-19 session implementation. Its HTTP adapter is
qualified through a dedicated test mux; it is not registered by normal startup.

The server work belongs to [shipping Ticket 8](dashboard-shipping-plan.md#ticket-8--qualify-the-external-worker-server).
[Ticket 9](dashboard-shipping-plan.md#ticket-9--complete-the-go-management-consumers)
depends on it for tagged SDK completion. The approved contract is the
[optional external-worker protocol](api-management-plan.md#optional-external-worker-protocol)
and [API plan Ticket 8](api-management-plan.md#ticket-8--introduce-the-optional-external-worker-boundary).
The implemented private foundation is one prerequisite; it does not complete
either ticket.

## Observed implementation

| Boundary | Current source and status |
| --- | --- |
| Draft wire contract | [externaljobs.json](../../api/openapi/externaljobs.json) and [operations.json](../../api/openapi/operations.json) define tagged JobType CRUD, worker observations, Poll, Start, Heartbeat, Result and LateEvidence. There is no enrollment operation. |
| Go clients | [services_external.go](../../sdk/go/services_external.go) implements JobType/worker observations; [worker_client_external.go](../../sdk/go/worker_client_external.go) implements the execution protocol. Both require `externaljobs`. |
| Separate worker | [runner.go](../../sdk/go/worker/runner.go) and [journal.go](../../sdk/go/worker/journal.go) implement reservation, committed local journal/outbox state, conservative interrupted-start recovery and joined shutdown. The [worker README](../../sdk/go/worker/README.md) explicitly leaves real-server interoperability unqualified. |
| Private JobType foundation | [Tagged persistence](../../internal/persistence/job_type_external.go), [management preparation](../../internal/management/job_types_external.go) and the [bounded schema evaluator](../../internal/management/job_schema_external.go) implement conditional registration/replacement/deletion, encrypted immutable versions, current-revision CAS, committed operator-policy checks and startup verification. The [foundation contract](job-types.md) records the implemented bounds and focused evidence. The HTTP adapter uses authenticated reservation and commit; these methods grant no execution permission. |
| Production HTTP | [Tagged registration](../../internal/httpserver/extensions_external.go) and [handlers](../../internal/httpserver/job_types_external.go) expose List/Create/Get/Replace/Delete only when explicitly enabled. Discovery and self permissions follow actual registration. Worker routes remain absent. |
| Worker authority | [Tagged worker policy](../../internal/persistence/worker_policy_external.go) commits separate identities, credentials and scoped grants. [Local provisioning](../worker-authentication.md) requires the stopped store and current named operator. The [request adapter](../../internal/httpauth/worker_external.go) authenticates exact worker operations without reading bodies or granting execution. Management reader/operator credentials cannot authenticate as workers. |
| Durable execution | [Queued admission](worker-execution-admission.md) implements bounded encrypted execution intents, exact retry, original endpoint pins and current-owner ready reads. [model.go](../../internal/persistence/model.go) and [local_executor.go](../../internal/persistence/local_executor.go) retain the incident/action and local ownership contracts. Offers and starts are implemented by [format 21](worker-offers-and-start.md); result receipts and finalization remain absent. |
| Runtime and driver | [Tagged runtime configuration](../../internal/runtimeconfig/extensions_external.go) and [normal startup](../../main_externaljobs.go) carry explicit `external_jobs.enabled` through to the server, requiring authenticated management and Raft. The [driver registry](../../internal/jobs/capabilities.go) still contains no external execution adapter. |

The next missing end-to-end boundary is authenticated outcome delivery and
finalization, together with public external configuration admission and dispatch.
Adding route wrappers alone would not establish safe execution. Existing
[SDK inventory tests](../../sdk/go/external_test.go) use HTTP fixtures;
[worker tests](../../sdk/go/worker/worker_test.go) exercise the worker library and
process/journal failures. Neither substitutes for an actual worker communicating
with the production server's committed state machine.

## Next boundary — worker authority and protocol integration

Integrate the implemented worker authority with configuration/version references,
session ownership and the execution adapter. Keep worker execution unavailable
until the complete protocol passes its acceptance tests. The
[authorization checkpoint](../../bin/verification/worker-policy-2026-09-24/result.json)
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
   bindings and reconciliation-only requests. Add assignment allocation and
   authoritative Start/result dispositions before registering execution routes.
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
activation and execution projection remain pending.

Next implementation seams include configuration/version references, sessions,
assignment admission and protocol adapters.
Wire them through the existing server registration/configuration and
authentication boundaries linked above. Tagged JobType storage uses format 16 for
audited registration; separate worker policy uses format 17, and configuration
references use format 18, sessions use format 19, and queued execution admission
uses format 20. Offers and Start use format 21. Format-15–20 replay remains supported; default builds retain format
14 and reject external state.
Further persisted additions require an explicit
compatibility gate in [catalog_format.go](../../internal/persistence/catalog_format.go),
with historical digest bytes preserved through the frozen projections in
[command_digest_v1.go](../../internal/persistence/command_digest_v1.go). Do not
silently drop stored external resources when an untagged binary opens them.

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
  no execution capability. The ordinary [all-driver build](../../Makefile) stays
  separate from `externaljobs`; default schemas/types/routes remain excluded.
- Extend the recorded foundation checks with Go 1.25 and release-compiler
  ordinary/race checks, native replay/snapshot evidence and independent review
  for each integrated boundary. Existing JobType HTTP and authorization evidence
  does not establish worker execution interoperability.

## Later execution integration — still required

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
[worker suite](../../sdk/go/worker/worker_test.go), with the production server
replacing fixture protocol behavior. Verify journal/outbox saturation preserves
pending outcomes and unknown actions, and uncooperative shutdown retains owned
state until the process is stopped.

Dashboard JobType/worker management, executable upload, in-process plugins and
hostile-code sandbox claims remain outside this ticket. SDK publication remains
gated on the full server interoperability and distribution evidence; this
handoff does not relax those gates.
