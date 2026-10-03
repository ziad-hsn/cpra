---
title: Plan and status · Go SDK implementation and qualification
description: Plan and status · Go SDK implementation and qualification for the reviewed CPRa source; see the version and availability notice.
cpra_scope: plan
---

> **Candidate design and evidence:** this record describes unreleased implementation work or an approved plan. Its checklists do not establish availability in `main`. [Version and availability](../versions.md) identifies the earlier 13 September reviewed snapshots; [current implementation status](STATUS.md) records the later branch checkpoints.

# Go SDK implementation and qualification

The SDK is developed as two modules in this repository: `sdk/go` for management
and observations, and `sdk/go/worker` for the optional worker runner. Both target
Go 1.25. SDK versions are independent of the CPRa application version and the
HTTP API version.

## Server prerequisite

Normal application startup on the private `codex/dashboard-finalization` working
branch now integrates named v2 authorization, encrypted Raft configuration and
owner-loop reconciliation. Resource CRUD, controls, action review, operation
reads and observations have real TLS/public-SDK tests; selected embedded-browser
scenarios exercise normal startup and restart. See the
[progress record](dashboard-implementation-progress.md) for exact evidence and
asset identities. Earlier published servers are not qualified by this work.

The [management API plan](api-management-plan.md) remains authoritative.
Conditional collection activation, retained-result reads, original-operation
continuation and restarted CLI input reselection now have connected scoped
evidence, recorded in [current status](STATUS.md). Tagged JobType list/create/
get/replace/delete also run against the production TLS/Raft handlers, with
version guards, retained configuration receipts and explicit runtime enablement.
See the [JobType contract](job-types.md). Scoped worker grants and
[stopped local provisioning](../worker-authentication.md) are implemented;
encrypted configuration records also authenticate pinned JobType references.
Tagged session ownership now commits to Raft, and a test-only mux qualifies the
session HTTP adapter. The SDK checks session/Start identity echoes; journal
format 2 pins worker UID and uses reconciliation-only recovery. The later
[public external-configuration checkpoint](external-configuration.md) connects
API changes, file/URL collection validation and bootstrap to retained JobTypes.
Its 24 focused tests pass on each compiler; complete candidate qualification and
review remain open.
Normal startup now registers the explicitly enabled [worker HTTP routes](worker-http.md),
including a separate completion barrier during shutdown. The
[session contract](worker-session-protocol.md) distinguishes these boundaries.
The server also has [queued execution admission and private preparation](worker-execution-admission.md)
under tagged storage format 20. These retain original source/contract identities
and encrypted payloads; they do not establish SDK/worker execution interoperability.
The later [format-21 checkpoint](worker-offers-and-start.md) connects bounded
offers and one-time Start grants to the actual SDK through a private TLS test mux.
Startup authenticates retained execution payloads. Later private checkpoints add
[result commits and receipts](worker-result-receipts.md) and
[heartbeats and late evidence](worker-heartbeats-and-evidence.md), with actual SDK,
TLS and Raft coverage. The [format-24 checkpoint](worker-finalization.md) also
qualifies automatic finalization and original receipts through the SDK.
Admitted-check SLO measurements now use [post-commit receipt confirmation](worker-slo.md).
The [scheduling checkpoint](worker-scheduling.md) adds successive checks with
retained original receipts and safe unstarted requeue. SDK/TLS/Raft tests cover
successive-check receipt replay after log and snapshot recovery.
The private [check-retention implementation](worker-check-retention.md) adds
explicit `workerCheckExpired` responses and the tagged SDK sentinel
`ErrWorkerCheckExpired`; the worker removes only check replay records after that
disposition. Scoped retention tests pass on both supported test compilers;
independent review and full integration remain open.
The subsequent [controller dispatcher](worker-dispatch.md) and
[action-expiry transition](worker-expiry.md) are connected. The subsequent
[schedule-accounting checkpoint](worker-schedule-accounting.md) counts ready and
preparing work without duplicating admitted obligations. A later
[separate-process campaign](worker-dispatch.md#separate-process-verification)
connects the real SDK runner to normal application startup for all three handler
categories. Earlier partial campaigns retain their interrupted-start and report
failures. The later 37-case campaign and five-minute sustained-load campaign pass
on both compilers after the journal-accounting correction, including race detection
on Go 1.27.1. Those reports use stopped configuration fixtures. The ordinary
process cases now create monitors through the public SDK, and all 37 refreshed
scenarios pass on both compilers, including race detection on Go 1.27.1.
Frozen downloaded-module installation, complete candidate
qualification and independent review remain open.

Tagged `Client.Workers` and `Client.IterateWorkers` now use the production
[worker observation endpoint](worker-status.md). Status pages retain one capture
time and authorization-bound cursor; missing poll times and expired advertised
capacity remain explicitly unavailable. These read-only observations do not
establish controller dispatch or remote handler execution.

The agreed delivery also includes cpractl and a writable dashboard. The
[dashboard management plan](dashboard-management-plan.md) records their shared
Recipient, group-routing, and credential requirements. Extend the public schema,
SDK services, operation inventory, and real-server tests together when those
contracts are implemented. The shared Recipient models, CRUD/list methods and
collection reference handling now exist in the SDK. Shared-resource CRUD,
controls, action review and explicit collection import/continuation browser flows
have local integration evidence. Full shipping qualification remains open.

The SDK and CLI use one current management and observation client.
The v2 and worker contracts must pass tests against a real completed server before
SDK prerelease publication. A contract fixture is useful for serialization,
transport and worker failure behavior; it does not establish server durability,
provider delivery, or a complete management release.

## Distribution and development

The initial candidate version is `v0.1.0-rc.1`. Its module tags will be
`sdk/go/v0.1.0-rc.1` and `sdk/go/worker/v0.1.0-rc.1`. They are not published by
ordinary CI. Publish the SDK dependency before any application tag requiring it;
the published application and SDK modules must not contain local replacements.

Normal `make build`, `make build-ctl`, root test/vet targets and `make sdk-check`
create and use an ignored `bin/cpra-sdk.work` for the application, SDK and worker.
The example module is excluded. Explicit `GOWORK` settings, including `off`, take
precedence; Make neither rewrites a selected custom workspace nor exports its
implicit workspace to later GitHub Actions steps. Parallel build targets share
one setup step.

Direct Go commands can use that workspace or an explicitly generated temporary
workspace:

```sh
python3 scripts/sdk/workspace.py --output /tmp/cpra-sdk.work
GOWORK=/tmp/cpra-sdk.work make sdk-check
GOWORK=/tmp/cpra-sdk.work go test ./internal/cpractl/cli ./internal/httpserver
```

Release builds intentionally keep `GOWORK=off`. A source workspace is not a
substitute for the downloaded-module release gate.

A direct root Go build with `GOWORK=off` cannot yet resolve the unpublished SDK
requirement. This branch is an integration candidate, not a publishable
application release. Do not add a local `replace` to hide that gate.

```sh
python3 scripts/sdk/verify.py --out evidence/local/sdk-consumer.json
python3 scripts/sdk/verify.py --published --out evidence/local/sdk-published.json
```

The first command stages a private local module proxy and uses a temporary module
cache from a clean committed checkout. It tests module archive consumption with no workspace or replacement and
does not put an unpublished candidate in the normal module cache. The second
command actually downloads the published versions and must not be reported as
passing before they exist. Both commands compare all downloaded archive names and
file contents to the explicitly selected candidate sources. Extra or changed
downloaded files fail the check. Private candidate construction is not a claim
that a public Go proxy has already produced the same archive. Neither command
qualifies missing server behavior.

### Module archive checkpoint

The local verifier now selects source through `git archive` and delegates module
selection and content hashing to the isolated `tools/modulezip` tool, using
`golang.org/x/mod` v0.38.0. Each nested module is staged separately. Ignored and
untracked workstation files are excluded by committed-source selection; valid
committed files are not filtered by extension. Generated release metadata is
excluded by the application source-install harness.

Use `--sdk-version` and `--worker-version` to select independently versioned
payloads; both currently default to `v0.1.0-rc.1`. The worker's `go.mod` must require
the selected SDK version. Go working directories and caches are temporary, ambient
workspace/compiler settings are discarded, and public verification uses the public
proxy and checksum database without private exceptions. Existing staged versions
cannot be overwritten. Source and archived `go.mod` bytes must agree before any
version files are staged.

Python tests reproduce and reject a source change between archive creation and
metadata staging, verify source-bundle inventories, and use real Git/archive
fixtures for committed-source selection. Their archiver subprocess is a controlled
fixture. The subsequent [30 September Go execution](../review/local-evidence.md#record-71a5707e0af719e544efd054)
compiles and passes all three canonical-selection, invalid-input and command-report
tests on Go 1.27.1 with race detection and Go 1.25.0. This executes local filesystem
fixtures; no current candidate archive download or installation pass is claimed.
The [earlier Python report](../review/local-evidence.md#record-2ef4fd3f4b97810d56734741)
remains separate evidence for committed-source selection. Version freezing, private dependency-cache bootstrap,
actual installation on both compilers, public retrieval and independent review
remain open.

### Private dependency cache checkpoint

The local `scripts/release/dependencies.py` command now prepares the two private
modules and their dependencies from an inventoried source archive. It uses a new
private directory, records Go-reported hashes and graph identities, rejects
replacements and unexpected private versions, and restores the release checksum
policy before verification. Any source change during resolution prevents a pass;
the changed scratch files remain available for review and a later source freeze.
The working checkout is not rewritten.

Release builds and both extracted-source rebuilds accept `--dependencies` with
source-bound cache validation. Rebuilds reject a source archive from another
release identity and use separate compiler caches. Source validation includes
archived executable modes and Unix permission changes during preparation. A
missing source archive produces a failed preparation report.
The private release workflow prepares caches before application builds, native
correctness tests and both compiler-compatibility jobs; it exports only cache
locations, without propagating checksum exceptions or a workspace. Compatibility
tests explicitly include the application, SDK and worker modules.
Dependency reports, logs and proposed module-file changes are retained as private
workflow artifacts even when preparation fails.

The [scoped report](../review/local-evidence.md#record-9c804275daea5ceceb2b277c)
covers Python orchestration, real small archive/filesystem fixtures and the actual
workflow environment-export statements. Go metadata, downloads, compilation and
artifact comparisons are controlled fixtures in these tests. Complete bootstrap,
exact-recipe builds, platform behavior and independent review remain unqualified.
The later module-archive checkpoint records execution of the isolated Go helper.
Version/checksum freezing and public module retrieval
are still required; publication remains locked.

### Pre-freeze module preparation

The [1 October checkpoint](../review/local-evidence.md#record-4e4bf9fdb00d49b2193f12cf)
executes the preparation command against captured current source rather than
controlled Go replies. The leaf SDK is staged with Go's module ZIP rules;
actual Go dependency resolution generates four application/worker module-file
proposals. The SDK sums and required declarations are applied after checking the
original and proposed hashes. SDK source bytes still match the resolved payload.
Go 1.25 reports no differences from the Go 1.27.1 module files.

Both compiler consumer campaigns run with `GOWORK=off` and no replacements.
They download the two modules from a private proxy, execute all documented
examples, render seven package-documentation variants, verify default dependency
exclusion and exercise tagged and forbidden untagged APIs. The entire downloaded
modules also pass 186 default SDK, 203 tagged SDK and 40 worker top-level tests
per compiler. Including subtests, the two matrices contain 4,148 pass events.
No test case skips; the generated transport package's `[no test files]` completion
is recorded explicitly. Both CLI source installations use normal checksum policy
and the captured directory has no Git checkout, Node or generated dependencies.
The installed executables' help, version, build settings and module identity are
retained. Source and downloaded module inputs remain unchanged during execution.

The initial minimum-compiler campaign rejected different ZIP compression bytes.
Both actual canonical content hashes match; the verifier now requires exact ZIP
bytes only under the same compiler recipe and binds each download to its own
staged payload. The initial package-completion wrappers also rejected the
generated transport's no-test-files status; their failures remain preserved.
No skipped test is reclassified as a passing case.

These reports describe working-tree dependency preparation and private consumer
execution, not a reviewed frozen candidate or public module availability. The
release evidence reader rejects their evidence class. Freeze the complete version
graph and rerun the ordinary committed-source/archive gate before publication;
public download verification follows the qualified publication step. See the
[preparation procedure](../sdk/publishing.md#prepare-dependency-changes-before-freezing-source).

### Retained consumer inputs

The SDK consumer verifier now preserves both source-derived and downloaded
module archives and their `.mod`/`.info` metadata beside an exclusively created
report. Retention is bounded and uses private Unix permissions. Earlier outputs
are not overwritten, and a failed campaign preserves its available inputs.
Dependency preparation also records the application/SDK/worker requirements
used to select those independently versioned modules.

The new `scripts/release/sdk_evidence.py` reader checks the actual retained bytes
against the frozen source inventory, canonical dependency archives and both
compiler reports. It checks all distributed files, version/checksum agreement,
documentation/example records and input stability; metadata alone is insufficient.
The private release workflow now retains and reads those payloads after both
compiler jobs, using temporary paths outside the committed checkout. Ordinary
CI payload uploads remain restricted to private repositories.

The [30 September scoped record](../review/local-evidence.md#record-bd65f18d3bc023f603f4473a)
covers report/payload corruption, source and version mismatches, missing files,
compiler coverage, workflow arguments, private storage and failure preservation.
Its module payloads and compiler metadata are test fixtures. It does not report
actual current-candidate downloads, source installs, server interoperability,
independent review or completed publication. See the
[consumer procedure](../sdk/publishing.md) for the actual candidate commands.

`python3 scripts/sdk/regenerate_check.py --go /path/to/go` regenerates the public
models, transport, embedded schemas, and operation inventory in two separate
temporary source roots and compares both against the checked-in outputs.

### Complete module matrix checkpoint

The [30 September package campaign](../review/local-evidence.md#record-c74d0a65a8192831ab48848c)
executes every package in the SDK's default and `externaljobs` builds, plus the
tagged worker module, on Go 1.27.1 and Go 1.25.0. Each compiler passes 186 default
SDK tests, 203 tagged SDK tests and 38 worker tests. Go 1.27.1 uses race detection;
no test skips, no package completion is missing and the captured source inputs
remain unchanged.

The campaign uses the explicitly recorded development workspace. It is separate
from the frozen module/cache installation and later public-download gates. It
does not prove the complete server contract, independent review, provider-account
effects or publication readiness. The
[static campaign](../review/local-evidence.md#record-888cf0e434e6b7b9055d7b76)
also records passing SDK/worker vet on both compilers and release-compiler source
vulnerability scans; it retains the same scope and qualification limits.

## Optional jobs and evidence boundaries

Normal SDK consumers have no custom-job public methods or worker dependency.
`externaljobs` enables the optional protocol; the worker runner is a separate
module whose implementation files also require that build tag. Default CPRa
artifacts, including all-built-in-driver builds, do not implicitly enable it.

Go source archives still contain the relevant open-source tagged files. Build
constraints exclude compilation, not source visibility. The server must enforce
route exclusion and worker authorization independently of the client.

The worker runs operator-owned handlers in a separate process. Its encrypted
journal, local provider credentials, identity and storage are separate from
CPRa's. Cancellation is cooperative; the application supervisor must terminate
an uncooperative process. Restart must never repeat an uncertain external action.

## Publication gates

The [integrated shipping plan](dashboard-shipping-plan.md) requires candidates
to remain private until all agreed pre-publication gates pass. This includes
SDK prerelease tags: they are not a workaround for unpublished dependencies.
Real public download checks occur only after their corresponding qualified tags
exist; private archive checks must retain their distinct evidence classification.

The [package documentation and publishing guide](../sdk/publishing.md) specifies
module archive contents, executable examples, nested-module release order, and
the hosted documentation boundary for `externaljobs`. The archive consumer check
also renders package overviews and runs documentation examples from downloaded
modules with `GOWORK=off`; a passing local report does not publish a version.

- Independent review and all SDK, collection and worker correctness checks.
- Reproducible OpenAPI/model/transport generation with recorded tool versions.
- Default/tagged public-interface and dependency exclusion checks.
- Actual server v2 authorization, conditional writes, durable apply and worker
  execution/finalization/late-evidence acceptance tests.
- Native worker filesystem, locking, encryption and process-crash checks for
  each claimed platform; cross-compilation is recorded separately.
- Downloaded prerelease verification before `v0.1.0` publication.

Provider-account verification and the agreed comparative performance checks
remain separate release gates. The canceled 24-hour run is outside the requested
work. This document does not declare the remaining gates completed.

## Implementation status

| Ticket | Implemented in this branch | Remaining acceptance gates |
| --- | --- | --- |
| 1. Public contract | Independent Go 1.25 module; leaf public types; generated internal transport; 33 driver shapes; generated operation inventory (71 base operations and 11 tagged extensions) | Match and qualify the actual v2 server contract before publication |
| 2. API clients | CRUD, patch, controls, observations, pagination, errors, transport policy; scoped real-server management and JobType interoperability | Complete final operation inventory and mixed-version qualification; external execution remains pending |
| 3. Collections | Frozen bounded sources; preflight/diff/upload; asynchronous validation; conditional activation; bounded retained results; original continuation and CLI/SDK reselection | Complete maximum-input and final release qualification; preserve scoped evidence boundaries in current status |
| 4. Build exclusion | Default/tagged types, schemas, transports and methods; separate worker module; negative consumer compilation; production worker route exclusion, tagged worker policy/local administration/authentication, and explicit runtime opt-in | Complete controller/protocol integration and final build matrix |
| 5. Worker library | Encrypted journal, reserve/start/outcome/receipt lifecycle, conservative recovery, late evidence, limits, cooperative shutdown | Interoperate with real external-worker server; native filesystem qualification beyond Linux amd64 |
| 6. Consumers and publication | `cpractl` uses the current public SDK for reads and writes; examples; multi-module/native CI entry points; archive and generation checks | Public prerelease tags, actual downloaded-release qualification, signatures/notices, compatibility certification |

The implementation and review evidence is recorded in
[Go SDK verification](go-sdk-verification.md). No SDK tags or application release
were published by the SDK implementation work. Candidate documentation is
published separately for review; SDK and server release gates remain open.

## Service integration examples

`examples/sdk` is a separate Go 1.25 module with four runnable lessons: mock
queue registration, AWS deregistration, all Services in a Kubernetes namespace,
and a tagged DAO RPC/internal SMS worker. Its AWS/Kubernetes dependency versions
are included only when `scripts/sdk/workspace.py --examples` is requested.

The [SDK guide](../sdk/index.md) includes a complete operation/type/Go reference
and links to each lesson. [Example verification](../sdk/verification.md) records
the observation boundary and repeatable checks. All demos are explicit local
fixtures; configured v2 and external-worker use still requires the corresponding
server contracts above.

## Asynchronous validation client checkpoint

`Operations.Validate` now returns a 202 operation receipt. `Validation` reads a
bounded immutable result page; `WaitValidation` only polls GET and retains the
original operation handle on cancellation. `ValidationItems` iterates lazily with
a pinned result summary. Pending validation is not a failed verdict. The
collection helper requests activation only after a matching successful sealed
summary and original first-page input metadata; it never fetches the full result
fleet implicitly or retries a validation mutation to poll.

The SDK/default and `externaljobs` race suites, both Go 1.25 variants, vet,
two-root generation and examples pass. Exact commands, source identities and the
independent review are recorded in `bin/verification/sdk-async-validation`.
Generated API/wire/Go references and linked example source copies are current.
These SDK results qualify client and fixture behavior. Separately, the normal-main
Chrome/Worker/WASM/TLS/Raft release-Go race scenario passed in 11.608 s with the
same original sealed result after restart, five explicit writes and no activation.
Evidence: `bin/verification/collection-validation-browser/fixed-release-race.json`.
The Go 1.25 campaign also passed in 10.073 s. Parser reproducibility and exact
Vite/embedded-asset equality passed; consolidated evidence is in
`bin/verification/collection-validation-browser/result.json`. No full lifecycle
or release claim is inferred from these scoped passes. Activation, collection listing/reselection,
CLI apply/validation, downloaded publication, provider-account and comparative performance
gates remain separately open.
