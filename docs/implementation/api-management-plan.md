---
title: Plan and status · CPRa management API, batch configuration, and external workers
description: Plan and status · CPRa management API, batch configuration, and external workers for the reviewed CPRa source; see the version and availability notice.
cpra_scope: plan
---

> **Candidate design and evidence:** this record describes unreleased implementation work or an approved plan. Its checklists do not establish availability in `main`. See [version and availability](../versions.md).

# CPRa management API, batch configuration, and external workers

Status: approved implementation plan; implementation and qualification are pending.
Updated: 2026-09-12.

## Goal and boundaries

Deliver a versioned management API and matching cpractl commands for durable
configuration, incident attention, operational controls, and descriptive
diagnostics. Support collections of team/service configuration files and an
optional external-worker protocol for custom checks, recovery, and notifications.

This document consolidates the approved API plan and replaces earlier API design
proposals in the conversation. It does not mark the existing release-engineering,
provider-verification, or performance gates complete.

- Implement the API and CLI now. Publish the public Go SDK and dashboard editing
  in a later phase; preserve the current read-only dashboard throughout this work.
- Preserve Go 1.25 source compatibility, Ark v0.4.3, existing monitor manifests,
  optional built-in driver tags, and single-node Raft ownership.
- Keep the default durable mode and explicit disposable memory mode. Memory mode
  uses the same validation and admission semantics without claiming restart
  persistence. Encrypted canonical configuration applies to durable storage.
- Work on an isolated codex/ branch based on the reviewed current source
  candidate, preserving existing release/provider work and the separate
  documentation checkout.
- No check-now command or on-demand check endpoint. All checks remain scheduled.
- No Kubernetes-style field ownership, implicit pruning, remote configuration
  reload from files, general workflow engine, in-process custom code, distributed
  failover, team tenancy, subscriptions, or new provider-certification claims.

## Public resource and HTTP contract

Preserve /api/v1 response contracts, numeric monitor routes, and existing units.
Add /api/v2 with stable string resource identities and explicit HTTP methods.

Writable resources are Monitor, NotificationEndpoint, NotificationGroup, and
Credential. The externaljobs build additionally supports dynamic JobType
resources. Incidents, actions, operations, queues, pools, SLOs, worker observations,
and instance state expose bounded observations or the specific commands described
below; they are not arbitrary writable runtime objects.

Use Kubernetes-inspired apiVersion, kind, metadata, spec, and read-only status.
Keep these meanings separate:

| Field or identity | Meaning |
| --- | --- |
| Stable resource ID | Preserved across renames; legacy manifests retain their existing deterministic-ID rule. |
| Incarnation UID | New on delete/recreate, even when the stable ID is reused. |
| resourceVersion | Opaque version for conditional spec/metadata edits. Routine checks do not change it. |
| generation / observedGeneration | Desired effective configuration and the version installed by the owner loop. |
| controlRevision / incident triage version | Orders relevant operational controls independently of routine observations. |
| statusRevision | Observational freshness without creating edit conflicts. |
| Execution revision and execution ID | Bind a job to its configuration/dependencies, incarnation, and one execution. |

Do not reuse the existing pulse lifecycle generation as a resource edit version.
Metadata-only label edits and acknowledgment do not invalidate scheduled checks.
Disable/enable are conditional edits to spec.enabled; their committed changes
also update the admission projection and relevant scheduling revision.

- Pin OpenAPI 3.1.2 and the previously selected oapi-codegen 2.8.0. Commit generated
  DTOs, handler interfaces, and the internal CLI transport. Keep transport DTOs
  independent of Ark entities, Go errors, clients, and executable Job values.
- Use camelCase for v2, duration strings in configuration, explicit millisecond
  telemetry fields, UTC timestamps, string IDs/revisions, and explicit unavailable
  measurements. Preserve v1 through adapters.
- Round-trip every driver union correctly. Omitted enabled defaults to true;
  explicit false remains false. Reject unknown fields, duplicate keys, trailing
  documents where one object is expected, invalid unions, and invalid references.
- Return RFC 9457 problem details with field paths, request IDs, operation IDs
  where applicable, and actionable conflict/validation errors.
- Create conflicts with an existing identity. Updates/deletes require If-Match;
  create-if-absent uses the corresponding absence precondition. CLI apply resolves
  observed versions during preflight when files omit resourceVersion.
- Apply replaces the desired spec and editable metadata using documented
  omission/default rules. Preserve server-owned fields. It is not field-level
  server-side apply.
- Initially support JSON Merge Patch only: arrays replace, null removes optional
  fields, and resulting resources undergo full validation. Defer JSON Patch and
  strategic merge patch.
- Enforce compare-and-swap inside the durable command application, including
  dependency checks. An expected conflict is not a storage-failure condition.
- Block deletion of referenced credentials, endpoints, groups, and JobType
  versions. Require references to be changed first; do not cascade silently.
- Expose discovery of resources, verbs, schemas, patch formats, compiled drivers,
  runtime feature enablement, and external protocol versions.

### Multi-file collections and resumable apply

Use one shared input pipeline for create, apply, diff, and server dry-run:

```sh
cpractl apply -f shared/endpoints.yaml -f payments/api.yaml -f orders/api.yaml
cpractl apply -f config/payments/
cpractl apply -R -f config/
cpractl apply -f https://example.com/cpra/payments.yaml
cpractl apply -f -
cpractl diff -R -f config/
cpractl apply --dry-run=server -R -f config/
```

Accept YAML/JSON standalone resources, resource lists, multiple YAML documents,
and existing CPRa manifest envelopes. Shared-only files must work without a
monitors block. Directory processing selects recognized manifest extensions in
lexical order; recursion is explicit with -R. Do not follow directory symlinks
during traversal. Deduplicate overlapping source paths, then reject duplicate
resource definitions even when their contents are identical. Empty documents and
comments are ignored; a collection with no resources returns an explicit no-op.

