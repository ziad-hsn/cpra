# Collection writer contention, 14 October 2026

Private development checks start from `cffe22edea63117e6cd4879d226e23ad85008f54`
with the new regression test. No production code, storage format, batching
limit, durability setting or admission policy changes in this checkpoint.

`TestCollectionWriteContentionPreservesHealthAndOriginalOutcome` uses the real
single-node Raft Store and its synchronous bbolt collection ledger. A separate
bbolt write transaction holds the single writer while the original upload
reaches the ledger transaction and holds the authoritative FSM lock. The test
requires that the upload has not returned before probing health and admission.

The test establishes these distinct outcomes:

- Published health and status reads return while the writer is occupied and do
  not advertise the still-uncommitted prefix.
- Execution admission still waits for authoritative state and respects its
  deadline. A health read is not an execution grant.
- Canceling an admitted caller returns `ErrCommitUnconfirmed` and cancellation,
  with no successful result. It does not remove the original committed command.
- Releasing the writer allows the original upload to finish. An ordered barrier
  reconciles that outcome without a second upload; exact ciphertext rows are
  preserved, the next ordinal can append, and a real Store restart retains all
  three original rows.

`TestCollectionLedgerFailureRemainsUnavailable` removes a required metadata
bucket from an isolated real ledger. The subsequent upload fails and later
observations/submissions remain unavailable. The diagnostic status contains no
local directory path. This corruption contrast does not simulate a disk losing
power or prove every filesystem failure boundary.

Independent review identified two fixture gaps: a lock regression could prevent
the test from reaching its own deferred rollback, and final Store close errors
were discarded. Both top-level tests now execute their bodies in separate
children using the same test binary, with a 60-second child timeout, independent
75-second parent deadline, bounded pipe waiting and parent-owned temporary
directories. Unexpected close errors fail the test. These children are normal
test isolation; they are not forcibly terminated in the passing scenarios.

| Check | Result |
| --- | --- |
| Initial Go 1.27.1/default | Both production-path fixtures passed before containment strengthening; 1.860 s package time. |
| Initial broader Go 1.25/default selection | Persistence, controller observation and management cancellation/upload regressions passed before containment strengthening. |
| Initial broader Go 1.27.1/race selection | The same areas plus unresolved owner-write recovery passed before containment strengthening. |
| Final Go 1.27.1/race child fixtures | Both tests pass, 4.993 s package time; log SHA-256 `ec65b1eb0f72b69d7c28f9d40f14b1557bc3e8dedf78ff46ab05838bae6f2681`. |
| Final Go 1.25/default child fixtures | Both tests pass, 1.939 s package time; log SHA-256 `46c006336881beff2f32c5d17939feebf09b62d03b0fc718ef7b025eb22e3508`. |

The private records contain commands, initial source inventory, compiler,
starting commit, exit status and log digest. They use the local module workspace
on Linux/WSL. They do not establish HTTP/CLI admission classification, provider
effects, killed-server recovery, measured slow-disk throughput, fleet capacity
or final frozen-source qualification. The separate CLI and controller tests
retain their own evidence boundaries. Independent source re-review closed both
fixture findings after inspecting the strengthened children and close handling;
the reviewer did not run tests. No further concrete finding remained in this
finite checkpoint.
