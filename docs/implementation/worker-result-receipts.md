# Worker result commits and receipts

Updated: 2026-09-27. This private Ticket 8 checkpoint adds authenticated result
delivery to the existing offers and Start implementation. The server commits the
encrypted outcome, lifecycle change and original receipt together. The later
[worker HTTP integration](worker-http.md) connects opted-in production routes.
Public external configuration remains closed while controller
dispatch, aggregate SLO integration and full runner/server interoperability remain
unfinished. Later checkpoints add [heartbeats and late evidence](worker-heartbeats-and-evidence.md)
and [automatic finalization](worker-finalization.md). The subsequent
[format-25 SLO checkpoint](worker-slo.md) records post-commit confirmation and
missing measurements for admitted checks; full dispatcher integration remains open.

## Identity, validation and acknowledgment

The HTTP adapter authenticates the worker before reading the result body. Result
admission derives the execution revision, original JobType and worker/grant
binding from retained state. Caller-supplied monitor, endpoint or JobType choices
cannot redirect the result. The complete report is checked against that
execution's immutable result schema and encrypted before Raft submission.

The FSM checks the current credential for the original worker UID. Rotated
credentials can deliver the worker's retained outcomes; removed new-work grants
and expired polling sessions do not discard them. A revoked worker, stale
credential, foreign grant or restored server identity cannot obtain a receipt.
The authority check is repeated after commit before the HTTP response is released.

Tagged storage format 22 retains one result per execution. Its replay digest binds
the original server, worker, execution revision, grant and JobType to the encoded
report. Identical retries return the first receipt and preserve the first
ciphertext, timestamp and lifecycle outcome. A conflicting report returns a
conflict; it does not replace the original evidence or apply another transition.
Receipt identity derives deterministically from the original server, grant and
committed log index. Snapshot validation checks that identity again.

The adapter returns a receipt only after durable submission succeeds. A lost reply
or interrupted commit remains unconfirmed. The worker retains and retries the
identical outcome through its existing outbox. A new execution or changed report
is not an equivalent retry.

## Lifecycle meaning

| Receipt | Meaning |
| --- | --- |
| `accepted` | The original outcome was committed and applied to the applicable check/action transition. This is server acknowledgment, not independent proof of a provider effect. |
| `finalized` | The check contributes no new health observation: it reported `noData`, expired, belonged to an older configuration/owner, or was superseded by a newer generation. |
| `unknown` | The action remains held because its outcome is uncertain or its original execution can no longer safely change current lifecycle state. |

Check success/failure uses the existing incident transition. A missing check
advances its applicable generation watermark and next schedule without changing
the previous health observation, successful-check count or failure streak. A
superseded or replaced monitor does not receive the old observation. The protocol
does not carry trustworthy handler execution times, so these results do not
fabricate an execution-latency measurement. Controller integration still needs to
connect missing-check coverage and SLO accounting; this checkpoint does not claim
that complete path.

Recovery and notification results use the existing action/retry rules. Only a
definite rejection code declared by the original contract is retryable.
`accepted`, `completed` and `delivered` remain distinct reported outcomes in state
and history. Unknown, expired, previous-owner or stale-configuration actions stay
held; a report cannot implicitly resolve an operator review or become late
evidence. Append-only late evidence uses the separate
[format-23 operation](worker-heartbeats-and-evidence.md).

Once a result is retained, Start/reconciliation returns `terminal` with the
original receipt identity and no grant or deadline, including an unknown action
outcome. An unreceipted interrupted start still returns `unknown` with its
original grant for outcome correlation; it never permits another invocation.
The worker retains an unknown-action journal entry when the terminal receipt
matches its original receipt. A different receipt is an identity error, and
neither terminal reconciliation nor late evidence resolves the held outcome.
Neither response grants another invocation. Log replay and snapshot restoration
perform no external operations.

## Capacity and recovery

| Bound | Value |
| --- | --- |
| Encoded incoming outcome | 128 KiB |
| Diagnostic field | 64 KiB |
| Concurrent result requests | 32 globally, 8 per worker UID |
| Request/body-read budget | 10 seconds, also bounded by the caller's deadline |
| Retained encoded result per admitted execution | 256 KiB |
| Retained execution count | Existing 4,096 global / 256 per JobType-incarnation bounds |
| Retained-execution traversal | At most 256 records / 4 MiB, including outcome storage |

Each admitted execution reserves one result slot through the existing count
limit. Result capacity is separate from the 256 MiB intent/lifecycle namespace, so
a full intent namespace cannot reject the completion of already admitted work.
The maximum additional encoded result retention is **1 GiB** (4,096 × 256 KiB).
This is an additive storage bound, not preallocated memory or a measured RSS
limit. The bytes are still included in bounded traversal. Physical allocation,
snapshots and memory overhead need measurement before enabling production
dispatch. Terminal retirement remains unfinished; admission backpressure must
preserve receipts and unknown actions rather than evict them to make room.

Startup authenticates retained result ciphertext and its original schema as well
as assignments. It checks the digest and server-owned status/retry
classification against the decrypted report. Missing keys, corrupted data and
invalid contracts leave admission unavailable. Cancellation remains retryable and
does not permanently poison the catalog. Verification neither reseals reports nor
updates committed monitor state.

Existing tagged formats 15–21 remain readable. Result commands require format 22;
an older reader cannot consume them, even if a command ultimately conflicts.
Default builds retain format 14 and exclude the result methods, types and route
implementation. An older artifact must not open a store written by a newer
unsupported format; retain a complete stopped backup before upgrade.

## Verification scope

The focused tests exercise actual SDK requests over TLS, Raft commits, encrypted
catalog verification, log-only restart and snapshot recovery through a dedicated
test mux. Process-kill tests cover check, recovery and notification state before
result commit, after commit, and after snapshot. Persistence fixtures exercise
the committed transition directly; they do not invoke the worker library or
establish a complete provider effect.

Additional tests cover altered receipts, ownership, changed reports, credential
rotation and revocation, missing data, reserved result capacity, byte-bounded
pagination, stalled HTTP bodies and encrypted-result recovery. The
[verification record](../review/local-evidence.md#record-e94436c23f0678232d4ee847)
identifies the exact selected tests and source hashes. Independent review and
full production-server/worker execution remain outstanding.
