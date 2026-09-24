# CPRa management dashboard, recipients, groups, and secrets

Status: implementation in progress; end-to-end qualification is pending.
Requirements recorded: 2026-09-13. See the
[implementation progress record](dashboard-implementation-progress.md).

Execution order and release gates are consolidated in the
[dashboard finalization and shipping plan](dashboard-shipping-plan.md), including
the 2026-09-14 decision to keep candidates private until every agreed gate passes.
This document remains the detailed browser/shared-resource requirements.

## Goal and dependencies

Complete the original management request: a versioned API used by the Go SDK,
cpractl, and a dashboard that can create and manage monitors. API-first describes
the implementation order. The writable dashboard is part of the agreed delivery.

Use the [management API plan](api-management-plan.md) for resource identities,
conditional writes, durable admission, named authorization, encrypted catalog,
owner-loop reconciliation, collection operations, controls, and diagnostics.
The [SDK status](go-sdk-status.md) distinguishes client implementation from real
server qualification. The starting point was the read-only v1 server and
dashboard. New source components are being added, while normal-startup and
owner-loop integration remain open; draft SDK HTTP fixtures alone do not
establish management behavior.

Preserve Go 1.25, Ark ownership, existing manifests and reads, optional driver
builds, and single-node Raft. Provider-account and million-monitor endurance
evidence remain separate release gates. Custom-job configuration and worker
management remain outside the dashboard; their optional SDK/server paths require
the existing build, runtime, and authorization boundaries.

## Selected browser behavior

- Guided forms for built-in monitor, endpoint, recipient, group, and secret
  management; no raw configuration editor or custom-job designer.
- YAML/JSON file import with whole-collection preflight, redacted diff, explicit
  activation, and item-level progress. Browser URL import is excluded; CLI and
  SDK retain the approved file, directory, reader/stdin, and explicit URL inputs.
- A bearer token held only in the current tab's memory. Refresh or close requires
  sign-in again. No token persistence in browser storage and no new session,
  cookie, OIDC, or account-onboarding system.
- Notification recipients are contacts separate from dashboard access. Named
  reader/operator principals retain the server authorization contract.
- No check-now button, command, or endpoint.

## Shared resource contract

These additions belong to the API, Go SDK, cpractl, and file collections as well
as the browser. Update OpenAPI, generated models, discovery, operation inventory,
loaders, reference indexes, and tests together.

1. Add `Recipient` with the ordinary stable ID, incarnation UID, and resource
   version. Its `spec.endpointRefs` is an ordered list of references to existing
   typed `NotificationEndpoint` resources.
2. Add `recipientRefs` to `NotificationGroup` alongside `endpointRefs`. Require
   at least one member; do not add nested groups. Repeated members are validation
   errors rather than a way to send duplicate notifications.
3. Add `notifyType` and `recipientRefs` to the alert-rule/Code contract. The Code
   selects the actual notification driver key, such as `email`, `twilio`, or
   `telegram`; contacts and groups supply destinations. Retain `groupRef`, so a
   rule may target individual contacts, a group, or both.
4. Resolve only endpoints whose type matches the selected Code notification
   type. Each selected recipient needs a matching endpoint; reject a missing
   match before activation. Use all explicitly configured matching endpoints,
   not an arbitrary first match. Filter direct group endpoints by the same
   type and reject a rule with no resolved targets.
5. Deduplicate the same endpoint incarnation per logical notification occurrence
   in stable first-seen order. Distinct endpoint resources are not merged solely
   because their addresses happen to match.
6. Validate reverse dependencies on endpoint, recipient, and group changes,
   including affected monitors outside an uploaded collection. Block referenced
   deletion. Apply credentials before endpoints, endpoints before recipients,
   recipients before groups, and referenced contacts/groups before monitors.
7. Freeze endpoint incarnation, configuration, credential, and execution revisions
   in delivery intent. A metadata-only rename does not invalidate it. Changed
   targets cancel queued/unstarted work; preserve started and unknown outcomes
   under their original identities. Never redirect old delivery to a new target.

Preserve legacy endpoint-only groups and their heterogeneous fanout. Do not
reinterpret legacy `Code.notify` beside `notify_group` as the new type filter.
An untyped rule targeting a group that includes recipients must explicitly select
a Code notification type before activation. The new typed mode does not mix
inline driver configuration or direct rule endpoint references with contact/group
selection. Show actionable, source-attributed validation errors.

## Secrets and durable configuration

Use the existing planned `Credential` resource, with `secret`/`secrets` CLI
aliases. Do not introduce a separate dashboard secret store. Encrypt protected
payloads before submitting them to Raft; snapshots and persistent staging retain
ciphertext. Wrapping keys remain outside the data directory according to the
management API key-maintenance contract.

Secret values are write-only. Reads show metadata, references, and availability.
Editing a monitor preserves existing secret references unless the operator
explicitly supplies a replacement. Never round-trip a redaction placeholder as a
secret. Exclude secret values from browser persistence, audit records, exports,
errors, and diagnostics. Block deletion while referenced. Notification contacts
do not become authentication principals by holding a destination.

## Controls and descriptive observations

