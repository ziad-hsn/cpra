# 22 October repair dispositions

Baseline: `60cf271036e681f5cb3b771d2599315066a8f946`. Updated 2026-10-23 (unfrozen development checkpoint).
Plan: [approved addendum](product-review-repairs-2026-10-22.md).
Source/retained-evidence confirmation is distinct from a new passing regression.
The repairs have focused development evidence. Frozen-candidate verification
remains open; this document does not qualify the candidate for merge.

| Finding | Current disposition | Ticket / required evidence |
|---|---|---|
| R01 | Start-budget/condition repair implemented; focused checks pass; first real burst retained as failed because additional timeout episodes occurred; corrected fixture runs pending | 1: bursts, delayed start, no deadline renewal |
| R02 | Explicit pre-40 binding and bounded later-format ledger migration implemented; default/tagged focused checks, actual `b293f34`/`0a22680` stores and process-crash restoration pass | 2: old stores, original keys, no duplicate effects |
| R03 | Owner availability projection implemented; current/last-observed health, maintenance and metrics regressions and focused real-browser checks pass | 6: unavailable current state, retained observation |
| R04 | Pending original closure implemented; maintenance/later-outage regression passes | 1: original closure after suppression/later outage |
| R05 | Mixed: unknown-effect hold intentional; exhaustion reasons, unconditional local import warnings and bounded blue-condition detail reads pass focused storage/interface/frontend and real-browser checks; full frozen checks remain open | 1,6: actual terminal reason, warnings and pending closures |
| R06 | Private validated staging and stopped inspected discard implemented; focused library and actual CLI root-command checks pass, with independent source review | 4: typo correction, stopped safe discard |
| R07 | Bounded structured details and original phase/handle implemented; focused checks pass | 4: bounded causes, actual phase/handle |
| R08 | Pressure reclamation implemented including partial-byte quota; focused tests and independent source review pass | 5: pressure, in-flight pins, replay/410 |
| R09 | Fence-aware relevance index implemented; fixed relevant visits at 0/500/5,000 ordinary history pass | 3: fence-aware relevant indexes, 0/500/5,000 |
| R10 | 90-day retirement, generation compaction and bounded envelope checks implemented; native Linux allocation/reopen, process-crash, receipt and authenticated backup checks pass; final matrix and other platforms pending | 3: 90-day dependency-safe retirement and startup |
| R11 | Controller maintenance projection and measured virtual rows implemented; focused projection and real-browser layout checks pass | 6: controller projection and real browser layouts |
| R12 | 4 MiB decoded batches and pre-expansion accounting implemented; focused quotas pass; retained 300/600-alias cases finish inside explicit memory/time limits | 4: byte bounds and retained alias fixtures |
| R13 | Three attempts and 45-second budget are independent limits | 1: preserve caps, expose exhaustion; no new cycle |
| R14 | Unknown recovery hold and stopped file review are intentional | 1: retain effect-safety and review regression |
| R15 | Incident IDs local; other correlation identities already distinct | 2,7: document scope; do not rewrite historical IDs |
| R16 | Private log-root `0700` setup and specific safe cause implemented; focused local filesystem tests pass | 5,7: 0700 setup and safe cause |
| R17 | Native opened-handle policy implemented, including refusal to repair supplied Unix token permissions; focused Linux tests, independent review and Windows/Darwin cross-builds pass; privileged/native deployment evidence pending | 5: native opened-handle checks and explicit mount policy |
| R18 | Empty log filename intentionally writes stdout | 5,7: documentation and regression |
| R19 | Engineering records intentionally retained; private rendered output and search exclusion checks pass | 7: verify output/search exclusion, preserve records |
| R20 | Scoped product-page corrections and independent source review complete; strict private rendering/link checks pass after collection/detail changes; final token guidance awaits frozen re-render | 7: product documentation/support matrix |
| R21 | Reporting disabled and main policy absent at 22 October readback; 23 October connector reports admin permission but exposes no settings mutation operation; attempted public refresh could not connect | 7: enablement/form verification open; main policy follows separately authorized merge |
| R22 | Description typo editorial; topics intentional; repository-description mutation unavailable through the configured connector | 7: description correction remains open |
| R23 | Original pulse_check spelling/coordinates verified; known propagation losses repaired with regressions | 4: preserve original spelling/locations where reproduced |
| R24 | Broad claim remains unproven; bounded ten-error aggregation/truncation implemented and tested | 4: bounded ordered details, not unbounded aggregation |
| R25 | SDK and runtime JSON BOM acceptance verified by explicit tests; no new decoder defect claimed | 4: check agreed input corpus |
| R26 | Typed auth/permission diagnostics implemented for six DB/broker tags; bounded loopback/typed fixtures pass | 6: no arbitrary provider strings/bodies |
| R27 | Automatic transitions verified; missing manual-control logs repaired; automatic/manual logging regressions pass | 6: structured transitions retain actor/reason restrictions |
| R28 | Confirmed layout/warning findings pass focused browser checks and manual image inspection; full frozen campaign pending; secret concealment intentional | 6: actual layouts, input-only reveal, no stored secrets |
| R29 | Restart-only green suspicion not isolated | 1: retain as unconfirmed; do not conflate with R04 |

