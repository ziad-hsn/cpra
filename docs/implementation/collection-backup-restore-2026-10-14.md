# Collection backup and restore repair — 14 October 2026

Scope: private development checks on `codex/release-repairs` after checkpoint
`ddc2210`. These results are not frozen release qualification. No public
artifact, tag, module, image, chart or documentation publication is performed.

## Reproduced failure and repair

A stopped store can retain a cleanup intent after admitting deletion of an old
collection generation and before completing that deletion. The complete backup
correctly includes this record. Its native filesystem identities, however, still
refer to the original directory and files. The old restore path copied the
record unchanged, so the restored runtime rejected it and could no longer
retire old generations. Deletion remained conservative; disk accumulation was
the defect.

The separate-process regression first failed because the copied intent remained.
The failed baseline is retained in private evidence as
`backup-pending-retirement-baseline-go1.27.2`.

Explicit restore now finishes only that already-admitted deletion in its
authenticated private copy, while holding offline store ownership and before
publishing the destination. It verifies the strict catalog and selector, original
intent, protected current/previous generations, original marker digest where
present, and destination native identities. It removes only the expected files
and empty candidate directory. It commits catalog retirement before removing
the same verified intent. A catalog entry already recorded as retired is not
counted twice. Incomplete publication, an invalid proof, an unexpected entry or
an unsupported native identity check prevents publication.

The original backup remains unchanged. Ordinary startup still rejects copied
native identities. Offline backup inspection does not clean up generations, and
no provider runs during preparation. This does not introduce a general repair
command, recursive generation deletion or a storage-format change.

## Regression coverage

- The native fixture uses a real Raft store, encrypted partial collection,
  repeated snapshots/restarts, committed incident history and a started action.
  It exercises both completed and interrupted retirement, authenticated backup,
  explicit restore, authentication reprovisioning, old-operation fencing,
  retained history, unknown-action preservation and a fresh collection/restart.
- Five focused cases cover intent publication, database removal, marker removal,
  directory removal and catalog-recorded completion. They check exact accounting,
  unchanged protected/source bytes and idempotent repeat preparation.
- Sixteen invalid-proof cases require rejection without changing files, including
  protected-generation selection, mismatched identities, malformed schemas,
  missing catalog/marker, extra files, hard links and temporary publication both
  with and without a published intent.
- Tests contain blocking snapshot/close calls in a child process with an internal
  timeout and an independent parent deadline. The parent owns temporary paths.
  A completed filesystem step exceeding its cooperative 25 ms budget remains
  visible in test diagnostics; it is not a latency pass. The fixture still
  requires a fresh observation, exact retention counts, no pending intent and
  the expected stopped inventory.

The independent reviewer identified the pending-intent defect while reviewing
the initial backup fixture. The reviewer also required completion beyond catalog
counters and independent process containment; both were added. Finite source
re-review of the repair, five phases and sixteen rejection cases found no
remaining concrete issue. The reviewer executed no tests or builds.

## Recorded checks and limits

The repaired pending-retirement regression passes on Linux with Go 1.27.2.
The broader backup, restore, generation and cleanup selections pass with race
detection: persistence in 93.972 seconds and local administration in 75.387
seconds, 135.225 seconds total including compilation. The retained log SHA-256 is
`c21ed07258f43c0af094c83f8e115e8651511a76763f73a615a5554771f21031`.
These checks use `GOWORK=off` and the prepared private SDK cache.

The first native Windows attempt correctly rejected a key below an insufficiently
protected temporary-directory ancestor. The fixture now uses a protected,
task-owned parent beneath the user profile; it does not alter ancestor ACLs or
relax production checks. A later run failed at restore with an unclassified
access-denied error. The subsequent run with added preparation/publication error
context passed restore but failed a test wait because completed filesystem work
exceeded the cooperative budget. Both failures remain recorded. The corrected
wait retains timing evidence and does not accept an unfinished or blocked step.

The same broader selection passes on Go 1.25.0: persistence in 51.127 seconds
and local administration in 70.133 seconds, 111.370 seconds including compilation.
Its log SHA-256 is
`ea20262db2a2fc038393f0cd72786bbbadec4e8ef69ee402059301d90388393e`.

The repaired tests also ran natively on Windows NT 10.0.26300.0, x64, local NTFS,
using an executable cross-compiled with Go 1.27.2. Two repetitions produced three
passes and one failure. Both interrupted-retirement restore cases passed. The
second completed-retirement case passed restore but failed during subsequent
runtime cleanup: it still retained two old generations, with `budget_exhausted`,
at the 30-second test deadline. The run is **failed evidence**, not a Windows
qualification pass. The earlier access-denied error did not recur; its cause is
not established. Native fixture peak logical size was 824,027,674 bytes beneath
the enforced 1 GiB limit, and no fixture entries remained after cleanup.

The subsequent maintenance-budget investigation remains separate from the
reviewed explicit-restore repair. Native macOS, Windows arm64, system service identities,
the complete corruption/distribution matrix and frozen-source qualification
remain open. The 24-hour endurance run remains cancelled.
