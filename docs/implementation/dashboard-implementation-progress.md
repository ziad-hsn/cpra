# Dashboard management implementation progress

Updated: 2026-09-20. Local branch: `codex/dashboard-finalization`.
Base commit: `410fbfb0092d01277b3884cd04151c27443a4226` plus preserved and ongoing
uncommitted work. This is an implementation log, not a qualified release report.

The active delivery is the complete [dashboard management plan](dashboard-management-plan.md),
including its [API prerequisites](api-management-plan.md). Release publication
remains private until every gate in the [shipping plan](dashboard-shipping-plan.md)
passes. No public candidate, tag, image, chart or signing publication has been
performed during this implementation.

## Implemented components and present boundaries

| Component | Implemented locally | What is still required |
| --- | --- | --- |
| Development build | Parallel-safe managed Go workspace for the unpublished SDK; explicit `GOWORK`, including `off`, respected. Actual development application and CLI builds passed. | Downloaded public modules remain a release gate; release isolation is unchanged. |
| Shared contract | Recipients, recipient-containing groups, Code `notifyType`, access information and operation listing in generated SDK types, transport and inventory. | Actual server coverage for every supported operation; final schema review. |
| Encryption | AES-256-GCM envelopes, protected local key-file provisioning/loading, and OpenBao/Vault Transit and AWS KMS wrappers with bounded authenticated transports. | Offline key maintenance/retirement and complete cross-platform restore qualification. Normal startup now selects and verifies the configured backend without fallback. Remote backend evidence uses local TLS protocol fixtures, not production accounts. |
| Raft catalog | Catalog-bearing snapshots/logs, format-3 encrypted collection snapshots, ciphertext-only resources, conditional edits, direct/reverse reference indexes, immutable bounded pages and encoded commit-byte budgets. Exact owner-installed markers now back observed generation. | Collection activation and final qualification. Bounded operation listing is integrated; durable epoch/sequence allocation now precedes public mutations; reservations are bounded and expire safely. Initial encrypted migration and incremental owner reconciliation are connected. |
| Resource service | Full-resource create/replace preparation, merge patch, deletion preparation, write-only credentials, compiled-driver checks, recursive affected-graph validation and guarded commits. | Collections and final release qualification. Guarded recovery and audited action review are connected; normal startup, operational projection and affected-consumer validation on secret rotation are connected. |
| Authorization | Committed named reader/operator verifiers, one-time bootstrap import, exact permissions, expiry at admission, TLS/proxy boundaries and stopped local token administration. Explicit restore resets authentication before ordinary admission. | Package/deployment qualification and final integrated release checks. Normal startup/revocation/restore now have real TLS/Raft tests; installed service permissions remain separate. |
| HTTP | V2 CRUD/self/discovery, exact incident attention and snooze controls, indexed incident reads and operation lookup, strong version validators, bounded principal-bound cursor views and direct TLS pass real TLS-listener/SDK tests. Delete returns a durable receipt; HTTP never marks projection complete. | Collection operations. Bounded operation listing is integrated. V2 queue/pool/system/SLO/state/config/health/metrics observations are now connected and qualified through the SDK. Indexed action/history reads, guarded recovery and audited review are connected. Normal main now supplies readiness and joins the HTTP-admission, durable-queue and owner-drain barriers. |
| Notification routing | Ordered matching destinations, stable endpoint-incarnation deduplication, typed recipient routing and preserved legacy heterogeneous endpoint groups. | Complete end-to-end routing matrix and incident controls. Managed dispatch uses current dependency guards; credential rotation during an in-flight recovery retains known late evidence and keeps unknown actions held. |
| Browser boundary | Current-tab memory token, identity-fenced requests/caches, no redirects or mutation retries, permissions and explicit uncertain outcomes. Actual Chrome scenarios pass against real TLS handlers and encrypted Raft. | Broader browser/accessibility and final release qualification. Normal main/controller and embedded-asset controls have passed; the new action-browser campaign is recorded below. |
| Shared-resource screens | Guided monitor, endpoint, recipient, group and secret forms; bounded operation listing, exact lookup and receipt polling; available queue/SLO diagnostics. Actual browser checks cover CRUD, conflicts, references, reader denial, secret redaction and receipts. | Collection import and final integrated qualification. Incident controls, enable/disable, guarded recovery and action review have dedicated UI coverage; normal-startup control verification passed. |
| Collection foundation | Bounded preflight/CLI diff, encrypted inactive upload and original-ticket admission, snapshot/log replay, public inactive cancellation, retained terminal receipts, scoped whole-graph validation, inactive durable plan/result staging, bounded 30-day result publication, original request/claim storage, a main-owned single compiler worker with exclusive startup retirement and bounded shutdown, public async validation admission and original bounded retained-result reads, and a real-browser file-preview path using the production Worker/parser. | Conditional activation and item outcomes, cancellation during activation, original-operation reselection/resume, collection listing, cardinality qualification and automatic physical cleanup. Component evidence below does not claim complete apply. |
| Runtime preparation | Strict mappings for all 33 built-in drivers, resolved credential copies, legacy fingerprint preservation, ordered endpoint identities, and retained recovery/maintenance settings. | Final observed-generation/control projection and fleet measurements. Startup configures bounded batches; background preparation hands one private bundle to the Ark owner; recovery limits/cooldowns and absolute maintenance are durable. |
| Migration preparation | Shared parser streams into synchronously encrypted staging with complete-input validation, authenticated inventory and original-ciphertext pagination. Legacy conversion preserves tested defaults, absent/empty notifications and execution identities. | Full deployment restore and migration tooling qualification. Normal startup selects the authoritative catalog, resumes only the original frozen encrypted stage, and refuses incomplete or mismatched input. Offline backup rejects pending migration. |
| Execution fencing | Complete current dependency guards on configure/start/pulse/remove; durable monitor/action incarnation ownership; one-time adoption of existing manifest incidents and action IDs. | Expanded final release failure campaigns. Process-local executor claims, durable completion markers and restart session fencing support audited review. Exact incident/snooze revisions now fence queued work and retain real started observations. Guards now fence queued health checks and committed action starts; result correlation keeps superseded results out of replacement incidents. |
| Observation membership | Dynamic insert/rename/remove index; O(log N) membership edits, atomic observation rows, filtering outside the owner lock. | Final fleet measurements and richer v2 observation contracts. Dynamic owner reconciliation now updates the dashboard index on insert/rename/remove. |
| Scheduler cancellation | Indexed due-time replacement/cancellation, reusable ready-queue slots using full Ark entity identity, and driver-level intentional-pause SLO exposure. Durable controls use a bounded change stream and staggered resume. | Fleet measurements. Disable/edit/delete cancel scheduler membership and pending worker copies recheck committed guards. Pause exposure covers owner-observed intervals; restart gaps remain unavailable. |

## Owner installation and intentional pause measurements

Monitor `observedGeneration` requires an owner-installed marker bound to the exact
monitor incarnation, resource revision and resolved dependency closure. Saving
configuration alone does not set it. Startup publishes the marker after building
the operational index and schedules. Restart rebuilds markers; shared credential
rotation invalidates them even when the monitor's spec generation did not change.
A deterministic test loads the catalog before starting the owner and requires
the observation to remain zero until initialization completes. Initialization
failure also joins the reconciler during shutdown rather than waiting for a
goroutine that never started.

A `CheckPause` ECS component tracks disabled-or-snoozed membership once per monitor.
Driver-level rolling aggregates report the current paused count and elapsed
monitor-seconds. Overlapping controls count once; driver edits transfer membership,
and deletion removes it. Pauses add no successful samples, erase no earlier
scheduled obligations or misses, and do not estimate unobserved downtime.
Persisted exposure survives restart while current membership is rebuilt. Work is
bounded by driver/window limits, without a paused-fleet scan at each report.
Dashboard and CLI distinguish missing pause measurements from measured zero.

Local verification for this slice:

- Owner/restart/dependency and pause lifecycle tests passed Go 1.27.1 race in
  5.178 seconds. Initialization-failure/shutdown and CLI pause rendering selections
  passed in 1.431 and 1.072 seconds.
- Full Go 1.25.0 controller suite passed in 33.583 seconds; entities, systems,
  jobs and SLO passed in 0.044, 0.093, 0.252 and 0.011 seconds.
- SLO synthetic distribution, bounded exposure, restart, backward-clock and invalid
  snapshot tests passed Go 1.27 race in 1.030 seconds. Fractional pause exposure
  exposed an encoded-byte walker that rejected floating point; finite-number
  accounting now preserves the existing limits and has a persisted-SLO regression.
  Full durable checks passed Go 1.27 race in 68.456 seconds and Go 1.25 in 40.868
  seconds, also covering authentication/reset.
- The pinned clean dashboard build, type checking, lint and generated-contract
  check passed with 162 tests across 14 files (9.99 seconds). Embedded assets
  and notices were regenerated. Embedded index SHA-256:
  `e78dd0987521b56516b00e4cd027bc33b269e254a68037393f0ba1421365c110`.
- Independent owner initialization and pause reviews found no remaining actionable
  issue in these slices. V2 observation endpoint qualification has passed; its exact scope is recorded below.

Earlier browser evidence below identifies the assets it actually executed. This
new build alone does not requalify those scenarios or any shipping gate.

## Allocated operation handles and observed API progress

Public resource edits, attention/snooze controls, manual recovery and action
reviews now reserve a durable `op.<epoch>.<sequence>` identity before submitting
an active target change. Configuration, control and review versions remain
independent. Pure preparation allocates nothing. The reservation contains bounded
identities and a content digest, not provider inputs or executable work.

An unconfirmed allocation submits no target mutation and provides no invented
handle. Its exact HTTP 503 problem and `not-submitted` header are verified by
both the SDK and dashboard before reporting this distinction. Other ambiguous
server errors retain uncertain outcomes. Duplicate JSON names, invalid UTF-8,
malformed MIME headers, contradictory operation handles, null or incorrectly
shaped problem fields cannot turn uncertainty into a non-admission claim. Neither
client retries a mutation automatically.

Confirmed reservations retain their handles on admission conflicts, with zero
committed/applied counts. Handles are published atomically for concurrent readers.
The receipt state separates reservation, target commitment and owner application.
Reservations and active receipts share a 4,096-entry quota; unused reservations
expire after 24 hours with bounded maintenance. Snapshot/replay retains the
allocation high-water mark; explicit restore changes the operation epoch. The
operation-list implementation is qualified separately below; these earlier
lookup/admission tests do not establish its pagination behavior.

V2 observations now expose the actual queue, pool, system, state, configuration,
readiness, liveness, SLO and metrics aggregates through the public SDK. Required
fields follow the canonical schema independently of zero-emission choices.
At this earlier checkpoint, queue age, exact busy-worker counts and other
unrecorded measurements remained unavailable. Queue age was subsequently added
and qualified below; exact busy-worker counts remain unavailable. Prometheus
labels use the documented escape rules; an actual
scrape was parsed independently with the official Prometheus parser. See
[management observations](../management-observations.md).

Private verification for this slice:

- Full durable Go 1.27 race passed in 74.118 seconds and Go 1.25 in 43.541
  seconds. Final allocator/authentication/restore race coverage after bounded
  maintenance integration passed in 45.238 seconds. Tests include actual Linux
  process termination, reservation quota/expiry, content binding and restore.
- Full management, server and CLI Go 1.27 race suites passed in 47.136, 25.232
  and 3.402 seconds. The controller suite exposed a one-millisecond fixture
  snooze that elapsed during allocation. The fixture now waits actual committed
  deadlines; a separate regression rejects already-elapsed preparation with
  zero writes. The corrected full controller race suite passed in 41.662 seconds.
- Concurrent prepared-handle reads and elapsed-deadline rejection passed race in
  1.169 seconds. Actual TLS/SDK operation/error-writer interoperability passed
  race in 1.252 seconds and Go 1.25 in 0.205 seconds. The error-writer fixture
  tests transport classification, not a real storage failure injection.
- The pinned clean dashboard build, type checking, lint and contract generation
  passed with 206 tests across 14 files in 7.63 seconds. Embedded index SHA-256:
  `6b0f1bcbd0c711c5a8b40d35e396a106f38b6494685aeadfd7b10d2da709db4f`.
- Normal-main Chrome verification executed those embedded assets with actual TLS,
  encrypted Raft and local HTTP/webhook targets in 27.557 seconds. It observed
  105 local checks, exactly one recovery request and three explicit browser
  writes. Named operator review and the unknown provider outcome survived
  restart without another recovery request. Evidence is retained locally in
  `bin/verification/main-browser-actions/result.json`; a rendered review screen
  was inspected. This does not qualify production accounts or performance.
- Independent allocator/admission, observation, browser/SDK error-boundary and
  expiry-test reviews found no remaining actionable issue in those slices.

The asset hash above identifies the earlier completed browser scenario. The
newer operation-list build and browser evidence below supersede it for that UI
scope. Neither is an attestation of the complete working tree or shipping approval.

## Bounded operation discovery and measurement presence

The API, SDK, CLI and dashboard now expose retained operation lists with an
exact optional monitor filter. A list freezes at most 4,096 live receipts and a
terminal-history watermark. Completions and later admissions do not duplicate
or enter that original list. Terminal rows use daily on-disk indexes, with at
most 10,000 charged lookups across 64 segments per page. A bounded empty page
can still provide an advancing continuation. Cursors preserve their principal,
authority generation, filter and page size; expiry or unreadable history is
explicit. HTTP snapshots share count limits and enforce live-copy byte quotas
before allocation. See [operation observations](../management-operations.md).

Normal startup migrates old terminal indexes in synchronous batches of at most
256 events, with committed progress and validation against authoritative event
records. A real terminated-process test resumes this migration. Administrative
opening does not migrate. Review added cancellation checks in application scans
and encoded-size checks before decoding corrupt values. The bbolt physical
integrity check must still drain before its read transaction can safely close.

The public SDK now preserves absence independently of numeric zero and boolean
false for all six operation-progress fields. A real HTTPS-to-SDK-to-CLI test
exposed the previous loss of presence through a typed response; the corrected
models use optional pointers and reject unsupported JSON null. Mutable collection
resume with missing upload progress stops after its GET, before any upload,
validation or activation. This is client-boundary coverage, not implementation of
server collection routes.

Verification completed for this slice:

- Full durable Go 1.27 race: 88.665 seconds; final focused index/corruption race
  after the reviewed byte checks: 8.681 seconds. Full final Go 1.25 durable:
  50.873 seconds. Durable vet passed.
- Real TLS/public-SDK list and admission contracts passed Go 1.27 race in 1.684
  seconds and Go 1.25 in 0.479 seconds. Quota tests exercise seeded accounting
  charges rather than claiming large allocation measurements. Exact evidence:
  `bin/verification/operation-list-verification.json`.
- Full SDK and CLI suites passed Go 1.27 race and Go 1.25; tagged focused checks
  and vet passed. Real HTTPS resume-with-missing-progress passed race in 1.121
  seconds and Go 1.25 in 0.114 seconds. Two isolated schema-generation roots
  match current output; see `bin/verification/operation-presence/generation.json`.
- Pinned dashboard build, type checking, lint and generated-contract checks
  passed with 213 tests across 15 files. Embedded index SHA-256:
  `4db016463f2bfd116251bb11d61e3b2cc29c61796eda4cbce32dfb1acb1e7151`.
- Normal-main Chrome verification passed in 26.975 seconds with those assets:
  103 actual local checks, exactly one recovery request and three explicit
  browser writes. A reader filtered retained operations and opened the original
  completed review; reads did not repeat recovery. The review and unknown
  provider outcome survived restart. The rendered operation list was inspected.
  Evidence: `bin/verification/main-browser-actions/result.json`.
- A fresh native `cpractl` executable passed the normal-main integration in
  60.248 seconds. Reader commands traversed and retried the original monitor
  cursor, opened its exact patch receipt and kept table output to one page.
  These reads caused zero effects while the monitor was disabled. The wider
  existing scenario observed 96 local checks, one unknown recovery effect and
  one deliberately lost committed response without an automatic mutation retry.
  Exact executable SHA-256 and scope are recorded in
  `bin/verification/operation-list-native-cli/result.json`; its invocation count
  explicitly covers the primary runner only. This was a graceful restart test,
  not a process-crash or power-loss test.
- Independent reviews covered durable pagination/index migration, HTTP/cursor
  boundaries, SDK progress presence, CLI list behavior, the native CLI scenario
  and the browser list.
  Reported issues were corrected; no remaining findings in those reviewed slices.

Collection upload/activation and browser import remain unfinished. Their first
pure staged-union validator passed the full management package race suite on
Go 1.27.1 (75.455 s), focused Go 1.25.0 collection/catalog/routing/credential
regressions (4.008 s), vet and independent review. It covers 600 independent
monitor additions, source attribution, outside-consumer safety at every ordered
prefix, original version guards, routing-work limits, cancellation, unchanged
ciphertext and zero provider effects. Its limits are 10,000 desired/live resource
versions, 32 MiB of accounted payload/metadata and 100,000 work visits; these are
bounded preflight limits, not million-resource staging qualification. Evidence:
`bin/verification/collection-preflight-verification.json`. The helper itself
does not expose collection routes, seal new data or make active changes.
Candidates remain private.

## Keyed file inputs and ephemeral collection preflight

The SDK now freezes each collection under a fresh private key. Item commitments
bind exact transmitted resource-object bytes, ordinal, resource identity and
opaque source coordinates. Original filenames and URLs stay in the client's
private source map. Refreezing even identical files creates a different identity;
resumption requires the original live `Frozen` instance. This does not implement
cross-process or browser-refresh resume. Key and source-fingerprint request fields
are write-only, and collection receipts acknowledge format, digest and count.

`POST /api/v2/collections/preflight` is registered in normal startup and discovery.
It verifies the raw object spans before decoding, then resolves staged and live
dependencies under authorization. The endpoint returns redacted proposed changes
with explicit false committed/applied counters. It creates no operation, stage,
encryption key or active catalog mutation. Request limits are 4 MiB, 1 MiB per
resource and 10,000 submitted items, plus the separate graph bounds above.
Persistent upload/activation is still required for complete collection apply.

- Endpoint TLS/public-SDK checks passed Go 1.27.1 race testing (3.945 s), Go 1.25.0
  testing (1.920 s) and server vet. Signed identity tampering, duplicate IDs,
  independent byte/item bounds, actual slow-body deadlines, missing references,
  denied-reference non-disclosure and zero state/key/provider effects are covered.
  Independent review found no remaining blocking finding in that endpoint.
- `TestMainManagementCollectionPreflightDoesNotActivate` passed against normal
  `runCPRa` TLS and Raft startup on Go 1.27.1 with race detection (2.603 s) and
  Go 1.25.0 (1.180 s). Five separate files propose one monitor edit and four shared
  resources; validation reports all five correctly. A malformed final file and
  an authoritative missing reference are rejected. The original monitor and
  operation receipt survive restart without new resources, provider calls,
  notification output or persisted proposed plaintext. This is a graceful restart
  and preflight test, not a collection-activation or crash-recovery test.
- Canonical schema generation matched in two isolated source roots. The pinned
  dashboard rebuild, TypeScript, lint and all 213 tests passed. Embedded index
  SHA-256 remains `4db016463f2bfd116251bb11d61e3b2cc29c61796eda4cbce32dfb1acb1e7151`;
  this schema-only update does not introduce a browser import screen.