## Development evidence

Records are retained privately under `cpra-review-repairs-2026-10-20/evidence`.
Each runner JSON records the source baseline, diff/untracked hashes, compiler,
command, logs and scope. These are development checks, not frozen qualification.

- `oct22-bootstrap-input-3`: management, loader, local administration, CLI and SDK
  collection tests pass. Earlier `-1` ownership-guard failure and `-2` unrelated
  compilation failure remain retained.
- `oct22-cursor-token-6`: cursor, partial-byte quota, token, service-rendering and
  initial projection checks pass. Its jobs filter selected no tests.
- `oct22-action-storage-3`: relevance, retirement, compaction/pins, review and
  snapshot/corruption checks pass. Subsequent dependency additions require rerun.
- `oct22-notification-start-1`, `oct22-notification-resolution-1`: start budgets,
  original deadlines and pending closure conditions pass.
- `oct22-delivery-upgrade-1`: parent migration/job regressions pass, but the combined
  command failed a storage fixture assertion; that fixture passed the later
  storage run. This record is not reported as an overall pass.
- `oct22-provider-diagnostics-1`: six optional DB/broker tags pass typed diagnostics
  and bounded local protocol checks. No production account is implied.
- `oct22-worker-start-budget-1`: compilation failed while shared projection source
  was incomplete; tagged persistence tests did not run. `oct22-conditions-tagged-2`
  subsequently passed the selected persistence/controller/job/HTTP checks; its
  management fixture required a catalog guard and passed a later focused run.
- `oct23-historical-upgrade-1`: hash-verified stores created by the exact historical
  `b293f34` and `0a22680` sources pass candidate consumption, original correlation,
  unknown-hold and snapshot/restart checks. Original stores remain unchanged; no
  provider was invoked.
- `oct23-surface-logs-projections-1`: management and HTTP projection/manual-log/metrics
  checks passed; the automatic-log test exposed a stale ECS pointer in its test
  helper. After copying the monitor identity before the archetype move,
  `oct23-surface-logs-projections-2` passes that controller regression.
- `oct23-release-script-contracts-1`: 32 release-recipe and package-contract tests
  pass, including base/tagged format46/47 metadata. This is not native packaging
  qualification.
- `oct23-notification-burst30-1`: the first outage delivered all 30 red and 30 green
  notifications; eight red deliveries waited more than 45 seconds before their
  first start, without consuming their execution budget. The whole run failed:
  later timeout episodes produced two further red/green pairs. Receiver records
  contain 64 distinct action/delivery identities, not repeated invocations of one
  action. The fixture also recorded clock discontinuity and used a longer recovery
  observation than the retained scenario. Original evidence is unchanged; revised
  fixture/accounting tests do not turn this workload result into a pass.
- `oct23-alias-memory-x300-2` and `oct23-alias-memory-x600-1`: the exact retained
  inputs complete validation without creating a store, under a 2,000,000 KiB
  virtual-address-space limit and a 60-second deadline. Recorded peak RSS is
  48,564 KiB and 46,872 KiB, respectively. The binary is identified by its earlier
  Go 1.27.3 development build record; the output does not independently report
  parsed monitor count. These are bounded-memory measurements, not matched old/new
  compiler comparisons or complete fleet-coverage evidence. The original x300
  wrapper result remains failed because it incorrectly expected only rejection.
