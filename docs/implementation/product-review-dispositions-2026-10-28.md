# Product review repair dispositions — 28 October 2026

Implementation in progress. Baseline `967f9cf8b5733a9f93c56ceaf8712a748cdf5e08` plus existing edits.
Private evidence: `cpra-review-repairs-2026-10-28` in the operator state directory.
The baseline inventory contains 2,469 files. No final frozen-candidate claim.

Plan: [Approved implementation plan](product-review-repairs-2026-10-28.md).

The separately reported September CI failures are investigated in
[CI failure investigation — 1 November](ci-failures-2026-11-01.md). Its focused
repair results do not replace the final candidate matrix below.

| ID | Finding | Status | Repair / evidence |
|---|---|---|---|
| B1 | monitoring fail-stop under incident burst | Implemented; partial verification | Original completion identity/time retained; retry bookkeeping only; malformed/permanent replies stop admission. `reconciliation-pacing-5`, `progress-pagination-1` and `action-observability-2` pass. The combined real HTTP/Raft schedule retains the original completion timestamp, holds controller readiness while storage leadership has recovered, preserves last observation/incident facts and makes one provider invocation. Final loaded/process matrix pending. |
| B2 | unavailable checks never page; rules widen this behavior | Repaired; focused tests pass | Rule-independent check classification; unavailable monitoring retains last observation and suppresses invented outage. `observation-1`, `systems-regression-2`; no configuration-failure paging is intentional. |
| B3 | configuration edits discard green | Implemented; integration gate open | Dependency-bound resolutions survive irrelevant edits; altered dependencies cancel safely. `notification-fences-1` and `dispatch-budget-3`; final restart/credential-change campaign pending. |
| B4 | recovery-only notification triggers can leave an outage silent | Intentional trigger; warning repaired | Recovery-only red remains recovery-only. Added bounded `recoveryOnlyCoverage`; none-only policies do not warn about impossible unknown recovery. Browser warning boundary now 33; final import tests pending. |
| B5 | valid multi-rule monitor cannot start / can stop management | Repaired; focused tests pass | Custom JSON admission accounting preserves 4 MiB/32-rule limits. `initial-repairs-2`; full maximum-rule startup matrix pending. |
| B6 | security reporting unavailable | Unresolved administration gate | Repository read access confirms admin identity but the available connector has no settings mutation. Private-reporting setting and live form have not been enabled/verified. |
| M1 | queue-time recovery deadlines / burst outcomes | Implemented; focused tests pass | New recovery budget starts at committed start; retries retain deadline. `dispatch-budget-3`, `worker-dispatch-deadline-1`; old recorded deadlines preserved. |
| M2 | serial destination bottleneck | Implemented; partial measurement | Webhook cap four, sequence fencing, shared pacing, persisted pauses. `notification-pacing-2`, `pacing-review-fixes-3`, `reconciliation-pacing-5`. Serial 250 fixture passed; concurrent fixture delivered all 500 once but failed its old long-wait setup assertion. Final binary rerun required. |
| M3 | action-bearing rules fail management bootstrap | Repaired; focused tests pass | Bootstrap driver category/path ordering corrected for action rules and credentials. `initial-repairs-2`; full-driver matrix pending. |
| M4 | observed codes change when rules are enabled | Repaired; focused tests pass | Classifier runs regardless of recovery policy; paired observations covered in `observation-1` and `systems-regression-2`. |
| M5 | guarded manual recovery usability | Implemented; partial verification | Recovery-rule contract errors map to 422; dropped selection restored. CLI qualification/precondition presentation requires final end-to-end checks. |
| M6 | file-mode restore/rollback authentication dead end | Implemented; focused tests pass | Stopped reprovisioning distinguishes observation/management and preserves identity fencing. `restored-http-modes-1` passes the stopped authenticated backup/restore/reprovision/start procedure and real HTTP reads for both modes. Scope: Linux test-process startup and an empty catalog; native services remain separate. |
| M7 | validation parity and diagnostics | Repaired; focused corpus passes | v5 adds metadata/version and original driver-spelling validation; literal v1–v4 identities preserved. `input-parity-and-context-1`, `input-snapshot-retained-4` API/collection/loader checks pass; all 35 driver tags are checked. Native/WASM v5 parity passes in `browser-parser-v5` and `frontend-complete-2`. |
| M8 | dashboard triage / offline navigation | Implemented; browser gate open | Bounded incident filtering/order; observation-only details; simultaneous fleet conditions; shell-preserving route error boundary. `frontend-complete-7` passes 654 tests, types, lint, parser verification and build. The original operation receipt now survives successful route changes. The focused offline/reconnect campaign passes (`browser-offline-focus-1`); final complete campaigns remain pending. |
| M9 | public documentation | In progress | `documentation-site-3` passed strict rendering and 20,231 link/asset checks, with internal records absent from output/search. Subsequent metric documentation needs refreshed rendering; complete newcomer examples and final source synchronization remain pending. |
| S01 | Green after an undelivered red; automatic Ignore recovery green | Intentional behavior | Green requires a committed opening start, not proven provider acceptance. Ignore recovery exception preserved. Unknown effects remain held. |
| S02 | Blue for a later outage; yellow “threshold may not yet” wording; red “perform manual recovery” after a none/no-match decision | Partial; wording gate open | Blue later-outage behavior remains intentional. Recovery-only red warnings added; all notification wording fixtures still need final review. |
| S03 | Mixed UTC/offset timestamps | Implemented; regression gate open | UTC observation projections and explicit absent times retained; machine-output and browser matrix pending. |
| S04 | Snooze/unsnooze, upgraded unknown actions, failed log delivery and canceled recovery logs | Partial; regression gate open | Lifecycle logger includes committed cancellation/unknown transitions. Snooze/unsnooze and restart transitions require targeted coverage. |
| S05 | gRPC wrong-CA versus unavailable; generic database errors; hung HTTP code | Classification repaired | Unavailable local preparation/resource inspection does not establish a target outage. Generic failures remain unclassified and cannot authorize rules; all-driver typed corpus pending. |
| S06 | Overlap/missing-blue warning parity | Implemented; integration gate open | 33 bounded advisories, overlap/coverage/blue distinction; 90 literal normalization cases and seven native/WASM warning cases pass in `frontend-complete-2`. Real-server import campaign underway. |
| S07 | Offline rule-test errors, impossible phases, missing monitor, YAML output, weaker full-driver validation | Implemented; focused checks pass | Offline testing validates all inert resources/compiled capabilities and distinguishes absent monitor/rules; `input-contract-v5-1` CLI checks passed. Full tagged corpus pending. |
| S08 | Taxonomy version freeze | Implemented; focused tests pass | Taxonomy-v1 canonical digest fixture freezes current meanings. `small-contracts-1`. |
| S09 | Built-in external-worker failure codes | Intentional protocol boundary | Tagged check declarations and protocol-2 structured reports remain scoped to immutable JobTypes; no arbitrary code/permission input added. |
| S10 | Worker certificate report accepted locally but rejected by server | Repaired; focused tests pass | Certificate detail pairing now matches server canonicalization, including zero remaining days. `small-contracts-1`; runner rejection matrix pending. |
| S11 | Interrupted journal conversion leaves encrypted partial backup | Implemented; process gate open | Bounded identity-checked encrypted partial-conversion discovery preserves originals. `worker-full-tagged-2` passes, including SIGKILL during creation of a nonempty partial backup of an encrypted journal. Repeated conversion refuses additional copies and preserves original bytes/records. This boundary may contain only bbolt metadata; native Windows remains unverified. |
| S12 | Bootstrap inspect ergonomics; safe local error paths; auth failure described as unhealthy; inconsistent CLI names/output; diff duration spelling | Partial; integration gate open | Explicit bootstrap prerequisites and safe health-query transport errors added. Full stopped-admin, TLS/token and duration-diff output corpus pending. |
| S13 | Cursor pressure | Repaired; focused tests pass | Removed blanket idle delay, retained active-reader protection and 410 semantics. `progress-pagination-1` covers rapid first-page pressure. |
| S14 | Token rotation ignoring bootstrap files | Intentional authority; documentation pending | Committed grants remain authoritative; source already warns about ignored bootstrap input. Rotation instructions must remain stopped and mode aware. |
| S15 | Incarnation identity scope | Intentional identity scope | Incarnation and incident IDs are installation-local. Action/episode/delivery namespacing remains distinct; no historical identity rewrite. |
| S16 | Rules selection label versus admission; disabled manual button; rapidly stale decision revision | Implemented; browser gate open | Rule qualification, selected action and admission reason shown separately. Conditional decisions remain required; no automatic conflict retry. |
| S17 | Rule editor size, irrelevant fields, full-JSON diff, reorder focus, missing inline errors | Implemented; unit review underway | Relevant predicates, stable reorder focus, compact conditional summary and protected driver allowlists. `frontend-complete-2` passes all 646 dashboard tests, types, lint, build and staged assets; direct rule-diff redaction tests pass. The rule editor real-server scenario passed in `browser-default-campaign-2`; the complete campaign still failed on offline recovery, subsequently repaired and verified by the focused campaign. |
| S18 | Mobile tables, hidden controls, IDs, keyboard navigation, nested main elements, text sizing, 403/429 wording | Partial; browser gate open | Shell error boundary, bounded retry, wrapping and clearer authorization messages implemented; desktop/mobile/zoom/keyboard campaign still required. |
| S19 | Guided webhook password field and credential messages | Intentional concealment; diagnostics pending | New secret-bearing webhook input remains concealed; malformed URL versus credential-specific error wording needs focused coverage. |
| S20 | Rules/examples/reference pages and slow bootstrap progress | Pending documentation verification | Complete runtime/log prerequisites and bootstrap progress examples must be executed against final candidate. |
| S21 | Package-root tests, duplicate embedded assets/fonts, branch/PR/issue counts, historical engineering records | Intentional; preserve artifacts | Root package tests and required embedded assets are valid build inputs. Historical engineering records stay retained and publication-filtered; branch/issue/PR counts are not defects. |
| S22 | Repository description typo / topics / community percentage | Editorial gate open | Description typo confirmed; mutation capability unavailable. Topics unchanged; community percentage is not a qualification signal. |
| S23 | Pool desired capacity versus active/draining work; missing recovery cancellation logs | Implemented; validation underway | Driver ceiling, headroom, once-per-admission pacing counter and reconciliation reservation gauge added. Headroom explicitly does not claim exact executable capacity; retained cancellations are now exposed through API/SDK/CLI/dashboard; condition/action termination reasons remain explicit. `action-observability-2` and `action-observability-tagged-2` pass bounded cancellation, permanent-rejection and exhaustion counters, duplicate outcomes, delayed deadline assessment, replay exclusion, failed history writes and private-label checks. Race execution and the corrected historical action-version fixture remain pending. |

