# Catalog mutation format 4

Status: implemented private durability prerequisite, 19 September 2026.
This change provides unique reverse-dependent mutation tokens. Executable
collection plans, item outcomes and public activation remain separate work.

## Why a log index is insufficient

Two successful catalog mutations can share one Raft entry. If both change
consumers of the same dependency, stamping that dependency with the entry index
cannot distinguish the first mutation from the second. An executor retaining
the first mutation's token must detect the second, even when it commits in the
same entry.

Format 4 adds a durable `CatalogMutationSequence`. Each accepted catalog mutation
increments it once and returns that value with its result. All old and new
reference targets touched by the mutation receive that token as their
`DependentsVersion`. Unrelated targets retain their guards. `CommittedIndex`
continues to identify the Raft entry and is never an own-mutation token.

## Compatibility and transition

| Boundary | Behavior |
| --- | --- |
| Historical commands | Format 1 lifecycle commands and format-2/3 catalog commands preserve their previous replay semantics. |
| New writer | Catalog mutations and bootstrap seeds select format 4. A mixed submission retains the highest required envelope format. |
| First successful mutation | Seed the sequence from the preceding committed image index, then increment. This exceeds every retained legacy reverse guard without rewriting the catalog. |
| Later mutations | Increment the sequence once, after validation and all resource/dependency preconditions succeed. |
| Snapshot format 3 | Preserve its literal outer magic, image interpretation and inner input-ledger framing. A frozen old snapshot remains byte-identical after later writes. |
| Snapshot format 4 | Use explicit `CPRA-COLLECTION-SNAPSHOT-4` outer magic, retain the input-ledger stream, and include the sequence in the state image. |
| Other new commands | Preserve the upgraded image version. A command need not use a format-4 envelope unless its behavior requires it. |
| Old catalog commands after transition | Reject before consuming a typed operation reservation or changing catalog state. |

Collection-capable formats are explicitly 3 and 4; the collection-format
constant is not an alias for the latest format. Decoder compatibility is
separate from the writer's current choice. A format-4 image must contain a
nonzero sequence, and a format-1/2/3 image cannot contain one. Snapshot validation
still bounds `CommittedIndex` by the image index; it bounds reverse-dependent
guards by the sequence once format 4 is active.

An ordinary catalog-only store uses the same self-contained format-4 snapshot
with an empty input-ledger stream. Snapshot capture does not create a mutable
staging database just to represent that empty stream. Online recovery and
stopped-backup validation both accept this case, including log-only recovery.

No ciphertext binding, encrypted payload or bootstrap content identity changes.
Bootstrap begin, activation and retries do not allocate tokens; only an accepted
seed does. Conflicts and direct command retries do not allocate tokens either.

Sequence exhaustion fails before catalog, reference-index or sequence mutation.
For an already reserved typed operation, established rejection handling still
consumes its reservation and retains an `activation_rejected` receipt. That
receipt truthfully records failure; it is not a successful catalog mutation.

## Upgrade, rollback and evidence boundary

Format 4 introduced this mutation-token boundary. The later
[inactive plan snapshot extension](collection-plan-snapshots.md) raises the
highest supported format to 5 while retaining these semantics. The state image
and mutation-token mode originally advanced on the first
successful new catalog mutation. Rollback compatibility can change earlier: a
committed format-4 command envelope still requires a format-4 reader when its
resource precondition rejects the mutation. Merely changing release metadata
does neither. Keep a complete stopped backup and
the matching configuration and encryption-key access before upgrading. An older
binary must support every format written after the upgrade before it can be
used for rollback. Editing a snapshot version or deleting a staging generation
is not a downgrade procedure.

Focused tests cover historical format-2/3 same-entry replay; unique format-4
tokens; stale own-token conflicts; mixed writer format selection; bootstrap
lifecycle/retries; sequence overflow and typed rejection receipts; snapshot
validation; frozen format-3 bytes; explicit format-4 framing; catalog-only
snapshots and stopped validation; and a real Raft format-3 snapshot followed by
format-4 logs, restart and format-4 restore. Existing format-1 tests remain in
the `internal/persistence` package suite. These are current-reader compatibility tests; they
do not claim a separately executed historical format-3 release binary.

Format 4 itself contains no plan/outcome rows. The unique result token
alone does not authorize future activation or prove that an executor substituted
the correct previous item. That storage and execution boundary still requires
its own implementation and qualification.
