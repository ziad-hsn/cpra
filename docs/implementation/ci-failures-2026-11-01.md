# CI failure investigation — 1 November 2026

The reported [release verification run](https://github.com/ziad-hsn/cpra/actions/runs/35982249904)
executed on 24 September 2026 against `b293f34dd57ee61c78371643de6d0ebaa75c626e`.
It did not test the current `codex/release-repairs` working tree at
`967f9cf8b5733a9f93c56ceaf8712a748cdf5e08`. The reported compiler versions were
Go 1.25.0 and 1.27.1; the current workflow already selects Go 1.26.10 and 1.27.3.

## Findings and repairs

| Reported checks | Evidence | Disposition |
|---|---|---|
| macOS arm64 and amd64 | Backup/key-generation fixtures passed temporary paths through the macOS `/var` symlink. Production rejected those paths as designed. | Backup fixtures now use the existing private test-root helper, which resolves only its own temporary root. Production key and installation path checks remain unchanged. A controlled aliased `GOTMPDIR` reproduced the original failures on Linux before the repair. |
| macOS amd64 and both Windows architectures | The persistence package reached its overall ten-minute timeout. The tests active at timeout had run for 1–12 seconds. | The current native workflow already has a finite 45-minute package budget. The four tests active at native/all-driver timeout pass individually in the current Linux focused run. This is not a native platform pass or proof that the entire native suite fits the budget. |
| Both SDK compiler jobs | `sync_guides.py` could not find the linked `examples/sdk/queue-registration/registrations.jsonl`. | The synthetic fixture existed locally but was ignored by `*.jsonl`. An exception now admits that exact file; it must be included in the repair commit. Its bytes match the retained documentation source copy. Current SDK references/guides were regenerated and checked. |
| Go 1.25.0 all-driver job | Persistence exceeded its twenty-minute package budget, and `TestKMSBoundsErrorsAndCancellation` returned an invalid-response error instead of deadline cancellation. | The current Makefile already uses 45 minutes. KMS cancellation precedence is now repaired and covered by a deterministic regression through the real AWS SDK decoder. The old log does not prove the exact transport interleaving. |
| Go 1.27.1 canceled job | The job was canceled during worker-exclusion verification after the other compiler failed. No assertion failure was logged in that step. | Cancellation remains incomplete evidence. The Go and A2A compiler matrices now set `fail-fast: false`, as SDK/native matrices already did, so one compiler failure does not cancel its sibling. Cancellation of superseded workflow runs remains enabled. |

The KMS repair checks cancellation before inspecting a successful SDK result.
Canceled decryptions clear any returned plaintext. Invalid responses with a live
context still fail validation; bounded bodies, redirect rejection, deadlines and
`aws.NopRetryer` are unchanged. The regression cancels after the request enters
an admitted transport and requires exactly one intercepted request. Initialization,
encryption and decryption all failed before the repair and pass afterward.

The subsequent complete tagged administration run found an additional defect in
the current candidate: protocol-2 JobTypes were still restricted to snapshot
format 49 after the candidate introduced format 51. Snapshot validation now
admits exactly 49 and 51, while refusing 47, untagged 48/50 and unsupported 53.
A real stopped backup/restore regression and a frozen snapshot format test pass.
This was discovered during current verification; it is not attributed to the
September run. Two more test-owned action/service fixtures also needed canonical
temporary roots. Production path restrictions remain unchanged.

## Focused verification

Evidence is private under `cpra-review-repairs-2026-10-28/evidence` in the task
state directory. The original job logs are retained in `ci-run-35982249904`.
`ci-repair-source-1.json` and `ci-repair-source-2.json` record the hashes of the
relevant source inputs, including the additional snapshot repair.
These results describe a dirty working tree, not a frozen release candidate.

| Evidence ID | Result and scope |
|---|---|
| `ci-aliased-temp-baseline-2` | Expected failure: reproduced the backup/key and service-update errors using an aliased test root. |
| `ci-aliased-temp-repaired-1` | Pass: backup/restore and key-file regressions with the same alias, including unsafe-path rejection, Go 1.27.3 on Linux. |
| `ci-kms-cancellation-baseline-1` | Expected failure: deterministic cancellation precedence reproduction for all three KMS operations. |
| `ci-kms-repaired-race-1` | Pass: 30 repetitions of KMS/wrapping tests with the race detector, Go 1.27.3. |
| `ci-kms-minimum-repaired-race-1` | Pass: the same 30 race repetitions, Go 1.26.10. |
| `ci-timeout-tests-1` | Pass: all four persistence tests active at the old timeouts, Go 1.27.3 on Linux. |
| `ci-sdk-lessons-1` / `ci-sdk-lessons-report-1` | Pass: default/tagged example tests with race detection, vet, package/dependency exclusions and four local demos, Go 1.27.3. This is local fixture evidence, not provider-account qualification. |
| `ci-sdk-documentation-1` | Pass: seven documentation-generation contract tests. |
| `ci-workflow-policy-1` | Pass: fifteen workflow, compiler and private-publication policy tests. |
| `ci-localadmin-tagged-1` | Failed and retained: two remaining aliased fixtures and the protocol-2 format-51 snapshot gate. |
| `ci-snapshot-tagged-repair-1` | Pass: corrected action/service fixtures, real external-rule backup/restore and snapshot format boundaries with `externaljobs`, Go 1.27.3. |
| `ci-sdk-reference-check-1` | Pass: all three generated SDK references match their source inputs. |
| `ci-documentation-check-1` | Pass: documentation links, source contracts and publication filtering checks; 57 navigation pages, no errors. |
| `ci-localadmin-base-final-1` | Pass: complete administration package under the aliased test root, Go 1.27.3 on Linux. |
| `ci-localadmin-tagged-final-1` | Pass: complete `externaljobs` administration package under the aliased test root, including authenticated backup/restore, Go 1.27.3 on Linux. |
| `ci-frontend-current-1` | Pass: TypeScript, lint, all 654 tests in 42 files, parser/API generation checks and production build. Stale embedded assets from the pending API/dashboard edits were rebuilt and the freshness manifest updated. This is not a new real-server browser campaign. |
| `ci-current-vet-1` | Pass: `go vet ./...` for the current default application build, Go 1.27.3. |

Independent source review found no blocking issue in the fixture, ignore-rule,
workflow or KMS changes. That review did not execute tests.

## Remaining gates

The exact new commit still needs the complete GitHub Actions matrix, including
native macOS and Windows execution and uninterrupted worker-exclusion checks.
Local focused checks cannot replace that result. The September jobs remain
failed/canceled; rerunning them would retest the old source.

The broader [product repair inventory](product-review-dispositions-2026-10-28.md)
remains authoritative for unfinished implementation and final qualification.
On 1 November, the maintainer explicitly authorized integrating the completed
source changes into `main`. That source integration does not close the remaining
verification gates or authorize release, module, image or chart publication.
Provider-account, native-distribution and fleet-scale qualification remain open.