See [the identity contract](collection-identity-contract.md),
`bin/verification/collection-preflight-endpoint/result.json`, and
`bin/verification/collection-preflight-normal-main/result.json`, and
`bin/verification/collection-identity-generation.json`. Full SDK default and
`externaljobs` race suites passed on Go 1.27.1, both build variants passed on
Go 1.25.0, both SDK vet variants passed, and regenerated references/guide sync
plus five documentation tests passed. Independent review found and corrected a
canceled-empty-preflight success path; final review has no unresolved blocking
finding in this slice. Durable collection apply and browser import remain open.

The browser has a native Web Crypto counterpart for this inventory protocol in
`dashboard/src/api/collectionInventory.ts`. Independent Python vectors match
both Go and browser HMACs, including exact large-number and escape spans. Ten
helper tests, type checking, lint and an actual Chrome helper run passed;
`bin/verification/browser-inventory/result.json` records the source identity and
boundaries. Ordinary inspection exposes only a safe summary; explicit request
serialization contains private input and must never enter browser storage.
The holder clears owned byte buffers and drops references when closed, without
claiming erasure of JavaScript or HTTP copies. Independent source review found
no blocking issue in this helper.

This initial helper accepts at most 4 MiB of already-loaded source bytes and a
4 MiB preflight request. It is not an import screen or a completed implementation
of the approved 1,000-file/64 MiB browser contract. Off-main-thread parsing,
larger bounded source handling, encrypted upload/activation, and original-operation
resume still require implementation and qualification.

## Stopped local authentication administration

The [local authentication guide](../local-authentication.md) documents
`cpractl local auth list`, `bootstrap`, `issue`, `rotate`, `revoke` and
post-restore `reprovision`. These commands open only an exclusively locked local
Raft store. They do not open an HTTP client, load a monitor configuration or
decryption backend, initialize a controller, or invoke a provider.

Issuance publishes 32 cryptographically random bytes encoded as a bearer token
to a new, explicitly selected protected file outside the state directory. The
file is flushed and exclusively published before its SHA-256 verifier is
submitted with the exact authentication epoch and policy revision. Token values
and verifiers are excluded from command output. Rotation preserves the principal's
role and existing expiry unless a new expiry is explicitly selected; there is
no implicit expiry or overlap. An unconfirmed commit retains its original file
and intended revision for inspection, without retrying issuance.

The local policy is bounded to 1,024 retained principals. Reader and operator
roles are explicit. The first named issuance into an anonymous-only policy ends
anonymous access. Revoking the reserved `legacy-read` identity clears only the
shared legacy verifier. A restored store requires explicit reprovisioning in its
new current epoch, with freshly chosen credentials rather than resurrected grants.

Current local evidence:

- Go 1.27.1 race checks passed the new protected-file tests (1.042 seconds),
  stopped authentication tests (7.299 seconds), and CLI authentication tests
  (2.240 seconds), including an actual separate
  process rejected by the active bbolt lock, stopped issuance, restart,
  rotation/revocation, and complete-directory backup/restore with current-epoch
  provisioning. The administrative reopen preserves a previously started
  incident action unchanged; it does not perform normal runtime recovery.
- Go 1.25.0 passed the protected-file, local-administration and CLI selections
  (0.060, 3.890 and 1.126 seconds). CLI tests use actual stopped Raft stores and
  make zero HTTP requests. Non-bootstrap commands also reject empty or incomplete
  state without creating files; only explicit bootstrap may initialize a store.
- Injected policy-store tests cover uncertain committed replies, preserving the
  same output file, exact revision inspection, principal limits and permissions,
  role/expiry preservation, and no bearer material in output or durable files.
- Go vet passed for `internal/secureconfig`, `internal/localadmin` and the CLI.
  Independent review of the local CLI, administrative helper and protected-file
  writer found no actionable issues.

- A Go 1.27.1 cross-built test executable ran natively on Windows/amd64
  (Windows 10.0.26200, NTFS): ancestor ACL validation, exact private-file
  publication, rejected/canceled input and concurrent exclusive publication
  passed in 0.279 seconds. The Linux-specific mode/symlink case was explicitly
  skipped. Private output and the bounded runner are in
  `bin/verification/local-auth/windows`; binary SHA-256 is
  `7dfbc4d53b13d3fb02cb4f7eba966995514f225c010d3cfb2cd2f52abe027708`.
  A preceding direct-UNC launch returned no test output or exit code and remains
  recorded as a failed launch. The passing run copied that exact binary to a new
  temporary Windows directory, used a verified parent and removed it afterward.

This evidence covers native Linux/amd64 local administration, separate-process
locking, and the narrower native Windows protected-file helper. It does not
establish native Windows/macOS local authentication, installed service ACLs,
full command-line executable qualification, or release publication.

## Incident controls and CLI integration

The durable incident/control foundation, actual HTTP controls and owner projection
are implemented locally. Acknowledge, dismiss and reopen use the exact incident
and attention version. Snooze/unsnooze have a separate control version; enable and
disable remain conditional `spec.enabled` patches. Actor/time/reason/note are
retained in history, while the owner keeps a compact `ControlState` component.
Snooze is bounded to 30 days, preserves disabled state across restart, cancels
unsent intents and retains actual outcomes of already-started work. Old worker
copies recheck their captured control revision before invoking a provider.

Expiry work is bounded to 100 monitors in one durable submission with a five-second
completion budget. Busy receipt capacity retries without treating pressure as a
disk failure. Resume uses a fresh deterministic phase; it does not replay pause
intervals. Configuration, incident and control receipts become applied only after
the owner has installed the exact corresponding projection. `observedGeneration`
now follows the exact owner marker described above; a configure commit alone
does not claim successful installation.

The [CLI resource guide](../cpractl-management.md) and
[CLI control guide](../cpractl-controls.md) document the new public-SDK-backed
commands. Existing numeric v1 reads remain available. CRUD and controls use explicit
versions, bounded files/stdin, no mutation prefetch/retry and write-only Credential
rendering. Independent reviews of the CLI CRUD and control changes found no
remaining actionable issue in those slices. Collection application, selector
control batches and complete collection application remain open. The guarded
recovery/review and bounded action/event CLI are qualified below and documented in
the [CLI action guide](../cpractl-actions.md).

New verification evidence for this slice:

- Full durable Go 1.27 race suite: passed (51.982 seconds); full Go 1.25 suite:
  passed (38.495 seconds); durable vet passed. Later history/executor changes need
  their own relevant qualification.
- Real HTTP controller scenarios: queued-check snooze fencing, started observation
  retention, resume, Raft restart with disabled-plus-snoozed state, audited expiry,
  owner receipts and acknowledgement with continuing checks passed. Simultaneous
  expiries share a single committed batch (0.124 seconds for that focused test).
- Full controller/systems/jobs Go 1.27 race run passed (54.278 / 1.857 / 1.610
  seconds); Go 1.25 run passed (21.336 / 0.120 / 0.287 seconds). A legacy test was
  corrected to copy entity identity instead of retaining an Ark pointer across
  initialization's structural component addition; its outcome assertions remain.
- Real-TLS SDK control tests and focused race checks passed. Full Go 1.25 server
  regression passed (20.930 seconds); control facade/SDK and vet passed.
- Full CLI Go 1.27 race and Go 1.25 suites passed (2.832 / 1.039 seconds) after
  action controls and v2 audit reads were added; tagged `externaljobs` management
  CLI tests passed (8.085 seconds), and CLI vet passed. Tutorial resources validate against
  the real encrypted catalog, without executing their example destinations.
- Dashboard: 130 tests across 11 files, TypeScript, lint and the pinned clean
  production/embedded-assets build passed. Thirteen new tests cover frozen
  incident/control/config versions, reader access, conflicts, uncertain responses
  with retained operation links, note byte limits, sign-out and control behavior.
  The timeline renders operator notes as text. Test workers are bounded to two:
  an unrestricted concurrent run had two timing failures under host contention;
  assertions and deadlines were retained and the full bounded run passed.

Normal-entrypoint browser verification passed in 43.907 seconds on Linux/amd64,
Go 1.27.1, Chrome 153.0.8010.36, Node 24.21.0 and Playwright 1.56.1. It served the
actual embedded SPA through normal `runCPRa` startup, TLS, encrypted Raft and the
owner loop. Seven explicit browser writes covered acknowledge/dismiss/reopen,
snooze, disable, unsnooze and enable. Independent target-side accounting recorded
102 actual HTTP checks, confirmed no checks after applied snooze, and confirmed
fresh checks after enabling. Named-actor timeline notes, actual applied receipts,
navigation cleanup, reader denial and tab-only tokens also passed. This is normal
application-entrypoint evidence in the Go test process; native service/package
lifecycle remains a separate gate. Embedded index SHA-256 for this run:
`2977724117b4102939031da7bde9fa4b9ec6bd99d46a1c2dd89079da319e024b`.

The opt-in test is `TestMainManagementControlsBrowser` in `main_browser_test.go`;
it uses already installed, explicit browser paths and does not download tooling.
Normal-entrypoint CLI subprocess verification now passes on Go 1.27.1 and 1.25.0 as recorded below. These
checks do not complete the full dashboard or shipping plan, certify providers,
or establish million-monitor performance.

## Guarded recovery, audited review and retained v2 history

Manual recovery now commits a guarded action intent before controller dispatch.
Admission and start validate the current dependency closure, observed unhealthy
state, enable/snooze/maintenance settings, unresolved work, attempt limits and
cooldowns. Default manual limits are one request per 60 seconds and three per hour.
A rejected operator assertion never manufactures a provider failure or enables an
automatic retry; a later explicit request still observes the recovery cooldown.

A local execution reserves a process-local claim before submitting its started
marker. Its invocation-local finalizer commits only that the executor returned,
including panic or rejected/uncertain-start paths. Provider success/failure is a
separate fact. An unconfirmed finalizer stops readiness/admission and retains the
claim and data-directory lock. A genuinely failed store requires process fencing
and restart; it is never repaired by clearing an error flag. Restart's committed
local session fences only earlier classified local executors. Legacy or external
executors are not declared stopped from lease expiry alone.

Action review requires the exact action observation version and retains the
original monitor incarnation. Accepted/rejected/inconclusive are operator
assertions, with actor/time/reason and up to eight unique evidence references.
Conclusive review requires proven executor fencing and cannot contradict known
provider evidence. Provider state can remain unknown after a conclusive review;
its hold is separate. Later contradictory evidence is visible and restores it.
Review never replays the action. A review of a deleted/recreated monitor can complete
against the original action without affecting the replacement monitor.

V2 history uses stable monitor identity and bounded frozen pages. It exposes only
allowlisted event and audit fields. A closed store, missing segment/bucket or unreadable
history reports unavailable. Page continuation preserves committed ordering, byte
bounds and retention semantics. Action lists are indexed by monitor and use the
same principal-bound cursor discipline. Paginated dashboard views stop background
snapshot creation until explicit refresh; detail reads remain current.

Qualified component evidence (later edits still require relevant checks):

- Full durable/runtime configuration Go 1.27 race passed in 31.263 / 1.387 seconds;
  full Go 1.25 passed in 32.087 / 0.062 seconds; vet passed. These include real
  subprocess executor fencing, forced termination, cancellation/uncertain marker,
  failed-history writes, cooldown/review and restart regressions.
- The uncooperative controller executor test passed Go 1.27 race in 4.997 seconds
  and Go 1.25 in 2.301 seconds. Its
  test-owned Go handler uses real local HTTP with an independent context. A timed-out
  stop leaves controller completion pending and another process cannot open the
  data directory. After actual handler return, known results and completion markers
  commit and a separate-process reopen succeeds. This is executor lifecycle evidence,
  not certification of a substituted provider handler.
- The normal controller/Raft/compiled webhook test passed race in 8.192 seconds.
  A failed check below the automatic threshold admits one explicit recovery; a lost
  network response becomes unknown. Review and restart retain the original fact;
  a review after delete/recreate completes its owner receipt without replay or
  work against the replacement target.
- New real TLS history/recovery/review route tests passed race in 9.866 seconds
  and targeted Go 1.25 checks passed. SDK race and tagged contracts passed. Two-root
  schema generation evidence is `bin/verification/history-actions-sdk-generation.json`.
  The broader management/server race rerun passed in 232.903 / 149.949 seconds.
  Earlier runs exposed a status-comparison fixture issue (fixed) and intermittent
  large-create conflicts that still require classification; the passing rerun does
  not erase those failures.
- Dashboard production build, generated-contract checks, TypeScript, lint and all
  152 tests in 13 files passed (46.42 seconds on the final known-false wire and
  bounded-dialog candidate; an earlier build passed in 82.37 seconds under load).
  Action tests cover fresh/frozen observation versions, executor restrictions,
  inert audit text, reader permissions, foreign IDs, pagination, lost responses and
  identity/draft cleanup. Actual normal-startup action browser qualification passed separately as recorded
  below; unit tests do not stand in for that integration evidence.
- Normal-entrypoint CLI subprocess evidence passed Go 1.27.1 race (78.039 seconds,
  96 checks) and Go 1.25.0 (68.111 seconds, 95 checks). The actual CLI uses TLS and
  the normal application entrypoint, encrypted Raft, local HTTP checks and exactly
  one unknown recovery effect per run. It covers CRUD, incident controls,
  disable/snooze, a committed mutation with its reply deliberately lost, action
  reads, inconclusive review and v2 audit events. A stale review and reader review
  are rejected; the separate assertion, actor, provider facts and unresolved hold
  survive restart. The acknowledgment appears in retained history exactly once.
  Its post-restart observation spans four committed checks and more than the
  configured cooldown despite three available attempts. Evidence files are
  `bin/verification/main-cli/go1.27.1/result.json` and the Go 1.25 counterpart.
  The CPRa entrypoint runs inside the Go test process; native service/package
  execution remains unqualified by this test. These runs use local HTTP targets,
  not provider accounts, and exercise graceful restart rather than power loss.
  Fresh CLI SHA-256 values: Go 1.27.1
  `754bb196f79505434b2bc7e0c027a76a229e9b26f8a0a13b98b41b0b91e5e675`;
  Go 1.25.0
  `7aed0c83a41bc71d292b91c2b3f6c7584c6997b7c46f158370b9e71f3483d2f5`.
  Independent shutdown/CLI reviews found no remaining actionable issues. An
  unknown review-enum terminal-escaping finding was fixed and covered by a
  regression test. Action/event reads fetch one page with an explicit monitor
  filter where required; no implicit collection or legacy fallback occurs.

Normal-startup action browser verification passed in 29.998 seconds on
Linux/amd64, Go 1.27.1, Chrome 153.0.8010.36, Node 24.21.0 and Playwright 1.56.1.
It exercised one actual compiled-webhook recovery against a local fixture, then
inconclusive and accepted reviews with named operator and retained evidence text.
The original unknown provider outcome survived, the conclusive review cleared its
hold, and neither review nor a normal application restart repeated recovery.
Independent target accounting recorded 111 health checks and exactly one recovery
request. Three explicit browser writes, reader restrictions, draft cleanup and
no token persistence passed. The first attempt failed because the test's exact
input label did not include its help text; the scoped selector was corrected.
Independent review also caught missing JSON `false` values for hold/fencing; the
canonical schema now requires both fields, and real HTTP JSON tests cover them.

Result: `bin/verification/main-browser-actions/result.json`; operator screenshot:
`bin/verification/main-browser-actions/operator-review.png`. Embedded index SHA-256:
`ae9cdb2d4f9c049149d501dfafd116c8655402a580825438e36b6f2f16030bd0`.
The root test is `TestMainManagementRecoveryReviewBrowser` in
`main_actions_browser_test.go`. CPRa runs its normal entrypoint within the Go test
process and Chrome is a separate process. These are local fixtures, not provider
account certification or native service-package evidence.

The normal control browser scenario also passed again in 55.76 seconds with
140 actual checks and seven explicit writes, exercising the new authenticated v2
timeline. That earlier embedded candidate's index was
`ec357a6fc2bd46abecea9e5f0270a239b99e50acd72d83fdc0860b08413b9d3f`.
Final release evidence still needs one frozen source/artifact identity across all
required campaigns; these private working-tree runs are scoped implementation evidence.

## Executed component checks

These are checks of local components during implementation. Later code changes
require relevant checks again; this section does not certify the final candidate.

- Workspace helper: seven tests, two release-isolation tests, actual `make build`
  and `make build-ctl`, and targeted existing CLI tests.
- SDK schema/client additions: generation repeated deterministically; default,
  tagged and race tests with Go 1.27.1; default tests with Go 1.25.0.
- Encryption: tampered bindings/ciphertext/wrapped keys, key rotation/readability,
  bounds, cancellation, ownership and concurrent independent data-key tests;
  Go 1.27.1 race/vet and Go 1.25.0 tests passed. Independent primitive review found
  no blocking primitive defect; broader key lifecycle requirements remain open.
- Protected key files: Linux race/minimum-version checks and native Windows
  file/ACL creation, loading, replacement, corruption and concurrent-publisher
  checks passed. Windows symlink coverage was skipped for missing privileges;
  service-account execution and macOS/BSD ACL qualification remain open.
- Transit/KMS wrappers: real local TLS exchanges, actual AWS SDK serialization
  and signing, AAD rejection, response bounds, cancellation, no retries/redirects,
  redaction and borrowed-transport ownership passed Go 1.27.1 race and Go 1.25.0.
  No installed Transit backend or cloud account was verified by these tests.
- Routing: legacy/typed selection, overlapping contacts/groups, missing matches,
  changed endpoint types, incarnation identity and input isolation; Go 1.27.1
  regular/race and Go 1.25.0 tests passed.
- Catalog service: write-only read/mutation results, encrypted preparation,
  concurrent-edit conflicts, delete/recreate identities, changed shared reference
  validation, immutable pages, forbidden patches and reference-index integrity.
  Go 1.27.1 regular/race and Go 1.25.0 tests have passed during implementation.
- Durable receipt checks: committed/applied separation, supersession, deletion lookup,
  capacity with replacement, caller ownership, snapshot consistency, segment loss,
  indexed-history corruption/retention, and process kill after snapshot plus a later
  committed update. Go 1.27.1 race and Go 1.25.0 durable/server tests passed.
- Runtime mapping and bootstrap conversion: all built-in driver shapes, safe
  resolved validation, no provider/file side effects, stable secret identities,
  input isolation and preservation of an actual legacy resolved fingerprint.
  Go 1.27.1 race and Go 1.25.0 management tests passed. SDK streaming parser
  parity, late failures, quotas, cancellation and absence of plaintext spools
  passed collection race tests.
- Encrypted staging: all 33 built-in driver validation fixtures, protected
  inventory tampering, wrong keys/identities/limits, missing references, late
  malformed input, quotas, concurrent opens, pagination, and forced-process
  restart passed normal/race and minimum-compiler component checks.
- Bootstrap and execution guards: incomplete catalog admission/readiness,
  original ciphertext/count/digest activation, snapshot-prefix process-kill
  recovery, stale dependencies, identical-configuration delete/recreate, and
  adoption of legacy incident/action identity passed. The combined durable,
  entities, management and server race run passed. Later normal-startup evidence is listed below.
- Dynamic observation index: inserts, rename order, removal/reuse, ownership,
  filtering concurrent with edits, and concurrent readers/writers passed race tests.
- Scheduler: indexed replacement/cancellation, recycled entity identities,
  reference-model comparison and zero steady-state schedule/cancel allocation
  test passed. Ready-queue/controller regression race tests passed.
