# Audit checkpoint: 2026-09-23

Working tree: `codex/dashboard-finalization`, kept private. The storage package is
now `internal/persistence`; package declarations, imports, tests, scripts and
active documentation use that name. The package rename does not change persisted
identities, encryption domains, Raft envelopes or snapshot formats. Historical
verification logs retain their original paths.

This checkpoint records corrections to the supplied 14 findings. It does not
complete collection application, native release qualification, provider-account
verification or the million-monitor endurance campaign.

| # | Disposition | Implementation and evidence boundary |
| --- | --- | --- |
| 1 | Fixed | Server construction always installs authentication. Zero credentials deny API and metrics requests; anonymous compatibility requires explicit loopback configuration and checks the listener and peer. Main already enforced loopback for its committed anonymous policy. |
| 2 | Fixed | CI installs locked Playwright and requires every registered browser security/startup campaign to pass. Missing tooling fails the required campaign rather than producing green skips. The earlier combined four-test Go race campaign passed on Node 24.21.0, Playwright 1.56.1 and Chrome 153.0.8010.36. The newly registered execution-result campaign has separate native browser/race evidence; the combined five-test campaign has not been rerun in this checkpoint. |
| 3 | Fixed | Owner-maintained arrival demand changes on monitor creation, interval edit, disable/enable, snooze/unsnooze and deletion. The pool receives current demand after every update, without a fleet scan. Explicit zero permits idle shrinking. |
| 4 | Hardened | Writer and decoder share command minimum-format selection. Separate frozen digest projections prevent envelope field additions from silently altering retained authentication/reservation identities; transitive wire-shape and golden replay tests guard nested changes. Historical format-2/3 catalog replay remains valid. Previously emitted format-1 pulses carrying control revisions remain readable; new writes select format 2. |
| 5 | Fixed | The v1 history handler returns a fixed public error. A real wrapped filesystem-path failure remains available internally and is absent from the HTTP response. |
| 6 | Fixed | Cursor metadata locks and authorization policy locks are released before snapshot capture, decoding and history I/O. Eight concurrent reads are admitted; identity, authorization generation, cancellation and expiry are rechecked before publication. TLS tests cover blocked reads, revocation, concurrency and cursor quotas. |
| 7 | Fixed | Authenticated CLI requests require HTTPS unless the configured origin is explicitly permitted by `--allow-insecure-http`. Legacy reads honor private CA roots. Existing Compose/Helm/native HTTP probes declare their transport explicitly; redirects remain disabled. |
| 8 | Fixed with scoped integration evidence | Store leadership errors no longer become recorded disk failures. Collection coordinators retain their claim/candidate while retrying reads and FIFO barriers. The initialized controller retains and reconciles original unresolved batches across transient leadership loss, including result/projection commits and executor finalization. Recovery keeps readiness unavailable; startup and genuine storage, verification, restore and migration failures stay fail-closed. Current affected suites, Go 1.25, contextual-read and optional-driver races passed with independent review. The latter includes actual Raft step-down/re-election and startup-follower regressions. |
| 9 | Clarified and instrumented | Retry occupancy belongs in the worker service-time measurement because the worker is occupied. Removing it would understate required capacity. Checks retain their total timeout; individual attempt contexts are canceled promptly, retries use the remaining budget, and attempt duration, timeouts and retry delay have separate raw statistics. A slow first successful attempt retains its budget. |
| 10 | Fixed observability; denominator retained | A rejected queue admission retains the original scheduled obligation and retries admission. It was not a silent dropped check. Monitor summaries now expose awaiting capacity, missed checks and queue rejections; AdaptiveQueue reports actual rejections. Missed/overdue work stays in the SLO denominator. These counters are process/incarnation scoped. |
| 11 | Fixed | New backup format 2 authenticates metadata and inventory with domain-separated HMAC-SHA256 and an independent external 32-byte key. Backup, restore and local service update require `--backup-auth-key`. Recomputed file hashes cannot authenticate a modified manifest. The key is checked before an update stops the existing service. Unsigned backups are rejected explicitly. |
| 12 | Hardened internal access | Unscoped `Catalog.Operation` no longer falls back to collection receipts. Collection reads require the actor-aware method already used by HTTP. Ordinary resource receipts remain shared by design. |
| 13 | Fixed | Lifecycle commands reject monitor identities exceeding 256 bytes, including pulse/start/result/late-result. Tests cover the exact boundary, multibyte input and replay decoding. Existing manifest IDs were already bounded below this limit. |
| 14 | Fixed | SDK workspace requirements come from module metadata rather than a hardcoded prerelease. Development builds verify the embedded dashboard input/output manifest without requiring Node. Dashboard regeneration remains a separate pinned build. |

## Executed verification

The original audit checkpoint below predates the subsequent controller recovery
and format-10 finalization integration. Its broad passing suites remain scoped
to that earlier source; they do not certify the latest combined tree.

- Root module compiled after the package rename.
- Focused persistence/management tests and race checks passed, including actual
  Raft leadership transitions, original-candidate retention and historical format
  compatibility.
- Full controller, jobs and queue suites passed, with race regressions for live
  demand, retries and queue admission. Optional database/broker driver tests passed.
