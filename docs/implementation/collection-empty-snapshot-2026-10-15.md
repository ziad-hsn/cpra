# Empty collection snapshot recovery — 15 October 2026

Scope: private working-source checks after `16569a7` on `codex/release-repairs`.
These results do not qualify a frozen release, other native platforms, services
or packages. No public artifact or documentation publication is performed.

## Reproduced failure and repair

The finding that every startup creates a collection ledger was too broad.
Fresh empty stores remain lazy. A narrower failure is reproducible: after a
monitor snapshot advances to the streamed format, its empty collection
namespaces cause recovery to create a disk generation. Snapshot preflight also
creates a temporary disk ledger. On Windows each such ledger initially occupies
64 MiB. Generation retention bounds the completed copies, but they are still
unnecessary when the store has never used a collection.

The original regression fails on unchanged production source at `16569a7` with
`store without collection operations created a disk ledger`. Its log and original
test source are retained privately. The failed log SHA-256 is
`e3b4e5ff48307567968ca49b803eca7329d23c1791660dc5feae255a91390982`.

Recovery now validates an empty collection image against the exact empty
namespace stream without opening a disk ledger. It requires the namespace
order, framing, footer digests and end of input for the recorded format. Current
formats require 338 bytes; the comparison reads at most 339 bytes after the
header. Unexpected rows, missing namespaces, corrupt footers and trailing data
are rejected. Memory callers retain an empty memory ledger; disk callers receive
no collection ledger. Execution recovery accepts that absence only when no
collection headers exist.

The first subsequent collection still allocates durable storage. Existing
generation ownership, current/previous retention, reader/snapshot pins and
synchronous writes are preserved. There is no storage-format change or deletion
of existing generations.

## Recorded verification

The new regression exercises three snapshot/close/open cycles for each of an
empty store, an authenticated store and a monitor with retained incident events.
It checks the absence of a collection directory, unchanged monitor/history
state, then a real first collection upload and another snapshot/restart. Framing
tests reject malformed input in all four namespaces without creating a disk
generation.

| Selected persistence checks | Compiler | Race detector | Passing tests/subtests |
| --- | --- | --- | ---: |
| Snapshot, recovery, retained history, corruption and restore selection | Go 1.27.2 | Yes | 149 |
| Same selection, `externaljobs` | Go 1.27.2 | Yes | 149 |
| Same selection | Go 1.26.0 | No | 149 |
| Selection plus worker restart, isolation and format 41 cases | Go 1.26.0 | No | 165 |
| Worker restart, isolation and format 41 follow-up | Go 1.27.2 | Yes | 16 |

All five selected runs finish without failures, skips or unfinished tests. They
use `GOWORK=off` and the prepared private SDK cache. Go 1.27.2 static analysis
passes in default and `externaljobs` builds. Formatting and whitespace checks
pass. The selector contains the name of a local-administration integration test,
but these commands target only `internal/persistence`; that separate stopped
backup/restore integration test is not included in this result.

The new working-source executable was built on Linux with Go 1.27.2 and executed
natively on Windows AMD64, Windows NT 10.0.26300.0, fixed local NTFS. All seven
selected top-level tests and 29 subtests pass. They cover the two new regressions,
metadata initial/replacement writes and rejected replacement, retained history
and partial delivery across restart, locked/missing identity, and an incompatible
snapshot. This execution is not race-instrumented.

The executable SHA-256 is
`ed8c52da583501e0a30cdeada98b72cbebc8e901f5d36b31e6ff9532064d5a57`.
Its original stdout/stderr are retained with SHA-256 values
`ccd1455e64a87cfc382447751230abc9fa726d53e116bdf9039cef77a946a862`
and `b41a9d6d67b14afeef0d95944538711fe60bc493c3b54b3699e7606dcb853d3b`.
Ordinary Raft diagnostics on stderr are retained. Exit status, exact test
completion, failure/skip and fatal diagnostics remain mandatory checks.

The native helper uses fail-fast and finite process deadlines, and reserves
256 MiB for its executable and serial fixtures. No live directory traversal
interferes with Windows replacement/removal. Its before/after accounting is not
a measured peak or filesystem quota. After confirmed process exit the fixture
has no unexpected entries and is removed. Task-owned stopped compiler caches
and archived tool copies were removed separately; original evidence, source and
crash fixtures remain.

Independent source, Linux and native Windows execution-record reviews found no
remaining concrete defect. These bounded reviews are closed. Final-source
matrices, native platform/service coverage and the other release gates remain
required. The 24-hour endurance run remains cancelled.
