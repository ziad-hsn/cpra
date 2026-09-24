# Plan: Finalize the CPRa dashboard and ship the qualified release

Status: implementation in progress on the private local branch
`codex/dashboard-finalization`; implementation and release gates remain open.
Updated: 2026-09-20.
Source inspected: `/home/ziad/cpra-durable`, `codex/go-sdk`,
`410fbfb0092d01277b3884cd04151c27443a4226`, with existing uncommitted work.
No implementation, campaign or publication is performed by saving this plan.
Subsequent execution is tracked in the
[implementation progress record](dashboard-implementation-progress.md). The
starting-point observations below are the planning inspection, not a current
statement that no implementation files exist.

**Goal:** Ship CPRa with real durable management through its API, Go SDK, cpractl
and guided dashboard, and publish only after the agreed operational and evidence
gates pass.

**Scope:** Go server/controller/persistence, three Go modules and examples,
React/Vite dashboard, authentication, provider verification, performance evidence,
native packages/services, OCI/Compose/Helm, CI, documentation and OSS release.

**Subtasks:** 16 delivery tickets. This is release-sized work. Existing detailed
API/SDK/packaging tickets remain the implementation definitions; this document
orders them with dashboard completion and publication. Large foundation and
campaign tickets have the explicit work packages referenced below.

## Authority and preserved decisions

- [Management API plan](api-management-plan.md): wire semantics, catalog,
  encryption, Ark ownership, operations, controls and external-worker protocol.
- [Dashboard management plan](dashboard-management-plan.md): guided forms,
  shared recipients/groups/secrets, import limits and browser behavior.
- [SDK status](go-sdk-status.md) and [SDK publishing](../sdk/publishing.md):
  separate modules and qualification/publication boundaries.
- [Release engineering](../release-engineering.md): build, storage, service,
  package, container and chart contracts.
- This plan owns execution order and the user's 2026-09-14 decision:
  **keep candidates private until all gates pass**. It adds no public beta
  exception and does not waive provider, endurance, native or SDK gates.

The implementation scope is API first, followed by SDK, CLI and dashboard
management. Preserve existing read-only compatibility while adding authorized
writes. Notification contacts are not dashboard accounts. Code notification type
selects how to send; selected recipients/groups determine matching destinations.
Credentials are write-only and encrypted before Raft; keys stay outside the state
directory. Custom-job authoring and worker management remain outside the browser.
The project remains donation-supported OSS; subscriptions and hosted operations
are outside this delivery.

| Control | Required behavior |
| --- | --- |
| Acknowledge | Record authenticated actor, time and note; checks, notifications and recovery continue. |
| Dismiss | Suppress future notifications for the exact incident; checks and recovery continue. |
| Snooze | Pause checks, notifications and new recovery admission for a specified period. |
| Disable | Pause new checks, notifications and recovery indefinitely until enabled. |

Complements, guarded recovery and audited unknown-action review follow the API
plan. Started work may still complete; retain its outcomes. There is no check-now
command, force bypass or automatic replay of uncertain actions.

## Verified starting point

| Surface | Current observation | Implication |
| --- | --- | --- |
| Server and dashboard | V1 read routes; dashboard has GET-only requests; server test expects v2 POST to return 404. | Implement server writes and authorization before declaring browser management complete. |
| Persistence | Incident/action/SLO durability exists; encrypted writable resource catalog does not. | Reuse Raft, add canonical configuration and dynamic reconciliation. |
| SDK and CLI | Draft v2 SDK and worker library; cpractl uses legacy reads. | Add missing recipients/contracts and qualify against real server behavior. |
| Dashboard | Existing routing, themes, virtual table, history, SLO and queue displays; component tests. | Extend these foundations; add forms, sign-in, operations and actual-browser tests. |
| Packaging | Release tooling, native/service/package workflows, Compose and Helm exist. | Repair identified gaps and requalify final payloads; do not count historical reports as current release passes. |
| Publication | Packaging-first checks; candidate images may be pushed before final publication evidence validation. | Gate every public action and use genuinely private staging. |
| WSL campaign | Fresh physical C: check reports approximately 2.21 GiB free, below 30 GiB plus fixture headroom. | Campaign remains blocked until a fresh passing preflight; development can continue. |

This inspection did not rerun application, browser, platform or campaign tests.
Recorded older successes are reusable background, not evidence for a changed
release candidate. The complete current release remains unqualified.

## Execution order

The critical implementation path is **1 → 2 → 3 → 4 → 5**, followed by controls
(6), collections (7), and consumer completion (9–12). External workers (8) can
proceed after their durable/auth foundations. Browser scaffolding can proceed
against stable schemas, but only real-server results close its tickets.

Prepare native environments, provider configuration and docs in parallel without
claiming completion. Freeze the candidate at 13, qualify distributions at 14 and
campaigns at 15, then publish through 16. Any change to qualified code, dependencies
or artifacts requires an explicit impact review and replacement of affected
evidence. Do not combine reports from unrelated revisions to manufacture a pass.

