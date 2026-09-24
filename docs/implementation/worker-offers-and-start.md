# Worker offers and Start authorization

Updated: 2026-09-24. This private Ticket 8 checkpoint connects queued encrypted
intent to committed offers and Start authorization. It does not enable normal
startup routes or controller dispatch. Heartbeats, outcome receipts, late evidence
and check finalization remain required before full worker execution is available.

## Offers and protected parameters

Tagged storage format 21 extends the existing session poll with an exact retained
offer batch. Allocation checks the worker's current credentials, granted resource
IDs and immutable JobType versions against eligible queued records. Advertised
capabilities only narrow existing grants. A repeated poll sequence reuses the
original batch and does not allocate another lease or renew its deadline.

The server permits at most 100 retained non-rejected offers/starts per worker UID,
within the existing 4,096-record global and 256-record per-type limits. Each batch
also respects the advertised free capacity and request limit. Allocation reserves
32 KiB of lifecycle metadata per offered record so reaching the namespace limit
cannot prevent a subsequent Start, rejection or unknown-state update. The wire
budget conservatively accounts for payloads and metadata below the SDK's 4 MiB
response ceiling; the management layer checks the final encoded size too.

An offer lease lasts at most 30 seconds, capped by the original execution deadline.
The assignment sent to the worker carries the **overall execution deadline**;
lease expiry is a separate server-side Start guard. This checkpoint never reoffers
an execution. Rejected and expired records remain retained pending finalization.

`Catalog.WorkerAssignments` authenticates the complete committed poll response
before decrypting parameters, and rechecks it afterward. Controls, source changes,
revocation, expiry or cancellation during projection discard the entire response.
No partially prepared batch is returned. Provider credentials remain worker-local;
the encrypted assignment contains only the configured profile alias and parameters.

## One executable grant

Start derives ownership from the authenticated worker and its exact retained
server/session/execution/revision/lease tuple. A caller cannot choose a different
monitor, endpoint or handler through this request.

The first eligible `begin` commits the start before returning `granted`. Recovery
and notification starts update the original action in that same committed
transition, including existing attempt and cooldown rules. Repeated `begin`
returns `started` or `unknown`, never another executable grant. `reconcile` cannot
start work: it returns `pending` for an eligible unstarted offer or its retained
disposition after a transition.

An ineligible offered lease is **committed as rejected** before that disposition
is returned. A delayed begin cannot later activate that lease. Missing or foreign
leases produce conflicts rather than a false rejection. After a started
execution's owner is lost or its overall deadline expires, reconciliation retains
the original grant and reports `unknown`; it does not authorize another handler.

Remote actions use `ExecutorKind: worker`. Local process completion and restart
fences cannot prove that a remote handler stopped. Reconciliation authenticates
eligible current credentials for the original worker UID, independent of removed
new-work grants or an expired polling session. Restore identity remains distinct
from an ordinary process restart.

## Startup and HTTP boundaries

Catalog startup now streams every retained execution, including expired and
previous-owner records, and authenticates its ciphertext, complete scheduling
identity and original retained parameter schema. It does not require the source
resource to remain current or active. Missing keys, corrupted payloads and invalid
schemas leave readiness unavailable; cancellation permits a later verification
attempt. Recovery invokes no external handler.

The private TLS adapter authenticates before reading bodies. Poll is bounded to
32 concurrent requests globally and one per worker. Start is bounded to 64 globally
and 16 per worker; its body limit is 8 KiB. Both have a ten-second request budget.
Stalled reads retain their deadline through body closure so an unfinished body
cannot indefinitely hold an admission slot. Backend errors and request data are
not echoed in problem responses.

These adapters are exercised through an explicit test mux. Normal application
startup still registers no worker execution routes. Public configuration admission
continues to reject external jobs until the remaining lifecycle is connected.

## Evidence and remaining work

The [local verification record](../../bin/verification/worker-offers-2026-09-24/result.json)
records the tested source, commands and boundaries. The TLS tests use the actual
SDK, encrypted catalog and Raft methods for Poll and Start. They cover exact offer
replay, pending reconciliation, one grant, repeated Start, lost committed-start
replies, guarded parameter projection, authentication and stalled request cleanup.
They do not execute provider handlers or establish complete runner interoperability.

Remaining work includes bounded long polling, heartbeat decisions, authenticated
result commits and exact receipts, append-only late evidence, terminal retention
and expiry processing, health-neutral `noData` accounting, controller dispatch,
public configuration admission, observations and separate-process failure tests.
Provider-account verification, endurance, packaging and publication gates remain
separate. This checkpoint is private and makes no release-readiness claim.