- Browser: 116 tests across 10 files, six generator tests, TypeScript, lint and
  private production build passed. The opt-in actual Chrome test passed all ten
  management checkpoints on real TLS handlers and Raft (6.64 seconds; Chrome
  153.0.8010.36, Node 24.21.0, Playwright 1.56.1, Go 1.27.1 Linux/amd64).
  It found and fixed missing create preconditions, post-save dirty navigation,
  and forbidden immutable fields in credential patches. The fixture deliberately
  excludes normal main/bootstrap, controller execution and provider operations.

## Integrated application evidence

Normal `runCPRa` startup now opens the selected encryption backend, authenticates
named operator policy and TLS sources, activates or restores the authoritative
catalog, loads the owner projection and serves the management API. A later
startup does not reread the initial manifest over committed API edits. A retained
catalog, including one with all resources deleted, cannot silently return to
manifest-only operation. See [management startup](../management-startup.md).

- Real normal-main TLS/Raft tests passed, including SDK writes, applied receipts,
  protected persistence, shutdown and restart without the original manifest.
  Main race and Go 1.25 checks passed during this integration.
- Full controller/system race tests passed after initial batching and late-result
  integration (22.314s / 1.684s on Go 1.27.1). The controller tests exercise actual
  local HTTP targets, runtime create/edit/disable/delete/recreate, Raft restart,
  and a superseded job already waiting for the sole worker slot. Go 1.25
  controller/system/durable checks also passed (17.031s / 0.110s / 28.896s).
- A shared recovery credential is rotated while its original HTTP operation is
  in flight. Its eventual success appears as retained late evidence; the original
  action remains unknown, and the new destination receives no repeated action.
- Startup batching configures eight monitors in three commits when the configured
  batch limit is three, with zero provider executions before the controller starts.
  This validates batching behavior, not million-monitor startup performance.
- The durable shutdown barrier is tested after an admitted write loses its reply.
  It returns only after that earlier write has applied. HTTP admission stops first;
  reads remain available until the owner and dependencies close.
- Independent review found the active-only deletion lookup defect. The reconciler
  now uses an internal retained-record lookup for exact deletion markers; public
  resource reads still omit deleted resources.

These checks are local integration evidence. The initial CRUD Chrome run used a
handler fixture. The later controls run exercises the normal entrypoint and embedded
assets; neither result alone qualifies the whole release.

## Next integration work

### Additional observation and preflight verification on 2026-09-19

The dashboard now links saved configurations and incident rows to a stable-ID
operational monitor page, independently of the legacy numeric observation index.
It shows the owner-observed version, incident controls, actions and retained
history. Incarnation changes discard old control drafts. Unsupported driver
configurations remain observable without becoming editable.

Authenticated Alerts uses bounded v2 incident pages, with an explicit next-page
action, exact optional monitor filter and a frozen server snapshot. The counts
describe the current page, not fleet totals. These are the latest incident records
per monitor, including closed records; earlier incidents remain in history.
Expired snapshots and failed refreshes are explicit. Denied v2 access does not
fall back to legacy reads.

Settings now provides an authenticated, on-demand Prometheus text view. Its
decoded body is bounded to 1 MiB and oversized responses fail explicitly. It has
no hidden polling, download token in a URL, or browser persistence. Hide, logout
and component teardown clear the visible response. A regression test covers an
old request returning 401 while a new identity signs in: that old request cannot
log out the newer identity.

System Health separately reports admission readiness, controller progress,
dashboard projection freshness and storage availability. An explicit not-ready
response is distinct from a failed readiness request. The legacy latency views
now require the availability flag and a finite, nonnegative measurement;
measured zero displays as zero while an absent measurement is unavailable.
Virtualized table headers, cells and row positions have explicit semantics.

The frozen dashboard build, generation check, TypeScript, lint and all 290 tests
across 22 files passed; the tests took 10.28 seconds. It rebuilt embedded assets
and dependency notices using Node 24.21.0 and pnpm 11.22.0. Evidence:
`bin/verification/dashboard-observation-build/result.json`. Embedded index SHA-256:
`302ba23984fd2037f24ac414c940f8e8951bc9982ce9dae5359a3a4270ed0d69`.
Independent review covered the stable-ID route, incident pagination, metric
transport and refresh/identity boundaries.

The final normal-main Chrome run executed that embedded candidate in 30.994
seconds. It recorded 120 actual local checks, exactly one recovery, three explicit
writes and four on-demand authenticated metrics requests. The accepted review
survived restart without another recovery. Saved configuration and incident rows
opened the stable-ID monitor page, and the real state/readiness endpoints remained
ready despite the failing monitored target. The 390-by-844 mobile scenario reached
Alerts, the stable monitor and the View/Hide metrics controls using only Tab and
Enter, with visible focus and no body horizontal overflow. This is scoped keyboard
and mobile evidence, not complete accessibility certification.

Evidence is `bin/verification/main-browser-actions/result.json`, with desktop and
mobile screenshots alongside it. `capture-attempts.json` preserves one initial
Chromium response-body capture failure and an interrupted experiment waiting for
an aborted response to finish. The final harness correlates actual HTTP runtime
responses with the UI's decoded current observations; it retains the operation
and incident body checks and all write/provider-count assertions. No production
source changed between the recorded dashboard build and the final browser run.

`cpractl diff` now freezes files, explicit directories, stdin and explicitly
requested URLs before invoking the pure collection preflight endpoint. It
returns 0 for unchanged input, 1 for differences, and 2 for input, permission or
transport failure. A separate source client never receives API credentials.
Output includes local source attribution and safe item outcomes rather than
protected input values. The command does not allocate an apply operation, upload
persistent staging or activate resources. Unix SIGINT/SIGTERM cancellation of
blocked native stdin exits and removes the private temporary input spool.

Actual native CLI subprocesses exercised the normal application with TLS and
Raft, including unchanged input, a proposed monitor/credential update, a malformed
final file, a missing reference and a denied reader. All five cases produced the
expected exit code. The original catalog and sole earlier receipt were unchanged
before and after a normal owner restart; independent target accounting recorded
zero provider calls. Go 1.27.1 root race verification passed in 3.768 seconds and
Go 1.25.0 verification in 4.407 seconds. The subprocess CLI executables were
ordinary optimized builds, not race-instrumented builds. Evidence:
`bin/verification/collection-diff-native-cli/result.json` and
`bin/verification/collection-diff-native-cli-go125/result.json`.

CLI component/race, signal-process, minimum-compiler and vet checks also passed,
with independent review. These checks establish preflight behavior and graceful
restart; they do not establish collection activation or process-crash recovery.

### Queue age and encrypted input retention on 2026-09-19

Queue statistics now measure the oldest pending queue entry in Hybrid, Adaptive,
and Workiva queues. Entries carry a monotonic admission time and independent
lifetime bookkeeping, so reused jobs cannot reset the age of an earlier queued
copy. Provisional admission, empty queues, and closed queues expose explicit
availability. The implementation uses a short metrics lock and constant-time
updates; it does not scan the queue for each dashboard read. V1/v2 responses and
the queue diagnostics screen preserve measured zero separately from unavailable.

Full queue race tests passed (5.215 s); controller, entity and system regression
race suites passed (42.081 s, 1.085 s, and 1.129 s). Actual API mapping tests and
Go 1.25 checks passed. Three one-second Hybrid queue fixture runs measured serial enqueue/
dequeue at 244–255 ns compared with 154–159 ns before age tracking, and parallel
operations at 340–415 ns compared with 211–222 ns; both reported zero per-operation
allocations. A standing 4,096-entry backlog remained measurable at GOMAXPROCS 2
and 8. These results expose the added synchronization cost and are not a fleet
capacity claim.

The [encrypted collection foundation](../collection-persistence.md) now commits
inactive upload headers and original encrypted resource rows. Format 3 snapshots
freeze and stream their ledger; restart reconstructs a fresh materialization from
snapshot plus committed Raft logs. An old derived copy cannot repair a missing
authoritative log prefix. Normal catalog commands retain the upgraded format.
Actual forced-process termination after a snapshot and later upload preserves
the original ciphertext without activating resources. Stopped backup validation,
explicit restore invalidation, failed-ledger admission shutdown, and missing-log
rejection have dedicated tests.

Expired, unactivated input is now logically reclaimed in bounded committed steps.
Accepted retry activity fences stale cleanup, the first expiration records one
audit event, and partial tail removal survives snapshot plus later log replay.
Final removal preserves the operation high-water mark, keeping its handle
expired. Focused cleanup race tests passed in 10.616 seconds and Go 1.25 tests in
5.980 seconds, including actual background maintenance and stopped validation.
Independent review found a timestamp representation issue, corrected by comparing
instants; the final source review has no remaining blocker in this slice.

Ledger import and deletion use transactions of at most 256 rows and 4 MiB.
Count and byte limits are tested independently, along with frozen snapshots,
other-operation isolation, reusable logical quota, and actual filesystem write
rejection. The full ledger-focused race suite passed in 55.091 seconds and
Go 1.25 in 7.457 seconds, with vet and independent review. Evidence is in
`bin/verification/collection-ledger/result.json`. Physical file shrinkage,
old-generation cleanup, cancellation, scalable validation, conditional activation,
and original-operation browser reselection/resume remain separate work. No
public staged-apply route is claimed by these internal storage checks.

The shared collection parser also now preserves JSON numbers and converts YAML
decimal spelling without `float64` rounding. This prevents invalid fractional
retry controls from becoming accepted integers and preserves tagged external
parameters exactly. Both `Decode` and `Freeze` tests pass under default and
`externaljobs` builds: Go 1.27 race 6.788/6.775 seconds, Go 1.25 0.874/0.926 seconds,
and tagged vet. The browser parser was subsequently regenerated against this
correction, with final qualification recorded next.

### Final browser parser and embedded build on 2026-09-19

The dedicated Worker now uses the public Go SDK parser compiled to WebAssembly,
without introducing a separate JavaScript resource normalizer. It validates the
entire selection, freezes private exact resource bytes, and exposes only bounded
identity previews and explicitly requested request buffers. Source names stay
local. No plaintext browser persistence or source URL import is introduced.
The foundation supports the approved 1,000 files, 64 MiB source bytes, 10,000
resources, and 1 MiB per resource, with a separate 512 MiB normalized-buffer
safety quota. It remains unconnected to the import UI and collection activation.

Chrome 153 tested the final parser
`48712e2f8d310d4de2ce747e6dad57cf252138ddf7040e8fc96ea3d33a9a9d15`:
an exactly 64 MiB ordinary fixture froze and produced bounded chunks in 7.444
seconds; an escape-heavy 64 MiB fixture produced 402,368,530 normalized bytes in
64.157 seconds. Whole-Chrome-process-tree RSS peaked at approximately 1.67 GB and
2.33 GB respectively. These figures include shared pages and other browser
processes; they are not private Worker allocation measurements. Main-thread
responsiveness, native parser parity, fractional/underflow rejection, malformed
final input, duplicate keys/IDs, aliases, an actual parser-asset 503, and abort
after parsing starts passed. No API calls or browser storage writes occurred.
Evidence: `bin/verification/browser-import-worker/result.json`.

Two distinct source directories without Git produced identical parser, matching
runtime, build metadata, dependency inventory and notices. Generation checks the
pinned Go/Node versions, isolates ambient build settings, and records linked
module identities from the actual Wasm executable. This is parser reproducibility,
not the whole release source-archive gate. See
`bin/verification/browser-import-worker/reproducibility.json` and
[the Worker contract](../../dashboard/src/import/README.md).

The combined sanitized dashboard build passed TypeScript, lint, generated-schema
checks and all **306 tests in 23 files** (16.79 seconds for the test phase).
The new embedded index SHA-256 is
`dc76f3893426c55bcd4a5d59aa7f8e8b97dab61de601eb64dcd32852f73e2d2d`.
The parser remains absent from the production bundle until the UI imports it;
distribution notices report only actual shipped assets. Its generated source
inputs carry their own complete notices. Evidence:
`bin/verification/dashboard-worker-queue-build/result.json`.

After bounded expiration was integrated, full durable and local-administration
race suites passed in 164.010 and 11.058 seconds. The source hashes, exact scoped
checks, independent review and remaining boundaries are recorded in
`bin/verification/collection-persistence/result.json`. This does not close the
full durability release matrix on other platforms.

The rebuilt embedded dashboard also passed the normal-server browser, HTTP
preflight and real-CLI tests together (Go 1.27.1, 33.472 seconds). Browser evidence
recorded 123 actual local checks, one explicit local recovery request, three
operator writes and four authenticated metrics reads. A normal restart preserved
the reviewed unknown action without replay. The same run retained the CLI
preflight's zero-change and zero-provider-operation assertions. See
`bin/verification/main-dashboard-collection-regression/result.json`; this run
precedes the subsequent scoped collection lookup changes and does not qualify
those later changes implicitly.

### Scoped collection validation on 2026-09-19

Complete encrypted uploads now have a protected validation view with short,
bounded reads and explicit expiry/restore checks. It copies ciphertext before
decoding and holds no transaction across resource callbacks or key-service calls.
Read-view race tests passed in 13.423 seconds and Go 1.25 tests in 7.350 seconds,
with vet and independent review. Evidence:
`bin/verification/collection-read-view/result.json`.

The management source verifies original resource bytes and position MACs, then
the final inventory commitment. It authorizes identity reads before existence
lookup or decryption, keeps keys private, and releases borrowed bytes and scratch
on success, error, cancellation and expiry. Source tests validate 72 resources
totaling more than 64 MiB while retaining less than 4 MiB of raw/decode
reservations simultaneously. The fixed allowance for source plus conservative
downstream validation copies is 64 MiB, independent of total collection bytes;
it is not a process RSS limit. Source tests pass with the race detector
(50.964 seconds) and Go 1.25 (4.314 seconds). Evidence:
`bin/verification/collection-source/result.json`.

A subsequent source-attribution check ensures that a malformed final resource
points to its own opaque input position, while a final inventory mismatch does
not blame the preceding valid resource. That updated source suite passed in
3.061 seconds. Diagnostic HeapAlloc samples during the normal-build 72-resource
walk measured about 167 MB before the walk and a highest callback sample of
331 MB, including the complete encrypted in-memory fixture and GC behavior.
These samples distinguish the logical reservation from actual heap consumption;
they are neither continuous peak-memory measurements nor release sizing data.

Driver credential resolution and notification routing now support borrowed
resource lookups. Existing map callers preserve their behavior. Pure validation
does not retain destination lists; driver output reservations remain live until
their final consumer releases them. Atomic per-resource activation, public cancellation, original-operation resume and
the browser import UI remain unfinished; the bounded internal whole-graph
validator is now implemented and its qualification is recorded below. The [staged validation design](collection-staged-validation.md)
separates the implemented components from those remaining contracts.

The [browser reselection proposal](collection-browser-reselection.md) records a
way to verify reselected source files against the original server-held inventory
key after refresh. It requires exact source and normalized-resource commitments,
and identifies parser-version compatibility and raw-source disclosure explicitly.
It is design only; no refresh-resume route or browser workflow is claimed yet.

### Bounded staged graph validation on 2026-09-19

The internal validator authenticates the complete original input, validates the
final resource graph and each dependency-ordered intermediate state, and checks
original resource/reverse-edge guards before reporting success. Its retained maps
contain identities, direct references and version guards. Credential values,
resource specs and resolved driver configuration remain within scoped reads.
Omitted credentials use the captured original value transiently; validation does
not replace the original staged bytes, seal new resources or invoke providers.

Focused tests passed on Go 1.25 (71.469 seconds), Go 1.27.1 with `externaljobs`
(31.344 seconds), and vet. Independent review found no unresolved blocker after
correcting late-item attribution and expiry checks around live-resource reads.
The full default management race suite passed in 854.713 seconds. This includes
the final large-input tests and an actual Raft snapshot, later committed log entry,
and normal restart with unchanged source ciphertext; it is not a process-kill
test. The focused source restart test also passed on Go 1.25 in 0.810 seconds.
This run precedes the subsequent upload-preparation and cancellation work.
Evidence: `bin/verification/staged-collection-validation/result.json`.

A 72-resource inventory exceeding 70,041,600 plaintext bytes used a peak logical
borrow/scratch reservation of 19,462,985 bytes. An actual encrypted notification
graph containing 40 endpoints, one group and one monitor exceeded 36,864,000
plaintext bytes and peaked at 31,342,344 reserved bytes. Near-1 MiB credential and
endpoint updates remained below 54.5 MB; an omitted large credential remained
unchanged. These figures are accounting measurements, not heap or RSS.

The initial internal limits are **10,000 retained old-plus-desired versions**,
**32 MiB of estimated graph metadata**, **100,000 traversal visits**, and the
shared **64 MiB scratch allowance**. These limits do not qualify every 10,000-item
browser update or arbitrary SDK-scale cardinality. Public admission, larger graph
indexes, durable validation progress and activation still need implementation and
qualification. A successful validation is an observation, not a lock against
subsequent edits.

### Encrypted upload preparation on 2026-09-19

The new partial-upload view captures only the committed prefix. An empty upload
works without creating placeholder database buckets; appending a row invalidates
the old view. An identical retry can extend upload activity while preserving its
original content. Complete validation still rejects incomplete uploads. Review
also found and fixed a corruption path where an oversized first row could yield
an empty page and panic in the single-item reader. Empty/oversized stored rows now
return unavailable before copying or indexing them.

The complete/partial read-view race suite passed in 15.495 seconds, Go 1.25 in
8.395 seconds, and vet passed. Independent review found no unresolved blocker.
Evidence: `bin/verification/collection-upload-view/result.json`.

The internal management upload helper checks permissions and original item MACs,
then schema and identity, before preparing encrypted input. Confirmed retries
return the original stored ciphertext even after wrapping-key rotation. A
concurrent append, cancellation or expiry during encryption prevents returning a
stale row. Preparation itself submits nothing, renews no deadline and makes no
active resource changes. Focused tests pass with the race detector, Go 1.25,
`externaljobs`, and vet; final supplemental tests and independent review are
recorded in `bin/verification/collection-upload-preparation/result.json`.
The existing normal-application TLS/Raft preflight regression also passed with
the race detector in 2.676 seconds, retaining its zero-activation assertions.
That check exercises existing preflight, not the unconnected staged apply flow.

Public create/upload admission, lost-create-response reconciliation, whole
operation progress/receipts and activation remain unfinished. An individually
authenticated input item is not permission to skip whole-collection validation.

### Inactive upload cancellation on 2026-09-19

The internal durable command cancels only the original uploader's unexpired
inactive input, fenced by operation epoch, upload identity, cancellation UUID and
observation time. It immediately blocks uploads and captured or newly requested
validation reads. It preserves ciphertext and original inventory/activity
metadata; existing maintenance removes at most 256 rows and 4 MiB per committed
cleanup step. Exact retained retries emit no second event or cleanup operation.
Final cleanup frees the header slot, and an issued retired handle stays expired.

One safe `collection_canceled` history event survives header retirement under
normal 30-day retention. **This audit is not the planned 30-day queryable terminal
collection operation receipt or result index.** Those public operation contracts
remain unfinished. This change adds no HTTP/SDK cancellation registration,
activation, provider operation or browser workflow. Explicit backup restore
preserves existing terminal outcomes while its new epoch invalidates old handles.

