# CPRa review repairs and full release qualification

Approved: 2026-10-12. Implementation branch: `codex/release-repairs`.
Baseline: `2ba7b73f0c9d398bfee2fbcf3896f70ccc1fb37c`.
Status: implementation in progress; qualification and publication remain gated.

## Summary

Repair all applicable findings from the three reviews, then qualify and publish
the first **`v0.1.0-rc.1`** release of CPRa, its Go SDK, worker module, and Helm
chart. This is an addendum to the approved API, SDK, dashboard, and
[shipping plan](dashboard-shipping-plan.md). Track individual findings in the
[review inventory](release-review-findings.md) and execution in the
[status record](STATUS.md).

Start from **`2ba7b73`** on a fresh `codex/release-repairs` branch. Preserve the
existing candidate, other reviewers' worktrees, retained evidence, and separate
documentation checkout.

The selected decisions are:

- Recommended first run: **automated local management initialization**, including
  protected credentials and localhost TLS, with an explicit browser trust step.
- CPRa-authored artwork: **CC BY 4.0**; application code remains MIT and third-party
  notices remain intact.
- gRPC: add an explicit **health-check mode** while keeping TCP reachability for
  existing manifests that omit the mode.
- Complete native, provider, packaging, and comparative-performance qualification
  before publication.
- **The 24-hour endurance run remains cancelled.**
- Keep candidates, qualification logs, artifacts, module staging, and images
  access-controlled while gates remain open. Public-repository CI artifacts are
  not private staging.

Every implementation ticket requires focused regression tests, relevant passing
build/lint/type checks, updated documentation, and independent review. Complete
release matrices run against the final frozen commit, rather than being
attributed to it from an earlier checkpoint.

## Implementation sequence

### 1. Establish the repair baseline

- Save this approved plan and connect it to the existing shipping plan and status
  record.
- Create a finding inventory covering every item in the attached reviews:
  reproduced, source-confirmed, corrected, intentional behavior, or awaiting
  reproduction. Record the reviewed commit and evidence for each disposition.
- Preserve reproductions for stale notifications, premature restored-health
  wording, ambiguous HTTP delivery, permissive configuration, collection
  admission failure, and collection-dependent restart growth.
- Recheck remaining claims about idle CPU, rendered headings, API coverage, unused
  alert code, and platform storage behavior before changing those areas.
- Treat existing worker integration, token rotation, dependency-notice collection,
  pool-demand refresh, and the 45-minute test timeout as implemented behavior
  requiring regression coverage.
- Preserve current package names. Do not reset storage formats or remove approved
  collections/worker features.

**Acceptance:** every review finding has a disposition; failing baseline
reproductions remain distinguishable from passing checks and unsupported claims.

### 2. Repair incident and notification correctness

- Bind notification intent to a stable monitor incarnation, notification episode,
  incident when applicable, endpoint identity, and logical delivery identity.
  Attempts retain the original logical identity.
- Apply cooldown to repeated notifications within the same episode. Suppress
  repeats rather than scheduling them for later. A new outage or distinct
  severity transition receives its first eligible notification immediately.
- On confirmed recovery, commit cancellation of obsolete **queued** notifications.
  Preserve started, completed, and unknown actions and their evidence. Recheck
  episode/configuration eligibility before committing a start.
- During maintenance, continue health observations but suppress new notifications
  and recovery admission. Cancel unsent notifications covered by suppression; do
  not release accumulated alerts after maintenance ends.
- Render intervention completion as **"health verification pending."** Emit
  restored-health wording only after the configured successful-check threshold.
  Cyan is not required to follow a red notification.
- Preserve a bounded, sanitized failure diagnostic. Store a cause code and safe
  message in current state and relevant incident/action events; expose them
  consistently through notifications, API, SDK, CLI, and dashboard. Do not retain
  raw check history or provider response bodies.
- Replace blanket HTTP `5xx` retry classification with provider-specific rejection
  classification. Unknown effects remain held. Proven retryable rejection may use
  the existing maximum of three attempts.
- Persist one overall deadline across attempts. Ordinary HTTP notifications
  default to **45 seconds**, with each HTTP call bounded by the smaller of ten
  seconds and remaining time. A2A and external workers retain their configured
  overall task/execution budgets.
- The controller remains the retry owner for built-in notifications. Driver
  transports must not add another replay layer. Provider deduplication identifiers
  remain stable across attempts.

