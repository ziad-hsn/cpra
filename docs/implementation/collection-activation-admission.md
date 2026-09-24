# Private collection activation admission

Status: implemented with independent review and a passing integrated regression
matrix on the private branch, 20 September 2026.
This historical slice admits an immutable activation intent. Later checkpoints
added item execution and [public Activate](collection-public-activation.md).
Candidates remain private until the agreed release gates pass.

## Original input and current authority

Admission starts only from an unexpired, complete owned upload with a finalized
plan and an original successful validation result already sealed in history.
The structural `validated` phase alone is insufficient. Input rows, plan
fragments and result rows must remain complete and unremoved.

The activation record binds one caller-generated identity and time to the
original input progress/count, finalized plan identity and complete descriptor,
validation identity and complete descriptor, capability profile and original
validation-request fence where present. The parent operation and upload identity
remain unchanged. No preparation or validation is repeated.

The command separately carries a fresh observation of durable operator authority.
Apply checks the same permanent actor and authentication epoch against current
policy at the supplied observation time. On first admission that observation
also becomes the immutable admission authority. A later exact retry may carry a
new current policy revision after legitimate token rotation; it cannot rewrite
the original activation record, plan or timestamp. Revocation, expiry, anonymous
access, policy reset and stale revision fences fail explicitly.

An admitted parent enters `applying` with zero item progress. No catalog resource
changes, child operations or provider calls occur in this slice. This internal
state must not be presented as completed collection application. Later item
commands must atomically record their original conditional catalog acceptance
and parent-linked child outcome as specified in the
[activation execution boundary](collection-activation.md).

## Lifecycle and observation

Inactive staging and applying are distinct states. The former upload activity
and expiry timestamps remain audit metadata after admission; they do not expire
an applying parent. Its authoritative input, plan and validation staging remain
retained until a safe terminal transition permits cleanup.

An ordinary process restart preserves the original activation. Explicit backup
restore invalidates its old execution epoch and does not recreate execution
permission from retained history. Exact retries return the original disposition
or a conflict; a canceled or restored operation cannot become applying again.

Cancellation of an admitted parent requires its exact activation fence and fresh
current owner authority. With no item execution in this slice, it can become
terminal immediately even after the former 24-hour upload deadline. Cancellation
retains the activation identity and emits only one original terminal receipt.
Bounded cleanup checks the activation fence along with all existing input, plan,
validation and request progress. A cleanup observation captured before admission
cannot delete the newly admitted state.

Point and list observations treat applying as live. Original validation reads
retain their separate 30-day lifetime: expired validation history does not expire
an admitted parent. Observations expose no encrypted payload, execution grant or
provider configuration. The nonterminal admission audit uses a distinct history
namespace; `collection/` remains reserved for immutable terminal receipts.

## Storage compatibility

The format-8 extension adds bounded activation metadata to the existing
state image. It preserves the input, plan and validation namespace streams and
introduces no fictitious item-outcome namespace. Formats 3–7 retain their existing
framing and behavior. Older formats must reject activation fields rather than
silently discard them. Online replay, frozen snapshots and stopped backup
validation use the same strict model and command checks.

A rejected committed format-8 command can still make an older reader unsuitable
for log replay, even when the state image has not advanced. Rollback therefore
requires a binary supporting every format written since the backup; editing a
format number or deleting staging files is not a downgrade procedure.

## Qualification boundary

Required evidence for this slice includes original intent/authority conflicts,
exact retries and rotation, frozen snapshots, formats 3–7 compatibility,
malformed binding rejection, real process termination around admission and
cancellation, offline validation, ordinary restart, explicit restore, and reads
past the original staging and validation-history deadlines. Those tests must
continue to establish zero catalog and child-operation mutations.

The completed integration matrix passed the full durable race suite (471.212 s),
affected management/server race checks (9.868/19.753 s), the full localadmin race
suite (10.825 s), affected Go 1.25 checks, the tagged admission matrix, vet and
19 release-recipe/publication-policy tests. Recorded source contents and file
inventory remained unchanged. Commands, logs and source hashes are in
`bin/verification/collection-activation-integration/followup/result.json`, with
the final inventory audit alongside it. Snapshot/process-specific evidence is in
`bin/verification/collection-activation-snapshot-crash/result.json`.

The first broad run failed an existing manual-cleanup fixture when ordinary
maintenance advanced its captured page fence. The original full failure,
reproductions and counter diagnostics remain preserved. The independently reviewed
test-only correction retains all four exact cleanup pages and intermediate
snapshot checks; ten race and ten minimum-Go repetitions passed before the full
rerun. See `bin/verification/collection-validation-cleanup-fixture/result.json`.
Future observation timestamps test lifecycle rules, not elapsed endurance.

Conditional item execution, bounded parent-linked outcomes, controller
reconciliation, cancellation after partial execution and public API/SDK/dashboard
activation remain separate unfinished work. No current validation or admission
test qualifies those behaviors or the broader shipping gates.