## Environment and qualification

- Physical C: free space at start: 4,372,615,168 bytes. Scratch must fit actual free space minus the 64 MiB reserve as well as the 6 GiB ceiling.
- Prior reviewer executables used Go 1.27.0; new evidence must identify the selected compiler.
- Native-platform, provider-account, packaging and larger capacity gates remain separate. No publication is authorized by this repair.
- Real default-store inspection is separate and read-only; no repair or downgrade is authorized implicitly.

## Current verification scope

All evidence IDs above refer to bounded private logs and JSON reports under the task evidence directory. They describe a dirty working tree at the baseline HEAD, not a frozen candidate or native/provider qualification. Failed runs are retained.

`persistence-controller-1` passed controller packages and queue tests but failed persistence: three obsolete fixture assumptions and a ten-minute suite timeout. The subsequent full run (`persistence-full-2`, 912 seconds) exposed four first-start/fixture-clock failures. Corrected focused tests pass in `retained-review-budget-6`; `persistence-full-3` passed all persistence tests in 978 seconds on Go 1.27.3. Later metric and worker-expiry changes have focused evidence only until the final matrix runs. Independent review also strengthened archived snapshot corruption tests: matching framing, an undamaged supported-image control and semantic failure checks pass in `archived-evidence-reader-8`. No failed run is relabeled as passing.