Freeze file, URL, and stdin bytes before multi-pass validation. URLs are fetched
by the CLI with bounded redirects, bodies, decompression, deadlines, and staging
space. Do not forward CPRa credentials to source URLs. HTTPS is the default;
plain HTTP requires an explicit CLI opt-in. Diagnostics redact URL credentials
and query secrets. Do not infer overrides from filenames or deep-merge two
definitions of one monitor. External renderers can supply documents through
stdin; an embedded template/overlay language is out of scope.

CLI input spools may contain plaintext from the supplied configuration. Create
them with owner-only permissions (0600 on Unix, equivalent user-only ACLs on
Windows), enforce byte quotas, and remove them on completion/cancellation. They
are not durable server staging and are never copied into Raft, evidence, or logs.

One collection follows this lifecycle:

1. Collect source-attributed resources into bounded private staging. Each item
   retains resource identity, source/document/item location, and immutable content
   identity. Persistent server staging is encrypted before durable submission.
2. Validate the entire collection before active-resource changes: schemas,
   permissions, duplicate identities, capabilities, references, and dependencies.
   A preflight error causes zero active configuration changes.
3. Resolve references against the staged union plus authorized live resources
   omitted from the collection. Record their observed versions. Validate shared
   definitions even if no submitted monitor references them. Preserve group
   endpoint order; reject empty groups and duplicate members that would otherwise
   create unintended repeated delivery.
4. Finalize an immutable apply operation. Apply dependencies before consumers:
   credentials and JobTypes before consumers, endpoints before groups, groups
   before monitors. Traverse indexed dependents when a shared update affects
   monitors outside the submitted files, and expose that impact in diff/results.
5. Activate through bounded per-resource CAS. Independent items may continue after
   a conflict. If an included dependency fails, mark its dependent items blocked;
   never silently bind them to the older live dependency instead.
6. Record created, updated, unchanged, conflict, dependencyBlocked, failed, and
   notAttempted outcomes, with old/new versions and committed-versus-controller-
   applied progress. Cancellation stops remaining work without undoing commits.
7. Resume using the operation and item identities. A changed file or refetched URL
   creates a new operation, not a reinterpretation of the old one. Do not blindly
   retry mutations after an unknown commit outcome.

Operation handles are server-issued and contain a store/restore epoch and
monotonic sequence. Persist the allocation high-water mark with operation
creation; clients cannot create an operation by supplying an arbitrary old ID.
Lookup live records first. A missing handle from an issued/retired range returns
expired (410), an unissued handle returns not found, and a previous restore epoch
returns expired. All activation/retry paths require a live operation record.
This retains replay rejection after detailed receipts expire without an unbounded
tombstone per operation. Explicit restore creates a new operation epoch as well
as resetting authentication; historical operations remain evidence, not executable
resume handles. Apply the same handle discipline to retried side-effect commands.

Preflight does not lock live resources. Concurrent edits can therefore produce
partial activation after successful validation. Raft submission batching does
not imply collection-wide atomicity or reverse external actions.

Only supplied resources are applied. Omission never deletes another service's
monitors. Team folders, labels, and source attribution do not confer authorization.
No implicit prune is provided.

### Bounds and retention defaults

| Boundary | Initial contract |
| --- | --- |
| Resource payload | 1 MiB maximum. |
| Collection upload chunk | At most 256 resources or 4 MiB. A collection can span many chunks. |
| Raft submission | At most 1,000 commands or 5 ms, also constrained by encoded-byte limits. |
| Owner reconciliation turn | At most 100 mutations or 5 ms, whichever is reached first. |
| Resource/history/action page | 100 by default, 500 maximum. |
| Selector | At most 16 clauses and 1 KiB of selector text. |
| Catalog continuation | Stable snapshot semantics with a five-minute cursor lifetime; expiry is explicit. |
| Unactivated persistent staging | Expire after 24 hours of inactivity; never expire an actively applying operation. |
| Terminal apply results/audit | Retain for 30 days; return explicit expiry afterward and never silently rerun an expired operation ID. |
| CLI watch | Poll every five seconds; server-push watch is deferred. |

Staging quotas, byte accounting, and bounded cleanup are mandatory. Store large
collections and per-item progress incrementally; do not require fleet-sized HTTP
bodies, Raft commands, or decoded Go slices. Server dry-run validates and returns
redacted proposed changes without active commits, provider operations, or key
generation; ephemeral validation staging is discarded afterward.

Server dry-run uses bounded request-scoped memory and streamed validation,
retaining only the identity/dependency information necessary for the collection.
It does not persist submitted plaintext or create data/wrapping keys. If a dry-run
exceeds its configured validation-memory bound, reject it explicitly before any
active change rather than spilling plaintext or reporting partial validation as
success. This bound is distinct from ordinary encrypted staged-apply capacity.

Catalog queries use maintained indexes and bounded page construction outside the
owner loop. Do not scan the fleet to provide exact filtered totals. Runtime-status
filters are explicitly timestamped live views rather than snapshot claims. Keep
label values in metadata/read indexes, not one ECS component or relation target
per arbitrary user label.

## Durable execution and operator behavior

### Canonical configuration and ECS projection

Introduce a versioned durable resource catalog separate from incident/action
state. On first migration, validate and stage the complete existing manifest,
preserve stable IDs, execution fingerprints, incidents, cooldowns, verification
state, attempts, and unknown outcomes, then activate the catalog. Interrupted
migration must not expose a half-imported fleet.

After activation, restart restores the catalog. Existing -yaml/-config arguments
are bootstrap/migration inputs and must not overwrite API edits or become a
requirement for restoring the active fleet. Runtime configuration remains the
startup authority for storage, encryption providers, server behavior, global SLO
settings, and extension enablement.

The mutation path is validation and immutable preparation, conditional durable
commit, owner-loop reconciliation, then observational publication. Prepare clients
and jobs outside the owner loop. HTTP handlers, Raft FSM application, and external
workers never mutate the Ark world directly. Dynamically maintain entity
membership, stable-ID mappings, reverse dependency indexes, schedulers, and read
projections; the current startup-only loader/index is not a CRUD implementation.