The final cancellation tests passed on Go 1.27.1 in 18.978 seconds. Cancellation,
cleanup, read-view, command and snapshot regression selections passed with the
race detector in 56.406 seconds and Go 1.25 in 40.331 seconds; durable vet passed.
Tests include actual Raft log-only and snapshot-before/after-cancel restart,
stopped-directory validation, explicit restore, same-log upload/cancel ordering,
wrong identities, failed-storage/bootstrap/authentication admission, copied
metadata, bounded deletion, quota reuse and 29/31-day audit retention. Independent
review found no unresolved blocking finding. Evidence:
`bin/verification/collection-cancellation/result.json`; scope and remaining receipt
work: [internal cancellation boundary](collection-cancellation.md).

### Generation retirement primitive on 2026-09-19

A private Linux primitive can retire one eligible derived generation under a
mandatory ownership guard, using a durable external intent and one unlink per
step. It remains disconnected from Store startup and maintenance. Its focused
race and Go 1.25 checks passed, including actual temporary bind mounts, directory
sync failure, and SIGKILL after four completed transitions. Windows and macOS
were cross-compiled only. See
`bin/verification/collection-generation-retirement/result.json` and the updated
[implementation boundary](collection-generation-cleanup.md).

Automatic scanning, real reader-pin registration, lifecycle wiring and physical
accounting remain pending. These tests do not establish power-loss guarantees or
a bound on physical disk consumption.

### Public inactive collection admission and private import UI on 2026-09-19

The creation contract now includes an encrypted original admission ticket. Its
consumption identity remains in bounded authoritative Raft state independently of
upload cleanup, and explicit restore fences it with a new operation epoch. New
Prepare/Create/Upload routes stage only encrypted inactive input. The registered
Cancel route retires the original inactive upload and reconciles the same
receipt after cleanup without repeating a command or event. Operation GET
can return upload progress and retained terminal receipts. See
[public admission boundary](collection-admission.md) and
[terminal receipts](collection-terminal-receipts.md).

The dashboard now has a source-attributed private file Import page and exact-byte
transport. Its 104 scoped tests passed along with TypeScript, lint and generated
contract checks. The clean embedded build also passed all 328 tests across 25
files, type checking, lint and generation checks. The actual server still lacks
staged Validate/Activate, so durable Apply stays disabled. This UI slice does not
establish real-server activation. Evidence is in
`bin/verification/dashboard-collection-import/result.json`.

The SDK retains the original creation ticket in Frozen client state, rejects
concurrent Apply/Resume, and never silently replaces a ticket after an uncertain
Create. Its default/tagged race, Go 1.25 and vet suites passed for this slice.
Root `go test ./...` alone does not cover these nested-module checks.

Integrated admission/cancellation tests passed with Go 1.27.1 race (management
1.298 s, server 6.598 s) and Go 1.25.0 (0.210 s and 5.443 s). The normal-main
SDK/TLS/Raft scenario passed in 2.554 s with race and 1.250 s on Go 1.25: two
encrypted inactive rows and their original ticket/handle survive graceful owner
restart, with zero active resources and zero provider requests. The full server
regression suite passed in 12.794 s. These tests do not qualify activation or a
process-kill boundary. Evidence is in
`bin/verification/collection-public-admission/result.json`.

Independent review strengthened that restart test to require the actual typed
HTTP 404 for the unavailable Validate operation on both Apply attempts. The
supplemental main tests passed in 2.435 s with race and 1.222 s on Go 1.25;
`collection-public-admission/exact-response.json` records those commands.
The final test additionally cancels the inactive upload and verifies the same
GET/cancellation receipt after a second graceful owner restart; it passed race
in 3.062 s and Go 1.25 in 1.747 s. An all-built-in-driver race selection also
passed (management 1.316 s, server 6.698 s, main 3.296 s) before that test-only
extension. Both boundaries are recorded in `collection-public-admission/supplemental.json`.

The production embedded Import page now also passes a real Chrome file-picker
scenario against normal TLS/Raft startup: YAML/JSON cross-file references,
source attribution, a server preflight, a malformed final file, discard,
sign-out and reader denial. It opens and closes three actual Worker instances,
performs one preflight POST, and leaves active resources empty with zero
provider calls across graceful restart. The completed API response bodies
observed in the fixture contain none of its private canaries. Final browser
checks passed in 5.765 s with Go 1.27.1 race and 4.497 s on Go 1.25. Three earlier
failed/interrupted response-accounting attempts are preserved and excluded from
passing evidence. Bounded Chrome protocol observation resolved the test-tool
retrieval issue without intercepting requests. This is a finite preview test,
not a successful durable Apply or large-import qualification. Exact scope,
versions and hashes: `bin/verification/collection-browser/result.json`.

Terminal receipts now survive canceled/expired input cleanup for normal 30-day
history retention, with a dedicated indexed observation and strict reciprocal
history validation. Earlier private events lacking counts remain timeline-only;
they are not fabricated into complete receipts. Receipt-specific qualification
and independent review are recorded in
`bin/verification/collection-terminal-receipts/result.json`.

### Private plan compilation and catalog mutation tokens on 2026-09-19

The private staged validator can now emit a metadata-only per-item plan after
the entire inventory, final union, ordered prefixes and final captured versions
pass. It retains original target/dependency guards, source coordinates and
earlier included dependencies. A transitive included resource classified as
unchanged still requires its own successful conditional outcome; a consumer
cannot fall back to an old live version after that row fails. The compiler
prepares no catalog mutation, seals no provider data and supplies no execution
or authorization grant.

Focused compiler and adversarial race checks passed on Go 1.27.1 in 1.904 s
(default) and 1.883 s (`externaljobs`). The compiler plus staged-validator
selection passed on Go 1.25 in 70.864 s. An earlier broad race selection reached
its ten-minute deadline in the existing large notification-closure fixture;
that failed run is preserved. The unchanged fixture passed an isolated race
rerun in 517.638 s with an explicit twenty-minute deadline, recorded separately in
`bin/verification/collection-plan-compiler/result.json`. These compiler checks
do not establish million-resource validation or end-to-end activation.

[Catalog format 4](catalog-mutation-format.md) now gives each accepted catalog
mutation a unique reverse-dependent token, including two mutations in the same
Raft entry. Historical format-2/3 replay keeps its old index-based interpretation.
This removes a prerequisite ambiguity in identifying an earlier collection
item's own writes. The new format still contains only the existing encrypted
input stream; it does not persist executable plans or item outcomes.

Normal TLS application writes, authoritative catalog restart, catalog guards,
and inactive collection admission/cancellation passed together on Go 1.27.1
with race (server 6.589 s, management 1.719 s, main 14.901 s) and Go 1.25
(5.324 s, 0.592 s and 13.419 s). Affected-package vet passed. Exact selection
and source hashes: `bin/verification/catalog-format4/integration.json`. The
normal-main restart is graceful; process-kill and historical-binary execution
are not inferred from it.

The complete durable package race suite passed in 225.780 s after correcting
older tests that restored bare image JSON and failed to release captured
snapshot views. Those fixtures now use the actual framed snapshot path. The
first 600.065 s failed/timeout run remains preserved in
`bin/verification/catalog-mutation-format/result.json`. The suite's source
inventory is recorded; later codec changes are qualified separately below.

The private [plan artifact codec](collection-plan-artifact.md) and compiler
adapter now stream bounded typed fragments with a complete intended descriptor
computed before future staging. Changed plan bytes cannot silently replace that
descriptor. Final combined `^TestCollectionPlan` checks passed: release race
(durable 4.334 s, management 3.910 s), `externaljobs` race (4.321 s, 3.912 s),
Go 1.25 (0.373 s, 0.891 s), and affected-package vet. Artifact source hashes and
precise scope are in `bin/verification/collection-plan-artifact/result.json`.
The synthetic large-row tests exercise serialization limits; they do not prove
large-fleet graph validation. No durable plan/header command or activation route
was added by this codec.

### Inactive durable plan staging on 2026-09-19

The [staging implementation](collection-plan-staging.md) now commits one original
plan descriptor, accepts bounded exact fragments, verifies every row against its
encrypted original input and conditionally finalizes the complete artifact.
Format 5 snapshots both namespaces from one frozen view. Cancellation, expiry
and explicit restore fence the inactive plan; cleanup removes plan tails before
input tails and preserves original audit identity. This internal `validated`
state is structural artifact state. It is not a public validation verdict or
permission to apply resources.

The complete durable package passed with Go 1.27.1 race in **282.189 s**. The
focused plan/compatibility selection passed on Go 1.25 (durable 51.235 s,
management 1.028 s), and the `externaljobs` plan selection passed with race
(53.783 s and 3.457 s). Affected-package vet passed. Existing normal TLS startup,
catalog writes/restart and inactive admission/cancellation regressions also
passed with race (server 6.675 s, management 1.746 s, main 14.909 s) and Go 1.25
(5.486 s, 0.614 s and 13.503 s). Tested source hashes remained unchanged across
these checks. Exact selections, commands and boundaries are in
`bin/verification/collection-plan-integration/result.json`.

Three new real-process kill cases cover format-3 snapshot plus partial plan logs,
format-5 partial snapshot plus later fragments, and committed finalization with
a lost reply. The parent fixture retains the original complete artifact; after
reopening it compares the committed prefix and resumes only those exact bytes.
Replay/finalization leave the active catalog, monitors and ordinary operations
empty. This process-kill evidence does not establish power-loss behavior or a
public coordinator that reconstructs an unavailable original suffix.

Independent review closed three findings: out-of-range proposed rows were
misclassified as storage failure; cleanup compared finalization timestamp
representations instead of instants; and a lock-cancellation test could pass
before reaching the contested lock. The corrected regressions are in the final
passing matrix. The initial test-only compile typo and narrower earlier runs
are retained in the evidence. No publication occurred.

A later test-only supplement connects the actual encrypted source, dependency
compiler, artifact adapter, codec, real Raft submissions, verification and
finalization. Five resources in reverse dependency input order produce 21
source-bound fragments. Two finalization retries preserve the original result;
the active catalog stays empty, the instrumented HTTP target receives no request
and the configured notification log is never created. Default and `externaljobs`
race each passed in 2.493 s; Go 1.25 passed in 1.129 s, and management vet passed.
Independent review and unchanged source hashes are recorded in
`bin/verification/collection-plan-durable/result.json`. This test adds no public
coordinator, authorization grant or activation behavior.

### Immutable validation results and retained history on 2026-09-20

The [format-6 result contract](collection-validation-staging.md) now retains one
original validation descriptor and exact result rows separately from encrypted
input and plan fragments. Current named-principal IDs are permanent within their
authentication epoch. Admission captures the original owner; validation fixes
that owner's authority revision and cannot silently acquire a newer one on retry.
An ownerless historical upload does not become eligible for background execution.

Finalization binds the complete original verdict without applying any resource.
A deterministic rejection preserves every source-attributed row within the
10,000-input graph limit; larger inventories receive an explicit bounded summary
rejection, not a truncated result. Snapshots freeze all three namespaces and reject
incomplete, incompatible or inconsistent data. Raw resource bodies, credentials,
source paths and provider diagnostics are excluded from result metadata.

Bounded maintenance publishes finalized rows and a final summary to retained
history before temporary staging is removed. Reads use original ordinal order,
the committed watermark and a fixed 30-day finalization cohort; the upload's
24-hour inactivity deadline cannot shorten result retention. Cancellation and
explicit restore preserve finalized evidence. Structural plans and unsealed
results do not project public validation success.

Five real-process crash scenarios passed on Go 1.27.1 race (21.978 s), Go 1.25
(13.140 s) and `externaljobs` race (19.674 s). They cover partial/finalized result
staging, partial-history snapshots, the final history row/seal, canceled cleanup
and a second restart. The tests retain original descriptors externally when
resuming an uncommitted suffix; they do not prove an automatic public coordinator.
The fixture's future observation time isolates explicit crash boundaries from
the real maintenance timer and does not replace real Raft writes or process kills.
Source hashes, original failed fixture attempts and review scope are recorded in
`bin/verification/collection-validation-*` and
`bin/verification/authentication-lifecycle`.

Independent review corrected two publication defects: restored canceled/expired
old-epoch evidence must remain eligible for inert publication, and impossible
partial publication offsets must fail snapshot/header validation. The corrected
restore, publication, validation and receipt selection passed in 13.399 s.
The complete durable package then passed Go 1.27.1 race in **359.848 s**. The
affected validation/result/receipt/authority selection passed Go 1.25 in 40.846 s
and `externaljobs` race in 98.347 s; durable vet passed. Those checks recorded
unchanged tested durable source hashes. They precede the separately qualified
backward-clock expiry-read supplement; the report preserves that boundary.
Evidence is in `bin/verification/collection-validation-publication-integration`.

The broader management/auth/localadmin/web-server/application race attempt hit
its ten-minute management-package limit while the existing large notification
closure fixture had been running for 4m14s. The other four packages passed.
The original failure and unchanged source inventory are retained. The complementary management race selection then passed in 341.335 s, and vet
passed for management, authentication, local administration, the web server and
application. The isolated expensive fixture then passed in **510.153 s** under its justified
twenty-minute budget, with unchanged source hashes. The two complementary race
runs cover the management package; the original timed-out invocation remains a
failed attempt. This is correctness/race evidence, not a performance campaign. Make
`test`/`test-all-drivers` now default to a configurable `GO_TEST_TIMEOUT=20m`;
release compatibility uses the same package budget. Dry-run commands preserve
the complete test selection and accept a caller override; this setting change
does not claim that the full Make/tag matrix was rerun.

Management cancellation now accepts inactive validation-in-progress and finalized
success/rejection states. Six actual Store/FSM fixtures verify original metadata,
authorization, no activation, bounded cleanup and day-29 result/receipt access.
Focused default race passed in 1.982 s and Go 1.25 in 0.799 s; independent review
passed. Evidence: `bin/verification/collection-cancel-inactive/result.json`.

The backward-clock supplement makes persisted validation expiry authoritative
in both protected reads and synchronizes the history cutoff before header
cleanup. Real Raft tests cover cleanup/reopen and actual catalog replacement
failure while preserving a later cancellation receipt. Focused release race
passed in 18.838 s and Go 1.25 in 12.429 s; durable vet and independent review
passed. Evidence: `bin/verification/collection-validation-expiry-read/result.json`.

A private canonical validation profile now records the selected embedded schema,
actual built-in driver availability, inert configuration-model mappings, platform,
semantic policy/compiler/codec versions and deterministic validation limits.
It excludes provider data, diagnostics and release/machine identity. The default,
Go 1.25, `externaljobs`, all-built-in-driver and combined tag selections pass,
including the existing full-value mappings for all 33 drivers; management vet and
independent review pass. Evidence: `bin/verification/collection-validation-profile`.
This builder is ready for the coordinator; it is not yet bound by public Validate.

Public Validate/Activate, collection result endpoints, item application, SDK/CLI
apply/resume and browser Apply remain unfinished. No source, SDK, image, chart,
release or documentation publication occurred.

### Private publication lock on 2026-09-20

The release workflow now permits candidate qualification only in an explicitly
private repository with `publish=false`. Every build/qualification job depends
on that initial check; publishing requires `publish=true` and therefore cannot
run. Direct publisher invocation also stops before release-payload reads,
verification or external operations. CI evidence uploads and dedicated live
campaigns require private repository visibility. No runtime switch bypasses the
lock while the aggregate gate is unfinished.

Four workflow-policy tests execute the actual shell guard and inspect the job
graph, all 25 release-tool tests pass, and actionlint passes for all three affected
workflows. Independent review found no remaining bypass in this local boundary.
Evidence and frozen source hashes are in `bin/verification/private-publication-lock`;
the actual local PyYAML version was 6.0.1, distinct from the CI pin of 6.0.3.
This is an interim enforcement lock, not completed ticket-16 qualification or
proof of remote repository/registry protections. Manual candidate source/SDK tag
pushes remain unauthorized. No external workflow or publication was executed.

### Private request/claim coordinator slice on 2026-09-20

Storage format 7 adds one immutable validation request, one exclusive compiler
claim and a distinct audited interruption. Each request fixes the original
input count/digest, named authority revision and canonical capability profile.
Plan/result writes and protected input reads must carry the same request, claim
and run identity. A first artifact Begin closes compiler access to the input.
Historical formats 3–6 keep their original framing and replay semantics.

The private management adapter reconciles simultaneous requests to the original
operation. Its runner commits a claim before compilation, freezes both artifacts,
then streams bounded plan fragments and result batches through the real Store.
It never installs resources, seals resource mutations or invokes providers.
Cancellation keeps the original claim and committed prefix; another runner cannot
recompile it. Request/claim/interruption metadata survives log/snapshot recovery,
and retained interruption receipts survive bounded cleanup. A malformed snapshot
regression exposed a missing request on an interrupted state; that state is now
rejected explicitly.

Scoped qualification and independent review passed:

| Slice | Executed evidence |
| --- | --- |
| Request transitions | Go 1.27.1 race 1.828 s; Go 1.25 0.656 s; competing claims, authority/expiry and immutable interruption checks. |
| Snapshot format | Release race 22.489 s; minimum Go 9.954 s; externaljobs race 22.377 s; six real Raft log/snapshot restart cases and prior-format regressions. |
| Receipts and cleanup | Release race 26.317 s; minimum Go 18.760 s; original receipt after partial cleanup/restart and day-29/day-31 retention. |
| Claimed input view | Release race 2.406 s; minimum Go 1.184 s; memory/bbolt, isolated copies and closure after cancel/revoke/expiry/Begin. |
| Private runner | Final release race 1.914 s; minimum Go 0.743 s; externaljobs race 2.001 s; valid/rejected outcomes, retained original result attribution, simultaneous requests, interrupted unwrap/append and active streaming cancellation. |

Management source/artifact/coordinator race tests also passed in 63.079 s against
unchanged production code, before the final additional test assertions. Final
focused qualification includes those added assertions. Durable and management
vet pass. SDK `Operations.Wait` now returns original `interrupted` terminal
responses after one GET; its regression failed before the fix and passed with
release race 2.134 s and minimum Go 1.079 s. `validated` remains pending.

Reports and source hashes are under `bin/verification/collection-validation-request-integration`,
`collection-validation-request-core`, `collection-validation-request-snapshot`,
`collection-validation-request-receipts`, `collection-validation-attempt-view`
and `sdk-operation-wait-interruption`. Earlier fixture/compile failures remain
recorded. The complete `internal/persistence` release race suite subsequently passed
in 397.519 s with no filters/exclusions and no changes among 713 recorded
Go/module/testdata/workspace inputs. Web-server race tests passed in 39.854 s,
application tests in 25.886 s and management-authentication tests in 1.286 s.
The first application invocation included an incorrect `internal/authn` package
path; its setup failure remains recorded, and the actual `managementauth` package
passed separately. These results do not represent the full repository/release
matrix or an automatic coordinator restart-continuation guarantee.

That request/claim slice left lifecycle ownership and startup retirement pending;
the subsequent private lifecycle qualification below closes those internal seams.
Public Validate/Activate, result pagination, conditional item activation and
connected SDK/CLI/browser Apply remain open. No publication or embedded-asset
change was made in the request/claim slice.

### Private worker and main lifecycle on 2026-09-20

Normal main now owns the collection validation coordinator after authentication
and catalog verification and before HTTP readiness. Its Store selector copies at
most 64 detached metadata records, with no resource payload, ledger/history read
or permission to execute. One registration is allowed per opened Store, including
across separate Catalog instances; stopping a coordinator does not release it.

The registering owner retires eligible abandoned claims before launching one
compiler worker. Never-claimed requests may start their first compilation after
current authority and the original capability profile are checked. Previously
claimed work is interrupted under its original operation/request/claim identity;
it is never recompiled from a newer catalog. Finalized, canceled, expired and
otherwise terminal evidence remains intact. No validation path applies a resource,
installs a job or performs a provider operation.