**Acceptance:** second-outage tests deliver no stale red after recovery;
maintenance emits no deferred alerts; an effect followed by HTTP `500` produces
one invocation and an unknown hold; no message claims restored health while
verification remains incomplete. These conditions survive restart and
configuration changes.

### 3. Repair collection admission and continuation

- Separate routine health observation, temporary contention, unresolved-write
  recovery, leadership unavailability, and permanent storage failure.
- Publish immutable operational health information for routine readiness checks,
  avoiding the hot FSM lock. Keep authoritative mutation/start guards and
  unresolved-write reconciliation intact.
- A pure health-observation timeout must not set unresolved-write recovery. Actual
  uncertain writes must still close unsafe admission until reconciled. Sustained
  overload remains eligible for bounded backpressure.
- Process contiguous upload commands for the same operation through a single
  synchronous ledger transaction for their accepted prefix. Preserve per-command
  validation, quota accounting, fences, results, replay identity, and
  accepted-prefix semantics.
- Limit server-side application groups to **64 resources or 1 MiB**, while
  retaining the public upload limit of 256 resources or 4 MiB. Do not disable
  durable synchronization.
- Emit the existing machine-readable admission marker with `admissionUnavailable`
  only where the server proves that the particular request was rejected before
  submission. Include `Retry-After` and the original operation handle when
  appropriate.
- Extend SDK classification to recognize that complete contract. Lost, malformed,
  contradictory, or post-admission failures remain ambiguous.
- Add **`cpractl resume operation/ID -f …`** for explicit continuation through
  validation and activation of the original collection. It verifies original
  source/content identities and reconciles progress before proceeding.
- Keep `resume-upload` limited to missing uploads. Keep ordinary SDK mutations
  free of automatic retries.
- Dashboard operation views show the failed phase, original handle, and safe
  explicit continuation. No background reactivation or creation of a replacement
  operation.

**Acceptance:** ordinary 35–50-monitor imports complete; slow-upload tests
distinguish busy observations from genuine recovery; interruption at each phase
retains a usable original handle; continuation produces no duplicate activation.

### 4. Bound collection storage across restarts

- Integrate generation retirement with Store ownership, a durable generation
  catalog, and reference-counted reader/snapshot pins.
- Retain the current completed generation, the previous completed generation, and
  all pinned generations. Retire older reconstructable generations only after
  recovery and publication of the new generation succeed.
- Use bounded scanning and retirement steps outside controller/FSM locks. Retain
  the existing 25 ms cooperative step budget; report filesystem operations that
  exceed it rather than claiming they can be forcibly cancelled.
- Implement native filesystem identity and safe-removal handling for Linux,
  macOS, and Windows. Refuse symlinks/reparse points, unexpected entries, identity
  changes, or unverifiable candidates. Do not substitute recursive deletion for
  failed verification.