Retain the existing Disabled zero-size tag. Add a small always-present ControlState
projection for control revision, snooze deadline, and incident suppression
references. Keep frequently changing pending/needed/verification flags in ordinary
state fields. Keep actor/note history outside the data touched on every dispatch.
Do not put operator bits into a field that routine durable projection resets.

Apply structural changes with queries closed and reacquire component pointers
afterward. Reuse filters/mappers and combine unavoidable component exchanges.
The timing wheel and ready queues remain scheduling indexes: adding a tag does
not invalidate their entries or worker job copies. Use indexed cancellation and
revision validation with full entity/incarnation identity, preventing unbounded
stale entries under repeated edits. Do not replace due scheduling with a full-fleet
archetype scan each tick.

Commit changes before admitting affected work. Validate exact execution and
dependency revisions at pre-start authorization. Preserve known results from old
actions as historical facts without applying them to replacement targets.
Deletion retains required tombstones, history, and unknown outcomes. Storage
failure stops admission and makes readiness unavailable; expected conflicts and
provider outages do not masquerade as disk failure.

### Controls and incident attention

| Command | Durable meaning and scheduling behavior |
| --- | --- |
| acknowledge incident | Record authenticated actor, time, and optional note. Checks, notifications, and recovery continue. |
| dismiss incident | Require a reason; suppress future notifications for that exact incident and cancel its unsent deliveries. Checks and recovery continue. |
| reopen incident | Remove dismissal on the same still-active incident. Resume future eligible notifications without replaying cancelled, completed, started, or unknown deliveries. Reject a closed/replaced incident. |
| snooze monitor | Require a positive duration and reason; pause checks, notifications, and new recovery admission until the durable expiry. |
| unsnooze monitor | End snooze early while respecting disabled state and other admission rules. |
| disable monitor | Conditional spec.enabled=false; stop new checks, notifications, and recovery indefinitely while retaining state and history. |
| enable monitor | Conditional spec.enabled=true; resume only when other eligibility rules permit. |

Acknowledgment, dismissal, snooze, enabled state, and last observed health remain
independent fields. Incident commands require exact incident identity and expected
triage version; a delayed command cannot affect a replacement incident.

Notification intent/retry/history records carry incident identity, including the
closing incident's recovery notification. Dismissal must not suppress unrelated
notifications or a future incident. Reopen preserves cooldowns and attempt limits.

Snooze uses one expiry entry, replaced when extended; stale wake-ups cannot resume
work. Expiry never enables a disabled monitor, and unsnooze never enables it.
Resume schedules fresh work without replaying missed pause intervals or old
actions. Bulk resumes use deterministic staggering to avoid a fleet-wide burst.
Intentional pause intervals are reported separately; they neither count as
healthy checks nor erase misses that occurred before the pause.

Controls revoke new admission at their committed boundary. Queued work rechecks
eligibility before starting. Already-started work retains its actual outcome;
result ingestion and history processing remain active during a pause. Clearing an
ECS pending flag is not evidence of cancellation. Unknown actions remain held.

Disable and snooze cancel pre-existing queued/unstarted action intents with an
audited control reason, including offers or worker-queue copies that have not
received a start grant. Invalidate pending check slots and finalize their
accounting without retroactively removing misses. Resume creates only fresh work
allowed by the current incident policy; it does not revive cancelled intents.

Manual recovery is allowed only for an enabled, unsnoozed monitor outside active
maintenance, with current unhealthy state, configured recovery, and no active or
unresolved recovery. Start with a configurable manual limit of one request per
60 seconds and three per hour. Preserve automatic attempt limits and provide no
general force bypass.

Review of an unknown action is a distinct audited operation with actor, reason,
and an optional evidence reference that CPRa does not automatically fetch. Store
the operator assertion separately from provider facts. Clear a hold only for a
conclusive review with no active executor; an unfenced remote executor may remain
active despite lease expiry. Review never automatically replays or rearms work.
A contradictory late receipt creates a visible conflict and restores the hold.

### CLI and descriptive observations

Provide resource discovery/explain, create/apply/diff/patch/delete, get/describe,
operation status/resume/wait, controls, recovery requests, and action review.
Support table, wide, JSON, and YAML output. New resource addressing uses stable
IDs; retain existing numeric read commands as compatibility adapters.

```sh
cpractl api-resources
cpractl explain monitor.spec
cpractl get monitors -l 'team=platform,environment=production'
cpractl describe monitor/MONITOR_ID
cpractl patch monitor/MONITOR_ID --type=merge -p '{"spec":{"enabled":false}}'
cpractl disable monitor/MONITOR_ID --reason="Retired environment"
cpractl snooze monitors -l 'service=payments' --for=30m --reason="Maintenance"
cpractl acknowledge incident/INCIDENT_ID
cpractl dismiss incident/INCIDENT_ID --reason="Known issue"
cpractl get operations
cpractl wait operation/OPERATION_ID
cpractl get queues --watch
cpractl describe queue/pulse
cpractl get slo
cpractl describe state
```

Selector-based mutations freeze selected resource IDs and preconditions in a
bounded operation. Later matching resources do not inherit the command.

Monitor describe joins one configuration/status with bounded actions and recent
events. Show last/next check, measurement availability, configuration/application
versions, attention, pause reasons, unknown actions, and credential availability.
Reference shared pipeline metrics without inventing a per-monitor queue position.

Queue/pool/state views expose depth/capacity, arrival and service rates, drops,
oldest queued age, pending results, worker bounds, model recommendation, last
scaling decision and reasons, storage health, controller progress, and projection
freshness. Add aggregate ECS table/archetype capacity, control-application lag,
and stale scheduling-entry counters through bounded owner-produced snapshots.