The supervisor detects committed cancellation, authority/profile changes and
expiry, cancels the attempt and joins it before selecting another. Uncertain
submissions are flushed before interruption uses the current committed prefix.
Main observes fatal coordinator exits and includes its health in readiness.
Shutdown closes admission and cancels/joins the worker before the durable flush
and storage/encryption closure. A deadline bounds waiting, not arbitrary Go/KMS
execution: if work remains blocked, dependencies stay owned until process exit.

Scoped executed evidence:

| Slice | Results |
| --- | --- |
| Metadata selector and registration | Go 1.27.1 ordinary 2.037 s, race 3.563 s; Go 1.25 2.054 s. |
| Worker concurrency and shutdown | Go 1.27.1 race 1.305 s. |
| Real-process worker crash/restart | Go 1.27.1 race 6.689 s; Go 1.25 6.078 s. |
| Normal main lifecycle | Go 1.27.1 race 29.771 s; Go 1.25 5.021 s; `externaljobs` race 6.063 s. |

Independent reviews passed. Commands, source hashes and exact boundaries are
recorded under `bin/verification/collection-validation-work`,
`collection-validation-worker`, `collection-validation-worker-restart` and
`main-collection-lifecycle`. The full durable race pass of 397.519 s recorded in
the preceding section predates this selector/worker/main slice. Broader current
qualification is not inferred from those earlier results.

This completes private lifecycle ownership, not the public asynchronous Validate
or retained-result contract, conditional activation or SDK/CLI/browser Apply.
Candidates remain private. Provider-account, native/platform, packaging,
documentation and million-monitor 24-hour gates remain open. No publication was
performed.

### Public async validation and SDK contract on 2026-09-20

The private branch now registers `POST /operations/{id}/validate` as 202 admission
of the original durable request, and `GET /operations/{id}/validation` as a
bounded read of its original sealed result. Pending compilation/publication,
sealed rejection, interruption, cancellation, expiry and unavailable history
remain distinct. Cursors retain original result identity and reader/policy bounds;
result metadata includes original source coordinates without resource bodies,
credentials or filenames. Activation remains absent from discovery.

SDK `Validate` returns the original operation; `Validation`, read-only
`WaitValidation` and lazy `ValidationItems` expose retained evidence. Apply checks
a matching sealed successful summary and first-page input metadata before its
explicit activation request. It does not gather the complete result fleet,
recompile, replace an operation or turn canceled waiting into server cancellation.
General Wait now recognizes rejected/invalidated terminal states while leaving
validated pending for activation completion.

Scoped SDK qualification passed on unchanged recorded source: release default
race (client 7.706 s, collection 11.418 s), release tagged race (11.017 s and 9.852 s),
Go 1.25 default (9.424 s and 2.219 s), Go 1.25 tagged (8.716 s and 1.999 s), and vet.
Both isolated generation roots matched checked-in outputs; reference/source-copy
consistency and six documentation tests passed. Example modules passed default
race, Go 1.25 and tagged race; their immediate-seal in-memory fixture is explicitly
separate from server durability/history evidence. An initial documentation-test
invocation used the wrong Python import path; the failure and corrected pass are
retained. Independent SDK source review passed. Commands, all package results,
source hashes and boundaries: `bin/verification/sdk-async-validation/result.json`.

The first real-server campaign found that the SDK emitted `cursor=` on a first
page, which strict HTTP query validation correctly rejected. The SDK now omits
an absent cursor; native TLS/generated URL tests assert first and continued
queries exactly. The final SDK matrix above includes that correction; earlier
results and the actual integration failure remain recorded. The corrected real
browser/SDK/restart release-Go race campaign subsequently passed in 11.608 s
(test 10.53 s), using Chrome 153.0.8010.36, Node 24.21.0 and Playwright 1.56.1.
It exercised normal TLS/Raft main startup, the embedded file chooser and actual
Worker/WASM parsing, exactly five explicit browser writes and zero Activate
requests. SDK reads reconciled the original operation/result/digest after restart;
target requests and active catalog changes remained zero, and checked plaintext
canaries were absent. Embedded index SHA-256:
`df03963247b61545e917ebbea80e1bc53d9c46eab32b91e228505069918bc578`.
Source hashes and complete output:
`bin/verification/collection-validation-browser/fixed-release-race.json` and its log.
The same Go 1.25 campaign passed in 10.073 s (test 10.05 s). Final parser
reproduction and current Vite/embedded assets byte equality passed. Both commands,
versions, hashes and the retained first failure are consolidated in
`bin/verification/collection-validation-browser/result.json`.

The complete default-build `internal/httpserver` race suite also passed in
52.800 s, including actual SDK first-page, continuation and WaitValidation calls
against the real handler, alongside route/auth/cursor coverage. Evidence:
`bin/verification/collection-validation-http/full-server-race.json` and its log.
The existing CLI package ordinary suite passed in 2.427 s before the isolated
SDK cursor omission correction, which current CLI commands do not call. That
CLI result does not implement or qualify future collection validation/apply commands.
This is scoped validation/import evidence,
not activation, full browser accessibility or a release qualification. Candidate
publication remains private; no tag, release, image or chart was pushed.

### Remaining work

The SDK `Operations.Wait` cancellation spelling mismatch is separately corrected:
canonical `canceled` and legacy `cancelled` both return the original response after
one authenticated TLS GET. `validated` remains pending, and context cancellation
sends no mutation. Focused Go 1.27.1 race passed in 2.088 s, Go 1.25 passed in
1.168 s, and independent review passed. Evidence:
`bin/verification/sdk-operation-wait-cancellation/result.json`. This later SDK
change has its own scoped evidence; the earlier application/web-server run is
not presented as a rerun against it.

1. Implement conditional activation, cancellation during activation, per-item
   progress/recovery and original-operation input reselection,
   then qualify complete browser and shared SDK/CLI Apply. Keep remaining async
   failure/cursor/permission checks scoped to their actual evidence. The
   [collection-listing contract](collection-operation-listing.md) is implemented
   and has the separate qualification record below.
2. Finish remaining SDK/CLI operation contracts and browser diagnostics.
3. Exercise the actual normal server, browser, restarts, permissions and packaged
   embedded assets together; independently review the integrated result.
4. Complete packaging, dependency, platform and documentation acceptance before
   selecting any release candidate.

The named publication, native packaging, provider-account and million-monitor
24-hour endurance gates remain separate and unpassed. No component test above
changes that status or permits early public release.

### Owner collection discovery and retained-result browsing on 2026-09-20

The operation list now captures ordinary shared receipts and the current named
operator's collection metadata at one durable observation boundary. Later
completion, cancellation, cleanup and insertion do not rebase the original list.
Terminal collection rows merge bounded primary evidence and validate their indexes;
missing evidence is unavailable. Small lists fill one page across phase boundaries;
large filtered histories may return an advancing empty page when their shared
inspection budget is exhausted. Exact monitor filtering excludes collection-wide
operations, because no retained collection-membership index is claimed.

Collection detail requires the original actor and current read permissions, while
ordinary receipts remain shared. Both families capture detached metadata, release
Store/FSM locks before history I/O, then recheck health and restore identity. HTTP
reads keep policy/cursor locks out of history access, reserve snapshot bytes before
copying, cap concurrent reads and recheck current authority and deadlines before
publishing. Lock waits honor cancellation; kernel filesystem calls are still
cooperative and cannot be forcibly interrupted by this contract.

The dashboard distinguishes collection inventory from application outcomes and
provides an explicit **Read original validation result** action. It keeps one
100-row page, pins pagination/refresh to the original seal, and clears data on
identity loss or denied detail access. Source tokens and document/item coordinates
are retained without local filenames, input bodies or secrets. A validated upload
that has expired is shown as expired without manufacturing a terminal event; its
original result has its separate retention period.

Frontend qualification passed all 387 tests across 26 files, generated-contract
consistency, TypeScript, lint and the Vite build. Independent review passed. The
embedded assets and dependency notices were regenerated. A real Chrome campaign
used normal main startup, authenticated TLS, Raft, the embedded file picker and
production Worker/WASM. After disposing of the local draft, it found the original
collection through the list and explicitly read the same sealed result. The test
pins the first successful result rather than comparing only the latest response.
The SDK then reconciled the complete list/detail receipt and original result after
a normal owner restart. Release-Go race passed in 12.896 s; Go 1.25 passed in
11.845 s. Exactly five explicit import writes occurred, with no additional browsing
writes, no active catalog changes and zero target calls. The desktop result view
was inspected; this single viewport does not establish a full accessibility audit.
Embedded index: `b4f2223ac4a876a91e65bc3c1369cc6023c63272604e24e0bb1515bc8573129a`.
Reports: `bin/verification/dashboard-operation-browsing/result.json` and
`bin/verification/collection-operation-browser/result.json`.

The existing controls and recovery/review normal-main browser scenarios also
passed against all 38 unchanged embedded assets: release-Go race package
74.918 s (31.18 s recovery/review, 42.71 s controls), Go 1.25 package 74.589 s
(31.79/42.79 s). Controls exercised acknowledgment, dismissal/reopening, snooze,
disable, unsnooze and enable with 96/98 actual local checks. Recovery made exactly
one local webhook request per run; its unknown outcome and accepted operator
review survived normal restart without replay. These are local compiled-driver
fixtures, not account-backed provider evidence. Logs, source hashes and rendered
screens are preserved separately per compiler under
`bin/verification/operation-read-existing-browsers/result.json`.

Scoped backend qualification and independent reviews passed. Review found and corrected extra
empty phase pages in small lists, Store/FSM locks held during history reads, and
uncancelable readiness/ordinary-lookup waits. A final inspection-budget correction
charges newly retained segments when verifying original summaries. Full durable
race passed in 427.210 s before that final scalar-cost change and removal of unused
wrappers. Final affected durable race passed in 84.165 s, Go 1.25 in 46.167 s, and
vet passed. Their exact source boundaries are in
`bin/verification/management-operation-view/result.json`. A test incorrectly
used a legal space-containing monitor ID as invalid input; the fixture now uses
a forbidden NUL. An older admission test also expected a reader to see another
operator's collection. It now verifies reader denial and owner progress, including
during drain; independent review passed. The corrected complete web-server race
suite passed in 53.388 s. Final focused web-server/management checks passed Go 1.25
in 13.566/0.298 s and `externaljobs` race in 15.356/1.380 s; vet passed for both.
Final source hashes remained unchanged during that matrix. The point lookup's
separate release/minimum/tagged lock, restart and cancellation checks passed;
see `bin/verification/operation-access/result.json`. HTTP review is recorded in
its `http-review.json`. Final integration evidence is in
`bin/verification/management-operation-integration/followup.json`; the earlier
failed server assertion remains in the initial full-run report. The broad
management suite launched alongside that earlier server run failed six cancellation
assertions and reached its 600-second package timeout. The cancellation assertion
incorrectly required `validated: false`; a canceled operation now must omit that
field while preserving its original sealed result separately. The test-only
correction has independent review and focused ordinary/race/Go 1.25 passes
(0.860/2.061/0.856 s), recorded in
`bin/verification/collection-cancel-verdict-presence/result.json`.

The timeout interrupted the unchanged 40-by-900-KiB notification fixture after
250 seconds. Historical isolated race evidence took 510.153 package seconds;
that older result explains the time budget but does not qualify current source.
The repository Make recipe already allows 20 minutes for the full race package;
the initial direct command used a shorter limit. The complete management race rerun passed in **942.098 package seconds**
(943.316 seconds wall time), with an explicit 1,800-second package budget and
unchanged recorded source hashes. Evidence is in
`bin/verification/management-operation-integration/full-management-extended.json`.
No fixture was reduced or skipped. The earlier failed report remains preserved.
Race-fixture timing is not a production benchmark; this qualifies the completed
operation-browsing slice before subsequent activation work.

This adds observation and browsing, not conditional collection activation,
original-input reselection, CLI apply/validation or optional worker integration.
All candidates remain private and the broader shipping/provider/endurance gates
remain open.


## Private activation admission — 20 September 2026

Storage format 8 now admits one immutable original activation identity, bound to
complete encrypted input, the finalized plan, sealed successful validation and
original owner/profile. Fresh current authority is checked separately, including
exact retries after token rotation. Applying parents survive the former 24-hour
staging deadline; original validation history retains its independent 30-day
lifetime. Cancellation and explicit restore preserve and fence that identity.
No item mutation, child operation, provider call or public Activate route is
introduced. See [the admission contract](collection-activation-admission.md).

Core, snapshot, real-process forced-termination and connected management tests
have independent review. Tests found and fixed activation receipt comparison by
pointer identity; comparison now uses semantic values. Review also replaced a
preliminary cancellation health check that could wait on owner locks without
respecting the context. Protected owner-aware storage reads now perform that
check. Frozen snapshots, mandatory namespaces, formats 3–7, post-deadline reads,
exact cancellation and restored-epoch denial have scoped evidence in
`bin/verification/collection-activation-admission/result.json` and
`bin/verification/collection-activation-snapshot-crash/result.json`.

The first full durable race run failed after 469.152 package seconds in an
existing manual cleanup fixture. Diagnostics reproduced background maintenance
advancing validation/input removed counts from the captured `257/0` to
`257/256`, or retiring the header before the next manual step. A test-only change
sets its cancellation observation ahead of wall time and before expiry, preserving
all four exact cleanup steps, timezone fences, intermediate snapshot checks and
final header removal. Original failures and diagnostic evidence remain in
`bin/verification/collection-validation-cleanup-fixture/result.json`. Independent
review passed; corrected race and Go 1.25 repetitions passed 10/10 each in
31.378 and 16.869 seconds. Production cleanup behavior is unchanged.

The integrated rerun passed:

| Check | Package result |
| --- | --- |
| Complete durable race suite | 471.212 s |
| Affected management / web-server race checks | 9.868 / 19.753 s |
| Complete localadmin race suite | 10.825 s |
| Affected Go 1.25 durable checks | 106.643 s |
| Affected Go 1.25 management / web-server checks | 8.484 / 16.077 s |
| Tagged admission race: durable / management / server | 38.728 / 2.467 / 2.463 s |
| Vet for the four affected packages | Passed |
| Release recipe and publication-policy tests | 19 passed |

Exact commands, logs and hashes are in
`bin/verification/collection-activation-integration/followup/result.json`.
`closeout-audit.json` confirms both source contents and file inventory stayed
unchanged; the initial failed run remains in the parent directory. The matrix
qualifies the admission boundary before subsequent execution prerequisites.
Future timestamps exercise lifecycle rules; process kills exercise tested
committed boundaries. Neither is a 24-hour soak or universal power-loss claim.

The next private work is catalog preparation/installation and a bounded index of
the original sealed plan, followed by atomic conditional item acceptance with
retained child outcomes. Public activation, original-input reselection, CLI
apply/validation, optional workers and broader shipping gates remain open.
All candidates remain private.

## Collection execution prerequisites — 20 September 2026

The catalog mutation path now separates pure preparation from installation under
one uninterrupted FSM lock. Preparation owns submitted buffers and checks the
original conditions without changing the catalog or its indexes. Installation
performs no storage I/O. The historical path and the refactored path produce the
same scoped results, receipts and catalog tokens. Independent review passed;
new release/default/race/minimum/tagged tests passed, as did existing catalog,
operation, restore, control-removal and adoption race tests (84.174 s), connected
management/controller/server checks (1.540/37.574/2.202 s), and vet. Evidence:
`bin/verification/catalog-delta-integration/result.json`. No accepted child
terminal is persisted by this refactor alone.

The original-plan execution index is also implemented and reviewed. It verifies
the complete encrypted-input commitment, finalized plan framing, original source
coordinates and per-row range digests. It retains bounded row ranges and original
reverse-reference obligations, including required earlier touches and their last
use. It neither reads current catalog state nor authorizes execution. Applied
tests passed ordinary Go 1.27.1 (4.668 s), race (18.654 s), Go 1.25 (4.985 s),
`externaljobs` race (18.351 s), and vet. The tests include actual memory/disk
views, corruption, quotas, contended-lock cancellation and a logical row above
4 MiB with 7,000 guards. Exact hashes and scope are in
`bin/verification/collection-execution-index-integration/result.json`.

Original conditional guard resolution and the
[execution ledger](collection-execution-ledger.md) are now implemented and have
passed scoped qualification. The verifier streams the complete original row,
uses only certified earlier accepted outcomes, and checks reverse-reference
obligations before overwriting their tokens. Missing progress is an explicit
error; a known failed predecessor blocks the dependent item. Its tests passed
Go 1.27.1 ordinary (6.488 s), race (26.241 s), Go 1.25 (6.778 s), tagged race
(26.936 s), and vet. Evidence:
`bin/verification/collection-item-guards-integration/result.json`.

The ledger atomically consumes preparation, appends immutable outcomes and
records compact child terminals. Each accepted child prepays 4,096 bytes for
its terminal, preserving completion capacity at the logical quota ceiling.
Record/stream/ledger tests passed release race (24.352 s), Go 1.25 (1.945 s),
`externaljobs` race (24.121 s), and vet. Existing input/plan/result ledger and
format 6/7/8 snapshot/restart regressions passed with race detection (79.329 s).
Tests cover real bbolt write failure, cross-parent rollback, quota exhaustion,
concurrent frozen memory views, disk corruption and the 3 MiB record boundary.
All 178 durable source hashes matched the final guard inventory. Independent
review passed. Exact commands, hashes and preserved initial test-compilation
failures: `bin/verification/collection-execution-ledger-integration/result.json`.

Runtime conditional acceptance, authoritative progress, child-lifecycle hooks,
snapshot integration, cleanup and public Activate remain unfinished. Storage
format 8 remains the latest enabled format; formats 3–8 reject a nonempty
execution namespace instead of silently omitting it. These prerequisite reports
qualify their recorded source boundaries; they do not imply a whole-repository
or end-to-end batch-application pass on subsequent source changes. All candidates
remain private until the agreed gates pass.


## Collection child completion boundaries — 20 September 2026

Catalog reservations now survive pure preparation and any pre-installation
storage failure. A business rejection still consumes the reservation and emits
its original terminal rejection; retry cannot resurrect it. The focused
release/race/minimum/tagged checks, existing allocation/catalog regressions and
vet passed before child-hook integration. Independent review passed. Evidence:
`bin/verification/catalog-reservation-integration/result.json`.

The execution progress model commits the original activation time, prepared
ciphertext identity, contiguous outcome prefix and sparse child-terminal root.
It distinguishes conditional catalog decisions from controller results and
preserves prepaid completion capacity. Its terminal tree has constant-time
cloning and copies 15 nodes on an insertion. Delayed observations preserve their
own timestamps; `LastAt` is the maximum observed time. Root reconstruction and
framing tests use independent reference calculations. Final model race (1.310 s),
Go 1.25 (0.171 s), tagged race (1.247 s), vet and independent review passed:
`bin/verification/collection-execution-progress/result.json`.

The bounded pending-child index and pure terminal preparation are implemented.
Only 4,096 unresolved ordinary configuration children can be retained; historical
completed outcomes do not consume those slots. Exact original receipt identity
and parent/plan/row binding are checked. Canceled and restore-invalidated parents
retain their unresolved children. Focused race (8.157 s), minimum (0.734 s),
tagged race (8.057 s) and independent review passed:
`bin/verification/collection-child-links/result.json`.

