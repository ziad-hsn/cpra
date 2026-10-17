# Product-review repair progress

Updated 17 October 2026. Work started from
`34ef88aa3dd5ff840285d9f59ac8a95ce7ea6865` on `codex/release-repairs`.
The [approved plan](product-review-repair-plan.md) governs this repair candidate.
Current package names, existing housekeeping, retained evidence and the separate
documentation checkout are preserved. Every module requires Go 1.26; the release
compiler is Go 1.27.2. The 24-hour run remains cancelled.

## Implementation status

The eight implementation areas have independent source review. The reviewer found
no remaining actionable finding in the changed paths after the documented repairs.
This is source review, not native-platform, provider, performance or release approval.
The late-catalog measurement passed its progress/cadence assertions; the final
affected compiler/tag matrix is still pending at this checkpoint. No push, merge,
module tag, image or documentation publication
has been performed by this work.

| Ticket | Implemented and reviewed | Verification boundary |
| --- | --- | --- |
| 1. Incident/delivery/restart | Format 42/43 transitions; episode/destination-specific holds; observed health progresses around unknown interventions; green requires actual prior notification start; new file incarnations; fresh-check restart barrier; original intervention deadlines; stopped action inspection/review | Focused lifecycle, real child-process store/receiver, historical action and actual CLI tests passed. Final compatibility/failure selection remains; unknown provider outcomes are never inferred from healthy checks. |
| 2. Ignore/gray/maintenance | Explicit rolling count/window policy; audited Ignore/Unignore across API/SDK/CLI/dashboard; retained counters/recovery; gray control-transition intents; ECS suppression with durable start guards; captured worker maintenance at offers/starts/heartbeats/results | Focused base/tagged transition tests and real-server browser controls passed. Native time-zone/service environments remain distinct from deterministic DST fixtures. |
| 3. Authentication/secrets/files | Authentication precedes bounded failed-auth throttling; safe environment-token parsing; parsed MySQL local-file denial; rooted notification output with process-level capability; non-secret target projections | Scoped attacks, token markers, rooted-log and target fixtures passed. The executable walkthrough also checked help/flag-error token output. Native filesystem/service identity qualification remains open. |
| 4. Validation/errors | Shared pure driver/monitor validators; reference graph checks; source coordinates through file/SDK/browser spools; bounded aliases; original normalization-v1 continuation and stricter v2 imports; safe typed causes | Full loader/manifest/capability suites and a 49-case SDK/file/management parity corpus passed at their recorded checkpoints. Final variants and retained collection-format checks remain. |
| 5. TLS/provider content | Per-monitor TLS adapters for nine checks, trust/server-name/skip policy and pool isolation; bounded diagnostics; notification identity/content; provider correlation/severity and deliberate Pushover color overrides | Local production-path transport/provider fixtures passed. Local database handshakes and acceptance fixtures do not establish account-backed delivery/effect qualification. |
| 6. Slow-target isolation | Bounded driver/flow dispatch under one global budget; pending/active ages; arrival-based initial sizing and incremental cold-demand floor; runtime queue diagnostics | Both startup outage scenarios and the corrected late-growth case passed cadence/progress assertions after retained failures. The late-growth fault window exceeded latency targets. No fleet or five-minute SLO pass is claimed. |
| 7. Operator interfaces | Common v2 observation reads with stable IDs/incarnations; mode-aware SDK/CLI reads; Ignore/TLS/diagnostic/queue fields; guided five-resource webhook setup; status, errors, layout and permission repairs | Full frontend build/type/lint and 565 tests passed; all eight required real-server browser campaigns passed. Final generated contract and compiler/tag checks remain. |
| 8. First run/docs/contributors | Readable quickstart IDs; one-time import guidance and restart reminder; SIGHUP notice/graceful shutdown; UTC machine output; workspace/editor/fast-test guidance; README/image; public source filtering and exclude_docs; repository ignores | Executable first-run/restart walkthrough and earlier strict rendered-site checks passed. Updated references need a final render. Private vulnerability reporting is still disabled and requires repository-admin configuration. |