Retain Erlang C/Allen-Cunneen plus existing bounded percentile feedback. SLO views
separate queue delay, execution, and scheduled-to-committed-result latency, with
p50/p95/p99, thresholds, samples, misses, timeouts, and coverage. Preserve the
five-minute health-check targets of 250 ms scheduling-plus-queue p99 and five
seconds scheduled-to-result p99; these are measured objectives, not an SLA.

## Authentication, encrypted configuration, and key maintenance

Named principals have reader or operator access. Existing read tokens/Basic
credentials remain read-only. Provision and rotate tokens through local
administration; store verifiers for high-entropy tokens. Add a distinct narrowly
scoped worker identity only for the optional protocol below. No OIDC or team
tenancy is introduced in this phase.

Management writes require bearer authentication and TLS, or a deliberately
configured trusted TLS-terminating proxy. Browser Basic authentication never
authorizes writes. Enforce explicit origin/content-type handling rather than
treating CORS as authorization. Replacing a token revokes old-token admissions
after the authoritative revocation commit, including pending poll/start requests.
Already admitted external operations cannot be undone. Explicit backup restore
resets authentication before network admission so old backups cannot silently
resurrect revoked access.

Encrypt canonical configuration specs and built-in provider Credential payloads
before creating Raft commands. Snapshots and persistent staging retain ciphertext.
Incident/history/SLO data is not blanket-encrypted by this feature, so its fields
must use an explicit non-secret allowlist. Do not duplicate plaintext specs,
credential-bearing URLs, tokens, or request bodies into audit logs and errors.

Credential values are write-only. Reads expose identity, version, references, and
availability. Import monitor-inline secrets into monitor-owned credentials and
shared endpoint secrets into endpoint-owned credentials; export references.
Derive private credential IDs deterministically from owner identity and field
purpose, and journal their migration so restart cannot create duplicates. Preserve
the existing resolved execution fingerprint during this representation-only
conversion, so unchanged targets do not lose pending-action identity. Redaction
placeholders are never accepted as replacement secrets. Updates preserve existing
credentials through explicit references; secret
replacement requires supplied values and a conditional write.

Use standard Go AES-256-GCM envelope encryption with fresh nonces. Authenticated
metadata binds immutable store identity, resource kind/incarnation, payload purpose,
format, and assigned configuration revision. Randomness, key retrieval, wrapping,
and client I/O stay outside deterministic FSM application.

Support these wrapping backends through one versioned envelope contract:

- Supplied or locally generated 256-bit key files.
- OpenBao Transit and HashiCorp Vault Transit.
- AWS customer-managed symmetric KMS keys, recording the resolved key ARN.

The default wrapping backend is local. Add an explicit local encryption init
operation which provisions the first key and activates its reference in runtime
configuration; the standalone Generate operation remains non-activating. Place
the default key under the platform configuration directory's keys subdirectory,
outside the data directory, using the existing platform path resolver. User-mode
keys are owner-readable only; system installations grant administrators management
access and the service identity only the required read access. Init refuses to
overwrite existing key material and can instead bind a supplied key or configured
Transit/KMS backend. Durable startup with no initialized key fails with the exact
initialization instructions before catalog activation. Containers/Helm require an
explicit mounted key or external-backend configuration; recreation must never
generate a replacement key automatically. Package examples perform provisioning
before enabling the service rather than manufacturing credentials in upgrade
hooks. Memory-only runs do not need a persistent wrapping key.

Use bounded data-key epochs, renewed at process startup and after 65,536 payload
encryptions, with bounded plaintext-key caching. No remote key-service call occurs
per health check. Keep wrapping keys and bootstrap credentials outside the Raft
directory and ordinary data backup. Missing keys, bad ciphertext, or unavailable
required key services never cause a fresh empty store or silent memory fallback.

Provide distinct local maintenance operations:

| Operation | Contract |
| --- | --- |
| Generate | Create protected key material without silently activating it. |
| Rewrap | Change wrappers around existing data keys; do not claim compromised data keys are revoked. |
| Rotate | Offline full re-encryption into a fresh store generation using new data keys. |
| Retire | Allow retirement only after every supported live recovery path works without the old key. |

Full rotation requires stopped exclusive ownership, measured staging space, a
durable progress journal, preservation of the old generation, complete validation,
new-key-only cold recovery, and an atomic durable generation switch. Include logs,
snapshots, configuration, credentials, and protected staging in the dependency
inventory; preserve incident/action/history identities and unknown outcomes.

Document backend differences and backup consequences. Rewrapping does not revoke
copied plaintext data keys. AWS rotation retains old material; independent
retirement requires a distinct key. Transit version restrictions and permanent
trimming have different effects. Copies of old keys/ciphertext outside the live
store cannot be revoked by CPRa, and logical compaction is not secure erasure.

## Optional external-worker protocol

### Build and registry boundary

The dedicated externaljobs build tag compiles the generic adapter, JobType
management routes, worker polling/start/heartbeat/result routes, and related
validation. Runtime configuration separately enables the feature. Default official
artifacts and the ordinary all-built-in-driver build do not enable it implicitly.
Untyped/unsupported external configuration produces explicit capability errors;
stored resources are never silently dropped to make startup succeed.

JobType definitions are dynamic durable API resources, not one compiled plugin
per custom type. Operators register an external identifier, immutable category,
versioned parameter/result schemas, protocol version, worker handler identifier,
execution limits, and allowed definite-rejection classifications. Reject built-in
name collisions. Keep old immutable versions while referenced by configurations,
outstanding executions, or unknown actions. Changing implementation contracts
requires a new version; executions remain pinned to their original version.

Use bounded declarative schemas with locally bundled references and no executable
validators, remote schema fetching, scripts, module downloads, or shared-library
loading. CPRa never starts user-supplied binaries or executes custom handlers.
The future Go SDK registers handlers in the separately deployed worker process.
This phase implements the versioned wire contract and separate-process reference
workers for tests; public SDK packaging remains later work.