Existing completion, catalog supersession and explicit-restore paths now persist
linked terminals before removing pending receipts. Reserved replacement capacity
is retained on storage failure; restore stages parent invalidations and its next
cursor before writing. A fatal terminal write stops the remainder of the Raft
envelope. Encoded bytes and reserved capacity are checked separately against the
parent commitment, and an empty unbuilt link index cannot masquerade as no
pending children. Cleanup rejects execution-bearing parents until result
publication and retention are integrated.

Initial focused lifecycle ordinary/race/minimum/tagged checks passed. Selected
existing catalog/operation/restore/activation race regressions passed (82.485 s),
as did connected management/controller/server checks (38.291/35.597/10.439 s)
and vet. Independent review prompted additional direct-catalog, dispatcher and
old-format negative controls; their final focused evidence is recorded separately
in `bin/verification/collection-child-lifecycle/review-followup.json`. The original
matrix and exact source boundary remain in
`bin/verification/collection-child-lifecycle/result.json`.

These hooks are exercised with a real admitted parent and explicitly supplied
acceptance fixtures. They do not establish public batch execution, execution
snapshot recovery or a new storage format. Execution-bearing headers and rows
remain rejected by formats 3–8. Original-item acceptance, complete inventory
recovery, public activation, Apply results and their interface tests remain open.
All candidates remain private.

## Registered private collection execution — 20 September 2026

This checkpoint supersedes the earlier statements that execution remained
unregistered and format 8 was the latest enabled format. Storage format 9 now
includes isolated `begin`, `prepare` and `decide` commands plus the complete
execution snapshot stream. The
[integration contract](collection-execution-integration.md) records the current
transaction, recovery and compatibility boundaries. Public activation remains
unregistered.

Original target/dependency guards, certified own outcomes and current authority
are checked before conditional acceptance. Consuming preparation, recording an
item outcome and superseding any linked earlier child share one ledger
transaction. Native failed-write tests preserve both parents and active catalog
state. The source review passed. Default race, Go 1.25, tagged race and connected
backend checks passed for the scoped execution path; the final full durable race
rerun passed in 449.031 s. All 200 recorded durable-source and release-recipe
hashes matched before and after that run. The first full run found a cancellation
fixture race with legitimate background cleanup, not a new item transaction
failure. Its unchanged assertions now use a future cancellation observation so
only the explicit test cleanup advances pages. Independent review and ten race
repetitions passed (48.359 s). The initial failure remains in
`bin/verification/collection-execution-log/durable-race.log`.

The first real-process execution campaign passed (6.438 s with its adjacent
ordinary checks). It kills a single-node Raft owner after format-8 snapshot plus
execution logs, a prepared snapshot, an accepted snapshot, and an accepted
snapshot plus a child-terminal log. Every case validates the stopped directory
and reopens the Store before reconciling the original item. Restored preparation
does not become acceptance automatically; an accepted retry creates no new child.
These tests use synthetic encrypted catalog candidates and invoke no providers.

The separate protected preparation slice passed independent review after fixing
an uncancellable readiness read and a frozen-reader/FSM remap wait cycle. Its
holder now verifies and detaches one next item, closes the reader and drops the
full index before crypto or live-state rechecks. A native 8 MiB database-growth
test exercises a writer while the detached holder remains alive. Original input
MAC, source/plan/result identity, omitted credential values, current authority,
target changes, cancellation and secret-safe errors are checked. Scoped evidence:
`bin/verification/collection-candidate-preparation/result.json`.

The actual management compiler and preparation helper are now connected to the
registered commands by integration tests. They pass release race (4.111 s),
Go 1.25 (2.540 s), tagged race (4.077 s), vet and independent review. The tests
cover real Raft prepared/accepted snapshot-reopen, exact committed ciphertext
reuse with crypto unavailable, one original pending child, omitted credentials,
unchanged items, cancellation, principal expiry and outside target changes.
The initial nil-versus-empty reference-slice assertion is preserved; exact
canonical serialized bytes are the retry invariant. Evidence:
`bin/verification/collection-candidate-execution/result.json`.

The tested child is catalog-committed, not controller-applied. The management
coordinator, cached per-item protected reads, retained application results,
terminal cleanup, original-input reselection and public API/SDK/dashboard Apply
are still open. The [next implementation notes](collection-executor-next-steps.md)
give concrete bounded work for those connections. Validation-time headroom for
server-added candidate metadata is being implemented separately. No shipping or
provider-performance gate is closed by these scoped checks. Candidates remain
private.


## Candidate headroom and private coordinator — 20 September 2026

Validation now checks exact eventual resource encoding after normalized defaults
and omitted credential preservation. Creates account for server UUIDs; updates
retain the exact original UID and generation rules. Unchanged resources do not
require replacement headroom. Oversized items receive the original source's
`invalidResource` issue before a successful plan. Policy version 2 preserves the
compatibility boundary: the one-attempt runner rejects old-profile work without
recompilation, while its lifecycle supervisor may record a capabilities-change
interruption; admitted old-profile preparation rejects before crypto. Independent
review, release race (35.163 s), Go 1.25 (10.653 s), tagged race (34.872 s) and vet
passed. Evidence: `bin/verification/collection-candidate-headroom/result.json`.

The bounded execution selector and once-per-Store registration passed independent
reviews, default race (5.438 s), Go 1.25 (3.871 s), tagged race (5.564 s), and vet.
It exposes detached scalar original identities and progress only. Ordinary Raft
reopen resumes applying work; explicit restore and reprovisioning do not revive
old-epoch admissions. Former upload expiry does not stop admitted execution.
Evidence: `bin/verification/collection-execution-work/result.json`.

The private management coordinator now keeps one original pending command across
backpressure and uncertain replies, requiring a FIFO flush before retry or an
absence decision. Committed preparation and unchanged rows bypass decryption;
current authority and management admission are rechecked before submission. An
uncooperative crypto call retains ownership through shutdown wait expiry.
Original five-resource dependency order and partial conflict outcomes passed
ordinary tests; default race plus existing preparation/command integration
passed (25.217 s). Go 1.25 (10.477 s), tagged race (27.114 s), complete affected
management/controller/server ordinary tests and final vet also passed. All
management and durable production hashes stayed unchanged; two durable-only
tests subsequently accounted for the new original-frame hash charge. Evidence:
`bin/verification/collection-execution-coordinator/result.json`. Application startup/supervision wiring is a
separate in-progress slice. No public activation route is enabled.

Cached protected reads retain original encrypted-frame hashes and charge their
32-byte-per-row cost. Their focused release race (51.837 s), Go 1.25 (24.059 s),
tagged race (50.137 s), affected vet and independent review passed. A native
64-row fixture used 1,974 cursor operations cold, 61 hot, and 61 after another
prior outcome; this measures read work, not throughput. Two test-only accounting
corrections and their earlier failures remain recorded in
`bin/verification/collection-preparation-cache/result.json`. A full durable race
run on this later cached implementation is in progress separately. The coordinator,
cache and application integration do not claim retained apply-result publication,
parent completion/cleanup, dashboard Apply, native packaging, provider-account
verification or endurance qualification. All candidates remain private.


The physical C: mount was rechecked at 2026-09-20 03:51:41 UTC: available
space was 24,890,294,272 bytes (23.181 GiB), below the agreed 30 GB minimum.
The virtual Linux filesystem's advertised free space does not satisfy that
host prerequisite. Fixture/campaign headroom has not been measured, and no
large validation campaign was started. This is a point-in-time observation;
remeasure before the campaign. Evidence: `bin/verification/host-space-2026-09-20.json`.

## Controller recovery and private execution finalization — 23 September 2026

The storage package now uses the technical name `internal/persistence`. Imports,
tests, scripts and active documentation were migrated; stored identities and
encryption domains were not renamed. The current branch remains private.

Initialized controllers now retain their original unresolved write and received
result suffix through temporary Raft leadership loss. A FIFO barrier precedes
exact command retries. Check observations, prepared catalog projections,
removals, snooze expiries and returned executor markers keep their original
identities and times. The owner records which local result/expiry items have
already been applied, preventing a later canceled read from duplicating SLO
accounting. Check admission withheld before invocation creates no provider
failure. Startup failures and permanent storage faults remain unavailable.

The contextual read path now covers both the ECS owner and its background
catalog reconciler. It bounds Store/FSM lock acquisition and preserves native
administrative catalog inspection separately from operational readiness. An
interrupted index rebuild publishes no partial index. Shutdown stops admission,
joins preparation and preserves unresolved result/executor ownership; a caller's
drain deadline cannot turn ambiguous work into a clean shutdown.

Independent review found additional uncancellable background reads and malformed
expiry/configure responses that could have released ownership or projected the
wrong state. Those boundaries now use the caller's context and reject malformed
responses before changing any item in the local batch. Fourteen response
corruption fixtures commit through the real state machine before corrupting only
the returned projection. Lock-contention tests execute in the persistence
package; management tests separately cover cooperative encryption cancellation.
They are not a combined real-process lock-contention shutdown campaign.

The [controller recovery contract](controller-leadership-recovery.md) records the
implemented ownership rules and remaining combined failure schedules. The actual
Raft controller test uses a higher-term vote while one HTTP check is in flight:
readiness closes during follower state and the same process recovers one committed
check and one SLO observation without invoking the provider again. A separate
startup test proves that re-election does not revive incomplete initialization.

Private storage format 10 now retains an immutable original execution summary
and typed history anchor. Accepted catalog decisions remain separate from child
application. Cancellation and explicit restore keep their original identities
and timestamps. Exact finalization retries do not add another event. A valid
unstarted admission beyond the audit bound stays healthy, retained and unfinalized
on quota rejection. See the [finalization contract](collection-execution-finalization.md).
Coordinator-driven finalization, joined item publication, result seals/protected
pagination, execution cleanup and public activation are still unfinished.

The full Go 1.27.1 root suite passed, including persistence in 272.183 seconds,
before the last review followups. After those followups, the full affected
application/management/controller/server suites passed (37.193/86.762/38.381/
27.633 seconds; systems 2.382 seconds). The final controller/format-10 Go 1.25
selection passed (systems 1.941 seconds, persistence 12.247 seconds). Contextual
persistence tests passed with race detection (5.492 seconds) and Go 1.25
(4.862 seconds); contextual management/preparation races passed (1.350 seconds).
Affected-package vet passed. All 23 projection/malformed-response scenarios passed
under race detection (2.747 seconds), and management cancellation races passed on
Go 1.25 (1.355 seconds). Independent reviews of controller, persistence and
management changes found no further actionable defect after the corrections.
The final optional-driver race selection passed (systems 3.350 seconds,
persistence 36.970 seconds), including contextual reads, actual controller Raft
re-election/startup, local executors and format-10 results. All 402 recorded
production/build input hashes remained unchanged. Commands, logs and scoped
results are recorded in `bin/verification/controller-recovery-2026-09-23/result.json`.

The format-10 focused race campaign passed in 35.214 seconds, including native
log/snapshot reopen, stopped inspection, actual restore and fresh authentication,
corruption, original command/digest compatibility, historical cleanup semantics
and the concrete 10,001-guard quota boundary. Its independent result suite passed
in 7.034 seconds and two release format/build contract tests passed. No
process-kill campaign specifically at the new finalization/history boundary has
been run. The initial root-suite historical-cleanup fixture failure is retained
with the evidence; its corrected test explicitly exercises old-format replay,
while separate new-format tests require admitted input retention.

This checkpoint does not qualify native releases, provider accounts or the
million-monitor campaign. It does not publish source, SDK tags, packages, images,
charts or documentation. The dashboard finalization goal remains open.

## Retained execution results and coordinator finalization — 23 September 2026

The existing execution coordinator now finalizes applying, canceled and
restore-invalidated parents after the original accepted children settle. It
preserves one original command across uncertain replies and leadership barriers,
resolves pending item work first, and defers quota-limited parents without blocking
others. The actual main/controller race scenario completes the original child,
finalizes its parent, restarts with authentication, and retains the same immutable
summary and exactly one anchor. Public Activate still returns 404.

Private storage format 11 adds canonical joined item rows, a bounded publication
command, an immutable history descriptor/seal, and explicit retained-result
expiry. Cold audit and cached continuation preserve original input/plan identity
and distinguish committed catalog decisions from controller outcomes. Native
restart retains partial publication; snapshots recompute the published prefix
commitment. Original source namespaces still cannot be retired. The protected
Store result view checks ownership before history and rechecks epoch, health,
summary and elapsed expiry after reads; it requires the retained original header.

The canonical schema, generated SDK and browser models now describe result
availability independently of parent state. SDK result pages, lazy iteration and
waiting preserve original identities and distinguish pending, unsupported and
expired observations. Browser decoders enforce the same bounded metadata and
original ordering. The pinned dashboard rebuild passed TypeScript, lint and all
404 tests in 27 files; SDK default and externaljobs race suites and deterministic
regeneration passed. These consumers do not enable server-side Apply by themselves.

Independent review corrected missing anchor-primary validation, snapshot prefix
validation and full-prefix map copying in memory publication. Retention tests
preserve deterministic replay while enforcing the monotonic local cutoff. The
maintenance retention floor is used only for already-expired cohorts; still-
retained publication uses the actual supplied observation. A test that assumed a
seal must precede explicit expiry now exercises both legal orderings through the
protected reader. Repeated race runs cover that scheduling boundary.

Commands, retained failure/correction evidence, source hashes and final results
are recorded in `bin/verification/collection-execution-publication-2026-09-23/result.json`.
See the [publication contract](collection-execution-publication.md). Remaining
work includes execution cleanup and partial-retirement recovery, protected HTTP
result pagination and accurate operation projections, public activation, shared
SDK/CLI/dashboard Apply acceptance, optional external-worker integration and the
full private shipping matrix. Actual provider-account evidence and the selected
million-monitor 24-hour campaign remain outstanding. This checkpoint publishes
no source, modules, packages, images, chart or documentation site.


## Protected collection result interfaces — 23 September 2026

Collection operation detail and list observations now report authoritative
committed and applied counts. Protected HTTP result cursors bind original owner,
operation, sealed result, authorization generation, limit and watermark. The
server does not hold cursor/policy locks over history I/O, and checks storage and
retention again after any final lock wait. The committed history cutoff is
published atomically after successful persistence; a failed catalog save cannot
advertise an uncommitted cutoff. Historical receipt JSON remains unchanged.

The dashboard explicitly reads one result page, separates catalog decisions from
controller outcomes, and can wait for results independently of parent state. It
clears rows on sign-out/failure and fences late replies after Stop or navigation.
A sticky invalidation flag prevents a failed refresh followed by retry/Stop from
reviving stale ready/count metadata. The parent query cache retains no duplicate
item page. `cpractl get operation ID --results` reads exactly one SDK page, with
bounded flags, canonical JSON/YAML, and separate decision/controller/restore
columns. Ordinary operation reads retain their existing flag contracts.

Independent review corrected three additional admission/display cases: storage
closure while waiting for a cursor lock, global history expiry after the page
read with an unchanged parent marker, and a missing per-row CLI restore marker.
The repaired projection, HTTP, dashboard, CLI and standalone retirement primitive
received independent review with no remaining actionable findings. The browser
capture harness was also independently reviewed.

| Check | Executed result |
| --- | --- |
| Full Go 1.27 persistence/management before the final cutoff followup | PASS, 478.577 / 92.350 seconds |
| Post-followup retention/projection race checks | PASS, persistence 45.228 / management 1.538 seconds |
| Post-followup Go 1.25 retention/projection | PASS, 5.948 / 0.365 seconds |
| Protected HTTP pagination/lock/fence races | PASS, 15.125 seconds |
| Full server suite before the final cutoff followup | PASS, 42.778 seconds |
| Historical digest and command shape compatibility | PASS, 0.016 seconds |
| Standalone retirement checkpoint, race / Go 1.25 | PASS, 6.606 / 0.671 seconds |
| Pinned dashboard rebuild, typecheck, lint and tests | PASS, 422 tests in 28 files |
| Normal embedded dashboard, TLS/Raft/controller and restart | PASS, Go 1.27 race 21.591 / Go 1.25 19.423 seconds |
| Full CLI suite | PASS, 3.702 seconds |
| CLI and actual HTTP result integration, race | PASS, CLI 1.765 / server 2.185 seconds |
| Go 1.25 CLI and server operation compatibility | PASS, CLI 0.523 / server 3.495 seconds |
| Development builds, embedded freshness and affected-package vet | PASS |

The browser test starts a privately admitted one-Credential collection through
normal main, then observes the real embedded application. It verifies resource
rows, original identity, committed/applied distinction, hide/reread, sign-out,
reader isolation and the same rows after a graceful restart. Its observed API
requests are GET-only; background execution still commits state. Two initial
response-capture harness failures are preserved. The corrected bounded CDP
observer validates complete JSON and reports interrupted captures separately.
The registered campaign rejects missing/skipped coverage, but this checkpoint
runs the added fixture separately rather than rerunning all five browser tests.

The bounded retirement checkpoint now verifies paired retired outcomes/terminals
and reconstructs original digest/root commitments through 10,000 decisions. It
has no persisted field, cleanup registration or deletion authority. Paired
retirement, exact quota refunds, partially retired snapshot recovery, header-last
cleanup and result lookup after header removal remain unfinished. The source
header remains required for all protected execution-result reads.

The [interface contract](collection-execution-interfaces.md) and
`bin/verification/collection-execution-interfaces-2026-09-23/result.json` record
these facts, commands, logs, source hashes and limitations. Public Activate,
connected SDK/CLI/dashboard Apply, external-worker integration and the full
private shipping matrix remain open. Provider-account and million-monitor
24-hour evidence remain separate outstanding gates. No source, SDK version,
release asset, image, chart or documentation site was published.

## Execution-prefix retirement and retained reads — 23 September 2026

The private format-12 `retire` command now certifies and deletes a bounded
execution prefix, pairing accepted outcomes with their child terminals. Each
transaction removes at most 256 records or 4 MiB and refunds exact execution
charge, including the original terminal reserve only after paired deletion.
Original execution/result commitments remain separate from physical remaining
charge. Encrypted input, plans, validation rows and headers are preserved.

Partial-prefix snapshot recovery reconstructs the original outcome digest,
terminal root, counts and charges from the checkpoint and surviving rows. It
rejects missing, extra, resurrected and unpaired records and installs no
executable caches or child ownership. Native process tests kill the owner after
the first committed batch and after completion, then verify stopped storage and
reopen from format11 plus a retirement log and from a partial format12 snapshot
plus its completion log. Committed history expiry between batches does not
refresh old command timestamps or turn retries into additional quota refunds.

Protected result points/pages can now recover the original owner, anchor, seal
and watermark after test-only removal of the source header. Reads check primary
and derived evidence, fail unavailable on missing unexpired records, and preserve
retention independent of an earlier cancellation receipt. Epoch, issued-handle
high-water, history identity, storage and cutoff changes are fenced after I/O.
Production header deletion and retained listing without headers are still open.

Independent reviews corrected one first-retirement certification gap: the
original validation rows must be verified before deletion, even when their
sealed descriptor remains intact. A native/memory corruption regression proves
that failure preserves image, rows and quota. Test review also fixed two failure
paths in the process harness: joining the diagnostics writer and capturing each
reopened Store independently. Primitive, command, recovery and protected-read
reviews now have no outstanding scoped findings.