## Finding disposition

The original review and checked assessment are retained unchanged. A repair below
means implementation and scoped review/coverage, not completion of release gates.

| Finding | Disposition |
| --- | --- |
| Blocker 1: held delivery suppresses later episodes | Confirmed and repaired: original delivery identity, incarnation, episode and destination scope the hold. Later incidents/destinations remain eligible. |
| Blocker 2: unknown intervention prevents health progress | Confirmed and repaired: health/incident progression is independent of intervention authority; conflicting interventions remain held. |
| Blocker 3: public installation unavailable | Confirmed publication gap. Development uses the documented generated workspace. Public tags remain intentionally withheld until qualification. |
| Blocker 4: failed-auth lockout | Confirmed and repaired; current valid credentials are checked before failure throttling. Same-peer/proxy/table-saturation regressions are present. |
| Blocker 5: private reporting disabled | Confirmed by fresh GitHub readback. No administrative writer credential/tool is configured here; this announcement prerequisite remains open. |
| Blocker 6: quickstart identities and CLI causes | Repaired with readable IDs, typed safe errors, explicit connection instructions and an executed initialization/validate/start/describe/apply/restart walkthrough. |
| M1: worker periodic-maintenance boundaries | Repaired with committed decisions/configuration guards across offer/start/heartbeat/result paths; started outcomes remain accepted without suppressed follow-ups. |
| M2: normal green without prior notification | Repaired using actual notification starts; canceled queued work does not qualify. Ignore recovery has the explicitly selected exception. |
| M3: slow-target starvation | Repaired dispatch/sizing implementation; startup and late-growth measurements pass their bounded cadence/progress assertions. Fault-window latency exceeds targets; final matrix remains pending. |
| M4: validation parity | Shared validators and the reviewed 49-case corpus replace competing target rules. PostgreSQL duplicate URI-host precedence matches pinned pgx; SDK has no provider execution dependencies. |
| M5: source locations and generic errors | Safe field/rule causes and original coordinates survive file/collection/browser paths. Missing positions are unavailable, not fabricated. |
| M6: observation-mode CLI | Supported bounded v2 reads work without fabricated writable catalog resources; access discovery never downgrades failed writes. |
| M7: notification content/provider mappings | Repaired colors, correlation/severity, identity envelopes, diagnostic content and Pushover overrides. Real account/effect qualification remains open. |
| M8: repeated outages | Existing per-incident max_attempts remains intentional. Explicit count/window automatic Ignore and audited manual controls implement the approved addition. |
| M9: bootstrap edits and SIGHUP | One-time import remains intentional, now explained/logged. SIGHUP logs unsupported reload and leaves monitoring running; SIGINT/SIGTERM drain. |
| M10: MySQL/log/target disclosure | Repaired parsed MySQL policy, rooted log destinations and credential-free target projection. The MySQL reproduction requires its documented initialization-query precondition. |
| M11: UI state/language | Repaired distinct health/control/delivery/review displays, field labels, unavailable timestamps, guided setup and layout. Real-server browser coverage passes. |
| M12: public documentation/front door | README and guides rewritten; internal implementation/review/candidate records are excluded from source sync, generated output and search, while retained in the repository. |
| M13: contributor setup | Generated workspace/editor, tool prerequisites, fast tests, regeneration and driver-authoring guidance are present. The fast target passed as a development check. |
| Environment token disclosure | Confirmed and repaired; parsing and executable marker checks passed. |
| Removed/re-added monitors and restart admission | Repaired durable incarnation, fresh-observation, original deadline and local-review boundaries; history and unknown facts remain retained. |
| SDK alias / YAML syntax / VictorOps documentation claims | Assessment corrections retained: SDK aliasing is optional, flow-style YAML is valid, and VictorOps fields were already documented. No workaround added for incorrect claims. |
| Native/DST/service behavior | Deterministic DST and Linux local fixtures are scoped evidence; native platform/service/CSI qualification remains separate. |

