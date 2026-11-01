# CPRa: implementation plan for the 25–27 October review repairs

Approved 28 October 2026. Implementation baseline: `codex/release-repairs` at
`967f9cf8b5733a9f93c56ceaf8712a748cdf5e08`, including its existing uncommitted
implementation. The checked assessment is retained with the private review
evidence. This is an implementation record, excluded from the public site.

## Summary

Repair all confirmed findings in the checked assessment, including the selected
notification-throughput changes, and produce an independently reviewed merge
candidate. Preserve existing implementation, evidence, worktrees and the separate
documentation checkout. Keep Go 1.26.10 across every module and Go 1.27.3 as the
release compiler.

Maintain dispositions for B1–B6, M1–M9 and every selected minor finding. Every
finding links to a repair and regression evidence, an intentional-behavior
explanation, or an explicit unresolved result. No release publication,
million-monitor campaign or 24-hour test is included.

## Selected behavior and compatibility

| Area | Implementation contract |
|---|---|
| Notification bursts | Bounded concurrency between independent notification sequences sharing a destination; ordering within related sequences. |
| Initial concurrency | Generic HTTP webhook notifications: four per destination. Other notification drivers: one by default, with explicit bounded overrides. Existing global limits and destination fairness remain authoritative. |
| Provider limits | Provider-specific pacing, shared quota scopes and documented response handling; concurrency cannot override a rate limit. |
| Debounce and digests | Deferred; no deliberate grouping delay, dropped independent alerts or combined incident messages. |
| New intervention budgets | Absolute deadline established at the first committed start; later permitted attempts share it. |
| Existing actions | Preserve recorded deadlines, identities, attempts, target bindings and unknown holds. |
| Recovery-triggered red | Preserve meaning; warn when none/no-match leaves an outage outside its coverage. |
| Green eligibility | An opening actually started, plus the existing Ignore exception; provider acceptance is not an additional prerequisite. |
| Unknown effects | Hold the affected action; no automatic replay. Health recovery does not prove intervention success. |
| Monitoring unavailable | Current health unknown, last observation retained separately; no manufactured outage or configuration-failure paging. |

Reserve storage formats 50/51 for base/tagged changes. Preserve supported
historical replay, including 48/49. Migration invokes no providers and never
invents exposure, redirects old work or renews existing budgets. Require a
complete stopped authenticated backup before upgrade. Older binaries reject newer
state; rollback requires an older-compatible backup.

## Ordered implementation checkpoints

### 1. Isolate verification and preserve evidence

Addresses the reviewer's real-store incident and verification reliability.

- Require process-test helpers to receive explicit task-owned state,
  configuration, credentials and log locations; reject paths resolving to normal
  CPRa directories.
- Record the source inventory and preserve failed reproductions before repairs.
- Investigate reported modification of the real default store separately using
  stopped, read-only inspection. If active, leave it pending rather than stopping
  or opening it implicitly. Establish format and recovery evidence before any
  restore, downgrade or repair proposal.
- Keep the 6 GiB scratch ceiling and 64 MiB reserve; clean only identified
  task-owned disposable files.

Acceptance: default-store misuse is rejected, evidence remains intact, and
qualification modifies no user store.

### 2. Repair durable completion reconciliation

Addresses B1 and the selected-rule preparation failure path.

- Distinguish contention, leadership unavailability, uncertain submitted writes
  and permanent storage failure.
- Reconcile using the original executor identity and completion timestamp;
  preserve provider outcomes while markers remain unresolved. Retry persistence
  reconciliation only, never the provider operation.
- Bound reconciliation and backlog; close unsafe admission until uncertainty is
  resolved. Permanent storage failure still requires stopped repair/restore.
- Isolate selected-intervention preparation errors to the monitor's
  availability/admission state instead of permanently stopping the controller.
- Publish truthful progress and availability. Readiness reflects safe admission;
  process liveness is not evidence of progressing checks.

Acceptance: temporary completion-write delays recover without duplicate effects;
permanent failures stay unavailable; stale observations are not current health.

### 3. Correct rule admission, classification and validation

Addresses B2, B4, B5, M3, M4, M7 and related validation findings.

- Account for custom JSON representations, including RecoveryRules, while keeping
  the 4 MiB command limit and 32-rule maximum.
- Correct bootstrap credential extraction's path/category ordering; cover all
  action-bearing branches and credential identities.
- Classify observations independently of rule configuration. Identical outcomes
  produce identical health, availability and incident behavior.
