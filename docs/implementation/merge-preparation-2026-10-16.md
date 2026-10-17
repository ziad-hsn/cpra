# Merge preparation — 16 October 2026

Source checkpoint: `34ef88aa3dd5ff840285d9f59ac8a95ce7ea6865` on
`codex/release-repairs`. Compared with published `main` at
`b293f34dd57ee61c78371643de6d0ebaa75c626e`: 75 commits ahead, zero behind.
All modules require Go 1.26.0; the release compiler is Go 1.27.2.

This record covers local merge preparation. Original execution logs and failed
attempts are retained privately. No public release, tag, module, image or
documentation promotion is implied by these results.

## Completed private checks

| Area | Verified scope |
| --- | --- |
| Browser campaigns | Both compilers, default and `externaljobs`: 32 required case executions, no required skips or failures. The Go 1.27.2 selections use race detection. |
| SDK and worker modules | Both compilers, with race detection: SDK default, SDK `externaljobs`, and worker `externaljobs` pass package selection, static analysis and tests. |
| External-worker processes | Both compilers: 37 process/capacity leaf cases. Each five-minute, 64-monitor fixture drains with zero duplicate invocations or unreconciled outcomes. This is not a fleet-capacity measurement. |
| Generated outputs | SDK generation matches ten committed outputs across two roots. The isolated dashboard rebuild matches assets/notices; 541 tests, type checking and lint pass. |
| Linux amd64 distribution | Both executables, the Linux archive and Compose bundle match across a checkout and two independent extracted-source builds. Extracted builds require no Git, Node or pnpm. |
| Packaged management | Protected localhost TLS/operator initialization, a 40-monitor import, forced termination and recovery of the same identities/configuration versions pass. The fixture monitors are disabled; no target connections occur. |

These checks use frozen checkpoint `34ef88a`. The uncommitted whitespace fixes
regenerate embedded notice text and update its manifest hashes; they leave all
40 frontend payloads unchanged. Those new notice bytes have not been rebuilt
into the qualified executables. The earlier build and package evidence remains
evidence for the frozen checkpoint, not the modified working tree.

The source, compiler, commands and original output for these checks received
independent review. The review does not turn fixture results into provider
delivery proof or extend Linux executable checks to other operating systems,
services, package managers or container deployments.

## Application matrix

Complete coverage is required for Go 1.26.0 and Go 1.27.2 with default, all
built-in drivers, `externaljobs`, and all built-in drivers plus `externaljobs`.

| Compiler | Default | All built-in drivers | `externaljobs` | All built-ins plus `externaljobs` |
| --- | --- | --- | --- | --- |
| Go 1.27.2 | Passed and independently reviewed | Passed and independently reviewed | Passed and independently reviewed | Passed and independently reviewed |
| Go 1.26.0 | Passed and independently reviewed | Passed and independently reviewed | Passed and independently reviewed | Passed and independently reviewed |

All eight completed rows run unrestricted package selection, static analysis
and race-enabled tests against `34ef88a`. Each completes all 32 packages.
For each compiler, the test/subtest events contain 5,179 passes for default,
5,197 for all built-ins, 6,307 for `externaljobs` and 6,325 for the combined
selection, with no failures or race diagnostics. The minimum compiler's
separate browser and worker-process/load companions run without race detection;
its complete application rows use race detection. All skips are classified,
with browser and worker-load execution covered by the
separate campaigns above. Those companions used default or `externaljobs`
selections; they were not browser/load executions with every built-in tag.
The complete application matrix is verified; this is not complete release
qualification or merge sign-off.

Earlier interrupted runs remain failed/incomplete records. Completed rows do
not relabel the original interrupted release campaign as successful. All
reruns preserve the full package selection, race instrumentation, deadlines
and the 6 GiB task allowance, with its unchanged sampling reserve.

Only identified task-owned compiler workspaces and disposable cache objects
were removed. Inactive tools, interrupted fixtures and older raw logs were
retained in indexed archives; the cleanup records distinguish parent checks
from independent review. Those fixture archives are forensic evidence, not
consistent application backups. Current-candidate raw logs remain directly
available. Readers of archived historical log paths must restore them first.

Four duplicate qualification-executable paths were temporarily removed after
verification against their unchanged distribution archive. Their exact
contents, permissions and recorded metadata are restored. The final Go 1.26.0
combined row completed in 2,611 seconds, with a sampled peak of 5,434,437,632
bytes below the 6,174,015,488-byte guard. Frozen-snapshot ledger growth and
large-ledger streaming passed. After all test processes exited, clearing only
the task build cache reclaimed 1,901,334,528 bytes.

## Combined branch review

An independent reviewer inspected the combined changes against the published
main commit above, including controller, persistence, worker, API, SDK,
dashboard, initialization, shutdown and CI/publication interactions. This was
a risk-focused source review of a 1,115-file diff, not exhaustive certification.
The reviewer ran no tests. Two source-confirmed P2 defects remain open:

- **Shared-peer authentication denial:** the failed-authentication limiter can
  reject valid operators and probes before checking their credentials after
  another caller behind the same proxy or NAT reaches the failure threshold.
  The correction must retain current authority, revocation, operation,
  transport and origin checks, including observation and worker credentials.
- **Recurring maintenance with external workers:** worker action starts and
  check-result transitions lack the periodic maintenance observation used by
  built-in execution. An offered action can cross a cron boundary and start;
  a check result can create intents during the suppressed interval. Absolute
  windows are guarded. The correction must preserve deterministic replay,
  pinned configuration and unknown-outcome handling.

Required browser campaigns are wired into CI; inspected artifact uploads and
release preflight preserve private qualification. Ordinary CI's selected
external-worker suites do not replace the complete application matrix above.
No additional confirmed CI privacy or campaign-reachability defect was found.

## Remaining boundaries

The two defects require correction, regression checks and review before merge
sign-off. The user owns the subsequent push, pull request and merge. Native
platform/service/package coverage, deployment qualification, provider-account
effects, accessibility review and comparative performance remain separate
release gates. The 24-hour endurance run remains cancelled.
