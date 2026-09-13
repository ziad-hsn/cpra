---
title: SDK candidate · CPRa SDK examples
description: SDK candidate · CPRa SDK examples for the reviewed CPRa source; see the version and availability notice.
cpra_scope: sdk
---

> **Unpublished SDK candidate:** this package guide reflects the 13 September source snapshot. See [availability and source](../versions.md#go-sdk-and-approved-management-plan) before running candidate commands.

# CPRa SDK examples

Run four small integrations that turn service events into CPRa configuration
changes, then read the code behind each result.

The examples use a separate Go module so AWS and Kubernetes dependencies stay
out of the public SDK. Go 1.25 or later is required. Node, Redis, a Kubernetes
cluster, an AWS account, and SMS credentials are unnecessary for the local demos.

**Current boundary:** CPRa's application server exposes the read-only v1 API.
The v2 management and external-worker contracts are SDK candidates. The finite
demos use clearly labeled local fixtures. Configured modes require a server
that implements and qualifies those contracts; this branch cannot supply one.

## Set up the candidate workspace

From the CPRa repository root, select your installed Go compiler and create a
workspace for the unpublished SDK modules:

```bash
export GOTOOLCHAIN=local
python3 scripts/sdk/workspace.py --examples --output /tmp/cpra-sdk.work
export GOWORK=/tmp/cpra-sdk.work
cd examples/sdk
```

The workspace is development-only. It does not publish module tags or change
`go.mod` with local replacements. The first run downloads each example's public
dependencies. Ordinary `go test ./...` at the repository root does not test this
nested module.

The `--examples` workspace includes this module's AWS and Kubernetes dependency
versions. For application or SDK release checks, create a separate workspace
without `--examples`; example dependencies must not change that build's module
selection.

## Run the examples

| Command, from `examples/sdk` | Observable result | Guide |
| --- | --- | --- |
| `go run ./queue-registration -demo` | Two deliveries acknowledged; one monitor created | [Queue registration](queue-registration.md) |
| `go run ./aws-deregister -demo` | A mapped monitor disabled once; repeated event ignored | [AWS deregistration](aws-deregister.md) |
| `go run ./kubernetes-services -demo` | Five DNS/TCP monitors created from three Services; second listing creates no duplicates | [Kubernetes Services](kubernetes-services.md) |
| `go run -tags=externaljobs ./dao-sms -mode=demo` | DAO RPC checked, stale head detected, one SMS accepted, lost receipt replayed | [DAO and internal SMS](dao-sms.md) |

The Kubernetes example covers **all Services in a selected namespace**. CPRa
must run inside the same cluster to use the generated Service DNS targets. DNS
and TCP observations have different meanings; the guide explains their coverage.

The DAO/SMS directory is absent from the default Go package selection. Its
implementation, schemas, protocol APIs, and worker library require
`externaljobs`. Enabling a client build tag does not grant server permission.

## Learn how to develop an integration

Each lesson follows the same path: run its local demo, inspect the expected result,
read a file/function map, walk through selected Go code, and make a small change
with a focused test. The excerpts link to their complete source files; they are
parts of those programs, not standalone programs to paste into `main`.

| Start here | Code concept to learn | Suggested next change |
| --- | --- | --- |
| Queue registration | Construct a typed monitor, reconcile an uncertain create, and acknowledge a delivery only after reading its committed identity | Change the interval and test changed redelivery |
| AWS deregistration | Translate a provider event into a conditional control request using separate AWS and CPRa clients | Add a new target mapping while retaining account, incarnation, and version guards |
| Kubernetes Services | Reconcile two APIs with separate version systems and a narrow merge patch | Add another TCP port and verify that operator controls survive reconciliation |
| DAO and internal SMS | Register a JobType contract and a matching compiled handler, then return a bounded typed outcome | Add a handler parameter and update its schema, validation, and fixture together |

Your integration imports the public SDK, its `api` types, and `collection` when
applying related resources. AWS/client-go belong to the integration's own module.
The `internal/` helpers here support these examples; an application outside this
module cannot import them. Read their code to understand token loading and fixture
setup, then implement the equivalent policy in your own application.

For your own project, start with `go mod init <your-module-path>`. Once the SDK
version is published, add it with `go get`; do not copy this development workspace
or the examples' cloud dependencies into your library. The
[package and publishing guide](publishing.md) explains the two SDK
module versions, executable documentation examples, and publication gates. Today,
use the workspace above to evaluate the unpublished candidate.

## Verify your edits

```bash
go test ./...
go test -tags=externaljobs ./...
go test -race -tags=externaljobs ./...
go vet -tags=externaljobs ./...
```

From the repository root, `make sdk-examples-check` runs tests, vet, and all four
finite demos. `scripts/sdk/verify_examples.py` writes a content-hashed report and
checks default-build exclusion. It records fixture evidence, not cloud-account
or production-server certification.

## Explore the API

The [SDK guide](index.md) introduces conditional changes, batch
application, controls, errors, pagination, and observations. The
[HTTP reference](api-reference.md) covers every operation in the
SDK inventory, including explicit v1 compatibility. The
[wire reference](wire-types.md) lists every schema and driver
field; the [Go reference](go-reference.md) contains exported
signatures, configuration, helpers, and worker types.

<!-- Imported from examples/sdk/README.md; preserve candidate scope and reconcile with original before regenerating. -->
