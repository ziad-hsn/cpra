# CPRa: repair the confirmed 22 October findings

Approved implementation addendum, 2026-10-22. Baseline:
`60cf271036e681f5cb3b771d2599315066a8f946` on `codex/release-repairs`.
This extends the previous product-review and release-repair plans. The checked
assessment is retained privately under `cpra-product-review-check-2026-10-22`.
Track every finding in [the disposition record](product-review-dispositions-2026-10-22.md).

The deliverable is a tested, independently reviewed repair candidate and an
accurate merge handoff. Preserve worktrees, evidence, package names and the
separate documentation checkout. Every module requires Go 1.26.10; the release
compiler is Go 1.27.3. There is no release publication, million-monitor campaign
or 24-hour run. A missing final scale report is neither a pass nor a confirmed
additional defect.

## Selected contract

- New notifications retain their outstanding condition while relevant. Capture
  their execution budget at admission and begin it at the first committed start,
  after execution capacity is available.
- Ordinary built-in notifications have 45 seconds total and at most three
  attempts. An HTTP call has at most ten seconds or the remaining budget. A2A
  and external notifications keep their configured budget lengths.
- Existing absolute deadlines, identities, outcomes and holds remain unchanged.
  Restart, migration, retry and re-offer never renew them.
- Preparation failures are reported in status, warnings, structured logs and
  metrics. No external configuration-failure notification type is added.
- Current public health is `unknown` while preparation prevents checks. Keep
  the previous observation and timestamp separately and preserve incident facts.
- Retain dependency-free ordinary completed actions for 90 days; event history
  stays at 30 days. Unknown actions, reviewed evidence and required dependencies
  remain protected. Logical retirement does not imply immediate file shrinkage.
- Attempt exhaustion and the overall deadline are independent limits. Neither
  starts a new retry cycle. Missing blue destinations remain valid configuration
  with visible warnings; blue never inherits red destinations.

Unknown effects remain held. Preserve conditional writes, actor/reason rules,
executor fencing, configuration/target bindings, synchronous storage,
authenticated stopped backups and tagged-worker exclusion. Health recovery is
not evidence of an intervention's outcome. Replay requires affirmative evidence
of safety: [RFC 9110 §9.2.2](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.2).

## 1. Notification conditions and execution attempts

Addresses R01, R04, R05 and the clarified R13–R14 behavior.

- Represent opening and resolution conditions independently of attempts, bound
  to incarnation, episode, endpoint, configuration and provider correlation.
  Keep one pending condition per applicable identity. Capacity rejection leaves
  a visible pending age and reason instead of dropping the condition.
- Atomically commit first-start time and deadline using the captured budget;
  apply the returned deadline to the actual transport context. Keep bounded
  destination fairness/serialization and recheck eligibility before every start.
- The controller owns built-in retries. Retry only proven transient no-effect
  failures, with stable delivery identity, original deadline and three-attempt
  cap. Permanent failure, exhaustion and expiry have explicit terminal reasons.
- Old expired actions stay canceled. A fresh observation can establish a new
  current condition only with proof of no start/no effect, through a separate
  committed authorization; never reopen the old action.
- Keep an eligible resolution for an incident whose opening was attempted. A
  recovery during maintenance is reassessed after suppression using the original
  destination/key; stale openings are not released. If a later outage exists,
  closure describes the earlier recovery and current health accurately.
- Preserve normal green's prior-start rule and Ignore's exception. Define
  `recovery_exhausted` by confirmed attempt, deadline or permanent-failure
  termination. Unknown recovery remains review-required and uses configured blue.
- Worker offers retain their 30-second lifetime. Assignment deadline is a
  conservative execution upper bound; first committed start returns the exact
  deadline. Repeated starts return the original disposition/deadline. Update
  tagged capability descriptions and versioned digests, not the start endpoint.