## Implementation tickets

### Ticket 1 — Restore the development build

#### Context

The application depends on an unpublished nested SDK module; ordinary make build-ctl still bypasses the available workspace helper. Existing source changes must be preserved.

#### What to implement

Add one parallel-safe development workspace prerequisite for the application, SDK, and worker modules, excluding examples by default. Honor explicit GOWORK, including off, and avoid writing GITHUB_ENV from implicit Make setup. Inventory the current dirty changes and isolate the implementation on a codex/ branch without discarding or mixing unrelated work. Keep official release settings unchanged.

#### Where

Makefile; scripts/sdk/workspace.py; scripts/sdk tests; root and nested go.mod files; developer README.

#### Acceptance criteria

- [ ] Fresh and parallel make build/build-ctl work with an unset workspace; custom/off settings remain authoritative.
- [ ] The helper does not pollute CI environment or add distributable local replacements, fabricated sums, or example dependencies.
- [ ] Selected source changes and separate documentation checkouts are recorded before release preparation.

#### Out of scope

Publishing SDK tags to work around a development error; changing release reproducibility settings.

#### Depends on

None.

#### Technical notes

Direct Go commands may still require an explicit workspace until the nested dependencies are available. Candidate dependency qualification is defined in ticket 13; developer convenience must not bypass it.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 2 — Finalize the shared public contract

#### Context

Draft SDK v2 schemas exist, but the server does not implement them and the Recipient and browser access additions are missing.

#### What to implement

Freeze the canonical Monitor, NotificationEndpoint, Recipient, NotificationGroup, Credential, incident/control, operation and observation contracts. Add self/access discovery, capabilities/form metadata and paginated operations. Keep IDs, incarnation UIDs, configuration/control/status/execution versions distinct; define conditional writes, errors, omitted/false/zero/null values and duration units. Generate server interfaces, SDK public types/internal transport and browser types from the same reviewed inputs, including default/tagged schema projection.

#### Where

api/openapi/; tools/sdkgen/; sdk/go/api/; sdk/go/internal/; internal/httpserver/; dashboard/src/api/.

#### Acceptance criteria

- [ ] All 33 built-in driver shapes round-trip without provider execution dependencies in SDK types.
- [ ] Recipient and Code routing are represented consistently in schema, SDK inventory, CLI loaders and browser models.
- [ ] V1 compatibility, field errors, CAS preconditions and tagged exclusion have executable contract fixtures; generation is deterministic.

#### Out of scope

Kubernetes field ownership, strategic merge patch, a TypeScript SDK release, custom-job browser authoring.

#### Depends on

Ticket 1.

#### Technical notes

Follow the existing API plan's semantics; its draft schemas are inputs to review, not proof of server completion. Kubernetes-inspired versions protect concurrent updates without copying Kubernetes's entire API.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 3 — Establish the encrypted configuration catalog

#### Context

Current Raft state covers incidents/actions/SLOs, while executable configuration still originates from startup manifests. API-edited configuration needs its own durable authority.

#### What to implement

Implement the API plan's encrypted catalog and key-management work, including local and approved Transit/KMS wrapping backends, initial manifest migration, indexed shared references, protected staging primitives, and stopped backup/restore. Preserve stable IDs, action identities and existing incidents while extracting inline secrets. Restore the catalog after restart; bootstrap manifests must not overwrite committed API edits.

#### Where

internal/persistence/; internal/runtimeconfig/; internal/localadmin/; schema/loaders; existing API plan ticket 2.

#### Acceptance criteria

- [ ] CRUD-ready resources and encrypted credentials survive real process restart; old startup files cannot replace newer catalog state.
- [ ] Protected plaintext is absent from Raft commands, logs, snapshots, persistent staging and error/audit fields.
- [ ] Interrupted migration/key changes, missing or wrong keys, corruption, locked storage and full-directory restore have explicit tested outcomes.

#### Out of scope

Plaintext fallback, automatic destructive repair, keys stored beside encrypted data, multi-node failover.

#### Depends on

Ticket 2.

#### Technical notes

Use the existing Raft implementation. Keep runtime jobs, clients and randomness outside deterministic replay. History remains an allowlisted event record; this feature does not blanket-encrypt every telemetry field.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 4 — Enforce management authorization

#### Context

One shared read token currently protects both SPA and API, accepting Basic and Bearer equivalently. Writable management needs named actors and separate permissions.

#### What to implement

Implement local principal/token provisioning, reader/operator authorization, committed revocation, self/access discovery, TLS/trusted-proxy and origin/content-type enforcement. Serve the non-secret browser sign-in shell separately from protected data. Preserve legacy authenticated reads; Basic/read tokens never gain write permissions. Keep worker execution identities and operator JobType permissions distinct.