## Recorded development evidence

Records are private, under the task's evidence directory, and contain the command,
compiler, source fingerprint, exit status, elapsed time, bounded scratch usage and
log digest. These checks use Linux amd64. Application checks use the generated
workspace; they do not prove public module installation. Earlier failed runs and
the previous progress checkpoint remain retained.

| Record | Result and scope |
| --- | --- |
| product-restart-process-1; product-intervention-deadline-2; product-ignore-interface-1 | Focused lifecycle/restart/deadline/Ignore checks passed. The process case exercises a child store and receiver, not the entire installed application. |
| product-worker-maintenance-3 | Selected tagged worker/maintenance/Ignore/gray checks passed after retained failing runs. |
| product-security-boundaries-3; product-security-tagged-3; product-tls-drivers-1 | Scoped authentication/log/MySQL/TLS/notification checks passed. Earlier failures remain recorded. |
| product-file-field-locations-7 | Full loader, manifest and capability suites passed at source `2a74eb4a8f39360bee2965227a1d392a49e5ca5dd037017b269a8e10025d7696`. |
| product-validation-parity-3 | Final 49-case target/settings corpus and typed management cause checks passed at `ff4adef630edd76468d7d54d23be336163fc9e8e5e999a0ae9ceacd33abbee84`. |
| product-sdk-validation-1; product-sdk-validation-tagged-1 | SDK api/collection packages passed at their earlier recorded checkpoints; the final duplicate-host correction still needs the full tagged rerun. |
| product-monitor-observation-app-3; product-monitor-observation-sdk-1; product-historical-review-summary-3 | Focused HTTP/SDK/CLI reads and historical review summaries passed. |
| product-dashboard-rebuild-6 | Go 1.27.2 / Node 24.21.0 / pnpm 11.22.0 parser, assets, type, lint and 565 tests in 36 files passed. React act warnings remain visible in the log. |
| product-browser-default-3 | All eight required real-server browser tests and both package completions passed without skips in 311 seconds at source `47533bafeb69864d7badce38df788db16a25cac51413cd685e04305dfbef8250`. This run has no race instrumentation and precedes the frozen repair commit. |
| product-local-action-cli-1 | Actual command/store test passed at that same source: exclusive ownership, actor/reason, revision conflict, persisted assertion, preserved unknown facts and zero remote API requests. |
| product-newcomer-3 | Eighteen executable walkthrough steps passed: protected init, validation, local TLS, writes/reads, real health receiver, SIGHUP, bounded shutdown, same-state restart and token-output canaries. Not package/service qualification. |
| product-doc-publication-tests-2; product-doc-render-1; product-doc-boundary-1 | Forty-five publication tests, strict build of 73 HTML pages and 18,008 local links/assets, and zero excluded source/search entries passed at earlier documentation checkpoints. New documentation requires a rerender. |
| product-fast-target-1 | Named fast development target passed. It does not replace the affected race/integration matrix. |

### Outage evidence and retained failures

`product-outage-isolation-3` passed the two startup cases with a real local target,
Raft, snapshots, bounded target-side accounting and GOMAXPROCS=2:

- 200 hanging HTTP checks plus one healthy TCP check: the healthy check completed
  18 observations in 90 seconds; maximum cadence gap 5.017 seconds. All 200 slow
  targets had a timeout-length hang and later recovered.
- 300 hanging and 700 healthy HTTP checks: every healthy target completed 9–10
  observations; maximum cadence gap 10.059 seconds. All 300 slow targets had a
  timeout-length hang and later recovered.

These cadence gaps are not queue-latency percentiles. Ninety-second regressions do
not establish the agreed five-minute SLO or million-monitor capacity.
The earlier `product-outage-isolation-1` failure (34.918-second healthy gap) remains
retained. Run 3 was an overall failure: late growth reached 647 activated monitors
before the harness's 150-second CLI deadline, with storage still ready. Its original
operation handle and receipt remain in the evidence. The harness now records a
bounded 600-second activation allowance separately from the 90-second measurement;
that change is not a measured pass. Missing latency measurements stay null rather
than becoming zero, with explicit rolling-window coverage.