Use the [shared control definitions](api-management-plan.md#controls-and-incident-attention)
without implementing a competing browser interpretation:

| Control | Meaning |
| --- | --- |
| Acknowledge | Record who is investigating, when, and an optional note visible to other authorized operators. Checks, notifications, and recovery continue. |
| Dismiss | Require a reason and suppress future notifications for the exact incident, cancelling its unsent deliveries. Checks and recovery continue. |
| Reopen | Remove dismissal from the same still-active incident, allowing future eligible notifications without replaying previous deliveries. |
| Snooze | Require a duration and reason; pause checks, notifications, and new recovery admission until expiry. |
| Unsnooze | End snooze early while respecting disabled state and other admission rules. |
| Disable | Pause new checks, notifications, and recovery indefinitely while retaining configuration, observations, and history. |
| Enable | Resume only when the remaining eligibility rules permit; do not clear snooze or incident attention. |

Attention and suppression do not change observed health. Require current resource,
incarnation, incident, and control preconditions as appropriate. Started results
and history remain recordable during pauses; unknown actions stay held. Resume
creates eligible fresh work without replaying missed intervals.

Expose the already-approved guarded recovery request only when eligible. Unknown
action review records an operator assertion separately from provider facts and
does not automatically replay work. No force bypass or manual check is added.

Describe views show configuration/application versions, references, last/next
checks, measurement availability, attention, pause reasons, history, and unknown
actions. Queues, pools, SLOs, and state show bounded shared metrics: saturation,
oldest work, rates, drops, worker bounds and scaling reasons, controller/storage
health, coverage, and latency percentiles. Do not invent per-monitor queue
positions, successful measurements, or SLA guarantees.

## Browser transport and editing

Generate browser types from the same OpenAPI sources, using a pinned local
generator and a small transport layer. Add an authenticated self/access response,
capability/form metadata, and paginated operation discovery. Keep the non-secret
SPA shell accessible for sign-in and protect all API data. Browser writes use
same-origin bearer requests over TLS or the explicitly trusted proxy contract;
Basic/read-only credentials cannot authorize mutations.

Implement Monitors, Recipients, Groups, Endpoints, Secrets, and Operations views.
Guided forms cover all 33 documented built-in driver configurations and show
actual compiled capabilities. Keep an edit draft separate from polling updates.
Freeze its observed version; on conflict present comparison/reload instead of
silently fetching a new version and overwriting concurrent edits.

For SDK-created custom or unsupported configurations, allow supported generic
observation and controls without exposing custom-job editing or rewriting unknown
configuration. Read-only principals get observation views; server checks remain
authoritative regardless of hidden or disabled buttons.

Report durably saved, controller applied, failed, and outcome unconfirmed
separately. Never automatically retry a mutation after a lost response. Reconcile
the original resource or operation identity, including after token re-entry.

## Bounded file import and operations

Initial browser limits are 1,000 files, 64 MiB total, 10,000 resources, and 1 MiB
per resource. Use bounded in-memory staging and parsing off the main UI thread;
do not persist plaintext inputs in browser storage. Reject conflicting definitions,
malformed trailing files, invalid references, and quota overflow before activation.
Preserve deterministic ordering and source/document attribution.

Upload at most 256 resources or 4 MiB per chunk. Validate the entire frozen
collection before per-resource conditional activation in dependency order. Show
partial outcomes explicitly; do not promise collection-wide rollback. A malformed
final file must produce zero active changes. Cancellation of waiting or a page
refresh does not cancel the server operation. An incomplete upload requires
reselecting identical source content to resume that operation; changed content
creates a new one.

List pages default to 100 and never exceed 500. Poll operations every five seconds
unless the server requests a longer interval. Explicit bulk selections are bounded
to 500 resources; do not silently select the whole fleet. Paginate navigation and
keep filtering/serialization outside the controller's execution path.

## Ordered implementation and acceptance

Each item requires independent review and relevant passing checks. None is marked
complete by saving this plan.

1. Complete the API foundation: encrypted durable catalog, authorization, CAS,
   operation identity, owner-loop reconciliation, and control admission.
2. Implement shared recipients, group routing, credential management, and reverse
   dependency validation through API, SDK, CLI, and collections.
3. Add browser types, in-memory bearer sign-in, access discovery, and transport.
4. Add guided CRUD, conflict handling, references, write-only secret replacement,
   and accurate committed/applied/unknown outcomes.
5. Add exact incident/monitor controls, descriptive diagnostics, file import,
   operation progress, and restart/reconnection behavior.
6. Qualify real-server/browser/SDK/CLI interoperability, then update help, docs,
   generated/embedded assets, and packaging against the same candidate.

Acceptance evidence must cover:

- Actual server CRUD, controls, encrypted persistence, restart, and API/SDK/CLI/UI
  parity; HTTP fixtures alone are insufficient.
- Recipient routing, multiple matching endpoints, stable deduplication, missing
  methods, reverse dependencies, and legacy group compatibility.
- Encrypted stored bytes and absence of secret plaintext in responses, audit,
  browser storage, errors, and exports.
- Reader, revoked, and Basic credentials denied writes; TLS/proxy and origin rules.
- Concurrent edits, delete/recreate, incident replacement, double submission,
  lost responses, storage failure, and conservative unknown-action handling.
- Malformed final files, partial activation, bounded inputs, resume/cancellation,
  token re-entry, and no unintended retries or fleet-wide requests.
- All built-in forms round-trip without erased defaults, false values, credentials,
  or unknown variants. Keyboard, mobile, and large-list behavior remain usable.
- Zero provider operations caused by probes, schema discovery, form validation,
  or dry-run. Runtime checks cover the Go/compiler/tag matrix and relevant race
  tests; dashboard checks cover types, lint, build, browser scenarios, reproducible
  generation/embedded assets, and documentation links.

The local `cpractl` build-workspace issue is a prerequisite development fix,
tracked as pending: unpublished SDK dependencies need an explicit or managed
development workspace. Honor explicit `GOWORK` settings, keep release builds at
`GOWORK=off`, and do not add local module replacements or fabricated checksums.
That build fix does not implement management commands.