Workers manage provider credentials exclusively. Parameters may contain opaque
worker-local credential-profile aliases; CPRa does not resolve aliases or deliver
its stored Credential values to custom workers. Custom parameters are intended
for non-secret data; schema key names alone cannot prove arbitrary strings are
secret-free. Keep their stored configuration encrypted and logs bounded/redacted.
Worker authentication credentials for CPRa are separate from provider credentials.

### Worker identity and execution lifecycle

Use authenticated HTTPS. A worker principal has operator-provisioned allowed
JobTypes, categories, and resource scopes. Worker registration advertises versions,
session identity, and available capacity within those grants; it grants no new
permissions. A worker cannot edit fleet configuration, read credential values,
claim unrelated work, or create monitors by posting observations. Optional mTLS
can further constrain worker identity.

Support checks, recovery, and notifications with this lifecycle:

1. CPRa schedules eligible work and records action intent where required.
2. A capacity-aware poll assigns work with an attempt token, worker session,
   incarnation, execution revision, exact JobType version, and bounded deadline.
   Assignment is not permission to invoke the provider.
3. A separate start request checks assignment ownership, expiry, authorization,
   current controls, dependencies, and action limits. Persist side-effect starts
   before granting execution. Server restart invalidates unstarted check sessions;
   durable action starts remain recoverable as unknown.
4. The external worker executes its locally registered handler. It keeps a durable
   journal preventing duplicate handler execution and a bounded durable outbox for
   pending results. If the outbox fills, stop accepting new execution grants;
   never discard pending outcomes to make room. Heartbeats cannot extend the
   overall deadline indefinitely.
5. Result ingress authenticates the assigned worker and derives authoritative job
   identity from the server record. Validate bounded outcome fields against the
   original type version, commit, then acknowledge with a receipt.

The result envelope and its core meaning are owned by CPRa: checks distinguish
target success/failure from noData; recovery distinguishes acceptance from
reported completion; notifications distinguish acceptance from reported delivery.
Definite rejection and unknown are explicit outcomes. JobType schemas validate
bounded supplemental evidence; arbitrary custom fields cannot select FSM commands,
set another monitor's health, or grant retry/recovery authority.
Keep arbitrary diagnostics and supplemental result fields out of plaintext history
unless they satisfy the existing non-secret allowlist. An authenticated worker's
reported effect remains distinct from independently observed provider evidence.

Use bounded long polling, initially up to 25 seconds with default fetch batches
of 16 and a maximum of 100, also limited by available slots and response bytes.
Enforce configured global/per-worker/per-type limits on active assignments,
pollers, queues, and bytes before allocating work. Bound diagnostic output to
64 KiB with explicit truncation; do not ingest unrestricted stdout or provider
response bodies. Expose configured limits through discovery/state.

An expired unstarted assignment can requeue with a new token. A started recovery
or notification whose outcome is lost becomes unknown and is never automatically
reassigned because a lease expired. An ambiguous start response grants no new
execution permission: reconnect/status/retry must not cause a second invocation.
Only a registered definite rejection can enter CPRa's existing bounded retry
policy; a worker-supplied retryable boolean is insufficient.

Identical repeated results return the original receipt while retained; conflicting
results fail explicitly. Preserve current check generation/finalization watermarks
and bounded recent receipts so expired old results never apply again. Do not
retain every successful external check for 30 days. Retain individual recovery/
notification identities and necessary history; unknown actions do not expire
automatically. Credentials renewed for the same authorized principal can deliver
its outbox; revoked credentials themselves remain invalid.

Accept outcomes for already-started work during pauses and after configuration
changes, attributed to the original action. Do not apply old recovery effects to
a replacement target or create newly forbidden work. Late evidence contradicting
an operator review becomes a visible resolution conflict.

Missing check reports mean noData/executor-unavailable by default. Preserve last
observed health and incident state; do not fabricate target success/failure,
advance recovery thresholds, or verify recovery. Missed expected observations
still reduce coverage/attainment. Actual reported target failures use ordinary
health transitions. No unsolicited observations or manual checks are introduced.

When an external check's reporting deadline expires, durably finalize that
generation as noData with its missing/timeout accounting and finalization
watermark. A late report can be identified as late evidence but cannot change
current health, verification, or the finalized window's attainment retrospectively.

Disable/snooze prevent new start grants according to their committed order;
dismissal gates only its incident notifications. An already issued grant may still
lead to I/O after a pause. Cancellation is cooperative; lease expiry does not
fence a remote process. Claim quiescence only after acknowledged completion or
independent fencing, and keep ambiguous actions held.

### Isolation and measurements

Deploy workers under separate accounts/containers with their own provider
credentials, CPU/memory/process limits, and destination access. Never mount CPRa's
state or encryption-key directories into worker examples. Process separation is
not a claim that arbitrary code is sandboxed; deployment isolation remains an
explicit operator responsibility.

Use a bounded external assignment dispatcher, not one blocked internal worker or
goroutine per remote job. Keep remote capacity separate from local worker sizing.
Expose connected workers, allowed/advertised type versions, available slots,
queue/assignment age, expired offers, admission failures, pending receipts, and
unknown actions. Add cpractl get/describe forms for JobTypes and workers.

Server timestamps establish scheduled-to-committed-result latency and dispatch/
grant timing. Worker execution durations are labeled worker-reported. A start
grant is not a measured remote execution-start timestamp, so do not claim the
internal queue-delay percentile was directly observed remotely. Authentication
identifies the reporter; it does not independently certify provider effects.

## Ordered implementation tickets

This is release-sized work. Create ten separate implementation tickets from the
following definitions; every ticket requires independent review and relevant
passing checks. No checklist below is complete merely because this plan exists.

### Ticket 1 — Define the v2 resource contract

#### Context

The current telemetry DTOs and sealed driver schema are not a writable public API
or a future SDK contract.

#### What to implement

Define the resources, version meanings, OpenAPI/code generation, strict decoding,
canonical serialization, error model, and discovery described above. Add v1
adapters and keep generated public-facing types free of internal runtime objects.

#### Where

Go schema/DTO layer, internal/web, internal/client, and API schema sources.