- Distinguish target observations from inability to observe: missing CA material,
  local resource exhaustion and failed container inspection do not establish the
  target's health. Keep typed evidence; no raw-error matching or inferred causes.
- Freeze taxonomy versions with fixtures/digests; changed meanings require a new
  version and historical replay retains its interpretation.
- Preserve recovery-only red, warn about none/no-match coverage, and suppress
  missing-blue warnings when no intervention is possible.
- Align spellings, version types, source locations and bounded field errors across
  files, bootstrap, API and collections. Ordinary writes remain reference-only;
  bootstrap may convert inline credentials.
- Make offline rule testing validate complete inert inputs and observations,
  distinguish absent monitors/policies, and honor human-readable output.

Acceptance: valid maximum-rule fixtures start within quotas; action-bearing
bootstrap works; paired observations agree; validation errors are equivalent and
safe, without provider or CA-file I/O.

Reference: [Go JSON contract](https://pkg.go.dev/encoding/json#Marshal).

### 4. Preserve notification ownership and intervention timing

Addresses B3, M1, M5 and lifecycle findings.

- Preserve resolutions across unrelated edits; retain original incarnation,
  episode, destination, provider correlation and credential dependencies.
- Compare relevant dependencies rather than every revision change. Changed or
  revoked dependencies receive explicit safe dispositions, never ordinal
  rebinding or silently substituted credentials.
- Reconstruct missing conditions only from proof of original starts and bindings;
  otherwise expose uncertainty.
- Capture new intervention budgets at admission and commit deadlines at first
  start. Apply the returned deadline to local/external execution; worker-offer
  expiry stays separate. Subsequent attempts share deadline and incident allowance;
  cancellation refunds neither attempts nor budget.
- Correct guarded-recovery error mapping and separately expose qualification,
  selection, admission reasons, attempts and deadlines.
- Preserve eligible resolutions after unknown openings with fencing/suppression;
  never claim that a resolution proves the earlier external effect.

Acceptance: harmless edits retain green; changed targets receive no old work;
new queued work retains its budget until start; restart/lost replies never renew
started work; malformed/stale requests have appropriate problem responses.

### 5. Implement bounded notification dispatch

Addresses M2 and queue/pool observability.

- Extend existing queues/pools with separate destination-concurrency and
  related-delivery serialization accounting. Historical provider keys identifying
  one external incident retain their serialization domain.
- Apply selected defaults and bounded runtime overrides. Intervention target
  serialization remains unchanged. Support explicit shared limiter scopes where
  account/channel relationships cannot be inferred.
- Reserve capacity and limiter permission before start commitment. Destination or
  rate-window waits consume neither workers nor new execution budgets.
- Preserve ordinary notification limits: 45 seconds total, ten seconds per HTTP
  call, at most three attempts. Documented limiter rejections count as attempts.
- Keep provider effect classification authoritative: generic 429/503 or
  Retry-After alone cannot authorize replay.
- Persist committed cooldowns/accounting against restart bursts, using versioned
  commands and captured times. Recheck eligibility at each start; cancel recovered
  pending openings and preserve eligible original-correlation closures.
- Export bounded pipeline/provider metrics for age, active/executable work,
  rate-limiting, cancellation and exhaustion; no private destination/secret labels.
- Bound active flow bookkeeping by admission limits, with no permanent
  goroutine/timer per monitor.

Acceptance: measure shared-webhook improvement and destination isolation; retain
local invocation order and committed pauses; effect-then-500 remains one invocation
and an unknown hold. Asynchronous provider processing order is not guaranteed.

References: [Slack](https://docs.slack.dev/apis/web-api/rate-limits/),
[Discord](https://docs.discord.com/developers/topics/rate-limits),
[Opsgenie](https://docs.opsgenie.com/docs/api-rate-limiting),
[Power Automate](https://learn.microsoft.com/en-us/power-automate/limits-and-config).
Respect provider-specific scopes; connector limits do not universally describe
Power Automate workflows, and accepted delayed requests are not rejected requests.

### 6. Repair restored access, cursors and worker edge cases

Addresses M6 and selected administration/protocol findings.

- Make stopped reprovisioning explicitly access-mode aware, supporting file-mode
  authentication and named management authority without silently converting.
  Preserve restore identities, revocations, exclusive ownership and administrator
  intent.
- Reclaim expired, completed, then least-recently-used cursor views without active
  readers under pressure. Remove the blanket idle delay, preserve quotas and
  retained-page replay, return 410 cursorExpired after eviction, and close pins
  outside metadata locks.
- Align worker certificate validation with server canonicalization; reject invalid
  reports locally rather than entering repeated rejected delivery.
- Discover interrupted encrypted journal-conversion partials with bounded,
  identity-checked recovery. Preserve original journals; invoke no handlers.

Acceptance: documented restore procedures work for both modes; cursor pressure and
eviction are safe; worker validation agrees; conversion preserves originals.

Reference: [Kubernetes pagination](https://kubernetes.io/docs/reference/using-api/api-concepts/#retrieving-large-results-sets-in-chunks).

### 7. Synchronize API, SDK, CLI and dashboard

Addresses M8, guarded recovery usability and selected interface findings.

- Update canonical base/tagged schemas, generated types, operation inventory and
  adapters together, including dropped recoverySelection.
- Separate health, availability, suppression, qualification, selected action,
  admission blockers and delivery outcomes. Show simultaneous fleet conditions.
- Add bounded server-side filters and stable incident triage order, binding cursors
  to queries and preserving latest-episode-per-monitor semantics.
- Retain the shell on lazy-route failure with offline/stale state and bounded
  retry controls; never persist bearer tokens or retry mutations automatically.
- Improve rule summaries/driver fields, reorder focus, conditional diffs and
  input-linked errors. Normalize machine UTC and explicit absent values.
- Correct recovery wording and add safe structured transition/cancellation
  diagnostics; no arbitrary provider errors or repeated per-check warnings.
- Repair demonstrated mobile, wrapping, keyboard, zoom and heading issues.

Acceptance: all interfaces agree; qualification and permission are distinguishable;
real-server desktop/mobile checks pass without credential exposure.

### 8. Complete documentation and security readiness

Addresses B6, M9 and remaining selected documentation findings.

- Update timing, provider limits, resolutions, unavailable observations, rules,
  guarded recovery and restore guides. Execute complete examples with required
  runtime/log settings and first-import versus explicit apply behavior.
- Consolidate implemented versus tested support/compatibility. Preserve historical
  records while excluding them from public pages/search.
- Enable private vulnerability reporting through authorized administration and
  verify the form. Prepare root SECURITY.md for merge; default-branch verification
  follows separately authorized merge, never early candidate publication.
- Correct the confirmed description typo without changing topics; record every
  finding disposition and open qualification gate.

Acceptance: strict docs/links/examples pass, historical evidence is unchanged, and
the reporting channel is verified.

Reference: [GitHub reporting configuration](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/configure-vulnerability-reporting/configure-for-a-repository).

## Verification and completion

Every checkpoint requires focused tests, documentation and a separate reviewer
agent. Preserve baseline failures and distinguish source-only findings from runs.

| Area | Required scenarios |
|---|---|
| Durable progress | Loaded bursts, delayed/lost completion commits, permanent failures, truthful readiness/staleness. |
| Rules | Maximum rules/encoded boundaries, bootstrap, policy-independent observations, taxonomy compatibility/shared validation. |
| Lifecycle | Harmless edits, changed dependencies, unknown opening then eligible resolution, maintenance recovery, blue closure retaining unknown hold. |
| Budgets | Long waits, first-start deadline, shared retry budget, historical deadlines, lost grants/restarts. |
| Throughput | Retained 30/40/60-monitor cases, 250 monitors at one 300-ms receiver, hanging/healthy destinations, pauses, independent accounting. |
| Administration | Both restore modes, cursor pressure/concurrent eviction, conversion interruption, invalid structured reports. |
| Interfaces | Default/tagged browser campaigns, triage, offline, field errors, mobile/zoom, secret scans. |
| Storage | Historical replay, 48/49 to 50/51, snapshots, authenticated restore, failed writes, dependencies, downgrade refusal. |

Run performance scenarios sequentially, at most 15 minutes each, with bounded logs.
Record timing, invocation counts, pending/canceled outcomes and resource use.
250 × 300 ms ÷ 4 = 18.75 seconds is a calculation, not measured acceptance or a
provider guarantee.

After repairs and independent reviews, freeze a candidate and run affected
application, SDK and worker matrices on Go 1.26.10 and 1.27.3: default, built-in
tags, externaljobs, combined, race, vulnerability, reproducible generation,
frontend and required browser checks. Missing tooling or timed-out scans remain
failures. Keep HTTP handling bounded; connection reuse does not authorize
additional mutation retries or unbounded draining.
[Go guidance](https://go.dev/blog/http-connection-reuse).

Completion requires linked dispositions/evidence, passing affected checks, reviewed
migrations and an accurate handoff. Ready to merge is distinct from qualified to
publish; native platforms, provider accounts, packaging and larger capacity remain
independent gates.