| Check | Executed result |
| --- | --- |
| Paired ledger/stream/checkpoint tests, release Go race / Go 1.25 | PASS, 43.842 / 6.360 seconds |
| Final retired-parent recovery tests, race / Go 1.25 | PASS, 5.523 / 2.526 seconds |
| Command integration and historical format/digest compatibility, race | PASS, 94.302 seconds |
| Command integration, compatibility and process kill, Go 1.25 | PASS, 94.882 seconds |
| Native process-kill test, release Go / race | PASS, 54.732 including validation corruption / 91.200 seconds |
| Committed expiry and frozen-command retry, race / Go 1.25 | PASS, 111.961 / 86.918 seconds |
| Final retained point/page reads, Go 1.25 | PASS, 23.664 seconds |
| Final high-water/blocked-read fences, race / Go 1.25 | PASS, 2.615 / 1.222 seconds |
| Retained-read management and HTTP/CLI compatibility, race | PASS, management 1.593 / server 16.505 seconds |
| Corrected publication format-isolation test, race | PASS, 1.200 seconds |
| All-built-in-driver compilation and selected format gates | PASS; management/server selected no runtime tests |
| Release-recipe unit checks | PASS, 19 tests |
| Pinned dashboard rebuild, TypeScript, lint and tests | PASS, 422 tests in 28 files |
| Development `cpra` / `cpractl` builds and final affected-package vet | PASS |

The wider retained-read/publication race run originally failed only because its
old test treated format12 as unsupported. That log is retained; the corrected
format test and final read-fence checks passed separately. The original build
freshness failure also remains recorded; updating the recipe to format12
required the pinned dashboard rebuild. The rendered embedded index digest stayed
`85ac03eb483d404ddc7576fe9ec94f6ec8b6b3502c355412c7f81e0ec3e67bd7`.
No browser process campaign was repeated for this storage-only checkpoint.

The process test's cleanup-capture correction landed during its passing race
and Go1.25 runs. Final source review, compilation on Go1.25 and vet cover that
test-only failure-path edit; the evidence does not mislabel the preceding runs
as executing that edit. No production code changed after those runs started.

Exact commands, logs, source hashes and limitations are in
`bin/verification/collection-execution-retirement-2026-09-23/result.json`.
The [retirement contract](collection-execution-retirement.md) states the current
boundary. Automatic retirement selection, source/header cleanup, retained
listing after header removal, public Activate, connected Apply, external-worker
integration and complete private shipping qualification remain unfinished.
Provider-account verification and the million-monitor 24-hour campaign remain
separate gates. No commit, push, tag, release or documentation-site publication
was performed.

## Source retirement and retained operation listing — 24 September 2026

The private format-13 `retire_sources` command now completes collection cleanup
after all execution records have retired. It removes validation rows, then plan
fragments, then encrypted input, with a 256-record / 4 MiB transaction limit.
The header disappears only after all source namespaces are empty. Three compact
rolling digests commit the surviving prefixes while the original execution,
activation and result descriptors remain unchanged. Snapshot recovery verifies
those commitments without building executable state.

Automatic maintenance checks retained result metadata before proposing cleanup.
Live parents, unsettled accepted children and missing unexpired seals cannot
retire. Explicit committed expiry permits cleanup after history expires. Failed
history catalog replacement or ledger writes preserve the final source header,
counters and quota and make storage unavailable.

Protected operation listing now merges original execution anchors with terminal
receipts and captured headers. It retains owner and outcome, emits one operation,
and reads only metadata. Native queries seek over item namespaces; memory uses
an ordered anchor index. Final HTTP admission repeats storage, epoch, high-water
and retention checks after policy/cursor waits. Actual cleanup and native reopen
preserve the original result. Early validation/plan deletion keeps a captured
list valid; committing the cutoff before final header removal expires its old
cursor, and a fresh list resolves retained authority.

Review found and corrected two implementation issues. First, repeated complete
source audits made a sequence of small deletion transactions quadratic. A cold
audit now builds one separate, bounded 4 MiB certificate containing only digest
checkpoints; warm verification authenticates the selected tail in at most 512
decoded records / 8 MiB, plus one excluded frame-length probe of at most 2 MiB.
Borrowed ledger reads also avoid copying all memory maps each turn. Second,
advancing the history cutoff during every intermediate cleanup unnecessarily
expired list cursors. Only the final header-removal batch now explicitly advances
that cutoff. Natural history expiry retains its existing behavior.

| Check | Executed result |
| --- | --- |
| Integrated source cleanup, snapshots, four native process-kill boundaries, expiry replay and historical formats, Go 1.27 race / Go 1.25 | PASS, 227.429 / 117.043 seconds |
| Final source cache, complete source audit and model/recovery cases, ordinary / race / Go 1.25 | PASS, 23.378 / 111.532 / 24.429 seconds |
| Final negative admission, seal, fence and real storage-fault cases, ordinary / race / Go 1.25 | PASS, 8.648 / 9.959 / 5.855 seconds |
| Final retained-list groups including actual source retirement and native reopen, race / Go 1.25 | PASS, 27.712 / 25.620 seconds |
| Wider operation list/point/projection compatibility, race | PASS, persistence 103.268 / management 1.634 seconds |
| HTTP operation admission/list/result regressions, race | PASS, 18.127 seconds |
| All built-in driver compilation and selected storage-format tests | PASS; management/server/CLI selected no runtime tests |
| Pinned dashboard build, TypeScript, lint and tests | PASS, 422 tests in 28 files |
| Release-recipe unit checks | PASS, 19 tests |
| Development `cpra` / `cpractl` builds and affected-package vet | PASS |

Exact commands, logs, source hashes, earlier failures and corrected reruns are in
[`bin/verification/collection-source-retirement-2026-09-24/result.json`](../../bin/verification/collection-source-retirement-2026-09-24/result.json).
Initial test expectations incorrectly treated a removed issued header as a nil
result and assumed a list cursor survived committed final retention. Both were
corrected and rerun. An accidental collision with an existing untracked HTTP
test file was repaired from its recorded original contents; its SHA-256 matches
the prior checkpoint and its tests passed again. Independent reviews cover
storage, cache lifetime, failure handling and list/admission composition.

The embedded dashboard index remains
`85ac03eb483d404ddc7576fe9ec94f6ec8b6b3502c355412c7f81e0ec3e67bd7`.
This checkpoint did not repeat a real-browser process campaign or the complete
repository suite. Process-crash evidence is limited to the current Linux amd64
WSL filesystem. Cold maximum-size audit latency is not established by the warm
read counters. The [retirement contract](collection-execution-retirement.md)
states those boundaries.

Public path-only Activate and the connected SDK/CLI/dashboard Apply flow are
next. Optional external-worker integration, the complete private shipping matrix,
actual provider accounts and the selected million-monitor comparative/24-hour
campaign remain open. The overall dashboard goal remains active. No commit,
push, tag, release, module, image, chart or documentation site was published.

### 2026-09-24 — Public collection activation and connected Apply

The technical package remains `internal/persistence` (`package persistence`);
no Go imports or declarations use the former `internal/durable` name.

Public bodyless activation now binds the original owned input, successful sealed
validation and frozen plan to current committed operator authority. It verifies
artifacts outside the authorization lock and admits only the original intent.
Concurrent requests and uncertain replies reconcile that identity without a
second activation. The normal executor applies dependency-ordered conditional
changes and retains configuration decisions separately from controller outcomes.

The SDK now verifies activation disposition, can reconcile an uncertain response
through one read, and provides content-bound `collection.Wait`. The CLI adds
multi-file `apply`, ephemeral `--dry-run=server`, optional result waiting, and
`wait operation/ID`. The dashboard confirms the exact operation and validation,
allows cancellation while applying, and uses the bounded result viewer for
source-attributed decisions and controller dispositions. A canceled parent can
still have pending children; waiting never resubmits or cancels work.

Independent review corrected a protected receipt captured before a response-
admission wait: both domain retries and final HTTP responses now recheck current
epoch, health and retention. The final policy callback reads bounded metadata,
not history. Review also corrected successful HTTP replies with no admission
evidence and distinguished ready metadata-only activation receipts from full
result pages. The first connected race run exposed that latter SDK incompatibility;
its failed log is preserved beside the passing final runs.

Executed checks include:

- A real Chrome/file-chooser/embedded Worker/WASM/TLS/Raft import, validation,
  explicit activation, controller application, result read and owner restart.
  The exact original result survives restart and a subsequent activation retry.
- A native CLI two-file apply/Wait, cross-file credential dependency, two retained
  pages, dry-run with zero allocations, and unchanged identity after restart.
- The four connected collection scenarios pass with Go 1.27 race detection
  (89.473 s). Fresh browser activation and CLI apply also pass with Go 1.25
  (55.250 s). Disabled imported monitors perform zero target operations.
- Scoped management, HTTP, persistence response-fence and CLI regressions pass
  in ordinary/race/minimum-Go configurations. Applicable built-in driver tags
  pass the affected activation tests; no provider-account claim follows.
- SDK default race/minimum-Go and tagged tests, 430 dashboard tests, generated
  API/type/lint/build checks, affected-package vet, browser campaign contracts,
  and freshness-enforcing development builds of both binaries pass.

The [public activation record](collection-public-activation.md) and its linked
private manifest preserve commands, source hashes, review corrections and
observation limits. Existing browser validation/result-only campaigns retain
their distinct scope. CI now requires the new browser scenario and invokes the
actual native CLI integration, rather than leaving either behind an unset flag.

Cross-process original-input reselection remains the next collection dependency;
same-`Frozen` SDK resumption does not fulfill refreshed-browser or restarted-CLI
resumption. Maximum-input resource measurements, external-worker integration,
the full private shipping matrix, live provider evidence and the selected scale/
24-hour campaign remain open. No commit or publication occurred.

### 2026-09-24 — File normalization and format-14 upload fencing

The private normalization checkpoint now records the shared
`cpra.file.base.v1` parser profile, admission identity binding and storage
format-14 prerequisites for original-file reselection. Seventy-one fixed vectors
pin normalized JSON and source coordinates across the compiled browser Wasm and
independent native base/tagged adapters. The format persists the profile through
headers, tickets, replay, snapshots, retained results and source cleanup; omitted
legacy profiles retain their earlier identity bytes and command formats. The
internal upload fence checks the original prefix and current authority in the
same FSM step as the next append. It does not authorize configuration changes.

The [normalization record](collection-normalization.md) links its new
[private evidence manifest](../../bin/verification/collection-normalization-2026-09-24/result.json)
and selected source hashes. Existing normalizer and temporary-spool evidence
remain separate records, and the earlier public-activation checkpoint is
unchanged. Final executed evidence includes the format/prefix persistence race
and minimum-Go groups, management/HTTP regressions, reproducible SDK generation,
built-in-driver checks, 27 release-script tests and 433 dashboard tests. Connected
normal-startup browser/CLI reruns pass with Go 1.27 race detection (65.190 s);
activation browser and CLI checks also pass on Go 1.25 (49.906 s). These fixtures
use Linux amd64 WSL, disabled imported monitors and zero target calls.

The initial connected browser failure exposed loss of the normalization profile
between retained validation and operation observation; the corrected rerun is
recorded alongside that failure. Initial admission/vet compile failures are also
preserved with their corrections. Final response authorization now confirms the
same principal before returning admission headers or errors; its final race
passes (6.115 s), and a fresh Go 1.25 admission HTTP group passes (4.754 s).
Fresh SDK-reference and guide-synchronization checks pass. Final affected-package
vet has the executing agent's confirmed zero exit status.

This record qualifies the settled normalization slice only. Its source snapshot
excludes the separately developed source-reselection, proof and upload-verification
files. The encrypted-spool primitive and
[source/proof components](collection-reselection-proof.md) have separate evidence;
those primitive checks do not qualify the connected public attempt flow. Connected
source/inventory proof, authenticated runtime-attempt ownership and routing,
resumed suffix transfer, refreshed-browser/restarted-CLI flows and maximum-input
resource measurements remain unconnected or unqualified here. The overall
dashboard goal and private shipping gates remain open. No production code,
commit, release or publication was added while assembling this checkpoint.

### 2026-09-24 — Private encrypted source staging and original-input proof

The subsequent [source/proof component checkpoint](collection-reselection-proof.md)
implements bounded encrypted temporary files, ordered source-part admission,
exact last-part retry, streaming readers, and verification against both original
input commitments. It decrypts and compares the already-uploaded prefix and
recomputes that prefix's ciphertext digest and encoded byte total. Only the
missing normalized suffix is retained in encrypted temporary frames. Proof and
staging do not submit durable mutations or activate configuration.

The [scoped evidence manifest](../../bin/verification/collection-reselection-proof-2026-09-24/result.json)
records an independent combined Go 1.27 race run (11.488 s), Go 1.25 combined
tests (11.776 s), and applicable built-in-driver proof tests (9.421 s). Native
Raft log/snapshot close-and-reopen tests preserve the original operation and
ciphertext. Stopped policy replacement admits a fresh authorized proof; a revoked
operator is denied before key unwrapping. A separate actual killed-process spool
test verifies disposable ciphertext and bounded ownership-aware cleanup.

Independent review corrections include owner-first lookup, permission checks
around prefix decryption, ciphertext-chain verification, panic cleanup, and
suffix callbacks outside spool locks. Native Windows, proof-level forced crashes
and restored epochs, maximum-input resource measurements, runtime attempt
ownership/routing, durable suffix transfer, and browser/CLI reselection remain
outside this completed component scope. The dashboard goal remains active and
no release was published.

### 2026-09-24 — Connected original-file recovery and operation continuation

Original-file recovery now runs through normal application startup, authenticated
HTTP routes, the public SDK and the embedded dashboard. The runtime owner joins
verification/transfer work before storage closes, uses encrypted disposable
staging, and preserves the original operation and accepted ciphertext across
restart. Six bounded API operations expose attempt creation, observation, source
parts, verification, suffix transfer and disposal. Fresh authority checks guard
admission and output, and no restoration or proof step executes provider work.

The operation page can now reselect the original files after refresh, explicitly
resume only the missing upload, validate the original collection, review its
retained result, and separately confirm activation. It can also request confirmed
cancellation of an operation observed inactive. The cancellation wording preserves
the server's existing concurrent-activation semantics: committed work cannot be
undone. Unknown control outcomes require an explicit original-progress read;
background observations and stale same-ID activation responses do not unlock a
second submission.

The declared base file profile is enforced before upload encryption and again
when staged input is used for validation or fresh, retained, cached or unchanged
execution decisions. Tagged external resource vocabulary cannot be relabeled as
base-profile input. The existing unprofiled contract and normalization bytes are
preserved. Historical incompatible profiled work fails closed; automatic repair
or cancellation is not implemented. Transitive graph validation remains the
compiler's responsibility.

Executed final checks include:

- **489 dashboard tests**, TypeScript, lint, a pinned build, and embedded-asset
  freshness verification.
- Actual Chrome/file-chooser/TLS/Raft scenarios for fresh import, original upload
  recovery after refresh, and explicit original-operation continuation. The
  recovery fixture deliberately loses a real successful resume response, reads
  the original attempt, and observes exactly one resume. Dismissing activation
  confirmation makes no write; later explicit activation applies exactly two
  credential resources. These scenarios pass with Go 1.27 race detection
  (**90.705 s**) and Go 1.25 (**76.911 s**).
- Final runtime/HTTP/restart groups pass with race detection (**29.312 s**) and
  Go 1.25 (**24.049 s**), with unchanged selected source hashes. Main/server vet
  also passes. Tests cover lifecycle joins and cleanup, preserved original
  ciphertext, lost responses and current authorization.
- The full SDK matrix passes default and `externaljobs` builds with both Go
  versions, plus default/tagged race testing. Profile-specific management
  matrices and independent source review have separate linked evidence.

The [runtime contract](collection-reselection-runtime.md) and
[private checkpoint](../../bin/verification/collection-reselection-runtime-2026-09-24/result.json)
record source hashes, commands, failed attempts and their corrections. The
initial TLS-report warning, ambiguous browser heading selector, obsolete staging
test expectation, and reproduced stale-activation response failures are retained.
Independent review covers the original controls, profile enforcement and shared
response guard. Native execution here is Linux amd64 WSL; credential-only
continuation fixtures make no provider operations.

Restarted CLI input recovery remains open: current CLI Apply creates unprofiled
operations, while this recovery protocol requires the explicit base file
profile. A lost create-attempt reply without its ID may also require waiting for
expiry before another attempt. Maximum-input measurements, external-worker
integration, the complete private shipping matrix, production-provider evidence
and the million-monitor/24-hour campaign remain separate gates. No commit, push,
tag or publication occurred, and the overall dashboard goal remains active.


### 2026-09-24 — Restarted CLI input recovery and bounded upload admission

The public SDK now exposes `FreezeProfile` and `Reselect`, shared with
`cpractl apply --file-profile cpra.file.base.v1`, `resume-upload`, and
`get upload-attempt`. Explicit recovery retains the original collection and
already committed input, acquires only required sources, and reports safe
progress handles. Completion means uploaded input; validation and activation
remain separate actions. Ordinary unprofiled SDK creation is unchanged.

Native process testing reproduced a 256-row upload timeout: sequential admission
committed only 245 or 255 rows before the ten-second request deadline. Profiling
identified repeated durable submission costs. Bounded existing Raft envelopes
now admit prepared rows with format-14 prefix/authority fences and unchanged
synchronous writes. Accepted rows survive later failure; chunks are not atomic.
The same 256-row fixture completes in the final race/minimum-Go campaigns without
increasing its deadline. This is correctness evidence, not a fleet benchmark.
The FSM mutex remains owned for each bounded envelope; concurrent-monitor SLOs
need separate measurement.

| Final check | Recorded result |
| --- | --- |
| Full SDK, default/tagged on Go 1.27 and Go 1.25; default/tagged release-toolchain race | Six passes |
| Focused management/persistence upload, profile, ownership, cancellation and recovery matrix | Six passes, plus independent review/race and default/tagged vet |
| Full CLI package, release-toolchain race / Go 1.25 | PASS, 11.683 / 7.479 seconds wall time |
| Native CLI Apply, killed-process recovery and lost replies, race / Go 1.25 | PASS, 90.164 / 79.563 seconds wall time |
| Three connected Chrome/Worker/WASM/TLS/Raft scenarios, race / Go 1.25 | PASS, 80.559 / 70.674 seconds wall time |
| Affected runtime/HTTP/management/persistence scenarios, race / Go 1.25 | PASS, 25.883 / 27.078 seconds wall time |
| Pinned dashboard build, TypeScript, lint, tests and embedded freshness | PASS, 489 tests in 31 files |
| Full management package race | FAILED: ten-minute timeout, 601.045 seconds wall time |

The timed-out suite was executing
`TestStagedValidationLargeNotificationClosureUsesLookups` for 3m54s. Its trace
shows resource decoding in notification dependency validation; it does not
establish full-package race correctness. The failure and exact command are
retained, and the deadline was not raised to conceal it.

Native recovery kills the CLI after 256 of 300 resources commit, gracefully
restarts the server, rejects changed original input and uploads only the 44-row
suffix. Exact committed ciphertext is preserved. A separate fixture loses real
successful source-upload and Resume replies; six distinct mutation paths are
observed once each, with no validation or activation. Browser recovery requires
token re-entry, clears private input and makes no implicit activation; explicit
confirmation continues the original operation.

The [combined checkpoint](../../bin/verification/collection-cli-reselection-2026-09-24/result.json)
records 625 selected source hashes, commands, logs, independent reviews and
retained failures. During the native race invocation only an uncompiled backend
test file changed; an exact `go list -race -deps -test` manifest confirms compiled
sources were stable. The added backend test passed separately, and final
browser/runtime captures include it unchanged. Local documentation targets and
SDK reference generation passed; no documentation-site build is claimed.