#### Acceptance criteria

- [ ] All 33 built-in driver configurations round-trip with correct durations,
  defaults, redaction, and explicit enabled=false.
- [ ] Unknown/duplicate/trailing input and invalid unions yield field errors.
- [ ] V1 numeric routes and response units retain their contracts.
- [ ] Discovery and generated interfaces agree with executable route behavior.

#### Out of scope

Public SDK publication, dashboard editing, server-side field ownership, JSON Patch.

#### Depends on

None.

#### Technical notes

Use the pinned OpenAPI/code-generator versions. Existing health generations and
configuration fingerprints are not resource edit versions.

#### Definition of done

- [ ] Acceptance criteria and relevant contract tests pass.
- [ ] No new build, lint, or type errors remain.
- [ ] Independent review is complete.
- [ ] Public schema and compatibility behavior are documented.

### Ticket 2 — Establish the encrypted configuration catalog

#### Context

Restart currently reconstructs executable configuration from files. API edits
require canonical durable resources and a recoverable encryption contract.

#### What to implement

Implement the versioned catalog, migration/activation, credential references,
envelope encryption, local/Transit/KMS backends, protected staging, and local
generate/rewrap/full-rotate/retire operations. Preserve lifecycle identities and
restore requirements.

#### Where

internal/durable, runtime configuration, schema conversion, and local cpractl
administration/backup tooling.

#### Acceptance criteria

- [ ] Restart works from the catalog without reimporting an old manifest.
- [ ] Crash-interrupted migration and key rotation select a valid committed state.
- [ ] No plaintext protected payload reaches Raft, snapshots, persistent server
  staging, or logs; CLI input spools obey their explicit private-file contract.
- [ ] Cold restore with only the new keys validates retirement eligibility.
- [ ] Missing/corrupt key material fails explicitly without resetting storage.

#### Out of scope

Blanket encryption of all history/SLO records, online full rotation, remote
worker-provider credential delivery, revoking copied historical keys.

#### Depends on

Ticket 1 — Define the v2 resource contract.

#### Technical notes

Use bounded data-key epochs and keep randomness/KMS calls outside FSM application.
Exercise real local OpenBao/Vault services; report unavailable AWS account-backed
checks explicitly rather than passing them through mocks.

#### Definition of done

- [ ] Acceptance criteria and storage/migration/rotation/race tests pass.
- [ ] No new build, lint, or type errors remain.
- [ ] Independent review is complete.
- [ ] Configuration, key maintenance, backup, and restore procedures are documented.

### Ticket 3 — Enforce named principal authorization

#### Context

Existing dashboard/probe credentials must not silently gain mutation authority.

#### What to implement

Add reader/operator identities, local provisioning/rotation, authoritative token
revocation, worker-scope support, write transport/origin policy, and restore-time
authentication reset. Separate worker enrollment from capability advertisement.

#### Where

HTTP middleware, durable identity records, runtime configuration, and cpractl
local administration.

#### Acceptance criteria

- [ ] Legacy credentials remain read-only and cannot invoke management commands.
- [ ] Revoked tokens fail subsequent admissions and pending polls cannot bypass it.
- [ ] Worker credentials cannot edit resources or read unrelated configuration.
- [ ] Explicit restore cannot reopen admission with resurrected old credentials.

#### Out of scope

OIDC, user accounts/onboarding, team tenancy, label-derived implicit authorization.

#### Depends on

Tickets 1 and 2.

#### Technical notes

Store token verifiers. Successful token revocation cannot undo already admitted
provider operations or erase secrets held by external systems.

#### Definition of done

- [ ] Acceptance criteria and authorization/revocation/restore tests pass.
- [ ] No new build, lint, or type errors remain.
- [ ] Independent review is complete.
- [ ] Roles, transport requirements, and token lifecycle are documented.

### Ticket 4 — Reconcile dynamic resources through ECS

#### Context

The controller and read index were built around a startup-loaded fleet. Live API
changes require safe dynamic membership and job revision handling.

#### What to implement

Implement immutable preparation, bounded committed-update delivery, owner-loop
membership changes, Disabled/ControlState projection, reverse dependencies,
indexed scheduling cancellation, incarnation validation, and incremental read
publication. Keep outcomes flowing during pauses and draining.

#### Where

internal/controller, internal/scheduler, internal/jobs, and internal/web/snapshot.

#### Acceptance criteria

- [ ] Create/edit/delete/control changes work while monitoring continues.
- [ ] No HTTP, FSM, or worker goroutine mutates Ark components.
- [ ] Stale schedules/job copies cannot run against new targets or incarnations.
- [ ] Repeated controls cannot grow stale scheduling entries without bound.
- [ ] Known late outcomes survive without changing replacement-target state.

#### Out of scope

One component per user label/status, one ECS world per team, multi-instance owners.

#### Depends on

Tickets 1 and 2.

#### Technical notes

Queries must close before structural changes; pointers are reacquired afterward.
Keep the timing wheel rather than introducing a fleet scan for every tick.

#### Definition of done

- [ ] Acceptance criteria and lifecycle/concurrency/race tests pass.
- [ ] No new build, lint, or type errors remain.
- [ ] Independent review is complete.
- [ ] Owner-loop and execution-revision contracts are documented.

### Ticket 5 — Implement conditional resource operations

#### Context

The new API needs durable mutation semantics and bounded observations suitable
for large fleets and future clients.

#### What to implement

Implement create/get/list/apply/merge-patch/delete, durable CAS/idempotency,
reference checks, schema validation/dry-run, operation lookup, indexed labels and
allowed selectors, and bounded snapshot/live pagination.

#### Where

Go management handlers, durable command handlers, resource/read indexes, and
internal client transport.

#### Acceptance criteria

- [ ] Concurrent editors produce explicit conflicts rather than lost writes.
- [ ] Unknown commit responses are resolved by operation identity without replay.
- [ ] Referenced resource deletion is rejected and omissions never prune.
- [ ] Million-resource queries construct bounded pages without owner-loop scans.
- [ ] Discovery, describe, and dry-run cause zero provider operations.

