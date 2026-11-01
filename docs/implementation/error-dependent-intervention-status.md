# Error-dependent intervention implementation status

Started 23 October 2026 from `codex/release-repairs` at
`967f9cf8b5733a9f93c56ceaf8712a748cdf5e08`. Updated 25 October 2026.
The pre-existing 71-line `internal/jobs/a2a_job_test.go` addition is preserved.
This is an uncommitted private repair candidate, not a frozen release candidate.

## Implemented behavior and review

| Checkpoint | Implementation | Independent review |
|---|---|---|
| Contract and classifiers | Versioned safe failure registry; at most 32 ordered rules; first match; explicit no-action; full-branch validation; file/API schemas; built-in typed observations; normalization profile v4. | Scoped core and surface reviews completed. |
| Execution and persistence | Formats 48/49; same-rule qualification; first-start selection; conditional manual recovery; original bindings and budgets; immutable policy sharing; lazy preparation of the selected job. | Core, historical compatibility, real-process and final admission-guard reviews completed. |
| External workers | Protocol 2 declarations/reports; original JobType binding; identical receipt replay; explicit encrypted journal 2-to-3 conversion. | Journal/worker review completed, including protected evidence and interrupted conversion. |
| Operator interfaces | SDK models; taxonomy discovery; decision observations; offline `rules test`; dashboard editing, ordering, conditional diffs and selection explanations. | Surface review completed; required browser scenarios exercised on both compilers. Final post-guard tagged release-compiler race campaign passed all ten scenarios. |
| Documentation | Public guide, disabled examples, migration instructions, generated SDK/source copies and embedded dashboard. | Documentation and final independent evidence reviews completed. |

Rule classification does not grant retry permission. Unknown effects remain held;
health recovery does not prove an intervention succeeded. No escalation, implicit
write downgrade, check-now operation or release publication was added.

## Corrections from final integration checks

- Preserve the configured external-notification driver even when its local
  executable job is nil. Ordinals, original source pins, zero local queue keys
  and restart behavior are covered by the catalog integration regression.
- Test new notification conditions with their captured execution budget and no
  pre-start absolute deadline. Expired-attempt fixtures now first commit a start
  and a proven retryable no-effect result; their original deadline remains fixed.
  Duplicate worker starts preserve the original started disposition and grant.
- Give cursor-allocation fixtures distinct observation times instead of relying
  on nondeterministic UUID ordering for identical timestamps.
- Exercise both policy-cache boundaries: 16,384 interned entries and 64 MiB of
  accounted interned program payload. Private fallback, release, snapshot pins
  and later readmission are tested. This is not a total heap or private-policy cap.
- Require a valid configuration hash when admitting a rules-enabled durable
  monitor. Previously an invalid revision could survive admission/start and fail
  snapshot validation. Ruleless historical state and diagnostic-only state retain
  their supported opaque revisions. The rejection has no partial admission.
- Add real Raft log-only and snapshot authenticated stopped backup/restore cases
  for base and tagged rule state. Preserve policy, decision, action bindings and
  JobType declarations; restored queued work is canceled and started work is held
  unknown under the existing restore contract. Update three current-writer format
  assertions to 48/49; historical digest/replay fixtures remain unchanged.

## Evidence scope and source identity

Evidence is retained in the private task evidence directory. A command record
contains the compiler, source HEAD, tracked-diff hash, untracked-file hashes,
command, logs, result and sampled resource usage. **Both** the tracked diff and
untracked hashes are required: the new rule implementation is untracked, so the
tracked-diff hash alone does not identify it. End-of-run source/log rechecks are
recorded separately. `oct25-rules-final-runtime-source-recheck-1.json` matches
the final minimum-compiler race, release-compiler delta, browser and failed scan
records to the same code and their log hashes. Documentation updates are not
attributed to earlier tests.

All execution below is Linux/amd64 on the current WSL host, using a reviewed local
workspace for the unpublished nested modules. These are development checks, not
downloaded-module, native-platform, provider-account or frozen-release evidence.

