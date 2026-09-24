# Collection execution retirement

The private candidate has registered commands for execution retirement in
storage format **12** and source retirement in format **13**. Execution retirement
removes original outcome/terminal pairs and any abandoned prepared record.
Source retirement then removes validation rows, plan fragments and encrypted
input, in that order, before deleting the collection header. Automatic
maintenance selects these commands. The subsequent
[public activation implementation](collection-public-activation.md) connects
Activate and Apply; its evidence is separate from this retirement checkpoint.

The package is `internal/persistence`, with Go package name `persistence`.
This describes its storage responsibility; it does not change the on-disk data
directory or rename a user's existing store.

## Committed boundary

`CollectionExecuteCommand{Action: "retire"}` carries an original execution
binding and a fence over the immutable result and expected retired prefix. It
contains no resource payload, refreshed conditional guard or execution grant.
Only a finalized terminal parent with a committed publication seal or explicit
history-expiry disposition qualifies. Accepted children must already have
retained terminal records and no pending operation or reservation.

Before the first removal, the isolated FSM transition freezes its current ledger
generation. It verifies the original input and plan, sealed validation rows,
execution commitments, published-prefix commitment and exact physical execution
namespace. It releases the FSM mutex for this work while retaining exclusive
transition ownership, and closes the frozen reader before opening a deletion
transaction. Failed certification makes storage unavailable; it does not advance
the header or refund execution quota.

One synchronous bbolt transaction, or the equivalent memory transaction, removes
at most **256 records or 4 MiB**. An accepted outcome and its matching terminal
are inseparable. Their removal refunds the original 4,096-byte terminal reserve;
no reserve is refunded from a count alone. Nonaccepted outcomes have no terminal.
An abandoned prepared slot is removed after the original processed prefix, and
only within the same record/byte limits.

The committed `ExecutionRetirement` state captures the original result,
checkpoint, prepared-slot disposition and supplied observation times. The
original `Execution` and `ExecutionResult` facts remain immutable except for the
existing later history-expiry marker. Original byte totals describe execution;
remaining physical charge is derived separately from the retired checkpoint.

A repeated compatible old fence returns the existing receipt without another
deletion or refund. An execution-only fence may conflict once source retirement
has advanced. A delayed pre-expiry command remains bound to its original observation
time. Replay follows committed publication/expiry facts rather than selecting a
different prefix from the current wall clock or local history availability.

## Source retirement

`CollectionExecuteCommand{Action: "retire_sources"}` requires completed execution
retirement. It binds the original result and exact source-removal counters; it
cannot change activation, item decisions, child outcomes or original descriptors.
One transaction removes at most 256 records or 4 MiB from one source namespace.
The FSM compares the actual deletion to its verified selection before committing
the updated header and quota accounting.

The `ExecutionRetirement.Sources` state stores three rolling commitments for the
surviving input, plan and validation prefixes. Counts and byte totals alone cannot
detect a same-length substitution after a tail has been deleted. Each remaining
prefix must match its committed digest as well as its physical records, indexes
and source relationships. The existing digest algorithms are reused. Original
full-source commitments remain unchanged.

A cold cleanup pass verifies the entire selected parent's remaining source and
records sparse digest checkpoints at the actual deletion boundaries. Subsequent
passes use one generation- and fence-bound certificate: they run the existing
bounded tail selector and hash the selected frames from the certified surviving
digest to the current committed digest. The warm path examines at most 512
decoded records / 8 MiB, plus one excluded frame-length probe of at most 2 MiB
when the byte limit chooses the boundary. It does not copy memory ledger maps.

The certificate has a separate 4 MiB logical metadata allowance, independent of
the executable/publication index budget. It retains no source frames, plaintext,
codec, transaction or frozen reader. It advances only after verified deletion,
and a mismatched fence, replaced ledger, restore or source-retirement failure discards it.
Recovery always uses the full source auditor. Corruption outside a warm selected
tail is detected when selected or during a cold/recovery audit; a warm command
does not rescan the whole store. Cold-pass latency at the maximum admitted
collection size remains a measurement boundary, not a five-second guarantee.

Automatic maintenance admits one command per turn after metadata-only checks of
the retained execution and validation results. Unexpired missing evidence fails
unavailable. Explicit committed history expiry permits retirement after the
retention cohort ends. Transient leadership loss or a canceled metadata read is
retried; it does not certify missing evidence or reopen admission.