#### Where

internal/httpserver/; local cpractl administration; durable principal records; native/Compose/Helm authentication configuration.

#### Acceptance criteria

- [ ] Reader, expired/revoked and legacy Basic credentials cannot mutate any management resource or control.
- [ ] Acknowledgment and audit use server-authenticated actor identity; restoring a backup cannot resurrect revoked access.
- [ ] SPA sign-in, v1 reads, v2 writes, trusted proxy deployment and safe loopback probes have real-server authorization tests.

#### Out of scope

OIDC, team tenancy, user registration, notification contacts as login accounts, cookie sessions.

#### Depends on

Tickets 2–3.

#### Technical notes

Provider credentials are not dashboard tokens. Authorization belongs at the server; hiding buttons is only a usability measure.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 5 — Reconcile conditional resource changes

#### Context

Startup-only ECS population and stable-ID indexes are insufficient for online create, patch, delete and configuration changes.

#### What to implement

Implement create/read/list/replace/merge-patch/delete through conditional durable commits, followed by bounded owner-loop projection. Build clients/jobs outside the owner loop. Maintain incarnation-aware maps, scheduling indexes, reverse dependencies and bounded read indexes. Add Recipient/group resolution: Code selects driver type, contacts/groups supply matching endpoints, all configured matching destinations are used, and endpoint incarnations are deduplicated per notification occurrence.

#### Where

internal/controller/; internal/persistence/; internal/jobs/; internal/httpserver/; API plan tickets 4–5; dashboard shared resource contract.

#### Acceptance criteria

- [ ] Create/edit/delete/recreate works during monitoring and after restart without stale work reaching replacement targets.
- [ ] Missing contact methods, referenced deletion and invalid shared-resource updates fail before activation; legacy heterogeneous endpoint-only groups retain their behavior.
- [ ] Delivery intents freeze endpoint/configuration/credential identities; edits cancel unsent work while preserving started/unknown outcomes.
- [ ] Responses distinguish durable commit, controller application and unavailable storage; fleet reads remain bounded.

#### Out of scope

Direct Ark mutation from HTTP/FSM goroutines, implicit cascading deletion, automatic replay of unknown side effects.

#### Depends on

Tickets 2–4.

#### Technical notes

Reuse the Disabled tag and owner loop. Tags alone do not invalidate timing-wheel entries, queued jobs or worker copies; admission must recheck committed identity and eligibility.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 6 — Implement operator controls

#### Context

Acknowledge, dismiss, snooze and disable have approved distinct meanings; current runtime state does not implement their complete durable management contract.

#### What to implement

Implement exact-incident acknowledge/dismiss/reopen, timed snooze/unsnooze, disable/enable, guarded recovery and audited unknown-action review. Persist actor/reason/time/expiry and appropriate preconditions. Add descriptive monitor/state/queue/pool/SLO observations and control application progress. Preserve existing active Erlang C/Allen–Cunneen capacity modeling and percentile feedback.

#### Where

Durable commands; controller admission and scheduling; history; web observations; API plan ticket 7.

#### Acceptance criteria

- [ ] All controls retain their specified behavior across crashes, expiry, concurrent edits and incident replacement.
- [ ] Already-started results remain recordable; snooze expiry never enables a disabled monitor or replays missed intervals.
- [ ] Attention, pause state and last observed health remain independent; unavailable latency and coverage remain explicit.
- [ ] Queue saturation, age, scaling reasons and controller/storage limits come from measured state; no manual check endpoint exists.

#### Out of scope

Check now, force recovery, review-based automatic retries, guaranteed percentiles or contractual SLA.

#### Depends on

Ticket 5.

#### Technical notes

Intentional pauses neither count as healthy observations nor erase earlier misses. Validate manual recovery limits and unresolved action holds using the existing approved API contract.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 7 — Implement resumable collection operations

#### Context

Teams need the same multi-file semantics through CLI and SDK, with a bounded browser importer and no partial activation caused by malformed input.

#### What to implement

Implement encrypted server staging, whole-collection validation, redacted preflight/diff, bounded chunk upload, conditional dependency-ordered activation, item receipts, progress/resume/cancel/wait and expiry/restore epochs. Share the SDK loader with cpractl for files, explicit directory recursion, YAML/JSON documents, stdin/readers and explicitly requested URLs. Freeze source bytes and operation/content identity before activation.

#### Where

Server operation/staging modules; sdk/go/collection/; internal/cpractl/; API plan ticket 6.

#### Acceptance criteria

- [ ] A malformed final file or quota failure causes zero active changes; duplicates and dependency errors identify their source.
- [ ] Conflicts during activation produce accurate partial outcomes and blocked dependents, not collection-wide rollback claims.
- [ ] Lost replies and resumed identical uploads cannot duplicate committed mutations; cancelling a waiter does not cancel the server operation.
- [ ] Source fetching never receives CPRa authentication; secret-bearing persistent staging is encrypted and cleanup is bounded.

