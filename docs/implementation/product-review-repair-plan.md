# CPRa product-review repairs and finalization

Approved 16 October 2026. Baseline: `codex/release-repairs`,
`34ef88aa3dd5ff840285d9f59ac8a95ce7ea6865`, with existing housekeeping preserved.
This addendum follows [release repairs](release-repair-plan.md) and
[dashboard shipping](dashboard-shipping-plan.md). Progress and evidence belong in
[the product repair record](product-review-repair-progress.md).

## Scope and decisions

Repair the confirmed product-review findings, retaining corrected dispositions
from the checked assessment. Preserve package names, single-node Raft, optional
driver tags, existing evidence and the separate documentation checkout. Every
module keeps Go 1.26 as its minimum. The pinned release compiler is Go 1.27.2.
No 24-hour run, public candidate, module tag, image or documentation publication
is authorized by completion of these repairs. Existing private qualification
gates remain applicable.

* Normal green follows the healthy threshold and an actual yellow/red/cyan
  attempt in the same episode. A canceled unsent notification does not qualify.
* An unknown notification holds that delivery, not later incidents or other
  destinations. An unknown intervention prevents conflicting intervention, but
  does not prevent observations, incident transitions or eligible notifications.
* `intervention.max_attempts` remains a per-incident limit, default one.
* Optional `auto_ignore` / `autoIgnore` requires explicit `incidents` (2–1,000)
  and `window` (positive, at most 30 days). Count distinct threshold-reaching
  outages; retain a bounded rolling timestamp set only for configured monitors.
  Ordinary recovery does not reset this count. Enter Ignore before admitting
  the triggering incident's ordinary notifications or interventions.
* Ignore preserves checks, actual health and history; suppresses ordinary alerts
  and interventions; cancels queued work; preserves started/unknown evidence.
  Verified recovery admits one eligible green, clears Ignore and resets the
  counter without waiting for delivery. This green does not require an earlier
  alert attempt. Other controls still apply. Unignore is explicit and audited.
* Gray describes actual enabled/disabled transitions, remains eligible during
  Ignore and maintenance, and is not emitted for unchanged startup or replay.
* Manual Ignore/Unignore requires a reason, authority and version precondition
  across API, SDK, CLI and dashboard.
* Stopped local action review preserves provider facts and records an
  administrator assertion. It starts no jobs or controller.
* Per-monitor TLS `insecure_skip_verify` defaults off. It does not skip recovery
  health verification, select cleartext transport or weaken API authentication.

## Ordered implementation tickets

### 1. Incident progression, delivery identity and restart

Separate observed health/incident progression from intervention execution
authority. Scope notification holds and deduplication to incarnation, episode,
logical delivery and endpoint identity, preserving retries' original identity.
Persist actual alert exposure for green eligibility. Restored queued recovery
requires a fresh observation, matching original target/policy/deadline; cancel
on health. File removal/re-add gets a fresh incarnation without erasing history
or ambiguous outcomes. Add `cpractl local actions` and `local review action/ID`
under exclusive storage ownership, exact review revision, actor, reason,
executor fencing and existing evidence rules. Generic HTTP 500/timeouts remain
uncertain unless rejection is proven.

Acceptance: retained second-outage cases pass; unknown recovery does not block
incident closure; conflicting effects remain held; restart/review causes no
unintended provider call. Reference: [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.2).

### 2. Ignore, gray and maintenance (depends on 1)

Implement the decisions above, including canonical schemas, `/ignore` and
`/unignore`, discovery, permissions, audit and every client. Persist Ignore
cause/actor/reason/counter/recovery progress; configuration edits do not clear
it. Add an ECS control component without excluding checks; durable start guards
remain authoritative. Gray uses a narrow control-notification identity and
exception. Apply suppression to worker offers/starts/result follow-ups and
built-in jobs alike. Committed commands capture maintenance/configuration
identity; replay never consults the wall clock. Test queue/window crossings,
DST, restarts, configuration changes and long-lived assignments. Do not release
suppressed backlog on exit.

