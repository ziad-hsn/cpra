# Original collection continuation, 14 October 2026

This is scoped private development evidence on `codex/release-repairs`, starting
from `57b24ef4c141dc5f722159af60aa8786df198ad8` with the working changes described
below. It is not a frozen release qualification or a provider/performance result.

## Reproduced defects and repairs

The real native CLI could not resume an ordinary YAML import because the SDK's
low-level reselection client rejected its empty normalization profile. The
server and `ResumeFiles` already supported that exact original profile. The
client now accepts either empty or `cpra.file.base.v1`, still requires exact
response identity, and rejects unknown profiles. Inputs are never relabeled.

A second reproduction showed that a validating or validated original input
could not finish verification. `VerifyPrefix` incorrectly reused the final
append check, which requires an upload without a validation request or plan.
Prefix verification now checks the read capability's allowed phase and captured
prefix, immutable input identity, authority at its caller, and current expiry.
Append verification retains its original strict upload-only restrictions. No
FSM format, command, route, write synchronization or compiler claim changed.

The CLI now describes interrupted validation, closed operations, rejected input
and already-started execution using fixed messages. Original handles and error
classification remain available; an ambiguous mutation still takes precedence.
The CLI and SDK guides distinguish continuation from observation after activation.

## Intentional interruption remains terminal

The first fixture incorrectly expected successful validation after every owner
restart. Actual observation was `validating` before stop and `interrupted` after
restart. This is the existing [coordinator contract](collection-validation-coordinator.md):
a lost claimed compiler cannot recreate its decisions against newer catalog
state. Its original interruption evidence remains readable and nothing applies.

The corrected fixture distinguishes client loss, a finalized verdict, and owner
loss during unfinished work. The last scenario records the observed outcome:
an interrupted original gets zero continuation writes and zero active resources;
an original that finished before shutdown remains eligible. It does not claim
that timing-dependent scenario always interrupts a claim. The separate existing
`TestCollectionValidationWorkerProcessCrashRestart` deterministically covers
queued, claimed, partial-plan, finalized and canceled boundaries by killing a
child with real Raft state, then reading its original evidence after restart.

## Native CLI scenarios

`TestMainCollectionCLIContinuationAfterLostPhaseReply` builds and executes the
actual `cpractl` using the test compiler and build tags. A TLS proxy withholds a
selected admitted response; the test kills that CLI process before delivery.
Each case uses 35 disabled monitors, the normal application startup, TLS,
authentication and Raft, and an independently counted local target.

| Boundary | Required result |
| --- | --- |
| Upload reply lost, then owner restart | Resume original input, validate and apply each resource once. |
| Validation reply lost, original owner alive | Verify original files and consume the original verdict before activation. |
| Published validation result, then owner restart | Consume that retained verdict and apply once. |
| Owner restart before validation finishes | Preserve the actual disposition; interrupted validation cannot submit new work. |
| Activation reply lost, then owner restart | Wait for the original execution without another activation. |

All successful cases require the same operation/content identity, 35 accepted
and applied resources, generation one on every monitor, and no replacement
collection. All cases require zero provider calls. The killed client's plaintext
spool is confined to a parent-owned temporary directory on every platform.
The application owner is stopped and reopened within the test process; this is
not a forced server-process termination test. The startup fixture currently runs
on Linux. Native Windows/macOS CLI execution remains a separate gate.

## Recorded checks

The private records identify command, source inventory, starting commit,
compiler, elapsed time, exit status and log digest. They use a local module
workspace; they do not establish public SDK installation.

| Record | Result and scope |
| --- | --- |
| `sdk-resume-ordinary-baseline` | Failed ordinary-profile regression before correction. |
| `input-verification-validation-baseline` | Failed claimed-validation prefix proof in memory and a real bbolt ledger. |
| `collection-continuation-input-proof-fixed` | Go 1.27.1/default native CLI, 96.763 s; focused persistence, 10.935 s. An incorrectly spelled management selector selected no tests and supplies no management evidence. |
| `collection-continuation-minimum-check` | Go 1.25.0: native CLI 109.879 s; SDK, collection helper, CLI, persistence and actual coordinator crash/restart selections pass. |
| `collection-continuation-external` | Go 1.27.1 with `externaljobs`: the same selected packages and native CLI scenarios pass. |
| `collection-continuation-race` | Go 1.27.1 race checks pass for selected SDK, collection, CLI, persistence and management cases, including the coordinator crash fixture; total 50.954 s. The native CLI matrix above is uninstrumented. |

The initial stronger owner-restart assertion, intermediate CLI/proof failures
and an invalid tool-path invocation remain distinguishable from these passing
checks. No broad package matrix or final-source qualification is inferred.

Log SHA-256 values:

| Passing record | SHA-256 |
| --- | --- |
| Default native CLI and prefix checks | `24fea3a959655a8a9fd8bb42163ecef99c378caf4b538a0ba0f942e827a13284` |
| Minimum compiler | `28dfebe118e8f659e8e7d88d63c85e730c0508a3ea90e878ec04c509748dfce1` |
| External-jobs build | `668da94bbf3fe423e6fe415305c0875616a6fc225c559a8a34fceb23fb0e783c` |
| Race checks | `ab2bedcc409dce82f30d6f00e28078ec3e1e18ef0398ae273e7cf61d353b6adf` |

The subsequent pinned dashboard rebuild passes all 541 tests, type checking,
lint, parser generation and embedded-asset checks. The required real-server
browser campaign passes all eight tests with race detection on Go 1.27.1,
Node v24.21.0, Playwright 1.56.1 and Chrome 153.0.8010.36. It reports no skipped
coverage and no changed source inputs over its 362.26-second run. Its recorded
source inventory SHA-256 is
`bc8309cc2ff012cdd7d455dcaaa87bc709ea88abee65717c3a6a26611ab8bb2f`,
and the Go event-log SHA-256 is
`ce1d82188e03bfcaedf771e64e3368a08848f063dfde27b395e07c5fb0d7bae2`.
This is the default build on the local Linux/WSL machine, using a private module
workspace; it is not the complete compiler/tag/platform release matrix.

Canonical documentation checks pass for 175 Markdown pages, 59 navigation
pages and 89 selected source references. Private reference contents are excluded
and are not independently verified by that check.

Independent read-only review closed the SDK profile correction, prefix/append
guard separation and fixture ownership repair. The reviewer inspected source
and assertions but ran no tests. The review also confirmed the terminal compiler
interruption boundary; it initially missed the inherited append restriction,
which the executed integration test exposed and the follow-up review examined.

The broader slow-storage contention, recovery/packaging, final frozen-source,
native platform, configured provider and comparative performance gates remain
open. Publication remains withheld and the 24-hour run remains canceled.