Physical C: free space measured 32,064,720,896 bytes (29.863 GiB), below the
30 GiB prerequisite before additional fixture headroom. This dated observation
must be refreshed before a large campaign. External-worker server integration,
maximum-input qualification, private packaging, actual provider evidence and
million-monitor/24-hour execution remain open. The goal remains active. No
commit, push, tag or publication occurred.

### 2026-09-24 — Bounded graph lookups and the private JobType foundation

The prior full management race timeout is preserved. Investigation isolated
repeated authenticated decoding and normalization of provider configuration in
notification graph lookups. Selected immutable graph metadata now retains only
existence and a cloned endpoint driver-name string, charged to the existing
metadata quota. Full resource and resolved-driver validation still runs for each
selected graph. No provider configuration cache, workload reduction or timeout
increase was introduced.

The unchanged 40-endpoint, 900-KiB-per-endpoint fixture took 18.62 seconds before
and 8.00 seconds after in one ordinary profile comparison. Sampled cumulative
allocations fell from 36,760.95 MiB to 15,794.45 MiB; accounted peak
plaintext/scratch remained 31,342,344 bytes. These are workload-specific
allocation and timing observations, not RSS or monitor SLO measurements.

The **full default management race suite passed in 557.230 seconds** under its
original ten-minute timeout (568.729 seconds wall time). All 355 selected
repository inputs remained unchanged. A default-build exclusion test added after
compilation passed separately under race detection and is explicitly identified
outside that compiled snapshot. Scoped default/tagged Go 1.27 and Go 1.25 checks,
vet, and independent review also passed. The
[performance qualification](../../bin/verification/staged-validation-performance-2026-09-24/qualification.json)
retains the original failure, profile data and final source boundaries.

The `externaljobs` build now has a private JobType foundation: a dedicated
encrypted persistence namespace, immutable implementation versions, conditional
management preparation, operator-policy fences, and bounded declarative schema
validation. Default builds retain format 14 and exclude the extension; tagged
builds support format 15 while preserving prior collection sections and command
digests. Startup authenticates retained contracts. Prepared values redact both
pointer and value formatting, and authority is rechecked around key operations.
Schema evaluation uses exact bounded numbers, local acyclic references and
bounded RE2 patterns. The [contract](job-types.md) lists the supported vocabulary
and storage/evaluation quotas.

Final scoped qualification passed:

| Check | Result |
| --- | --- |
| Persistence foundation and compatibility cases, tagged Go 1.27 race / Go 1.25 | PASS, 25.474 / 8.193 seconds package time |
| Default storage exclusion and digest/format cases, race / Go 1.25 | PASS, 3.784 / 2.816 seconds package time |
| Independent tagged schema/management race | PASS, 3.087 seconds package time |
| Tagged schema/management Go 1.25 | PASS, 2.084 seconds package time |
| Default management-method exclusion, race / Go 1.25 | PASS |
| Default/tagged persistence vet and tagged management vet | PASS |
| Application and CLI compilation, default / externaljobs / all built-in drivers | PASS; no runtime execution claimed for these compile checks |
| Release recipe tests | PASS, 20 tests; independent format resolver checks also passed |

Real Raft reopen tests cover snapshot/log state and stopped authentication
revocation; the revoked prepared command cannot commit after restart. Other
tests cover immutable ciphertext, authority expiry during wrapping, quotas,
historical digests, schema budgets, corruption rejection and detached snapshots.
Independent review found three preparation issues; authority checks, copied-value
redaction and pre-marshal metadata bounds were corrected and requalified.

The [private JobType checkpoint](../../bin/verification/job-type-foundation-2026-09-24/result.json)
records 30 final affected-source hashes, commands, logs and preserved fixture
failures. Native execution is Linux amd64 WSL. HTTP registration, runtime
enablement, pinned monitor references, worker grants and execution remain open;
this does not complete shipping Ticket 8 or qualify real-worker interoperability.
Full platform packaging, provider-account evidence and the million-monitor
24-hour campaign remain separate gates. No publication occurred; the overall
dashboard goal remains active.

### 2026-09-24 — JobType HTTP registration and audited configuration receipts

The tagged JobType boundary now reaches the real server: list, create, get,
replace and delete use the existing public SDK contract. Create requires an
absence precondition; replace/delete require the observed resource version.
Only an `externaljobs` build with explicit `external_jobs.enabled: true` registers
and advertises the routes. Runtime enablement requires authenticated management,
Raft and the existing protected transport/encryption setup. Default and ordinary
all-built-in-driver builds cannot express the custom-job configuration type.

Tagged storage format 16 adds authority-bound operation allocation and terminal
configuration receipts. The original encrypted intent, selectors, revision
guards, preparation times and operator policy are fixed before allocation.
Resource mutation and its audit receipt commit together. Unknown allocation
submits no target mutation; unknown target outcomes retain the original handle.
Neither automatically retries. Format-15 command digests and replay remain
supported, while default readers remain at format 14 and reject external state.
Registration completion does not claim worker execution.

List cursors retain one encrypted generation across replacement, deletion and
recreation. They preserve current authority checks, five-minute expiry,
default-100/max-500 rows and an 8-MiB encoded response limit. Snapshot accounting
reserves capacity before capture, shrinks to current-descriptor encoding plus
bounded overhead, and keeps active-reader charges after cursor pruning.
Decryption holds neither the cursor mutex nor the authorization lock. Global
and per-principal byte limits supplement existing cursor-count limits.

| Final check | Result |
| --- | --- |
| Independent actual SDK/HTTP and snapshot lifecycle race | PASS, 17.024 seconds package time |
| Tagged schema/management race | PASS, 3.130 seconds |
| Go 1.25 tagged management / HTTP | PASS, 2.407 / 6.560 seconds |
| Shared cursor/observation/operation-read race, default / tagged | PASS, 4.558 / 4.717 seconds |
| Persistence operation/format/recovery scoped race, tagged / default | PASS, 37.779 / 11.151 seconds |
| Same scoped persistence cases on Go 1.25, tagged / default | PASS, 19.023 / 7.189 seconds |
| Normal main TLS/Raft default/tagged, Go 1.27 ordinary/race and Go 1.25 | Six passes |
| Runtime configuration matrix, all-built-in exclusion and negative compile probes | PASS |
| Affected vet, all-built-in application/CLI compilation and release recipe tests | PASS; 20 release tests |

Native tests cover retained ciphertext and operation receipts after restart,
failed CAS, revoked/expired authority, explicit restore, allocation/commit reply
loss, and exact original-operation reconciliation. SDK tests use real TLS/Raft
handlers. A 70-resource fixture with approximately 120 KiB of schema per resource
crosses the response byte limit and traverses two pages without losing or
duplicating IDs. Blocked-page tests prove cancellation, revocation, expiry and
concurrent reads preserve snapshot ownership and resource accounting.

The [combined private checkpoint](../../bin/verification/job-type-http-2026-09-24/result.json)
records 38 final affected-source hashes, commands, logs and independent review.
Initial implementation/test fixture failures are retained. Two source captures
included metadata for dependency tests that were not compiled into their tested
packages; their original records remain, with separate compiled-input boundaries
showing unchanged actual inputs. The storage tests that changed were then
qualified separately. Native execution is Linux amd64 WSL; no whole-tree suite,
new dashboard build, or documentation-site build is claimed for this phase.

The [JobType contract](job-types.md), SDK status/README and
[next server boundary](external-worker-server-next-steps.md) identify the remaining
work: committed worker grants, pinned external configuration references,
assignment/start permission, results/receipts, late evidence and separate-process
handler qualification. Shipping Ticket 8 and the overall dashboard goal remain
active. Provider-account, platform packaging and million-monitor/24-hour gates
are still outstanding. No commit, push, tag or publication occurred.

### 2026-09-24 — Worker policy, local provisioning and authentication

Tagged persistence format 17 adds committed worker identities, credential and
grant revisions, scoped JobType references and audit events. Provisioning requires
exclusive stopped administration and current named operator authority. The local
`worker-auth` commands issue, rotate, revoke and reprovision credentials, update
grants, and inspect policy. Tokens are written to protected files outside the data
directory; committed state contains only their verifiers. Restored state requires
explicit reprovisioning and does not reactivate previously revoked identities.
Default builds remain at storage format 14.

The tagged HTTP authentication adapter validates the request origin, operation
and current committed worker credential. It is separate from management
authorization and from permission to start work. It does not register execution
routes. Non-revoked worker grants, including expired credentials, prevent deletion
of their referenced JobType. Real SDK/TLS/Raft tests verify the 409 response,
retained failed operation receipt, restart, explicit reference removal and a
subsequent successful deletion.

| Final verification | Result |
| --- | --- |
| Authentication, local administration and CLI, Go 1.27.1 race / Go 1.25 | PASS; 21.883 / 16.352 seconds wall time |
| Scoped persistence, tagged race / Go 1.25 | PASS; 36.729 / 18.819 seconds package time |
| Scoped persistence, default race / Go 1.25 | PASS; 7.896 / 6.209 seconds package time |
| All 13 JobType server contract tests, race / Go 1.25 | PASS; 21.417 / 10.094 seconds package time |
| Worker exclusion, both compilers | PASS; default and all-built-in builds reject tagged symbols; route, schema and CLI checks pass |
| Affected vet, release Python tests and CI actionlint | PASS; 31 Python tests |

The [private checkpoint](../../bin/verification/worker-policy-2026-09-24/result.json)
records commands, logs, source hashes, review and the preserved intermediate
fixture failure. Verification uses native Linux amd64 WSL execution. The new CI
steps are locally validated; a remote workflow run is not claimed. The
[operator instructions](../worker-authentication.md) describe grants, credential
files, rotation, revocation and restore handling.

Ticket 8 remains active. External configuration references, worker sessions,
assignment/start/heartbeat/result and late-evidence processing, controller
dispatch and real worker/server process qualification remain unfinished. No
worker execution, whole-repository qualification, dashboard/site rebuild or
release publication is claimed by this checkpoint.

### 2026-09-24 — Immutable JobType configuration references

Tagged format 18 adds canonical JobType references to encrypted catalog records.
The reference identifies the original incarnation, version, immutable revision
and category. Monitor checks, recovery, inline notifications and notification
endpoints share this contract. Apply rechecks the reference and prevents deletion
while an active source holds it. Source deletion releases current references;
retained encrypted pages still authenticate their historical contracts after
JobType recreation. Custom parameters and worker-local credential selectors
remain encrypted. External drivers cannot query CPRa-held Credential resources.

Strict decoding now distinguishes an omitted extension from explicit null or
empty fields when enforcing older storage formats. Empty format-18 tombstones
retain their format requirement without retaining an active dependency.
Historical operation digest fixtures remain unchanged.

The [private reference checkpoint](../../bin/verification/job-type-references-2026-09-24/result.json)
records scoped persistence, management, SDK/server regression, race, Go 1.25,
vet and build-exclusion results. Independent review covered reference lifetime,
credential separation, atomic deletion, retained pages and raw-format gates.
Two compiler runs each execute 15 tagged-positive and 30 exact negative symbol
checks, plus schema, route, CLI and dependency checks. Initial test-fixture
failures and source-drift runs remain distinct from final qualifying records.
CI now includes the new tagged reference tests and compiler probes.

Public external configuration admission is still closed pending the runtime
adapter and protocol. The [session proposal](worker-session-protocol.md) describes
the next implementation: process-bound sessions, bounded Poll replay and a
non-executing Start reconciliation mode. It has no implementation evidence yet.
Ticket 8, SDK/worker interoperability and the overall shipping goal remain open.
No dashboard/site build, full repository qualification or publication is claimed
for this phase.

### 2026-09-24 — Worker sessions and journal identity

The storage package is `internal/persistence` (`package persistence`). Current
Go sources have no imports or declarations using the former package name, and
the native Windows test script uses `cpra-persistence-windows.test.exe`.

Tagged format 19 now commits one active polling session per worker UID, with an
owner epoch, restore-qualified server identity, frozen capabilities and exact
request replay. A new accepted sequence renews the two-minute idle expiry;
replay does not. The namespace is bounded to 1,024 sessions and 16 MiB of
accounted state, with 64 capabilities per session. Historical formats and frozen
digests remain supported. Stopped provisioning reports `protocolServerID`.

The session HTTP adapter authenticates before reading input, enforces bounded
body reads and per-worker/global admission, and maps storage failures to safe
problem responses. Real TLS/SDK/Raft tests use a dedicated mux. **Normal startup
still registers no worker execution routes.** Assignment dispatch, authoritative
Start, heartbeat, result and late-evidence server processing remain outstanding.

The SDK now validates session and execution identity echoes and typed Start
dispositions. Worker journal format 2 binds the provisioned UID and original
server/session to encrypted records. Only the original live `begin` may invoke a
handler; recovery sends `reconcile`, retains pending reservations and preserves
unknown outcomes. Explicit expiry changes the client nonce; ordinary response
loss reuses the exact request. Format-1 journals are rejected without deleting
records or replacing the wrapping key.

Review also fixed a stale flush-inventory race that could overwrite a newly
saved handler outcome, expiry-renewal validation, duplicate-lease validation,
escaped-identity reservation accounting and request-body deadline enforcement.
The [checkpoint](../../bin/verification/worker-sessions-2026-09-24/result.json)
links exact source manifests and completed Go 1.27.1 race, Go 1.25, generation,
build-exclusion, SDK/example and tooling checks. Independent source reviews
cover the HTTP, SDK, worker and persistence boundaries.

A full default persistence race run exceeded the checkpoint runner's five-minute
limit. Its active collection-publication regression subsequently passed in
isolation in approximately three minutes. The normal Make test allowance is
20 minutes; this checkpoint does not claim that full suite passed. Broader runs,
initial fixture failures and pre-fix evidence remain recorded separately from
qualifying scopes.

Native evidence is Linux amd64 on the current WSL filesystem. Session storage
tests exercise real Raft snapshots and stopped restart/restore; separate worker
tests force process termination around journal/handler boundaries using protocol
fixtures. The DAO example uses local RPC/SMS fixtures. These checks establish
neither production-server execution interoperability nor live-provider effects.
Ticket 8 and the overall goal remain active. No commit, push, tag, release,
module, image, chart or documentation site was published.

### 2026-09-24 — Queued external execution and private preparation

Tagged format 20 adds bounded, encrypted queued execution records for checks,
recovery and notifications. Admission checks original monitor/control/dependency
identities, immutable JobType versions, action eligibility and exact notification
endpoint positions. Exact retries preserve the original record; conflicts fail
without a lifecycle transition. Ready lookup is indexed and excludes expired or
old-owner work. Retained execution references prevent JobType deletion. Default
builds have neither this namespace nor its public Go symbols.

Authenticated catalog parameters now flow through assignment preparation,
committed intent, ready lookup and authenticated reopening in focused tests.
Result preparation validates the original contract and preserves distinct
accepted/completed/delivered/rejected/unknown/noData classifications before
encrypting diagnostics and supplemental evidence. Neither helper grants execution
permission or produces a receipt. Cancellation during sealing makes no mutation.

Tagged inert runtime descriptors preserve original source and slot identities.
Explicit entities preparation keeps them separate from local jobs; mixed
notification lists retain their ordinals through copy and Ark installation. The
generic nil-slot compaction defect in JobStorage.Copy is corrected. Ordinary
preparation and public configuration validation continue rejecting unsupported
external execution.

The [admission checkpoint](../../bin/verification/worker-admission-2026-09-24/result.json)
links focused Go 1.27.1 race and Go 1.25 storage/management checks, controller and
schema checks, compiler exclusion probes, and the exact selected source hashes.
Storage replay tests exercise actual Raft logs and completed snapshots followed
by stopped reopening. They do not force termination or invoke remote handlers.
Independent reviews cover storage admission, descriptors/entities, and protected
assignment/outcome preparation. Intermediate compilation failures during shared
edits and the initial fixture failures remain separate from qualifying runs.

Start/heartbeat/result/late-evidence processing, offer allocation, terminalization,
explicit noData finalization, remote capacity accounting and owner-loop dispatch
remain incomplete. Expired/old-owner intents still retain capacity, so dispatch
and public worker execution routes remain closed. Ticket 8 and the full dashboard
shipping goal remain active; no full-repository, native-platform, live-provider,
endurance or publication claim is made by this checkpoint.

### 2026-09-24 — Worker offers, Start authorization and startup verification

Tagged format 21 connects encrypted queued intent to bounded committed offers
and one executable Start grant. Reconciliation never grants execution; duplicate
begin returns original started/unknown state. Exact-lease rejection is committed
before releasing a worker reservation. Remote action ownership cannot be resolved
through local process-fence evidence. The management projection decrypts only a
verified poll batch and checks current authority/controls again before returning it.

Startup now authenticates every retained execution payload and original parameter
contract, including expired and previous-owner records. The private TLS adapter
uses the actual SDK and Raft path for offer replay, pending/granted reconciliation
and lost Start replies. Public route registration and controller dispatch remain
closed. See [implementation details](worker-offers-and-start.md) and the
[verification checkpoint](../../bin/verification/worker-offers-2026-09-24/result.json).

Ticket 8 remains active. Heartbeats, authenticated result commits/receipts, late
evidence, expiry/noData finalization, active dispatch and separate-process runner
interop remain required. No candidate source or artifacts were published.

### 2026-09-24 — Naming cleanup and source checkpoint

Implementation is paused at the user's request. This source checkpoint includes
the accumulated dashboard/API/SDK work and the approved package renames. The
unreleased SDK compatibility client and `internal/client` have been removed;
`cpractl` uses the current SDK. Authenticated v2 health/readiness probes also work
in token-only deployments. A complete symbol-by-symbol naming review remains a
separate approval step.

Verification after the naming changes passed application/CLI builds, the root
application and HTTP server suites, SDK default/tagged and worker tagged suites
and race checks, Go vet, all-driver/tagged compilation, dashboard build/type/lint
checks and 489 dashboard tests. Private archive consumer verification passed with
`GOWORK=off`; this is not downloaded public-module evidence. SDK qualification
details remain local in `bin/verification/naming-sdk-2026-09-24/qualification.json`.
The full persistence suite exceeded an imposed five-minute limit and is not
claimed as passing; focused restart, snapshot, locking, crash and backup checks
passed. Live Compose, Helm and browser campaigns were not rerun for this rename.

Remaining work includes the external-worker server/runtime integration listed
above; final dashboard, security, accessibility and cross-consumer qualification;
complete candidate compiler/tag/race/vulnerability checks; native package/service,
Compose and Helm qualification; all-provider account evidence; and the baseline
comparison and million-monitor 24-hour campaign. The campaign still requires a
fresh physical-host free-space check and at least 30 GiB plus fixture headroom.
Final documentation and artifact publication remain gated on that evidence.
Pushing this development branch does not qualify or publish a release candidate.

### 2026-09-24 — A2A added to remaining work

The user requested an A2A intervention job type for delegating work to agents,
plus an A2A notification type for incident escalation. The example is a Docker
restart intervention failing and CPRa asking an agent to investigate. The
[shipping backlog](dashboard-shipping-plan.md#remaining-addition-a2a-agent-intervention-and-notifications)
records both roles, failure-versus-unknown handling, bounded task correlation,
authorization and operator-control requirements, and integration/verification
work. Protocol and build-placement decisions remain open. This is a planning
addition only; implementation remains paused.
