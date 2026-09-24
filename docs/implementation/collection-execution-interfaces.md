# Collection execution result interfaces

This private checkpoint exposes retained execution results through the existing
operation API, Go SDK, CLI and dashboard. Format 10 introduced finalization and
format 11 introduced joined publication. The current private format-12
[retirement checkpoint](collection-execution-retirement.md) adds bounded paired
execution-record deletion and partial-prefix recovery in `internal/persistence`.
Format 13 subsequently completed source cleanup and retained listing. The
[public activation integration](collection-public-activation.md) now connects
Apply through the public route and shared consumers; private shipping gates and
cross-process original-input reselection remain open.

## Operation observations

Collection detail and list observations distinguish an accepted catalog mutation
from its controller outcome. `committed` counts accepted mutations; `applied`
counts accepted children with recorded applied outcomes. A later projection
failure does not undo the earlier committed decision. Unchanged resources do not
increment either count.

`executionResult.state` is independent of parent state:

- `pending` reports the original execution's available counters while child
  settlement or result publication is incomplete.
- `ready` requires the immutable original summary, history anchor and publication
  seal to agree. Result rows retain original input order.
- `expired` reports the result's own retention deadline or committed retention
  cutoff. An earlier cancellation receipt's deadline does not expire a still-live
  execution result.

The detached observation is excluded from persisted receipt JSON. This change
does not alter historical receipt encodings, command digests or storage formats.
Missing or inconsistent retained evidence is an unavailable read, not an empty
successful result.

## Protected HTTP reads

`GET /api/v2/operations/{id}` returns one bounded execution-result page for an
activated collection. Ordinary operation receipt behavior remains unchanged.
The default limit is 100 items, the maximum is 500, and the response page has a
4 MiB ceiling. A continuation cursor binds the original principal, authorization
generation, operation, immutable result identity, watermark and requested limit.
It cannot extend past the original result deadline or its five-minute lifetime.

With a live source header, ownership is checked before protected history access.
After header removal, bounded original-anchor metadata recovers the owner before
seal or item access. Disk reads occur outside the cursor and authorization locks.
After the read, the server checks current authority, storage health, epoch,
issued-handle high-water, history identity, result binding and expiry
again before admitting the response. The last metadata check also observes the
monotonic committed history cutoff without taking a history lock. Native history
publishes that cutoff only after its catalog save succeeds; failed retention
writes cannot advertise expiry that was never committed.

The reader shares the existing eight-reader admission limit and bounded snapshot
count/byte quotas. Authentication revocation, expired cursors, storage shutdown
and cancellation cannot return an earlier cached page as a fallback. Protected
point reads and result pages now retain their authority after source-header
removal; tests remove the header only after committed finalization/publication.
The format-12 retirement command keeps the source header intact. Format 13 removes
that header only after source cleanup; retained operation listing is implemented
and preserves original owner authority after its removal.

## SDK, CLI and dashboard

The SDK's `Operations.ExecutionResult` reads one page, `ExecutionItems` iterates
pages lazily, and `WaitExecutionResult` waits for result availability separately
from parent state. Canceling a caller's wait does not cancel the server operation.

The CLI exposes the same page with:

```sh
cpractl get operation "$OPERATION_ID" --results --limit 100
cpractl get operation "$OPERATION_ID" --results --limit 100 --cursor "$CURSOR" -o json
```

The command makes one read and never collects all pages. Its table separates the
configuration decision from controller state/outcome and displays unavailable
measurements explicitly. JSON and YAML use the canonical typed response.

The dashboard operation page provides explicit result reading, pagination,
refresh, hiding and optional waiting. It retains one displayed result page;
parent query metadata does not retain a second copy. Waiting continues for a
canceled parent with pending children, at intervals of at least five seconds or
the longer server delay. Stopping a wait only cancels local observation.

Sign-out, navigation and stopped reads fence delayed replies. A failed read
clears prior rows and suppresses stale ready/count displays even across a later
retry followed by Stop. Unknown availability remains unsupported observation;
it is not converted into successful completion or automatic polling.

## Verification and remaining work

The earlier interface evidence file is
`bin/verification/collection-execution-interfaces-2026-09-23/result.json`.
It records scoped backend, race, minimum-Go, CLI, frontend and native-browser
checks, source hashes, corrected failures and independent reviews.

The browser fixture uses normal application startup, TLS, Raft, the real
controller and embedded dashboard. A stopped private admission seeds one
Credential collection; public Activate is not exercised. The owner reads the
original result, distinguishes committed/applied outcomes, hides and rereads it,
signs out, and verifies reader isolation. All browser API requests are GETs. A
normal restart preserves the original summary and rows. Response capture uses
bounded CDP buffers and reports interrupted captures separately; it validates
complete JSON and independently checks rendered UI. Two initial harness failures
are retained with the successful corrected runs.

The [retirement contract](collection-execution-retirement.md) connects
the bounded checkpoint to a registered private `retire` command, paired
execution-record deletion, quota refunds and partial-prefix snapshot/recovery.
Format 13 adds source retirement with surviving-prefix digests, header-last
removal and automatic selection. Protected point/page/list lookup independently
verifies retained anchor/seal authority after removal; it never reconstructs an
execution grant from history. The source-retirement record documents the scoped
format-13 qualification and its cold-audit performance boundary.
Public activation and connected Apply remain unfinished. The retirement record
carries the current scope; earlier interface evidence remains unchanged.

This scope does not qualify external workers, native release artifacts, provider
accounts or the one-million-monitor 24-hour campaign. Candidates remain private.
