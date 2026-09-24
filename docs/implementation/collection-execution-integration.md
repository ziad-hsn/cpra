# Private collection execution and storage format 9

Status: the private command/snapshot boundary passed independent review,
scoped default/tagged race and Go 1.25 checks, connected backend tests, and the
full durable race suite (449.031 s). Later management coordinator and preparation
cache changes have separate evidence on the private `codex/dashboard-finalization`
working tree. Exact source boundaries are recorded in the
[implementation progress log](dashboard-implementation-progress.md).
This is not a public Apply or shipping claim. All candidates remain private.

## Conditional command boundary

Three internal commands operate on an already admitted original collection:

| Command | Durable effect |
| --- | --- |
| `begin` | Initialize progress for the original activation, plan and result |
| `prepare` | Retain one exact encrypted candidate for the next original plan row |
| `decide` | Commit that row's conditional disposition and any accepted ordinary configuration child |

Commands include current named-operator authority and the original capability
profile. Those checks remain separate from immutable admission identity. A
canceled parent, changed authority, incompatible profile or expired operation
epoch cannot reuse an old grant. The old staging expiration does not stop an
already admitted applying parent.

Each execution command occupies one Raft entry. Submission rejects mixed command
lists, batching preserves ordinary/execution/ordinary ordering, and replay rejects
a mixed committed envelope before any command runs. Ordinary monitoring and
lifecycle commands retain their existing batching.

Before mutation, a bounded verifier reconstructs the complete original input,
plan and sealed result outside the FSM state lock. A separate transition mutex
keeps Apply and Restore from replacing that generation during verification.
The verifier closes its frozen reader before reacquiring the state lock. At most
one immutable plan index remains cached; its identity includes the original
artifacts and ledger generation. Eviction changes cost, never authorization or
the conditional result. Replay uses fixed work limits without a caller deadline.

Under the state lock, item acceptance checks original target and dependency
guards, using only certified earlier outcomes from that collection. It cannot
substitute a newly fetched version or recompile the original plan. Unchanged,
conflicting and dependency-blocked rows have no fabricated child receipt.
If the original target required for preparation has changed, the no-candidate
path can record the verified target conflict without constructing a replacement.

The ledger transaction consumes the exact prepared slot, appends the immutable
outcome and records any superseded earlier collection child. Both parents are
included when that child belongs to another collection. Only after the write
succeeds are catalog state, operation allocation, headers and derived indexes
installed. Native write failure leaves those active values unchanged and makes
storage unavailable. The existing history commit barrier still precedes an
acknowledged successful Store submission.

## Recovery and compatibility

Format 9 adds the mandatory execution stream after encrypted input, plan and
validation namespaces. A snapshot freezes all four namespaces with the same
image. Import builds a fresh unpublished generation, validates every namespace
and rejects trailing or incomplete data. Execution verification reconstructs
progress, canonical outcome commitments, child-terminal roots and prepaid
capacity; it also checks cross-parent child identities and mutation tokens.

Restore and stopped offline log validation rebuild the bounded pending-child
links and outcome certificates before ordinary completion or supersession can
run. A missing cache cannot be interpreted as absence of pending children.
Restoration executes no providers. A merely prepared candidate remains prepared;
reconciling an accepted decision keeps its original child and mutation token.

Release metadata records `storage_format_version: 9`, the highest format this
binary understands. Stores remain at their existing image version until a newer
feature commits. Once format-9 log entries or snapshots exist, rollback requires
a reader that understands them, including rejected committed execution entries.
Back up the complete stopped directory and keep its matching wrapping keys
separate, following the [persistence contract](../collection-persistence.md).

## Protected candidate preparation

Preparation verifies the original encrypted item, its inventory authentication
code and its exact plan/source coordinates. Omitted write-only values can be
preserved only from the original target incarnation and revision. It never
calls the ordinary fresh-plan preparation path or constructs providers.

The preparation holder copies only the next row, encrypted input, original target
and optional committed candidate. It closes its database transaction and drops
the full plan/catalog views before returning. Encryption and subsequent authority
or target checks therefore do not pin a reader needed by another writer. Existing
or locally cached candidate identities and ciphertext are reused exactly; changed
state rejects the operation rather than silently selecting a replacement.

The verified index now includes the canonical encrypted input frame hash, charged
at 32 bytes per row. Reused preparation checks that selected input hash, original
row range/digest, fresh progress commitments, and any prepared commitment.
Independent child completion may advance while a holder remains usable; the
original input, prepared slot and conditional guards remain fixed. Cold reads
retain full verification. In one native 64-row fixture, database cursor operations
fell from 1,974 cold to 61 hot and remained 61 after another prior outcome; this
is a read-work observation, not fleet throughput evidence. Focused default race
(51.837 s), Go 1.25 (24.059 s), tagged race (50.137 s), vet and review passed.

## Verification boundaries and remaining work

The first command/snapshot tests exercise strict envelopes, batching isolation,
cold and cached original-plan indexes, snapshot restoration and ordinary child
completion after recovery. Process tests forcibly terminate a real single-node
Raft owner at four acknowledged boundaries:

1. Format-8 snapshot followed by execution in the log.
2. Format-9 prepared-candidate snapshot.
3. Format-9 accepted-child snapshot.
4. Accepted snapshot followed by child completion in the log.

Each process test validates the stopped directory, reopens the Store and reconciles
the original preparation or accepted outcome. These durable tests use synthetic
encrypted catalog candidates and invoke no providers. The separate management
preparation tests cover original encrypted input and the actual compiler; neither
boundary alone establishes connected public API/dashboard Apply.

A private management coordinator now drives these commands from bounded original
work metadata. It preserves pending ciphertext across quota pressure, joins the
FIFO submission prefix after uncertain replies, and bypasses crypto for committed
prepared or unchanged items. Its test and application-lifecycle integration
qualification is a separate boundary.

The public activation endpoint, retained application-result publication/pagination,
safe retention cleanup, original-input reselection and SDK/CLI/dashboard integration
remain unfinished. Execution-bearing parents
currently refuse cleanup, preserving evidence until final retention is wired.
Candidate resource-size headroom is now checked before successful validation,
using the final normalized metadata and preserving omitted credential values.
The shared size check enforces the 1 MiB resource limit; separate prepared-record,
command and storage quotas still apply. Policy version 2 prevents older sealed
validation profiles from silently acquiring this new preparation behavior.
Provider-account verification, the million-monitor campaign, native distribution
qualification and all public publication gates remain separate and open.