#### Out of scope

Implicit prune, filename-based overrides, templating, automatic watchers, a server URL downloader.

#### Depends on

Tickets 2–5.

#### Technical notes

Resource maximum 1 MiB; chunks at most 256 resources or 4 MiB; pages default 100/max 500. Preserve the approved 30-day terminal-operation retention and explicit expired handles.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 8 — Qualify the external-worker server

#### Context

The worker library exists as a draft, but real assignment/start/heartbeat/result routes and server safety behavior remain unimplemented. Excluding its UI does not remove this SDK release dependency.

#### What to implement

Complete the tagged server protocol against the existing SDK/worker implementation: versioned JobTypes, scoped enrollment, capacity-aware assignment, committed start permission, results/receipts, finalization and append-only late evidence. Exercise operator-owned check, recovery and notification handlers in separate processes with worker-local credentials and encrypted journals.

#### Where

Build-constrained server adapters/registration; durable execution records; sdk/go tagged protocol; sdk/go/worker; API plan ticket 8.

#### Acceptance criteria

- [ ] Untagged server routes/schemas/types and normal all-driver builds exclude externaljobs; tagged but disabled or unauthorized execution is rejected.
- [ ] Crash/lost-start/lost-receipt/revocation/late-evidence tests assert actual invocation counts and no uncertain action is invoked twice.
- [ ] Journal/outbox saturation stops admission without losing outcomes; unknown actions do not expire to reclaim space.
- [ ] Worker capacity, cancellation deadlines and uncooperative shutdown preserve the documented isolation boundary.

#### Out of scope

Dashboard JobType or worker management, uploaded executables, in-process plugins, hostile-code sandbox claims.

#### Depends on

Tickets 3–6; can run alongside tickets 7 and 9–11 after its foundations pass.

#### Technical notes

The server tag, runtime setting and authorization are independent gates. Keep the SDK and worker modules separate; worker code remains compiled out without externaljobs.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 9 — Complete the Go management consumers

#### Context

cpractl currently uses the SDK's legacy reads; drafted v2 SDK methods still need real-server qualification and new shared resources.

#### What to implement

Finish SDK services and operation coverage, then wire cpractl CRUD, apply/diff/patch, get/describe, controls, operation management and diagnostic commands to that public client. Add Recipient support and secret aliases; retain all legacy read/output/probe behavior. Update examples and consumer documentation using the final contracts, and test root, SDK, worker and examples as separate modules.

#### Where

sdk/go/; internal/cpractl/; cmd/cpractl/; examples/sdk/; scripts/sdk/; docs/sdk/.

#### Acceptance criteria

- [ ] Every supported public operation has an SDK method and server contract test; CLI outcomes agree with the same resource/operation state.
- [ ] Context cancellation, version conflicts, typed errors and ambiguous mutation outcomes remain distinguishable with no automatic mutation retries.
- [ ] Go 1.25 and the recorded release compiler pass applicable normal/tagged checks; private downloaded-module consumers use no local replacement.
- [ ] Examples build outside the application dependency graph and do not claim fixture results are live provider effects.

#### Out of scope

Local service/backup administration becoming remote API calls; silently downgrading v2 writes to v1.

#### Depends on

Tickets 2 and 5–7; tagged completion also requires ticket 8.

#### Technical notes

Keep SDK semantic versions separate from HTTP /api/v2. Publication remains private-gated under ticket 16; passing an archive fixture does not mean public tags exist.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 10 — Add the browser management boundary

#### Context

The current browser has GET-only v1 requests, a polling query cache and no sign-in or editable resource transport.

#### What to implement

Add generated v2 types, bounded/cancellable requests, typed field/problem errors, request/operation handles and explicit mutation behavior. Hold bearer credentials only in current-tab memory. Add self/access/capability discovery and reader/operator presentation. Cancel active requests and clear cached data/drafts on logout, revocation or identity replacement; ignore late responses from an earlier identity. Prevent offline queued mutations, automatic retries and reconnect replay.

Resolve bearer-authenticated management requests against the trusted current
origin and reject redirects. Do not inherit arbitrary VITE_API_BASE values from
the legacy read client when attaching operator credentials.

#### Where

dashboard/src/api/; dashboard/src/hooks/; dashboard/src/App.tsx; shared form/error/auth components.

#### Acceptance criteria

- [ ] Refresh requires token re-entry; tokens and secrets never enter browser storage, URLs, query keys or diagnostics.
- [ ] Two identities cannot see each other's cached data, including responses racing with logout.
- [ ] V1-only/read-only mode remains usable; v2 writes carry exact preconditions and do not silently retry.
- [ ] Offline/reconnect, cancellation and lost responses show failure or outcome unconfirmed without duplicate submission.
- [ ] Cross-origin targets and redirects are rejected without forwarding tokens
  or replaying mutations to another destination.

