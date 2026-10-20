# CPRa notification, storage, scheduling and operator-interface repairs

Approved 20 October 2026. This addendum continues the product-review repair plan
from `0a22680c48e59e054fbf298d6f99c5ac62342e5a` on `codex/release-repairs`.
The source review is the 17–19 October 2026 product review. Its checked assessment
distinguishes reproduced defects, source-supported findings and intentional
safety behavior. Finding dispositions and current execution status are maintained
in [the inventory](product-review-finalization-status.md).

## Deliverable and boundaries

Deliver a tested, independently reviewed repair candidate and merge handoff.
Preserve existing worktrees, evidence, package names and the separate documentation
checkout. Keep Go 1.26 as the minimum across every module and the currently pinned
Go 1.27.2 release compiler. No release publication, million-monitor campaign or
24-hour run is included. Preserve the 6 GiB task scratch limit and 64 MiB reserve;
clean only identified task-owned disposable files.

## Agreed behavior

| Situation | Required behavior |
| --- | --- |
| Outage continues after maintenance or Unignore | Re-evaluate the current outage using fresh observations. Admit its eligible notification once; never replay canceled alerts from an outage that already recovered. |
| Ignore applied while healthy | Keep Ignore until a subsequent outage reaches the unhealthy threshold and then passes the healthy threshold, or explicit Unignore. Successful checks alone do not manufacture recovery. |
| Automatic Ignore and maintenance | Outages wholly inside maintenance do not count. A continuing outage counts once after the configured unhealthy threshold is confirmed outside maintenance. |
| Ignore recovery | Preserve automatic exit and counter reset after verified recovery, and its explicitly allowed recovery green. |
| Unknown intervention | Continue health observations and incident progression; retain the unknown outcome and conflicting-intervention hold. |
| Blue notification | “Recovery outcome unknown — operator review required.” Explicit blue destinations only; never inherit red destinations. |
| Blue closure | After the healthy threshold: “Health recovered; earlier recovery outcome remains unknown.” Retain the unknown action and hold. A later qualifying outage can produce a new blue alert when that hold still blocks recovery. |
| Missing blue configuration | Valid configuration, with a preflight/operator warning that uncertain recovery has no external notification destination. |
| Gray | Preserve the existing enabled/disabled exception during Ignore and maintenance. Blue does not acquire that exception. |

Health recovery is not proof of intervention outcome. Only existing audited
review/evidence rules release unknown intervention holds. Conclusive review may
close the review-needed alert without claiming an unhealthy service recovered.
Preserve original action identities, target/configuration bindings, deadlines,
conditional writes, actor/reason requirements and executor fencing. Generic HTTP
500, lost responses and interrupted execution remain uncertain unless affirmative
evidence proves rejection or no effect. Never automatically replay unknown actions,
resolve conflicts, introduce notification-driven recovery loops, downgrade
authentication or add check-now. Preserve single-node Raft, synchronous writes,
stopped authenticated backups, external-worker build exclusion and read-only
observation mode.

## 1. Effect classification and deadlines

Addresses M2–M3 and deadline findings.

- Use a shared internal classification of success, proven rejection/no effect and
  uncertain outcome, retaining provider-specific evidence.
- Recognize connection refusal before transmission, certificate verification
  failure before application data and local rejection before writing. An absent
  tracing callback is not proof that nothing was sent.
- Retry confirmed transient no-effect failures only within existing attempt limits
  and the original deadline. Invalid CA settings, forbidden log targets and malformed
  local settings need correction, not automatic repetition.
- Preserve one built-in notification retry owner. Exercise actual transports,
  reused connections, partial writes, response loss and SMTP acceptance boundaries.
- Reject expired queued actions before start. Never refresh restored/delayed budgets.
- Explain that max_attempts is a limit, not permission to repeat uncertain effects.

