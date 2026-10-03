# Worker heartbeats and late evidence

Updated: 2026-09-27. This private Ticket 8 checkpoint adds committed heartbeat
decisions and encrypted append-only late evidence to the existing
[result and receipt contract](worker-result-receipts.md). The HTTP adapters are
tested with the actual SDK, TLS and Raft through a dedicated test mux. The later
[worker HTTP integration](worker-http.md) connects opted-in production routes.
Public external configuration,
controller dispatch and complete runner/server interoperability remain unfinished.

## Heartbeat decisions

The worker authenticates with its current credential and supplies the original
server, session, execution and grant identities. The original polling session can
be idle while a handler runs; its pinned identity must still match. Authentication
does not require a credential or grant revision that has since been replaced.

A positive decision requires an unfinished started execution, its original live
controller owner, unexpired overall deadline, current configuration and control
pins, and current permission for that work. Recovery and notification also require
the original started action and worker grant. A newer check generation makes the
old execution ineligible to continue.

Acknowledging an incident does not stop its work. Dismissing an incident stops
continuation of that incident's notification; it does not stop checks or recovery.
Snooze, disable, changed configuration and removed authorization are checked
against the original execution. Once a negative heartbeat decision is committed,
later heartbeats cannot revive the same grant, even if a control is reopened.

A heartbeat grants no new invocation and never extends the overall deadline.
It requests cooperative behavior; it cannot prove that remote handler code has
stopped. An eligible actual result can still be recorded after a negative decision.
Completed outcomes preserve their original timestamps and receipts. Heartbeats
use already-reserved lifecycle capacity, so a full intent namespace cannot prevent
recording a stop decision.

## Append-only evidence

Late evidence requires the original receipt for an unknown action or a finalized
`noData` check. The server derives the original execution revision, grant,
immutable JobType and worker identity from that retained result. Current rotated
credentials may deliver this evidence after new-work grants or polling sessions
end. Revoked identities and stale credentials remain rejected.

Each request has a caller-selected evidence ID. Repeating that ID with the same
canonical content returns the first evidence receipt and preserves the first
ciphertext and timestamp. Changed content under that ID conflicts. A new ID adds
a separate record within the retention limits.

The payload is validated against the original result schema and encrypted before
Raft submission. Its binding includes the original execution and result receipt,
evidence ID and purpose. Startup authenticates the ciphertext, original contract
and canonical digest. It does not reinterpret the evidence as a new result.

Evidence never replaces the original result, action state, operator review,
health observation or execution ownership. Contradictory supplemental reports
remain separate records for review. The protocol carries no trustworthy handler
timing, so evidence does not fabricate execution latency. Late check evidence
cannot improve health or SLO attainment and creates no raw check-history event.
Action evidence adds one safe timeline event referencing the server's evidence
receipt; private payloads and provider credentials are not included in that event.

## Capacity and HTTP handling

| Bound | Heartbeat | Late evidence |
| --- | --- | --- |
| Incoming encoded request | 8 KiB | 128 KiB |
| Concurrent requests, global / per worker UID | 64 / 16 | 16 / 4 |
| Request budget | 10 seconds, also bounded by caller | 10 seconds, also bounded by caller |
| Retention | Latest decision in reserved lifecycle capacity | 8 records per execution; 64 MiB total encoded evidence |

Evidence records are individually limited to 256 KiB including envelope and
metadata. This namespace is separate from original-result capacity. Filling it
rejects new evidence without evicting old evidence, losing an original result or
preventing an identical receipt replay. These are encoded accounting bounds,
not preallocated memory or measured RSS limits. Recovery traversal includes all
retained evidence within its existing 256-record / 4 MiB page limits.

Both adapters authenticate before reading bodies. Their admission limits are
independent of original-result ingress. Stalled requests release their slots on
deadline; error responses do not expose stored payloads, backend paths or keys.
Requests that lose their commit response remain unconfirmed.

## Storage compatibility and verification

Tagged storage format 23 adds heartbeat and evidence commands. Earlier tagged
formats 15–22 remain readable; older binaries must not open a store after a newer
unsupported command format has been written. Default builds remain at format 14
and exclude these types, methods and route implementations. Keep a complete,
stopped backup before upgrading storage formats.

Snapshots freeze the heartbeat and evidence state before background persistence.
Restore validates identities, receipt hashes, ordering, duplicate evidence IDs,
format gates and byte accounting. Snapshot restoration and log replay invoke no
external operation.

The scoped suite covers SDK/TLS/Raft behavior for checks, recovery and
notifications; credential rotation/revocation; cooperative stop decisions;
unchanged operator reviews; encrypted recovery; duplicate requests; corruption;
capacity limits; bounded pages; and stalled HTTP bodies. Twelve additional native
process-kill cases cover each category after heartbeat commit, before evidence
commit, after evidence commit and after snapshot. The earlier nine result-commit
crash cases remain in the suite. These exercise persistence and adapters, not
actual handler invocations through `worker.Run` and production dispatch.

The [verification record](../review/local-evidence.md#record-4b7aa3dc023482578db15e1c)
identifies the selected commands and exact source inputs. The later
[format-24 checkpoint](worker-finalization.md) adds automatic expiry/noData
finalization. Independent review, aggregate missing-check SLO accounting,
terminal retention/requeue, bounded long polling, worker observations and complete
separate-process runner/server integration remain open. Provider, endurance and
distribution qualification remain separate release requirements.