Independent reviewers inspected dispatch, operational reconciliation and interfaces. Their source-only results are separate from executed tests. The new action-bound preparation marker, cancellation-after-health-recovery regression and corrected snapshot tests received a separate source review with no remaining defect in that scope. The final affected dual-compiler and real-server matrices remain open.

**Final merge qualification: pending. Source integration into `main`: explicitly
authorized by the maintainer on 1 November 2026. Release publication: not qualified
and not authorized.** The source integration preserves the open findings and
failed evidence below; it does not turn partial checks into a final matrix pass.

The default user store was not opened. `default-store-metadata-2` found no visible owner but could not inspect 274 process descriptor directories; it does not prove exclusive stopped ownership. No restore, downgrade or format claim follows from that metadata check.

## Additional checkpoint evidence

- `sdk-full-3` (default), `sdk-full-tagged-4` and `worker-full-tagged-2` pass all respective module tests with Go 1.27.3. These workspace runs do not establish downloaded-module publication.
- `browser-default-campaign-1` and `browser-default-campaign-2` remain failed evidence. Repairs align the v5 new-collection profile across SDK/browser, retain v1–v4 continuation identities, preserve post-save receipts and recover lazy navigation after a known-offline failure. `browser-offline-focus-1` passes against unchanged inventoried source.
- `release-recipe-3` timed out after 180 seconds and included an obsolete tagged-format assertion. It is not a pass. `release-recipe-focused-4` passes the corrected base50/tagged51 recipe tests; the wider script matrix remains open.
- Independent source reviews caught the missing delayed-recovery exhaustion counter, an invalid historical action-version test fixture, the mutating claim-removal assertion and overbroad wording about partial-backup bytes. Those corrections are retained separately from execution evidence.