#### Out of scope

Field ownership, implicit cascade/prune, expensive exact filtered totals, check-now.

#### Depends on

Tickets 1–4.

#### Technical notes

CAS runs inside the durable state transition. Ordinary conflicts never mark
storage unavailable. Credential values remain write-only.

#### Definition of done

- [ ] Acceptance criteria and API/client/pagination/idempotency tests pass.
- [ ] No new build, lint, or type errors remain.
- [ ] Independent review is complete.
- [ ] Methods, errors, limits, and mutation semantics are documented.

### Ticket 6 — Apply multi-file collections resumably

#### Context

Teams need service-specific files with shared resources and a clear boundary
between all-input validation and partial activation.

#### What to implement

Implement shared file/URL/stdin collection, multi-document parsing, frozen private
staging, cross-file catalog/dependency validation, chunked operation staging,
dependency-ordered activation, source attribution, quotas/expiry, and resume.

#### Where

Loader collection layer, cpractl input handling, management operation APIs, and
durable staging/progress records.

#### Acceptance criteria

- [ ] Shared-only files and references across services resolve correctly.
- [ ] Duplicate identities report both origins and never use last-file-wins.
- [ ] An invalid final document causes zero active changes.
- [ ] A failed included dependency blocks consumers instead of using old data.
- [ ] Interrupted apply resumes original bytes and exposes partial outcomes.
- [ ] Large collections obey memory, request, staging-space, and owner budgets.

#### Out of scope

Collection-wide atomic rollback, implicit overlays, template execution, prune.

#### Depends on

Tickets 1–5.

#### Technical notes

Do not loop the existing startup loader, which treats one manifest's shared maps
and present fleet as authoritative. Source paths are attribution, not identity.

#### Definition of done

- [ ] Acceptance criteria and collection/dependency/resume/expiry tests pass.
- [ ] No new build, lint, or type errors remain.
- [ ] Independent review is complete.
- [ ] File layouts, partial outcomes, limits, and recovery examples are documented.

### Ticket 7 — Add operator controls and diagnostic views

#### Context

Operators need explicit attention, suppression, scheduling controls, and evidence
about capacity without changing incident facts or replaying uncertain actions.

#### What to implement

Implement acknowledge/dismiss/reopen, snooze/unsnooze, disable/enable, constrained
manual recovery, audited unknown-action review, and the descriptive monitor,
queue/pool/SLO/state/ECS views. Keep scope and control fields independent.

#### Where

Durable lifecycle commands, controller projection/admission, API observations,
internal client, and cpractl commands.

#### Acceptance criteria

- [ ] Every control obeys the behavior table across restart and concurrent changes.
- [ ] Dismissal follows incident identity, including closing notifications.
- [ ] Snooze expiry never enables a disabled monitor or creates a catch-up burst.
- [ ] Started outcomes remain recordable and unknown actions are not replayed.
- [ ] Describe reports unavailable/stale data accurately; no fake queue positions.
- [ ] Queueing theory stays active; intentional pauses cannot improve attainment.

#### Out of scope

Check-now, force recovery, automatic review-based replay, dashboard editing.

#### Depends on

Tickets 1–6.

#### Technical notes

Use execution-specific admission and incident-specific notification suppression.
Sample/copy ECS statistics on the owner; do not expose mutable World.Stats data.

#### Definition of done

- [ ] Acceptance criteria and control-race/expiry/SLO/diagnostic tests pass.
- [ ] No new build, lint, or type errors remain.
- [ ] Independent review is complete.
- [ ] Command meanings, rates, uncertainty, and measurement limits are documented.

### Ticket 8 — Introduce the optional external-worker boundary

#### Context

Custom implementations need a future Go SDK contract without loading their code
or credentials into CPRa's core process.

#### What to implement

Add the externaljobs build boundary, dynamic versioned JobTypes, scoped worker
enrollment, capacity-aware polling, assignment/start/heartbeat/result protocol,
bounded external queues, durable replay handling, and separate-process reference
workers for all three categories.

#### Where

Build-constrained adapter/HTTP modules, durable execution records, runtime
configuration, capabilities, CLI observations, and integration fixtures.

#### Acceptance criteria

- [ ] Untagged builds cannot enable execution routes through runtime settings.
- [ ] JobType registration is declarative and never loads custom code.
- [ ] Workers receive only authorized assignments and no CPRa-held provider secrets.
- [ ] Stale grants, forged identities, duplicate results, and revocation are handled.
- [ ] Lost started side effects remain unknown without lease-based redelivery.
- [ ] Worker loss yields noData and coverage loss rather than invented target failure.
- [ ] Slow external workers cannot occupy all built-in worker capacity.
- [ ] Separate-process faults cover lost start replies, journal/outbox restart,
  commit-before-receipt loss, old-token/revision replay, and late receipts after
  pause, deletion, timeout, and operator review.
- [ ] Fault tests assert durable receipts and actual handler invocation counts;
  a full result outbox stops new grants without losing pending outcomes.

#### Out of scope

Public SDK publication, in-process plugins, worker process management, executable
upload, unsolicited results, custom-job credential delivery, automatic side-effect
retry after uncertain loss.

#### Depends on

Tickets 1–7.

#### Technical notes

Persist assignment/action identity; retain bounded check receipts and durable
finalization watermarks. A grant or heartbeat lease cannot fence remote I/O.

#### Definition of done

- [ ] Acceptance criteria and real-process/network-failure/protocol tests pass.
- [ ] Tagged/untagged/all-driver builds introduce no lint, type, or build errors.
- [ ] Independent review is complete.
- [ ] Protocol, deployment isolation, credentials, and evidence boundaries are documented.

### Ticket 9 — Complete the cpractl management workflow

#### Context

The command-line interface must exercise the same public contracts later used by
the SDK and dashboard rather than acquire private controller shortcuts.

#### What to implement

