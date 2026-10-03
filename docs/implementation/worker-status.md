# External-worker observations

The tagged server implements the operator endpoint `GET /api/v2/external-workers`
and the SDK methods `Client.Workers` and `Client.IterateWorkers`. Both the
`externaljobs` build tag and `external_jobs.enabled: true` are required. Named
management readers and operators may inspect observations; worker execution
credentials do not authorize this endpoint. Ordinary builds contain neither its
route nor its SDK types and methods.

This read path does not enable public external monitor configuration or controller
dispatch. Those remain part of the [unfinished execution integration](external-worker-server-next-steps.md).

## Field meanings

| Field | Meaning |
| --- | --- |
| `workerID`, `workerUID` | Provisioned stable ID and its separate incarnation. Workers are listed even when they have never polled. |
| `sessionStatus` | `no_session`, `current`, `expired`, `previous_owner`, `clock_regression`, `reset_required`, `revoked`, or `credential_expired`, evaluated at snapshot capture. Preserve unknown future values. |
| `capacityAvailable`, `capacity` | The last committed poll's advertised free result capacity, available only for a current session with eligible credentials. It is not total concurrency or a current measurement of the worker process. |
| `active` | Committed Start grants without terminal receipts, including grants waiting for timeout finalization. An outstanding grant does not prove a remote handler is running. Unstarted offers are not counted. |
| `unknownActions` | Retained recovery/notification executions whose original terminal outcome was unknown, including operator-reviewed actions. Counted separately from active grants; action status provides the current hold and review. |
| `lastSeenAt` | Last committed poll timestamp. Absent when no session observation exists. An exact poll replay does not refresh it. It is not a fabricated zero time or a heartbeat promise. |
| List `generatedAt` | Capture time shared by every page of the same immutable observation snapshot. |

No credentials, verifiers, grants, execution IDs, encrypted payloads, parameters,
diagnostic text, evidence, or worker-local journal contents enter these responses.
Reading status creates no Raft command, starts no job, and calls no provider.
Storage failures produce the ordinary unavailable problem without native paths.

## Snapshot pagination and bounds

Rows sort by stable worker ID. A page defaults to 100 rows and accepts 1–500.
Continue with `nextCursor` and the original limit. The cursor is bound to the
management principal, authorization generation, resource kind and five-minute
expiry. It cannot cross to another principal or silently restart a query. Changes
to sessions, grants or outcomes after capture appear in a new query; continuation
and replay keep the original rows and `generatedAt`.

Selectors and monitor filtering are not implemented and return an explicit
feature-unavailable error. Duplicate and unknown query parameters are rejected.

At most eight worker snapshots exist globally and two per principal, including
captures and active reads whose cursors have expired. Each contains at most 1,024
scalar worker rows. Capture reserves capacity before reading storage. Authority
and cursor locks are released during capture and pagination; current authority
and expiry are checked again before response publication. Cancellation or failed
capture releases its reservation. Diagnostics remain readable during admission
drain while storage and authorization remain available.

## Verification boundary

Focused tests exercise the actual public SDK, production TLS listener and Raft
store, stopped worker provisioning, pagination during a committed poll, unknown
action receipts, snapshot restoration, opt-in/identity rejection, sanitized
storage failures, and snapshot-reservation lifetime. The
[29 September checkpoint](../review/local-evidence.md#record-d747aeae189b2c5d44d9fecf)
records 25 selected integration tests and all 238 tagged SDK/worker tests passing
on Go 1.27.1 with race detection and on Go 1.25.0. Tagged vet, all 232 negative
compilation checks for 116 excluded symbols, and generation in two isolated
roots passed. The 1,096 recorded Go/module inputs remained unchanged during
these checks. Earlier failed attempts remain in the report.

This is scoped local evidence. Independent review, controller dispatch, physical
worker liveness and complete worker-runner interoperability remain unqualified.