- Full server, management, local administration and CLI suites passed. API cursor,
  authentication, backup-tampering and CLI backup/restore race regressions passed.
- The dashboard build, type check, lint and 387 tests passed. The combined real-browser race campaign passed all four required tests.
- Helm rendering and Compose interpolation contracts passed (11 tests); modified
  native fixture scripts compile. This is not native supervisor execution evidence.
- The full Go 1.27.1 root-module suite passed, including the persistence suite
  (255.646 seconds). Go 1.25 targeted regressions, the standalone SDK race suite,
  affected-package vet, 19 release-contract tests and formatting checks passed.

Local logs and final command results are in
`bin/verification/audit-2026-09-23/`. Temporary failed checks and corrected fixtures
are distinguished from passing reruns; browser campaign tooling rejects missing
or skipped required tests.

## Subsequent private integration

[Format-10 execution finalization](collection-execution-finalization.md) is now
implemented. An isolated `CollectionExecuteCommand` action `finalize` binds the
original admitted operation and exact execution/stop fence, certifies its original
artifacts and complete execution namespace, and freezes an immutable summary.
Committed decisions remain distinct from applied children; cancellation and
restore retain their original state, identity and time. A typed history anchor
uses the first finalization time, and exact retries add no replacement event.
The original namespaces remain retained and publication fields remain zero.

The final focused race campaign passed in **35.214 s**. It covers native log and
snapshot reopen, `LockOffline` inspection, actual explicit restore of a pending
child followed by fresh authentication, immutable stop dispositions, corruption
rejection, historical format-9 command bytes and digest goldens, and historical
cleanup replay. It also checks a valid admission with **10,001 reverse guards**:
begin and pre-begin finalization return quota without poisoning storage or deleting
the retained input. Independent source review and an independent result suite
passed (**7.034 s**); the reviewer separately approved the final quota correction.
Reader/release-recipe format parity and build-configuration checks passed
(**2 tests**). The recipe now reports maximum supported storage format **10**.
Evidence for this scoped integration is in
`/tmp/cpra-execution-result-final-race.log` and
`/tmp/cpra-execution-result-release-tests.log`.

[Controller leadership recovery](controller-leadership-recovery.md) is also
implemented locally and independently reviewed. The full root suite passed before
the final review followups (persistence **272.183 s**). The complete affected
application, management, controller and server suites passed after the followups,
as did Go 1.25 controller/result regressions, management/persistence contextual
cancellation races and vet. All 23 projection/malformed-response scenarios passed
under race detection (**2.747 s**). The final optional-driver race selection
passed (**systems 3.350 s; persistence 36.970 s**), including actual Raft
step-down/re-election, startup-follower, local-executor and format-10 regressions.
All 402 recorded production/build inputs remained unchanged. The commands,
logs, earlier fixture failure and precise qualification boundaries are recorded
in `bin/verification/controller-recovery-2026-09-23/result.json`.

The subsequent [format-11 execution publication checkpoint](collection-execution-publication.md)
connects coordinator finalization, publishes bounded joined item pages and their
seal, and adds owner-protected private result reads. That checkpoint used
storage format 11; the format-10 results above describe their original
checkpoint. The legacy monitor-history route now rejects the four internal
collection namespaces before storage access, preventing it from bypassing
collection ownership checks. Nonreserved monitor history and pagination remain
available; legacy IDs that collide with these internal prefixes are rejected.
Qualification details are recorded in the publication checkpoint.

The subsequent [format-12 execution retirement checkpoint](collection-execution-retirement.md)
registers bounded paired deletion, exact execution quota refunds and recovery
from a committed prefix plus original surviving records. The current recipe
reports maximum storage format 12. Protected point/page reads can use original
retained anchor/seal authority after test-only header removal. Production source
and header deletion, automatic retirement selection and retained listing after
header removal remain unfinished.

## Remaining boundaries

- The controller's component and actual-election checks do not cover every
  combined process-kill schedule or native platform. Held-lock cancellation is
  exercised in persistence, not a combined catalog-reconciler shutdown campaign.
- Complete source/header cleanup and automatic retirement before public
  activation. Protected application-result pagination and execution-only paired
  retirement now have scoped evidence. A valid unstarted
  admission beyond the current audit bound remains retained and unfinalized;
  this slice does not add a larger-budget finalizer or permit its cleanup.
- Run a process-kill campaign specifically at the new finalization/history
  boundary. Native reopen and explicit-restore tests do not substitute for it.
- Native restore fixtures must provision fresh authentication and the matching
  management/TLS setup after restore; the fixture reports this requirement before
  trying to restart. No native supervisor lifecycle pass is claimed here.
- Protected backup-key access is currently qualified on Linux and Windows. Other
  Unix platforms, including Darwin, fail closed pending native ACL support and
  qualification; cross-compilation is not native execution evidence.
- The new raw attempt metrics are not yet fields in the typed v2 Pool contract.
- Protected execution-result projection, HTTP pagination, SDK/CLI reads and the
  dashboard result view now have scoped evidence in the
  [interface checkpoint](collection-execution-interfaces.md). Public collection
  activation, source/header retirement and post-retirement operation listing,
  optional external-worker integration and the remaining shipping gates are open.
- No source commit, tag, release, package, image, chart or documentation publication
  occurred in this checkpoint.