Before final header removal, the command advances the persisted history cutoff
using its replicated observation time. Replaying an older command cannot revive
expired history, recreate a header or refund quota twice. The generic cleanup
command remains unavailable to execution-bearing parents.

## Snapshots and restart

Format 12 adds an explicit snapshot frame and permits original execution suffix
ordinals. Import offsets come only from validated retirement headers; older
snapshot import retains its zero-offset requirement. Earlier format-10/11 result
and publication records preserve their existing encoding and replay semantics.

Format 13 adds explicit source-retirement framing. A validated Sources marker
permits shortened plans for completed, partial and failed outcomes; older
formats retain their original rules. Recovery verifies the surviving source
prefixes and requires an empty execution namespace. It installs no execution
index or child ownership. Cleanup progress is not permission to execute a plan.

Recovery reads the retained original source artifacts and reconstructs the final
outcome digest, terminal root, counters and charges from the retired checkpoint
plus every remaining row. It rejects missing suffix rows, extra physical rows,
resurrected prefix rows, unpaired terminals, altered prepared records and pending
children. Recovery installs no executable index, terminal tree, outcome cache
or unresolved child ownership for a retired parent.

A canceled parent that never began execution has no checkpoint and must have an
empty execution namespace. A canceled parent with an unaccepted prepared record
preserves its original identity until that final record is removed. Neither case
can create or repeat a catalog mutation during recovery.

The generated ledger is recoverable from the committed snapshot/log sequence.
Native process tests kill an owner after a committed first deletion and after
completion, then exercise offline inspection and reopen. This tests process
termination at those boundaries; it is not a power-loss guarantee or evidence
for an untested operating system/filesystem.

## Result access independent of staging

Protected point and page reads can recover read authority from retained history
after a source header disappears. The lookup seeks bounded segment indexes for
the original anchor, checks its owner, and verifies the matching seal and
publication progress before exposing item rows. Primary records and derived
indexes must agree. Missing unexpired evidence is unavailable, never a fabricated
pending or empty successful result.

Existing cursors keep their immutable original summary, descriptor and watermark
when a header disappears. Final metadata checks fence epoch changes, operation
high-water changes, storage failure, replaced history and committed retention
cutoff. They do not hold history locks. An older cancellation receipt can expire
before a late final result; execution history supplies that result's independent
30-day cohort without changing the original cancellation.

Operation listing merges retained execution anchors with earlier terminal
receipts and captured headers. It emits each original operation once, preserves
owner and watermark checks, and reads metadata rather than item bodies. Native
history seeks past each operation's item namespace; memory history maintains an
ordered anchor index. A shared inspection budget bounds one list page. Final
response admission repeats epoch, health and retention checks after policy or
cursor waits without opening history transactions.

Tests exercise real source-retirement commands as well as isolated retained-read
fixtures. Native restart keeps the original result accessible after all source
namespaces and the header have been removed.

## Verification and remaining work

The format-12 checkpoint evidence records exact commands, retained failures, corrected
reruns and source hashes in
[`bin/verification/collection-execution-retirement-2026-09-23/result.json`](../../bin/verification/collection-execution-retirement-2026-09-23/result.json).
It covers paired transactions, quota refunds, stale retries, frozen readers,
failed native writes, partial-prefix recovery, corruption, committed history
expiry, process termination, retained reads and historical format compatibility.
Independent reviews cover the primitive, recovery, command integration and
protected history boundary. The record identifies which checks used race
detection or Go 1.25.

The current format-13 qualification adds partial-source snapshots, strict prefix
commitments, actual header deletion, retained listing and forced termination at
the validation, plan, input and header boundaries. The
[source-retirement record](../../bin/verification/collection-source-retirement-2026-09-24/result.json)
records the separate current checks and source hashes. Warm verification uses
the bounded certificate described above. Cold audits retain full verification;
their maximum-size latency has not been benchmarked by these regressions.

Public activation, the shared SDK/CLI/dashboard Apply flow, optional external
workers and the remaining distribution gates are still required before shipping.

Candidates remain private. Provider-account verification and the selected
million-monitor comparative/24-hour campaign are separate outstanding gates.