- `oct23-resolution-retention-2` and `oct23-worker-deadline-contracts-1`: focused
  condition cleanup, tagged digest compatibility and worker start-deadline/journal
  tests pass. A subsequent independent review found a recovery fallback could
  recreate a canceled condition against replacement destinations. The subsequent
  correction resolves only previously authorized destinations; Ignore recovery
  and historical migration capture their own explicit authorization.
- `oct23-notification-projection-4`, `oct23-notification-retention-5`,
  `oct23-notification-formats-6` and `oct23-notification-projection-external-7`:
  focused default/tagged migration, cancellation, accounting, retirement and
  restored-store checks pass. Separate processes are forcibly terminated during
  migration before and after a snapshot; reopening preserves original actions,
  deadlines and unknown outcomes. Format 40/42 conversion and 44/45 migration
  retain their existing lifecycle semantics. Contradictory delivery evidence is
  protected from retirement, including the nonselected proof. Earlier failures
  remain retained; these passes are not a complete race or frozen-source matrix.
- `oct23-dashboard-build-3`: fresh parser/WASM normalization, production assets,
  TypeScript, lint and all 617 frontend tests pass. The original successful build
  and the later stale-generated-input failure remain retained. Real-server
  browser campaigns are separate.
- `oct23-documentation-render-1` and `oct23-documentation-exclusion-1`: strict
  private rendering passes 19,196 local links/assets across 56 content pages;
  generated paths and 1,182 search entries contain no implementation, review or
  candidate records. The separate documentation checkout is unchanged. Scoped
  independent product-page review is recorded in
  `oct23-public-docs-independent-review-1`.
- `oct23-walkthrough-source-2`: the bounded newcomer walkthrough is prepared and
  source-reviewed, but has not run. It uses one task-owned input path for the
  initial typo, correction and restart, and checks stopped bootstrap inspection
  without claiming that an absent stage proves successful discard.
- `oct23-notification-health-default-1` and `oct23-notification-health-tagged-1`:
  queued green/blue closures report unknown current health during preparation
  failure, while retaining historical recovery and the original delivery budget.
  A safe blue closure retry can refresh its wording; delayed assessment facts
  cannot overwrite it with stale healthy wording. Started payloads stay fixed.
  The subsequent compact-context version guard passes the selected default and
  tagged subcases in the storage checks; full commands with unrelated fixture
  failures remain failed records.
- `oct23-action-storage-native-acceptance`: all newly added storage scenarios have
  passing focused evidence. With 256 ordinary actions and one unknown action,
  logical retirement used 3,915,776 allocated bytes; replacement and old-pin
  retirement reduced that to 385,024 bytes while retaining the current and
  previous sparse files. This is ext4 allocated-block accounting on Linux/WSL,
  not reclaimed physical C: space. Three-snapshot fixtures with 0/256/1,024
  ordinary actions each construct one restored generation; measured startup
  timings are small-fixture observations, not fleet capacity results. SIGKILL
  publication boundaries, pending worker receipts and authenticated stopped
  backup restoration preserve protected unknown evidence. Incorrect fixture
  grant, apparent-size and control assumptions remain in the failed evidence.
  Parent source/evidence review is recorded in
  `oct23-action-storage-parent-review-1`.
- `oct23-windows-token-cross-1`: Windows amd64 token/local-administration tests
  cross-compile; this is not native execution or proof of service-account reads.
  New system configuration receives its protected owner/permissions at creation;
  incompatible supplied tokens are refused without taking ownership.
  `oct23-native-token-linux-1` passes focused native Linux token/initialization
  checks; the administrator-owned projected-Secret case explicitly remains
  unverified because the runner is not privileged.
- `oct23-blue-detail-storage-1`: bounded detail reads expose a suppressed blue
  closure with no closing action, without changing the original unknown hold.
  Memory/Raft, pin lifetime, current transaction progress, owner/configuration
  fences, cancellation and row/byte bounds pass. These reads run only on detail
  paths; compact fleet projections explicitly report incomplete coverage.
  The initial interface run passes management checks but exposes an incorrect
  permission mapping in the new numeric detail read. The correction reuses the
  existing numeric-route permission mapping, preserving transport/origin checks.
  `oct23-blue-detail-cli-import-2` passes the corrected interface/CLI checks and
  real 35-monitor single-file and 50-monitor five-file imports. Each import takes
  about 15.3 seconds in that run, survives owner restart and invokes no provider.
  This is bounded Linux development evidence, not capacity qualification.
