# Worker session and execution protocol

Status: **partially implemented private contract, 2026-09-24**. Tagged SDK
messages, worker journal format 2, and Raft session ownership are implemented.
The HTTP session adapter is exercised through a dedicated test mux; normal
startup still registers no worker execution routes. [Assignment and Start admission](worker-offers-and-start.md)
now have private TLS/SDK integration. Heartbeat, result and late-evidence server
handlers remain outstanding.
This document refines the [approved worker lifecycle](api-management-plan.md#worker-identity-and-execution-lifecycle)
and [remaining server work](external-worker-server-next-steps.md). It does not
complete shipping Ticket 8 or establish worker/server interoperability.

## Identity and ownership

Keep the following identities distinct. A bearer authenticates a provisioned
principal; none of the request identifiers independently grants permission.

| Field or binding | Meaning |
| --- | --- |
| `workerID`, `workerUID` | Stable provisioned name and immutable principal incarnation, derived from committed authentication. A supplied name must match that authority. |
| Credential and grant revisions | Current policy observations used to fence new work; not the identity of an existing execution. |
| `serverID` | Versioned, domain-separated identity derived from store NodeID and worker-policy epoch. It survives ordinary restart and rotation, and changes on restore. |
| Server owner epoch | Committed process lifetime used to reject starts from old offers after restart. |
| `clientSessionID` | Random nonce generated per worker `Run`, or after an explicit session-expired response; unchanged across ordinary reconnects and lost replies. |
| `sessionID` | Server-issued identifier bound to worker UID, client nonce, server identity, owner epoch, frozen capabilities and expiry. |
| `executionID`, `executionRevision`, `leaseID`, `grantID` | Scheduled execution, pinned configuration identity, exact offered attempt and committed start respectively. |

[Restore preserves NodeID](../../internal/persistence/restore_reset.go), so NodeID
alone cannot be the protocol `serverID`. The worker must pin the expected server
identity and provisioned UID; local provisioning reports `protocolServerID` and
the worker UID. The protocol identity hashes NodeID and worker-policy epoch with
a versioned domain separator; it is not the node ID alone.
Changing configuration must not silently rebind an existing journal.

[Store startup](../../internal/persistence/store.go) already commits a process
session before opening completes. Reuse that ownership pattern where appropriate;
do not treat a changed server epoch as proof that remote I/O stopped. Old remote
handlers may remain active after CPRa restarts and cannot be physically fenced by
a lease, heartbeat failure or server-side state transition.

## Private wire contract

Retain the five existing execution operation IDs and routes in the tagged
[OpenAPI overlay](../../api/openapi/externaljobs.json). Poll establishes or resumes
a session within existing grants; it does not enroll a principal. No new endpoint
is required for registration or reconciliation.

| Message | Required additions or refinements |
| --- | --- |
| `PollRequest` | `serverID`, `clientSessionID`, `sessionID`, positive bounded `pollSequence`. An explicitly empty `sessionID` opens a session at sequence 1. |
| `Assignments` | Echo `serverID`, `workerUID`, `clientSessionID`, `sessionID`, `pollSequence`; include `sessionExpiresAt`. |
| `Assignment` | Add `workerUID`, `sessionID`, `jobTypeUID`; preserve the original execution, lease, target incarnation and deadline. |
| `StartRequest` | Add `serverID`, original `sessionID`, required `mode` (`begin` or `reconcile`). Preserve `executionID`, `executionRevision`, `leaseID`; do not add caller-selected ownership or target fields. |
| `StartResponse` | Echo original execution ID/revision, lease, session, worker UID and server identity. Enforce the dispositions below and their conditional grant/receipt fields. |
| `HeartbeatRequest` | Add `serverID` and original `sessionID`; bind the exact execution and grant. Remove the unused `executionIDs` array from the private draft rather than imply batch renewal. |
| `HeartbeatResponse` | Echo the exact execution, grant, session, worker UID and server identity with `accepted`. |
| `Outcome`, `LateEvidenceRequest` | Include original `serverID` and `workerUID`. Authenticate current worker ownership; do not require a live polling session. |
| `Receipt` | Add worker UID to the existing server/execution/receipt identity. |

Use bounded canonical identifiers and strict request decoding. The SDK must check
echoed identities and disposition-specific fields before returning a successful
observation. Handler output cannot override protocol identity.

The existing `WorkerCapability` and handler registry can retain their exact
JobType ID/version/category key: the [JobType contract](job-types.md) forbids reuse
of a retained version name, including across recreation. The server must resolve
and pin the JobType UID from committed grants and the original execution record.

## Polling and reconnect

Authenticate with [AuthorizeWorker](../../internal/httpauth/worker_external.go)
before reading a bounded body. Resolve scopes from committed policy; advertisements
and capacity never enlarge them. Freeze the capability set for a session.

Allow one outstanding long poll per session. Retain one bounded last-request
identity and last response. An exact repeated sequence returns the same offers,
lease IDs and deadlines; it allocates no additional work and extends no expiry.
A repeated sequence with changed input, an older sequence, or an unexpected jump
fails explicitly. Advance only after the preceding response has been received and
its batch admitted locally. Retry a lost initial reply using the same client nonce,
empty session ID and sequence 1; resolve it to the same unexpired session/result.

A nonempty unknown or expired session ID must not open a replacement. After an
explicit session-expired response, generate a fresh client nonce, open a fresh
session and preserve old journal records for reconciliation. Reusing the expired
nonce does not open a replacement. A new worker process always generates a fresh client
nonce. For the initial one-active-session-per-worker policy, another live nonce
receives a conflict until the prior session expires; there is no implicit takeover.

The implemented session namespace retains at most 1,024 worker sessions, 64
capabilities per session and 16 MiB of accounted state. The idle lifetime is two
minutes; only a new accepted sequence renews it. The HTTP adapter permits one
concurrent poll per worker UID and 32 globally, bounds request bodies to 64 KiB,
and enforces a ten-second session-admission/read deadline. The format-21 extension
returns bounded committed offers immediately. This is not a long-poll dispatcher.

The [offer allocator](worker-offers-and-start.md) bounds retained offers globally
and per worker/type and limits response bytes and advertised capacity. The
dispatcher must still implement the approved 25-second wait. Replay must not extend an offer's
deadline. An expired response is not an executable offer.

## Start and reconciliation

The SDK and worker enforce these dispositions. Server-side Start admission is
implemented in the private adapter; full outcome handling is still required
before execution routes can be registered.

`begin` verifies the original assignment ownership, current owner epoch, policy,
target UID, execution revision, JobType UID/version, controls, dependencies,
attempt limits and deadline in the authoritative admission path. Commit a
side-effect start before publishing any executable grant. The FSM performs no
provider I/O, key resolution or handler invocation.

| Disposition | Meaning and worker behavior |
| --- | --- |
| `granted` | Only the original live `begin` request may receive executable permission after a confirmed start commit. Persist the local started marker before invoking. |
| `started` | The exact attempt already started. Return its original grant identity for result reconciliation, never permission to invoke. |
| `pending` | The previous start may still commit, or the offer is not authoritatively finalized. Preserve the reservation and do not invoke. |
| `unknown` | A started action is held unknown. Preserve the original identity and available receipt; do not retry the action. |
| `terminal` | Return the retained final receipt without an executable grant. |
| `rejected` | The exact attempt is authoritatively fenced against starting; a reserved journal record can be released. |

The SDK must never automatically repeat `begin`. A lost reply can arrive after
reconciliation has begun: the runner accepts execution permission only for the
original live request while it still owns that execution path. A duplicate begin
returns `started`, `unknown` or `terminal`, never another executable grant.

`reconcile` never creates a start and never invokes a handler. Mere absence or an
unstarted observation cannot establish `rejected`: a timed-out command may still
commit. Keep `pending` until an authoritative commit/finalization or fully recovered
old-owner fence establishes the outcome. In particular, a delayed grant reply
must not escape local cancellation or revive a reconciled journal entry.

The [runner recovery path](../../sdk/go/worker/runner.go) persists and sends only
`reconcile` requests. It preserves pending reservations and never invokes an
interrupted handler. Only the original live path sends `begin`.

## Recovery and credential changes

New assignments and starts require current scope authorization. Result, receipt
and late-evidence paths instead require eligible current credentials for the
**original worker UID**, original server identity and exact execution/grant.
Do not require the old credential/grant revision or a current polling session to
deliver an existing outbox. This preserves the distinction already stated by
[WorkerAuthority](../../internal/persistence/worker_authority_external.go).

On ordinary server restart, invalidate unstarted offers, finish recovery before
admission, and hold uncertain committed actions as unknown. Old remote work may
continue; cooperative cancellation is not physical fencing. Results stay attached
to the original target and type. They cannot silently overwrite an unknown outcome,
operator review or finalized check generation; later evidence remains append-only.

On worker restart, retain original session fields in encrypted execution records
and use reconciliation only. Resend persisted outcomes unchanged until a durable
receipt. Token rotation for the same UID permits that delivery; revoked or expired
credentials remain invalid. Grant removal blocks new work without erasing original
ownership. Restore changes `serverID` and requires explicit operator reconciliation;
reject and preserve the old journal instead of adopting the restored server.

## Compatibility and implementation boundary

The current [JobType validator](../../internal/management/job_types_external.go)
and [stored version validator](../../internal/persistence/job_type_external.go)
accept only `ProtocolVersion == "1"`. There is no registered server execution
protocol to preserve yet. Proposed decision: finish this unpublished protocol-1
contract before qualification, regenerate both tagged clients together, and reject
older requests missing the required fields. Do not silently accept a second shape
or relabel immutable stored JobType versions. Any later published incompatible
protocol requires an explicit new protocol version and compatibility policy.

The separate [worker journal](../../sdk/go/worker/journal.go) uses format 2.
Worker UID is bound into authenticated metadata/AAD, and replay records retain
original session identities with reconciliation-only Start requests. Format-1
records fail explicitly without deleting pending records or replacing a key.
No migration is provided. Server session state uses tagged format 19 and retains
the existing format 15–18 readers and frozen command digests. Base builds retain
their separate format boundary.

Implementation affects the overlay/generated tagged schema and transport,
`worker_client_external.go`, worker `types.go`, `runner.go`, `journal.go`, tagged
server handlers, committed execution state and their tests. Keep default builds
and all-built-in-driver builds free of these types, routes and dependencies.
Register and advertise execution only after the server and worker changes pass
the actual interoperability gates.

## Required negative and process tests

- Worker, session, lease, JobType or target-incarnation substitution; authentication
  failure before body reads; capability advertisement outside committed grants.
- Lost first/subsequent Poll replies: bounded identical replay, changed-body
  conflict, stale/skipped sequence rejection and no extra offers or renewed lease.
- Lost/delayed Start replies: no automatic begin retry, no handler after local
  reconciliation/cancellation, and no grant from duplicate or terminal starts.
- Delayed commit during reconciliation: absence stays pending until fenced;
  `reconcile` creates zero starts and invokes zero handlers.
- Worker/server termination around offer, start commit, local marker, external
  effect, outcome persistence and receipt; assert actual invocation counts.
- Rotated token delivering the original UID's outbox; old/revoked token denied;
  removed grants preventing new starts; restored server rejecting old records.
- Pause/recreation/configuration changes between Poll and Start; accepted old
  outcomes attributed only to their original execution.
- Poll/session/outbox saturation, heartbeat expiry, conflicting outcomes and late
  evidence after review; no discarded unknown action or physical-fencing claim.
- Journal identity/format rejection, snapshot/log replay without execution,
  tagged/default exclusion and Go 1.25/release-compiler race qualification.
