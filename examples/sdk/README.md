# CPRa SDK examples

Run four small integrations that turn service events into CPRa configuration
changes, then read the code behind each result.

The examples use a separate Go module so AWS and Kubernetes dependencies stay
out of the public SDK. Go 1.25 or later is required. Node, Redis, a Kubernetes
cluster, an AWS account, and SMS credentials are unnecessary for the local demos.

**Current boundary:** the private working branch implements v2 management and
public collection activation with retained execution results. Cross-process
file reselection and external-worker qualification remain separate gates.
These finite demos use clearly labeled local fixtures. Configured modes
require a server that implements and qualifies every operation the chosen example
uses; passing a local demo does not establish that server capability.

The collection fixture accepts preparation tickets and verifies the keyed item
and inventory commitments produced by `collection.Freeze`. Repeating Create with
the original ticket resolves its original operation. Its 202 validation admission
seals an immediate, immutable in-memory result; a separate bounded GET serves
that original result. Repeating validation reconciles it, and further uploads are
rejected. Activation completes fixture children synchronously and returns a
metadata-only admission; a bounded GET serves immutable original-order execution
rows for `collection.Wait`. This fixture does not model production worker
scheduling or history retention; its cursor is a local ordinal and its summary
digest is fixture-only.
All fixture state and private
input remain in process memory; there is no Raft, encrypted storage, retained
history, restore fencing or provider certification in this mock server.

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
| `go run ./queue-registration -demo` | Two deliveries acknowledged; one monitor created | [Queue registration](queue-registration/README.md) |
| `go run ./elb-deregistration -demo` | A mapped monitor disabled once; repeated event ignored | [AWS deregistration](elb-deregistration/README.md) |
| `go run ./kubernetes-services -demo` | Five DNS/TCP monitors created from three Services; second listing creates no duplicates | [Kubernetes Services](kubernetes-services/README.md) |
| `go run -tags=externaljobs ./dao-sms -mode=demo` | DAO RPC checked, stale head detected, one SMS accepted, lost receipt replayed | [DAO and internal SMS](dao-sms/README.md) |

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
[package and publishing guide](../../docs/sdk/publishing.md) explains the two SDK
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

The [SDK guide](../../docs/sdk/index.md) introduces conditional changes, batch
application, controls, errors, pagination, and observations. The
[HTTP reference](../../docs/sdk/api-reference.md) covers the SDK HTTP operations. The
[wire reference](../../docs/sdk/wire-types.md) lists every schema and driver
field; the [Go reference](../../docs/sdk/go-reference.md) contains exported
signatures, configuration, helpers, and worker types.