- `oct23-import-warnings-metadata-independent-review-1`: source review finds no
  actionable issue in unconditional local warnings, bounded details, secret
  stripping or optional detail-observation metadata. Execution remains separate.
  `oct23-collection-docs-parent-review-1` independently checks the corrected
  public collection workflow, continuation semantics and documented bounds.
- `oct23-blue-detail-retry-2`: a follow-up independent review found that the
  assessment index omits a blue closure after its first start even if a safe
  retry remains queued. Detail reads now combine bounded current execution
  references with the bounded pending-condition index, retaining the shared
  100-condition/1 MiB cap. Started closures, safe second attempts, deduplication
  and crowded-index incomplete reporting pass in memory/Raft. The assessment
  index and retry authorization are unchanged.
- `oct23-dashboard-build-7`: the actual Go/WASM warning bridge, frozen profile
  fixtures, production bundle, TypeScript, lint and all 623 frontend tests pass.
  Earlier attempts remain retained: a scratch-limit stop, an invalid new fixture
  missing its required timeout, and unsupported test query options. This pass
  does not replace required real-server browser checks.
- `oct23-dashboard-build-9` and `oct23-review-browser-4`: all 623 frontend tests,
  type checking, lint, fresh assets and the focused real-server notification
  campaign pass. The browser records 1,855 unchanged selected source inputs.
  Desktop, enlarged-text, 720 CSS-pixel equivalent reflow and mobile captures
  retain labels and values without overlapping rows. Equivalent viewport reflow
  is not a claim of native browser-toolbar zoom execution. The earlier visual
  clipping findings and intermediate passes remain retained.
- `oct23-collection-browser-1`: real-server collection import warnings, secret
  concealment and safe writes pass, with zero provider operations. This remains
  separate from the four required frozen compiler/tag campaigns.
- `oct23-unix-supplied-token-2`: Linux initialization and service-policy checks
  pass after independently reviewed refusal of unsafe supplied tokens. Existing
  protected token bytes, ownership and modes are preserved. New tokens receive
  final protection before publication. The administrator-only case is explicitly
  skipped. `oct23-unix-darwin-cross-1` is a successful amd64 cross-build only.
  The first scratch-limited compile remains failed and retained.
- `oct23-bootstrap-cli-2`: the actual Cobra root command inspects and conditionally
  discards an encrypted unadmitted stage, preserves the original database file,
  installation identity and committed monitor data, records protected intent and
  completion, and makes no API requests. Missing audit inputs, stale identity,
  a running owner and absent-stage inspection fail safely. This is Linux command
  integration, not a packaged-executable walkthrough. The first run's incorrect
  byte-identical database assertion remains failed; administrative opening may
  legitimately advance Raft election metadata without changing application data.
- `oct23-documentation-render-2` and `oct23-documentation-exclusion-2`: strict
  private rendering passes 19,205 local links/assets and excludes engineering
  records from generated paths and all 1,184 search entries. The separate
  documentation checkout is unchanged. Later token guidance will be checked in
  the frozen rendering.

Independent reviewers inspected ticket 4 and ticket 5. Ticket 5 review identified
and repaired partial-byte cursor reclamation; macOS reader-group rendering now
uses the native numeric GID. Parent delivery review found and repaired selection
of an incompatible historical opening and a restored nil action-map write; the
tagged focused regressions pass. Independent storage review is complete. A
separate surface review found stale suppression after dismiss/reopen; matching
incident revisions and the actual-control regression repair that projection gap.
Worker deadline/protocol review found a stale format allowlist and golden digest;
corrected focused tests pass. Action accounting now exposes sampled row counts and
encoded bytes with a UTC observation time and bounded cleanup coverage. Independent
source reviews found no issue in its sample publication or HTTP/CLI mapping;
focused accounting tests pass. The complete browser campaign remains pending.
Independent migration follow-up reviews cover committed cutover, restored action
summaries and preservation of conflicting evidence. Exact source hashes and
author checks are recorded separately; neither replaces the final matrix.

The final affected frozen-source matrix, required real-server browser work and
remaining behavioral campaigns are pending. Native-platform, provider-account,
packaging and larger capacity qualification remain separate. No 24-hour run or
publication is part of this repair.