#### Out of scope

Cookie sessions, token persistence, external-job authoring, replacing the existing design system.

#### Depends on

Tickets 2, 4–5; development may proceed against reviewed fixtures, completion requires the real server.

#### Technical notes

TanStack supports mutation retries, offline persistence and resumption; CPRa must explicitly prevent those behaviors for uncertain writes, rather than relying only on a disabled submit button.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 11 — Build guided resource management

#### Context

Current monitor summaries are observational and cannot be safely reused as complete edit/replace payloads.

#### What to implement

Build shared accessible editor primitives and Monitors, Endpoints, Recipients, Groups and Secrets pages. Cover all 14 checks, five recoveries and 14 notification drivers using compiled-capability discovery. Use full resource reads, frozen observed versions and independent drafts. Provide deliberate conflict comparison/reload and explicit secret replacement while preserving unchanged references. Keep existing stable/numeric read links and generic observation/control access for unsupported or SDK-created custom resources.

#### Where

dashboard/src/pages/; dashboard/src/components/; dashboard/src/api/; source-attributed validation and form metadata.

#### Acceptance criteria

- [ ] An operator creates a secret, endpoint, contact, group and monitor, edits and deletes eligible resources, and sees the same state through SDK/CLI and after restart.
- [ ] Code selects the delivery type; contact/group selection resolves only matching destinations and exposes validation failures clearly.
- [ ] All driver forms preserve omitted/false/zero/null and secret references without raw JSON fallback or destructive unknown-field round trips.
- [ ] Keyboard-only, mobile, field labels, error focus, dialogs and dirty-navigation behavior pass browser tests.

#### Out of scope

Raw source editor, custom-job designer, recipient login accounts, a general dashboard redesign.

#### Depends on

Tickets 5, 9–10; shared form scaffolding can start once ticket 2 is stable.

#### Technical notes

Secret reads show metadata and availability only. Clear sensitive mutation variables/drafts after completion or cancellation; do not keep credential values in the query cache.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 12 — Complete the operator workflows

#### Context

Resource forms alone do not deliver the requested incident attention, maintenance, file application and descriptive operational console.

#### What to implement

Add control toolbars and history/audit displays, guarded recovery and unknown-action review, bounded YAML/JSON file import, redacted diff and operation progress/resume/cancel. Extend queue/pool/SLO/state and monitor describe views. Use stable-ID routes and bounded cursor pages/selections. Fix the fleet latency cell to use explicit availability rather than truthiness; finish virtual-table keyboard/screen-reader semantics.

Require a separate Apply/Activate action after whole-collection validation and
redacted review; upload and preflight never activate automatically. After refresh,
resume incomplete uploads only when reselected files match the original content
identities. Poll operation progress every five seconds or a longer server interval.

#### Where

dashboard monitor/incident/detail/system views; new Operations and Import routes; parser worker; shared table/timeline components.

#### Acceptance criteria

- [ ] Acknowledge/dismiss/snooze/disable and complements match the shared semantics, actor/reason/version boundaries and restart behavior.
- [ ] Browser import enforces 1,000 files, 64 MiB total, 10,000 resources, 1 MiB each and bounded upload chunks; malformed last input yields zero activation.
- [ ] Operation views distinguish committed/applied/failed/unconfirmed; refresh or stopped waiting never implicitly cancels or repeats server work.
- [ ] Pages default 100/max 500, explicit selections max 500; no hidden whole-fleet download or fabricated zero latency/queue position.
- [ ] Preflight has zero activation effects; identical files resume the original
  operation and changed files require a new operation with a new explicit review.

#### Out of scope

Browser URL import, check now, automatic recovery replay, 'select all million monitors' behavior.

#### Depends on

Tickets 6–7 and 10–11.

#### Technical notes

Parse off the main UI thread with memory-only staging. Preserve current virtualization/theme and accurate unavailable/stale/unknown observations; no historical per-monitor raw-check store is added.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 13 — Qualify the private management candidate

#### Context

Existing component fixtures and prior packaging reports do not qualify newly writable application behavior or a changed final artifact.

#### What to implement

Introduce a repeatable browser suite against a real CPRa process and persistent directory. Cover API/SDK/CLI/browser parity, two operators, restart/crash boundaries, encrypted storage, identity changes, unavailable storage, imports, controls and no unintended provider I/O. Maintain a private evidence manifest binding source commit, source inventory, compiler/recipe, nested module contents, target and executable digests to every report. Establish a private source-derived module proxy/cache for qualification before public SDK tags; record real computed hashes and provenance without altering official release isolation.