The subsequent `product-outage-growth-4` passed at commit `574de34`: all 999
additions activated in 177.179 seconds, with a 10.128-second maximum seed-monitor
gap. During the subsequent 90-second outage/recovery window, all 700 healthy
monitors completed 9–10 checks and the maximum gap was 10.097 seconds. All 300
slow targets exhibited a timeout-length hang and later recovered. Sampled maxima
of the reported rolling p99 values reached 276.801 ms queue delay and 5.313 s total
latency: this is not an SLO pass. The run completed before a five-minute snapshot
and retained no raw fleet check history. Earlier startup scenarios included snapshots.

`product-doc-render-frozen-1` rendered that commit privately: 73 HTML pages,
18,038 checked internal links/assets, and no excluded engineering records in
the generated files or 1,153 search entries. No documentation branch was changed.

The first affected default/race row at `574de34` was interrupted at the scratch
guard and is not a pass. It also exposed outdated fixtures: missing log-root/A2A
configuration, old dispatch-counter barriers, pre-redaction target expectations,
green eligibility and CLI access-discovery/error text. Test-only corrections
retain exact private job-target assertions, committed fencing, actual notification
starts and the healthy threshold. Their focused race checks passed; generation
fixtures now include the shared validation messages. The full matrix must rerun.

`product-browser-default-1` failed the guided setup fixture, and run 2 was explicitly
interrupted before completion. Both remain failed/partial, independent of run 3.
`product-historical-fence-1` reproduced an old test's writer-format expectation;
only the new-write expectation was updated to format 42, retaining historical
control-fence decoding assertions. `product-historical-fence-2` passed the corrected
check plus historical digest/catalog, target-guard and restore selections.

## Final verification and release boundary

The second default/race row at `a69a517` completed its selected tests, with
2,287 passing and 20 failing test/subtest events, five classified subprocess
entry-point skips, no missing selected tests and no race diagnostic. The failures
were stale regression fixtures: restored A2A work lacked a fresh observation,
notification-log fixtures omitted the new process capability, observation routes
and validation causes retained old expectations, current-writer assertions used
older formats, and maintenance/restart fixtures expired their action deadlines.
The backup cleanup assertion also allowed less time than its bounded retirement
backlog needed under race instrumentation. That failed row remains retained.

Seventeen test-only files correct these expectations. They strengthen original
action identity/deadline assertions across restart, retain historical decoding
checks, require the exact stopped backup generation pair and preserve zero-effect
validation checks. Independent source review closed without an outstanding
finding. Focused race records `product-fixture-regressions-3`,
`product-fixture-restart-probes-backup-1` and
`product-format-external-regressions-2` passed. The latter two use the final test
source fingerprint `653694c55dd2587257811436e86475998816bf2becee0d8333fd1c85ec22dd7b`;
the first predates the final strengthened assertions, which the restart/backup
run then exercised. The all-built-in-plus-worker validation fixture passed in
`product-stage-all-drivers-regression-3`; its two scratch-interrupted predecessors
remain incomplete. These are configuration-contract checks, not provider-account
qualification.

Full SDK default/tagged and worker tagged suites passed on both compilers at
`a69a517`, using race instrumentation on Go 1.27.2. SDK checks use `GOWORK=off`;
the unpublished worker dependency uses the explicit private workspace. This
does not establish public module availability. The next frozen matrix repeats
the affected application and module checks against the corrected source.

At `89e58a1`, the default affected row completed through a separately reviewed
package-level continuation after the original command hit its scratch guard.
The continuation reused only completed packages with matching source, toolchain,
selection and log identities, reran incomplete packages, and ran full-package vet.
This was not an uninterrupted run. The combined row then found outdated worker
poll fixtures: current commands require selection version 2 and captured maintenance
decisions. Historical replay commands retain their original versions.

