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
format 2 pins worker UID and uses reconciliation-only recovery. Public external
configuration activation and the server execution protocol remain unfinished.
Normal startup still registers no worker execution routes. The
[session contract](worker-session-protocol.md) distinguishes these boundaries.
The server also has [queued execution admission and private preparation](worker-execution-admission.md)
under tagged storage format 20. These retain original source/contract identities
and encrypted payloads; they do not establish SDK/worker execution interoperability.
The later [format-21 checkpoint](worker-offers-and-start.md) connects bounded
offers and one-time Start grants to the actual SDK through a private TLS test mux.
Startup authenticates retained execution payloads. Heartbeat/result/late-evidence
handling and full runner interoperability remain unfinished.

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
cache. It tests module archive consumption with no workspace or replacement and
does not put an unpublished candidate in the normal module cache. The second
command actually downloads the published versions and must not be reported as
passing before they exist. Both commands compare all downloaded archive names and
file contents to the explicitly selected candidate sources. Extra or changed
downloaded files fail the check. Private candidate construction is not a claim
that a public Go proxy has already produced the same archive. Neither command
qualifies missing server behavior.

`python3 scripts/sdk/regenerate_check.py --go /path/to/go` regenerates the public
models, transport, embedded schemas, and operation inventory in two separate
temporary source roots and compares both against the checked-in outputs.

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

Provider-account certification and the million-monitor endurance campaign remain
separate gates. This document does not declare them completed.

## Implementation status

| Ticket | Implemented in this branch | Remaining acceptance gates |
| --- | --- | --- |
| 1. Public contract | Independent Go 1.25 module; leaf public types; generated internal transport; 33 driver shapes; generated operation inventory (71 base operations and 11 tagged extensions) | Match and qualify the actual v2 server contract before publication |
| 2. API clients | CRUD, patch, controls, observations, pagination, errors, transport policy; scoped real-server management and JobType interoperability | Complete final operation inventory and mixed-version qualification; external execution remains pending |
| 3. Collections | Frozen bounded sources; preflight/diff/upload; asynchronous validation; conditional activation; bounded retained results; original continuation and CLI/SDK reselection | Complete maximum-input and final release qualification; preserve scoped evidence boundaries in current status |
| 4. Build exclusion | Default/tagged types, schemas, transports and methods; separate worker module; negative consumer compilation; server JobType route exclusion, tagged worker policy/local administration/authentication, and explicit runtime opt-in | Worker execution route enforcement and complete protocol integration |
| 5. Worker library | Encrypted journal, reserve/start/outcome/receipt lifecycle, conservative recovery, late evidence, limits, cooperative shutdown | Interoperate with real external-worker server; native filesystem qualification beyond Linux amd64 |
| 6. Consumers and publication | `cpractl` uses the current public SDK for reads and writes; examples; multi-module/native CI entry points; archive and generation checks | Public prerelease tags, actual downloaded-release qualification, signatures/notices, compatibility certification |

The implementation and review evidence is recorded in
[Go SDK verification](go-sdk-verification.md). No SDK tags, application release,
or documentation deployment were published by this work.

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
CLI apply/validation, downloaded publication, provider-account and endurance
gates remain separately open.