Before freezing this candidate, select every intended SDK/worker prerelease and
final version, plus the application and worker dependency requirements. Construct
the corresponding versioned module payloads privately and record their exact
hashes and dependency graph. The final application's dependency graph must be
qualified as it will ship; publishing an RC first does not authorize changing
the application from RC dependencies to stable dependencies after the campaign.
Commit the selected dependency declarations and genuinely computed checksums
before the source freeze. Use canonical Go module ZIP selection, including
exclusion of nested modules from their parent's module archive; update the root
source-install harness, which currently copies essentially the whole source
archive, to test the real multi-module distribution boundary.

Implement a separate, explicit bootstrap command that fills an isolated module
cache from the immutable private module proxy and verified public dependencies.
Scope any bootstrap-only checksum-database exception to the two unpublished CPRa
module paths; compute and record their actual module hashes from reviewed source.
Do not invent go.sum entries or disable checksum verification globally. Then
discard bootstrap environment overrides and run the exact recorded release
recipe, including GOWORK=off and its public proxy/checksum settings, against that
isolated cache. Record cache provenance and demonstrate that clean source
rebuilds work without hidden workspace or dependency changes. This establishes
private/offline qualification, not prior public-proxy or sumdb verification.

#### Where

dashboard browser tests; repository-level integration harness; scripts/sdk/; CI; scripts/release/ candidate/evidence validation.

#### Acceptance criteria

- [ ] Real-server tests replace v2-404 expectations only when routes are implemented; fixture-only results never satisfy management completion.
- [ ] Secrets, auth, CAS, lost responses, delete/recreate, unknown actions and interrupted apply have assertions against committed state and actual effects.
- [ ] All applicable Go/compiler/tag/race/vulnerability and dashboard type/lint/test/browser gates pass; generated assets and notices reproduce exactly.
- [ ] Evidence rejects missing/failed/stale/wrong-digest reports; clean-source private builds use no local module replacements or invented checksums.
- [ ] Intended module versions, application/worker requirements and genuine
  dependency hashes are frozen before final runtime/native/endurance tests;
  publication cannot rewrite the qualified dependency graph.

#### Out of scope

Public candidate publication, claiming cross-compilation is native execution, unbounded raw provider logs.

#### Depends on

Tickets 1–12, with independent verification alongside each completed ticket.

#### Technical notes

Private module downloads are labeled private. At publication, verify the public module archives and checksums equal the qualified inputs; any mismatch invalidates promotion. Account-backed tests never run in untrusted pull-request jobs.

Public download verification uses a fresh module cache and ordinary public
checksum verification, with no private bootstrap exceptions. Candidate cache
inputs must never be injected into the developer's normal module cache.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 14 — Verify the distribution matrix

#### Context

Archives, service installers, packages, images, Compose and Helm machinery exist, but their evidence must cover the final management candidate and its encryption/auth configuration.

#### What to implement

Run the existing release recipe and exact-payload verification for six native archives, four DEB/RPM outputs, two Linux image platforms, Compose and Helm 3/4. Qualify service identities/paths/permissions, readiness before drain, shutdown deadlines, Windows atomic persistence, upgrade/remove/reinstall/purge preservation, complete backup/restore and legacy data migration. Extend deployment templates for encrypted catalog keys, operator provisioning and token rotation. Verify two independent source extractions reproduce unsigned outputs.

Assemble the final multi-platform OCI index privately, preserving exact manifest
and layer bytes. Bind and package the final Helm chart to that index's intended
public repository/digest before its acceptance tests. Use private mirrors or
preloaded content for execution while retaining the final identities. Publication
copies these already-qualified bytes; it must not build a new index or rewrite
the chart after the aggregate gate has passed.

Test upgrades using two actual binaries: a selected previously published version
and the final candidate. Perform the stopped backup, replacement, restart and
state/history/unknown-action verification against that pair. Qualify rollback
only if the old binary supports newly written storage; otherwise state that it
requires the compatible stopped backup. Same-binary package-version ordering
fixtures remain useful but cannot satisfy cross-version storage compatibility.

#### Where

scripts/release/; scripts/native/; scripts/packaging/; packaging/; docker/; charts/cpra/; internal/installpath/; internal/localadmin/; separate docs checkout.

#### Acceptance criteria

- [ ] Native execution of each claimed OS/architecture and actual installed service/package lifecycle are recorded; cross-build/emulation remain labeled separately.
- [ ] Linux system services use /var/lib/cpra, Linux user mode uses XDG state, macOS/Windows use the approved platform locations, and containers use /var/lib/cpra; credentials/keys have separate protected handling.
- [ ] Restart/force-kill/corruption/failed-write/locking/backup/restore preserve committed configuration, history and unknown actions.
- [ ] Compose volumes survive recreation; Helm validates RWOP ownership, existing claims, slow restore, large configuration volumes, token rotation, failed upgrade and Helm 3-to-4 behavior; probes induce no provider work.
- [ ] Executable/archive/package/image/chart digests match what was tested; release outputs remain unstripped and reproducible with Go 1.25 compatibility and recorded toolchain hashes.

#### Out of scope