Expanding the focused regression to every requeue crash boundary also reproduced
a runtime defect: snapshot restoration omitted the derived ready index, and the
first poll prepared no maintenance decisions before Apply rebuilt that index.
Poll preparation now rebuilds it under exclusive state ownership before selection;
maintenance evaluation still happens outside the lock. The crash fixtures now
prove that restarted recovery work remains blocked until a fresh failed check,
without changing its original action or deadline. Separate assertions verify
that restoration and later adoption/start do not invent health observations.
`product-worker-poll-fixtures-1` retains the failure. The first focused repair run,
`product-worker-restart-index-1`, passed with race instrumentation; independent
review then requested the explicit pre-observation assertion retained above.
`product-worker-restart-index-2` passed with that assertion, and the scoped
independent source review closed without an outstanding finding.
The final affected selection includes the newly exposed process-crash case.

At `a8f2213`, the combined affected race row and all-package vet passed with
3,097 passing test/subtest events and nine classified skips. All eight required
real-server browser scenarios passed too. Inspecting their screenshots exposed
an assertion gap: optional integer counters omitted a known zero, displaying
"Unavailable" after an administrator had conclusively reviewed an unknown action.
The two optional monitor-status counters now preserve their presence in both SDK
variants. Configuration-pending or mismatched observations still omit them; a
matching observation emits zero. CLI describe preserves absence and includes
the review-needed count. Required observation fields and storage formats are
unchanged. New serialized-response, SDK and CLI regressions passed; the browser
scenario now asserts one original unknown outcome and zero actions needing review.
Independent source review closed the correction. The failed baseline and two
fixture-setup failures remain recorded separately from the passing checks.
This response change requires a new freeze and affected verification; the earlier
combined and browser passes are evidence for `a8f2213`, not the new candidate.

The next frozen browser run exposed two native-CLI integration assertions that
still compared optional counts as plain integers. Both now require a present
count of one. Independent source review closed the assertion update, and the
explicitly enabled native CLI scenario passed with race instrumentation in
`product-action-counts-cli-integration-2`: 98 checks, one ambiguous external
effect, retained controls/configuration and unknown facts after restart. The
preceding invocation omitted its opt-in and is explicitly recorded as unexercised.

Independent verification review added authentication and worker-exclusion cases
to the affected inventory. Default and combined builds run the full affected
selection on each compiler. Builtins-only and externaljobs-only intermediate
rows run explicit tag-interaction profiles, including controller/queue, driver,
management, authorization and HTTP/CLI contracts. The worker profile retains
maintenance, Ignore, requeue and snapshot boundaries. Required profile tests must
compile and pass; all rows separately compile and vet every application package.
Generic storage/backup repetition omitted from intermediate rows remains covered
by each compiler's full default/combined rows. Intermediate profiles are not
reported as equivalent execution of the full affected selection.

At `48aa591`, the required tagged browser campaigns, SDK/worker race and vet
checks, deterministic generation, private fresh-consumer archive checks and
seven vulnerability scans passed within their recorded scopes. The built-in
scans retained the module-only OpenPGP advisory GO-2026-5932; no affected package
or symbol was imported. These results do not qualify provider accounts or a
public release. The worker tests used the workspace graph.

Preparing a separate worker-module check then exposed stale SDK archive checksums
in the application, worker and examples. A fresh consumer did not inspect those
source modules' own checksum files, and workspace tests bypassed that boundary.
The application matrix was deliberately interrupted; its completed package logs
remain partial evidence, not a passing row. Dependency preparation now includes
the examples after finalizing the SDK and worker payloads. Consumer verification
also requires the original modules' version pins and both checksums to match.
Go generated the three checksum-file proposals in an isolated source copy;
dependency versions and module definitions are unchanged. The 49 preparation and
archive regression tests passed, and independent source review closed the changes.
The frozen archive and worker checks must pass with the corrected source before
the remaining matrix can qualify it. No unpublished module was tagged to resolve
the mismatch.

