---
title: Plan and status · Candidate author review
description: Plan and status · Candidate author review for the reviewed CPRa source; see the version and availability notice.
cpra_scope: plan
---

> **Candidate design and evidence:** this record describes unreleased implementation work or an approved plan. Its checklists do not establish availability in `main`. [Version and availability](../versions.md) identifies the earlier 13 September reviewed snapshots; [current implementation status](STATUS.md) records the later branch checkpoints.

# Candidate author review

The review compared the approved requirements with changed code, tests,
configuration, operator views and harness behavior. This is an author review,
not an independent sign-off.

## Selected first candidate, 3 October 2026

The user approved `v0.1.0-rc.1` as the first application, SDK and worker
version. The matching Helm chart version is `0.1.0-rc.1`; its `appVersion`
and the Compose image tag use `v0.1.0-rc.1`. Nested module tags will be
`sdk/go/v0.1.0-rc.1` and `sdk/go/worker/v0.1.0-rc.1`.

This selects the version; it does not freeze the source or qualify any
artifact. Candidates remain private until all agreed gates pass. No tag,
release, module, image or chart was published by this selection.

The [candidate version review record](../review/local-evidence.md#record-853668ba8ddfa48457648c9d)
contains 94 passing focused tests, strict chart lint and rendering with pinned
Helm 3.22.0 and 4.0.4, and unchanged final input identities. Both rendered charts
select `ghcr.io/ziad-hsn/cpra:v0.1.0-rc.1`. The corrected policy test checks the
workflow's delegation to the frozen build-matrix runner; the recorder tests use
fixtures and do not qualify an executed application/SDK/worker matrix. Original
failed and interrupted attempts remain in the record. All scratch directories
from these attempts were removed.

## Management deployment integration, 3 October 2026

The chart now connects explicit management settings to immutable policy,
local wrapping-key and TLS Secret references. The read-only default is preserved.
A non-root init container copies selected sources into private `0700`/`0600`
paths; CPRa mounts only that protected directory read-only. The init container
has no state volume. Probes use verified loopback HTTPS; the Helm test Job
receives only the read token and CA and verifies the Service DNS certificate.
Management requires persisted Raft and rejects generic chart-managed Ingress.
The [operator guide](../container-helm.md#management-over-https) documents these
requirements, stopped authentication changes and retained decryption keys.

The [Helm management review record](../review/local-evidence.md#record-3fb39b45a76af2d51cbb750e)
retains the original four rejected management cases, fifteen passing chart
contracts on each pinned Helm client, strict managed lint and rendering, and
twenty-six packaging/workflow contracts. A real Linux process consumes the
rendered bootstrap and runtime with remapped host paths. Its seven checks cover
private copies, storage-free preflight, actual HTTPS CLI probes, committed
credential/monitor changes, forced termination, recovered identities and
authentication, and wrong-key refusal. The generated plaintext credential and
wrapping key are absent from Raft files; disabled-target receipts remain zero.
Changing bootstrap sources does not replace the committed authority.

This process evidence uses UID 1000 and development executables built with
Go 1.27.1 and an explicit SDK workspace. It does not qualify packaged binaries,
UID 1001 Kubernetes mounts, Secret projection, Service TLS, RWOP/CSI behavior,
managed cluster upgrades or restore. At that checkpoint, the live cluster
harness exercised only the read-only chart mode. The source-level extension
below adds managed scenarios; their actual cluster execution and independent
review remain open before the candidate can be qualified.

## Managed Helm release evidence gate, 3 October 2026

The release evidence reader previously accepted both Helm reports when
management execution was explicitly absent. The new negative contract
reproduced that acceptance before the correction. The reader now requires
actual managed observations in addition to every read-only scenario, with
matching executable/image identities, private file modes, conditional committed
writes, authorization failures, changed pod identity, retained catalog
incarnations, stopped access rotation and complete event recovery.

The [managed runtime harness](../review/source/scripts/packaging/helm_management.py.txt)
uses a separate state claim and release after the read-only owner is stopped.
It exercises verified TLS, encrypted credentials, monitor creation/editing,
restart, authority retention, stopped reader/operator rotation, RWOP contention,
and recovery from a stopped complete-directory clone. Both client campaigns in
the private workflow enable it; the Helm 3 campaign also requires the managed
upgrade to Helm 4. No provider-account resources or production namespaces are
used. The local clone deliberately does not claim the authenticated CLI restore
or its explicit-restore access reset.

The [Helm runtime gate record](../review/local-evidence.md#record-a09c5d7e42ea66bccc56e0b3)
retains the original gate failure and passing reader, harness, workflow,
publication-consumer and chart contracts. Real local TLS tests reject the wrong
host or untrusted certificate before a bearer reaches the fixture. The transport
does not follow redirects or retry uncertain mutations. Interrupted tunnel
processes are reaped, and unconfirmed token rotation does not resume an owner.
These are local harness and evidence-validation checks, not an executed
Kubernetes campaign, packaged application qualification or independent review.
No new cluster, release tag, image, chart or documentation publication was made.

## Publisher input verification, 3 October 2026

The source review found that both HTTP asset upload and ORAS checked a source
file before subsequently reading its mutable path. The corrected negative
tests reproduced different bytes reaching an actual loopback HTTP receiver and
a native uploader fixture. The first failed test setup is retained separately;
it is not evidence of the input defect.

Both transports now stage a private copy and verify its size, SHA-256 and source
identity before upload. ORAS also runs a copied, checksum-verified executable.
Unsafe file types, source changes while copying, workspace exhaustion and expired
copy budgets stop admission. Changed original files cannot change an admitted
body. The existing journal and remote byte readback still decide continuation
after an uncertain result; mutations are not automatically retried.

The [publisher input review record](../review/local-evidence.md#record-d1ca67a8df494ee9f7ebad55)
retains the reproduced failures and final checks. Its native local campaign uses
pinned ORAS 1.3.4, Distribution 3.0.0 and Helm 3.22.0/4.0.4, including process
termination after accepted image/chart writes and identical downloaded chart
bytes. Its OCI payloads and chart are small synthetic fixtures, not final CPRa
images, chart qualification or production-account publication. Frozen source,
independent review and the remaining release gates stay open. Publication remains
locked; no public object or endurance run was created.

## Integrated Go source review, 3 October 2026

The [integrated Go record](../review/local-evidence.md#record-f9f5479c6aaed7b428b37c2d)
adds complete SDK/worker checks on Go 1.27.1 and Go 1.25.0 and real separate-process
worker scenarios against the development server. These are unfrozen source
checks using an explicit workspace, not downloaded-module or packaged-release
qualification.

The killed-owner staging test reproduced premature ownership rejection. The
acquisition budget now permits bounded bbolt lock retries while preserving
exclusive ownership, synchronous writes and cleanup quotas. The selected final
source suites pass in both build variants and on both compilers. Saved Go source
captures that polluted ordinary package selection were archived losslessly;
historical results and logs were preserved.

The broad A2A monitor-admission failure remains open despite passing isolated
scenarios. Its diagnostics now preserve safe error classifications. The broad
persistence run exhausted its whole-package limit; the active case passes in
isolation. Make and native CI now use the release runner's existing 45-minute
budget. The fresh complete default run is recorded below; the complete frozen
release matrix remains open. None of these
focused checks supplies independent sign-off or closes publication gates.

## Application and dashboard checkpoint, 3 October 2026

The fresh complete default race run records all 31 test packages passing and
no failed Go events; its persistence package completes at the corrected budget.
The original coordinator was lost during task continuation, so the preserved
package output has no captured parent exit status. It cannot substitute for a
qualifying frozen-release record. The earlier A2A admission timeout did not recur;
its cause remains undiagnosed. The other complete application profiles still
need qualification against frozen source.

The focused author review reproduced cache-path loss in both frontend build
helpers. The correction preserves configured cache directories while retaining
the execution/credential allowlist. Existing regressions, a clean 513-test
frontend rebuild and all eight required browser cases in each compiler/tag
profile pass. The rebuild preserves every embedded output and updates the
manifest's stale recipe identity. Original failed preflight and browser attempts
remain in the [development verification record](../review/local-evidence.md#record-9ad3f31cf44c02740a9bc4ff).

A tagged browser attempt also reproduced a local bind collision before readiness.
At that checkpoint, the fixture closed its selected-port listener before normal
application startup, leaving a reservation gap. The isolated rerun did not remove
that risk; the correction below now keeps ownership in the application.
Automated accessibility
scans report no violations but retain incomplete findings requiring human review.
This author review supplies neither independent sign-off nor final artifact,
provider-account or publication qualification.

## Application-owned listener fixtures, 3 October 2026

The focused author review compared all 23 changed fixture files with retained
originals. Normal startup already exposed the real bound address through
`runOptions.webStarted`; the correction uses that callback with loopback port
zero and changes no runtime interface or production source. A readiness channel
publishes the address before SDK/browser/CLI use. The options passed to the
application are copied first. File-only subprocesses use atomic readiness-file
replacement; deliberate bind-failure cases retain their occupied sockets.

The original fixture fails the new occupied-port regression. Three targeted
Go 1.27.1 race repetitions pass. The complete affected root package passes with
Go 1.27.1 and Go 1.25.0, each in default and tagged variants. Both tagged runs include
37 actual separate-process worker scenarios with race-instrumented worker
children. All eight required real-server browser cases pass in each profile,
with matching source inventories, and both root vet variants pass. The
[listener ownership record](../review/local-evidence.md#record-1a56fa77db5112730fd7df41)
preserves the original failure, exact source captures, command exits, logs,
tool identities and cleanup observations. The
[implementation status](STATUS.md#application-owned-listener-fixtures-3-october-2026)
lists instrumentation and explicit skips.

This closes the fixture's port-reservation issue. It does not diagnose the earlier
broad A2A admission timeout, supply independent review, freeze the candidate or
qualify packaged/native/provider execution. Temporary compilers, browser tools,
caches and staged dashboard output were removed; retained evidence stays private.

## API admission and shutdown, 3 October 2026

The focused author review traced the admission barrier through readiness,
management commits, tagged worker routes and application shutdown. Production
readiness can acquire storage locks. The original API held its admission mutex
while invoking that callback, so shutdown could not close the barrier or observe
its expired context until readiness returned. It also checked request
cancellation only before readiness, allowing cancellation during that wait to
reach the commit callback.

The original implementation fails both controlled regressions. Readiness now
runs outside the mutex, with the final stopped-state check and active callback
registration serialized against shutdown. Request cancellation is checked again
at that boundary. Already accepted callbacks still join through the original
drain channel; cancellation never reopens admission. Catalog commit and
authentication checks remain in their existing paths.

Twenty focused race repetitions, the complete HTTP-server package on both
compilers in both build variants, both vet variants and related application
shutdown/recovery tests pass. The
[implementation status](STATUS.md#api-admission-and-shutdown-3-october-2026)
records counts, instrumentation and explicit browser skips. The
[admission review record](../review/local-evidence.md#record-f3d45c67c91e0cb488930581)
preserves the original failures, source captures, command exits and cleanup
observations. All captured inputs stayed unchanged during the final checks.

The same runtime inspection confirms that owner ticks refresh local catalog
demand, capacity sizing calls `FindCForSLO` with measured variability, and the
production dispatch wrapper preserves operation identity when a provider
panics. Those paths needed no additional change in this review. No performance
improvement or provider effect is claimed from that inspection.

At that checkpoint, readiness callbacks remained uncancellable. The tests establish the
admission synchronization contract, not a real stalled-disk campaign or complete
process-shutdown qualification. This is author review of unfrozen Linux amd64
development source, not independent sign-off. The earlier broad A2A admission
timeout and the remaining release gates stay open.

## Request authentication and catalog/history deadlines, 3 October 2026

The focused author review preserves the original failing authentication and
history-lock regressions. Cancellation now prevents new policy admission and
worker credential reads in the covered paths, while accepted callbacks retain
the replacement barrier until their actual completion. Catalog, action,
incident, history and aggregate-cursor reads use caller contexts. The v1 history
cursor envelope is retained while its scan shares the bounded v2 traversal.

Full authentication/HTTP tests and selected persistence/management tests pass
in all four compiler/build profiles: Go 1.27.1 uses race detection, and Go
1.25.0 uses ordinary verification. Every affected-package vet profile and all
four application/CLI compile checks pass. Browser fixtures and parent-only
process helpers remain explicit skips. All 1,983 source inputs stayed unchanged
during the final profiles; the 26-file correction, original source captures,
terminal exits and compressed raw output are retained in the private
[API read review record](../review/local-evidence.md#record-f481a98b682ea4a757490d0f). Its publisher mapping covers nine
requirements with unchanged source/test hashes from the preceding local
checkpoint; this is author inspection of implemented helpers, not final
publication or independent sign-off.

At that checkpoint, the next context inventory covered operation and retained
collection-result wrappers, nested validation, mutations, SLO aggregation and
cursor cleanup. The following correction addresses those request paths. Native filesystem calls remain cooperative cancellation
boundaries. The complete accumulated-source review, frozen candidate, packaged
and downloaded-consumer checks, native/Helm/provider/A2A/comparative evidence,
independent review and matching rendered documentation remain open. Candidates
stay private at `v0.1.0-rc.1`; the public publisher stays locked. No publication
or endurance run was performed, and the canceled 24-hour run stays outside the
goal.

## API readiness and observation cancellation, 3 October 2026

The focused author review traced request contexts through API readiness, the
controller, collection owners and Store/FSM diagnostics. The original GET
middleware fails all five server-deadline contract cases; the POST worker-poll
control passes. Production readiness previously called contextless diagnostics,
so moving that callback outside the admission mutex did not cancel its own lock
waits.

GET observations now receive the configured read deadline before authentication.
The production readiness chain and state/config diagnostics preserve the request
context. Mutation readiness has a separate observation budget; worker POST polls
keep their existing request lifetime. Canceled diagnostic waits return without
calling `MarkUnavailable`, altering committed state or poisoning a collection
owner. Initialization preserves cancellation rather than reporting it as an
owner failure. State diagnostics still expose sanitized storage errors and
explicit measurement availability.

Controlled Store/FSM lock tests, HTTP request-cancellation gates and subsequent
healthy reads pass. The complete HTTP package passes on both compilers in both
build variants. The full controller package, selected collection/persistence
tests, application initialization/shutdown/TLS recovery scenarios and both vet
variants also pass with their recorded instrumentation and explicit helper/browser
skips. The [implementation status](STATUS.md#api-readiness-and-observation-cancellation-3-october-2026)
records the counts. All final test inputs stayed unchanged.

The private [readiness observation record](../review/local-evidence.md#record-8ce7b4e4690a8576bcd3088c)
retains the original deadline failure, a callback-signature build failure and a
fixture authentication failure separately from the final passes. The management
fixture initially used plaintext HTTP, so authentication rejected it before
readiness; the corrected fixture uses HTTPS. Retained before/after captures cover
all 38 changed Go files. The original usage file was reconstructed and matched to
the initial inventory hash; the record identifies that distinction.

Filesystem calls already executing in the OS remain outside Go context
cancellation. Authentication-policy mutex acquisition and remaining catalog and
history reads still require audit; adding a request deadline alone does not
prove a complete handler bound. This is author review of unfrozen Linux amd64
development source, not independent release sign-off. Packaged/native execution,
configured-provider/A2A effects, final distribution and short performance
campaigns remain separate gates. Candidates remain private; no publication or
endurance run was performed.

## Worker protocol source review, 3 October 2026

The focused author review traced offers, Start grants, heartbeat updates,
outcome receipts, expiry, finalization, requeueing and check-record retention
through the committed state machine. First committed Start returns the
executable grant once. Repeated Start and finalization use the original
execution identity; only work that never started can be requeued. Interrupted
actions with uncertain external effects remain unknown. Outcome capacity is
reserved independently from the intent quota, and unknown actions cannot be
retired to free that capacity.

The review also found that inline notification preparation discarded temporary
Raft leadership errors. The correction preserves those causes so the controller
can recover from a temporary leadership change. The retained
[worker leadership review record](../review/local-evidence.md#record-f0c688da557e1be369149b55) contains the original failing
classification cases, ten passing management tests, six passing controller
tests and passing vet output. These checks used Go 1.27.1 with `externaljobs`;
they do not reproduce an election inside preparation or establish independent
review or full candidate qualification.

## Corrections made during review

- Restored empty action/cooldown maps safely after a snapshot of a healthy monitor.
- Cancelled unsent actions when a monitor is removed or its revision changes.
- Rechecked maintenance at dispatch and execution and rescheduled deferred work.
- Allowed observed healthy recovery to cancel an intervention that never started.
- Kept pre-intervention check generations from invalidating verification.
- Recorded scheduled obligations before admission and counted unfinished work
  against threshold attainment without waiting for its completion.
- Removed whole-window copies from each histogram observation; frozen copies
  remain at the snapshot boundary. The recorder microbenchmark measured zero
  allocations per observation on this host.
- Redacted arbitrary observer text and retained only numeric/boolean evidence,
  resource fingerprints and source hashes in private evidence artifacts.
- Verified complete backup restoration, including the retained event catalog.
- Preserved numeric API routes, bounded pages, authentication and actual latency
  availability in the dashboard.

## Earlier checkpoint evidence boundaries

Real subprocess crash tests and six local live-driver scenarios passed. The
browser timeline agreed with API/CLI events. Default/all-driver Go checks,
Go 1.25 compatibility and dashboard checks passed. Local AMD64/ARM64/source
archives include dependency notices and exclude data directories, credentials,
node_modules and local campaign evidence. ARM64 was cross-compiled, not executed.

Provider-account readers, systemd privileges and designated Kubernetes/AWS
resources are not configured. Database fixture downloads and large campaigns
failed their physical-disk prerequisite before starting. The million-monitor
rate, p99 targets at that rate, 24-hour endurance and resource stabilization
remain unverified. The harness preserves failed and incomplete outcomes.

The user's 1 October 2026 instruction removes the 24-hour endurance test from
the task and required completion checks. No endurance result is claimed.

## Release decision

Keep the candidate application separate from published main until
independent review and the required provider and comparative evidence
are complete. No full-provider certification, production-capacity claim, HA
claim or unconditional SLA is justified by the available evidence.

Earlier candidate documentation was published for review alongside the main
documentation; the [version and availability notice](../versions.md) identifies
those reviewed snapshots. It does not publish or qualify the current private
application candidate or close the release gates above.

## Management preparation and SLO request deadlines, 3 October 2026

The focused author review adds context-bearing ordinary operation capture/pages,
collection-result and validation wrappers, preparation/control/action reads,
submission diagnostics and SLO aggregation. Unknown durable submissions keep
that classification before diagnostic health is read; confirmed writes are not
made ambiguous by optional enrichment. Observation capture/encoding no longer
holds shared cursor metadata, and cancellation still releases owned quota.
Native administrative operation inspection preserves its diagnostic policy.

Full HTTP/SLO and selected persistence/management checks pass for Go 1.27.1
(default/tagged race) and Go 1.25.0 (default/tagged ordinary). Both vet variants
and four application/CLI compile checks pass. Original deadline failures, the
administrative-read regression, an invalid new fixture, two rounding-sensitive
assertion failures and their passing corrections remain recorded. Large staged
cases passed in an earlier overall-failed attempt and are outside the final
selection. Browser and parent-only helper skips are explicit.

The initial matrix held all 1,988 inputs. Only the new SLO fixture changed before
corrected profiles; production inputs stayed fixed. The 30-file correction adds
no package and changes no SDK/schema or persisted format. Exact captures,
source identities, failed attempts and terminal records are in the private
[management context review record](../review/local-evidence.md#record-f9a60baea4b3c117616b747f). Native filesystem and
synchronous statistics boundaries still need their own operating evidence.
This is author review, not independent approval, a source freeze or packaged
qualification. The complete release gates remain open; versions stay at
`v0.1.0-rc.1` with chart `0.1.0-rc.1`, private until qualification. No publication,
provider, comparative or endurance campaign was run.

## A2A admission and verification-process cleanup, 3 October 2026

The earlier disabled-monitor create failure remains undiagnosed: its retained
error omits the transport cause, status and handle. Eight real TLS/Raft
lost-reply tests now cover both A2A roles, header/body loss and
cancellation/deadlines. They preserve uncertainty, retain only received handles,
issue one create and leave the monitor disabled across restart, without provider
operations. The SDK guide documents this distinction from an unknown agent
effect, and failure diagnostics expose only timing and counters.

The full default application/controller race profile passes on Go 1.27.1.
Focused tagged race and both Go 1.25.0 ordinary admission/escalation profiles
pass, as do both vet variants. Browser/helper skips remain explicit. A broader
tagged root run timed out during a worker capacity case and remains failed. Its
surviving fixture was stopped by verified identity. Campaign capture now cleans
its process group on every exit; a real subprocess regression and 27 evidence
tests pass. CI uses bounded A2A groups; local workflow lint passes.

The private [A2A admission review record](../review/local-evidence.md#record-a060fb74aa7e2a2345d4b533)
retains exact source, original/failed output and passing terminal records. Go
inputs stayed fixed across the qualified profiles; later workflow/Python changes
have separate checks. This is author review, not independent sign-off or complete
frozen-source qualification. Original-cause, complete worker, configured-agent,
provider, platform, comparative, publication and rendered-site gates remain
open. Candidate versions and privacy remain unchanged; no 24-hour run occurred.

## Dedicated worker process and load review, 3 October 2026

The current dedicated selection passes all 37 crash/capacity cases plus five
actual measurement minutes on each compiler, without skipped cases. Go 1.27.1
uses race instrumentation in both processes; Go 1.25.0 uses ordinary builds.
Both independently counted targets reach 16 concurrent handlers, and accepted
work drains with zero duplicate invocations or unreconciled outcomes. The load
totals are 2,174 and 2,252 invocations respectively, including setup and drain.
The fault cases preserve uncertain actions, original receipts, reserved/live and
allocated capacity distinctions, and journal ownership during uncooperative
shutdown. The earlier combined run remains failed.

The original result streams pass the exact-coverage and helper-identity
inspector. All 1,990 inputs stay fixed across the profiles. Author review of
selected execution, journal and release-evidence paths establishes no new runtime
defect; no application, SDK, schema or format change is made. The private
[worker process and load review record](../review/local-evidence.md#record-ff596b7e08e6e91e7a92b13b)
retains source captures, commands, compiler identities and original results.

This closes the selected current-source coverage gap, not the complete release
gate. Test executables, the development workspace and local synthetic effects
remain distinct from frozen module/package qualification and configured-agent or
provider evidence. Full accumulated-source and independent review, final
packaged/browser/security checks, public consumers, platform/Helm operations,
reproducibility, comparative evidence and final-commit publication stay open.
Candidate versions remain `v0.1.0-rc.1` with chart `0.1.0-rc.1`, private until
qualification. The 24-hour test remains outside the task.

## Native local installation review, 3 October 2026

The service installer had a source-change gap: validation used the caller's
executable, but publication reopened it after validation and shutdown. Actual
Linux fresh-install and update fixtures both reproduced changed contents being
installed. The installer now freezes a private copy before validation, uses it
for replacement and reports staging cleanup failures. Source-change and
rejection/cleanup cases pass with both supported compiler profiles and both
normal/tagged builds.

The macOS source also recursively changed credential helpers into non-executable
configuration files and accepted special state entries. Repair now targets
managed objects and rejects linked/non-regular state. Its new regression cases
compile under Go 1.27.1 and 1.25.0 on both Mac targets; native execution remains
pending. There is no new package or symbol renaming.

Four local administration/path profiles pass: 31 top-level tests and 60 nested
cases for normal builds, 42 and 74 for `externaljobs`. Go 1.27.1 uses race
instrumentation; 1.25.0 uses ordinary builds. The helper-only skips are exercised
through parent process tests. Sixty-nine packaging/native-harness contracts and
selected vet checks pass. Six release-target test executables cross-build;
these are not release archives or native-platform execution results.

The [private native installer review record](../review/local-evidence.md#record-22d0ed3c4b7a10e8540eb70c)
retains failures, exact source and result identities, limits and all open release
gates. This is author review, not independent sign-off or frozen qualification.
Candidates remain private at the agreed prerelease versions. Production-account,
configured-agent, full platform/package/Helm, comparative, final-commit and
publication gates remain open; the 24-hour test remains outside the task.

### Publication admission author review, 3 October 2026

The separate release decision rechecks all fourteen component readers and the
signed independent approval under the operator's pinned trust policy. Direct
protected publisher phases now require that decision from the original inputs;
they bind the chosen distribution, OCI assembly and module cache and recheck
retained hashes before a public phase. A saved ready summary, partial evidence
or a candidate-selected trust policy cannot admit publication.

All 701 release-tool contracts pass with no skips. The positive decision tests
combine synthetic prerequisite outcomes with actual disposable OpenSSH
signatures; local process, Git and HTTP fixtures remain distinct from provider,
native-platform or official signing evidence. Three failed development attempts
are retained, including two corrected test expectations. The
[private review record](../review/local-evidence.md#record-0b0cbe61fa7c072a82cf1d98)
preserves exact inputs and the complete remaining goal.

This is author review, not independent approval or frozen-release qualification.
The private workflow remains locked pending integration and all original gates.
Application, SDK and worker remain `v0.1.0-rc.1`; the chart remains
`0.1.0-rc.1`. No public object or 24-hour test was produced.
