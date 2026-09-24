# Private collection execution finalization

The private persistence layer can now finalize an admitted collection's original
execution facts. This is application storage **format 10**, with result projection
version 1. Coordinator-driven finalization is implemented. Format 11 adds
[private retained publication and result reads](collection-execution-publication.md).
Public activation, HTTP result pagination and execution cleanup remain separate work.
The result anchor does not claim that item results are available.

## Committed contract

`CollectionExecuteCommand{Action: "finalize"}` is an isolated command carrying
only its original `CollectionExecutionBinding` and a
`CollectionExecutionFinalizeFence`. The fence copies the complete bounded
execution progress and original cancellation/restore observation. Its
`Authority`, capability, preparation and ordinal fields must be empty. Finalizing
known committed facts does not require an active execution credential. Store
health, pending restore and authentication-reset fences still apply.

An applying parent must have decided every item, have no prepared candidate,
and have terminal evidence for every accepted child. The resulting state is:

- `completed` when every input was accepted or unchanged and all accepted
  children applied;
- `partial` when at least one input was accepted or unchanged but another
  decision or child disposition prevented full success;
- `failed` when no input was accepted or unchanged.

All accepted children failing projection is `partial`: the accepted catalog
changes remain committed, while the applied count is zero. An all-unchanged
collection is `completed` with no newly accepted changes or applied children.

Canceled and restore-invalidated parents finalize after their accepted children
settle. Their original state, stop time, cancellation identity or restore marker
remain unchanged. An activated parent canceled before execution began has a
nil progress fence and a fully unattempted inventory. Finalization never creates
an execution begin or a child for that inventory. A prepared but unaccepted
candidate remains committed metadata in the unattempted suffix.

`CollectionExecutionResultState.Summary` freezes the original binding, actor,
public inventory identity, activation time, exact progress/stop fence, outcome,
unattempted count and `FinalizedAt`. The original activation ID is the result
identity. In the retained progress, `Accepted` counts committed catalog changes;
`ChildApplied` counts applied children. The original outcome chain and final
child-terminal root remain distinct. Publication fields remain zero in format 10; format 11 adds an independent
retained-item descriptor and explicit expiry.

Finalization requires an observation at or after activation, execution progress
and the original stop. An exact retry at the same or a later observation returns
the original summary and finalization time without another event. It never
renews staging `ActivityAt`, allocates a child, changes the catalog mutation
sequence, or refreshes the original plan.

## Certification, history and recovery

The isolated prepass drops its disposable executor index before building one
bounded audit index. It releases the FSM mutex while verifying the selected
parent's original encrypted input, plan, sealed validation descriptor, immutable
conditional decisions, accepted-child identities and terminal evidence. It
rebuilds progress and terminal commitments and pages that parent's execution
namespace to exhaustion. Every frozen reader closes before the final mutation.
Unrelated retained collections are not rescanned during this finalization.
Global allocation uniqueness remains enforced at admission and complete
snapshot recovery. A valid admitted artifact that exceeds the executor audit
bounds before execution began returns the same `ErrCollectionQuota` as begin;
it remains retained and unfinalized without poisoning store health. This slice
does not introduce a larger-budget finalizer or authorize cleanup of that input.

The finalization emits a typed immutable anchor under
`collection-execution/<operationID>`. The anchor has its own history index;
primary and index records are checked on reopening. Unknown fields, malformed
summaries and conflicting anchor identities fail closed. Its event time is the
first `FinalizedAt`; its event ID uses the enclosing committed log position.
The ordinary history append and acknowledgement barrier applies. A newly
completed/partial/failed parent also gets its terminal collection receipt.
Previously stopped parents retain their existing receipt byte-for-byte.

Format 10 snapshots include the full execution namespace and immutable result.
Format 9 snapshots remain readable without results; lower or unknown formats
cannot silently accept result state. Existing `begin`, `prepare` and `decide`
writes still use format 9. The optional nested finalization field is omitted
from their JSON, preserving their historical bytes. No top-level command field
or version-1 operation/authentication digest projection changed.

New activation-fenced cleanup writes use format 10 and refuse to delete admitted
input, including cancellation before begin. Background maintenance skips these
parents. Historical format-8/9 cleanup entries retain their original replay
behavior. All execution/result-bearing parents continue to refuse cleanup until
the later publication/retirement contract is implemented.

## Verification boundary

The focused `TestCollectionExecutionResult*` suite exercises:

- Real admission, decisions and child settlement for completed, partial,
  unchanged and conflict-only outcomes; delayed immutable retries and detached
  returned values.
- Pending children, stale progress, backward observations, revoked execution
  authority, original cancellation, abandoned preparation and cancellation
  before begin.
- Missing input/decision/terminal evidence, unexpected execution rows, changed
  summary facts, unproved publication, older-format result rejection and exact
  historical format-9 command bytes.
- Native log replay and snapshot reopen, `LockOffline` stopped-store inspection,
  actual explicit restore of a pending child followed by fresh authentication,
  immutable restore disposition, and anchor primary/index corruption.
- Pre-begin retention, the original format-8 cleanup replay semantics, and
  matching begin/finalize quota rejection for a valid oversized admission.

Existing operation/authentication digest goldens and format-9 execution recovery
checks run with the focused race campaign. Release-recipe tests assert that the
reader's maximum supported format and the private candidate recipe both report the current maximum format (now 11). These historical format-10
checks do not establish public activation readiness. Format-11 retained publication
has its own scoped verification. A process-kill campaign specifically at the new finalization/history
boundary has not been run; ordinary reopen and explicit restore are separate
observations, not substitutes for that crash qualification.