Acceptance: restart preserves Ignore, checks progress, recovery green is admitted
once, automatic resume works, gray works during suppression, workers cannot
bypass it. Reference: [Nagios flap handling](https://assets.nagios.com/downloads/nagioscore/docs/nagioscore/4/en/flapping.html).

### 3. Authentication and secret/filesystem boundaries

Authenticate against current authority before failed-auth throttling. Valid
credentials must survive same-peer/proxy/table saturation. Bound failed-entry
expiry/eviction; use 429 with bounded Retry-After for throttled failures, 401 for
invalid authentication and 403 for denied authority. Preserve origin, revocation,
proxy and scoped-worker checks without a stale success cache. Resolve environment
bearers after flag parsing. Reject parsed MySQL `allowAllFiles`, including alternate
boolean encodings and initialization queries. Confine log notifications to a
process-configured `os.Root`; reject traversal, unsafe file types and protected
configuration/state. Public target summaries must come from non-secret inputs,
never resolved credential URLs (including their paths).

Acceptance: authenticated probes survive attack fixtures, output contains no
token markers or credential-derived targets, MySQL canary/log attacks fail.
References: [OWASP](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html),
[429](https://www.rfc-editor.org/rfc/rfc6585.html#section-4),
[os.Root](https://go.dev/blog/osroot),
[MySQL](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#allowallfiles).

### 4. Shared validation and errors

Extend existing pure validators for files, validate-only, API, collection and
dashboard. Validate complete references, counters/defaults, colors, UDP payload,
HTTP methods/statuses, DNS servers, addresses, Pushover and Telegram conflicts.
Reject unsupported recovery kinds/actions/platforms and health-only gRPC fields
in TCP mode before opening storage. Document zero normalization, expiry defaults,
cadence/timeout/no-overlap behavior; reject sub-resolution intervals. Preserve
source/document/resource/field/line/column through spooling without secret values.
Validate-only must not inspect legacy state directories. Carry safe typed input,
TLS/auth/unavailability/readiness errors and request/operation IDs across clients.

Acceptance: one justified corpus agrees across paths, gives safe locations,
invokes no provider, and malformed final input activates nothing. Reference:
[RFC 9457](https://www.rfc-editor.org/rfc/rfc9457.html).

### 5. TLS and notification/provider content (depends on 1, 4)

Preserve existing HTTP/TLS skip fields; extend explicit TLS options to gRPC,
Redis, PostgreSQL, MySQL, MongoDB, RabbitMQ and Kafka. Reject them on non-TLS
checks. Trust roots/server names are supported; contradictory DSN/typed settings
fail rather than weakening verification. Preserve explicit transport choices;
new TLS verifies by default. Isolate transports/pools, display disabled
verification and retain configured expiry checks. Add bounded typed diagnostic
codes/values without remote error strings/bodies. Repair color wording/subjects,
webhook identities/timestamps, Splunk correlation and Datadog severity. Preserve
custom recovery webhook bodies. Pushover green/gray emergency paging requires
deliberate per-color override, not inherited emergency settings.

Acceptance: trusted/self-signed/expired/wrong-host tests, monitor isolation and
provider production-path fixtures pass. References:
[Go TLS](https://pkg.go.dev/crypto/tls#Config),
[Redis](https://redis.io/docs/latest/develop/clients/go/connect/),
[PostgreSQL](https://www.postgresql.org/docs/18/libpq-ssl.html),
[Splunk](https://help.splunk.com/en/splunk-enterprise/alert-and-respond/splunk-on-call/integrations-with-splunk-on-call/rest-endpoint-integration-for-splunk-on-call).

### 6. Slow-target isolation

Retain Erlang C/Allen–Cunneen, worker limits, five-second evaluation, twofold
growth and healthy reduction hold. Include active ages and oldest pending work;
bound per-driver pulse queues/reservations inside one global worker budget with
fair within-driver dispatch and bounded active-flow bookkeeping. Apply backlog
relief with configured arrival demand. Count dequeued-but-not-running work as
pending. Expose driver saturation, ages, active work, timeouts, misses and limits.

Acceptance: repeat 200 hanging HTTP + healthy TCP and 300 hanging + 700 healthy,
including same-driver mixtures. Healthy work progresses without the reported
minute-long gaps. Record cadence, p99, maximum gap/resources/recovery and retain
failed measurements. No general SLO guarantee. Reference:
[Kubernetes fairness](https://kubernetes.io/docs/concepts/cluster-administration/flow-control/).

### 7. Operator interfaces (depends on 1–6)

Expose supported v2 observation in management and observation modes using bounded
read representations, without fabricated writable resources/versions. Discovery
selects supported reads, never downgrades writes on authorization failures.
Synchronize base/tagged canonical contracts, SDK and inventory. Distinguish health,
Ignore/disabled/snooze, pending/completed deliveries and review-needed actions.
Correct configured-notification/count/time labels. Add guided notifications via
existing secret/endpoint/recipient/monitor collection validation and retain handles
and partial results. Repair demonstrated layouts, import wording, mobile controls,
placeholders and secret toggles. Distinguish attempts/retries/thresholds. Improve
YAML/help/version/environment/connection guidance while preserving audit and CAS.

Acceptance: API/SDK/CLI/dashboard agree; observation reads work; controls enforce
authority/revision; mandatory real-server browser tooling fails closed if absent.

### 8. First run, contributors and public documentation

Use explicit IDs and execute init/validate/start/describe/apply/restart examples.
Explain bootstrap once/apply and log committed-catalog startup. SIGHUP reports
unsupported reload; SIGINT/TERM drain with a shutdown-start message. UTC machine
times/local UI, names plus IDs, appropriate startup logging. Document generated
uncommitted editor workspace, GOWORK=off distributed checks, Python/tool versions,
regeneration and driver development. Add fast tests without weakening full tests.
Rewrite user-facing README/support/versioning/screenshot. Exclude engineering
records from public output/search while preserving repository sources; repair
links. Enable and verify private vulnerability reporting before announcement.
Remove tracked caches and ignore local-agent files, preserving assets/licenses
and other users' files, refs and evidence.

Acceptance: newcomer path, development setup, strict docs/links, actual publication
filtering and reporting channel work. References:
[Go workspaces](https://go.dev/ref/mod#workspaces),
[MkDocs exclusion](https://www.mkdocs.org/user-guide/configuration/#exclude_docs),
[GitHub private reporting](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/configure-vulnerability-reporting/configure-for-a-repository).

## Verification and completion

Every blocker, M1–M13 and selected minor finding receives a checked disposition
and evidence; incorrect review claims are corrected, not implemented. Reserve
formats 42/43 for base/tagged additions, preserving historical replay and using
committed migration. Test validation/digests/snapshots/restore/old reads/refusal;
migration invokes no provider. Exercise crashes at admission/start/effect/result,
review, repeated incidents, Ignore, gray, maintenance/configuration and re-add.

Run focused checks and independent review for each ticket, then the affected
application/SDK/worker matrix on Go 1.26 and 1.27.2, default/built-in/externaljobs/
combined, appropriate race/vulnerability/generation/frontend/browser checks at
one frozen repair commit. Keep fixtures, native, account and fleet evidence
distinct. Finish with a merge handoff and accurate open release gates. Missing
native/account/large-campaign infrastructure remains visible, not inferred passed.
