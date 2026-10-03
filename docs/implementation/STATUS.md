---
title: Plan and status · CPRa implementation status
description: Plan and status · CPRa implementation status for the reviewed CPRa source; see the version and availability notice.
cpra_scope: plan
---

**Scope correction, 1 October 2026:** The user removed the 24-hour endurance run from the task and mandatory completion checks. No such run has been started or completed. Historical entries below record earlier requirements; they do not reinstate this test. Comparative performance remains separate from endurance, and documentation must not claim an unperformed endurance result.

## First candidate version selected, 3 October 2026

The user approved `v0.1.0-rc.1` for the application, SDK and worker, with
Helm chart version `0.1.0-rc.1`. Module requirements already select that SDK
and worker version. The source chart, Compose image example and private-release
workflow defaults now agree. This closes version selection; accumulated source
review, candidate freeze, independent review and artifact qualification remain
open. Candidates remain private until all agreed gates pass. No publication or
endurance run was performed.

Pinned Helm 3.22.0 and Helm 4.0.4 each pass ten chart contracts, strict lint and
rendering with the selected image tag. The 58 release/package/workflow contracts
and sixteen build-reader/recorder tests also pass without skips. The policy test
now executes the workflow wrapper with a recording interpreter to check its
frozen-source, dependency-cache and compiler bindings; it does not execute the
complete Go matrix. All 117 captured inputs remained unchanged during the final
checks. The [candidate version review record](../review/local-evidence.md#record-853668ba8ddfa48457648c9d)
retains the original policy-test failure, the disk-usage sampler interruption,
the passing reruns and cleanup observations. Temporary tools and compiler caches
were removed; the largest observed scratch allocation was 127,221,760 bytes.

The deployment gap found by that version review is now connected in source.
Explicit management values select immutable policy, local wrapping-key and TLS
Secret references, a private non-root init copy, a read-only protected directory
and verified HTTPS probes. The read-only default is preserved. Fifteen chart
contracts pass on each pinned Helm client, with strict managed lint/rendering
and twenty-six packaging/workflow contracts. Seven checks using the rendered
settings and real Linux application/CLI processes cover committed writes,
SIGKILL restart, retained identities and authority, redacted credentials,
zero disabled-target receipts and wrong-key refusal. This uses development
executables, an explicit SDK workspace and UID 1000, not actual Kubernetes
execution or final artifacts. The
[Helm management record](../review/local-evidence.md#record-3fb39b45a76af2d51cbb750e)
retains the original failures and passing checks. The
[candidate author review](review.md) keeps actual UID 1001 Secret mounts,
Service TLS, managed cluster upgrades/restore and CSI fencing visibly pending.

The private Helm workflow now requires an additional managed deployment in each
client campaign. The evidence reader's reproduced read-only acceptance gap is
closed: it requires protected UID 1001 files, verified TLS, conditional encrypted
writes, authorization failures, retained incarnations and authority, stopped
access rotation, calibrated probe counters, RWOP rejection and retained-history
recovery from a stopped complete-directory clone. The managed Helm 3 to 4
upgrade is also required. Local reader/harness/workflow contracts pass, including
actual TLS rejection before bearer transmission and tunnel cleanup. These
contracts do not supply native cluster observations. The
[runtime gate record](../review/local-evidence.md#record-a09c5d7e42ea66bccc56e0b3)
and [author review](review.md#managed-helm-release-evidence-gate-3-october-2026)
preserve this boundary. The actual isolated cluster campaign, frozen source and
independent review remain open; no publication or endurance run was performed.

Publisher source review also reproduced a change between file verification and
upload in both transports. HTTP asset bodies and ORAS inputs now use private,
verified copies; the ORAS executable is copied and checked too. Unsafe inputs,
changes during copying, exhausted workspace capacity and expired copy budgets
prevent upload. Existing interrupted-publication behavior still reconciles the
same remote identities and bytes without automatic mutation retries. The
[publisher input review record](../review/local-evidence.md#record-d1ca67a8df494ee9f7ebad55)
retains local HTTP/process contracts and native ORAS/Distribution/Helm transport
checks. Those small fixtures do not qualify final CPRa artifacts, public modules
or GitHub/GHCR publication. Source freeze, independent review and the broader
release gates remain open; publication stays locked.

The [candidate author review](review.md) records the focused worker protocol
review and the correction to temporary leadership-error handling. The retained
[worker leadership review record](../review/local-evidence.md#record-f0c688da557e1be369149b55) records the original failure,
sixteen focused passing tests and vet. This is working-source evidence, not a
frozen candidate or independent sign-off.

## Integrated Go source checks, 3 October 2026

The application, SDK and worker were checked with checksum-pinned Go 1.27.1
and Go 1.25.0 using an explicit private development workspace. The SDK passes
186 top-level tests in its default build and 203 with `externaljobs`; the worker
passes 41. These counts apply separately to each compiler. Go 1.27.1 used race
detection; the minimum-compiler checks used ordinary builds. SDK and worker vet,
application vet and formatting of all 1,167 captured Go files pass.

Separate application and worker processes pass 37 scenarios covering all three
handler roles, lost replies, crashes, revoked credentials, late evidence,
shutdown and capacity limits. Independently counted targets are local HTTP
fixtures. This establishes development-process interoperability, not final
packaged execution, public-module installation or production-agent effects.

The broad application run exposed a staging-lock failure after a killed owner.
It failed three of five isolated repetitions. Its 25 ms acquisition timeout was
shorter than bbolt's 50 ms Unix retry interval. The corrected 250 ms window keeps
exclusive ownership and synchronous writes. Twenty proposal repetitions and the
selected final reselection suites pass; the final suites cover both compilers
and default/tagged builds. Their subprocess-only helper skip remains explicit.

The broad run also timed out admitting an A2A monitor before provider dispatch.
Three isolated repetitions and all six escalation scenarios pass at unchanged
deadlines. Safe test diagnostics now report status, deadline, cancellation and
operation-handle availability. The original broad-run failure remains open.
The persistence package exhausted its cumulative 20-minute test limit; its
active inspection-budget case passes three isolated repetitions. Make and
native CI now match the release runner's existing 45-minute limit. The fresh complete default run is recorded below; the remaining complete
build profiles and frozen-release process evidence are still required.

The initial selection also compiled saved evidence snapshots as application
packages. Thirty-two saved files were archived with verified contents and modes
before removing only those captured-data directories. Original evidence results
and logs remain unchanged. Go now selects 33 application packages. Save future
source captures as text or archives; executable fixtures belong in private
temporary directories.

The [integrated Go review record](../review/local-evidence.md#record-f9f5479c6aaed7b428b37c2d)
preserves original failures, incomplete runs, exact inputs, focused passing
checks and cleanup observations. Source freeze, independent review and the
complete application/interface/native/provider/performance qualification remain
open. Candidates stay private; no publication or endurance run was performed.

## Application and dashboard checkpoint, 3 October 2026

The fresh complete default application race run recorded terminal passes for
all 31 test packages, plus two packages with no tests. It passed 1,640 top-level
cases and 4,720 tests/subtests at the 45-minute package budget, with no failed
Go events. Its fifteen test skips comprise eight separately required browser
fixtures and seven subprocess-only helpers. The persistence package completed
in 1,258 seconds. All 1,979 captured inputs remained unchanged through this run.
The original coordinator handle was lost during task continuation; observation
of the same live process preserved complete Go output, but its parent exit code
was not captured. This is complete package-test evidence, not a qualifying
frozen-release process record.

Two dashboard build helpers discarded explicitly configured cache locations.
They now preserve the supplied Go and package-manager cache paths while keeping
ambient execution overrides and credentials excluded. The existing regression
suite passes eight cases. A clean pinned rebuild passes type checks, lint and
513 frontend tests across 33 files, reproducing all 42 embedded assets and
notice outputs byte for byte. The manifest also corrects its previously stale
recipe digest; the actual bundle did not change.

All eight required real-server browser scenarios pass in each of four profiles:
Go 1.27.1 with race detection and Go 1.25.0 without it, each in default and
`externaljobs` builds. They use unchanged input inventories and an explicit
private development workspace. The first default attempt failed because its
local `dashboard/dist` was absent; the required fixture then used a verified
copy of the unchanged embedded output. One tagged attempt failed binding its
selected local port before readiness. At that checkpoint, the isolated tagged
rerun did not close the fixture's listener-reservation gap. The application-owned
listener section below records its correction and fresh verification.

The separate frontend HTTP fixture passes fifteen automated accessibility
checkpoints and twelve keyboard checks with no reported automated violations.
Its ten incomplete findings still need human review; these scans do not establish
complete WCAG conformance or production API behavior. Twenty-two documentation
contracts pass. The original failures, tool identities, raw output, source
inventories and cleanup observations are retained in the
[application and dashboard record](../review/local-evidence.md#record-9ad3f31cf44c02740a9bc4ff).

The earlier broad A2A admission timeout did not recur in the complete default
run, but its cause has not been diagnosed. Other complete application build
profiles, frozen-source and packaged execution, independent review, public
module installation, native deployment, configured-provider effects and matched
performance evidence remain open. Candidates stay private. No publication or
endurance run was performed.

## Application-owned listener fixtures, 3 October 2026

Normal-startup tests now request `127.0.0.1:0` and receive the actual bound
address through the existing `runOptions.webStarted` callback. Runtime options
are copied before the application starts; the fixture publishes its address
after readiness. File-only A2A subprocesses atomically publish the actual address
in their existing readiness file. Intentional bind-failure tests still hold a
real occupied socket. This correction changes 23 existing root test files and
no production source.

The new ownership regression fails the original fixture deterministically when
its previously selected address is occupied. Three Go 1.27.1 race repetitions
then verify authenticated TLS, exclusive listener ownership and port release
through initial startup and restart. The complete affected root package passes
on both compilers in both build variants:

| Compiler | Build | Instrumentation | Top-level passes | Subtest passes |
| --- | --- | --- | ---: | ---: |
| Go 1.27.1 | Default | Race | 36 | 38 |
| Go 1.27.1 | `externaljobs` | Race | 41 | 77 |
| Go 1.25.0 | Default | Ordinary | 36 | 38 |
| Go 1.25.0 | `externaljobs` | Ordinary parent, race worker child | 41 | 77 |

Each tagged run includes all 37 separate-process worker scenarios. Seven browser
fixtures skip in each root run and pass in their required campaigns instead;
the tagged root runs also retain two opt-in load skips and one subprocess-helper
skip. All eight required browser scenarios pass without skips in every
compiler/tag profile, including the HTTP-server package case. Their source
inventories are identical and unchanged during execution. Root vet in both
variants, fixture formatting, embedded-asset freshness and whitespace checks
pass.

The [listener ownership record](../review/local-evidence.md#record-1a56fa77db5112730fd7df41)
retains the original failure, terminal process statuses, source captures, raw
logs and browser images. All 1,979 captured inputs differ from the turn's initial
inventory only in the 23 intended fixture files before the documentation update.
The private scratch directory and staged dashboard output are removed; no owned
test processes remain. The peak scratch allocation was 3,788,382,208 bytes within
the 4 GiB limit.

These are Linux amd64 development checks using an explicit SDK workspace. They
do not qualify frozen source, packaged artifacts, downloaded modules, native
platforms or provider accounts. The earlier broad A2A admission timeout did not
recur, but its cause remains undiagnosed. Independent review and the remaining
release gates stay open; candidates remain private.

## API admission and shutdown, 3 October 2026

The focused source review reproduced two API admission failures. A readiness
callback waiting on storage held the admission mutex, preventing shutdown from
reaching its cancellation boundary. Cancellation arriving during readiness also
allowed the commit callback to run. Controlled channel gates reproduce both
failures against the original implementation; they do not simulate provider
effects or establish a native stalled-disk result.

Readiness now runs outside the admission mutex. The API rechecks cancellation
and the stopped state under that mutex before registering a commit. Shutdown
can close admission while readiness waits, and accepted callbacks remain counted
and joined. Cancellation of one request does not close global admission.

Twenty race-enabled repetitions pass for the new regressions and existing
barrier cases. The complete affected HTTP-server package passes in four profiles:

| Compiler | Build | Instrumentation | Top-level passes | Subtest passes |
| --- | --- | --- | ---: | ---: |
| Go 1.27.1 | Default | Race | 126 | 138 |
| Go 1.27.1 | `externaljobs` | Race | 177 | 216 |
| Go 1.25.0 | Default | Ordinary | 126 | 138 |
| Go 1.25.0 | `externaljobs` | Ordinary | 177 | 216 |

Each profile explicitly skips its one browser fixture. The separate browser
campaign above remains development-checkpoint evidence. Three related
application tests, including graceful stop, listener-failure cleanup and
TLS-backed catalog recovery, pass with race detection in both build variants,
with two additional subtests each. Both HTTP-server vet variants pass. All 1,979
captured inputs stayed unchanged after the correction; only the two intended
Go files differ from the initial inventory before documentation changes.

The [admission review record](../review/local-evidence.md#record-f3d45c67c91e0cb488930581)
retains the original failures, terminal command statuses, compiler identities,
exact source captures and cleanup record. At that checkpoint, readiness callbacks
remained uncancellable; that correction isolated their waits from the admission
mutex. The following observation correction addresses the production callbacks.
These are Linux amd64 development checks with an explicit SDK workspace. They do
not diagnose the earlier A2A admission timeout or qualify frozen source,
packaged artifacts, downloaded modules, native services or provider effects.
Independent review and the remaining release gates stay open; candidates remain
private. No publication or endurance run was performed.

## Request authentication and catalog/history deadlines, 3 October 2026

The next focused author review reproduced two cancellation defects: all four
ordinary authentication paths waited past a request deadline while policy
replacement held its mutex, and the history reader waited past its deadline
while a write held the history mutex. Cancellation during authentication could
also reach the admission callback.

Authentication now bounds policy-lock acquisition by the request context and
rechecks cancellation before admission. An already-admitted callback retains
its policy barrier until it returns. Tagged worker authentication checks
cancellation before the committed credential reader. Catalog resource details,
action/incident details and snapshots, retained history captures/pages, and
aggregate observation cursors carry the caller's context through their lock
waits. Initial action/incident index rebuilds observe cancellation.

The v1 history endpoint now shares the bounded scan with v2 history. Its cursor
envelope stays unchanged; a sparse scan can return an empty page with a
continuation. At most 10,000 records are inspected per daily segment, candidate
retention is bounded by page size plus one, and records newer than the frozen
upper bound are not decoded. Tests cover cursor bounds, sparse continuation,
unchanged frozen results, cancellation during scans, and later healthy reads.

All eight affected test profiles pass:

| Compiler | Build | Instrumentation | Authentication top/subtests | HTTP top/subtests | Selected persistence top/subtests | Selected management top/subtests |
| --- | --- | --- | --- | --- | --- | --- |
| Go 1.27.1 | Default | Race | 14 / 44 | 131 / 147 | 151 / 393 | 66 / 188 |
| Go 1.27.1 | `externaljobs` | Race | 23 / 74 | 182 / 225 | 159 / 404 | 70 / 188 |
| Go 1.25.0 | Default | Ordinary | 14 / 44 | 131 / 147 | 151 / 393 | 66 / 188 |
| Go 1.25.0 | `externaljobs` | Ordinary | 23 / 74 | 182 / 225 | 159 / 404 | 70 / 188 |

Authentication and HTTP-server packages run in full; persistence and management
use the recorded selected-test expression. Each HTTP profile explicitly skips
its browser fixture, and each selected management profile skips the validation
worker's parent-only process-helper entry. Both affected-package vet variants
pass. Application and CLI packages compile on both compilers with both build
variants; these four compile checks execute no application tests. The original
failing reproductions and ten repeated post-fix authentication race runs remain
retained. The first Go 1.25 distribution download timed out; its bounded retry
completed against the pinned checksum.

All 1,983 captured source inputs remained unchanged during the final profiles;
this correction changes 26 Go files. The private
[API read review record](../review/local-evidence.md#record-f481a98b682ea4a757490d0f) preserves source hashes, exact changed
source captures, the focused diff, compressed raw output and terminal exits.
It also records nine publisher requirements against current implementation and
tests. Those publisher inputs match the preceding local checkpoint; the
assessment and public publisher remain locked. Aggregate gate qualification,
actual signed registry/download readback, and independent review remain open.

This is unfrozen Linux amd64 development evidence using the explicit SDK
workspace. At that checkpoint, the next context audit covered ordinary operation
and retained collection-result reads, nested validation, mutation preparation,
SLO aggregation and cursor cleanup. The following correction addresses those
request paths. Native filesystem and
bbolt operations are cooperative boundaries; this record establishes no hard
stalled-disk deadline. Browser repetition, frozen module/package execution,
configured providers, native platforms, comparative workloads and final rendered
documentation remain separate release gates. Application, SDK and worker stay
at `v0.1.0-rc.1`; the chart stays at `0.1.0-rc.1`. Candidates remain private. No
publication or endurance run was performed; the canceled 24-hour run remains
outside this goal.

## API readiness and observation cancellation, 3 October 2026

The next focused review reproduced missing work deadlines on API GET observation
requests. Production readiness also discarded request cancellation while waiting
for Store/FSM diagnostic locks. A blocked observation could therefore outlive its
request even after the admission-mutex correction above.

API GET observations and `/metrics` now receive the configured read deadline,
with a ten-second default and any earlier caller deadline preserved. Readiness
passes that context through the controller, catalog and collection owners.
State and runtime-configuration diagnostics use cancellable state-lock waits.
Mutation readiness receives its own observation budget; the change does not
shorten worker POST long-poll requests. Interrupted observations do not change
committed state, poison storage health or stop an otherwise healthy owner.

The complete affected HTTP-server package passes in all four profiles:

| Compiler | Build | Instrumentation | Top-level passes | Subtest passes |
| --- | --- | --- | ---: | ---: |
| Go 1.27.1 | Default | Race | 130 | 147 |
| Go 1.27.1 | `externaljobs` | Race | 181 | 225 |
| Go 1.25.0 | Default | Ordinary | 130 | 147 |
| Go 1.25.0 | `externaljobs` | Ordinary | 181 | 225 |

Each profile explicitly skips its browser fixture. The full controller package
passes with race detection: 19 top-level tests pass and its process-helper test
skips in the parent invocation. Selected collection-owner and persistence tests
pass with race detection in both build variants, and on Go 1.25 in the default
variant. Their process-helper entry also skips in the parent invocation. Eleven
application initialization, cancellation, shutdown and TLS recovery tests, with
19 subtests, pass in each of the four compiler/build profiles. Both release
compiler vet variants pass. All 1,979 captured inputs stayed unchanged during
the final profiles; this correction changes 38 Go files, including the callback
signature migrations in existing fixtures.

The private [readiness observation record](../review/local-evidence.md#record-8ce7b4e4690a8576bcd3088c)
retains source inventories and captures, the original failing deadline contract,
corrected fixture failures, terminal exits and cleanup observations. The usage
file's original text was reconstructed by reversing this context-only edit and
verified against its pre-edit inventory hash. This is author review of unfrozen
Linux amd64 development source using an explicit SDK workspace.

The deadline is cooperative. Native filesystem calls must return before
cancellation is observed. At this checkpoint, authentication-policy locks and
the remaining catalog/history paths still required the focused audit recorded
above. This readiness record does not
establish complete handler time bounds, a native stalled-disk result, independent
sign-off or final artifact qualification. The broader release gates remain open.
Application, SDK and worker candidates use `v0.1.0-rc.1`; the chart uses
`0.1.0-rc.1`. Candidates stay private. No publication or endurance run was
performed, and the canceled 24-hour run remains outside the task.

## Linux package execution observations, 3 October 2026

Package qualification now requires actual installed executable hashes, file
ownership/modes/types and five ordered lifecycle observations. Running phases
also check the actual process image and service UID/GID, distinct process start
identities, usable Raft recovery with a stable node and nondecreasing commit
position, and an empty fixture's observed zero monitor/provider-task counters.
Native architecture is checked before execution. Existing installation paths and
retained package-recovery backups, including dangling links, prevent any package
or service command. Authenticated observation reads reject redirects, ambient
proxies, oversized responses and ambiguous JSON.

All 669 release-tool tests pass without skips; the selected process-deadline and
descendant-termination checks also pass. The earlier 180-second interrupted run
remains explicitly incomplete. The first complete rerun failed its Go-event
fixture because the installed compiler was absent from PATH; selecting that
compiler explicitly fixes the environment. This small fixture uses Go 1.27.0
and does not build a release executable. The backup guard's original failures and
fixture correction are retained alongside the final passing results.

All 1,243 captured source/tool/documentation inputs remained unchanged during the
final test run. The [package lifecycle review record](../review/local-evidence.md#record-699dec7f333ac216963d9373)
retains the original outputs, per-test timing, exact inputs and author-review
scope. These local contracts do not qualify actual native installation. Complete
source review, version selection, candidate freeze, independent review and the
remaining release gates stay open. No publication or endurance run was performed.

## Linux package hooks, 3 October 2026

Author review reproduced stale upgrade markers restarting a stopped service,
failed service queries permitting replacement, and RPM resumption after failed
configuration. Hooks now clear previous intent, require a known owner state and
successful stop, and authorize RPM resumption only after configuration completes.
Existing token/configuration modes are repaired without changing operator content.
Linked files are rejected before token or installation-marker writes.

The 199 selected release, package, service and evidence-reader tests pass. Actual
pinned nFPM 2.47.0 produces DEB/RPM metadata with the expected dependencies and
embedded hooks. Its unsupported `requires.pre` field was detected and removed;
Debian retains its supported `Pre-Depends: passwd`. These packages deliberately
contain small text fixtures, so they do not qualify CPRa executables or native
installation. Original failures, the correction and captured inputs remain in the
[package-hook review record](../review/local-evidence.md#record-3a033bcef13ce520a1067859).
The candidate is not frozen or independently reviewed; final package execution
and the other release gates remain open. No public publication or endurance run
was performed.

## Linux installation permissions, 3 October 2026

Author review reproduced user initialization retaining `0777` configuration/state
directories and `0666` starter/token/state files, and accepting linked or special
state entries. Linux initialization and stopped installation now repair managed
modes, reject linked managed paths and non-regular state, and preserve existing
contents and provider-helper permissions. System executable directories/binaries
receive explicit administrator-owned `0755` modes and units receive `0644`.
Ordinary application startup does not perform this recursive permission repair.

The local administration, installation-path and runtime-configuration packages
pass on Go 1.27.1 with race detection and on Go 1.25.0 without instrumentation,
in both default and `externaljobs` builds. Each compiler passes 46 top-level tests
in the default selection and 59 with the tag; subprocess-helper skips are recorded
separately. Go 1.27.1 vet passes both selections. All 463 captured Go/module inputs
remain unchanged through these checks.

Native-service observations now retain UID, GID, mode and file type for executable
parents, configuration, token, state and supervisor definitions after startup,
running upgrade and restore. The system fixture checks actual service-account
access. The 49 native/packaging contract tests and 118 related publication/review
reader tests pass. These filesystem and synthetic-report contracts do not establish
actual system-service or other-platform execution.

The actual canonical SDK archive matches the sums recorded by the application and
worker modules; all 130 captured SDK/worker inputs remain unchanged. This checks
working-source identity, not public downloaded-module publication. The
[permission review record](../review/local-evidence.md#record-8466c276fd36c2218418a60f)
retains original failing and passing output, input hashes, test-source boundaries
and the SDK archive. Complete source review, version selection, candidate freeze,
independent review and final native/release qualification remain unfinished.

## Runtime source review, 3 October 2026

The startup/shutdown, committed agent action, original-connection recovery,
worker admission and command-format paths received a focused author review.
Selected persistence, controller and HTTP-server cases pass with Go 1.27.1,
`externaljobs` and race detection, including real process-kill recovery. Vet
passes for those three packages. All 1,109 captured Go/module/tool inputs remain
unchanged during verification. The [review record](../review/local-evidence.md#record-dae0157a2b2f0138b7315d03)
retains the original output, exact test inventory and subprocess-helper boundaries.

The scale ticket still requested the canceled 24-hour run. Its current scope and
acceptance criteria now require only the agreed comparisons and fault scenarios.
All 22 documentation-tool tests pass. No new runtime defect was reproduced in
the selected cases. The complete source review, version selection, candidate
commit, independent review and final release qualification remain unfinished.

## Original release-asset signing recovery, 3 October 2026

The locked publisher now retains the selected trust roots and checksum manifest
before requesting asset provenance. Its source-bound, exclusively locked journal
commits intent before provenance generation and checksum signing. Original
provenance is preserved independently before checksum signing, which first
cryptographically verifies that provenance. Continuation verifies and reuses
original outputs; an interrupted generation without recoverable output is held.
Complete cache capture and later promotion require completed original generation
progress. Imported signing files also remain bounded if they grow after inspection.

All 631 release-tool tests pass, and the 80 captured code/workflow inputs remain
unchanged during verification. Pinned Cosign 3.1.3 performs actual local signing
and verification. Separate processes killed before checksum output remain held;
those killed after output resume by verifying the original provenance and signature
without signing again. Partial provenance and complete signing caches survive
workflow import/restoration. The existing actual ORAS/Distribution image/chart
and original-image-signing scenarios also pass, as does pinned workflow lint.
The [asset-signing checkpoint](../review/local-evidence.md#record-7646bb8a7c1af53df8a010fe)
retains original outputs, public fixture key, input identities and logs.

Author review reproduced the prior unbounded signing-cache copy. Its original
failure and the corrected regression are retained. Initial fixture permissions
and test selection were corrected. A disk watcher racing with normal temporary
file deletion interrupted the first full run; that partial log remains retained,
and the corrected bounded watcher completed the rerun.

These are ephemeral local keys and synthetic release/image/chart bytes, not
GitHub signing identity, transparency inclusion, production trust roots or final
CPRa artifacts. Actual protected-workflow execution, source freeze, independent
review and the other native, provider, worker, interface and comparative
performance gates remain open. Publication remains locked. No 24-hour run occurs.

## Original image signing and referrer recovery, 3 October 2026

The locked publisher now commits image-signing intent, preserves and seals the
original three signature bundles and multiarchitecture provenance, and transfers
their exact OCI envelopes. Continuation verifies original bytes and discovery;
missing completed objects, unrelated fallback indexes, altered source identities
and unknown generations without their original output are held. Independent
release, module, image-version and chart phases recheck this completed image
progress and its cryptographic verification without uploading anything.

The protected workflow separates provenance generation from registry transfer
and retains original public signing material with its journals. Restoration
accepts artifact extraction's directory modes but recreates private active
storage, rejecting files added or grown after inspection. Restoration also bounds
each copy and verifies its inspected digest before staging it. CI now explicitly installs
pinned Cosign and executes the signing-recovery scenario; it cannot silently
omit the optional invocation. Fixture key generation also verifies the executable
before starting it.

All 619 release-tool tests pass. The 79 captured code/workflow inputs remain
unchanged during the final run and actual integration. Pinned Cosign 3.1.3,
ORAS 1.3.4 and Distribution 3.0.0 pass real local process-crash and continuation
checks after signature output, bundle acceptance, referrer-manifest acceptance
and fallback-index acceptance. Original bundles are reused; all three signatures
and provenance verify, and final promotion performs zero uploads. Actual Helm
3.22.0 and 4.0.4 also retrieve the original chart by version and digest. Pinned
workflow lint passes. Author review reproduced an unrestricted copy of a growing
bundle; the corrected bounded-copy test and its original failure are retained. The
[image-signing recovery checkpoint](../review/local-evidence.md#record-e49e00e43acd8cf46889e5c0)
retains original outputs and input/tool identities.

These are ephemeral local keys and synthetic image/chart bytes. Native Referrers
API behavior has bounded HTTP contracts; the actual Distribution fixture uses
the specified fallback. The results do not qualify GitHub signing identity,
transparency-log inclusion, real-account publication or runnable CPRa images.
Full signing-workflow execution, source freeze, independent review and the
remaining native, provider, worker, interface and comparative performance gates
remain open. No public publication or 24-hour run occurs.

## Chart registry recovery, 2 October 2026

The locked publisher now preserves the tested chart archive inside a deterministic
Helm OCI envelope. Configuration/archive blobs precede the manifest and explicit
chart version. Its source-bound, exclusively locked journal commits intent before
upload, rechecks every remote object and refuses unrelated versions, conflicting
bytes or replacement of missing completed objects. Preflight also rechecks
previously verified dependencies before another release stage can continue.
Chart versions are validated separately from application image versions.

The protected workflow uses the chart helper after signed release readback and
original image-version verification. It retains and restores the original chart
journal; its one-shot Helm push is removed. The chart command remains subject to
the same unconditional publication lock. The shared transport keeps
credentials out of retained progress and limits work, output and request time.

All 592 release-tool tests pass, with 77 captured code/workflow inputs unchanged
during the final run. The explicit pinned ORAS 1.3.4/Distribution 3.0.0 suite
kills separate processes after chart configuration and version acceptance.
Continuation reuses the original graph without additional uploads. Both actual
Helm 3.22.0 and Helm 4.0.4 pull matching archive bytes by version and by digest.
The first consumer check assumed the version filename for a digest pull; retained
diagnosis proves the bytes already matched. That test assumption is corrected.
During author review, a new regression case exposed the need to recheck completed
chart dependencies during preflight; the final suite includes that correction.
Documentation tests and pinned workflow validation pass.

The [chart publication checkpoint](../review/local-evidence.md#record-da4ec043ae8d0f7e6e995c59)
retains original outputs and input/tool identities. These are small local chart
and registry fixtures, not real GHCR/Sigstore publication, runnable CPRa images
or Kubernetes deployment qualification. Image-attestation/referrer recovery,
source freeze, independent review and the remaining candidate, native, provider,
worker and comparative performance gates stay open. No public publication or
24-hour run occurs.

## Release recovery and original signing material, 2 October 2026

The locked publisher now uses its bounded release transport for draft creation,
asset upload/readback, immutable publication and temporary draft cleanup. Durable
intent precedes each mutation. Receipts retain the original remote IDs, so a lost
reply or forced process termination resumes through identity and byte verification
instead of replacement. Incomplete uploads, changed inputs, missing completed
objects and unrelated releases are held. Final verification precedes cleanup;
no Git reference is deleted.

The private progress artifact retains original checksum/provenance bundles and
the public trusted-root snapshot with a source/asset ledger. Its 64 MiB limit,
strict file inventory and corruption checks apply before import or restoration.
The workflow restores those bytes before continuing, avoids regenerating sealed
bundles and verifies image signatures again. Read-only signed asset verification
before image promotion uses bounded downloads instead of an unrestricted release
download. Public CLI/workflow locks remain enforced.

All 563 release-tool tests pass, including 35 new HTTP, storage and process-crash
contracts plus executed fresh/resumed workflow command selection. The 76 captured
code/workflow inputs stay unchanged during the final run. The first implementation
attempt exposed a stale inventory function reference. A later full run failed on
the workflow test's executable-name assertion; the revised test executes the
actual workflow loop with a recorded command boundary. A failing reproduction
and the later full failed/passing logs are retained in the
[publisher recovery checkpoint](../review/local-evidence.md#record-1d997068bfa041b260e9a2b5).
Documentation tests, canonical checks and pinned workflow validation also pass.

Bundle contents and API identities in these tests are fixtures. They do not prove
real GitHub/GHCR execution, cryptographic signing, a frozen CPRa candidate or
independent approval. Image-referrer/attestation and chart recovery, final source
freeze and the remaining native, provider, worker, interface and comparative
performance gates remain open. No public publication or 24-hour run occurs.

## Publisher workflow and exact image versions, 2 October 2026

The protected publisher job now uses the original private OCI layout, manifests,
index, image inventories and chart archive. Its Docker import/push/index rebuild
path is removed. It supplies the recorded compiler, qualified dependency payloads,
reviewed ORAS and private progress/work directories to the image and module
publisher stages. Private assessment remains a prerequisite, and every mutating
CLI phase and the publication workflow remain locked.

An explicit application image version preserves the index bytes. The helper
requires original progress for an existing tag, rechecks remote dependencies,
commits intent before writing and resumes uncertain outcomes through readback.
Missing completed tags or conflicting bytes cannot be overwritten. The protected
publisher must be the only writer because the registry does not provide atomic
conditional tag creation. Before image-version promotion, the CLI verifies the
original application tag, immutable published release and downloaded signed assets.

The workflow can restore OCI/module journals from a selected completed run of the
same source and manually dispatched main workflow. Authenticated metadata binds
repository, workflow, commit and run attempt. Import rejects unexpected files,
links, oversized records, changed release identities and existing destinations;
remote bytes remain authoritative. Only source-bound journals enter the retained
progress artifact. Registry credentials and working caches are cleaned up.

All 527 release-tool tests pass, including 22 new publisher cases covering exact
index tags, lost replies, source/configuration conflicts, progress restoration,
workflow input selection and all mutating CLI locks. The first full run failed
because its native Go event check could not find the installed compiler; the
recorded rerun sets the compiler path explicitly. The workflow checker also
caught an invalid context for credential-path settings, which is corrected.
The actual pinned ORAS/Distribution suite additionally kills a separate process
after the local registry accepts the original index tag; continuation verifies and
reuses it, then repeats readback. These are small local OCI fixtures, not runnable
CPRa image, GHCR, account, signing or frozen candidate evidence. The
[publisher workflow checkpoint](../review/local-evidence.md#record-aa9302fbc3ddab7ef04bf558)
retains the original failed/passing outputs and captured tooling identities.

Overall draft/asset/signing/chart continuation, the complete frozen candidate,
independent review and the remaining native/provider/performance gates are still
open. The 18-of-35 provider checkpoint is historical: earlier raw provider/service
reports were removed during disk cleanup, so final qualification needs fresh
retained reports. No public publication or 24-hour test is performed by this work.

## Ordered module publication, 2 October 2026

The publisher now calls a create-only, source-bound module sequence after its
private signed readback: SDK tag, public SDK download, worker tag, public worker
download, then application tag. All proposed names are checked before the first
creation. Private synchronous progress records preserve the same source, module
payload graph and versions. Lost creation replies and process termination resume
by checking the original reference; completed markers never replace remote
verification. Changed inputs or tags stop continuation. Unrelated existing tags
and missing verified tags cannot be overwritten.

Every download uses a new private cache, the recorded compiler, the public HTTPS
proxy and ordinary public checksum verification. Provider/GitHub credentials,
workspace settings, replacements, credential helpers and private checksum
exceptions are excluded. Public Go hashes, module declarations, source time and
all distributed file contents must match the qualified source. Processes have
bounded output, storage and deadlines and remove their own caches.

All 505 release-tool tests pass, including 24 new module-publication contracts.
The local sequence tests include a lost HTTP reply and an actual process kill
after a native Git tag is created, then continuation and repeated readback.
Their API and module payloads remain fixtures; they are not GitHub-account or
public CPRa-module qualification. A separate real Go 1.27.0 public download of
`github.com/google/uuid@v1.6.0` passes without checksum exceptions and removes its
cache. This tests transport policy, not the official Go 1.27.1 application build.
The [module-publication checkpoint](../review/local-evidence.md#record-a0d95b25d5a12be006de2ab0)
retains original check outputs and the unchanged source identities.

The CLI and workflow locks remain enforced. Release assets, chart publication, mutable image aliases,
signatures, overall publication continuation, actual CPRa public downloads, the
complete frozen candidate and independent review remain open. No public CPRa
tag, release or registry object is created by these tests. The 24-hour test
remains outside the task.

## OCI transfer and restart, 2 October 2026

The publisher now has an exact-digest transfer helper and private, synchronously
flushed progress records. It revalidates the retained assembly, uploads original
configs/layers followed by platform manifests and the unchanged index, and hashes
every remote object during readback. Continuation always checks remote bytes;
completed markers cannot authorize replacement of missing or changed objects.
Source/graph changes, corrupt records, competing owners, invalid responses and
authentication/network uncertainty stop the operation. ORAS execution is pinned,
environment-isolated, bounded and cleaned up after each operation.

Real ORAS 1.3.4 and Distribution 3.0.0 pass an explicit local integration: a
separate process is killed after the first remote upload is accepted, restart
reuses that object and completes the seven-object fixture graph, and a second
restart verifies all objects without another upload. No tags are created. The
initial failed attempts exposed an unsupported manifest flag and the registry's
empty-tag listing behavior; those fixture/protocol handling issues are corrected.
CI now invokes the actual local suite explicitly alongside the release checks.
All 481 release-tool tests pass; the 73 captured source/configuration inputs
remain unchanged during those tests and the final local-registry run. These
checks use the installed Go 1.27.0 for tiny compiler fixtures, not the official
application compiler or a runnable release image.
The [retained transfer checkpoint](../review/local-evidence.md#record-33230bae27106d7a9e6084dd)
records the original failed/passing attempts and source identities.

This is local transfer-tool verification with small OCI byte fixtures. It does
not qualify runnable CPRa images, a frozen release, GHCR credentials, signatures
or public downloads. Complete protected-workflow integration, ordered module
publication and final release resume/readback remain open. The CLI/workflow
locks remain enforced. No 24-hour run is required or started.

## Private image inventories, 2 October 2026

Image SBOM generation now reads the privately assembled OCI layout before any
publication, rather than mutable Docker tags. Each platform is selected explicitly.
The retained native Syft and SPDX documents must identify the same packages and
the exact manifest/configuration bytes; the configuration identifies architecture
even when the scanner's summary leaves it blank. Dashboard dependencies and the
scanner executable are checked against frozen source and reviewed checksums.

The new producer bounds output/cache bytes, document size and scanner lifetime,
isolates the child environment and removes incomplete output. Verification also
works after download/relocation. The private workflow retains original scan
documents separately from distribution assets. Assessment links the final SPDX
files to those original bytes and keeps packaging incomplete when they are absent.
Publication remains locked and the changed workflow has not executed.

Real Syft 1.51.1 has scanned tiny, distinct amd64/arm64 OCI fixtures from two
independent directories. Each scan finds the intended architecture's package and
the normalized final documents match across directories. A second fixture adds
real Go binaries built with the installed Go 1.27.0 compiler; both formats agree
on the Go module, standard library and npm package identities in both architectures.
All 454 release-tool tests and the pinned workflow checker pass. The
[private tooling checkpoint](../review/local-evidence.md#record-0a846fbebcc0274bfd69b211)
retains the original scanner outputs and failed/passing verification attempts.
These are tooling
observations using synthetic source/image identities, not runnable CPRa image or
frozen candidate qualification. Full image execution, authenticated independent
review, exact-blob publication, final readback and the other release gates remain
open. No 24-hour run is required or started.

## Private OCI and chart assembly, 2 October 2026

The offline image assembler now verifies both retained reproduction attempts and
their saved runtime reference for each Linux architecture. It copies the exact
manifest, configuration and layer bytes, deduplicates shared blobs and calculates
a deterministic two-platform index before any registry operation. Output is
exclusive and subject to an explicit byte quota. Changed inputs, corrupt blobs,
links and incomplete copies fail without leaving a successful assembly record.
The retained record supports verification after artifact download and relocation.

Chart packaging checks every source file against the frozen inventory, binds the
version and image index, bounds compressed/expanded archives and preserves an
existing output. Real checksum-pinned Helm 3.22.0 and 4.0.4 have linted and packaged
the current chart sources using a synthetic release identity; their normalized
package bytes match. These checks do not execute a Kubernetes deployment or
qualify a frozen candidate, storage driver, rollout or upgrade.

The [tooling checkpoint](../review/local-evidence.md#record-df0c426ad129a356f9a6ef56)
records all 433 release-tool tests, 22 documentation tests, workflow validation
and canonical checks of 157 pages passing. The real Helm tools also produce
matching manifests for Kubernetes 1.32.0, 1.35.8 and 1.36.0 and reject the declared
range's excluded 1.31.0 and 1.37.0 versions. Original failed fixture expectations
and the initial offline render using Helm's unsupported default are retained.
All 104 captured inputs stay unchanged during verification. These records use
synthetic image/release inputs alongside the real tools and a small Go 1.27.0
event-format fixture; they do not qualify official application binaries.

The private workflow now assembles the image layout and chart before assessment.
Its assessment reader rechecks every assembled byte and original transport,
rejects subjects that differ from the distribution and leaves packaging
incomplete when final assembly is missing. This workflow has not run. Final image
SBOMs, authenticated review, the publisher's exact-blob upload/module/resume
implementation and the other release execution gates remain open. Publication
is locked; no public objects or endurance run are created.

## Review and publication-procedure readers, 2 October 2026

All fourteen private assessment components now have retained-input readers.
The publication reader binds the source-owned procedure, complete unsigned
artifact inventory, SDK/worker module payloads and nested tags, dependency order,
OCI subjects, chart image digest and signing/resume policy. It compares module
versions with executed consumer evidence. Its collector derives values from
original bytes and writes a new private plan; it cannot publish or overwrite
inputs. Subsequent assembly tooling adds final OCI/chart bytes privately;
complete SBOM/readback qualification and the publisher's nested
module/interruption implementation remain unfinished.

The independent-review reader verifies real OpenSSH signatures over the exact
original assessment. It rechecks all retained inputs, compares current component
outcomes, requires complete scope and rejects self-review, duplicate identities
or keys, unresolved findings, untrusted policy and revoked reviewer eligibility.
Policy is supplied outside candidate source and review artifacts. Human key
ownership, the author list and an adequate independent review remain external
requirements. Test identities and synthetic assessments are not genuine approval.

The [review and publication procedure](../release-engineering.md#publication-procedure-and-independent-review)
documents the boundaries and configuration. Both new readers preserve
`release_gate_passed: false`; missing execution evidence stays incomplete even
with valid signatures. The workflow and publisher remain locked. This is reader
implementation, not complete candidate qualification or publication. Original
tool outputs are recorded in
[the tooling checkpoint](../review/local-evidence.md#record-e3a3428fe884c5ea34a804dc).
The other release workstreams remain open. No endurance run is required or started.

## Storage-upgrade qualification tooling, 2 October 2026

The release recipe now records common storage format 38, matching the current
default reader/writer; the optional worker reader reaches 39. A regression
rejects the previous recipe even when its manifest also claims format 35.
The native fixture emits schema-2 initial-state observations and retains only
bounded process logs. A source/artifact-bound reader compares all four tested
executables with their frozen native archives and checks log/snapshot recovery,
incident ownership, immutable events, held unknown actions and independently
counted effects. The private assessment now has twelve component readers;
authenticated independent review and the publication-plan reader remain missing.

The private workflow accepts a previous run ID for Linux amd64/arm64 upgrade
checks. It validates the completed private dispatch, matching source and
successful artifact build, then checks the stages before invoking either owner.
An omitted run, unsupported platform, same-format upgrade or optional worker
state remains unqualified. Publication stays locked, and no endurance run starts.

All 355 release-tool regressions pass. Their synthetic upgrade records test
reader boundaries and are not native execution evidence. The native Go
event-format regression uses the installed Go 1.27.0; this is not a new
Go 1.27.1 release build. The earlier failed tool run is retained and identifies
the missing Go path. The [tooling record](../review/local-evidence.md#record-cd162a609902ffa3284a50af)
retains the original outputs and source fingerprints. A frozen two-version
campaign, remaining native platforms, same-format/worker upgrade scenarios and
independent review remain open alongside the other release workstreams.

## Notification delivery and native upgrades, 2 October 2026

Repeated failed checks could replace a succeeded notification while another
endpoint or recovery action remained unfinished. The controller now queues that
notification only on entry into the recovering state. A later incident can
notify again. The change uses the recorded command envelope: new built-in
observations write format 38 and optional external-worker observations write
format 39. Earlier envelopes keep their original replay semantics and action
identities. The command structure has not gained another optional field.

The [delivery checkpoint](../review/local-evidence.md#record-628954891cf15c7d379073e9)
records Go 1.25 external-worker checks and Go 1.27.1 race checks for both build
variants, including real process-kill recovery. The focused runs pass 15 top-level
tests and 30 subtests on Go 1.25; the two race variants pass 32 top-level tests
and 86 subtests combined, without skips. The prior Go 1.25 default run passes
12 top-level tests and 10 subtests. The current 370 persistence source files
match those used by the preceding full default run and affected worker run.
Their original logs record 710/1,763 and 237/541 passing top-level/subtests,
respectively. Two and four subprocess entrypoints skip in their parent test
process; the corresponding process scenarios execute separately. Original
failures and interrupted observations remain retained.

Persistence, local administration and the controller pass vet in both build
variants. All 22 documentation-tool tests pass. Canonical checks cover 157 pages,
35 driver roles and 144 configuration fields without errors. A disposable copy
of the documentation site passes the strict build and checks 174 HTML pages,
157 content pages, 16 redirects and 44,889 internal links/assets. The original
documentation checkout is preserved; this is a working-source documentation
check, not publication of the final reviewed candidate.

The [two-version Linux checkpoint](../review/local-evidence.md#record-99e95fdbe8223ce1201e203e)
uses separately compiled default-driver development binaries from published-main
merge `b293f34dd57ee61c78371643de6d0ebaa75c626e` and the modified candidate,
with the same Go 1.27.1 compiler. Both log-only and snapshot-plus-log cases pass
forced termination, upgrade against the same state, older-reader rejection and
restoration of the previous stopped backup. Node, monitor, incident and action
identities, acknowledgement ownership and retained history survive. Seventeen
failed checks across the original, upgraded and restored owners leave exactly
one independently counted recovery request and one request to each notification
endpoint. The succeeded endpoint remains succeeded; interrupted actions stay
unknown. Authentication is reprovisioned after restore and the old token is
rejected. The previous CLI restores its own backup successfully and rejects the
candidate's newer inventory.

These are working-source results for Linux amd64, not a frozen release, another
native platform, a service/package upgrade or provider-account certification.
Temporary compilers and build/module caches are bounded and removed after the
run. The [native fixture instructions](../native-installation.md#verification-boundary)
describe the separate service and two-version evidence boundaries. Complete
candidate qualification, other native environments, configured providers,
matched performance comparisons and independent review remain open. The
24-hour test remains outside the task.

## Cleanup and storage-format work, 2 October 2026

The user requested removal of accumulated caches, binaries, private release
candidates and local verification artifacts. Those generated files were removed;
source, Git history, the separate documentation checkout and persistent service
volumes were preserved. Earlier entries record historical results. Their statements
about retained raw artifacts no longer establish current availability. Fresh
candidate evidence is required before release qualification. The reference index
records missing artifacts explicitly.

Stopped backups now record an authenticated `state_format` from validated
committed snapshots and Raft replay, separately from the base `storage_format`.
Restore checks the declared format against the backed-up state before publishing
its destination. Authenticated format-2 backups without the new field still
receive complete offline validation. Removing the field from a new backup fails
authentication. This is an implementation change; it does not establish
compatibility between released binary versions or close the native release gate.

The [2 October storage checkpoint](../review/local-evidence.md#record-329a61ccc44781c505025b29)
records 14 top-level tests and 58 subtests passing without skips in each of four
variants: Go 1.25 default/external-worker builds and Go 1.27.1 race-enabled
default/external-worker builds. These include real Raft logs, snapshots and later
commits, authentication/inventory tampering, state-format disagreement, prior
authenticated inventory shapes, process-kill restore recovery, incident/history
retention and unknown-action preservation. Both affected packages pass vet in
default and external-worker builds. All use the explicit development workspace;
none qualifies the complete frozen release or another native platform. The
three reproduced missing-metadata failures and two earlier fixture mistakes remain
in the small original logs. Temporary compilers, module/build caches and state
fixtures are removed after verification; only bounded test records remain.

## Dashboard accessibility and browser release verification, 1 October 2026

Confirmation, unsaved-draft, incident-control and action-review dialogs now use
the browser's native modal layer. Keyboard focus stays within the dialog, returns
to the initiating control or main content, and remains available while a pending
write disables its buttons. Escape keeps pending writes visible. Conflict and
unconfirmed-outcome messages appear inside the open dialog. The dashboard also
has a skip link, route-specific document titles and valid group labels.

The private checkpoint in `bin/verification/dashboard-accessibility-2026-10-01/result.json`
records 513 passing frontend tests. A bounded frontend HTTP fixture
passes 15 scans and its keyboard, pending-write and mobile checks. All eight
required real-server browser scenarios also pass on Go 1.27.1 without race
instrumentation, using the explicit development workspace. Its 32 scans have no
confirmed violations; contrast findings still require manual review. Captured
inputs remain unchanged during these executions. These results verify the
captured working source, not the complete frozen release or every compiler/tag
combination. Earlier failed builds and browser attempts are preserved.

The private release workflow now has four browser jobs that consume extracted
release source and verified private dependencies with `GOWORK=off`. The recorder
checks complete source contents before and after execution without requiring
Git inside that extraction. It retains original scans and verifies the executed
axe-core bytes against the locked npm archive. The installed scanner passes that
archive comparison locally. This new frozen workflow has not been executed.

All 336 release-tool regressions and 29 frontend-tool tests pass, including the
native Go event-format case. The first full tooling run failed because its test
environment omitted Go from `PATH`; that report remains retained. Focused
orchestration fixture failures and the correction to the qualification-test
insertion remain in the original tool output. Workflow, embedded-asset and
JavaScript syntax checks pass. These tool regressions use synthetic records
alongside the explicitly identified native event-format and scanner checks;
they do not establish frozen-release execution.

The eleventh assessment reader combines those frozen browser records with
source-bound manual review notes. Missing manual cases, unresolved findings,
changed inputs and incomplete compiler/tag coverage stay incomplete or invalid;
automatic scans cannot satisfy manual review or establish full WCAG conformance.
Storage-upgrade, independent-review and publication-procedure readers remain
unimplemented. Other release requirements and publication remain open. No
24-hour test is required or performed.

## A2A release evidence, 1 October 2026

The private release workflow now records a separately compiled upstream A2A
server and the complete jobs-package interoperability execution on both
compilers. The tenth retained-input reader binds the original reference sources,
verified module graph and checksums, executable, build information and 21 upstream
cases to the frozen release. It separately requires 114 lifecycle cases across
all four application variants, including explicit crash boundaries, original
connection recovery and confirmed-failure escalation.

The checkpoint in `bin/verification/a2a-evidence-2026-10-01/result.json`
records 314 passing release-tool regressions, workflow validation and formatting
checks. These use synthetic evidence fixtures plus a small native Go event-format
test. Re-reading four original restart/crash logs confirms all twelve selected
cases in each; changed surrounding source prevents treating those earlier runs
as qualification of the current candidate. No application campaign was restarted.

The assessment consumes these build records and optional frozen provider
reports. Missing designated-agent recovery or notification effects stay
unverified. Local protocol results cannot satisfy that requirement or certify the
other providers. Four component readers remain unimplemented: accessibility,
storage upgrades, independent review and the publication procedure. Complete
frozen execution and the other release requirements remain open. The 24-hour
test is removed from the work; no such test is required or performed.

## Management evidence, 1 October 2026

The private checkpoint in `bin/verification/management-evidence-2026-10-01/result.json`
records the ninth retained-input reader. It uses the complete frozen build records
and separately retained canonical API inputs. Server and SDK inventories, model
references, request media types, version preconditions and both generated schema
projections must agree. Every selected SDK operation requires a passing inventory
subtest, alongside 117 named contracts across server/controller, SDK/collections,
CLI and optional JobType coverage. The current contract has 71 base operations
and 11 optional external-worker operations.

All 298 release-tool regressions, workflow checks and formatting checks pass
with unchanged verification inputs. These include explicitly synthetic reader
fixtures and one small native Go event-format test, not a frozen application
campaign. Re-reading the original CLI logs confirms five completed scenarios and
their actual helper compiler/tags in each of three earlier successful runs.
The interrupted fourth run remains incomplete; its original failed outer report
and unfinished log are preserved. No workload was restarted to replace it.

The private assessment and workflow staging now consume this management reader.
Its consistency result cannot authorize publication. Full frozen execution,
independent review and the remaining native/provider/performance requirements
stay open. Five component readers remain to be implemented: A2A, accessibility,
storage upgrades, independent review and the publication procedure. No 24-hour
test is required or performed.

## Security evidence and downloaded SDK source mapping, 1 October 2026

The private checkpoint in `bin/verification/security-evidence-2026-10-01/result.json`
records the eighth retained-input reader. Its security assessment consumes the
complete frozen build records, explicit authentication/transport/encryption
contracts, original symbol-exclusion declarations and diagnostics, and pinned
vulnerability-scan messages. It cannot authorize publication. Missing checks,
changed inputs, narrowed commands, skipped required tests and reachable findings
remain incomplete or invalid; required/imported findings remain visible for review.

The build recorder's explicit security mode now records symbol exclusion on both
compilers and all seven vulnerability module/tag combinations on the production
compiler. It retains scanner bytes, build identity, database timestamps and
package/dependency inventories. The private release workflow requires this mode.
This wiring has passed local workflow checks; it has not been executed as the
full frozen release workflow or an independently reviewed security campaign.

The exclusion verifier previously assumed downloaded SDK files were inside the
application checkout. It now maps actual SDK-cache files to their canonical source
identities and rejects foreign modules, aliases and unframed filenames. Actual Go
package queries pass on both compilers with base/external tags and `GOWORK=off`:
each base selection has 18 SDK files and each external selection has 20. All match
the captured and current SDK source. This proves downloaded source selection, not
public module availability, compiled-symbol exclusion or provider effects.

All 281 release-tool tests and workflow/whitespace checks pass with unchanged
verification inputs. Reader/recorder regressions use explicitly synthetic data;
they do not establish release qualification. Earlier fixture-construction failures
remain recorded in the conversation, and the previous passing tool records are
preserved separately. Six component readers, full frozen execution, independent
review and the remaining native/provider/performance/publication gates remain
open. No 24-hour test or publication was performed.

## Build-matrix implementation and interrupted CLI verification, 1 October 2026

The private checkpoint in `bin/verification/build-matrix-2026-10-01/result.json`
records a seventh retained-input reader and a frozen-source build recorder.
The compatibility workflow now covers all seven application/SDK/worker module
and tag combinations, including built-in drivers with external workers. Both
compilers run race tests and vet; the release compiler also requires two-root SDK
generation and pinned dashboard types, lint, unit tests and asset reproduction.
Raw outputs, dependency archives/graphs, matching metadata and source identities
remain separate from the assessment. Missing coverage keeps publication locked.

All 264 release-tool regressions, workflow checks and formatting checks pass.
The new reader includes a real Go event-format regression and explicit rejection
of narrowed commands, missing modules, skipped coverage and contradictory results.
These checks validate the tooling, not execution of the full frozen workflow.

The CLI fixture now locates and verifies the selected compiler when `-trimpath`
omits its embedded installation directory, and records its actual build tags.
All five real CLI scenarios pass in Go 1.25 default and combined builds and Go
1.27 with race instrumentation in the default build. The combined Go 1.27 race
run started but hit the physical host disk reserve and was terminated. Its raw
output remains retained and the overall execution is failed/incomplete; no fourth
passing build is claimed. Captured source remains unchanged. The private source
copy's earlier import-setup failure is also retained.

The lowest measured host space was 4,155,400,192 bytes, below the fixture's 4 GiB
reserve. This is an observed interrupted-run limit, not a current free-space
reading. Complete frozen execution, independent review and the remaining release
workstreams stay open. No publication or 24-hour test was performed.

## Captured worker process verification, 1 October 2026

The [private execution checkpoint](../review/local-evidence.md#record-eacc8cbc59fe1cb1c2d7637a)
records all 37 crash/capacity cases and five actual minutes of mixed worker load
on Go 1.25.0 and Go 1.27.1 with race instrumentation. Both use `GOWORK=off`,
readonly module resolution, verified cached dependencies and content-matched SDK
archives. Captured runtime, test and module files match across the two runs;
only the Python reader and its regression tests changed between captures.

| Compiler | Checks | Recovery actions | Notifications | Duplicate invocations | Unreconciled outcomes |
| --- | ---: | ---: | ---: | ---: | ---: |
| Go 1.25.0 | 1,437 | 359 | 392 | 0 | 0 |
| Go 1.27.1, race | 1,397 | 349 | 380 | 0 | 0 |

Both journals drain to zero records, active handlers, pending outcomes and unknown
actions. Each run reaches 16 simultaneous handlers and confirms exact expiry
responses for previously accepted checks retired by retention. These are local
process fixtures, not a comparative fleet benchmark or provider-account proof.

The runs exposed and corrected compiler discovery in trimmed test builds, a
missing audit entry for explicit check expiry, and a reader that assumed long
Go output occupied one JSON event. Expiry remains distinct from a successful
result; actions still require actual receipts. The original Go 1.25 recorder
reports a decoding failure even though its complete test process passed. That
report remains intact alongside validation by the corrected reader. Earlier
failed executions and fixture assertions are also retained.

The focused expiry scenarios pass on both compilers, including race detection;
246 release-tool tests, 22 documentation-tool tests and formatting checks pass.
Reviewed clean source, the final version graph, independent review and the
remaining release workstreams are still open. No publication or 24-hour test
was performed.

## Private documentation build, 1 October 2026

The strict site failure was corrected without omitting implementation pages or
reducing link validation. Canonical navigation now covers all 157 Markdown pages.
Referenced repository files have 67 exact, content-identified text downloads.
Private verification links point to an index that records their locations and
availability at preparation; it publishes no artifact contents and verifies no
test outcomes. Missing records remain explicit.

The synchronization tool also aligns source metadata and the announcement block.
It distinguishes historical candidate guides, unreleased SDK working source,
implementation records and guides with individually stated revisions. The
current parent commit identifies the base of the uncommitted candidate, not all
its later content. The earlier 13 September review is labeled historical.

The [private documentation checkpoint](../review/local-evidence.md#record-dbc7f494a563a2b0f094b54d)
records the strict render, complete link/asset checks, independent build roots,
source-copy comparison, unchanged gh-pages sources and documentation-tool tests.
The earlier failed renders remain preserved. This closes the private rendering
failure; frozen release alignment, browser review and publication remain open.
All nine release workstreams still require their respective acceptance evidence.
No 24-hour test is required or performed.

> **Candidate design and evidence:** this record describes unreleased implementation work or an approved plan. Its checklists do not establish availability in `main`. [Version and availability](../versions.md) identifies the earlier 13 September reviewed snapshots; the current finalization record below preserves the later branch checkpoints.

# CPRa implementation status

## Release checkpoint: 2026-09-30

The finalization source through `fd2169c1a13a5e0c25f5ff0ae8fa9a45731f4f43`
has been merged into `main` by PR #21, merge commit
`b293f34dd57ee61c78371643de6d0ebaa75c626e`. Their source trees match.
Monitor, recipient, group, endpoint and secret management, incident controls,
and collection Apply are implemented. Merging the source does not complete
release qualification. The following gates remain open:

| Remaining work | Required completion |
| --- | --- |
| External workers | [Public configuration admission](external-configuration.md) now connects conditional API changes, file/URL collection validation and bootstrap to retained JobTypes under the build/runtime gates. Its 24 focused cases and all 37 refreshed process/crash scenarios pass on both compilers, with Go 1.27.1 race detection. The 27 ordinary scenarios create monitors through the public SDK; ten capacity cases retain stopped setup. The complete affected management package, 135-symbol build exclusion and affected vet checks pass on both compilers. Earlier [five-minute mixed-load reports](worker-dispatch.md#journal-accounting-under-load) pass at the default limits and drain without unknown actions, repeated invocations or unreconciled outcomes. Qualify the frozen candidate and complete independent review. |
| A2A jobs | Both built-in roles and confirmed-recovery-failure escalation are connected through configuration, storage and consumer interfaces. Managed and file-only restart observation read the original encrypted connection without repeating or resolving unknown actions. All four file-only compiler/tag campaigns pass; actual configured-agent effect evidence, full candidate qualification and independent review remain open. See the [restart checkpoint](a2a-jobs.md#reading-retained-tasks-after-restart). |
| Native recovery | The [Linux user-service checkpoint](../review/local-evidence.md#record-b686659d3a9584df5f3a62f8) passes actual local installation, systemd readiness, forced termination/restart, running and stopped updates, removal/reinstall and authenticated stopped backup restoration on WSL/ext4. The [two-version Linux checkpoint](../review/local-evidence.md#record-99e95fdbe8223ce1201e203e) adds log/snapshot recovery, older-format refusal, stopped previous-backup restoration, preserved incident ownership/history and independently counted notification/recovery effects. These use development binaries; system services, other native OS/architectures, final packaged artifacts and independent review remain open. Local key initialization/inspection and the earlier compiled foreground TLS/authentication restore flow pass. Seven Windows amd64 key scenarios pass under a protected user-profile directory; the host's AppData ancestor ACLs are rejected and remain unqualified. The [macOS adapter](macos-protected-files.md) cross-compiles on both architectures with both compilers; native permission/crash/backup tests remain pending. |
| Complete candidate verification | The recorded application, persistence, SDK/worker, vet and vulnerability matrices pass. The rebuilt dashboard passes 507 frontend tests and all eight required browser scenarios in each of four compiler/tag builds; all five CLI scenarios also pass in those four builds. These records use the development workspace and capture unchanged inputs during their individual runs. Freeze the final source/version graph, qualify cross-consumer contracts, accessibility and bounded-input behavior, refresh checks affected by subsequent changes, and complete independent review. Component results do not qualify the complete candidate. |
| Downloaded SDK consumers | [Pre-freeze module preparation](go-sdk-status.md#pre-freeze-module-preparation) now produces genuine Go dependency changes and matching private module downloads. Both compiler consumer campaigns, complete downloaded SDK/worker suites and source CLI installations pass with `GOWORK=off`. The four generated module-file changes are applied. Freeze the reviewed SDK, worker and application versions, qualify the unchanged frozen source/archive, then verify public downloads without replacements or checksum exceptions. The [retained-input reader](go-sdk-status.md#retained-consumer-inputs) continues to reject preparation reports as release evidence. |
| Distribution qualification | The [1 October Compose checkpoint](../review/local-evidence.md#record-41fffe6ff8fd9233cd2204ee) passes seven real Linux amd64 scenarios using the production Dockerfile and Compose definition. Stopped token rotation commits the new verifier; the new token works, the old token returns HTTP 401, and identity/history survive restart and down/up. Its two earlier failures remain retained. Both affected Go suites and vet variants pass; Helm 3/4 rendering/lint and release-tool checks pass. These are captured working-tree artifacts. Reproduce frozen unsigned artifacts from two roots and an extracted archive; execute every native OS/architecture and service/package lifecycle; qualify the second image architecture and actual Helm storage/upgrade behavior. |
| Provider evidence | The [short local fixture checkpoint](../review/local-evidence.md#record-9871808b82bc749a4d0aa5a2) adds Docker check/restart and six real database/broker checks, recording historical local coverage of 18 of 35 driver roles against the same verifier bytes. Its raw provider report was later removed during disk cleanup; final qualification requires fresh retained evidence. The [source-bound verifier checkpoint](../review/local-evidence.md#record-539e288c48441369de449445) adds recorded builds and a frozen-source/executable reader. Diagnostic source cannot qualify a release. The earlier [report-integrity checkpoint](../review/local-evidence.md#record-3ca0b65cf379d7b1cf5e0044) retains both compiler/tag matrices. Finish the remaining native and configured-provider effects, frozen qualification, independent review and aggregate publication validation. Missing accounts remain not verified. |
| Comparative performance | Qualify frozen builds, matched 10k/100k/1m comparisons and fault scenarios with durability and SLO measurement enabled. The [retained-evidence reader](07-scale-evidence.md#retained-campaign-verification) accepts comparison and fault inputs without a soak directory and records endurance as absent. A 24-hour run is outside the requested work. Existing harness checks establish tooling behavior only. |
| Publication and documentation | All fourteen assessment readers, private OCI/chart/SBOM assembly, exact-digest transfer/progress and ordered module-tag/download continuation are implemented. The locked protected workflow now consumes the exact OCI layout, tested chart and qualified module inputs, with source-bound OCI/module progress restoration and exact index versioning. That workflow has not executed. Actual independent review, complete frozen execution and image inventories/readback, real-account publication and image-attestation/referrer continuation, final signatures/provenance/downloads and aligned source/rendered documentation remain open. Release-asset, original signing-cache and chart continuation helpers now have retained local contracts; they do not establish actual publication. Keep candidates private until the agreed gates pass; a 24-hour run is not one of those gates. |

The [1 October worker-evidence checkpoint](../review/local-evidence.md#record-0d658b96acbd60b75c14120b)
adds a retained-input reader and bounded recorder for the frozen worker campaign.
The private release workflow records all 37 crash/capacity scenarios and the
five-minute mixed-load test on both compilers, with race instrumentation on the
release compiler. The reader checks original test/package completion, worker
helper instrumentation, source identity, offline verified SDK dependencies,
independent target counts, progress and complete journal drain. Original SDK
archive bytes and module graphs are retained rather than substituting summaries.

All 236 release-tool, 30 packaging-tool and 48 benchmark-tool regressions pass;
documentation, workflow and whitespace checks pass with unchanged captured
inputs. These checks include actual Python fixture-process deadline/descendant
cleanup, but no new native CPRa worker campaign, fleet benchmark or remote private
workflow execution. The frozen worker run and independent review remain open.
The private assessment now has six component readers; the other required readers
and evidence remain outstanding, and publication stays locked. No 24-hour test
is required or performed.

The [1 October local provider checkpoint](../review/local-evidence.md#record-9871808b82bc749a4d0aa5a2)
uses cached images and bounded disposable containers. Docker health and restart,
Redis, PostgreSQL, MySQL, MongoDB, RabbitMQ and Kafka pass through production
drivers with independently observed local effects. Two earlier Kafka readiness
failures remain retained. Separate internal/external advertised listeners correct
the fixture; failed startup never counts as an executed passing CPRa check.
Native client commands have container-side deadlines, and a real process check
confirms that a timed-out child is gone before the container is removed.

Local fixtures reserve 4 GiB of physical host space plus their selected budget;
this does not relax the separate 30 GiB plus fixture-headroom requirement for
large comparisons. Local evidence does not establish provider-account
certification. Frozen qualification, native platform coverage, independent
review and publication remain open; no 24-hour test is required or performed.

The [1 October Linux user-service checkpoint](../review/local-evidence.md#record-b686659d3a9584df5f3a62f8)
executes the production local installer and actual systemd user service on the
current Linux amd64 WSL host. The same Raft identity survives forced termination,
running/stopped updates, removal/reinstall and complete stopped backup restore.
Restore requires a new authentication epoch; the restored operator works over
verified TLS, while the old named token and startup token return HTTP 401.
Actual supervisor settings include `Type=notify`, `Restart=on-failure`,
`KillMode=mixed`, a 60-second stop allowance and `UMask=0077`. The mounted
filesystem is ext4; the filesystem magic label is recorded separately.

The explicit local-user fixture mode refuses existing CPRa resources and needs
an already-running user manager. It never enables lingering or starts a session.
The manifest contains zero monitors and dispatches zero provider operations.
Both actual runs pass, and cleanup confirms no owned files or CPRa service
registration remain. All 243 release-tool regressions pass, including preservation
of prior reports, rejection of unfrozen inputs by ordinary CI and cleanup ordering
before removal of an owned user manager. These are private working-tree results,
not frozen artifacts, a full native matrix, cross-version compatibility or an
independent review. No 24-hour test is required or performed.

Private rendered-site verification used the exact pinned documentation tools and
a separate copy of the site sources. The first strict build failed with 245
warnings. Fourteen broken section links are corrected and verified against the
rendered heading IDs; the repeat build has no broken-anchor warnings and still
fails with 231 navigation and missing-target warnings. These include private
qualification records and source files that are outside the website's asset set.
Both failed builds remain recorded under the native-user checkpoint. Source
Markdown checks are therefore not rendered-site qualification. The documentation
branch is unchanged, and its older source-version metadata still needs alignment
with the eventual frozen candidate before publication.

The [public-configuration process reports](external-configuration.md#verification-boundaries)
close the earlier stopped-setup gap for the 27 ordinary worker scenarios. The
complete 37-scenario campaign passes on Go 1.27.1 with race detection and Go
1.25.0, with no skips or changed Go inputs. It covers all three handler categories,
lost replies, process termination, unknown actions, revocation and journal limits.
The recorded 38 test passes include the campaign's parent test. These local
development-workspace runs do not qualify frozen downloaded modules, native
artifacts, provider accounts or the complete release. Older checkpoints below
retain their original scope and failures.

The [configuration checkpoint](../review/local-evidence.md#record-3482c687a1d4f99fa7e0a4d2)
records the campaign begun on 30 September and completed on 1 October. The
complete management package passes 856 all-built-in-driver tests and 1,108 tests
with `externaljobs` on each compiler. The four explicit skips in each variant
are three subprocess helpers and the unavailable-driver case that does not apply
to an all-driver build. All four affected vet variants and both build-exclusion
campaigns pass. Earlier failed bootstrap, compile and routing-fixture attempts
remain retained. The two routing fixtures now supply catalog settings while
preserving their work-limit and cancellation assertions. The checkpoint binds
raw reports and source inventories and identifies that later test-only correction
in the earlier API/process inputs; it is not one frozen release candidate.

The [complete SDK/worker package campaign](../review/local-evidence.md#record-c74d0a65a8192831ab48848c)
passes 186 default SDK tests, 203 tagged SDK tests and 38 worker tests on each
compiler. Go 1.27.1 uses race detection. Every selected package completes, no
tests skip, and the captured module inputs remain unchanged. This campaign uses
the explicit development workspace; it does not qualify frozen private archives,
public downloads or the complete candidate. Independent review remains pending.

The [1 October dependency checkpoint](../review/local-evidence.md#record-4e4bf9fdb00d49b2193f12cf)
records actual preparation from captured working-tree source, without editing it
during resolution. Go generates the application and worker module declarations
and missing SDK sums; the four proposals are subsequently applied with original
and resulting file-hash checks. Both compilers pass all ten private consumer
checks and seven package-documentation variants. Complete downloaded-module
tests pass 186 default SDK, 203 tagged SDK and 40 worker top-level cases on each
compiler, with 4,148 total test/subtest pass events, no skipped cases and unchanged
module inputs. The generated transport package has no test files and is recorded
separately. CLI source installation, module verification, help and version output
pass on both compilers without Git, a workspace, replacement modules or checksum
exceptions. These are normal CGO-free installation/test builds; the earlier race
campaigns retain their own scope. The first Go 1.25 ZIP-comparison rejection and
two no-test-package classification failures remain recorded. Frozen source,
versioned public installation, independent review and complete release gates
remain open.

The [retained-browser reader](../validation.md#inspect-retained-browser-evidence)
compares frozen-source inventories, raw Go completion, tool identities and
retained log/screenshot bytes. Its 23 report-reader tests and all 125 release-tool
tests pass. A consistent component result keeps the aggregate gate closed and
requires review. Current browser campaigns used the development workspace and
cannot be relabeled as frozen-candidate evidence.

The [dashboard/browser record](../review/local-evidence.md#record-c2d0d2744aa828240a58e3ee)
closes the stale build record after the release recipe changed. The pinned
frontend build passes build, type, lint and all 507 tests. All eight required
browser scenarios pass in default and `externaljobs` builds on both compilers;
Go 1.27.1 uses race detection. All four source inventories remain unchanged
during their runs. Each report retains its raw Go stream, stderr and seven
screenshots. The campaign now rejects skipped or contradictory test results,
missing package completion or screenshots, changed inputs and output overflow,
and preserves failed attempts without replacing earlier evidence. Tooling and
private-upload policy checks pass. These are local browser fixtures, not full
accessibility, frozen SDK installation, native distribution or release evidence.
The [first application attempt](../review/local-evidence.md#record-d6230f0c31f8e21df69915da)
retains 913 passing tests, two management test failures and excluded-API build
failures from intentionally invalid fixtures under `bin/`. Its source did not
change during execution. The policy test still pinned version 2 after the
implementation advanced to 4; it now checks the current reviewed profile and
retains rejection of the original policy. Both ciphertext-corruption tests now
read and flip an actual byte instead of writing a constant that might leave
randomized ciphertext unchanged. All three focused checks pass on both
compilers and both tag selections; 1,024 repeated corruption checks additionally
pass with race detection. The [corrected complete application matrix](../review/local-evidence.md#record-a60d73d4fc19c80f9d32d1ed)
passes its default Go 1.27.1 race run: 915 tests pass, with 18 explicit
browser/subprocess-helper skips, unchanged inputs and every selected package
completed. The full-driver race variant passes 926 tests and fails the Twilio
acceptance fixture, which selected the final inventory record instead of Twilio
after A2A was added. Both failed subcases and the unchanged source inventory are
retained. Remaining variants did not run in that campaign. The
[Twilio correction](../review/local-evidence.md#record-5b772257c2bf415ad434e9c5)
selects by the `code/twilio` identity and also checks configured/invoked status;
it retains rejection of observed delivery, complete-provider certification and
secret disclosure. The complete driver-verification package passes 21 tests in
all four full-driver compiler/tag variants. The subsequent
[complete application matrix](../review/local-evidence.md#record-674ea14f64f7c53897510c62)
records all six variants against that correction; its final results follow.

The corrected application matrix now passes all six variants on both compilers:
915 default-build tests, 927 full-driver tests and 1,081 full-driver tests with
`externaljobs`, totaling 5,846 passing test executions. Go 1.27.1 uses race
detection. Captured inputs remain unchanged and every selected package completes;
the persistence package has its separate complete matrix below. Their 18, 19 and
21 explicit skips include separately invoked browser and CLI scenarios,
subprocess helpers, and the unavailable-driver scenario that applies to default
builds. This completes the local application package matrix, not frozen-candidate,
native distribution or independent-review qualification.

The [native CLI record](../review/local-evidence.md#record-90fa7ab6457b2be08fe44eb7)
passes all five opt-in cases in default and tagged server builds on both
compilers: normal management, diff, collection apply, reselection and lost replies.
All 20 cases complete with no skips or changed captured inputs. They execute
actual child CLI binaries built by the parent's compiler against normal
application startup; the management and diff cases retain their binary hashes
and evidence files. The release compiler instruments the server/tests with race
detection; the child CLI builds are CGO-free and uninstrumented. Both CI compiler
jobs now invoke every case in both server builds, and a workflow coverage check
guards those invocations. Development-workspace results do not qualify frozen
downloaded modules, final native artifacts or independent review.

The [archive reader record](../review/local-evidence.md#record-39195c80695d4f09337b289c)
adds explicit target binding, exact executable inventory, canonical unique
member paths, regular-file and executable-mode checks, bounded metadata and
chunked executable hashing. Windows case aliases, links, privileged modes,
encrypted ZIPs, ambiguous JSON and numeric overflow are rejected. All 22 focused
archive tests and 147 release-tool tests pass on unchanged captured inputs. The
initial malformed-compression error is retained separately from its correction.
These are small reader fixtures, not final native artifact execution or a passing
aggregate publication gate. Publication remains locked and review is pending.

The [OCI retention record](../review/local-evidence.md#record-bfe1ab859ddc466c274b5b4c)
adds retained Docker reference and both independent OCI build transports instead
of deleting them with the temporary build directories. Their actual contents are
checked against recorded sizes, hashes, descriptor graphs, source labels and
runtime settings. Layer hashes are also verified after bounded decompression.
The container CI job retains reports and transports after failure. All 22 OCI
reader tests, 26 packaging-report tests and five producer tests pass; the complete
release-tool and packaging-tool suites pass 174 and 26 tests respectively.
Physical tar headers are inspected before library decoding; oversized cumulative
extension metadata, excessive extension headers, global metadata and sparse outer
files fail before unbounded metadata decoding or replication.
Producer tests use controlled Docker replies and small actual archive files.
They do not execute a container, qualify final registry/chart bytes or complete
the aggregate publication gate. The initial obsolete-report fixture rejection is
retained separately from the corrected archive-backed fixtures. Review remains
pending.

The [static and vulnerability matrix](../review/local-evidence.md#record-888cf0e434e6b7b9055d7b76)
passes all 12 vet combinations and six release-compiler source vulnerability
scans, with unchanged captured inputs. These checks precede the Twilio test-only
correction and use the explicit development workspace on Linux/amd64. They do
not establish packaged/native or frozen-candidate security qualification.

The [capacity record](../review/local-evidence.md#record-febb2e072dd75016224a10c1)
adds ten real server/SDK-runner scenarios to the original 27-case campaign. All
37 pass on Go 1.27.1 with both processes race-instrumented and on Go 1.25.0.
All 38 worker-module tests pass on each compiler. The new cases distinguish
reserved and live bytes from the allocated bbolt file, retain original outcome
digests and receipts across forced termination, preserve unknown actions across
repeated terminal reconciliation, and admit new work when the exhausted budget
is increased. Independent target counts show no repeated execution. These are
configured-limit correctness checks, not default-limit sustained load or native
distribution evidence. Public configuration and independent review remain open.

The [mixed-load raw result and recovered summary](../review/local-evidence.md#record-35be96acc6e57bb43b67e956)
record a Go 1.25 test and package pass after 300 actual measurement seconds at the
unchanged default worker limits. All 64 monitors receive checks, recovery and
notifications. The independent target records 1,449 checks, 363 recoveries and
394 notifications; 273 of 300 samples have at least 15 active handlers, with a
peak of 16. Every invocation has an original receipt and matching outcome, no
invocation repeats, and the journal drains. The restarted production API returns
explicit expired-check responses for 425 count-retired receipts. Earlier failed
fixtures remain recorded, including the five-minute verifier that incorrectly
required retired successful check records to remain stored.

The final Go stream is complete, but the outer evidence writer failed because
Go split its long output line across events. The recovered summary joins those
fragments and retains the wrapper's exit code 1; it does not invent an unrecorded
Go process exit code. The writer is corrected for subsequent invocations.
The subsequent [journal-accounting correction](worker-dispatch.md#journal-accounting-under-load)
now has complete passing reports on both compilers, including race detection on
Go 1.27.1. Its status benchmark retains three repetitions per record count, and
40 worker-package tests pass on each compiler. The updated five-minute campaigns
record 2,143 release-compiler and 2,188 minimum-compiler invocations, zero unknown
actions/duplicates/unreconciled outcomes, and complete journal drain. Earlier
failures are preserved. All 37 refreshed process-crash cases pass on both
compilers; public configuration admission, final qualification and independent review remain open.
This is neither million-monitor throughput nor exhaustion of the default journal
byte limits.

The [queue-order record](../review/local-evidence.md#record-e6f9d5f209b055d893d0668b)
preserves a reproduced failure in which a newer check overtook an older recovery.
New polls now select by committed log position across advertised types, with a
stable identity tie-breaker within one log entry. Tagged format 37 records that
rule; earlier poll commands retain their original replay behavior. The regression
keeps admitting work for 64 polls. Additional checks cover shared log entries,
response byte limits, exact retry, snapshot/log replay and five forced-process
restart cases, including held unknown recovery actions.

Both compilers pass 198 selected storage tests; Go 1.27.1 runs with race detection.
The real server/SDK-worker campaign passes all 27 cases on each compiler after
removing the fixture's free-port race: the child now reports its actual bound
loopback address. Those combined commands still failed a new HTTP test whose
logger setup was missing. Corrected HTTP checks pass all four compiler/tag
combinations; the failed commands remain recorded. Default storage-boundary,
build-exclusion and scoped static checks also pass. These component results do
not establish a complete candidate pass. An isolated selector benchmark measures
the existing maximum queue size; it is not fleet throughput or endurance evidence.
The public external-configuration gate remains closed.

The [local-key record](../review/local-evidence.md#record-300bff1ad214fa06eb7afeca)
adds explicit `init` and `inspect` commands, corrects the startup error's command
name, and exercises the commands from the native-service fixture. Both require
absolute key and state paths; generation is exclusive, inspection is read-only,
and neither opens Raft or activates runtime configuration. Output contains only
the non-secret key identity. Native-service report validation now requires that
initialization identity to be confirmed by inspection.

The affected packages pass 156 default and 159 tagged tests on each compiler,
with race detection on Go 1.27.1. All six CLI OS/architecture targets compile.
Compiled Linux/WSL startup, restart and authenticated backup/restore pass with a
CLI-created key. Windows NTFS tests pass seven user-key scenarios after correcting
the fixture's process-local executable-extension setting and DACL-only mutation.
The earlier attempts remain recorded. The default AppData attempt fails because
its ancestors grant an additional capability SID full control; this policy
rejection is retained separately from the passing user-profile test. Existing host
permissions were not changed. These checks do not qualify native supervisors,
Windows default paths, macOS execution, or final distribution artifacts.

The [file-only record](../review/local-evidence.md#record-ea5bfad2df183d61bf0d6870)
extends encrypted recovery to manifests with management disabled. A shared
`storage.encryption` source supports both modes and conflicts with a second
source under management. Startup validates retained connections even after
monitors are removed; it rejects missing/wrong keys before agent requests.
Each of the four compiler/tag campaigns passes 50 selected tests. The six new
forced-process-restart scenarios cover both roles and unchanged, changed or
removed manifests, including snapshot recovery and wrapping-key rotation.
The v1 state response now omits internal connection envelopes. The report keeps
the initial fixture compilation/filename failures and their corrections.
This is Linux/WSL fixture evidence, not native packaged or configured-agent
qualification. Independent review and the other eight workstreams remain open.

The earlier [restart-observation record](../review/local-evidence.md#record-6a4aabd9ccf17f9b6965a542)
adds authenticated original connections, separate process-bound read authority,
bounded polling and joined shutdown. A2A and related contracts pass 61 default
and 64 tagged tests per compiler, including six new restart scenarios, six
existing escalation scenarios, four configured startup/restart scenarios and
three observation process-kill boundaries. Go 1.27.1 runs with race detection;
Go 1.25.0 verifies minimum compatibility. All four format checks and the final
observer fixture checks pass after correcting their assertions/cleanup. These
test-only follow-ups are recorded separately from each earlier source inventory.

The attempted full persistence race suite passed 282 tests, reported two outdated
format assertions and hit its 12-minute aggregate timeout while replaying a
collection fixture. The assertion corrections and the interrupted collection
test pass their focused reruns. A new [complete package campaign](../review/local-evidence.md#record-670d8f9b739c85d3c022f614)
has completed default and tagged race builds without a test-name filter. They
pass 706 and 870 tests respectively, in approximately 28 and 35 minutes, with
unchanged Go inputs. The two and six skipped entries are subprocess helper entry
points, whose parent campaigns pass. Complete Go 1.25.0 package checks also pass the same 706 and 870 tests,
in approximately 18 and 24 minutes, with unchanged Go inputs and matching helper
skips. Their first launch attempts selected an absent compiler path and ran no
tests; those attempts remain recorded separately from the corrected passing commands.
Independent review and the rest of the full candidate matrix remain open. The
later file-only record closes that
implementation gap; deployed-agent effects, independent review and the other
release workstreams remain open.

The earlier [escalation record](../review/local-evidence.md#record-6d71220e33fdd57019a7cf45)
adds the confirmed-failure policy, original recovery context and consumer views.
All six normal-startup/restart scenarios and the selected policy/observation
contracts pass on Go 1.27.1 with race detection and Go 1.25.0, in default and
`externaljobs` builds. A test ordering correction prevents a second failed check
from replacing the early-escalation action before inspection; the three earlier
failed matrix runs remain recorded. The selected storage checks pass 137 default
and 160 tagged tests per compiler. The dashboard passes 507 tests plus type/lint/
build checks, and 418 selected Go regressions pass with race detection. All eight
required browser scenarios pass in the child-owned report; the outer report
summarizer failed afterward on a JSON string log line. That reporting failure is
retained separately. These are scoped checkpoints, not a complete-candidate pass.

The [configured-agent verifier record](../review/local-evidence.md#record-ede657ded5f96f511b411110)
adds real Raft admission to the provider runner and independent action-correlated
observers. Twenty Go tests pass in all four compiler/build-tag combinations,
including process termination during an agent request and refusal to reuse a
campaign directory. Additional checks validate the disabled manifest examples.
All 48 Python verification tests pass. The built, CGO-free full-driver CLI invokes
both roles against local fixtures, verifies their independently exported effects,
and passes those exact reports through the aggregator. Its evidence remains
`mock_contract`; no operator agent or account-backed effect is qualified.
Default/tagged vet, documentation and whitespace checks pass. These changes remain
local, with independent review and post-restart remote-task reconciliation open.

The earlier [A2A configuration record](../review/local-evidence.md#record-a5ea79740536373427f6a53c)
includes 54 default and 59 tagged focused tests on each compiler, four real
application startup/restart scenarios per build, and 504 dashboard tests with
type, lint and build checks. The broader selected Go suite passes 368 tests with
race detection after correcting its stale 33-driver inventory assertion; that
failed attempt is retained. SDK generation, build exclusion, vet and documentation
checks pass. The candidate inventory is 35 driver roles. These local changes are
uncommitted and do not complete independent review or release qualification.

The 30 September [worker record](../review/local-evidence.md#record-9349625d7766f4e47d5aed84)
supersedes the earlier incomplete process matrices below. The resumed Go 1.25 run
first reproduced a race in the lock-probe test's observation: a successful short
process could exit before the parent read its file. The parent now waits for its
successful exit before reading that result. Both complete 27-case matrices and
all 239 tagged SDK/worker tests pass on the unchanged recorded source; the failed
attempt remains in `bin/verification/worker-process-resumed-2026-09-29/`.
Tagged vet and the default/full-driver exclusion campaign also pass. Its first
attempt hit the compiler timeout; the unchanged check passed with a
larger compiler memory budget, and both attempts are retained in the record.

The subsequent [agent-storage record](../review/local-evidence.md#record-a9ec5149c084a9b33e33ba6b)
adds persisted task/message receipts and an agent executor marker. Local return
or process replacement cannot fence a still-running remote task. The selected
persistence suite passes 66 default and 82 tagged tests on each compiler,
including five real process-kill boundaries per combination; Go 1.27.1 uses
race detection. Controller systems tests, vet, build exclusion, release-tool
checks and local application/CLI builds pass. Common format 31 and optional
combined format 32 preserve the build boundary. This changes the current source;
earlier worker, browser and native reports remain evidence for their recorded
inputs, not a complete-candidate rerun. The controller A2A driver, escalation,
consumer contracts, configured agent effects and independent review remain open.

The later [agent-execution record](../review/local-evidence.md#record-ec9b4f3323df6787d2342d3d)
connects that storage boundary to actual controller jobs. Six process-kill
boundaries preserve original actions and resume only safe queued work. The
upstream SDK fixture exercises transport, intervention and notification roles
with independent effect counts. Receipt cancellation was reproduced and fixed
without permanently marking usable storage unavailable. This still leaves
public driver/configuration registration, escalation, consumer integration,
configured agent effects and independent review unfinished. The report records
each completed compiler/tag check and keeps broader release qualification false.

The native service fixture now provisions named access and verified TLS before
first startup, preserves protected configuration through lifecycle operations,
requires a new restore epoch and fresh operator, and rejects old credentials.
Its report reader requires these observations. All 83 release-tool tests pass,
including real isolated TLS requests and negative restore-report cases. The
[foreground execution](../review/local-evidence.md#record-b554d51acac0ce45fcdc01b6)
uses compiled application/CLI binaries, real Raft, local authentication commands
and authenticated backup/restore; original node identity and both HTTP 401
rejections were observed. It does not install a service or qualify official
artifacts. All changes remain local and publication remains locked.
The Linux secure-configuration and local-administration packages additionally
pass 56 top-level tests on each compiler, with release-compiler race detection;
one subprocess helper skips only in the parent invocation.

The local macOS protected-file change supplies native ACL inspection and exclusive
publication without adding a cgo dependency. Shared permission, alias, publication
and process-crash tests now select macOS, with additional native ACL and bounded
metadata-decoding cases. Native source and private release jobs explicitly select
the secure-configuration package. The
[checkpoint](macos-protected-files.md) distinguishes passing cross-compilation from
unexecuted native checks; no macOS support or release gate is complete.

The local [release-identity change](../release-engineering.md#source-and-build-identity)
passed 40 release-tool tests and four private-publication policy tests. Its tests
reject missing identity in each of the 34 required packaging reports and execute
the documentation workflow's report writer against matching and mismatched build
metadata. These are small Python contract tests, not native/service/container/chart
campaigns. The [scoped report](../review/local-evidence.md#record-b2dd15eb437d2b49faad213e)
records that boundary. Independent review, the complete evidence manifest and
aggregate publication gate remain open; publication is still locked.

The subsequent [packaging report validation](../release-engineering.md#packaging-report-validation)
rejects a `pass` report with missing restart observations, lifecycle scenarios,
source-install combinations or reproduction inventories. It verifies reported
outcomes against the final native executable and distribution hashes and rejects
failed/pending steps, inconsistent source metadata and ambiguous JSON. The
regression first reproduced acceptance of a native report with no restart data.
Source-install reports now record their execution platform. The obsolete macOS
backup marker was removed after the native adapter was added; actual native
qualification and the service fixture's authentication reset after restore remain
open. These changes are local and uncommitted. Their small fixtures do not perform
native, provider, container, chart or endurance campaigns, and the complete
publication gate remains unfinished.

The subsequent [module-archive change](go-sdk-status.md#module-archive-checkpoint)
selects committed sources with Go's upstream ZIP rules, stages the application's
pinned SDK separately, and rejects inconsistent dependency metadata before staging.
A Python regression reproduced an archive paired with a later `go.mod`; the
corrected wrapper rejects it. The
[scoped report](../review/local-evidence.md#record-2ef4fd3f4b97810d56734741)
separates executed Python/Git/archive tests from the unexecuted Go helper and
downloaded-consumer campaign. Independent review, version freezing, private cache
bootstrap and release qualification remain open. These changes are uncommitted.

The subsequent private dependency-cache implementation provides a source-only
archive command, isolated SDK/worker downloads, restored release checksum settings,
dependency-graph records and source-bound cache selection for release builds.
Changed dependency inputs fail preparation and remain in scratch for review;
neither the checkout nor official module declarations are rewritten. Source
rebuilds validate their archive identity and executable modes and use separate
compiler caches. Native and compiler-compatibility jobs prepare their private
dependencies before testing; compatibility covers all three application modules.
Failed preparation retains a report, including missing source archives. The
[scoped report](../review/local-evidence.md#record-9c804275daea5ceceb2b277c)
records Python fixtures and workflow-policy execution only. Actual Go bootstrap,
compiler/platform campaigns, independent review and the full release remain open.

The benchmark tooling now selects the release compiler/settings, keeps debug
information, excludes ambient workspace/compiler overrides, and builds from
committed archives with an explicit module cache. It records and checks the
target-server binary alongside baseline and candidate CPRa. A changed-target
regression now fails before workload execution. HTTP reads use a private per-run
token; redirects and ambient proxies are rejected, and an incomplete trial stops
the campaign. The [scoped report](../review/local-evidence.md#record-38b3f0bd29a3d9053bd519db)
separates local HTTP/Git and controlled compiler/process fixtures from actual
builds and performance evidence. Independent review and every scale/endurance
acceptance gate remain open.

The latest local integration work adds [bounded worker polling](worker-polling.md)
and [authenticated runtime preparation](worker-runtime.md). Polling and runtime
conversion have scoped race and Go 1.25 evidence; all 340 build-exclusion checks
passed. These checkpoints do not open public external configuration or connect
controller dispatch. Their scoped report is
`bin/verification/worker-runtime-2026-09-29/result.json`. Production worker route
registration and complete-request HTTP shutdown now pass 53 selected HTTP and
normal application tests on Go 1.27.1 with race detection and Go 1.25.0. Tagged
vet and all 340 build-exclusion checks passed. A regression reproduced and fixed
premature repeated-shutdown success while a request was still preparing. The
[HTTP qualification record](../review/local-evidence.md#record-87b68dbb023c8247d2d76eec)
preserves source hashes and failed attempts. This does not qualify complete
worker execution or close independent review.

The subsequent [notification checkpoint](worker-notifications.md) qualifies
inline and shared notification authority and retained original outcomes. The
[worker-observation checkpoint](../review/local-evidence.md#record-d747aeae189b2c5d44d9fecf)
records 25 selected integration checks and all 238 tagged SDK/worker tests
passing on Go 1.27.1 with race detection and Go 1.25.0. Tagged vet, two-root
generation and 232 negative compilation checks passed. Operator observations
are implemented; controller dispatch and public external configuration remain
unfinished. These changes are local and uncommitted.

The subsequent [controller preparation connection](worker-runtime.md#controller-projection)
now selects the external entity preparer under the runtime opt-in, persists
notification source identities at their original positions, and excludes remote
checks from local worker demand. The
[controller checkpoint](../review/local-evidence.md#record-ab6681e19b6cea4c39fc8984)
records 69 tagged and 63 default tests passing on each compiler, with race
detection on Go 1.27.1. Vet and build-exclusion checks passed. This completes
preparation and projection, not external dispatch or schedule accounting.
Those execution boundaries and independent review remain open.

Local qualification on 27–28 September:

- The unchanged-source default persistence race run reached Go's 20-minute
  package timeout after 466 top-level tests passed and one subprocess helper
  skipped. There was no completed package pass. The active test had only just
  started; the timeout was cumulative. Raw output and source hashes are retained
  in `bin/verification/persistence-release-2026-09-27/`.
- The first required browser campaign passed seven cases and failed collection
  activation while waiting for retained results. The dashboard stopped polling
  terminal operations even when their execution results were still pending.
  Four regression cases reproduced that boundary. The local correction preserves
  private-input cleanup and continues only reads of the original operation.
  The rebuilt dashboard passed all 497 tests, type checks, lint, contract checks
  and asset freshness. All eight required browser cases then passed against the
  rebuilt assets; the tested code and asset hashes stayed unchanged throughout.
  Evidence is in `bin/verification/dashboard-result-polling-2026-09-27/result.json`.
  The correction still needs independent review and integration; it is not part
  of the main commit above.
- Private worker result delivery, heartbeats, append-only late evidence and
  automatic finalization now have SDK/TLS/Raft and native process-kill evidence.
  The selected worker, JobType, command-validation and startup suite passed
  167 top-level tests with
  Go 1.27.1 race instrumentation and with Go 1.25.0; one subprocess helper skipped
  in each parent run. The suite includes 30 native process-kill cases. Default
  boundaries, tagged vet and all 277 build-exclusion checks also passed, with the
  1,062 recorded source inputs unchanged. This does not execute the complete
  worker runner against production dispatch or complete aggregate SLO integration.
  The implementation remains local and needs independent review. See the
  [latest contract](worker-finalization.md) and
  `bin/verification/worker-finalization-2026-09-27/result.json`.
- The subsequent worker monitor-projection change passed 198 selected tests on
  Go 1.27.1 with race detection and on Go 1.25.0, plus 41 default-build tests,
  tagged vet and 277 build-exclusion checks. All 30 native process-kill cases now
  verify that the recovered owner view includes the external monitor. Publisher
  and ECS consumer boundaries have focused tests; complete remote dispatch and
  worker-runner interoperability remain unqualified. All 1,065 recorded source
  inputs stayed unchanged. See
  `bin/verification/worker-projection-2026-09-28/result.json`.
- The subsequent external-check SLO change passed 213 selected tests on each
  compiler, including 36 native process-kill cases; one subprocess helper skipped
  in each parent run. Go 1.27.1 used race detection; Go 1.25.0 verified minimum
  compatibility. Another 46 default-build tests, the six SLO-window race tests,
  tagged vet, 286 build-exclusion checks and 31 release-tool tests passed.
  The rebuilt dashboard passed all 498 tests, type checks, lint, contract checks
  and asset freshness. All eight required browser cases passed against those
  embedded assets. The 1,070 recorded Go/module inputs, 24 release-tool inputs,
  120 frontend inputs and tested assets stayed unchanged. This is scoped local
  evidence; production external dispatch, complete worker-runner integration and
  independent review remain open. See
  `bin/verification/worker-slo-2026-09-28/result.json`.
- The subsequent [worker scheduling change](worker-scheduling.md) allows the next
  check generation while retaining original receipts and requeues eligible
  unstarted assignments with rejected old leases. It also fixes reordered Start
  request timestamps and rejects backwards-time owner adoption. The selected
  Go 1.27.1 race suite passed 229 top-level tests, including 45 native process-kill
  cases; one subprocess helper skipped in the parent run. Another 47 default-build
  tests, tagged vet and 31 release-tool tests passed. All 1,075 recorded source
  inputs stayed unchanged. Go 1.25 and the full build-exclusion rerun remain
  pending because physical C: space fell to 70 MiB. The initial six-failure run
  and its source hashes are preserved separately; five assertions expected older
  snapshot formats and one exposed the concurrent Start bug. The latest report is
  explicitly partial: `bin/verification/worker-scheduling-2026-09-28/result.json`.
  Independent review, terminal-record retirement and production integration
  remain open; this work is local and uncommitted.
- A subsequent monitor-projection correction removes the dependency on retained
  check receipts when rebuilding a healthy or no-data external monitor's owner
  view. Four focused projection tests passed on Go 1.27.1 with race detection and
  Go 1.25.0; four related default-build race tests also passed. These checks cover
  the four persistence files changed after the scheduling report. They use
  memory-store fixtures; record deletion, receipt expiry and a Raft restore after
  deletion remain unimplemented/unqualified. Compiler writes were confined to a
  bounded RAM workspace with read-only reuse of the existing disk cache. Normal
  fixture paths remained under `/tmp`; no physical cache cleanup or large disk
  campaign ran. See `bin/verification/worker-check-projection-2026-09-28/result.json`.
  The full scheduling compiler/exclusion matrix and independent review are still
  pending.
- The subsequent [completed-check retention change](worker-check-retention.md)
  reserves committed check identities and retires completed records without
  losing monitor state, SLO counters or uncertain actions. Expired poll replies
  retain an explicit disposition; the SDK/worker recognizes only the exact
  check-expiry response before removing local replay records. The selected
  persistence/integration suites passed 240 tests on Go 1.27.1 with race detection
  and on Go 1.25.0, including 48 native process-kill cases per compiler and one
  expected subprocess-helper skip. All 235 tagged SDK/worker tests and 48 default
  boundaries passed on both compilers. Tagged vet, 334 build-exclusion checks,
  two-root SDK generation, 31 release-tool tests and 27 SDK-tool tests passed.
  Generated references and copied guides were refreshed. All 1,080 Go/module
  inputs stayed unchanged during the final checks. The initial failed runs are
  retained. The change also corrects late-evidence retirement and serialized-map
  comparisons. See
  `bin/verification/worker-check-retention-2026-09-29/result.json`.
  This is local, uncommitted evidence; independent review and production dispatch,
  routes, schedule accounting, observations and full worker integration remain
  open. The 29 September host check measured 1,616,482,304 bytes (about 1.51 GiB)
  free on C:, below the large-campaign requirement. The filesystem is writable;
  no large campaign or physical-cache cleanup ran.
- Physical C: free space was about **7.9 GiB**. The required scale-campaign
  preflight failed against the **30 GiB minimum plus fixture headroom**; this
  harness reserves another 5 GiB. WSL's virtual free space does not satisfy it.
  A later read-only check during worker qualification reported **5.4 GiB**;
  a subsequent check on 28 September local time reported **3.7 GiB**. The latest
  check reported **1.4 GiB**. The campaign remains blocked. The latest recorded
  bytes and backing-volume inspection are in
  `bin/verification/worker-slo-2026-09-28/host-space.json`.
  The subsequent scheduling checks measured 73,883,648 bytes, about **70 MiB**,
  free on C:. Further build campaigns are deferred; no cleanup was performed.
  See `bin/verification/worker-scheduling-2026-09-28/host-space.json`.
  The focused projection checks subsequently recorded 71,225,344 bytes, about
  **68 MiB**, free on C:. Their limited RAM compiler workspace does not satisfy
  or bypass the physical-storage gate for larger verification campaigns.

## Current finalization plan

The subsequent [controller-dispatch checkpoint](worker-dispatch.md) connects
checks, recovery and notifications to bounded asynchronous assignment admission.
It preserves original identities through lost replies, observes committed
completion on the ECS owner, separates remote ready work from saturated local
queues, and merges definite misses and pauses into one external SLO report.
Public external configuration remains closed. Sustained saturation behavior,
actual worker/server process campaigns and independent review remain open.
The subsequent [action-expiry change](worker-expiry.md) commits an
explicit cancellation for expired, unadmitted actions. This work is local and
uncommitted.

The subsequent [schedule-accounting checkpoint](worker-schedule-accounting.md)
counts ready and preparing checks before admission. Original scheduling tickets
and cumulative bucket counters prevent replay or delayed admission from counting
an obligation twice. Cancellation and shutdown preserve missed obligations;
recovered windows retain explicit coverage gaps. The selected release-compiler
race run passed 281 tests, and the Go 1.25 compatibility run passed 71 tests.
Their report is `bin/verification/worker-schedule-2026-09-29/result.json`.
These checks do not establish complete worker/server interoperability or close
independent review.

The subsequent [admission-fairness change](worker-admission.md) reproduces and
corrects check-first starvation of incident actions, adds bounded per-type quota
backoff, and checks committed capacity before reserving check IDs. Its separate
qualification is recorded in `bin/verification/worker-admission-2026-09-29/`.
Sustained global/observation-capacity pressure and full worker/server execution
remain open.

The subsequent [observation-capacity correction](worker-admission.md#preparation-and-observation-capacity)
separates 16 accepted preparation requests from 4,096 execution observations.
Expired actions and confirmed rejections can complete while observation capacity
is full. Committed work retains its original identity until the owner can track
it, and shutdown accounts for canceled preparation without waiting behind old
observations. The report is
`bin/verification/worker-observation-2026-09-29/result.json`. Full-store tests use
the memory state machine; the owner tests use Raft and model retained observations
of retired checks. Sustained replacement/load and separate-process worker/server
qualification remain open.

The subsequent reconfiguration correction preserves one observation per committed
execution across monitor renames and reversed preparation replies. Four
Raft-backed tests include 20 metadata edits at full scalar-observation capacity
and completion of a moved observation. The full controller race runs passed 94
tagged and 63 default tests; 32 selected checks passed on Go 1.25. Vet and build
exclusion passed. The scoped report is
`bin/verification/worker-reconfiguration-2026-09-29/result.json`.
Real worker processes, sustained load and independent review remain separate
qualification requirements.

**Additional remaining work (2026-09-24):** add an A2A intervention job type and
an A2A notification type. The requested escalation example is a failed Docker
restart intervention notifying an agent to investigate. Protocol, authorization,
task lifecycle and integration design remain open; no A2A implementation is
claimed. See the [A2A backlog and acceptance scope](dashboard-shipping-plan.md#remaining-addition-a2a-agent-intervention-and-notifications).

The [2026-09-23 audit checkpoint](audit-findings-2026-09-23.md) records the
`internal/persistence` rename, security/runtime fixes, scoped verification, and
local controller leadership-recovery implementation. Current affected-package
integration, contextual cancellation, Go 1.25 and optional-driver race checks
passed with independent review. This checkpoint does not close the remaining
collection or shipping gates.

The [dashboard finalization and shipping plan](dashboard-shipping-plan.md) is the
execution roadmap as of 2026-09-14. It includes the missing management server,
SDK/CLI integration, writable dashboard, final distribution checks and provider/
endurance evidence. **Keep release candidates private until all agreed gates
pass.** No public prerelease, SDK tag, image or chart is an early exception.

Implementation has started on the private local branch
`codex/dashboard-finalization`, preserving the existing SDK and documentation
work. The [implementation progress record](dashboard-implementation-progress.md)
distinguishes implemented components, executed checks and incomplete integration.
Normal application startup now integrates the encrypted management catalog,
named authorization and owner reconciliation. Resource editing, controls,
audited reviews and operation reads have local SDK/browser evidence. Collection
preparation, encrypted inactive upload, cancellation and browser file preview now
have scoped SDK, restart and native-browser evidence. Public asynchronous
collection validation and bounded original-result reads are implemented and now
pass a connected normal-main Chrome/Worker/WASM/TLS/Raft campaign, including SDK
reconciliation of the same sealed result after restart. Collection listing,
owner-aware detail and explicit retained-result browsing are now implemented and
have connected browser/restart evidence, passing scoped backend checks and
independent review. [Public activation and connected Apply](collection-public-activation.md)
now connect the original validated operation to SDK/CLI/dashboard execution and
result waiting. Refreshed-browser original-input recovery and explicit operation
continuation are now connected. Restarted CLI recovery now uses the shared
`cpra.file.base.v1` profile and SDK `Reselect` helper, with native process-kill,
lost-reply and original-ciphertext evidence. Bounded upload batching fixes the
reproduced 256-row request timeout while preserving synchronous writes and
per-resource admission. See the [current recovery checkpoint](../review/local-evidence.md#record-ca59b6d8bcce1815a95136a0)
and [runtime contract](collection-reselection-runtime.md#cli-and-sdk-continuation).
The original full management race run exceeded its ten-minute timeout in
`TestStagedValidationLargeNotificationClosureUsesLookups`. Bounded authenticated
dependency metadata now avoids repeated full-resource decoding during graph
lookups. The unchanged full suite subsequently passed in 557.230 seconds under
the original timeout; its 355 selected repository inputs remained unchanged.
The original failure, focused profiles, independent review and final run are
retained in the [performance checkpoint](../review/local-evidence.md#record-2e963fda688bdf4702ea88db).
This is validation-cost and correctness evidence, not fleet-capacity evidence.
The optional [JobType implementation](job-types.md) now provides tagged encrypted
immutable versions, bounded schema validation, explicit runtime opt-in and five
authenticated HTTP operations. Mutations retain authority-bound operation handles
and terminal configuration receipts; paginated reads retain bounded encrypted
generations. [Real SDK/TLS/Raft and native-restart checks](../review/local-evidence.md#record-c42d0e762f3bfd4a68cbd05f)
cover these boundaries.
Scoped committed worker grants, stopped local provisioning and a separate tagged
authentication adapter are now implemented. Credential rotation preserves worker
identity; restore requires explicit reprovisioning; normal builds omit these
interfaces. See [worker administration](../worker-authentication.md) and the
[authorization checkpoint](../review/local-evidence.md#record-ca34842fb9b20113939b486f).
Authenticated immutable JobType references now extend encrypted monitor/endpoint
records, with atomic referenced-delete protection and tagged format-18 checks.
See the [reference contract](job-types.md#configuration-references) and
[scoped verification](../review/local-evidence.md#record-6230515b95343857e49f0f4a).
Tagged format-19 session ownership and exact empty-response replay are now
implemented. The private SDK and journal format 2 bind worker/server/session
identities and distinguish Start from reconciliation. The session HTTP adapter
is tested through a dedicated mux; normal startup still exposes no worker routes.
The [session checkpoint](../review/local-evidence.md#record-e9578c2b25a58ad474677365)
records exact local test scopes, source identities and the incomplete broader
five-minute persistence run. It is not full-package or release qualification.
Tagged format 20 now adds [encrypted queued execution admission](worker-execution-admission.md),
original endpoint pins, inert runtime bindings and original-contract result
preparation. The [admission checkpoint](../review/local-evidence.md#record-9a233417141c5b303c2be4d1)
qualifies these private boundaries. It does not supply Start grants, result
receipts, expiry/noData finalization or active controller dispatch.
The later [format-21 offers and Start checkpoint](worker-offers-and-start.md)
adds guarded allocation, one executable grant, non-executing reconciliation,
protected parameter projection and retained-ciphertext startup verification.
Public external configuration activation, controller dispatch
and full shipping qualification remain unfinished. See the
[external-worker integration boundary](external-worker-server-next-steps.md).
Remeasure physical
C: free space before the large campaign;
the required gate is 30 GiB plus measured fixture headroom, regardless of WSL's
reported virtual capacity.

The private collection plan compiler now records original per-item guards and
dependency outcomes without preparing mutations. Catalog storage format 4 adds
unique reverse-dependent mutation tokens while preserving format-2/3 replay.
These prerequisites have scoped tests; neither registers public Validate/Activate
or makes an uploaded collection executable. See the
[activation boundary](collection-activation.md) and
[format compatibility contract](catalog-mutation-format.md).

The next private prerequisite now retains the original plan descriptor and
bounded fragments with encrypted input in storage format 5. Conditional
finalization, original-input binding, plan-first terminal cleanup and process
restart checks are described in the [staging contract](collection-plan-staging.md).
This internal artifact state is not a public validation verdict or activation
grant. Storage format 6 now retains separate immutable validation results and
enforces permanent named-operator ownership. Finalized results now publish in
bounded pages to 30-day history before temporary staging is reclaimed; scoped
process-kill tests cover partial publication, the final seal and cleanup/restart.
See the [validation result contract](collection-validation-staging.md). Public
validation admission and result endpoints consume those original artifacts;
subsequent checkpoints added conditional execution and public activation.

Storage format 7 now retains one immutable validation request, an exclusive claim
and an audited interruption receipt. A private single-attempt runner binds the
original capability profile, checks current authority before protected reads and
artifact writes, and commits frozen success/rejection results without activation.
Claimed input access ends when either artifact begins. Scoped race, minimum-Go,
tagged, Raft restart and independent-review evidence is recorded in the progress
log. See the [coordinator boundary](collection-validation-coordinator.md).
Normal main now owns one bounded compiler worker, includes it in readiness, and
joins it before closing storage/encryption. Once-per-Store startup retires abandoned
claims without recompiling them; never-claimed requests can begin only after their
original authority/profile checks. Selector, blocked-shutdown, real-process restart
and normal-main tests have scoped evidence in the progress log. Public Validate
now returns a 202 operation; bounded GET validation exposes the original sealed
verdict. The connected release-Go race campaign passed in 11.608 seconds with
exactly five explicit browser writes, no activation, zero target requests and no
active catalog changes; original operation/result/digest survived restart. Its
recorded embedded index is `df03963247b61545e917ebbea80e1bc53d9c46eab32b91e228505069918bc578`.
The same scoped campaign passed Go 1.25 in 10.073 seconds. Parser reproduction
and byte equality between current Vite output and embedded assets also passed.
Evidence is in `bin/verification/collection-validation-browser/result.json`.
Conditional activation and complete collection lifecycle acceptance remain open.
The [collection operation-listing contract](collection-operation-listing.md)
now describes the implementation. Its unified snapshot preserves shared ordinary
receipts and original-owner collection observations with bounded work and no
input decryption. A separate explicit dashboard read retrieves the original
sealed validation result after the local draft is discarded. The new normal-main
Chrome/TLS/Raft campaign passed release-Go race in 12.896 s and Go 1.25 in
11.845 s; list/detail/SDK observations agree across restart, with exactly five
explicit import writes, zero additional browsing writes and zero provider calls.
Its embedded index is
`b4f2223ac4a876a91e65bc3c1369cc6023c63272604e24e0bb1515bc8573129a`.
Evidence: `bin/verification/collection-operation-browser/result.json`. This
supersedes the earlier embedded-asset evidence for this operation-browsing scope.
Earlier durable runs do
not substitute for a complete current shipping matrix; no public release is claimed.

The [private format-8 activation admission](collection-activation-admission.md)
now preserves one original successful plan/result binding and checks current
operator authority separately from that immutable identity. Applying parents
remain live after the old staging deadline; cancellation and explicit restore
retain and fence the original admission. Scoped ordinary, race, Go 1.25, tagged,
snapshot and real-process termination tests pass, with independent source review.
That admission matrix passes, including the complete durable and localadmin race
suites, affected management/server checks, minimum Go, tagged builds, vet and
release-policy tests. The preserved first-run cleanup-fixture failure and its
reviewed correction are described in the progress record. This creates no catalog
mutation, child operation or provider call.

The [private format-9 execution path](collection-execution-integration.md) now
registers isolated conditional-item commands, complete execution snapshots and
recovery of certified child outcomes. First ordinary tests include actual Raft
process kills and stopped replay validation. The private command/snapshot matrix
and full durable race suite pass (449.031 s), with exact source manifests. A
private management runner and cached per-item preparation reads now have their
own qualification in progress. Validation-time encoded-resource headroom and
policy version 2 have passed their scoped matrix. At that format-9 checkpoint,
application lifecycle wiring, retained application-result publication/cleanup,
controller-connected collection acceptance and public API/SDK/dashboard
activation remained unfinished. The later checkpoints below record subsequent
implementation. The current public integration is recorded separately above;
shipping qualification remains incomplete.

The [private format-10 finalization boundary](collection-execution-finalization.md)
now freezes original execution decisions, child dispositions and a result summary.
It distinguishes committed catalog changes from applied children, preserves
cancellation/restore receipts, and emits one immutable history anchor. Exact
retries preserve the original finalization time. Certification reads only the
selected parent's original artifacts and execution namespace outside the FSM
mutex, with one bounded audit index. Admitted input remains retained before begin
and after finalization; historical cleanup entries retain their original replay
behavior. A valid unstarted admission exceeding the audit bound remains healthy,
retained and unfinalized with a quota response.

The final focused format-10 race campaign passed in **35.214 s**, including native
log/snapshot reopen, stopped-store inspection, actual explicit restore with a
pending child, fresh authentication, corruption rejection, historical format-9
and digest compatibility, and the oversized-admission quota case. Independent
source review and a separate execution-result run passed; the reviewer also
checked the final quota correction. The release recipe/reader-format and build
configuration tests passed. These are scoped results: no process-kill campaign
specifically at the new finalization/history boundary has run. The subsequent
[format-11 result checkpoint](collection-execution-publication.md) implements
coordinator-driven finalization, joined publication/seals and protected Store
result reads, with additive generated SDK and browser observation contracts.
The subsequent [result interface checkpoint](collection-execution-interfaces.md)
adds authoritative operation counts, protected HTTP pagination, CLI result reads
and a paged dashboard result view. The real embedded-dashboard/TLS/Raft scenario
passes with Go 1.27 race detection and Go 1.25; restart preserves the original
result. The subsequent
[private retirement contract](collection-execution-retirement.md)
registers format-12 paired execution deletion and format-13 source cleanup, with
committed prefix digests for partial-source snapshots and recovery. Maintenance
deletes validation, plan and input records before the header. Protected points,
pages and operation lists retain original owner/result authority after removal;
history cannot grant execution. Scoped format-13 checks cover native process
termination, retained listing, failure handling and bounded warm verification;
the [evidence record](../review/local-evidence.md#record-e166fe715432c4e1f8c7f609)
preserves exact commands and limits. The subsequent
[public activation checkpoint](collection-public-activation.md) connects
SDK/CLI/dashboard Apply; cross-process reselection remains open. Its
[normalization prerequisites](collection-browser-reselection.md) now include a
shared versioned base-file parser, fixed compiled-artifact fixtures and
format-14 protected profile/prefix checks. The subsequent
[encrypted source and proof components](collection-reselection-proof.md) verify
original raw input, normalized inventory and uploaded ciphertext without
changing durable progress. Scoped native restart, stopped-policy replacement,
minimum-Go and race checks pass; HTTP attempt routing and suffix transfer remain
open. No public resume capability is advertised by these prerequisites. No release publication is authorized by
these private changes. See the
[remaining result lifecycle work](collection-execution-results-next-steps.md).

[Controller leadership recovery](controller-leadership-recovery.md) is implemented
locally. The initialized owner retains unresolved batches and reconciles their
original identities across transient Raft leadership changes, including projection
and executor finalization. Readiness remains unavailable during recovery; startup
failure and permanent storage/restore/migration failures remain fail-closed.
Focused controller races and actual Raft step-down/re-election tests passed,
including retained in-flight pulse work and startup on a follower. That evidence
precedes the latest context-aware persistence integration. The final root suite,
minimum-Go checks and integrated matrix for that source are pending; local
implementation and earlier passing tests do not establish shipping readiness.

## Historical durability candidate

The baseline and evidence below describe the earlier durability work. They do
not qualify the current integrated candidate; old disk readings are historical.

Base: `a370969b041b399c0778318d8915ce059fd74294`; branch: `codex/durable-cpra`.
The 13 September documentation review also identified release candidate
`410fbfb0092d01277b3884cd04151c27443a4226` on `codex/release-engineering`.
That reviewed snapshot predates the current finalization checkpoints above.

The eight adjacent tickets follow the approved dependency order. This is an
implementation candidate. The complete release gate is **open**.

| Ticket | Implemented | Evidence / outstanding work |
| --- | --- | --- |
| 1 Storage | Default Raft, versioned state, identity, locking, snapshots, explicit memory mode | Restart, forced-kill, corruption, format, snapshot, backup and race checks; Go 1.25 compatibility. |
| 2 Lifecycle | Deterministic commits, per-endpoint intent/start/results, unknown holds, configuration reconciliation | Real subprocess crash boundaries and real controller restart regression checks. |
| 3 History | Daily indexed bbolt segments, 30-day retention, compaction watermark, bounded cursor pages | Replay, retention, concurrent insert pagination, missing-segment and complete backup checks. |
| 4 SLO | Bounded driver histograms, exact counters, persisted coverage, cadence obligations, model plus feedback | Distribution, unfinished/missed work, timeout, restart and control-response tests. Full workload targets remain unverified. |
| 5 Interfaces | Read-only state/history/SLO API and CLI; incremental fleet index; dashboard timeline/status | Auth/route/page tests, real browser and API/CLI agreement; 15 dashboard tests/build/lint/type checks. |
| 6 Providers | 33-case config runner, effect/receipt observers, disposable fixture setup, evidence files, manual workflow | Six local driver scenarios passed. Remaining 27 have no passing live evidence. Actual database images, privileged systemd, designated Kubernetes/cloud accounts and receipt readers remain prerequisites. |
| 7 Scale | Pinned matched-build preparation, physical-disk gate, 10k/100k/1m comparison, fault and 24-hour harness | Ten-monitor harness smoke only. The earlier campaign stopped at its physical-space preflight. That historical free-space reading is not current; rerun preflight for at least 30 GiB plus fixture headroom before any large campaign. No completed full campaign is established here. |
| 8 Release | Persistent deployments, backup docs, workflow entry points, dependency notices and documentation candidate | Local packaging and docs-link checks. Independent review, completed provider/performance/endurance evidence and application publication to main remain outstanding. Candidate documentation is now included in the public documentation; that does not qualify the application. |

No ticket's independent-review checkbox is marked complete. The implementation
was checked and revised by its author; that is not an independent review.

Local evidence is under `evidence/local/` and excluded from source archives and
Git. It includes failed fixture attempts as well as successful runs. Component
checks and short harness runs cannot satisfy the million-monitor or full-provider
gates. No subscriptions, infrastructure purchases, user disk cleanup or unrelated
service changes are part of this work.

## Approved management API work

The [management API, batch configuration, and external-worker plan](api-management-plan.md)
records the approved API, SDK, CLI, and dashboard management scope. Its scope
statement was corrected on 2026-09-13: API-first is dependency order, not permission
to omit the writable dashboard. Its ten server/CLI foundation tickets include
encrypted canonical configuration, multi-file preflight/resumable apply,
acknowledge/dismiss/snooze/disable, Ark projection, and the optional externaljobs
protocol. These are planned capabilities; this link does not mark their
implementation or the separate release gates complete.

The [dashboard management plan](dashboard-management-plan.md) records guided
management forms, file import, notification recipients and groups, encrypted
write-only credentials, controls, and diagnostics. These shared resources also
require API, SDK, and CLI support. Generated contracts, guarded resource
preparation, encrypted catalog storage, named authorization, browser sign-in and
shared-resource screens are implemented locally. Normal startup, owner-loop
reconciliation, single-resource operation reads, controls and selected browser
flows have executed qualification. Resumable collections and final integrated
release gates remain open; neither source presence nor SDK fixtures completes
management delivery.
No check-now operation is planned.

Original-file upload recovery is now connected through normal startup, the API,
SDK and dashboard. Its [runtime contract](collection-reselection-runtime.md)
describes encrypted disposable attempts, exact original-input verification and
conditional suffix transfer. Actual Chrome checks cover refresh/token re-entry
and a lost resume reply without duplicate mutation. The operation page also
provides original validation, retained-result review, explicit activation and
confirmed cancellation. The normal-main Chrome continuation check passes with
Go 1.27 race detection and Go 1.25 and distinguishes upload recovery from the later
explicit activation. Restarted CLI helpers and the remaining dashboard/shipping gates are
tracked separately.

## Go SDK work

The [Go SDK implementation and qualification status](go-sdk-status.md) records the
independent management/worker modules, source verification commands, and the
remaining server prerequisites. SDK contract tests must not be reported as
completion of the planned v2 management server or external-worker dispatcher.

## Management preparation and SLO request deadlines, 3 October 2026

The next focused author review reproduced ordinary operation readers waiting
past their deadlines on Store, FSM and history locks, cancelled SLO/Prometheus
requests returning successful observations, preparation losing cancellation,
and a slow queue-statistics capture blocking the shared cursor metadata mutex.

Operation and retained collection-result wrappers, mutation preparation and
submission diagnostics, dependency/reverse-edge validation, and SLO aggregation
now carry the caller context. SLO reads cover the recorder and tagged worker
schedule/FSM locks and discard incomplete views. Queue/system capture and
encoding run outside cursor metadata. Cleanup still releases its owned
reservations after cancellation; it does not abandon them because the request
ended. An unconfirmed admitted mutation keeps its operation identity and unknown
outcome. A confirmed result stays confirmed if optional action enrichment ends.

The broader run caught a regression in this correction: administrative operation
inspection was incorrectly gated by runtime-admission health. Native operation
reads now observe the original diagnostic policy under context-bearing
Store/FSM locks. The focused race regression and later selected profiles pass.
An invalid new log fixture was corrected. A new pause-exposure test's exact
wall-clock floating sum was replaced with a deterministic slot-boundary fixture;
its four full SLO compiler/build profiles pass. The failed attempts are retained.

Passing affected package profiles:

| Compiler | Build | Instrumentation | HTTP top / child tests | Selected persistence top / child tests | Selected management top / child tests | Full SLO tests |
| --- | --- | --- | --- | --- | --- | --- |
| Go 1.27.1 | Default | Race | 133 / 149 | 196 / 446 | 83 / 222 | 8 |
| Go 1.27.1 | `externaljobs` | Race | 184 / 227 | 222 / 499 | 88 / 225 | 8 |
| Go 1.25.0 | Default | Ordinary | 133 / 149 | 196 / 446 | 83 / 222 | 8 |
| Go 1.25.0 | `externaljobs` | Ordinary | 184 / 227 | 222 / 499 | 88 / 225 | 8 |

HTTP and SLO run in full; persistence/management use the retained selection.
Two larger staged cases passed in the earlier broad attempt, whose overall
status failed; they are excluded from the final passing selection. Browser
fixtures and parent-only process helpers remain explicit skips. Both affected
vet variants and all four application/CLI compile checks pass. These compile
checks execute no application tests.

All 1,988 inputs stayed unchanged through the original matrix. Only the new SLO
test fixture changed before the corrected release-compiler runs and four full
SLO repeats; production inputs stayed fixed. This correction changes 24 existing
production files and six test files. It adds no package and changes no SDK,
schema, serializable field or storage format. The private
[management context review record](../review/local-evidence.md#record-f9a60baea4b3c117616b747f) retains exact source
captures, the task diff, terminal statuses and raw output.

This is author review of unfrozen Linux amd64 development source with the
explicit SDK workspace. Native filesystem calls remain cooperative cancellation
boundaries, and queue/pool/system Stats interfaces remain synchronous. It does
not establish hard producer deadlines, independent sign-off or a complete
frozen release. The complete accumulated-source review, final packaged/browser
and downloaded-module matrix, native/Helm/A2A/provider/comparative evidence,
rendered documentation and publication gates remain open. Candidate versions
stay `v0.1.0-rc.1` (chart `0.1.0-rc.1`), private until qualification. No publication,
provider campaign, benchmark or endurance run was performed.

## A2A monitor admission and fixture cleanup, 3 October 2026

The retained earlier failure occurred in `Monitors.Create` for a disabled
escalation monitor, before enablement. Its output retained only “mutation outcome
is uncertain”; it did not retain the underlying cause, HTTP status or received
operation handle. Its cause remains unresolved. The new checks do not establish
that the earlier failure was a deadline, storage stall or provider failure.

Eight new application tests commit a disabled monitor through the real TLS/Raft
management path, then lose response headers or the response body. They cover
cancellation and deadline expiry for both A2A intervention and notification
configuration. The SDK preserves uncertainty and any handle actually received,
sends one create, and dispatches no checks or actions before or after restart.
Read-only reconciliation verifies the same committed resource version. The SDK
guide explains that a missing handle does not prove non-admission. Escalation
failure diagnostics now retain elapsed admission and provider counters without
headers, bodies or credentials.

The full default application/controller race profile passes on Go 1.27.1:
38 application top-level tests, 55 application child tests and 19 controller
tests. Seven browser scenarios and a parent-only controller helper are explicit
skips. Focused admission/escalation profiles pass on Go 1.27.1 with
`externaljobs` and on Go 1.25.0 in both builds: two top-level tests and fourteen
child cases per profile. Both application vet variants pass.

A broader tagged application attempt exceeded its 12-minute package budget
during worker capacity testing. Its A2A cases passed, but the entire attempt is
failed, not complete worker qualification. The timeout left one owned fixture
server alive; its PID/start identity was checked before termination. Campaign
capture now terminates its process group on every exit, including a test panic.
A real subprocess regression covers successful and failed parent exits; the
27 worker-evidence tests pass. The first cleanup assertion sampled before signal
delivery; bounded polling and a procfs disappearance guard correct the test.
CI splits A2A checks into bounded groups and explicitly includes both admission
builds. Workflow lint passes; this is not a remote CI result.

All Go inputs remain unchanged across the qualified profiles. The later changes
are workflow and Python cleanup code, separately checked. Exact source captures,
terminal records, failed attempts and the original failure timeline are retained
in the private [A2A admission review record](../review/local-evidence.md#record-a060fb74aa7e2a2345d4b533).
This is Linux amd64 author review using the development SDK workspace and local
fixtures. It does not freeze the candidate, complete independent review, qualify
the full tagged worker campaign or prove configured-agent effects. The complete
release gates remain open. Application/SDK/worker stay `v0.1.0-rc.1` and the chart
`0.1.0-rc.1`, private until qualification. No publication, provider campaign,
comparative benchmark or 24-hour run was performed.

## External-worker process and load review, 3 October 2026

The dedicated worker selection now passes on the current development source for
both compilers. Each profile runs all 37 crash/capacity cases and the five-minute
mixed-load case, with no skips. Go 1.27.1 instruments both the application and
worker processes with the race detector; Go 1.25.0 uses ordinary test builds.
The original combined application timeout remains a failed attempt. The later
dedicated run also passes the recovery capacity case that was active at that
timeout; it does not diagnose the original A2A admission failure.

| Compiler | Instrumentation | Crash/capacity cases | Measured load | Independent check / recovery / notification invocations | Retired check receipts verified after restart |
| --- | --- | --- | --- | --- | --- |
| Go 1.27.1 | Race in both processes | 37 | 300.001 seconds | 1,428 / 358 / 388 | 405 |
| Go 1.25.0 | Ordinary | 37 | 300.000 seconds | 1,480 / 371 / 401 | 456 |

Both load cases reach 16 concurrent handlers at the default journal limits and
make progress in all three categories. They drain records, active handlers,
pending outcomes, unknown actions and reserved/live bytes to zero. Independent
target effects reconcile with original committed outcomes and receipts; duplicate
invocations and unreconciled outcomes are zero. The fault cases separately
retain unknown actions across restart and preserve lock ownership while an
uncooperative handler remains active. These invocations include setup and drain;
they are not comparative throughput measurements.

The raw-result inspector confirms exact case coverage and matching helper
compiler/instrumentation. All 1,990 recorded source inputs stay unchanged across
both profiles. Selected worker execution, journal accounting and evidence paths
receive author review without a new runtime defect or application change. The
private [worker process and load review record](../review/local-evidence.md#record-ff596b7e08e6e91e7a92b13b)
retains exact source sections, original terminal records, compressed output,
compiler identities and the earlier failed attempt's identity. Documentation
verification is recorded separately.

This is native Linux amd64 development verification using the explicit SDK
workspace, test executables and synthetic local targets. Capacity/load fixtures
seed encrypted configuration in a stopped store; ordinary crash cases create
monitors through the public SDK. It does not complete frozen-module, packaged,
public-consumer, independent-review or configured-provider/agent qualification.
The full accumulated-source review and final candidate matrix, native/package
and Helm operations, reproducibility, fresh all-35 provider-role evidence,
comparative scenarios, final-commit documentation and publication remain open.
Application/SDK/worker remain `v0.1.0-rc.1`, chart `0.1.0-rc.1`, private until
qualification. No publication, fleet benchmark or 24-hour run was performed.

## Native installer and package lifecycle review, 3 October 2026

The local installer now validates and installs one private executable copy.
Previously it validated the caller's path and reopened that path later; both
fresh installation and running update reproduced installation of changed,
unvalidated contents. The copy is removed after success or failure, and cleanup
errors are reported. Linux tests cover both source-change paths, rejected
validation, cleanup and supervisor command ordering without controlling a host
service.

The macOS permission repair now preserves operator credential-helper modes,
repairs only managed configuration/executable objects, and rejects linked or
non-regular state/log entries. New macOS regression cases compile for amd64 and
arm64 under both compilers. They still require execution on native macOS.

| Profile | Instrumentation | Top-level tests passed | Nested cases passed |
| --- | --- | --- | --- |
| Go 1.27.1, default | Race | 31 | 60 |
| Go 1.27.1, `externaljobs` | Race | 42 | 74 |
| Go 1.25.0, default | Ordinary | 31 | 60 |
| Go 1.25.0, `externaljobs` | Ordinary | 42 | 74 |

The only top-level skips are helper functions invoked by the owning separate
process tests. Sixty-nine package-hook/native-harness contracts pass. Release
compiler cross-builds cover local administration test executables for all six
archive targets; both Mac targets are rebuilt after the permission change with
both compilers. Cross-building does not qualify native service, filesystem,
durability or package-manager behavior. Selected Linux vet checks pass.

The private [native installer review record](../review/local-evidence.md#record-22d0ed3c4b7a10e8540eb70c)
retains the reproduced failures, corrected fixture, four profiles, build hashes,
31 selected review inputs and the full remaining gate list. This is author
review on uncommitted development source using the explicit SDK workspace.
Complete accumulated-source and independent review, candidate freeze, frozen
application/SDK/worker/dashboard/browser/security and package checks, public
module consumers, actual platform/Helm operations, reproducibility, configured
A2A effects, fresh all-35 provider evidence, comparative scenarios, final-commit
documentation and publication remain open. Application/SDK/worker remain
`v0.1.0-rc.1`, chart `0.1.0-rc.1`, private until all agreed gates pass. The
24-hour campaign remains excluded.

## Private publication decision and admission review, 3 October 2026

The release assessment now has a separate pre-publication decision. It rechecks
all fourteen original component readers and detached independent-review
signatures under an operator-pinned trust policy. Missing or changed evidence,
another policy, revoked reviewers, changes requested and unresolved findings
keep admission closed. An ordinary assessment cannot admit publication, and a
stored ready summary is never an admission credential.

Direct protected publisher phases require the original component configuration,
the operator policy and its protected-environment digest. The selected
distribution, OCI assembly and dependency cache must match those inspected
inputs. Retained fingerprints are checked again before the public phase. The
private workflow's existing policy remains locked; its private-to-public
integration and actual frozen execution remain open.

All 701 release-tool contract tests pass without skips. Positive admission cases
use controlled prerequisite-reader outcomes and actual disposable offline
OpenSSH signatures. Other contracts include real local Git, HTTP and process
fixtures, with controlled compiler/provider/signing replies where stated in
their test sources. They do not execute the fourteen release campaigns or
approve this candidate. Earlier missing-implementation failures and two test
expectation corrections remain retained.

The [private decision review record](../review/local-evidence.md#record-0b0cbe61fa7c072a82cf1d98)
captures source identities, limits, raw results and the full remaining goal.
This is author review of development source. Complete accumulated-source and
independent review, final freeze and application/SDK/worker/dashboard/security
checks, public consumers, actual platform/package/Helm operations,
reproducibility, configured A2A and all-35 provider evidence, comparative
scenarios, final-commit documentation and publication remain open. Candidate
versions stay `v0.1.0-rc.1`, with chart `0.1.0-rc.1`; the 24-hour test remains
outside the task. The private CI location has been requested while local work
continues; no new repository or public object was created.

## Module input preparation review, 4 October 2026

Committed-source qualification caught two preparation defects. The SDK archive
from `c72a499` differed from the recorded dependent checksum because its README
had gained ambiguity guidance. A Go-generated checksum correction was saved in
local commit `62e1560`. The next frozen check then reported additions to all
three `go.sum` files when downloading the full dependency graph; its source
archive was left unchanged and the failure was retained.

Working-tree preparation now completes `tidy` and the full graph download before
archiving each module, starting with the SDK. Refreshing a changed unpublished
SDK checksum requires the explicit `--refresh-sdk-checksum` option. It removes
only the stale content row from copied dependent files; Go regenerates that row,
and the original entries and proposals remain available for review. Frozen
preparation retains its ordinary checksum and source-immutability checks.

All 48 SDK-tool contract tests pass. The new controlled-graph regression checks
final archive contents, explicit refresh and preservation of the original
checkout. Actual Go 1.27.1 preparation also completed twenty commands covering
all three module graphs, cache verification and source-derived SDK/worker
downloads. It proposed changes only to the three checksum files, with no module
declaration or version change. The proposals identify SDK content as
`h1:dloPu/idkVjrLQa2Ocp9oJX8ctJFMwFO2mQOYUzahk8=`.

These results qualify preparation behavior. Committed-source cache and both
compiler consumer checks must run again after the reviewed proposals are saved;
complete candidate, independent review, platform/provider, performance and
publication gates remain open. No public object was created and no 24-hour run
was performed.
