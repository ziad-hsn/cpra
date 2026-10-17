---
title: Collection persistence
description: Encrypted staging, restart recovery, storage accounting and complete backups for configuration collections.
cpra_scope: development
---

# Collection persistence

Collections use inactive encrypted staging before they change configuration.
Uploading and validation do not invoke providers or activate resources. Follow
[collection preflight](collection-preflight.md) and
[operation progress](management-operations.md) for the explicit activation and
continuation steps. A malformed final input cannot partially activate a collection.

## Committed input and progress

The retained operation binds an actor, upload identity, ordered resource inventory,
source/content identities, deadline and committed progress. Resource input and
private source fingerprints are encrypted. Uploaded source locations use opaque
tokens and document/item coordinates; local filenames and source URLs remain on
the client. Provider clients and executable jobs are never serialized.

A public upload accepts at most 256 resources or 4 MiB. Internal application
groups are bounded to 64 resources or 1 MiB. Contiguous accepted commands for one
operation share a synchronous ledger transaction, preserving per-command
validation and accepted-prefix results. A later rejected item or lost response
does not undo an earlier committed prefix. Read the original operation before
resuming; changed input requires a new operation.

Accepted appends and identical retries renew the 24-hour inactivity deadline.
Expired inactive input is retired through bounded committed cleanup with progress
fences. Validation evidence has its own retention. Reading progress does not
renew an upload or request activation. Logical deletion does not promise physical
file shrinkage.

## Restart and snapshots

Raft logs and self-contained snapshots are authoritative. The encrypted bbolt
collection ledger is a derived generation reconstructed from committed records.
Snapshot capture freezes its state and read transaction before background
serialization. Later updates cannot modify the captured view. This follows the
[Raft FSM contract](https://pkg.go.dev/github.com/hashicorp/raft#FSM) and
[bbolt transaction semantics](https://pkg.go.dev/go.etcd.io/bbolt#Tx).

Recovery validates a fresh generation before publishing it. The current and
previous completed generations, and generations pinned by readers/snapshots,
remain retained. Older reconstructable generations can be retired only after
successful recovery and publication. Cleanup checks native file identity and
refuses unsafe or unverifiable paths. Work is bounded between filesystem calls;
a blocked filesystem call cannot be forcibly cancelled by that budget.

Storage accounting distinguishes active, retained, pinned, retired and
cleanup-blocked bytes. File-only or empty-catalog runs do not need a collection
ledger. A storage failure stops unsafe admission; CPRa does not delete state,
switch to memory or automatically reopen it. Stop the owner and repair or restore
from a validated complete backup.

## Formats, backup and restore

Release metadata's `storage_format_version` describes the selected binary's
capability, not a claim that every stored record already uses that format.
Historical records retain their supported replay semantics. Base and
`externaljobs` builds have different format capabilities. Newer state is rejected
by an older binary that cannot read it; rollback requires compatible data.

Take a **stopped complete-directory backup**: node identity, Raft database,
snapshots, history catalog/segments and collection generation metadata. Retain
matching configuration and artifact identity; keep wrapping keys and credentials
under their separate protected procedure. A live directory copy is not the
supported backup contract. See [native backup and restore](native-installation.md)
and [durability](durability.md).

Explicit restore changes the operation epoch. Previous collection handles expire;
inactive uploads/plans become invalidated, while committed configuration and
terminal evidence retain their meaning. Restore does not activate a collection
or repeat a provider action. Unknown external outcomes stay held for review.

## Capacity and verification

Initial bounds include 64 retained collection headers, 10 million declared items
per collection and 1 GiB of logically encoded input/plan data. Allocated database
space, snapshots, logs and filesystem overhead are additional costs. These bounds
are safety limits, not measured fleet-capacity claims. Native filesystem,
Windows map growth, cleanup/backup and deployment checks must pass against the
actual release artifact and storage stack. See [versions and availability](versions.md).
