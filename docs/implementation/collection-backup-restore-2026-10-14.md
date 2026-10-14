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
not established. The observer-reported sampled maximum logical total was
824,027,674 bytes, below its 1 GiB sampling guard; this was not a true peak or a
hard allocation limit. No fixture entries remained after cleanup.

The subsequent maintenance-budget investigation remains separate from the
reviewed explicit-restore repair. Native macOS, Windows arm64, system service identities,
the complete corruption/distribution matrix and frozen-source qualification
remain open. The 24-hour endurance run remains cancelled.

## Maintenance continuation and Windows measurement

After checkpoint `6ad60c6`, a separate regression exposed a maintenance-order
defect. An unrelated incomplete generation discovered during scanning prevented
an already-admitted retirement from progressing. On a slow filesystem, scanning
could also consume the cooperative budget before the retirement step began.
The failed `collection-scan-priority-baseline-go1.27.2` record is retained.

Maintenance now attempts one admitted transition before scanning. It selects a
new candidate after scanning and admits it on the next turn with a fresh budget.
Every transition still checks store ownership, protected generations, pins and
native file identities. The regression verifies completion exactly once after
restart, preservation of current/previous generations, and later reporting of
the unrelated incomplete generation without deleting it. The focused Linux
generation selection passes; its log SHA-256 is
`e1d424f9ae485b2a7bf0745a26478eef4a1211b61c20470172d375b8f258ffc2`.
Independent source review found no further concrete issue in this change and
executed no tests. This ordering does not establish a filesystem latency bound.

A native Windows rerun of the same backup/restore fixtures produced three
passes and one failure at **publication of the prepared restore directory**.
The private runner was concurrently enumerating the fixture tree to measure
disk usage. A separate native probe reproduced the identical access-denied
error in five out of five moves while a .NET directory enumerator remained
open. All five moves succeeded after disposing the enumerator, and ten controls
without a held enumerator passed. An explicitly share-delete directory handle
also prevented an ancestor move. These results agree with the documented
[Windows directory rename constraints](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information).

The runner no longer enumerates live descendants. It reserves 1 GiB within the
task's scratch budget and samples volume free space with a 1 GiB consumption
guard and 64 MiB free-space reserve. That observation includes other host
activity and is not a per-fixture peak measurement. It reports the missing
measurement explicitly. The original runner, failed campaigns and native
handle probes remain retained. No production rename retry or permission change
is introduced for this harness defect.

The first attempt with the new observer is incomplete: its volume guard fired,
then a PowerShell termination invocation failed before stdout was retained.
No test process remained afterward; the only remaining fixture file was the
verified test executable, which was removed. The failure and cleanup record
remain retained. The termination path now invokes the native process directly.

Separate reruns of both cases then pass against the **same executable**, SHA-256
`4c3f6854f3fc5ad81c5f0c975a81f18a4fc529493bf46313308db457685d4190`:
interrupted retirement in 36.44 seconds and completed retirement in 46.42
seconds. Both leave zero fixture entries. Their host-volume consumption samples
peak at 1,016,455,168 and 1,036,300,288 bytes respectively; these are not isolated
fixture peaks. Both retain completed-step timing overruns in their diagnostics.
The launcher logs have SHA-256
`d325c7afcc56e418b7ef5aa5bdb868aba9d2491bc6240bfb20813e28338a6c68`
and `2a3062faf110be1100fa1ec17a53d32ef228dfdfa7cabbe3140aeb555ee45604`.

The reviewer independently checked the native probe logs and their hashes,
the runner's observer boundary, and the production handle-lifetime path. This
read-only review found no successful-path handle retention across publication
and made no claim that it had rerun the tests. The passing reruns establish this
private Windows x64/local-NTFS boundary; the older failed runs remain failed and
the complete native/platform and frozen-source gates remain open.

The focused native Windows proof run also passes without skips: all five
retirement phases, sixteen invalid-proof cases and the maintenance-order
regression. Its executable SHA-256 is
`07f1ef1f6bbe8d80646def2ee15fd338b6f6afdc5a9dceba9b1df948ea1fc9b3`,
and the retained launcher-log SHA-256 is
`e721f4a4ba6d9115608a4a5f9085945df73691c70efbf6d5c41bc3fc1535591d`.
It leaves no fixture entries. These fixtures use the actual native filesystem
identity and permission checks; unsupported or unavailable proof is a failure.

## Complete Linux storage check

The complete persistence and local-administration package suites pass on Linux
amd64/WSL with Go 1.27.2, race detection, uncached execution, `GOWORK=off` and the
prepared private SDK cache. The run records 2,687 passing test/subtest events and
three skipped subprocess helper entries; their parent crash/restart fixtures
execute, and no functional case is excluded. Persistence takes 1,304.135 seconds
and local administration 89.874 seconds. Total wall time including compilation
is 1,525.382 seconds, within the existing 45-minute package budget.

The retained `storage-full-race-go1.27.2` log has SHA-256
`c2040babe3785d4fce276b9fabbb3d69f290081518b09fbd71effb9f830263ef`.
The task observer samples a maximum allocated total of 5,126,049,792 bytes,
below its 6 GiB limit minus 64 MiB reserve. This is a task-wide sampled total,
not a storage-product capacity or peak-RSS measurement. Static analysis of both
packages with the same compiler and race selection also passes.

These checks cover the working storage repair after `6ad60c6`; documentation
was updated during execution. They do not establish a frozen source identity,
public module availability, other native platforms or the remaining release
matrix. Earlier failed runs retain their original dispositions.

The focused minimum-compiler selection also passes: Go 1.25.0 persistence in
49.377 seconds and local administration in 78.093 seconds. The Go 1.27.2
`externaljobs` selection with race detection passes in 112.914 and 81.881
seconds respectively, including worker restore/authentication fences. Both
commands also execute the HTTP expected-status regression added during the
subsequent acceptance review; that package passes in each selection. Their
retained log SHA-256 values are
`bc125c22a223e3c7de43750ea3bd7340bf7abb64b255d6674724a561bda3076b`
and `35b7260d6d5bf6f051a74a397150afbcd58d92cddc574418cce617d2381bba81`.
These focused selections supplement the complete default storage run; they do
not replace the entire minimum/tagged release matrix.
