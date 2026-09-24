# External execution admission

Updated: 2026-09-24. Private implementation checkpoint for
[shipping Ticket 8](dashboard-shipping-plan.md#ticket-8--qualify-the-external-worker-server).
This implements encrypted queued intent and inert runtime preparation. Assignment
offers and Start grants are extended by the later [format-21 checkpoint](worker-offers-and-start.md).
Receipts and controller dispatch remain incomplete. Normal
startup does not register worker execution routes or accept external monitor
configuration.

## Committed intent

Tagged storage format 20 adds `WorkerExecutionIntent` and `WorkerExecutionRecord`.
An intent identifies its original monitor incarnation, execution configuration,
control revision, complete catalog dependency closure, immutable JobType version,
scheduled time and deadline. A check identifies its generation; recovery and
notification work identify an existing queued action. Notification admission also
checks the endpoint's ID, incarnation and configuration revision at the original
color/ordinal in `Policy.WorkerNotificationSources`. Built-in endpoint slots remain
empty in that parallel array. Inline external notification configuration is not
eligible for the current endpoint-scoped worker grants.

`CommitWorkerExecution` checks committed controls, dependencies, action limits and
the process owner before adding an intent. It does not transition an action to
started. While current admission guards remain satisfied, exact retries return the
original encrypted record; changed content under the same execution ID conflicts.
After eligibility changes, lookup still returns the original record even when
admission rejects a retry. The command digest uses a frozen versioned
projection. Time and identities are supplied before state-machine application.

| Bound | Initial value |
| --- | --- |
| Retained queued executions | 4,096 |
| Accounted namespace encoding | 256 MiB |
| Executions per JobType incarnation, across versions | 256 |
| Concurrent queued checks per monitor incarnation | 1 |
| Queued execution records per action identity | 1 |
| Assignment plaintext | 128 KiB, including its identity wrapper |
| Ready page | At most 256 records and 4 MiB of accounted encoding |
| Notification source positions | At most 10,000 per policy |

Ready lookup uses an index for the exact JobType version and checks current owner,
deadline, controls and dependencies again. It returns detached ciphertext. Expired
or old-owner records remain inspectable and continue consuming their reservation;
they are not silently reactivated or deleted to make room. Terminalization and
check finalization must be implemented before dispatch opens.

Snapshots include the encrypted namespace and exact byte accounting. Retained
execution references protect their JobType incarnation against deletion. Default
builds retain format 14 and explicitly reject external state; tagged readers retain
formats 15–19. Policy-only configuration with external endpoint pins also advances
the snapshot format to 20.

## Private preparation

`Catalog.PrepareWorkerExecution` obtains parameters from the authenticated source
resource and its original JobType contract. It never resolves CPRa Credential
resources for a worker. A worker-local credential-profile alias stays inside the
encrypted payload. The retained JobType timeout bounds the overall execution
window; absent an explicit timeout, the maximum preparation window is 24 hours.
The controller may select a shorter window.

Encryption authenticates the store, execution and monitor identity. The protected
payload additionally binds the entire scheduling/dependency projection.
`OpenWorkerExecution` verifies that identity and the retained parameter schema
before returning an inert descriptor. Neither preparation nor opening is permission
to invoke a handler; committed Start authorization remains required.

`ExternalRuntimeBinding` carries a non-secret source/slot key alongside privately
owned parameters. Full descriptors refuse JSON/YAML serialization and redact
formatting. Tagged marker configurations contain only that key, so configuration
fingerprints contain no parameter or credential-profile value.

`entities.PrepareMonitorExternal` requires explicit matching bindings. Its
`JobStorage.ExternalJobs` data preserves check, recovery and notification positions
without constructing a local executable job for an external slot. Ordinary
`PrepareMonitor` rejects those markers. Copying and Ark installation preserve nil
notification slots and detach the retained descriptor buffers.

## Result preparation

`Catalog.PrepareWorkerOutcome` checks the original server identity, worker UID,
execution, grant and pinned JobType contract supplied by the future committed
result-admission path. It encrypts the complete report and provides a stable replay
digest. It does not authenticate a worker, commit an outcome or issue a receipt.

Check success/failure is distinct from `noData`; recovery acceptance is distinct
from reported completion; notification acceptance is distinct from reported
delivery. Only a registered definite-rejection code permits the existing bounded
retry classification. Arbitrary diagnostics, evidence strings and supplemental
data stay inside the encrypted report, with a 128 KiB encoded envelope limit and
64 KiB diagnostic limit. Only the core status classification is returned separately.

An interrupted handler can submit `unknown` or `noData` with null supplemental
data even if its normal result schema requires an object. Non-null data still has
to satisfy the original schema. A newer or recreated JobType cannot redefine an
older execution's result contract. Sealing cancellation returns no prepared value
and makes no durable mutation.

## Verification boundary

The [local evidence record](../../bin/verification/worker-admission-2026-09-24/result.json)
identifies exact commands, source hashes and scoped results. The prepared assignment
tests exercise encrypted source read → committed queued intent → ready lookup →
authenticated reopening. Storage tests cover original category/source checks,
snapshot/reopen, ownership, quotas, corruption and format exclusion. They invoke
no external handler and do not establish worker protocol interoperability.

The later [offers and Start checkpoint](worker-offers-and-start.md) implements
bounded allocation, Start/reconciliation and retained-ciphertext startup verification.
Remaining work includes heartbeat deadlines, authenticated original-worker result receipts, late evidence,
queued cancellation and expiry, durable check `noData` watermarks/accounting,
owner-loop projection, operational observations, and separate-process failure
campaigns. Public execution remains
closed until those contracts are connected and verified.