Acceptance: retained 30/40/60-monitor bursts deliver eligible notices despite
long pre-start waits; recovered openings cancel; maintenance closes the right
condition; lost starts and effect-then-500 invoke no duplicate effect.
[Alertmanager timing precedent](https://prometheus.io/docs/alerting/latest/configuration/#route).

## 2. Supported-upgrade delivery ownership

Addresses R02 and R15; depends on ticket 1's state contract.

- Explicitly bind supported pre-identity records, including `b293f34`. Derive
  exposure only from durable start evidence. Preserve original actions,
  incidents, configuration/endpoint ownership and review digests.
- Preserve the actual historical provider-key algorithm, including the old
  PagerDuty key. An old version number is not today's hashing algorithm.
- If binding cannot be proven, hold the affected delivery and expose migration
  uncertainty. Do not map an old ordinal to a changed destination or create a
  replacement trigger. Cover working later-format migrations and namespaces.
- Document `incident_id` as installation-local; use episode/delivery identities
  for cross-installation correlation. Do not rewrite historical incident IDs.

Acceptance: queued, started, succeeded and unknown fixtures upgrade without
duplicate effects; closures use original keys; changed/incomplete bindings hold.
[PagerDuty correlation](https://docs.pagerduty.com/developer/send-alert-event),
[Opsgenie asynchronous processing](https://docs.opsgenie.com/docs/alert-api).

## 3. Historical processing and 90-day retirement

Addresses R09–R10.

- Replace lifetime scans with indexes of held interventions and relevant review
  conditions. Direct action assessments use lookup. Membership must be readable
  at the committed assessment fence; a current-only index is insufficient.
- Process bounded pages per committed maintenance step, preserving captured
  policy, observation time, order and progress. Checks and new blue assessments
  must not enumerate irrelevant completed actions.
- Index terminal time. Retirement commands contain an explicit UTC cutoff and
  expected versions. Retire only ordinary succeeded, proven-no-effect failed or
  canceled records older than 90 days without execution, retry, condition,
  correlation, review, receipt or assessment dependencies. Missing timestamps
  do not permit deletion. Protect unknown/review/late/conflicting evidence and
  keep worker receipt retention unchanged.
- Compact replacement generations using ownership/pins; keep current, previous
  and pinned generations. Validate retained snapshot envelopes/checksums with
  bounded readers, materialize the selected snapshot once, and refuse silent
  fallback over corrupt newer state.
- Report active, ordinary retained, protected and cleanup-blocked counts/bytes.

Acceptance: with fixed relevant holds, 0/500/5,000 ordinary records do not change
records visited per check/blue assessment; test 89/90/91-day boundaries, receipt
replay, pins, crashes and backup. Measure allocation/startup separately.
[Raft snapshot contract](https://pkg.go.dev/github.com/hashicorp/raft@v1.7.3#FSM),
[bbolt transaction constraints](https://github.com/etcd-io/bbolt/blob/v1.4.3/README.md#transactions).

## 4. Bootstrap, validation and parser memory

Addresses R06–R07, R12 and related configuration findings.

- Parse, validate and freeze first import in private encrypted same-volume
  temporary staging. Publish resumable staging only after validation; never
  reread a changing source.
- Add stopped `cpractl local bootstrap inspect` and `discard`. Discard requires
  exclusive ownership, exact stage identity, actor/reason and proof of no
  bootstrap/catalog admission. Reject frozen/admitted stages, corruption, wrong
  keys, uncertain ownership, unexpected entries, links/reparse points and identity
  changes. Never delete Raft state.
- Reuse structured errors; retain safe cause, original spelling, resource/document
  index and line/column. Return/render at most ten ordered field errors with
  truncation and collection pagination. Syntax failure may stop its document;
  independent parsed resources can contribute further bounded errors.
- Preserve operation handle/actual failed phase in SDK/CLI apply. Direct further
  inspection to `get operation ID --validation`; do not fetch every page.
- Limit parser batches by 4 MiB decoded bytes and record count, including buffered
  batches. Charge aliases before materialization and retain all resource, anchor,
  depth, input and spool limits. Check BOM/spelling claims with the shared corpus.

Acceptance: correcting an unadmitted typo permits initialization; malformed final
input activates nothing; paths agree on safe causes; alias inputs finish within
explicit memory limits or reject with a quota error.
[RFC 9457](https://www.rfc-editor.org/rfc/rfc9457.html),
[YAML 1.2.2](https://yaml.org/spec/1.2.2/),
[Go filesystem confinement](https://go.dev/blog/osroot).

## 5. Cursor reclamation and token files

Addresses R08, R16–R18.

- Retain 16 views/principal, 64 total, five-minute maximum retention and eight
  concurrent reads. Under pressure reclaim expired, completed, then LRU inactive
  views within the applicable quota. Never evict an in-flight read.
- Retained pages replay identically. Evicted continuations return `410
  cursorExpired`; never reconstruct newer state. Close pins outside metadata
  locks. One-page results neither consume nor depend on retained-view slots.
- Validate native ownership/permissions/ACLs on opened token handles and bounded
  contents. Unix defaults to current owner and owner-only access. Root-owned
  group-readable service files require explicit trusted-reader-group policy;
  projected Secret links require an explicitly trusted mount.
- Windows uses native DACL checks for owner, administrators/SYSTEM and an explicit
  service identity. Fail safely when unverifiable. Never chmod/chown supplied
  credentials. Update service/Compose/Helm policy and preserve immutable Secret
  references plus intentional rollout.
- Explain log-root owner-only permissions and writable `0700` setup; return a safe
  specific local cause. Empty log filename remains documented stdout.

Acceptance: browsing does not exhaust inactive views, replay/eviction is safe,
unsafe token files fail, and configured UID/GID 1001 Secret mounts work.
[Kubernetes pagination](https://kubernetes.io/docs/reference/using-api/api-concepts/#retrieving-large-results-sets-in-chunks),
[Secret permissions](https://kubernetes.io/docs/tasks/inject-data-application/distribute-credentials-secure/#set-posix-permissions-for-secret-keys).

## 6. Truthful operator state

Addresses R03, R05, R11 and confirmed interface findings; depends on tickets 1–5.

- Project current health as unknown during preparation failure while retaining
  last observation/time. Expose availability, safe cause and effective time. Clear
  the problem only after preparation succeeds and an actual check returns; retry
  preparation at normal cadence without configuration-induced recovery.
- Publish controller maintenance/suppression decisions with observation/config
  identity; clients must not reconstruct them from their clocks. Distinguish
  unavailable monitoring, paused checks, suppressed/unsuppressed outages, pending
  deliveries and unknown review. All-operational excludes unavailable monitoring.
- Show missing-blue warnings in validation/startup/apply/diff/describe/import,
  bounded with truncation and transition logs. Show pending age, canceled/expired
  attempts and terminal reasons; separate local closure, provider acceptance and
  independently confirmed resolution.
- Synchronize base/tagged schemas, SDK, CLI, dashboard and operation inventory.
  Measure virtual row heights for bounded pages; test wrapping, long IDs, zoom
  and mobile. Preserve stored-secret concealment; reveal only newly entered input.
- Verify remaining UI/log claims and add missing structured Ignore transitions
  or safe typed diagnostics where reproduced.

Acceptance: every interface agrees, old clients see unknown rather than false
green, maintenance is visible, rows do not overlap and results contain no secrets.
[WCAG reflow](https://www.w3.org/WAI/WCAG22/Understanding/reflow.html).

## 7. Documentation and security reporting

Addresses R19–R22 and remaining confirmed documentation findings.

- Document timing, exhaustion, pending closures, unavailable monitoring, 90-day
  actions, 30-day history and protected evidence. Consolidate implemented/tested
  platforms, compiler/module compatibility and upgrade/rollback limits.
- Remove process/host narration from product pages, preserve engineering records
  and publication filtering, and check generated output/search.
- Execute corrected initialization, validation, apply, failure recovery, cursor
  and stopped-administration examples. Explain ID scope, stdout, token deployment
  and bootstrap conversion versus explicit apply.
- Enable/verify private vulnerability reporting when authorized access exists.
  Default-branch SECURITY.md remains a gate after separately authorized merge;
  never publish early. Correct confirmed description typos if access permits,
  retaining topic choices.

Acceptance: strict docs/link checks and examples pass, evidence stays unchanged,
and reporting status is verified.
[MkDocs filtering](https://www.mkdocs.org/user-guide/configuration/#exclude_docs),
[GitHub reporting](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/configure-vulnerability-reporting/configure-for-a-repository).

## Migration, verification and handoff

Reserve base/tagged storage formats 46/47. Change command validation, digests,
snapshots and progress together; preserve supported historical replay including
44/45 and downgrade refusal. Migration invokes no providers. Require complete
stopped authenticated backup before upgrade; rollback requires an older-compatible
backup, not only an older executable.

Each ticket needs focused checks, documentation and a separate reviewer agent.
Keep failing evidence. Run delivery bursts, delayed/lost starts, partial endpoints,
maintenance/later outage, HTTP effect then 500 and retry exhaustion. Exercise
`b293f34`, `0a22680` and current stores, original provider keys, changed/incomplete
bindings, history sizes, retention boundaries, snapshot pins, writes/locking,
corruption, crash retirement and backup restoration. Cover typo correction,
malformed final files, aliases/quotas, token ACLs/mounts/path substitution and
secret scans. Required real-server browsers cover default/tagged desktop/mobile/
zoom, state, warnings, delivery and repeated pagination.

Performance scenarios run sequentially for at most 15 minutes each, with bounded
logs and independent receiver accounting. Preserve the 6 GiB task scratch limit
and 64 MiB reserve; clean only identified task-owned disposable files after
evidence capture.

Freeze one final candidate before the affected app/SDK/worker matrix on both
pinned compilers, default/built-in/externaljobs/combined selections, race and
vulnerability checks, generation, frontend, docs and required browsers. Missing
required tools fail rather than skip. Retain response-close/effect tests on both
compilers; [Go 1.27 reuse improvements](https://go.dev/blog/http-connection-reuse)
permit neither unbounded body draining nor extra mutation retries.

The handoff distinguishes ready-to-merge from qualified-to-publish, lists every
disposition, migration restrictions and remaining native/provider/packaging/
capacity gates. This addendum authorizes no release publication.