| Evidence | Result and boundary |
|---|---|
| `oct24-rules-dashboard-build-3` | Passed: 633 frontend tests, type checks, lint, parser parity and rebuilt embedded assets. |
| `oct24-rules-browser-campaign-base-3`, `oct24-rules-browser-campaign-tagged-1` | Passed: all ten real-server scenarios on Go 1.27.3, normal default/tagged builds. |
| `oct24-rules-browser-minimum-base-1`, `oct24-rules-browser-minimum-tagged-1` | Passed: all ten minimum-compiler default/tagged browser scenarios with race detection. |
| `oct25-rules-browser-release-tagged-race-1` | Passed: all ten real-server scenarios with Go 1.27.3 and `externaljobs -race`, before the final admission-hash guard. The post-guard refresh below passed. |
| `oct25-rules-browser-release-tagged-race-final-1` | Passed after the final guard: all ten required real-server browser scenarios on Go 1.27.3 with `externaljobs -race`; 337.85 seconds. Desktop and mobile rule screenshots inspected. |
| `oct24-rules-crash-boundaries-2` | Passed: six real Store/Raft process-kill boundaries using the production webhook driver and independent receiver counts. Checks rule, deadline, attempts and unknown holds; not every crash instant, controller dispatch or power loss. |
| `oct24-rules-manual-preconditions-2` | Passed: Store/FSM managed-catalog checks reject missing/stale rule and version conditions and obsolete queued starts. This specific test does not claim HTTP authorization or external execution. |
| `oct24-rules-affected-release-base-1` | Passed: Go 1.27.3 affected default package suites, explicit persistence selection and vet. Not all application packages/tests. |
| `oct25-rules-minimum-combined-final-1`, `oct25-rules-minimum-combined-final-2` | Partitioned Go 1.26.10 all-built-in-plus-external checks. The first run hit the scratch guard after successful jobs, manifest, controller, fleet and management suites; the second passed the unfinished full HTTP/CLI suites, explicit persistence selection and affected vet. The interrupted aggregate is not a pass. |
| `oct25-rules-systems-combined-3`, `oct25-rules-tagged-integration-2`, `oct25-rules-persistence-release-combined-1` | Go 1.27.3: corrected full controller systems, full tagged HTTP suite and explicit combined persistence selection/vet passed. The integration aggregate also contains a failed earlier systems fixture and is not itself a passing aggregate. |
| `oct25-rules-release-builtins-focus-1`, `oct25-rules-release-external-focus-1` | Passed: focused built-in-only and external-only application variants on Go 1.27.3. |
| `oct25-rules-release-base-race-1`, `oct25-rules-release-external-race-1` | Passed: focused base/tagged application race checks before the final admission-hash guard. |
| `oct25-rules-minimum-external-race-final-1` | Passed after the guard and final fixtures: focused application/controller/API/CLI checks, stopped backup cases, explicit persistence selection and affected vet on Go 1.26.10 with `externaljobs -race`; 415.07 seconds. |
| `oct25-rules-release-final-delta-race-1` | Passed: final admission/rule/backup delta, base and tagged race tests and vet on Go 1.27.3; 555.74 seconds. |
| `oct24-rules-nested-minimum-race-1`, `oct25-rules-nested-release-race-1`, `oct25-rules-nested-release-1` | Passed: complete SDK default/tagged and worker tagged suites; both pinned compilers covered with race checks, plus the release-compiler ordinary run. |
| `oct24-rules-worker-exclusion-minimum-2`, `oct25-rules-worker-exclusion-release-1` | Passed: default/optional extension exclusion checked on both compilers through source, dependency, compile and route boundaries. |
| `oct24-rules-repro-generation-1`, `oct24-rules-module-verification-1` | Passed: two-root pinned SDK generation matches checked outputs; application, SDK and worker dependency checksums verified. |
| `oct24-rules-release-python-3`, `oct24-rules-sdk-tooling-tests-1` | Passed: 718 release-tool tests and 50 SDK tooling tests. These do not establish publication/signing. |
| `oct24-rules-docs-final-3` | Passed: strict private site/link build, 57 content pages, 16 redirects, 20,228 internal links/assets without errors; canonical source check covers 190 Markdown pages and 57 navigation pages. Separate documentation checkout untouched. |

The 0/500/5,000 retained-action regression checks that below-threshold rule
qualification does not access historical action storage and that current-monitor
cloning stays at four allocations. The isolated matcher benchmark measured
53.05 ns/op for one rule and 352.1 ns/op for 32 rules, both zero allocations, on
Go 1.27.3/Linux amd64. Neither result establishes fleet capacity or total memory.

