# Persistence and restart recovery

The executable defaults to single-node HashiCorp Raft storage in the platform's
user state directory. `cpractl local paths` reports that location; Linux system
services explicitly use `/var/lib/cpra`. Explicit `-data-dir` overrides an
explicit runtime `storage.directory`, which overrides the platform default.
A legacy `./cpra-data` requires an explicit path or a stopped migration, so an
upgrade does not silently start a fresh store. Use `-runtime-config
examples/runtime.yaml` to select storage and runtime settings. `-yaml` and its `-config` alias still select the monitor
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
at v0.4.3. Jobs, clients, channels and contexts are rebuilt for execution and are
excluded from the Raft state. Legacy manifest fingerprints contain hashes, not
copies of provider configuration. The management catalog separately stores
provider configuration and write-only credential values in authenticated
encrypted envelopes before submitting them to Raft. Its encryption keys remain
outside the state directory; backups require the matching keys to recover those
envelopes.

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

Managed monitor recovery settings can explicitly allow more than one recovery
attempt per incident. An omitted or zero `maxAttempts` preserves the legacy
single-attempt policy. Each queued recovery reserves an attempt; a further
attempt requires a confirmed failed recovery and observes the configured
`cooldown` after that failure. Raising the limit does not retry unknown actions
or repeat a successful recovery while health verification is pending. Successful
health recovery resets the incident's attempt budget. The budget and cooldown
survive restart.

Absolute maintenance windows include their start and exclude their end.
Health observations continue during maintenance, while new recovery admission
and external action starts are held. A queued action is checked again when its
started marker is committed, including if the window began while it waited.
Known outcomes of actions already started are still recorded. This preserves
the existing periodic-maintenance behavior; it differs from snoozing checks.

After an interruption, a committed started action becomes unknown. This includes
the narrow interval after its started marker but before a provider call. CPRa
continues health checks and holds those actions. Snapshot restoration and log
replay do not invoke providers. A reported success means the driver's operation
was accepted; delivery to a person or completion of a recovery requires separate
provider evidence. An uncertain action is never automatically retried.

If an already-started built-in action finishes after its target configuration
changes, a late known success or confirmed rejection can be retained as separate
evidence on the original action and in its timeline. The held action remains
`unknown`; the late result does not change a replacement monitor's incident,
grant another attempt, or automatically resolve the held outcome. Repeated
identical evidence adds no duplicate event. Without an operator review,
contradictory known evidence is rejected explicitly. If a reviewed action later
receives contradictory provider evidence, both the assertion and that evidence
are retained, a conflict event is added, and its hold is restored. Ambiguous transport failures do not become confirmed
rejections.

Use `cpractl get actions --monitor-id MONITOR_ID` and `cpractl get history MONITOR_ID` to inspect
the operation identity and endpoint. Reconcile it against the designated target
or provider records. An audited review changes the held disposition under the rules below; it never
replays an operation. Preserve the record when carrying out recovery outside
CPRa; deleting the data directory is not a recovery procedure.

Storage failure stops new admission and makes readiness unavailable. The process
can remain alive for diagnostics. Runtime result channels are drained during
shutdown; uncommitted external outcomes remain conservatively recoverable as
unknown from the last committed started markers.

## Incident attention and temporary pauses

Management controls use separate versions from the monitor's desired
configuration and execution revision. An incident also has its own identity,
so an old browser view cannot acknowledge or dismiss a later incident that
happens to belong to the same monitor. Acknowledgment records the authenticated
operator, time and note; checks, recovery and notifications continue.

Dismissal requires a reason and cancels only unsent notifications for that
exact active incident. Recovery and checks continue. Notification outcomes
already in flight remain recorded, and their confirmed failures do not create
new retries while the incident is dismissed. Closing green notifications carry
the incident identity and follow its dismissal. Reopening permits future
notifications for the same still-active incident; it does not replay cancelled,
completed, started or unknown actions or reset recovery attempt limits.

