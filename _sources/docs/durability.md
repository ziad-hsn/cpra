---
description: Single-node Raft persistence, recovery policy and complete backups.
---

# Persistence and restart recovery

The executable defaults to single-node HashiCorp Raft storage in `./cpra-data`.
Use `-runtime-config examples/runtime.yaml` to select an absolute data directory
and runtime settings. `-yaml` and its `-config` alias still select the monitor
manifest. `examples/runtime-memory.yaml` explicitly selects disposable memory
storage; it uses the same durable-state transition path without writing files.

Raft uses synchronous bbolt log/stable writes and local file snapshots. There is
one voter and no network membership interface. This provides restart recovery on
one machine; it does not provide distributed failover or protection against loss
of the disk. A persisted node identity and exclusive database lock prevent an
ordinary restart from bootstrapping a replacement node or two processes from
opening the same directory. Startup errors never select memory automatically.

Submissions are grouped for at most 5 milliseconds or 1,000 commands. Snapshots
are requested every five minutes and three completed snapshots are retained.
The state is copied before background serialization, so subsequent applications
cannot mutate a snapshot being written. The versioned representation contains
plain durable records, independent of Ark's live entity numbering; Ark remains
at v0.4.3. Jobs, credentials, clients, channels and contexts are rebuilt from the
manifest and excluded from the Raft state. Configuration fingerprints contain
hashes, not copies of provider configuration.

## Monitor identity and configuration changes

An optional `id` is the durable identity:

```yaml
monitors:
  - id: orders-api
    name: Orders API
    pulse_check:
      type: http
      interval: 60s
      timeout: 5s
      config:
        url: https://orders.example.com/health
```

Keep the ID when renaming a monitor. Without an explicit ID, CPRa derives a
deterministic ID from the name, so a rename creates a different durable identity.
Duplicate effective IDs are rejected. Existing numeric API IDs remain available
as process-local identifiers; new `monitor_id` fields identify records across
restarts. Provider targets and referenced endpoint groups contribute to the
configuration revision. Changed revisions cancel unsent actions, preserve
history and retain uncertain outcomes; old work cannot run against a new target.
Configuration is loaded before startup; there is no live reload endpoint.

## External action outcomes

An intervention or notification endpoint moves through `queued`, `started`, and
`succeeded`, `failed`, or `unknown`. Configuration changes can also produce
`cancelled`. Intent is committed before admission and the started marker is
committed before invoking the external driver. Confirmed successful endpoints
are not repeated when another endpoint is unfinished. Confirmed retryable
notification rejections have at most three attempts per endpoint.

After an interruption, a committed started action becomes unknown. This includes
the narrow interval after its started marker but before a provider call. CPRa
continues health checks and holds those actions. Snapshot restoration and log
replay do not invoke providers. A reported success means the driver's operation
was accepted; delivery to a person or completion of a recovery requires separate
provider evidence. An uncertain action is never automatically retried.

Use `cpractl get state MONITOR_ID` and `cpractl get history MONITOR_ID` to inspect
the operation identity and endpoint. Reconcile it against the designated target
or provider records. The current read-only interfaces deliberately have no
replay or resolution command. Preserve the record when carrying out any manual
recovery outside CPRa; deleting the data directory is not a recovery procedure.

Storage failure stops new admission and makes readiness unavailable. The process
can remain alive for diagnostics. Runtime result channels are drained during
shutdown; uncommitted external outcomes remain conservatively recoverable as
unknown from the last committed started markers.

## Retained events and backup

Daily bbolt segments retain incident opening/closure, intervention and endpoint
action states, unknown outcomes and configuration cancellations for 30 days.
Successful raw health checks are not added to the timeline. Event IDs combine
the committed log position and event ordinal, making replay idempotent.
History writes and its progress catalog are synced before the corresponding
state is eligible for snapshot compaction. Expiration filters old events and
reclaims complete expired daily files; a partial boundary day can remain on disk
until all its events expire. A missing or unreadable segment is unavailable,
not an empty successful history query.

For a consistent backup, stop CPRa cleanly and copy the **entire data directory**,
including `identity.json`, `raft.db`, `snapshots/`, and `history/` (catalog and all
retained segments). Preserve permissions. Keep the matching monitor manifest,
runtime configuration, credential configuration and binary version separately.
Credentials should remain in their existing secret-management arrangement.
A Raft snapshot by itself is not a complete history backup.

To restore, stop the process, restore the complete backup into an empty directory
owned by the service account, set `storage.directory` to it, supply the matching
configuration and start CPRa. Check readiness, event history and unknown actions
before treating the instance as recovered. Missing identity, incompatible
formats, corrupt snapshots and incomplete history backups cause explicit
errors; do not fix them by deleting state files. Copying a live bbolt file with
ordinary file-copy tools is not a consistent backup method.
