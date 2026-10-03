# External-check receipt retention

This private implementation adds tagged storage format 27. It bounds completed
external-check records while preserving committed monitor state, SLO accounting
and conservative action recovery. Production external configuration, dispatch
and complete runner/server qualification remain unfinished. The subsequent
[worker HTTP integration](worker-http.md) connects opted-in production routes.

## Identity and admission

`AllocateWorkerCheckID` commits a store-specific UUID epoch and an increasing
sequence before the caller prepares an encrypted assignment. The resulting
`wc.<epoch>.<sequence>` identity is never issued again within the retained store
lineage. The reservation belongs to the current controller owner. Admission consumes it atomically and still checks
the original configuration, control state, generation and deadline.

A lost allocation response can leave an unused reservation. Only a committed
retirement removes it; elapsed time alone does not establish authoritative
expiry. The epoch and highest issued sequence survive log and snapshot recovery.
They distinguish a retired identity from a malformed, foreign or unissued one
without retaining an individual tombstone for every check.

Restoring an older backup still changes the provisioned worker server identity.
The watermark does not make that backup a continuation of work performed after
the backup. Old worker journals require operator reconciliation.

Existing unsequenced private execution IDs are retained. The production
dispatcher must use the allocation path for new checks when it is connected.
Interventions and notifications cannot use the reserved check-ID namespace.

## Bounds and retirement

| Limit | Value |
| --- | --- |
| Pending identity reservations | 1,024 and 1 MiB of accounted state |
| Unused reservation lifetime | One minute |
| Recent completed-check receipt target | 1,024 records or five minutes |
| Retirement command | At most 128 identities |

Live poll responses pin the check records they contain. Such pins can exceed the
recent-receipt target; the existing global execution count and byte limits still
bound admission. Retiring a record referenced by an expired poll response also
marks that response expired in the same commit. Retrying it cannot obtain a
successful response with silently removed assignments, even with a backwards
request timestamp.

The state machine validates the entire retirement batch before deleting any
record or changing byte counters. It removes completed check records, their
supplemental late evidence and their indexes. Evidence for recovery and
notification actions is retained with those actions. Unknown actions are never
evicted to make room. Concurrent result/evidence delivery either commits before
retirement or observes the retired state; it cannot recreate the record.

Retirement changes neither monitor health and generation nor incident history
and SLO counters. A successful result missing its post-commit timing has a
ten-second grace period before count-based retirement. If timing never arrives,
the aggregate retains the missing measurement; expiry cannot improve attainment.

## Worker responses and restart

After authentication and server-identity validation, Start and check Result
requests for retired issued identities receive HTTP 410 with problem code
`workerCheckExpired`. The tagged SDK exposes `ErrWorkerCheckExpired` only for
that exact response. A missing record, generic conflict, transport failure or
unavailable server does not establish expiry.

The worker library can remove a check's pending reconciliation or outcome only
after this explicit disposition. It never uses check expiry to discard a
recovery/notification outcome. Restart reconciliation does not invoke a handler.
Late evidence is separate: a removed check's evidence request is rejected, not
reported as delivered. Retained uncertain actions keep their evidence path.

Snapshots freeze the retention state before serialization. Format-27 commands
and state are excluded from untagged builds. Earlier tagged formats retain their
recorded encoding contracts; default storage remains format 14.

## Qualification

The scoped result is recorded in
`bin/verification/worker-check-retention-2026-09-29/result.json`. The selected
persistence and integration suites passed 240 tests on Go 1.27.1 with race
detection and on Go 1.25.0; each includes 48 native process-kill cases and one
subprocess helper skipped in the parent run. All 235 tagged SDK/worker tests and
48 default-boundary tests passed on both compilers. Tagged vet, two-root SDK
regeneration and all 334 build-exclusion checks passed on the release compiler.
Generated SDK references and copied guides were refreshed and checked.

The tests cover count-based retention while another worker's live poll pins a
receipt, concurrent result replay, late-evidence byte reclamation, expired poll
replay with backwards timestamps, unchanged health/SLO state, and encrypted
worker-journal cleanup limited to explicit check expiry. The initial failures and
source hashes are retained with the passing results. All 1,080 Go/module inputs
stayed unchanged during the final checks.

This remains local, uncommitted implementation evidence. Independent review,
production worker integration and complete release qualification remain open.