Snooze requires a positive duration of at most **30 days** and a reason. It
cancels unsent actions and pauses new checks, notifications and recovery.
Already-started work records its actual result. An in-flight health observation
can update measured health counters during the pause, but cannot create fresh
incident actions or a catch-up schedule. Unsnooze and expiry preserve the
independent enabled/disabled setting. Expiry is conditional on the exact snooze
version and deadline, so extending a snooze invalidates its old wakeup.

A queued check captures the control version under which it was dispatched.
The worker checks that version and current enabled/snooze state before invoking
its driver. Clearing a snooze therefore cannot revive a queued check from
before the pause. Disabling a monitor also cancels unsent actions and preserves
actual outcomes of work that already started.

Controls first return a durable committed receipt. Their operation becomes
applied only after the controller installs the exact control or incident
version. A later control, incident replacement or monitor deletion supersedes
a still-pending receipt. Current/latest incident pages use bounded indexes;
earlier incident events remain available in retained history. Older state
records receive deterministic incident identities during recovery, without
inventing an unavailable historical opening time.

## Guarded recovery requests and unknown-action reviews

An operator recovery request uses the exact monitor incarnation, current control
version and complete prepared configuration dependency closure. It is rejected
while the owner is still applying a newer monitor, endpoint or credential
revision. The monitor must be enabled, unsnoozed, outside an absolute maintenance
window, currently observed unhealthy, and have a configured recovery driver.
Unprobed/unknown health does not authorize recovery. An active or held recovery,
pending verification, exhausted attempt budget or unelapsed cooldown also blocks
the request. The worker checks admission again immediately before execution.

Manual requests may precede the automatic unhealthy threshold, but consume the
same per-incident recovery attempt budget; they cannot force past it. Additional
manual rate limits default to **one request per minute and three per rolling
hour**, and survive restart and public monitor-ID reuse:

```yaml
manual_recovery:
  minimum_interval: 60s
  per_hour: 3
```

The configured interval must be between 1 second and 1 hour, and the hourly cap
between 1 and 100. Exceeding either rejects admission. A manual recovery operation
receipt means the durable intent was installed by the controller; completion of
that receipt does not claim the external recovery succeeded. Actual provider
outcomes remain on the action and timeline.

An unknown-action review requires the original action/monitor incarnation and
the exact observation revision, an authenticated actor and a nonblank reason.
The resolutions are `accepted`, `rejected` and `inconclusive`. They are operator
assertions, stored separately from provider facts: the original action state
stays `unknown`. Reasons and notes are bounded to 4,096 UTF-8 bytes each. At most
eight evidence references of 2,048 bytes each are accepted; they are opaque audit
references and CPRa never fetches them. Do not put credentials in audit text.

An inconclusive review retains the hold. A conclusive review can clear it only
when the original built-in executor has durably finished, or an exclusive
process restart has fenced its previous local execution session. A context
cancellation, expired timer or cleared scheduler flag is not proof that arbitrary
Go handler code has stopped. Existing actions without a recorded local session
and external-worker executions lack this proof and cannot be conclusively
resolved by this local-executor mechanism.

Clearing a hold never restarts an action, changes provider facts, resets attempt
limits or alters health/verification state. An operator's rejected assertion does
not authorize an automatic retry. A later explicit recovery request may be
admitted only if all ordinary limits and checks still pass. For a rejected
operator assertion, manual cooldown uses the latest review/executor completion
time without inventing a confirmed provider failure. New contradictory provider
evidence restores the hold and cancels unsent recovery for that incarnation,
with an audit event. A later review cannot revive those cancelled requests. A review cannot
clear a hold by contradicting already recorded provider evidence. Two conflicting
provider facts remain visible and prevent a conclusive resolution.

Before requesting a started marker, the built-in worker reserves a local
execution claim. Even an uncertain start reply retains that claim until the
worker returns and its separate completion marker commits. The store refuses to
close or release its data-directory lock while active or unconfirmed completion
claims remain. A cancelled completion request can be retried identically;
a durable storage failure makes readiness unavailable and requires the process
supervisor's hard shutdown boundary. Diagnostics remain useful, but a failed
write is never repaired by clearing the error or silently unlocking the store.
Native process tests exercise this boundary; they do not establish a universal
power-loss guarantee or fencing for a remote worker.

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