New installers or hosted package repositories, multiple owners, routine force deletion, automatic live backup hooks, Authenticode/notarization claims without configured signing.

#### Depends on

Ticket 13 for final evidence; repair existing packaging and stage native environments in parallel after ticket 3.

#### Technical notes

Retain 0/1 StatefulSet replicas, persistent claims and explicit stopped backups. CSI compatibility is not proof of bbolt locking/durability. Do not reduce the agreed support matrix to hide a missing native runner.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 15 — Complete the release evidence campaigns

#### Context

The user requires private candidates until all gates pass. Production-provider evidence and the full million-monitor campaign remain independently necessary.

#### What to implement

Use two separately recorded work packages: provider qualification and scale/endurance. Run actual configured scenarios for all 33 drivers with independent effect evidence, preserving local_integration/mock_contract/provider_sandbox/live_account classifications. On the agreed WSL machine, pass fresh physical C: space and fixture-headroom preflight, then compare baseline a370969b041b399c0778318d8915ce059fd74294 with the frozen candidate at 10k/100k/1m monitors, 60-second cadence, five-minute warmup and fifteen-minute measurement, three repetitions per build. Run burst/slow/outage/recovery/crash scenarios and an uninterrupted 24-hour million-monitor soak with durability, history, SLOs and representative dashboard reads active.

#### Where

cmd/cpra-verify/; internal/drivertest/; examples/verification/; scripts/verification/; scripts/benchmark/; cmd/cpra-bench-target/; protected evidence storage.

#### Acceptance criteria

- [ ] Every required configured driver has passing actual-operation/effect evidence; missing accounts remain not verified and block full qualification.
- [ ] Healthy windows sustain approximately 16,667 checks/second with five-minute p99 scheduling-plus-queue <=250 ms and scheduled-to-committed-result <=5 s, with misses/timeouts/coverage honestly counted.
- [ ] Counts/digests reconcile accepted actions and results; no unintended duplicate intervention or unexplained loss is hidden, and resource/queue trends stabilize outside declared faults.
- [ ] The full 24 hours include snapshot/history activity and preserved progress; sleep, saturation, interruption or reduced load fails or leaves the run incomplete.
- [ ] Reports bind exact candidate binaries and environment; no new public release is allowed while either work package is incomplete.

#### Out of scope

Creating or buying accounts, cleaning the user's disk, cloud-host substitution, simulated time, mock-derived certification or universal SLA claims.

#### Depends on

Tickets 13–14 for final artifact evidence. Fixture configuration and harness fixes may proceed earlier; provider and WSL workloads must not interfere.

#### Technical notes

The harness requires 30 GiB physical C: free plus fixture headroom. Its fresh check found about 2.21 GiB on 2026-09-14; remeasure before any campaign. The 18 matched runs take six hours, plus the 24-hour soak: at least 30 hours of runtime before separate faults, setup and reruns. If target failures occur, preserve measured limits and keep the release gate open rather than silently reducing the target.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

### Ticket 16 — Publish the fully qualified release

#### Context

The existing publisher checks packaging evidence but can push candidate images before its final checks. The new private-candidate policy requires a single gate before any public release artifact or tag.

#### What to implement

Add a fail-closed gate covering management, SDK/worker, native packages/services, container/chart, security, docs, all-provider and endurance evidence before the first public action. Keep candidate builds, module proxies, OCI storage and reports access-controlled; public-repository CI artifacts and public candidate tags are not private staging. Prepare strict source/generated docs, installation/upgrade/restore examples, support/security/contribution guidance, existing license notices, and donation information without subscription features. Freeze one source/artifact identity; stage, download, verify and sign exact outputs. After all private gates pass, publish the already-planned nested SDK prerelease, verify public downloads, promote the qualified SDK versions, then release the application, image aliases, OCI chart and matching main/gh-pages documentation in dependency order.

Publish only the versioned dependency graph frozen in ticket 13: core SDK before
the worker version that requires it, then the application with its already-fixed
requirements. Qualify each intended RC/final identity in private beforehand.
There is no release-time dependency bump, asset regeneration, chart rebinding or
source-metadata rewrite; such changes create a new candidate and require new
affected evidence. Update release documentation to describe the application,
SDK and worker as separately versioned modules rather than one source module.

Treat keyless signing/attestation issuance and transparency-log submission as
public actions too. Prepare signing configuration and private integrity checks
earlier, but obtain and verify official public certificates/attestations only
after the aggregate private gate opens. Private signing tests must not be labeled
successful official workflow identity verification.

Keep new candidate source branches, pull requests and review artifacts private
as well. Go can resolve public commits through pseudo-versions without an RC tag.
After the aggregate gate passes, the reviewed frozen commit can be moved to
published main before the protected publication workflow runs. Preserve its
source identity; if main requires a changed release tree, requalify the candidate.

#### Where