The tagged backup fixture validates storage of the original protocol-2 JobType
and custom match contract using a canonical built-in observation allowed by that
contract. It does not bypass custom-report validation. Production structured
report ingestion and receipt replay are covered by separate worker tests.

## Security scan status

The validated partitions in `oct25-rules-vulnerability-minimum-final-report-1`
and `...-report-2` cover all seven application/SDK/worker build variants on
Go 1.26.10 **before** the final admission-hash guard. They report no imported or
called vulnerability. Both all-built-in variants report required-module-only
`GO-2026-5932` for unused `golang.org/x/crypto/openpgp`; this is not zero findings.

The first report remains an incomplete aggregate because another partition lacked
valid database metadata. Only its three validated variants are counted, alongside
the four validated variants in report 2.

Current-source refreshes `oct25-rules-vulnerability-minimum-final-3` and `-4`, and
`oct25-rules-vulnerability-release-final-1`, failed reaching the official database
(`vuln.go.dev/index/modules.json.gz`, connection timeout). They produced no
completed current-source variant. **The current-source security gate remains
open**; a failed request is neither a clean scan nor a new code vulnerability.
A final bounded HTTPS probe also timed out on both official database indexes.
Database metadata validation and TLS verification were not bypassed.

## Independent review and retained failures

Core and checkpoint review records include:

- `oct23-intervention-rules-core-review-2.json`, `oct23-rules-historical-tests-review-3.json`.
- `oct23-worker-failure-journal-review-2.json`, `error-rules-operator-surface-review-4.json`.
- `oct24-rules-crash-profile-review-1.json`, `oct24-rules-manual-preconditions-source-review-1.json`.
- `error-rules-checkpoint5-doc-contract-review-3.json`.
- `oct25-rules-integration-corrections-source-review-1.json`, `oct25-rules-expiry-fixtures-source-review-1.json`.
- `oct25-rules-backup-fixture-oracle-review-2.json`.
- `oct25-rules-status-final-evidence-review-1.json` verifies the final browser results, source and screenshot hashes, cleanup wording and open gates.
- `oct25-rules-status-draft-evidence-review-1.json` verifies the draft scopes, completed compiler results and source/log identities.
- `oct25-rules-revision-admission-fix-source-review-2.json` closes the final guard/fixture source findings without claiming reviewer-executed tests.

Failed baseline and interrupted records are retained: invalid notification-budget
fixtures, stale profile and current-format assertions, invalid backup fixtures,
the reproduced missing admission invariant, earlier test-time/scratch-limit stops
and scanner failures. Corrected runs do not rewrite those records. Successful
parts of failed aggregates are identified by package/scenario, not promoted to a
full aggregate pass.

## Handoff and upgrade boundary

Implementation and the final rule, backup and browser regression checks are
complete; this status update has passed independent evidence review. No unconditional merge
readiness is claimed while the current-source security refresh is unavailable.
The full affected matrix has not been run against a clean frozen commit. After
review/commit, preserve the exact source inventory and run the required frozen
candidate gates; do not relabel this dirty-tree evidence as frozen qualification.

Take a complete stopped authenticated backup before a server upgrade. Formats
48/49 cannot be opened by older readers; rollback requires a compatible backup.
Worker journal format 2 requires explicit stopped conversion to 3, retaining the
original encrypted backup/key, identities, pending outcomes and unknown evidence.

Native-platform, provider-account, packaging, downloaded-module and larger-capacity
qualification remain separate. This work publishes no source/module tag, image,
chart or documentation release. There is no million-monitor or 24-hour run.
Task scratch uses a sampled 6 GiB limit with a 64 MiB reserve. Cleanup records
identify only task-owned disposable caches/fixtures; retained evidence and other
worktrees remain intact. Reclaimed WSL allocation is not claimed as measured
physical host free-space recovery. The final cleanup record,
`oct25-rules-final-disposable-cache-cleanup-1.json`, records cleanup of task Go/browser caches previously
accounting for 1,801,711,616 bytes of allocation. An empty Go cache directory
remains. Sources, evidence, fixtures and other
worktrees were preserved.
