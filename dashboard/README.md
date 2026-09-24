# CPRa dashboard development

The dashboard uses the same v2 resource contract as the Go SDK and CLI. It also
keeps the existing v1 observation views for older servers. Notification recipients
are contacts, not dashboard accounts. A monitor's Code selects the actual delivery
driver; recipient and group references supply matching destinations.

An incomplete operation using the `cpra.file.base.v1` file profile can resume from
its operation page after token re-entry and selection of the original files.
`CollectionReselection` holds `File` references only for its mounted session and
streams at most 1 MiB per authenticated request. Raw source contents, including
comments, are sent to the same server; local names and paths are not sent. The
server verifies both original input commitments before allowing an explicit
resume of missing resources. Verification/upload do not activate configuration.
Lost mutation replies require a progress read, not an automatic retry. See the
[runtime contract](../docs/implementation/collection-reselection-runtime.md).

Run frontend commands with the Node and pnpm versions recorded in
`scripts/release/recipe.json` and the existing frozen lockfile. From this folder:

```sh
pnpm api:generate
pnpm api:check
pnpm exec tsc --noEmit
pnpm lint
pnpm test
```

`scripts/dashboard/generate_api.py` is a repository-owned, dependency-free
generator. It reads the canonical base OpenAPI models, HTTP contract and operation inventory,
plus the reviewed `api-generation.json` form mapping. The generated file records
input and generator hashes. `--check` never modifies files; the build checks it
before compilation. The script is covered by two-root/no-Git reproducibility tests:

```sh
python3 -m unittest discover -s ../scripts/dashboard -p 'test_*.py'
```

Generated TypeScript describes wire values and optional-field presence. It does
not replace server validation of references, lengths, constraints, authorization,
or driver configuration. Only the base contract is generated; the external-worker
overlay and custom-job authoring are absent from the dashboard. Do not edit
`src/api/generated.ts` manually or copy executable provider dependencies into the
frontend. Update the reviewed input mapping when protected field handling changes,
and keep its server-side credential-binding policy synchronized independently.
Required creation headers also come from this contract: resource creation sends
`If-None-Match: *`; conditional edits/deletes use a strong `If-Match` validator.
Secret merge patches include only changed editable metadata and spec fields.
Removed labels use JSON nulls, while immutable identity fields stay out of patch
bodies and the observed version remains frozen in the precondition.

`src/api/session.ts` holds the bearer token in a private field in the current tab.
Management requests require this origin and HTTPS, reject redirects, carry the
observed version as a strong `If-Match`, and do not automatically retry writes.
`SessionBoundary` removes data and drafts on logout or identity replacement and
rejects late responses from a previous identity. Existing browser Basic credentials
are used only for same-origin read-only discovery/legacy reads.

The fleet and numeric `/monitors/:id` routes remain observation views. Saved
resources use `/monitor-configurations/:id`, and the fleet/detail pages link there
using the stable monitor ID. Creation uses `?create=1` on resource list routes,
so a valid resource ID named `new` never collides with the create screen.

Resource pages use full detail reads before editing. A draft freezes its resource
version and incarnation; polling never replaces its values. A conflict offers an
explicit latest-resource comparison. It does not automatically rebase the draft.
An unconfirmed mutation disables repeat submission until the operator reconciles
the original resource or operation. Saved and controller-applied results remain
different states.

Use `useManagementMutation` for explicit writes. Request bodies are not placed in
TanStack's mutation cache or offline queues. Resource pages sanitize response data
before caching or showing feedback. Secrets accept values only in their write-only
editor; the input is cleared before sending. Driver forms contain credential
reference selectors for protected fields, including webhook URLs and headers.
They never retrieve a secret to fill a form. A headers credential contains a JSON
object with string values; other notification credential slots contain text.

Resource and reference lists load one bounded page at a time, and inactive pages
are removed from the query cache. Once a result has multiple pages, its snapshot
stays fixed while browsing; an explicit refresh starts current observations.
This prevents background polling from accumulating retained server snapshots.
Forms are derived for all fourteen check, five
recovery, and fourteen notification drivers and show the server's reported compiled
capabilities. Monitor fields include timing, thresholds, optional recovery, enabled
state, labels/tags, scheduled or recurring maintenance, and Code notification rules.
Typed Codes select `notifyType` plus recipients or a group. Existing legacy inline
drivers and direct endpoints remain explicit and unchanged unless the operator
chooses conversion. Optional dispatch, false/zero values, empty lists, durations and
existing credential references retain their wire meaning.

HTTP targets with userinfo, queries or fragments use a URL credential reference.
HTTP headers/bodies, database passwords and connection strings, and recovery
webhook URLs/headers/bodies also use credential references. Arbitrary supported
driver fields may opt into a secret reference; only the reference ID enters an edit. Unknown or legacy inline-secret configurations
remain observable through safe fields, but cannot be blindly rewritten.

