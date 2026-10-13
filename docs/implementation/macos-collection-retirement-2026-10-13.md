# macOS collection retirement implementation

This is development work after `bde6cf1` on `codex/release-repairs`, dated
13 October 2026. It is not native macOS qualification or a frozen release.
The [approved repairs](release-repair-plan.md) retain the Store ownership,
reader/snapshot pins and complete stopped-backup contracts.

## Implementation and boundary

The bounded retirement engine is shared by Linux and macOS. Linux's native
identity check and private intent format are unchanged. The engine keeps the
current completed generation, previous completed generation and pinned readers;
it admits an external intent before removing one expected entry per step.
The 25 ms budget remains cooperative around filesystem operations.

The [macOS identity implementation](../review/source/internal/persistence/collection_ledger_gc_darwin.go.txt)
reads metadata from open descriptors. It requires local APFS, matching filesystem
and device identity, nonzero persistent file ID and birth time, the executing
owner, private modes and a single regular-file link. It rejects symlinks,
unexpected entries, replacement identities and unverifiable candidates through
the [shared engine](../review/source/internal/persistence/collection_ledger_gc_engine.go.txt).

An additional descriptor-based `fgetattrlist` call verifies the absence of an
extended ACL. The response buffer is fixed; full-size reporting distinguishes a
truncated ACL from an empty reference. Every ACL or malformed response blocks
cleanup. Other filesystems report unavailable. This deliberately conservative
policy does not change manifest, Raft or history formats and is not a defense
against hostile code running as the same account outside the Store guard.

Apple's [file-status contract](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fstat.2.html)
documents file IDs, link count, owner and birth time. Its
[mounted-filesystem contract](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/statfs.2.html)
documents filesystem identity. The ACL request layout and empty-reference behavior
were checked against Apple's [attribute definitions](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/attr.h)
and [attribute packing implementation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/vfs/vfs_attrlist.c).
No Apple source implementation was copied into CPRa.

The existing `os.File.Sync` calls remain. Both pinned Go toolchains implement
Darwin file synchronization with `F_FULLFSYNC`, falling back to plain `fsync`
only on `ENOTSUP`. This was checked in the installed, checksum-pinned Go 1.25.0
and 1.27.1 `internal/poll/fd_fsync_darwin.go` and `os/file_posix.go` sources.
No extra synchronization wrapper or universal power-loss guarantee is added.
Native filesystem behavior remains part of the outstanding gate.

## Private checks

The shared native tests now compile for macOS, including real-process
termination after each completed retirement transition and Store-generation
retention/restart tests. macOS-specific tests cover identity validation,
ambiguous ACL buffers and an actual ACL created using the native utility.
Those macOS scenarios have not been executed on this host.

| Check | Observed result | Evidence boundary |
| --- | --- | --- |
| Go 1.27.1 Linux focused generation/retirement suite | Pass, 11.674 s test time | Native Linux/WSL, required identity flag, no skipped cases |
| Go 1.27.1 Linux focused race suite | Pass, 15.313 s test time | Same native cases, separate race build |
| Go 1.27.1 externaljobs Linux focused suite | Pass, 11.607 s test time | Shared protocol with tagged application types |
| Go 1.25.0 Linux focused suite | Pass, 11.480 s test time | Minimum compiler, native Linux/WSL |
| Go 1.27.1 macOS amd64 and arm64 test packages | Compile pass | Cross-build only; no execution |
| Go 1.25.0 macOS amd64 test package | Compile pass | Cross-build only; no execution |
| Go 1.27.1 macOS persistence vet | Pass | Static analysis only |

The focused pattern includes `CollectionGC`, `CollectionGeneration`,
`CollectionRetirement` and `StoreCollectionGenerations` tests. No provider jobs
are admitted by these fixtures. Private logs and source inventories remain in
the task-owned verification directory. The first Linux development check had
test extraction edits during its source inventory; its passing result is not
attributed to a frozen source. The subsequent scoped runs retained unchanged
source inventories. The first amd64 compile record identified its `env` wrapper
in the compiler-description field; its explicit compiler path remains recorded.

Required native Linux/macOS jobs set `CPRA_REQUIRED_COLLECTION_GC=1`. Lack of
filesystem identity evidence then fails the primitive fixture rather than
counting a skip as qualification. Windows remains outside this implementation:
it reports cleanup unavailable, and its retirement implementation and native
execution are still open. Allocated-size accounting, the complete backup/
corruption matrix, every claimed native target and final-source qualification
remain open.

## Independent source review

The user-authorized reviewer found no actionable source defect in the extension
from `bde6cf1`, including shared tests and the required-native flag. The reviewer
checked the ACL ABI against Apple source, the unchanged Linux native identity
and shared retirement ordering, protected pins and test preservation. The
initial synchronization concern was withdrawn after checking the pinned Go
Darwin build constraints and `F_FULLFSYNC` implementation. No tests, builds or
native macOS operations were run by that reviewer. This disposition closes the
finite source review, not native execution or release qualification.

The [14 October Windows record](windows-collection-retirement-2026-10-14.md)
describes subsequent engine extraction and a final-intent replacement correction.
The checks above remain evidence for the 13 October checkpoint.
