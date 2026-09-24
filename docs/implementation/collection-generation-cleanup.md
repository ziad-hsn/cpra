# Collection generation cleanup: implementation handoff

**Status: isolated Linux retirement primitive implemented and reviewed; automatic
generation cleanup remains unfinished and disabled.** The running Store still
preserves previous materialized generations. The primitive's passing tests do
not establish a bound on physical disk consumption or qualify automatic cleanup.
This document records both that implementation and the remaining storage work
under the [approved management API plan](api-management-plan.md); it does not
replace that plan or satisfy release gates.

## Invariant and ownership

Raft logs and self-contained collection snapshots are authoritative. A collection
ledger generation is a derived local database. Cleanup may remove only positively
identified completed generations that are no longer selected or retained. It
must never remove Raft data, history, snapshots, the current generation, a live
generation handle, or an unknown/incomplete/invalid directory.

The runtime must retain the main `raft.db` ownership lock throughout cleanup.
Only the normal `Store` lifecycle schedules cleanup, after snapshot/log recovery,
explicit restore fencing, `collectionLedger.Publish`, and successful startup.
Generic `Publish`, `FSM.Apply`, snapshot decoding, stopped backup validation and
administrative authentication operations do not invoke cleanup. Use a separate
maintenance worker; neither the controller owner loop nor request handlers do
directory scanning, unlinking or synchronous filesystem maintenance.

Initially keep **the selected current generation plus one other completed
generation**, choosing the latest completion-marker modification time and using
the canonical generation name as a deterministic tie breaker. This timestamp is
only a cache-retention preference; it is not a source revision, Raft index or
recovery authority. Exclude generations already under an admitted retirement
intent from the set of completed retention candidates.

Start with startup-originated collection: all earlier process generations are
closed, and `machine.Restore` closes a replaced ledger before installation. Do
not generalize this to deletion during a future live generation replacement
without tracking and retaining every pinned database/snapshot handle.

## Candidate eligibility and anchored access

A new retirement candidate must satisfy all of these checks:

- Its basename is exactly `generation-<canonical UUID>`.
- It is a private real directory with the supported ownership and native
  filesystem protections; symlinks, reparse points and mount boundaries are
  ineligible.
- Its complete entry inventory contains exactly two regular files:
  `ledger.db` and `generation.json`. Read at most three names to detect an
  unexpected extra entry; do not recursively traverse it.
- `generation.json` is at most 4 KiB, has the supported strict schema, and names
  the same generation. Its bytes are recorded by digest in the retirement
  intent. Reject trailing content, unknown fields and mismatched identities.
- It is different from current, the retained previous generation, and every
  pinned identity. Compare directory and database file identities as well as
  names so aliases to protected files cannot qualify.

Here, completion eligibility means the runtime's supported completion marker
and directory shape validate. It does **not** certify every database page again.
Any corruption or invalid-generation condition already found by storage
validation excludes the generation. A full historical database integrity scan
would be separate, incremental work; do not imply that bounded GC validates all
old database contents.

Open the collection directory with Go 1.25 `os.OpenRoot`. For each candidate,
compare its `Lstat` identity with the directory obtained by `OpenRoot(name)` and
hold that directory handle through the operation. Use fixed leaf names through
that handle, and compare `os.SameFile` identities again before removing the
parent entry. Never call `RemoveAll` or construct a deletion target from an
arbitrary path contained in metadata.

`os.Root` alone is insufficient: it follows relative symlinks within the root
and does not prevent traversal of mount boundaries or bind mounts. Native
eligibility checks must reject these cases, including aliases to current state.
Where the platform cannot establish the required identity/mount protections,
report cleanup as unavailable and preserve the candidate. Go 1.25 provides the
required `Root.OpenRoot`, `Lstat`, `OpenFile`, `Rename` and `Remove` methods; verify
the actual platform implementation and qualify Windows reparse/handle behavior.

## Bounded inventory and retention selection

Use a two-pass directory scan with an open directory cursor retained between
maintenance steps. The first pass chooses the retained previous generation and
collects bounded accounting. The second pass retires eligible older generations.
Do not materialize the whole directory listing or all candidate names in RAM.

Initial budgets per maintenance step:

| Work | Bound |
| --- | --- |
| Collection-root entries examined | 128 |
| Candidate directories inspected | 16 |
| Candidate child names read | 3 |
| Any selector, completion marker or retirement intent | 4 KiB |
| Active retirement intents | 1 |
| Retirement state transitions | 1 |
| Candidate file or directory unlinks | 1 |
| Cooperative work budget | 25 ms |

Stop at the first applicable bound. Filesystem calls, especially sync/unlink,
cannot be promised to finish within 25 ms; run them outside controller/Raft
execution and account for elapsed time. Schedule the next step after one second
while there is actionable work, and after 60 seconds when idle or blocked.
Cancellation closes scan handles after the current filesystem call returns.

Complete the retention-selection pass before admitting a new retirement. Resume
an existing valid intent first. If the current selection or native directory
identity changes, abandon the scan and revalidate before any deletion. Directory
mutation can make a directory cursor miss entries; repeat completed passes until
there are no eligible older generations. Do not claim a complete inventory while
a pass is unfinished or known entries cannot be inspected.

## Durable retirement intent and interrupted unlink

Use one fixed, private `gc-retirement.json` sidecar in the **collection root**.
Its versioned strict schema records the store/node identity, canonical generation
name, original completion-marker digest, and the identities needed to recheck
the admitted candidate. It contains no provider data, full paths or secrets.
File identity fields are platform-specific evidence, not portable Raft identity;
when they cannot be verified after recovery, retain the files and report that
cleanup needs attention.

