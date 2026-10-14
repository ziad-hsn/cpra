# Collection growth during frozen snapshots

Private development checks on 14 October 2026, starting from `4dc47ff` on
`codex/release-repairs`. These results qualify the finite storage regression,
not a frozen release or the complete platform matrix.

## Boundary exercised

`TestCollectionLedgerGrowthDuringFrozenSnapshot` opens the production collection
ledger with a 256 MiB logical quota. It commits 16 one-MiB payloads, freezes a
read transaction, and appends another 32 payloads concurrently. On Windows the
fixture verifies a 64 MiB initial file, then observes the writer blocked on
bbolt's mapping lock before exporting the snapshot. It releases the reader,
requires the writer to finish, restores the exported stream and checks every
payload. The snapshot contains exactly the original 16 rows; the live ledger
contains all 48. Synchronous writes remain enabled.

The mapping-lock observation is tied to the pinned bbolt implementation. A
sleep alone cannot satisfy the Windows assertion. The fixture runs in a child
process with a 60-second test deadline and a 75-second parent deadline, so a
regression in writer, reader release or deferred database close cannot hang the
remaining suite indefinitely. Fixture files remain under parent-owned cleanup.

## Native measurements

| Environment | Result | Observed file growth | Writer completion after reader release |
| --- | --- | --- | --- |
| Windows x64, NTFS, Go 1.27.1 | Pass, 1.47 s test | 64 MiB to 128 MiB | 373.6576 ms |
| Windows x64, NTFS, Go 1.25.0 | Pass, 1.83 s test | 64 MiB to 128 MiB | 338.4979 ms |
| WSL/Linux amd64, Go 1.27.1 race build | Pass, 17.117 s package | 35,012,608 to 165,126,144 bytes | 786.624139 ms |

Both Windows runs reached attempted row 23 with row 22 committed before reader
release. The host reported Windows NT 10.0.26300.0 and local NTFS. The temporary
Windows execution directories were empty after cleanup. These are native
process tests, not power-loss guarantees or service-identity qualification.

The Windows Go 1.27.1 executable SHA-256 is
`c8503c98a1cba9794846e71edbf4da3225482c56e5c369f7461bd00627ef8adb`.
The Go 1.25.0 executable SHA-256 is
`a905041fb0d875ff90385cdde3bf8f9df7a8673e7781c378e2d923550ff181a2`.
The Linux race log SHA-256 is
`380666bac39d7e06d8e8e78d38d85f4094c646ee20ab2259f9c52d344abb81a5`.

An earlier complete untagged Linux persistence suite passed in 574.886 seconds
on Go 1.27.1. It used the earlier fixture before the stronger blocked-writer
assertion and child-process deadline were added; it is not attributed to the
final test source. The corrected finite fixture was rerun as recorded above.
Exploratory failures and earlier fixture runs remain separate private records.

## Independent review

The reviewer found two gaps in the initial test: a timed wait did not prove a
writer reached the mapping boundary, and cleanup could itself hang. Both were
corrected and the finite source review closed without a further finding. The
reviewer ran no tests or builds.

The source review also traced Store submission, Raft snapshot configuration,
ledger export/release and compaction. A pinned reader can delay remapping.
Current single-node Store submission waits for each applied log before sending
the next, and snapshot export/release does not require the writer's ledger lock.
No reachable circular wait was identified in the current production path. That
conclusion depends on those ownership rules; adding parallel submissions or a
new snapshot dependency requires another review. The test does not prove every
Raft scheduling interleaving.

## Evidence and remaining work

The previous temporary evidence directory was absent when work resumed. Its
committed summaries remain historical records; the old raw logs are not claimed
available. New logs, metadata and executables use a private persistent task
directory under the operator's local state directory. Each record identifies its
source inventory, compiler, command and evidence scope. Local module replacements
were used and do not establish downloaded-module publication.

Native macOS and Windows arm64, restricted service identities, Windows link
fixtures, the full recovery/distribution matrices and final release qualification
remain open. Publication is withheld and the 24-hour run remains cancelled.