`cpractl local backup --data-dir /path/state --output /path/new-backup --backup-auth-key /path/backup-keys/authentication.key` performs
this stopped operation with exclusive locking and an HMAC-SHA256 authenticated
format 2 file inventory. Supply a separately protected random 32-byte binary key,
kept outside both directories and retained independently for restore. File checksums
alone detect accidental corruption; the external key authenticates all inventory
metadata and hashes. Unsigned format 1 backups are rejected without a fallback.
`cpractl local restore --backup /path/new-backup --data-dir /path/new-state --backup-auth-key /path/backup-keys/authentication.key`
requires a new destination and validates the complete inventory before publishing
it. See [native operations](native-installation.md) for ownership and upgrade details.

To restore, stop the process, restore the complete backup into a new directory
owned by the service account, set `storage.directory` to it and supply the matching
configuration. Explicitly provision fresh authentication with stopped local
administration before starting CPRa. Check readiness, event history and unknown actions
before treating the instance as recovered. Missing identity, incompatible
formats, corrupt snapshots and incomplete history backups cause explicit
errors; do not fix them by deleting state files. Copying a live bbolt file with
ordinary file-copy tools is not a consistent backup method.

## Durable authentication and explicit restore

Named reader/operator authority is committed with an authentication epoch and
revision. Only SHA-256 verifiers of high-entropy bearer tokens enter the log and
snapshots; local token generation and its protected output remain outside the
data directory. A policy contains at most 1,024 named principals, with optional
explicit expiration times. Expiration is evaluated at admission, not during
replay. No default token lifetime or rotation overlap is implied.

Initial bootstrap is consumed once. Normal restart uses committed authority;
re-reading an old principal file or supplying an old legacy read token cannot
overwrite it. Legacy Basic/token credentials remain read-only. An explicitly
anonymous deployment records a separate loopback-only permission; an initialized
policy with no usable credentials is denied rather than silently becoming
anonymous. Trusted-proxy and TLS configuration remain process settings.

Local token administration requires CPRa to be stopped and takes the same
exclusive database lock as the server. Administrative opening runs Raft replay
and authentication commits without monitor loading, provider calls, incident
recovery, a new executor session, background snapshots or retention cleanup.
It is not a live token-management API. A commit whose response is lost remains
unconfirmed: preserve the original protected token file and inspect the intended
revision before another change. Do not generate a replacement automatically.

An explicit `cpractl local restore` records a fresh `RESTORE.json` identity before
publishing the destination. The node ID remains unchanged because encrypted
catalog records depend on it. Restored identity-file format **3** requires a
matching retained marker; binaries supporting only format 1 reject it. This is
a restore compatibility boundary, not permission to roll back an older binary.

Before normal startup, reset commits remove named and legacy authentication and
anonymous permission, establish new authentication and operation epochs, cancel
queued external actions, and hold interrupted started actions as unknown. They
retain incident, attempt, cooldown and provider-result facts. Old pending
operation receipts become superseded evidence, never successful application.
Every such cancellation/invalidation is audited as `explicit_restore`.

The reset is resumable under the original marker identity. Action pages contain
at most 85 identities, leaving room for their recovery/review receipts; receipt
pages contain at most 256 records. Each page retains at most 256 audit events.
This bounds per-commit work and output; the total pass is proportional to retained
actions, and cloning a changed monitor still scales with that monitor's retained
actions. No fleet-capacity or restore-duration claim follows from these tests.

Partial reset is not a complete backup. Normal startup refuses admission until
all reset pages finish and stopped local administration explicitly reprovisions
fresh credentials for the new epoch. Losing the marker or presenting a mismatched
one is an error. Ordinary process restart is different: it preserves credentials
and may resume safe queued work. These tests establish process-interruption
recovery, not universal power-loss guarantees.