Operations use `/operations/:id`; `/operations` provides a bounded list and an
exact-ID lookup. `OperationsList` loads at most 100 receipts per page with an
optional exact monitor filter. Each continuation uses its original snapshot;
refresh starts a new first page. The query retains one page, does not poll or
accumulate cursors, and rejects a changed snapshot or a non-advancing cursor.
`ListOperations` and `GetOperation` permissions independently govern browsing and
opening a receipt. Missing counts or observation times remain unavailable.

The list includes shared resource/control receipts and the current named
operator's collection receipts. Collection rows preserve their original inventory
and upload count even when no application outcomes exist. Their identity format,
input digest and item count stay distinct from resource versions and result IDs.
An unfamiliar identity format remains a read-only observation; the dashboard does
not construct a result request from it.

`CollectionValidation` reads the retained result only after the operator clicks
**Read original validation result** and only when `GetOperationValidation` is
permitted. It retains one page of at most 100 rows and pins subsequent pages and
refreshes to the original inventory and sealed summary. The retained view uses
source tokens and document/item positions; it does not reconstruct local filenames
or fetch resource bodies. Stopping a read, hiding results or leaving the page does
not change the server operation. Sign-out, identity replacement and denied detail
access remove displayed results and discard late responses. Pending validation,
rejection, interruption, expired staging and unavailable history stay distinct.
The current operation receipt and its older retained validation result can have
different phases: an expired upload can still have a readable original verdict.

The `/import` page prepares selected YAML/JSON files in a private Worker using
the shared Go collection parser compiled to WASM. Preparation validates the whole
selection before any server request. Source names remain local display data;
requests use deterministic source tokens and document/item coordinates. Input,
admission tickets and credentials stay out of browser persistence and query caches.
Closing the import, signing out or replacing the identity terminates its Worker.

Server preview, inactive creation, upload and validation are separate explicit
steps. `POST /api/v2/operations/{id}/validate` returns **202** with the original
operation; it does not return a verdict. Waiting reads only
`GET /api/v2/operations/{id}/validation`, at least five seconds apart and honoring
a longer server interval. A pending response must identify that original operation.
Lost submission replies never trigger an automatic second POST. Stopping a wait
does not cancel server work.

Sealed validation results use bounded pages and preserve their original result
identity, summary and source ordering. Pending, rejected, interrupted, canceled,
expired and unavailable-history responses have different meanings. A successful
result is followed by an authoritative operation read before another step can be
enabled. If that read fails, the result stays visible and mutations remain
disabled. Result reads have a 4 MiB ceiling, further reduced by the session's
configured response limit.

`CollectionOperationControls` continues the same uploaded operation after a
refresh or upload recovery. Validation requires a complete upload under the
supported file profile. Activation first reads the retained validation summary
and a fresh receipt, then presents a separate confirmation with the original
operation ID and resource count. Confirmation refreshes progress again before
submitting. The server conditionally applies each resource; a successful preview
does not promise an atomic collection commit.

An uncertain control response requires an explicit progress read before another
submission. Cancellation is offered for an operation observed inactive, but it
can stop remaining work if activation begins before the server receives it.
Cancellation does not reverse committed changes or external actions. Unprofiled
collections retain reads and inactive cancellation; use the compatible SDK or CLI
for their validation and activation. Unknown identities or profiles remain
read-only. Restarted CLI input helpers, maximum-input measurements and the full
private shipping gates remain tracked in the
[implementation progress record](../docs/implementation/dashboard-implementation-progress.md).

Resource write responses link the
server's original receipt, including when headers arrive before an interrupted
response body. A returned resource's status flag does not prove that its change
was applied. Receipts distinguish reserved, committed, completed/applied, failed, and
partial/superseded outcomes. Polling honors the longer of five seconds and the
server's retry interval; terminal outcomes, invalid responses and denied,
missing or expired receipts stop automatic polling. Transient availability
failures remain eligible for bounded polling. Leaving the page stops waiting,
and never cancels or repeats
a mutation. An operation address survives refresh, while signing in again is
required to read its data. Only identities, versions, counts and typed outcomes
enter receipt state; arbitrary messages and resource payloads are removed.

The system view uses the existing read API for queue saturation (depth divided by
reported capacity), worker bounds, pending results, sizing model and latency
feedback. Historical dequeued waits are not presented as the age of queued work.
SLOs show separate scheduling/queue, execution and total p50/p95/p99 estimates,
exact threshold counts, expected/pending work and measurement coverage gaps.
Missing measurements stay unavailable, and no SLA or recovery-delivery claim is
inferred from configuration operation receipts.

Intentional monitoring pauses show current disabled-or-snoozed monitors and elapsed
monitor-seconds in the rolling window. Two monitors paused for one second contribute
two monitor-seconds; overlapping disable and snooze count once. These are intervals
observed by the controller. They add no successful checks and do not erase earlier
misses. Persisted exposure survives restart while unobserved coverage stays unavailable.