- Use a **64 MiB initial mapping for new Windows collection ledgers** rather than
  quota-sized file mapping. Keep synchronous writes and test growth while frozen
  snapshot transactions exist. The pinned bbolt implementation expands the
  writable Windows file to its mapping size.
  [bbolt Windows mapping implementation](https://github.com/etcd-io/bbolt/blob/v1.4.3/bolt_windows.go).
- Preserve complete stopped backups, including generation metadata, Raft state,
  snapshots, and retained history. Backup authentication and restore identity
  handling remain required.
- Expose active, retained, pinned, retired, and cleanup-blocked storage accounting
  without returning filesystem error paths through HTTP.

**Acceptance:** repeated collection-use/restart cycles stabilize after cleanup;
file-manifest and empty-catalog runs do not create unnecessary ledgers; pinned
snapshots survive concurrent cleanup; interruption during retirement safely
resumes; native Windows footprint and growth are measured.

### 5. Make access secure and usable

- Extend **`cpractl local init --management`** to generate a named operator token,
  its hash policy, wrapping key, localhost certificate material, runtime settings,
  and starter configuration using Go code.
- Use platform-standard paths and restrictive native permissions. Keep keys
  outside the state directory. In system installations, keep the operator bearer
  outside service-owned configuration/state and require an explicit protected
  output path.
- Initialization never prints bearer values, overwrites existing files, silently
  converts an existing store, starts a service, or installs trust roots. Render
  the exact server/CLI commands and document manual browser trust.
- Service arguments must not pass the named operator bearer as a read-only
  bootstrap token.
- Add non-sensitive access-mode discovery so the dashboard and CLI distinguish
  management from observation mode. Authentication failures remain `401`; they
  never trigger implicit downgrade of writes.
- Enforce a minimum of **32 printable characters** for presented API tokens;
  generated tokens use 32 random bytes. Document stopped rotation for existing
  short tokens. Length validation must not be described as proof of entropy.
- Warn when supplied bootstrap authentication sources are ignored because
  committed authority already exists. Do not silently replace that authority or
  require old bootstrap files for normal restart.
- Restrict anonymous Host/origin handling to approved loopback identities. Remove
  the profiling `cmdline` endpoint and apply loopback Host restrictions to
  profiling.
- Add bounded failed-authentication throttling and dashboard security headers.
  Use a CSP compatible with the existing parser worker, fonts, and styles;
  prohibit framing, plugins, and unrestricted script evaluation.
- Document that an operator with credential-use/configuration privileges is
  trusted to choose destinations. Do not imply per-credential destination
  isolation or multi-tenant security that CPRa does not implement.

**Acceptance:** a fresh user can initialize, start, authenticate, create a
monitor, and restart successfully; hostile Host/origin requests, weak tokens,
framing, and credential leakage tests fail safely.

### 6. Enforce configuration contracts and repair drivers

- Use shared static validation for file manifests, normalized API resources, SDK
  collection preflight, and driver settings. Reject unknown fields, duplicate
  keys, wrong types, missing required settings, and unsupported compiled/platform
  capabilities.
- Preserve valid manifests and explicit false/zero values. Validate configuration
  without opening storage, constructing operational clients, contacting
  providers, or running jobs.
- Produce source-attributed errors without echoing credentials. A malformed final
  input must cause zero active collection changes.
- Add gRPC **`mode: health`**, using unary `grpc.health.v1.Health/Check` and the
  configured service name. Only `SERVING` is healthy; absent health service or
  unsuccessful RPC is failure, with no TCP fallback. Omitted mode remains TCP
  reachability. New health-mode examples use TLS; cleartext requires explicit
  opt-in. [gRPC health-check contract](https://grpc.io/docs/guides/health-checking/).
- Preserve Docker `timeout` as configured stop grace and add a separate
  `operation_timeout`. Its default is the larger of **60 seconds** and explicit
  stop grace plus 20 seconds. Reject an explicitly insufficient overall budget;
  retain unknown outcomes on interruption.
- Correlate PagerDuty trigger/resolve and Opsgenie create/close with the same
  incident-and-endpoint identity. Map configured severity correctly. Opsgenie
  acceptance remains distinct from completed processing, which its API exposes
  asynchronously. [Opsgenie Alert API](https://docs.opsgenie.com/docs/alert-api).
- Make ICMP `ignore_privilege` select the supported unprivileged mode; reject
  unsupported platform combinations instead of ignoring the setting. Test under
  actual service identities without granting privileges automatically.
- Keep HTTP redirect following and ambient proxies disabled by default. Document
  and test expected-status behavior, including deliberately accepted redirect
  statuses. Do not turn the review into an unrequested global redirect/proxy
  feature.
- Explain clearly that management imports configured bootstrap manifests once;
  subsequent changes use explicit `apply`.

**Acceptance:** every reported typo and incomplete Discord configuration fails
validation; driver contract tests use production paths; configured fields have
observable behavior or explicit rejection.

### 7. Complete operator interfaces and diagnostics

- Implement the existing SDK-promised version and schema-explanation endpoints.
  Generate explanations from the canonical contract and advertise only
  registered, supported operations.
- Keep base/tagged schemas, SDK types, methods, route registrations, and operation
  inventory synchronized. Normal builds continue to exclude external-worker APIs
  and dependencies.
- Show safe problem codes, request IDs, operation IDs, phase, field errors, and
  actionable details through CLI/dashboard. Escape terminal controls and never
  expose raw filesystem/provider errors.
- Include operational up/down/unknown state in monitor list output. Distinguish
  configuration resources from live observations through clear labels and links
  rather than a dashboard redesign.
- Fix demonstrated empty-state wording, percentage overflow, zero-time formatting,
  heading hierarchy, and missing-route handling. Replace unexplained
  operator-facing ECS terminology with controller terminology.
- Use the existing logging package consistently for server/controller/Raft
  diagnostics. Emit incident opening/closure and action outcomes with correlation
  IDs. Correct caller attribution; disable colour in JSON, files, services, and
  non-terminal output.
- Remove old alert-path code only after production reachability and build-tag
  analysis demonstrate it is unused. Move meaningful regression coverage onto
  active paths.
- Profile empty and one-monitor operation before altering the 200 Hz loop. If
  high idle cost reproduces, add owner-loop wakeups and deadline-based idle
  waiting while preserving scheduling latency, cancellation, projection
  freshness, and worker-demand updates.

**Acceptance:** SDK/CLI/dashboard observations agree; supported operations do
not unexpectedly return `404`; unavailable measurements are explicit; logs
explain failures without leaking secrets; idle changes have measured before/after
evidence and passing latency checks.

### 8. Consolidate documentation and release materials

- Make README and quickstart describe the tested initialization flow. Explain
  configuration import/apply behavior, maintenance suppression, notification
  outcomes, unknown-action handling, TLS trust, upgrade, and complete backup/restore.
- Maintain one current support/availability matrix. Preserve historical
  engineering records with dates and scope, but remove them from user-facing
  navigation and replace local workstation paths with portable examples.
- Add security-reporting and contribution guidance, plus a project code of
  conduct. Security instructions direct users to GitHub private vulnerability
  reporting rather than public issues.
- Apply **CC BY 4.0** to CPRa-authored artwork, with Ziad Hassan attribution and a
  separate name/trademark note. Include attribution/licence material in archives,
  packages, images, source distributions, and dashboard notices. Preserve
  third-party licences. [CC BY 4.0 terms](https://creativecommons.org/licenses/by/4.0/).
- Update OpenAPI, examples, Go package documentation, dependency inventories,
  SBOMs, generated dashboard assets, service templates, Compose, and Helm wherever
  the repairs change behavior.
- Treat permanent storage failures remaining unavailable as intentional. Document
  stopped repair/restore; do not add automatic deletion or speculative reopening.
- Historical test counts and old compiler passes remain historical evidence.
  Current documentation is rendered from final release metadata, avoiding
  hardcoded self-referential commit values.

**Acceptance:** strict documentation/link checks and clean asset regeneration
pass; every installation example is executed; source, rendered pages, notices,
and evidence identify the same candidate.

## Public interfaces and compatibility

| Interface | Planned change |
| --- | --- |
| Local administration | `local init --management`; protected token-output support; mode-aware service rendering. |
| Access discovery | `GET /api/v2/access`, returning only access mode and authentication requirement; matching SDK method. |
| Collections | Exact non-admission classification; explicit CLI continuation of the original operation. |
| Observations | Bounded failure diagnostics and explicit health/contention/recovery/cleanup conditions. |
| Discovery | Complete the existing version/schema methods and their authorization/contract coverage. |
| Driver configuration | Explicit gRPC health mode and separate Docker operation budget. |

Reserve **storage formats 40/41** for the new durable notification identities,
diagnostics, and deadlines. Preserve supported older readers and deterministic
replay. Migration cancels obsolete unsent work through committed transitions; it
never invokes providers or repeats started/unknown actions. Older binaries must
refuse unsupported newer state.

Preserve single-node Raft, Go 1.25 compatibility, applicable built-in driver
tags, existing manifest flags, stable monitor IDs, read-only observation routes,
and the approved acknowledge/dismiss/snooze/disable semantics. No check-now
command, multi-node failover, automatic unknown-action replay, package-name
overhaul, or subscription system is added.

## Qualification and publication gates

### A. Freeze and verify the final source

Resolve the intended merge with current `main` locally, complete review, then
freeze one source commit **C**. Generate release metadata from C. Any later
behavior or generated-input change invalidates affected evidence and requires a
new freeze.

Run the complete application, SDK, and worker matrices on Go 1.25 and the pinned
release compiler, covering default, applicable built-in tags, `externaljobs`, and
combined variants. Run race checks where supported, vulnerability scans,
reproducible generation, frontend checks, required real-server browser campaigns,
and independent review.

A required browser campaign must fail when its tooling is absent; it must not
silently skip.

### B. Prove behavior and durability

Exercise real-process crashes around intent, start, external success, result
commit, collection upload/validation/activation, and receipt delivery. Test
partial endpoint completion, configuration changes, unknown-action preservation,
snapshot replay, history pagination/retention, authenticated backup/restore,
failed writes, corruption, locking, and storage upgrades.

Use real local filesystems and network receivers alongside deterministic fault
injection. Verify slow uploads, snapshot overlap, map growth, generation cleanup,
and genuine permanent failures. Preserve failed reproductions until corrected
behavior passes.

### C. Qualify distributions and deployment

- Rebuild unsigned executables/payloads from two distinct roots and an extracted
  source archive; require matching recorded outputs.
- Execute all six claimed Linux/macOS/Windows amd64/arm64 targets natively.
  Record OS, filesystem, toolchain, and evidence level; cross-build or emulation
  does not replace native execution.
- Verify native services and DEB/RPM lifecycle: fresh installation, edited
  configuration, running/stopped upgrades, shutdown deadlines, removal,
  reinstall, purge, retained state, and administrator-owned executables.
- Test both OCI architectures and Compose with retained volumes, UID 1001,
  read-only configuration/root filesystem, missing bind sources, bounded logs,
  and shutdown.
- Test Helm 3, Helm 4, and their upgrade path: one owner, maintenance scale-to-zero,
  existing claims, RWOP contention, token rotation, failed upgrades, slow startup,
  large file-backed manifests, and restore.
- Confirm probes and `helm test` cause zero provider operations.

Use access-controlled qualification infrastructure. Availability of suitable
native runners, accounts, storage classes, and designated resources is an
external prerequisite; missing infrastructure leaves the corresponding gate open.

### D. Qualify providers and agents

Cover the current **35 built-in roles**: 14 checks, six recovery roles, and 15
notifications.

Run production driver paths against real disposable local services and configured
external accounts. Independently verify delivery or effects, including provider
incident closure, Docker replacement, Kubernetes operations, EC2 evidence,
dedicated systemd operations, and designated A2A agent behavior.

Publish a redacted matrix separating fixture contracts, provider acceptance,
observed delivery/effect, and **not configured/not verified**. Missing
configuration cannot satisfy full-provider qualification. Do not create accounts,
purchase infrastructure, or silently substitute mocks for account evidence.

### E. Complete comparative performance qualification

Use baseline **`a370969b041b399c0778318d8915ce059fd74294`** and candidate C with
matched compiler, flags, tags, resources, and workload seeds.

Run 10,000, 100,000, and 1,000,000 distinct monitors at 60-second intervals:
five-minute warmup, fifteen-minute measurement, three repetitions per build.
Follow with separate burst, slow-target, outage, recovery-traffic, and
process-crash scenarios.

Candidate runs include Raft, snapshots, event history, SLO measurement, and
representative dashboard reads. Measure achieved rate, cadence, errors, CPU,
memory, goroutines, descriptors, queues, commit/snapshot latency, disk use, and
independent target-side accounting.

Healthy five-minute windows must meet the agreed p99 targets: **250 ms
scheduling-plus-queue delay** and **five seconds scheduled-to-committed-result
latency**. Baseline internal measurements that do not exist are reported
unavailable.

Before large runs, verify physical C: free space of at least **30 GiB plus
measured fixture headroom**, initially 5 GiB. Preserve the local task's **6 GiB
scratch limit and 64 MiB reserve**. Run fixtures sequentially, rotate logs, retain
bounded counters/digests, and clean only identified task-owned disposable
caches/fixtures after evidence capture. Insufficient space, sleep, clock
discontinuity, interruption, or target saturation produces a failed/incomplete
result—not a reduced workload or inferred pass.

There is **no 24-hour run**. Failed capacity targets keep the agreed gate open
until corrected or explicitly revised by the user.

### F. Publish only after private gates pass

1. Assemble and verify the private release bundle, including exact artifacts,
   checksums, notices, SBOMs, compatibility reports, and qualification evidence.
2. Publish nested SDK and worker **`v0.1.0-rc.1`** tags in dependency order; verify
   downloaded modules with `GOWORK=off`, no local replacement, and normal public
   checksum verification.
3. Publish the qualified application RC, native packages, image digests, chart
   **`0.1.0-rc.1`**, and matching `main`/`gh-pages` documentation.
4. Bind signatures and provenance to the exact qualified bytes and actual builder
   identities. Verify expected repository/workflow/issuer, digest, online
   readback, and offline bundles.
5. Attach all assets before immutable publication. On readback failure, stop
   remaining promotion; corrections require a new version, never rewritten
   published tags or assets.

The endpoint is the qualified **RC release**. Promotion to `v0.1.0` is a
subsequent release decision.
