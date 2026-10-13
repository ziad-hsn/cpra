# Collection file storage accounting

Private development checkpoint after `649bc32`, dated 14 October 2026, on
`codex/release-repairs`. This implements the completed-generation file subtotal
in the [repair plan](release-repair-plan.md). It is not full data-directory
accounting or final release qualification.

Implementation checkpoint: `df82cf8`. Documentation below was refreshed after
that code commit; the complete release matrices are still pending.

## Implementation and contract

The [native sampler](../review/source/internal/persistence/collection_usage_native.go.txt)
runs under the existing local generation mutex in the maintenance worker,
outside controller/FSM locks. It samples at most one catalogued generation and
two fixed files per step, without reading their database/provider contents.
Native handle, ownership, permission and identity checks are the same boundary
used for retirement. Status reads return immutable scalar observations and
perform no filesystem I/O. Sampling does not mutate retirement authorization,
publish intents or delete files; sampling errors do not close admission.

The cache is bounded by the 512-generation catalog and prunes retired entries.
Failed refreshes remove the affected sample. Overflow guards preserve
nonnegative byte sums. Coverage distinguishes pending, partial, complete and
unavailable catalog samples; the oldest contributing time remains visible.
This is an incremental subtotal, not an atomic filesystem snapshot. Incomplete
generations, unknown files, root metadata, Raft, snapshots, history and physical
host allocation are outside its scope.

Apparent file lengths remain separate from filesystem-reported allocation.
Linux/macOS use descriptor `st_blocks` in 512-byte units. Windows uses the
documented [FILE_STANDARD_INFO layout](https://learn.microsoft.com/en-us/windows/win32/api/winbase/ns-winbase-file_standard_info)
and `AllocationSize`; negative values, invalid queries, delete-pending files
and directories cannot become an allocation measurement. The supported
64-bit Windows structure is 24 bytes. API/SDK optional fields omit unavailable
bytes while preserving measured zero. The dashboard applies bounded decoding,
coverage descriptions and explicit exclusions. No persisted format changed.

## Private evidence

| Check | Result | Scope |
| --- | --- | --- |
| Go 1.27.1 Linux storage/HTTP focused suite | Pass | Native WSL/Linux; no skipped native identity cases |
| Go 1.27.1 Linux focused race suite | Pass, persistence 24.730 s and HTTP 1.242 s | Separate race build |
| Go 1.25.0 Linux focused suite | Pass, persistence 19.861 s and HTTP 0.075 s | Minimum compiler |
| Go 1.27.1 `externaljobs` Linux focused suite | Pass, persistence 13.375 s and HTTP 0.114 s | Tagged schema and application |
| Go 1.27.1 SDK API suite | Pass | Public model tests |
| Go 1.27.1 and Go 1.25.0 CLI observation cases | Pass | Human output, JSON/YAML parity, measured zero and unavailable allocation |
| Go 1.27.1 and Go 1.25.0 Windows x64 corrected subset | Pass | Native local NTFS; four permission-dependent symlink fixtures explicitly omitted |
| Go 1.27.1 macOS amd64 test package | Compile pass | Cross-build only |
| SDK generation in two isolated roots | Identical outputs matching checked-in files | Pinned local generator and compiler |
| Full dashboard build, type checks, lint and tests | Pass, 541 tests in 34 files | Node 24.21.0, pnpm 11.22.0, Go 1.27.1 browser parser; generated assets/notices refreshed |

The focused Go pattern includes collection GC/generation/retirement/usage,
sparse allocation, Store restart/snapshot cycles, wire availability and the
existing SDK state-observation contract. Linux's sparse-file fixture proves
apparent length is not substituted for allocated bytes. Tests also cover one
generation per sample, cached reads, failed refresh, uncatalogued files,
retired-sample pruning, lazy empty storage and measured-zero rendering.

Both native Windows compilers observed an empty completed generation with
**67,108,941 apparent bytes** and **67,108,944 filesystem-reported allocated
bytes**, under a **1,073,741,824-byte logical quota**. This confirms the 64 MiB
initial mapping does not preallocate that full quota. It does not test later
growth or establish total filesystem/host usage. The retained earlier full
native failure and its four unavailable symlink fixtures remain visible in the
[Windows retirement record](windows-collection-retirement-2026-10-14.md).

The Go 1.27.1 and Go 1.25.0 Windows test executable SHA-256 values are
`8d2e3af67fef36e83bafcd6425333be953fbbaf23e5f825a5313ba7cc465b9c2`
and `76b174f7b440cc11a70120cc3d99eebbe970119f96823affc28f28f933b402bc`.
Compile and Go test records identify unchanged source inventory
`0a8b7273572f252952fc6a469ae9262d7dd54ce018e71762f3e42cf1c10582dc`.
The dashboard rebuild deliberately changes generated files; its inventory
therefore is not described as unchanged or frozen. SDK generation hashes match
both isolated roots. Private logs retain failed initial tool/cache/root
invocations separately from these passing checks.

## Review and remaining gates

The independent reviewer closed the finite source change with no concrete
finding, including cache bounds, native ABI, overflow, availability, generated
schema parity, dashboard decoding and the CLI follow-up. That reviewer ran no
tests or builds. The application and CLI build from the private development
workspace; that is not downloaded-module or official distribution qualification.

Growth while snapshot transactions are frozen, native macOS/Windows arm64 and
service identities, the four Windows link fixtures, full backup/restore and
corruption, and complete frozen-source distribution qualification remain open.
The byte subtotal intentionally does not certify all local storage. Provider
accounts and comparative performance remain separate gates. Candidates remain
private; the 24-hour run remains cancelled.