Integrate all management, input collection, attention/control, operation, worker,
and diagnostic commands through the versioned client. Add structured output,
pagination, five-second polling watch, wait/resume, token-file refresh, and
actionable exit codes without blind mutation retries.

#### Where

cmd/cpractl, internal/cpractl, internal/client, embedded help/examples, and CLI
integration tests.

#### Acceptance criteria

- [ ] API and CLI outcomes agree for individual and multi-file operations.
- [ ] All selected inputs work outside a source checkout.
- [ ] Nonzero exits identify unresolved/failed items without hiding successful ones.
- [ ] Existing numeric reads and authenticated probes continue to work.
- [ ] No command or route implements manual health checks.

#### Out of scope

Public SDK packaging, dashboard mutation flows, server-push watch.

#### Depends on

Tickets 1–8; individual command work may proceed alongside completed contracts.

#### Technical notes

Handle 201/202/204 and problem details correctly. Resume by operation identity;
the local source need not remain unchanged after a collection has been frozen.

#### Definition of done

- [ ] Acceptance criteria and CLI/API integration tests pass.
- [ ] No new build, lint, or type errors remain.
- [ ] Independent review is complete.
- [ ] Help, installation examples, and client-visible behavior are documented.

### Ticket 10 — Qualify the management release

#### Context

The new management surface changes persistence, authorization, and dispatch. Its
release evidence must cover packaged behavior without overstating broader gates.

#### What to implement

Complete the cross-ticket validation matrix, compatibility checks, operational
documentation, deployment examples for key mounts and external workers, source
notices, strict documentation build/links, and candidate identity/evidence.

#### Where

CI, test/benchmark harnesses, native/container/Helm packaging checks, documentation
sources, and the separate documentation checkout.

#### Acceptance criteria

- [ ] Go 1.25, release compiler, default/all-driver/externaljobs builds pass.
- [ ] Required race, vulnerability, dashboard compatibility, and packaging checks pass.
- [ ] Real crashes cover migration, mutation, starts, results, and key maintenance.
- [ ] Million-resource collection/navigation/control tests show bounded resource work.
- [ ] ECS comparisons hold eligible workload constant and report measured results.
- [ ] Source/docs/artifact evidence identify the same candidate and no unchecked
  provider, endurance, platform, or SLA claim appears.

#### Out of scope

Treating this work as production-account certification or completion of the
separate one-million-monitor 24-hour endurance gate; automatic stable publication
before its applicable gates pass.

#### Depends on

Tickets 1–9.

#### Technical notes

Respect physical-host disk prerequisites for large campaigns. Benchmark real
component payloads at 0/50/95/100 percent disabled/snoozed and during bulk changes;
report CPU, RSS/allocations, retained table capacity, controller lag, queue delay,
and achieved eligible check rate. Remote start grants are not observed execution
starts. Missing provider configuration remains not verified.

#### Definition of done

- [ ] Acceptance criteria and all relevant release checks have recorded evidence.
- [ ] No new build, lint, type, vulnerability, or documentation errors remain.
- [ ] Independent review is complete.
- [ ] Support limits, upgrade/restore compatibility, and open gates are documented.

## Research references

These are design references; their guarantees do not automatically become CPRa
guarantees. The approved CPRa contracts above govern implementation choices.

- [Kubernetes API conventions](https://github.com/kubernetes/community/blob/main/contributors/devel/sig-architecture/api-conventions.md)
  inform resource/status separation and descriptive object metadata.
- [Kubernetes API concepts](https://kubernetes.io/docs/reference/using-api/api-concepts/)
  inform conditional updates, discovery, and bounded list behavior.
- [Kubernetes configuration organization](https://kubernetes.io/docs/concepts/workloads/management/)
  and [kubectl apply](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_apply/)
  inform repeated files, directories, URLs, stdin, and explicit recursion.
- [Google AIP-233](https://google.aip.dev/233) informs explicit asynchronous partial
  outcomes, rather than treating a batch response as proof of atomic success.
- [OpenAPI 3.1.2](https://spec.openapis.org/oas/v3.1.2.html),
  [oapi-codegen 2.8.0](https://github.com/oapi-codegen/oapi-codegen/releases/tag/v2.8.0),
  [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457.html), and
  [RFC 7396](https://www.rfc-editor.org/rfc/rfc7396) define selected interface formats.
- [Ark architecture](https://mlange-42.github.io/ark/architecture/),
  [queries](https://mlange-42.github.io/ark/queries/), and
  [performance guidance](https://mlange-42.github.io/ark/performance/), checked
  against [v0.4.3 source](https://github.com/mlange-42/ark/tree/v0.4.3), explain
  archetype selection, structural moves, ownership, and measurement tradeoffs.
- [AWS GenerateDataKey](https://docs.aws.amazon.com/kms/latest/APIReference/API_GenerateDataKey.html),
  [KMS rotation](https://docs.aws.amazon.com/kms/latest/developerguide/rotate-keys.html),
  [OpenBao Transit](https://openbao.org/docs/secrets/transit/), and
  [Vault Transit](https://developer.hashicorp.com/vault/api-docs/secret/transit)
  inform envelope/key lifecycle boundaries.
- [Temporal task queues](https://docs.temporal.io/task-queue) and
  [activity execution](https://docs.temporal.io/activity-execution) inform polling,
  attempt identity, completion, and cooperative cancellation. CPRa retains its
  stricter hold policy for uncertain side effects.
- [Terraform provider protocol](https://developer.hashicorp.com/terraform/plugin/terraform-plugin-protocol)
  informs wire/schema versioning independent of worker implementation versions.
- [Go plugin](https://pkg.go.dev/plugin) and
  [HashiCorp go-plugin](https://github.com/hashicorp/go-plugin) clarify why shared
  libraries and local subprocess plugins do not supply the selected remote model.
- [JSON Schema references](https://json-schema.org/understanding-json-schema/structuring)
  inform bounded, locally bundled descriptor validation without automatic fetches.