Monitor detail includes exact-version controls. Acknowledge records the named
operator and optional note while monitoring continues. Dismiss requires a reason
and pauses notifications for that incident. Snooze requires a reason and a positive
duration up to 30 days and pauses new checks, notifications and recovery. Disable
and enable patch only `spec.enabled`; ending a snooze never enables a disabled
monitor. Drafts keep their original incident/control/configuration version even
if observations change while a dialog is open.

Request recovery uses the saved control version and a required reason. It runs
the configured recovery only after the server validates current observed unhealthy
state, enabled/snooze/maintenance conditions, active or unresolved work, cooldowns
and attempt limits. The button does not bypass those constraints or add a check-now
operation. Its operation receipt confirms admission and owner application; the
separate action observation reports what happened at the provider.

Action outcomes use the authenticated v2 list/get contract with an exact monitor
filter. Opening a review fetches the latest action and freezes its review version
and original monitor incarnation. Inconclusive records an investigation and
retains the hold. Accepted/rejected record operator assertions separately from the
original provider outcome. A conclusive review requires a stopped or fenced
executor and cannot contradict recorded provider evidence. It never replays the
operation. Later contradictory evidence remains visible and restores the hold.
Reviews require a reason, allow an optional note, and accept up to eight distinct
evidence references. References are rendered as text and never fetched. Provider
secrets belong in Credential resources, not in audit notes or references.

The v2 timeline renders named actors, reasons, notes and evidence references from
retained events. A v2 denial or unavailable history never falls back to a less
restrictive v1 read. Legacy servers keep the explicit read-only timeline. Successful
raw checks are not stored as event history.

The component/request tests use controlled HTTP fixtures. Passing them does not
prove durable server admission, real provider delivery, restart recovery, or native
browser accessibility. Those remain separate integration/release gates.

An additional opt-in fixture in `internal/httpserver/management_browser_test.go`
serves the newly built `dashboard/dist` against actual Go API/authentication/CORS
handlers over TLS and a real single-node Raft store with encrypted catalog data.
It launches `scripts/dashboard/verify_browser.cjs` using explicitly supplied,
already-installed tools. It does not download browser packages, replace API
responses, execute providers, or alter the embedded release assets. The temporary
browser process adds a certificate exception only for the fixture's public-key pin.

Run it from the repository root after building the dashboard, using the explicit
development Go workspace when the SDK modules are still unpublished:

```sh
python3 scripts/sdk/workspace.py --output /tmp/cpra-browser-work/go.work --no-github-env
GOWORK=/tmp/cpra-browser-work/go.work GOTOOLCHAIN=local GOPROXY=off \
  CPRA_BROWSER_MODULE=/absolute/path/to/existing/node_modules/playwright \
  CPRA_BROWSER_NODE=/absolute/path/to/existing/node \
  CPRA_BROWSER_EXECUTABLE=/absolute/path/to/existing/chrome \
  go test -count=1 -run '^TestManagementBrowser$' -v ./internal/httpserver
```

The fixture checks real resource creation, credential metadata patching, secret
references, version conflicts, referenced deletion rejection, reader permissions,
identity replacement, conditional monitor deletion and operation reconciliation.
It also checks that the synthetic secret does not appear in read responses,
browser storage, request URLs or durable files. Report its actual browser/tool
versions alongside results; installed-browser execution does not establish other
platforms' support or a full accessibility audit. The fixture deliberately runs
without normal application bootstrap or controller projection, so receipts remain
committed or become superseded. It cannot qualify those lifecycle paths, restart
recovery, provider delivery or a complete production release.

Dashboard assets are embedded into Go via `make dashboard-build`; regenerate them only when
the complete frontend candidate is ready for review.

Normal application-entrypoint qualification is also available, using the same
explicit installed browser paths. Rebuild the embedded assets first, then run:

```sh
go test -count=1 -timeout=240s -run '^TestMainManagement(Controls|RecoveryReview)Browser$' -v .
```

These opt-in tests serve the embedded SPA through normal TLS startup, encrypted
Raft and the controller. They exercise actual local HTTP checks and the compiled
webhook recovery driver against designated disposable local targets. The action
test restarts normal startup and checks that the recorded review and original
unknown provider outcome survive without another recovery invocation. It writes
a result and operator screenshot under `bin/verification/main-browser-actions`.
The normal entrypoint runs inside the Go test process; service/package execution,
production-account verification and endurance remain separate evidence gates.

The same installed browser environment can exercise the complete implemented
collection-validation path:

```sh
go test -count=1 -timeout=240s -run '^TestMainManagementCollectionBrowser$' -v .
```

This test uses the real picker, embedded Worker/WASM, TLS handlers and Raft store.
It stages a monitor and referenced credential, submits validation once, checks
source-attributed results, and compares the original result through the SDK before
and after normal-owner restart. It also checks invalid-final-file behavior,
sign-out cleanup and reader denial. The target receives no requests and active
resources remain unchanged. Its report qualifies this bounded validation path;
it does not qualify activation, large imports or a production release.