The intent lives outside the directory being removed so a crash cannot strand
an unmarked, partly removed directory. Use this sequence:

1. Revalidate successful current publication, the main ownership lock, retention
   selection, pinned identities and the complete candidate shape.
2. Create and durably publish the retirement intent without replacing an
   existing unresolved intent. Sync the intent file and collection directory
   before performing any unlink.
3. Reopen/revalidate the anchored candidate against the intent and current
   selection. Remove `ledger.db`, then sync the candidate directory. Missing
   files are acceptable only when continuing this validated intent.
4. Remove `generation.json` and sync the candidate directory. The root intent
   remains present throughout this step.
5. Confirm the candidate is empty, confirm its native identity and current
   selection again, remove that exact directory entry, and sync the parent.
6. Remove `gc-retirement.json` last and sync the collection directory.

Each numbered transition is restartable and bounded. An interrupted or failed
write before durable intent publication permits no deletion. After admission,
already-missing expected files are idempotent progress, while additional files,
changed completion markers, changed identities or links stop cleanup. A missing
candidate directory permits removal of a valid matching intent after rechecking
current selection. Never convert an ordinary incomplete generation into a
retirement candidate merely because some files are missing.

Unix implementation should sync the relevant pinned directory handles. Windows
must use a qualified native durable-metadata publication/removal procedure;
`os.Root.Rename` by itself does not establish durable replacement. Do not reuse
an absolute-path helper in a way that loses the pinned-directory protections.
Keep automatic cleanup unavailable on a platform until those native guarantees
and interruption tests are implemented; do not silently ignore sync errors.

## Physical usage and error reporting

Publish cached maintenance observations rather than scanning from `/state`:
selected generation, retained/completed/retiring/incomplete/unknown counts,
known apparent bytes, optional allocated bytes, scan coverage, last successful
step, bytes unlinked, and a bounded reason code for blocked/error status.

`FileInfo.Size` measures apparent bytes, not necessarily allocated filesystem
space. Report allocated bytes only when a qualified platform implementation can
measure them, with explicit availability. Unknown directory contents are outside
the known-byte subtotal; do not label that subtotal as complete disk usage.
An unlink does not prove disk space was reclaimed while another process holds a
file open. Keep free-space observations distinct from logical row accounting,
file size and bytes targeted by cleanup.

Failed or incomplete GC preserves the current runtime and reports a separate
maintenance problem. It does not claim that Raft is corrupt or that configuration
was lost. Existing readiness behavior still applies if actual durable writes
fail. Unexpected current selection, ambiguous intent publication, unsupported
native identity checks, read errors, sync failures or deletion failures halt the
affected retirement; do not retry destructive steps against unchecked paths.
Keep status/log messages redacted and bounded.

The policy bounds normal **completed-generation** accumulation. It does not
bound incomplete failed-recovery generations, unknown files, filesystem
allocation or retained authoritative snapshots/history. Preserve those evidence
boundaries and surface remaining space rather than advertising a total disk cap.

## Required verification before enabling cleanup

- Repeated successful restarts retain current plus the selected previous
  completed generation and remove eligible older generations within the budgets.
- Failed startup/replay/publication admits no new retirement. Startup without a
  snapshot and restoration from older self-contained snapshots remain correct.
- Real-process interruption at every intent/sync/unlink boundary resumes only
  the original admitted retirement and preserves current/retained generations.
- Open/frozen generation handles remain pinned; alias, directory-swap, symlink,
  mount/bind-mount and Windows reparse cases cannot reach protected state.
- Unknown names, future schemas, missing ordinary completion markers, extra
  entries, malformed intents and mismatched store identities remain untouched.
- Interrupted file deletion, sync failure, permission denial, read-only storage
  and disk-full intent creation produce explicit maintenance state without
  deleting current, Raft, history or snapshot files.
- Large directories use bounded scans and constant candidate-selection memory;
  canceled work closes handles and blocks no controller loop.
- Empty-directory/intention completion is idempotent; observed physical usage
  distinguishes known, partial and unavailable measurements.
- Complete stopped backup and explicit restore remain valid with a pending
  retirement intent, and offline validation performs no GC.
- Go 1.25, the supported compiler, race checks and native filesystem tests pass;
  independent review confirms the ownership and filesystem boundaries.

## Implemented private primitive and evidence

`internal/persistence/collection_ledger_gc*.go` implements retirement of one supplied
candidate through a mandatory trusted guard. The guard must protect the current
selection, retained previous generation, all reader pins and main-store ownership
through each step. This is an internal contract, not proof that the Store already
provides those protections. No startup, replay, publication, backup or maintenance
path calls the primitive yet.

The Linux implementation uses anchored roots, exact private schemas, owner and
permission checks, and native file/directory/mount identities. It refuses aliases,
unknown entries and ambiguous state. Each step publishes an intent or performs
at most one unlink, with directory synchronization. Other platforms return
cleanup unavailable and preserve all files. This implementation does not defend
against hostile code running as the same account outside its required ownership
guard.

Focused tests passed on Go 1.27.1 with the race detector in 5.640 seconds and on
Go 1.25 in 3.601 seconds; durable-package vet passed. Native temporary-directory
tests actually exercised bind-mounted directories and database files, an actual
directory-sync error, and SIGKILL after four completed transition boundaries.
Those process tests do not simulate power loss or interruption inside a sync
syscall. Go 1.25 Windows/amd64 and Darwin/amd64 test executables cross-compiled;
neither platform was executed natively for this work. Private evidence is in
`bin/verification/collection-generation-retirement/result.json`.

Bounded directory scanning, retention selection, Store lifecycle integration,
real reader-pin registration, physical accounting and maintenance reporting
remain to be implemented and qualified before automatic cleanup can be enabled.
