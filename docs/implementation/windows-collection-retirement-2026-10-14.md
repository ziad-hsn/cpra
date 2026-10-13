# Windows collection retirement implementation

Private development checkpoint after `7d5f17a`, dated 14 October 2026, on
`codex/release-repairs`. This is not a frozen release or complete native-platform
qualification. The [repair plan](release-repair-plan.md) retains the single-owner,
reader/snapshot pin and stopped-backup contracts.

## Native filesystem boundary

The [shared engine](../review/source/internal/persistence/collection_ledger_gc_engine.go.txt)
now has separate Unix and Windows filesystem operations. Linux/macOS keep their
anchored opens, no-follow checks and directory synchronization. No Raft,
collection, history or retirement-intent format was reset.

The [Windows implementation](../review/source/internal/persistence/collection_ledger_gc_windows.go.txt)
requires a fixed local NTFS volume with persistent ACLs, a nonzero volume/file
identity and creation time, a real directory or single-link regular file, and
private ownership/ACLs. Reparse points, untrusted effective allow entries,
network volumes, ReFS and unverifiable identities cannot authorize retirement.
Trusted principals are the executing user, SYSTEM, Administrators and enabled
SCM service SIDs in the executing token. Shared LocalService and NetworkService
identities alone do not establish exclusive service ownership.

The intent is written to one exclusive, fixed temporary file, flushed and
published with a same-volume write-through move that cannot replace an existing
intent. An incomplete temporary file is preserved and blocks another admission.
Deletion opens the exact leaf relative to its retained parent handle, rejects
reparse points, rechecks the recorded identity and permissions, flushes regular
file data, and marks that checked handle for deletion. There is no recursive
removal or automatic deletion of unverified state.

Windows directory checkpoints verify identity/permissions. They do not provide
Unix directory `fsync`. Relevant documented contracts are
[file identity](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getfileinformationbyhandle),
[file flushing](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers),
[write-through moves](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexw)
and [handle-based deletion](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-setfileinformationbyhandle).
These tests establish the observed process-interruption boundary on one NTFS
host, without a universal power-loss guarantee.

## Final intent replacement repair

Independent review found that catalog publication could be followed by removal
of a substituted intent. Finalization now retains the flushed intent's identity
across publication and requires the same identity and bytes before removal.
Tests substitute identical and different bytes during publication; both
replacements remain intact and block cleanup. This correction applies to every
platform using the shared engine.

Rejection tests retry only the cooperative-budget result, at most 20 times.
Other results remain immediately observable. The interrupted-admission fixture
waits at most 100 maintenance steps for the actual intent before stopping.
These changes preserve unexpected-mutation and interrupted-recovery assertions.

## Recorded development checks

The focused pattern is `TestCollection(GC|Generation|Retirement)` plus
`TestStoreCollectionGenerations`. Required native fixtures set
`CPRA_REQUIRED_COLLECTION_GC=1`; unsupported filesystem evidence fails rather
than silently skipping qualification.

| Check | Result | Boundary |
| --- | --- | --- |
| Go 1.27.1 Linux focused suite | Pass | Native WSL/Linux |
| Go 1.27.1 Linux focused race suite | Pass, 10.255 s test time | Separate race build |
| Go 1.27.1 `externaljobs` Linux focused suite | Pass, 10.024 s test time | Tagged shared protocol |
| Go 1.25.0 Linux focused suite | Pass, 6.836 s test time | Minimum compiler |
| Go 1.27.1 Windows amd64 full requested suite | Failed | Initial restart-fixture timing assumption and four symlink-creation permission failures retained |
| Go 1.27.1 and Go 1.25.0 Windows amd64 corrected subset | Pass | Four permission-dependent symlink fixtures explicitly omitted |
| Go 1.27.1 Windows persistence vet | Pass | Static analysis |
| Go 1.27.1 Windows arm64 and macOS amd64 test packages | Compile pass | Cross-build only |

The native host is Windows NT 10.0.26300.0, x64, local C: NTFS. Its user account
cannot create the four symlink fixtures. The failed full run remains failed
evidence; the subset is not a passing full native campaign. The corrected subset
passed current/previous retention, private-ACL and hard-link rejection, unknown
entries, identity swaps, interrupted intents, snapshot pins, catalog-publication
failure, five Store restart/snapshot cycles and forced process termination after
each of four completed transitions. The final-intent replacement regression
passed natively on Windows and Linux.

The corrected Go 1.27.1 Windows test executable SHA-256 is
`4ebb467f0e9325170338ed34afc6386d648572a5ff3d017582334ca961c6d57e`;
the Go 1.25.0 executable is
`9f8a17dd373ef89aae9d72429c8479f2c207958475df8807c7fa19b6cc38f928`.
Their compile records and Linux race/minimum/tagged checks identify unchanged
source inventory `ff5128f8f5047fb8910a853300dcfc351df19de306f761baeefa0e234db66df9`.
Records precede this documentation update; no complete matrix is attributed
to a final source commit. Private logs retain every failed launch/compile/run.
A PowerShell invocation that returned no test exit status is explicitly not a
pass. The subsequent launcher captures the native process exit code and records
exclusions. Test fixture directories were removed after execution; two earlier
disposable executable copies were verified by digest and removed without
recursive directory deletion. Task-owned Go build caches were cleared after
the compilers stopped; evidence, toolchains and dependency inputs were retained.

## Review and remaining gates

The authorized independent reviewer closed the finite implementation and test
follow-up after the intent correction. That review was source-only; it did not
run native tests or certify a release.

Remaining storage work includes allocated-size accounting, measured Windows
mapping growth with frozen snapshot transactions, service-account/SID execution,
the four native symlink fixtures, Windows arm64/macOS native execution, the full
backup/restore/corruption matrix and final-source qualification. The 64 MiB
initial Windows mapping remains. Current counts and unlinked apparent bytes do
not establish allocated disk usage or prove physical reclamation. Provider and
comparative-performance gates remain open; the 24-hour run remains cancelled.