At `ec2bbae`, private archive consumers, both independent modules on both
compilers, standalone worker dependency checks, frontend regeneration, public
documentation filtering and Helm 3/4 render contracts passed. Default and
built-in application rows passed on Go 1.27.2 with race instrumentation. The
external-worker row found two older crash fixtures that expected operational
restart to leave the monitor unchanged and immediately adopt a queued recovery.
Operational restart intentionally commits a fresh-check barrier. Stopped replay
must still preserve the original records exactly.

The corrected fixtures distinguish those two paths. They require unrelated work
to remain adoptable while the original recovery is held, preserve its complete
record during the blocked adoption, then require a fresh failed check before
adoption with the original intent and commit position. The focused process-crash
regression passed with race instrumentation in
`product-worker-crash-barrier-fixtures-2`; independent source review closed the
assertions. No runtime behavior changed. The failed external row remains failed
evidence. The final affected application matrix runs again at the resulting
clean commit rather than treating the earlier passes as executions of that commit.

At `91c150a`, the actual repository documentation contract check found a stale
archived schema reference for the optional action counters. Public rendering and
link checks had passed because engineering references are excluded from that
output. The reference generator refreshed the source copy and its two inventory
entries; independent review verified byte identity with the canonical schema.
The focused repository documentation check then passed. This changes no runtime,
schema or public page content. The failed documentation record remains retained.
The combined application row also stopped at its scratch guard during cold
compilation, before producing a test verdict. Its abandoned temporary compiler
output was removed; completed reusable compiler cache and all logs were retained.

Before merge handoff, freeze the reviewed repair source and complete the affected
application matrix on Go 1.26.0 and 1.27.2 with default, built-in, externaljobs and
combined variants, appropriate race checks, full SDK/worker suites, generation,
vulnerability, browser, documentation and revised growth checks. Independent review
expanded the selection to historical command/digest formats, controller ownership,
original collection bindings/retained diagnostics and server-worker composition.
Large pure graph cases run once per compiler, rather than repeating identical
untagged work in every tag combination. Actual selected tests/skips must be recorded.
This affected repair matrix is separate from complete release qualification.

GitHub main readback on 17 October was
`b293f34dd57ee61c78371643de6d0ebaa75c626e`, an ancestor of the local baseline.
Native OS/architecture execution, services/packages/CSI, designated provider and
agent accounts, and comparative fleet qualification remain open where infrastructure
is absent. Private vulnerability reporting remains disabled. Candidates, evidence
and artifacts stay private, and the cancelled 24-hour run is not reinstated.

At `4701900`, the combined race row reached the explicitly enabled large-input
cases and found two invalid fixtures: the near-limit endpoint update and the
large notification graph supplied protected webhook URL/header fields inline.
The endpoint case failed during setup, before staged validation. The failed row
remains retained. A separate diagnostic execution of its twelve unexecuted
packages passed with 1,237 test/subtest events and six subprocess-helper skips;
that diagnostic does not complete the failed row or its vet gate.

The test-only correction uses credential references and JSON-object text for
header credentials. Near-limit credential envelopes remain covered; the endpoint
update now exercises a near-limit resolved configuration. The notification graph
contains 83 resources, including 40 distinct header credentials with 36,864,000
payload bytes (35.15625 MiB), plus resource-envelope overhead. It checks unique
identities, all dependency edges, bounded released plaintext reservations, no
committed writes, no sealing and zero target calls. Independent source review
closed the proposal. `product-large-validation-fixtures-1` passed both complete
large-input tests with Go 1.27.2 race instrumentation in 555.646 seconds, with
six passing test/subtest events and no skips or race reports. Production
validation and the credential policy did not change.

The next freeze runs the standard combined selection and its three expensive
collection/worker-process cases in separate, explicitly recorded invocations
on each compiler. Their union must equal the full combined selection. The
private runner's aggregate allowance is 45 minutes per invocation; the
25-minute package test timeout, 35-minute command cap, output limit and scratch
guard remain intact. Independent review accepted this scheduling boundary.
These are scoped correctness checks, not performance or release qualification.