Acceptance: refused delivery can recover through bounded retries; an effect followed
by 500 is invoked once and held; local configuration rejection is definite; expired
work invokes no provider. References: [RFC 9110 §9.2.2](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.2),
[Go HTTP transport](https://pkg.go.dev/net/http#Transport).

## 2. Notification eligibility, Ignore and blue

Addresses B1–B2, M1 and notification-content findings. Depends on ticket 1.

- Assess notification eligibility from committed current episode, controls and health,
  not only incident opening. Reconsider ongoing outages after suppression. An outage
  first established during maintenance requires fresh outside-maintenance threshold
  confirmation before automatic Ignore counting or its outage notification.
- Persist qualifying-outage evidence for Ignore. Existing qualifying outages can
  recover normally; Ignore entered while healthy waits for an actual qualifying outage.
- Count each outage once. Preserve rolling limits and selected reset behavior. Log
  Ignore entry/exit/cause/count; add no unapproved notice exception or expiry.
- Add blue to manifests, management resources, SDK, dashboard, discovery and drivers.
  Emit once per blocking action, episode and destination; retries keep delivery identity.
- Use blue-specific provider correlation, separate from health red/green. Closure
  retains original destination/correlation. Channels without resolution receive an
  explicit closure/update event.
- Verified health closes blue without releasing its action hold. Conclusive review
  can end the review-needed condition; describe actual current health.
- Blue opens/closures obey ordinary suppression. Record suppressed/failed closure
  honestly. Local health alone is not provider resolution. Re-evaluate current eligible
  conditions after suppression instead of replaying stale queued work.
- Preserve ordinary green's prior-start requirement, Ignore's exception, cyan's
  verification-pending wording, and failure versus uncertainty.
- Add stable email Date/Message-ID, IDs in log notifications and recovery-failure
  context in relevant red messages. Preserve configured recovery webhook bodies.

Acceptance: continuing outages alert after suppression; resolved suppressed outages
produce no stale red; healthy Ignore produces no false green; blue health closure
retains the unknown hold. References: [PagerDuty Events API](https://docs.pagerduty.com/developer/send-alert-event),
[Opsgenie Alert API](https://docs.opsgenie.com/docs/alert-api),
[RFC 5322](https://www.rfc-editor.org/rfc/rfc5322.html#section-3.6).
Provider acceptance and observed completion remain distinct.

## 3. Retained actions and migration

Addresses M4, installation correlation and upgrade findings.

- Separate current execution state from retained action records inside persistence.
  Keep current references, bounded eligibility information and aggregate hold/review
  counts in the frequently copied monitor representation.
- Use a Store-owned indexed bbolt action/review ledger, preserving lookup by action,
  monitor/incarnation, episode and review state. Reuse existing ownership, snapshot
  pins and recovery patterns.
- Apply committed commands with replay identities and durable progress. Ordinary
  successful checks neither copy nor scan historical actions.
- Keep paginated action reads outside hot controller/FSM locks; preserve exact review
  revisions, late evidence and conflict checks.
- Preserve retained unknown/reviewed evidence. Bound active processing and memory;
  do not promise constant disk usage for indefinitely unresolved records.
- Include ledger/catalog/progress in snapshots and stopped backups. Compaction must
  not discard reconstruction data; failed durable writes close unsafe admission.
- Persist an installation namespace for new provider keys. Independent installations
  differ; disaster recovery and in-flight work preserve original identities/keys.
- Upgrade prior notification exposure only from durable evidence proving actual start.
  Never manufacture exposure/correlation to produce green.
- Restrict new snapshot directory/file permissions and equivalent Windows ACLs.

Reserve formats 44/45 for base/tagged changes. Change command validation, digests,
snapshots and migrations together. Preserve supported older deterministic replay;
older binaries refuse newer state. Migration performs no provider operations.

Acceptance: 0/500/5,000 retained actions do not increase historical records copied or
visited per check; records remain queryable/reviewable after restart, snapshot and
backup restore. Freeze snapshot state before background serialization and test mapping
growth with snapshot readers. References: [Raft FSM](https://pkg.go.dev/github.com/hashicorp/raft@v1.7.3#FSM),
[bbolt transactions](https://github.com/etcd-io/bbolt/blob/v1.4.3/README.md#transactions).

## 4. Configuration and CLI errors

Addresses M5–M7 and validation/first-run exceptions.

- Extend existing validators/error types. Preserve source file, document/resource,
  original YAML spelling, path and line/column through normalization/spooling. Return
  bounded ordered field errors and attach them to dashboard controls.
- Distinguish input/token files, invalid settings, TLS trust, authentication,
  authorization, throttling, transport and readiness failures. Retain request IDs,
  operation handles and phases. Escape terminal controls; do not expose credentials,
  arbitrary remote errors/bodies or server filesystem paths.
- Keep static validation independent of provider and CA-file reads. Local preparation
  diagnoses missing/unreadable/invalid CA material before target invocation as a
  configuration problem, preserving incident facts and suppressing configuration-induced
  recovery. Retry local preparation at normal monitor cadence.
- Reject ca_file plus skip-verification conflicts. Preserve TLS defaults and isolation.
- Fix negative recovery timeouts, protected log-root conflicts and examples needing
  a log directory. Document working-directory-relative CA paths; use absolute service paths.
- Accept UTF-8 BOM and bounded same-document aliases across entries. Reject cycles,
  excessive expansion and cross-document references under existing spool/resource quotas.
- Ignore irrelevant client authentication environment inputs during management
  validation; validate explicit auth configuration and retain startup authority checks.
- Explain that validation checks supplied input without applying it to a stored catalog.
- Add safe field diffs, compact apply summaries, revision-discovery help and repeatable
  connection instructions. Add local init --port (default 8060), never printing tokens.

Acceptance: equivalent safe errors across input paths, malformed final collection
activates nothing, bad CA files do not open false service incidents. References:
[YAML 1.2.2](https://yaml.org/spec/1.2.2/), [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457.html).

## 5. Scheduling and destination fairness

Addresses M10 and queue/pool findings.

- Preserve Erlang C/Allen–Cunneen, global limits, five-second evaluation, twofold growth,
  minimum observations and healthy reduction hold.
- Allocate driver reservations by configured eligible demand, not all compiled drivers.
- Per-driver floor: sum min(1, timeout/interval), plus existing 15% headroom, bounded by
  global limits. Use the greater of reserve and model recommendation. Recompute on
  configuration/eligibility change; never shrink below the floor. Expose unmet capacity.
- Share spare capacity fairly while protecting other configured drivers/new flows.
  Include active execution ages and pending work already dequeued but not executing.
- Add bounded private-destination flows to notifications/interventions. Preserve
  delivery ordering and commit-before-start. A stalled destination cannot monopolize a pool.
- Report requested/active/draining capacity, pending depth/age, timeout/missed counts,
  limits and bounded pipeline/driver Prometheus labels with documented units.
- Align startup sizing to 250 ms. Log useful saturation transitions rather than routine
  misleading warnings or tuning chatter.

Acceptance: healthy checks and destinations progress through late correlated stalls;
capacity remains bounded and timeout/missed work remains in measurement. References:
[Kubernetes APF](https://kubernetes.io/docs/concepts/cluster-administration/flow-control/),
[Prometheus instrumentation](https://prometheus.io/docs/practices/instrumentation/).

## 6. API, SDK, CLI and dashboard

Addresses M9 and interface findings. Depends on tickets 1–5.

- Synchronize canonical base/tagged contracts, generation, inventory and clients.
  Blue is built-in; external worker interfaces remain absent from untagged builds.
- Separate health, configuration errors, controls, delivery, blue closure and review.
  Bind incident notification columns to deliveries, not dismissal. Only show recovery
  verification when it is actually occurring.
- Normalize machine timestamps to UTC and missing times explicitly; distinguish first
  failure from incident opening. Human-format durations without changing declared units.
- Add root/missing-route errors and offline/stale views without stacks, implicit mutation
  retries or authentication downgrade. Attach/focus field errors accurately.
- Expose guided notification setup from monitors/recipients; modifying rules requires
  a visible conditional diff, original operation handle and partial-result visibility.
- Repair clipping/mobile navigation/long IDs/tables/zero times/terminology and clarify
  intervention attempts, check retries and unhealthy thresholds.
- Generate blue through the canonical palette; distinguish from cyan with text/icons;
  reduce duplicate live-region announcements.

Acceptance: real-server default/tagged browser scenarios cover states, offline views,
blue lifecycle, errors and safe desktop/mobile writes. References:
[React Router 7.18.3](https://github.com/remix-run/react-router/blob/react-router%407.18.3/docs/how-to/error-boundary.md),
[WCAG color](https://www.w3.org/WAI/WCAG22/Understanding/use-of-color.html).

## 7. Documentation, reporting and hygiene

Addresses B3, M8 and remaining minors.

- Describe product behavior in README/reference pages. Centralize current availability,
  tested platforms and version compatibility rather than repeating process narration.
- Document colors/Ignore/review/CA preparation/import/apply/migration/backup/queues and
  intentional observation-only and insufficient-sample behavior.
- Use a populated synthetic dashboard image, preserving attribution/licenses.
- Correct stale SDK/OpenAPI/driver comments, workstation paths and docs-sync errors.
- Keep historical engineering records in the repository but out of published pages/search;
  routine doc synchronization must not rewrite historical evidence.
- Remove tracked build cache and ignore agent/build files, preserving embedded assets and
  useful source copies. Do not delete branches, issues, PRs or unrelated files.
- Consistent structured action/Ignore/restart-unknown logging; reduce routine Raft noise.
- Enable private vulnerability reporting with authorized administration, verify its public
  form and default-branch SECURITY.md before announcement. Missing admin access stays open.

Acceptance: newcomer walkthrough, strict docs/links, actual publication exclusions and
working private security channel. References: [MkDocs exclude_docs](https://www.mkdocs.org/user-guide/configuration/#exclude_docs),
[GitHub private reporting](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/configure-vulnerability-reporting/configure-for-a-repository).

## Verification and completion

Each ticket needs focused regressions, relevant checks and separate reviewer-agent
review. Preserve failed baseline evidence and bind corrected evidence to exact source.

- Notifications: maintenance/Unignore, suppression recovery, healthy Ignore, planned
  bounces, repeated incidents, blue configured/absent/closed with hold retained, partial endpoints.
- Effects: refusal, TLS/local rejection, partial write, applied-then-500, lost reply,
  expired deadline and crashes around intent/start/effect/result, with independent invocation counts.
- Storage: supported older formats, exposure migration, separate installations, action growth,
  snapshot/map overlap, failed writes, lock/corruption, authenticated backup/restore and downgrade.
- Inputs/security: parity corpus, source locations, alias/BOM quotas, CA/token failures,
  secret scans, rooted-log attacks and existing auth/authorization invariants.
- Scheduling: 200 hanging HTTP plus healthy TCP; 300 hanging plus 700 healthy with onset
  around 400 seconds; same-/cross-driver cases; stalled versus healthy notification destinations.
- Interfaces: API/SDK/CLI agreement and required real-server default/tagged browser campaigns,
  desktop/mobile/offline/field errors and no secrets.

Performance scenarios run sequentially for at most 15 minutes each with bounded logs
and target accounting. Record cadence, max healthy gap, p99, misses, resources and
post-fault recovery. Healthy-subset success does not establish aggregate SLO attainment.

Freeze one repaired commit, then run affected app/SDK/worker matrices on Go 1.26 and
the pinned compiler: default, applicable built-ins, externaljobs, combined, race,
vulnerability, generation, dashboard, required browsers and documentation/examples.
Required browser tools must fail clearly if absent. Final handoff lists every disposition,
migration/rollback restrictions and remaining native/provider/packaging/scale gates,
separating merge readiness from publication. No tag/image/chart/docs release follows
merely from passing these repairs; private release gates remain in force.