.github/workflows/release.yml; scripts/release/publish.py and registry/chart tooling; SDK publication tooling; README and docs; confirmed separate gh-pages checkout.

#### Acceptance criteria

- [ ] Missing, failed, stale, incomplete or mismatched evidence prevents every public tag, SDK prerelease, image, chart and release asset action.
- [ ] SDK public archives match privately qualified inputs; genuine public checksum/download verification and application GOWORK=off source installation pass without replacements. If final inputs or bytes change, rerun affected qualification before promotion.
- [ ] Signed checksum/provenance/SBOM bundles and downloaded payloads verify expected repository, workflow, issuer and digest; published versions are immutable and never overwritten.
- [ ] All six platform support claims and provider/performance claims link to matching evidence; rendered docs, links and embedded dashboard identify the same source candidate.
- [ ] Interrupted publication records completed steps and resumes only the same qualified identities without regenerating/replacing published assets.
- [ ] Public registries preserve the privately tested platform/index/chart
  digests; a registry transformation or public module/checksum mismatch stops
  promotion instead of silently accepting different payloads.

#### Out of scope

Publishing during this planning task, public prereleases while gates are pending, moving stable tags, claiming platform/production/scale certification from fixtures.

#### Depends on

Tickets 13–15, plus strict documentation/signature staging prepared in parallel. Every pre-publication gate must pass before publishing even an SDK prerelease.

#### Technical notes

Public download/registry verification necessarily runs after the corresponding object first exists; prepare private readback beforehand and stop promotion on any post-publication mismatch. Do not present those post-publication checks as completed in private. Native app, SDK modules, chart versions, storage format and source commit retain distinct recorded identities. Human review remains the final concrete release decision.

#### Definition of done

- [ ] Acceptance criteria are satisfied with recorded, appropriately scoped evidence.
- [ ] Relevant tests/checks pass; no new build, lint or type errors remain.
- [ ] An independent review is completed and findings are resolved.
- [ ] Public behavior, limits and outstanding gates are documented.

## Shipping rule and private staging

An interim enforcement lock is implemented locally: the release workflow accepts
only an explicitly private repository with `publish=false`; every qualification
job depends on that initial check. Direct publication remains disabled. SDK report
uploads and live-campaign workflow execution require private repository visibility.
This closes the known premature workflow paths while the aggregate evidence gate
is unfinished; it does not complete ticket 16 or authorize any candidate push.
See [release engineering](../release-engineering.md) for the exact boundary.

Until tickets 13–15 and the pre-publication checks in ticket 16 pass, retain
candidates in access-controlled local/private infrastructure. No public SDK
prerelease tags, candidate image tags, chart pushes or release attachments are
permitted merely because they are labeled RC, draft or experimental. Verify
actual artifact/registry access; public repository build artifacts are not an
assumed private store.

This also covers newly published candidate source branches/PRs and public signing
or transparency records. Previously published source is not retroactively made
private. Native qualification needs access-controlled execution environments;
availability of a public CI runner does not make its logs/artifacts private.

All required evidence must name the candidate commit and applicable artifact
digest, toolchain, platform/filesystem/storage class and test boundary. The
publication gate must inspect report contents and required case coverage, not
only file existence, workflow success or a caller-supplied `status: pass`.

Accounts/designated targets, physical free space and missing native execution
environments are external prerequisites. They remain visible blockers; this plan
does not authorize account creation, purchases, disk cleanup or service disruption.
No shipping date is promised before these prerequisites and the implementation
critical path are resolved.

## Reference checks used in this plan

- [Kubernetes API concepts](https://kubernetes.io/docs/reference/using-api/api-concepts/)
  supports resource versioning and bounded collection semantics. CPRa retains its
  own conditional-activation contract and does not claim Kubernetes field ownership.
- [TanStack mutation behavior](https://tanstack.com/query/latest/docs/framework/react/guides/mutations)
  documents optional retries, persisted mutation resumption and serial queues;
  CPRa's explicit no-retry/no-offline-replay policy is an application decision.
- [Go module version rules](https://go.dev/ref/mod#vcs-version)
  governs nested-module tag prefixes. Draft release visibility does not establish
  that an installable tag is private.
- [Go module ZIP rules](https://go.dev/ref/mod#module-zip-files)
  governs archive selection, including nested module boundaries. Private
  source-install qualification must represent the same module contents.
- [Sigstore blob signing](https://docs.sigstore.dev/cosign/signing/signing_with_blobs/)
  documents signing bundles and transparency inclusion. Public signing actions
  follow the private qualification gate, with identity verification at publication.
- [GitHub immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases)
  describes protected published assets/tags. Verify final signed/downloaded bytes
  before promotion and issue a new version for later corrections.

The current repositories' pinned toolchain, action, image and chart versions
must be checked against official distribution metadata at candidate freeze.
A planned version pin is not evidence of present runner availability or a
successful native platform test.
