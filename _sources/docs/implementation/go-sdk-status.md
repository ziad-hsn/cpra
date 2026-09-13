---
title: Plan and status · Go SDK implementation and qualification
description: Plan and status · Go SDK implementation and qualification for the reviewed CPRa source; see the version and availability notice.
cpra_scope: plan
---

> **Candidate design and evidence:** this record describes unreleased implementation work or an approved plan. Its checklists do not establish availability in `main`. See [version and availability](../versions.md).

# Go SDK implementation and qualification

The SDK is developed as two modules in this repository: `sdk/go` for management
and observations, and `sdk/go/worker` for the optional worker runner. Both target
Go 1.25. SDK versions are independent of the CPRa application version and the
HTTP API version.

## Server prerequisite

The checked-in server currently implements the read-only v1 API. The
[management API plan](api-management-plan.md) still owns durable v2 configuration,
named write authorization, owner-loop reconciliation, resumable server operations,
and the external-worker dispatcher. SDK methods and their HTTP contract fixtures
do not implement those server facilities.

The SDK's explicit `legacy` client is tested against the existing server handlers.
The v2 and worker contracts must pass tests against a real completed server before
SDK prerelease publication. A contract fixture is useful for serialization,
transport and worker failure behavior; it does not establish server durability,
provider delivery, or a complete management release.

## Distribution and development

The initial candidate version is `v0.1.0-rc.1`. Its module tags will be
`sdk/go/v0.1.0-rc.1` and `sdk/go/worker/v0.1.0-rc.1`. They are not published by
ordinary CI. Publish the SDK dependency before any application tag requiring it;
the published application and SDK modules must not contain local replacements.

Until publication, use an explicit temporary Go workspace to develop the three
modules together:

```sh
python3 scripts/sdk/workspace.py --output /tmp/cpra-sdk.work
GOWORK=/tmp/cpra-sdk.work make sdk-check
GOWORK=/tmp/cpra-sdk.work go test ./internal/cpractl/cli ./internal/web/server
```

Release builds intentionally keep `GOWORK=off`. A source workspace is not a
substitute for the downloaded-module release gate.

An ordinary root build without this workspace currently cannot resolve the
unpublished SDK requirement. This branch is an integration candidate, not a
publishable application release. Do not add a local `replace` to hide that gate.

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
| 1. Public contract | Independent Go 1.25 module; leaf public types; generated internal transport; 33 driver shapes; 66-operation draft inventory | Match and qualify the actual v2 server contract before publication |
| 2. API clients | CRUD, patch, controls, observations, pagination, errors, transport policy, explicit legacy client | Real v2 authorization, CAS, and management interoperability |
| 3. Collections | Frozen bounded inputs, streaming YAML/JSON/JSON Lines, legacy normalization, preflight/diff, upload/activation/resume | Server-side encrypted staging and conditional activation; wire the shared input package into future `cpractl apply` |
| 4. Build exclusion | Default/tagged public types, schemas, transports, methods; separate worker module; negative consumer compilation | Implement and test server build exclusion, runtime enablement, and scoped authorization |
| 5. Worker library | Encrypted journal, reserve/start/outcome/receipt lifecycle, conservative recovery, late evidence, limits, cooperative shutdown | Interoperate with real external-worker server; native filesystem qualification beyond Linux amd64 |
| 6. Consumers and publication | Existing `cpractl` reads use public legacy SDK; examples; multi-module/native CI entry points; archive and generation checks | Public prerelease tags, actual downloaded-release qualification, signatures/notices, compatibility certification |

The implementation and review evidence is recorded in
[Go SDK verification](go-sdk-verification.md). No SDK tags, application release,
were published by the SDK implementation work. These docs are now published separately; SDK and server release gates remain open.

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