Public mutations allocate an operation handle in the form
`op.<epoch-uuid>.<20-digit-sequence>`. Allocation commits the next sequence and a
bounded reservation together. A reservation contains resource/incarnation/version
identities, the authenticated actor and a digest of the prepared command; it does
not duplicate plaintext secrets, encrypted resources or executable jobs. Resource,
control and action-review versions remain distinct from the operation handle.

Preparation and dry-run do not allocate. If allocation is unconfirmed, no target
mutation is submitted and no canonical handle is invented. Once allocation is
confirmed, the handle remains available even when target admission fails or its
reply is lost. Activation consumes only the original matching reservation; changed
content, identity, actor or dependencies cannot reuse it. Fresh admission time
controls expiry and lifecycle eligibility. Neither allocation nor activation is
retried automatically. Internal snooze-expiry commands and retained legacy UUID
receipts keep their existing internal compatibility behavior.

Reserved operations report zero committed target changes. Known activation
rejections retain a failed receipt with zero target commits. Committed receipts
remain distinct from owner-applied completion. Reservations and pending committed
receipts share a maximum of 4,096 records. Unactivated reservations expire after
24 hours; allocation and the existing snapshot/retention maintenance tick retire
at most 256 expired reservations per pass. An internal cleanup entry point uses
the same bound. Maintenance submits no command when there is nothing to expire. An actively committed operation does not expire
through that path. Completed receipt details follow 30-day event retention.
The bounded secondary version index is rebuilt on replay and prevents a pending
operation scan for each monitor result.

Live current-epoch handles are read first. After detailed history expires, an
issued sequence still returns expired because the allocation high-water mark is
retained. A sequence above that mark is never-issued/not-found. A syntactically
valid handle from any other epoch is expired/inapplicable; this does not certify
that its foreign epoch was previously issued. Explicit restore establishes a new
epoch, resets its sequence and retires pending reservations with the same
`explicit_restore` audit as pending receipts. Retained historical events remain
evidence and cannot authorize execution. Native Linux process-termination tests
cover allocation recovery; they make no Windows or power-loss claim.

Operation lists freeze the bounded live set (at most 4,096 reservations and pending
receipts), the operation epoch and sequence ceiling, and the retained-history
boundary. Pages return live handles in handle order, followed by terminal receipts
in committed event-position order. A live operation that completes while paging
remains the original frozen live row; its later terminal event is excluded. Later
operations are not inserted into the existing view. An optional monitor filter
matches the operation's exact original `Monitor` target ID. It does not infer
operations against shared credentials, recipients or other dependencies.

Each read inspects at most 10,000 index keys, including receipt reconciliation,
across at most 64 retained segments. Work can reach this limit before finding a
matching receipt; an empty page with a continuation cursor is valid and advances
the scan. Terminal data stays in its daily history segments, and no database
transaction remains open between requests. The server bounds snapshot memory
before copying live receipts, with shared 64 MiB and per-principal 16 MiB quotas;
the HTTP cursor also binds its principal, authorization generation, filter and
page size. Cursors expire after five minutes, process restart, explicit restore
or removal of covered history. A new legacy UUID receipt also expires existing
views because old logs allowed those identities to be reused. Repeated older
terminal UUID records are reconciled against their latest retained receipt,
never returned as separate operations. Missing or corrupt segments and indexes
report unavailable rather than an empty successful list.

The first normal startup after upgrading a store without operation-list indexes
adds derived global and exact-monitor terminal indexes. Each synchronous database
transaction visits at most 256 primary events and stores its progress with the
index entries. A final validation cross-checks every terminal event and both
indexes before startup can report success; forged progress cannot hide earlier
events. Interrupted migration resumes from committed progress. Migration and
application validation honor startup cancellation. The existing bbolt physical
integrity check has no cancellation interface and must finish before its read
transaction can close safely. Stopped administrative and backup inspection never
run this migration; operation-list reads against incomplete indexes report
unavailable. These derived indexes contain identifiers and pointers to existing
redacted receipts, not provider parameters, credential values or executable jobs.
