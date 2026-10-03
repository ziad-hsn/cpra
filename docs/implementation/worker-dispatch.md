# External-worker controller dispatch

Private implementation checkpoint: 29 September 2026. The tagged controller now
connects stored external bindings to committed check, recovery and notification
assignments. Public API, bootstrap and collection admission still reject external
execution configuration. This checkpoint does not qualify full worker execution
or a release.

## Ownership and capacity

The ECS owner retains scheduling and pending-work state. Remote checks use their
own FIFO ready queue, with at most one slot per full entity identity. A saturated
local queue cannot put an external check behind the local fleet. Both queues
retain their original cadence and configuration/control guards. Pausing, replacing
or removing an entity cancels its ready entry and recycles the slot.

Four controller-owned goroutines prepare and submit assignments. At most 16
requests may be preparing or awaiting a completion handoff. Their dependency
metadata is charged against a 16 MiB admission budget before cloning. At most
4,096 admitted executions are observed by the owner, matching the persistent
record limit. Each tick visits at most 16 observations with a shared 50 ms read
budget. Completion lookup copies scalar metadata rather than assignment payloads.
Preparation slots are separate from those observation slots, allowing expiry and
confirmed rejection to finish at full observation capacity. A completed
preparation retains its slot until the owner finishes or transfers it; combined
tracking remains bounded at 4,112 entries.

Only copied identities and guards cross the preparation channel. The catalog
opens parameters and seals assignments outside the ECS loop. No Ark pointer,
provider client or executable job enters the queue. A goroutine finishes after
admission; it never occupies a local worker while waiting for a remote handler.

The pool starts with controller initialization. Admission stop cancels preparation;
finalization joins the same pool before dependencies can close. Its completion
buffer can hold every accepted request, so joining does not require a running
ECS result consumer.

The subsequent [admission-fairness change](worker-admission.md) rotates first
access between health checks and incident actions. Confirmed quota rejection
causes a bounded delay for the original JobType incarnation; a capacity read
precedes check-ID reservation and payload sealing. These reads and temporary
delays do not replace the state machine's admission or Start authorization.

## Original identity and uncertain replies

Before allocating work, the dispatcher looks up the active check for the exact
monitor incarnation or the retained execution for an action ID. Terminal actions
stay discoverable independently of unfinished endpoints. Checks use the committed
check-ID allocator, preserving the existing receipt-retirement contract.

Owner observations are indexed by committed execution ID. A metadata-only edit
that adopts an existing check transfers its observation to the current monitor
token. Delayed replies cannot replace that token or allocate duplicate tracking
slots. Removing a completed observation updates the index for any moved slot.
The original store record continues to own finalization throughout this handoff.

Preparation bounds the original scheduled-to-deadline window by the authenticated
JobType timeout and the monitor timeout for checks. Dispatch does not grant Start
permission or change the original action's queued state.

An uncertain commit retains the exact identity, timestamps and ciphertext. A FIFO
flush settles earlier submissions before the dispatcher reads the original
record or retries that same intent. Cancellation cannot discard an unresolved
submission. A permanent storage fault can therefore keep finalization waiting;
the existing supervisor deadline bounds the caller, not ownership of the store.

The owner clears pending checks only for the original local correlation token.
An old completion cannot clear a replacement monitor's pending check. It reads
the current committed monitor before projecting completion, including when a
worker finished before the admission reply reached the owner. Lookup and replay
never invoke a provider.

## Accounting and remaining execution boundaries

[Schedule accounting](worker-schedule-accounting.md) now records ready and
preparing checks before admission using original owner-issued tickets. Admission
and periodic checkpoints advance the same cumulative counts, including when
replies are lost or preparations finish out of order. Definite cancellations and
shutdown losses remain missed obligations. The local recorder adds other missed
cadence slots and deliberate pauses to produce one external report. Remote queue
and handler timing remain unavailable.

The following work remains required before public configuration admission opens:

- Carry the verified ready/preparing accounting through the complete worker/server
  campaign, retaining explicit storage-failure and restart coverage gaps.
- Qualify fairness and backoff under persistent per-type or global admission
  saturation, alongside cadence, pause and replacement accounting.
- Preserve the process campaign below while extending pressure coverage to
  sustained saturation and failed admission. The later capacity campaign covers
  configured byte limits and journals retaining unknown actions separately.
- Complete independent review and the broader compiler, platform and release gates.

The subsequent [action-expiry change](worker-expiry.md) commits cancellation when
a recovery or notification window expires before admission. It allocates no
assignment and preserves already admitted or started actions. Its separate
verification record covers expiry and recovery; full execution qualification
remains open.

## Verification

The [scoped evidence record](../review/local-evidence.md#record-0c49e80b753b79967a16a33f)
records commands, source hashes and unsuccessful attempts. Tests use real Raft and
encrypted catalog records installed through the internal fixture path. They cover
all three job categories, full-local-queue independence, original intent retries,
lost committed replies, shutdown ownership, capacity bounds, controller-state
reconstruction, missed schedules and stale completion tokens. Reconstruction of
the controller in those tests is not evidence of a killed worker/server process.

## Separate-process verification

The later `TestMainWorkerProcesses` campaign runs normal `runCPRa` startup in an
application test executable and the real SDK `Runner` in a separately built
worker-module test executable. The application module does not import the worker
module. Every process uses the selected compiler, and the race campaign explicitly
instruments both executables. This is Linux/WSL process evidence on local ext4;
native release-archive, service, container and provider qualification stay separate.

JobTypes are created through the authenticated SDK and production HTTPS routes.
Worker credentials are issued through stopped local administration. The monitor
configuration is an encrypted, reference-checked stopped fixture, preserving the
closed public external-configuration gate. Normal startup, dispatch, Poll, Start,
result delivery, controller projection and journal recovery use production code.
An independent local HTTP target counts effects by execution ID.

Each of check, recovery and notification runs these six cases:

| Case | Required observation |
| --- | --- |
| Normal completion | One handler invocation and the original committed receipt. The acknowledged execution leaves the journal; any later scheduled check retained during shutdown must match its own committed server identity. |
| Start reply interrupted | Kill the worker after the server grants Start but before the runner receives it. Restart reconciles that execution without invoking its handler. |
| Handler interrupted | Kill the worker after the independent effect is observed but before the handler returns. Restart does not invoke it again. |
| Saved outcome | Kill before sending the synchronously journaled result. Restart delivers that outcome without another handler invocation. |
| Result reply interrupted | Kill after the server commits a result but before the runner receives its receipt. Restart receives the same receipt and never repeats the effect. |
| Both processes interrupted | Kill the worker during a handler and kill CPRa. Normal restart finalizes the original lost-owner execution; later worker delivery cannot replace that receipt or repeat the handler. |

Nine additional cases also pass in the current campaign:

- Uncooperative handlers in all three categories require the real drain deadline
  to expire, admission to stop, and a second process to fail with the bbolt lock
  timeout before the parent kills the original worker.
- Interrupted late-evidence delivery for recovery and notification requires one
  append-only evidence record and its original receipt after restart, preserving
  the unknown action and single effect.
- Revocation in all three categories starts with a saved, unsent known outcome.
  Stopped local administration revokes the original worker identity. The restarted
  server must reject its polling and result requests with explicit HTTP 401
  decisions. The journal retains its known outcome while the server retains the
  original conservative finalization; no handler is repeated.
- One health-check outbox case sets the journal record limit to one and interrupts
  a committed result reply. After the handler exits, admission must remain stopped
  while further check obligations remain visible in SLO reporting. Stopped-store
  inspection must find later committed checks without Start grants. Restart uses
  the documented outbox-only runner to deliver the saved result and retire its row.

The one-record case tests the configured record-count boundary, not the default
4,096-record limit, byte thresholds, or sustained fleet throughput.

Interrupted checks remain no-data. Interrupted recovery and notification actions
remain unknown in both the server state and worker journal; they are not discarded
to free capacity. The tests assert committed action state, journal counts and
independent invocation counts. Fault wrappers pause around real SDK protocol
calls; they do not replace the SDK transport or generate server responses.

The earlier private [report](../review/local-evidence.md#record-9490a982f79b0dcdc2cf00b6)
remains explicitly partial. Its complete 18-case runs passed with Go 1.27.1 race
instrumentation and Go 1.25.0. A stronger rerun then exposed an aggregate
empty-journal assertion that also counted newer checks. The execution-specific
correction passed three race-enabled server-restart repetitions; the retained
extra row was independently matched to the next scheduled check. The complete
rerun stopped before executing tests when the physical-disk safety gate failed.
The nine added cases were not covered by those earlier passes.

After disk space recovered, the current 27-case matrix passed with race detection
on Go 1.27.1. Go 1.25 then exposed a short-process observation race in the test:
the lock probe wrote its successful result and exited before the parent could
read the file. The parent now waits for successful completion before reading the
probe's result. The complete matrix subsequently passed on both compilers, with
both processes race-instrumented for Go 1.27.1. All 201 tagged SDK and 38 worker
tests also passed on each compiler. The
[current record](../review/local-evidence.md#record-9349625d7766f4e47d5aed84)
retains the source hashes and links the earlier failure. Public configuration,
independent review, sustained load, byte/unknown-action limits and the overall
release gate remain outstanding.

### Journal capacity through process restart

The [capacity checkpoint](../review/local-evidence.md#record-febb2e072dd75016224a10c1)
adds ten cases to the separate-process campaign. Each starts with two configured
monitors, independent target-side invocation counts and an initially empty real
worker journal. The original 27 cases remain in the same campaign.

| Boundary | Categories | Required observations |
| --- | --- | --- |
| Reserved and live payload bytes | Check, recovery, notification | A 160 KiB live budget reserves one 128 KiB outcome plus metadata before Start, despite spare record and concurrency slots. A roughly 96 KiB result then keeps admission closed while its committed receipt is withheld. Forced worker termination and server restart preserve the outcome digest and original receipt. Acknowledgement frees the live budget and permits another execution. |
| Allocated database bytes | Check, recovery, notification | A large real outcome grows the bbolt file across a configured 128 KiB threshold. New admission stops while the reserved completion and receipt writes still succeed. Removing the acknowledged row leaves allocated space above the threshold. Increasing that limit permits new work. |
| Bytes retained by unknown outcomes | Recovery, notification | An acknowledged unknown action retains its payload and receipt across worker/server restart and repeated terminal reconciliation. It prevents new admission at the configured byte limit. Increasing that limit permits another action without repeating or deleting the original one. |
| Records retained by unknown outcomes | Recovery, notification | A one-record journal stays full after an unknown result and restart even though byte capacity is available. Raising the record limit to two admits a second action while retaining both unknown outcomes. |

Stopped Raft inspection must find the original committed result and other work
without Start grants. The independent target must observe each execution at most
once. Status distinguishes reserved, live and allocated bytes, and the final
encrypted journal must not contain the outcome's plaintext payload.

These cases use small, explicit limits to exercise real boundary crossings. They
do not measure sustained throughput at the default 4,096-record, 256 MiB live or
512 MiB allocation limits. The checkpoint records completion for each compiler;
independent review, sustained mixed-load saturation, public configuration and
broader release qualification remain separate gates.

## Sustained mixed-worker load

The [frozen worker evidence procedure](../validation.md#frozen-worker-verification)
records the process and five-minute load tests with `GOWORK=off`. Its reader checks
the 37 process cases, actual load duration, zero duplicates and unreconciled
outcomes, journal drain, both compiler identities and their retained SDK module
contents against the selected source. The private release assessment consumes
these original inputs. Reader regression tests and workflow validation are not
evidence that the frozen worker campaign ran; final execution and independent
review remain required. No 24-hour test is involved.

`TestMainWorkerLoad` is explicitly enabled with `CPRA_WORKER_LOAD=1` and measures
five actual minutes. Its separate CPRa and SDK-worker processes use production
TLS, scoped authentication, encrypted Raft configuration, controller dispatch,
worker polling, Start, heartbeats and result delivery. An independent HTTP target
counts invocations for 64 monitors and all three categories. Staggered failure
cycles keep checks, recovery and notifications arriving. Yellow notifications
retain the five-minute cooldown; green notifications use the existing recovery
bypass. Each target invocation takes two seconds to maintain concurrency pressure.

The worker uses its unmodified defaults: 16 concurrent handlers, 4,096 records,
256 MiB live bytes, 128 KiB outcomes, 64 KiB diagnostics and a 512 MiB allocated-file
admission threshold. Initial admission assertions wait for all 16 independent
target invocations and journal reservations, rather than treating the runner's
earlier task registration as completed reservation. Every category must progress
within each 30-second interval, and every monitor must receive each category.

After measurement, a test-owned polling barrier stops new requests while accepted
handlers and real result delivery drain. Audited Snooze requests then cancel
unsent server work. This controlled drain does not replace the separate shutdown
and cancellation campaigns. A bounded receipt audit verifies the exact outcome
digest and independently observed status for every invocation. Retained results
must match the reopened Raft store. Missing check records require the exact
expired-check response from the restarted production server; missing action
records, generic conflicts and unavailable responses fail reconciliation.

The [Go 1.25 raw result and recovered summary](../review/local-evidence.md#record-35be96acc6e57bb43b67e956)
record a complete test/package pass: 300 measurement seconds, 2,206 invocations,
273 saturated samples out of 300, peak concurrency 16, no repeated invocations,
no unreconciled outcomes and an empty drained journal. Count-based retention
retires 425 completed checks, each subsequently confirmed by HTTP 410 with the
specific expired-check code. The source inventory remains unchanged during the run.

The outer writer failed parsing a fragmented Go output line. Its failure and
unrecorded Go process exit code remain explicit in the recovered summary; the
reader now joins fragments for future campaigns. Earlier failed attempts are
retained. The subsequent runs below capture complete campaign reports on both
compilers. Public configuration and independent review remain required. This
workload exercises sustained default concurrency pressure; default record/byte
budget exhaustion and fleet performance at one million monitors require separate
evidence. CI explicitly invokes the full test with
race detection on both compiler jobs; ordinary unit runs skip the opt-in case.


## Journal accounting under load

The first Go 1.27.1 race attempts failed when completed outcomes accumulated and
a recovery or notification reached its original server deadline. All 131 result
requests in the diagnostic attempt returned HTTP 200; request rejection did not
explain that backlog. Separate CPU and mutex profiles identified repeated full
journal scans in status and admission accounting. Accounting consumed 35.22% of
sampled worker CPU, including repeated decryption and JSON decoding while holding
the journal mutex. Status observation alone consumed 17.79%. Mutex-profile delays
are accumulated waiter time, not elapsed wall time.

The worker now reconstructs record, byte, pending-outcome and unknown-action
counts from authenticated encrypted records once at startup. Reservation,
replacement and deletion update those counters only after bbolt reports a
successful synchronous transaction, under the existing journal mutex. File
allocation is still inspected separately. No additional counters are persisted,
and the journal format and encryption/receipt contracts are unchanged. Startup
still validates all encrypted records; status polling no longer repeats that
scan. Rejected transactions cannot change the counters, and queued evidence still
prevents terminal cleanup.

A local Go 1.27.1 status benchmark uses three repetitions with `GOMAXPROCS=2`,
normal optimization and no race instrumentation. Setup and journal mutation are
outside the timed interval:

| Journal records | Previous median | Current median | Previous allocations | Current allocations |
| --- | --- | --- | --- | --- |
| 16 | 328.526 us | 0.998 us | 426 | 2 |
| 128 | 5,022.009 us | 1.315 us | 3,359 | 2 |
| 512 | 8,559.862 us | 1.087 us | 13,346 | 2 |

The [comparison record](../review/local-evidence.md#record-cce01450e8272b68c28af4fa)
retains all samples and input/report hashes. These measurements concern status
reads, not overall controller throughput, one-million-monitor capacity or an SLA.
Both compiler worker-package runs pass 40 top-level tests (122 including subtests)
with no skips or changed captured inputs. The new checks cover state/byte counts,
restart reconstruction, rejected/failed writes and retained evidence. An existing
permission fixture now explicitly applies its unsafe directory mode, so a private
process umask cannot inadvertently turn that negative case into a safe directory.
The original failed attempts remain recorded.

After that change, the full five-minute load campaigns pass without profiling:

| Compiler | Race instrumentation | Measurement | Checks | Recovery | Notifications | Saturated samples | Expired check receipts |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Go 1.27.1 | Yes, both processes | 300.000970 s | 1,407 | 351 | 385 | 263 / 299 | 384 |
| Go 1.25.0 | No | 300.000579 s | 1,437 | 360 | 391 | 271 / 299 | 413 |

The [release-compiler report](../review/local-evidence.md#record-88da78f4544f99e36611a3f0)
and [minimum-compiler report](../review/local-evidence.md#record-8ec80d742accb0ea4ade1bfd)
record actual process exit code 0 and complete test/package termination. Both
reach peak concurrency 16, reconcile every independently observed invocation and
original outcome, and drain to zero records, pending outcomes, unknown actions and
active handlers. Duplicate invocations and unexplained outcomes are zero. The
specific expired-check response is verified against the restarted production
server for each retired check. Inputs remain unchanged during each campaign.
These runs use the explicit development workspace and stopped encrypted
configuration fixtures. Public external configuration, final artifact/deployment
qualification, remaining release gates and independent review stay open. The
refreshed 37-case process campaign passes on both compilers against the accounting
change, with race instrumentation on Go 1.27.1 and no skipped cases or changed
captured inputs.

The [accounting checkpoint](../review/local-evidence.md#record-7ac8b3dabea17162b24d8d86)
checks exact coverage of all 37 process cases, complete package termination,
unchanged captured Go/module inputs and all zero-loss/drain requirements. It
binds the reports and status benchmark by hash and leaves release qualification,
public configuration and independent review explicitly open.

## Captured source and offline SDK inputs

The [1 October private execution checkpoint](../review/local-evidence.md#record-eacc8cbc59fe1cb1c2d7637a)
refreshes all 37 process/crash cases and five-minute mixed-load tests from captured
working source with `GOWORK=off`. Runtime, test and module bytes match between Go
1.25.0 and the race-instrumented Go 1.27.1 run. The reader and its regression tests
changed between captures; no reviewed committed release identity is claimed.

The tests use the selected compiler even when `-trimpath` removes its runtime
GOROOT. Explicit check-expiry outcomes are audited with their original execution
identity and digest, separately from accepted receipts. Generic errors cannot
satisfy this path, and recovery/notification effects still require real receipts.
The audit's three focused scenarios pass under both compilers.

The recorded Go 1.25 test process passed, but its original reader failed because
Go split the load measurement across several JSON output events. That failure is
preserved; the corrected reader validates the original output without rerunning
the successful test process. It joins only events belonging to the same test and
retains the raw-output and reconstructed-line limits. The Go 1.27.1 run uses the
corrected reader from the start.

Both runs reconcile every independently observed effect and drain their journals
without repeated invocations, unexplained outcomes or unknown actions. Stopped
fixture files are cleaned up. These results do not replace clean release
qualification, independent review, configured-provider evidence, native-platform
tests or fleet comparisons. No 24-hour test is required or performed.
