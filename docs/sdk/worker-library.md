---
title: SDK candidate · CPRa external worker library
description: SDK candidate · CPRa external worker library for the reviewed CPRa source; see the version and availability notice.
cpra_scope: sdk
---

> **Unpublished SDK candidate:** this guide follows the source in this checkout. Confirm the connected server’s capabilities and release qualification before using candidate APIs. See [availability and source](../versions.md#go-sdk-and-approved-management-plan).

# CPRa external worker library

This independently versioned Go 1.25 module runs operator-owned Go handlers in a
separate process. Every Go implementation file requires `-tags externaljobs`.
Importing this package without the tag fails at compile time. The separate SDK
contains the tagged low-level protocol client; normal SDK applications do not
depend on this module or bbolt.

**Qualification:** the tests exercise protocol fixtures and real worker-process
termination. CPRa's management/external-worker server contracts are a release
prerequisite; fixture success is not evidence of production-server integration,
provider certification, or a published module version.

The private server now implements JobType registration and
[stopped worker credential provisioning](../worker-authentication.md).
Assignment, start, heartbeat, result and late-evidence routes remain unfinished;
provisioning a token does not make this runner usable against those routes yet.

## Add the module to your worker application

Use Go 1.25 or newer. The worker is a library, so create your own executable and
add the module with `go get`; importing it does not start a service. Once a
qualified version is published, replace the placeholders with the exact SDK and
worker versions listed as compatible in that release:

```sh
mkdir cpra-worker
cd cpra-worker
go mod init example.com/team/cpra-worker
GOWORK=off go get github.com/ziad-hsn/cpra/sdk/go@'REPLACE_WITH_PUBLISHED_SDK_VERSION'
GOWORK=off go get github.com/ziad-hsn/cpra/sdk/go/worker@'REPLACE_WITH_PUBLISHED_WORKER_VERSION'
go build -tags=externaljobs .
```

Save the complete program below as `main.go` before the build command. The
candidate `v0.1.0-rc.1` requirements do not mean these versions are already
downloadable. In this source checkout, use the repository's temporary workspace
for SDK/worker development until the nested modules are published. Published
consumers must not need that workspace or a local `replace` directive.

The worker module ships its own [MIT license](source/sdk/go/worker/LICENSE.txt), package overview, and
examples. Every Go file, including [doc.go](source/sdk/go/worker/doc.go.txt) and
[example_test.go](source/sdk/go/worker/example_test.go.txt), requires the build tag. The server separately
requires `externaljobs`, runtime enablement, and worker authorization.

## Start a worker

Provision a private, 32-byte raw wrapping-key file **outside** the state directory.
Use resolved absolute paths for both the state directory and wrapping key:
neither the final component nor any ancestor may be a symbolic link. On macOS,
for example, resolve a `/var/...` path to its actual `/private/var/...` location
before configuring the worker. The state path must be owned by the service
identity, mode `0700` on Unix; the key must be `0600`. On Windows, the state/key
DACL may grant access only to
the process identity and SYSTEM. The library creates a protected inheritable
Windows state-directory DACL and checks existing ownership/access rules. Native
Windows qualification is separate from cross compilation.

The key protects a data-key envelope, encrypted results, and replay metadata. It
is never generated or replaced automatically. Back up the **stopped complete
worker state directory**, and retain the matching key separately. The worker ID, immutable provisioned worker UID, and CPRa store/restore identity
must match on every restart. Credential rotation may retain the same UID; it
does not change the identity of pending results. A restored
or replaced server identity requires operator reconciliation; changing the
configured identity must never silently discard an old outbox.

```go
//go:build externaljobs

package main

import (
    "context"
    "log"
    "os"
    "os/signal"

    cpra "github.com/ziad-hsn/cpra/sdk/go"
    "github.com/ziad-hsn/cpra/sdk/go/api"
    "github.com/ziad-hsn/cpra/sdk/go/worker"
)

func main() {
    client, err := cpra.NewWorkerClient(cpra.Config{
        BaseURL: "https://cpra.example.net",
        AuthToken: os.Getenv("CPRA_WORKER_TOKEN"), // scoped worker principal
    })
    if err != nil { log.Fatal(err) }
    defer client.CloseIdleConnections()

    registry := worker.NewRegistry()
    err = registry.Register("example/health", "1", "check",
        func(ctx context.Context, job worker.Job) (api.Outcome, error) {
            // Call your provider here using worker-local configuration and
            // job.Credentials. This example deliberately makes no health claim.
            return api.Outcome{Status: "noData", Diagnostic: "handler not configured"}, nil
        })
    if err != nil { log.Fatal(err) }

    runner, err := worker.New(worker.Config{
        Client: client, Registry: registry,
        WorkerID: "checks-west", WorkerUID: "<provisioned-worker-uid>", ServerID: "<verified-server-store-epoch>",
        StateDir: "/var/lib/cpra-example-worker",
        WrappingKeyPath: "/etc/cpra-example-worker/wrapping.key",
    })
    if err != nil { log.Fatal(err) }
    defer runner.Close()
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
    defer stop()
    if err := runner.Run(ctx); err != nil { log.Print(err) }
}
```

Read the program as three separate responsibilities:

1. `NewWorkerClient` authenticates as a worker principal. It talks to the worker
   protocol; it does not provision JobTypes or grant itself management access.
2. `Registry.Register` binds a descriptor to a function compiled into this
   executable. The three strings must match the operator-provisioned JobType.
   The example handler returns `noData` deliberately: replace its body with an
   actual health observation before assigning work to it.
3. `worker.New` opens and checks the encrypted journal. `Run(ctx)` admits work,
   invokes registered handlers, records outcomes, and delivers those outcomes.
   Cancelling the context requests shutdown. The application owns signal handling
   and its supervisor owns the final process deadline.

There is no success output for the placeholder handler. A compatible server may
assign a check and receive `noData`; that proves no target health. To test handler
registration without a server, run the executable example below. The complete
DAO/SMS integration in the repository shows real HTTP handler bodies against
explicit local fixtures before configured deployment.

```sh
go test -tags=externaljobs -run ExampleRegistry_Register -v
```

Expected example output is `check <nil>`, `recovery <nil>`, and
`notification <nil>`. These values mean the registry accepted each handler. The
example does not obtain a start grant or invoke any external operation.

Register exact JobType ID/version/category matches before calling `Run`. The
registry freezes when execution begins. The SDK never downloads or executes
handler code. Versioned descriptors are separately provisioned through an
operator client; advertising a capability does not grant permission to use it.

| Handler category | Confirmed statuses | Unavailable/uncertain statuses |
| --- | --- | --- |
| `check` | `success`, `failure` | `noData` (`unknown`/`rejected` normalize to `noData`) |
| `recovery` | `accepted`, `completed` | `unknown`, `rejected` |
| `notification` | `accepted`, `delivered` | `unknown`, `rejected` |

The runner fills server, worker UID, execution, grant, and category identity. A returned error, panic,
invalid status, or oversized result becomes `unknown` (`noData` for checks).
Handler error text is not persisted because it may expose provider secrets.
`accepted` distinguishes provider acceptance from observed completion/delivery.
Rejection codes do not independently authorize retries; the server enforces its
registered classification and current policy.

Use `CredentialResolver` to resolve an opaque profile name locally. Resolved
values and assignment parameters are never written to the journal. Do not put
provider secrets in outcome data, diagnostics, or evidence. Such payloads are
encrypted locally but are deliberately delivered to CPRa when returned.

## Execution and restart contract

1. Reserve journal record and byte capacity before requesting a start grant.
2. Persist the grant/execution marker before invoking the handler.
3. Persist the resulting bounded envelope before attempting delivery.
4. Resend the same envelope until an authoritative durable receipt arrives.

The original live path calls `begin` once. Only its confirmed `granted` response
can lead to a handler call, after the durable marker and a cancellation check.
A lost start reply, `started`, or `unknown` never invokes a handler. Journal
recovery sends `reconcile`; `pending` retains the reservation until the server
reports an authoritative state. `started` records an interrupted outcome without
invocation. An authoritative `rejected` releases only an unstarted reservation;
`terminal` confirms the retained receipt. Uncertain recovery and notification
entries remain held after an unknown-result receipt. No local timeout or missing
response releases that hold.

Each `Run` creates a fresh client session nonce. One poll is outstanding at a
time; its exact nonce, session, sequence, capabilities, capacity, and limit remain
frozen across lost replies. The next sequence is sent only after the batch is
validated and admitted locally. Only the explicit `workerSessionExpired` error
opens a new session with a fresh nonce. Generic conflicts and transport failures
never replace a session. A newly accepted sequence may advance session expiry;
an exact replay does not renew it. All assignment, start, heartbeat, and receipt echoes
must match their original identities. Poll offers do not authorize execution.

An outbox result or late evidence uses the journal's original server ID and worker
UID and does not require a current polling session or new-work grant. A rotated
credential for the same UID can deliver it; a different UID or restored server ID
cannot silently adopt the journal. This is a client boundary, not a claim that
server execution routes or provider-side fencing are implemented.

`QueueLateEvidence` appends evidence using the original receipt and a stable new
evidence identity. It does not replace the result, clear the hold, or re-enable
execution. Duplicate evidence is safe to resend; server-side idempotency remains
required if a response is lost. Evidence URLs are data, never fetched by the
worker library.

## Capacity and shutdown

Defaults: 16 concurrent execution slots, 4,096 records, 256 MiB live-plus-reserved
record bytes, 128 KiB encoded outcomes, 64 KiB diagnostic text, and 512 MiB journal
allocation before admission stops. Allocation pressure never discards entries
or interrupts completion writes already reserved. Status reports allocated,
live, and reserved bytes separately. Deleting bbolt records does not shrink the
file; offline operational compaction is not provided by this first library.

The runner uses a single bounded poller and result-delivery loop. Execution
goroutines are bounded by concurrency slots, and durable record/byte capacity is
reserved before requesting start permission or invoking a handler. Each pending
execution reserves the outcome limit plus 32 KiB for bounded, JSON-escaped
identity metadata. An outcome limit too small for its interrupted result prevents
admission. Polls wait at
most 25 seconds. Result retries default to one second and use the same
persisted identity and envelope; ordinary SDK mutation retry policy stays off.

`Run(ctx)` installs no signal handler and never exits the process. Cancellation
stops admission, cancels cooperative handlers, and retains known results for
delivery/restart. After 45 seconds, `Status().DrainDeadlineExpired` becomes true.
The runner **retains its journal lock and does not return while a handler is
still alive**. A supervisor must terminate an uncooperative process. Lease expiry
does not fence an external process, and this library cannot sandbox hostile Go
code. Use a separate service account, process/container resource limits, network
policy, and provider credentials; never mount the CPRa server state or key paths.

## Verification and versioning

Run tagged tests explicitly; a parent module's `go test ./...` does not enter a
nested module. Repository scripts create a temporary Go workspace for local
verification. Published consumers must use downloaded module versions with
`GOWORK=off`, without local replacements.

```sh
go test -tags externaljobs ./...
go test -race -tags externaljobs ./...
```

Initial release tags are `sdk/go/worker/v0.1.0-rc.1` and, after qualification,
`sdk/go/worker/v0.1.0`. Journal format is **2**. Worker UID is bound into metadata and encryption AAD;
records retain their original session and execution identities. Format 1 is
explicitly rejected with `ErrJournalVersion`, preserving its records and key.
There is no automatic migration, journal reset, or outbox deletion. SDK, server API, worker protocol,
and journal versions are separate compatibility contracts. No tag is implied to
exist merely because it appears in the candidate module requirement.

## Read tagged package documentation

The hosted pkg.go.dev service selects a limited set of OS/architecture build
contexts. Its loader does not enable the arbitrary `externaljobs` tag. As a
result, normal hosted indexing does not render this package or the SDK's tagged
custom-job APIs. This is a documentation limitation, not a reason to expose an
untagged stub. See the [pkg.go.dev build-context policy](https://pkg.go.dev/about#build-context)
and its [source-file selection implementation](https://go.googlesource.com/pkgsite/%2B/master/internal/fetch/load.go).

Read this README, the source comments, and the CPRa generated tagged API
reference. The ordinary `go doc` command also selects the default build context;
setting `GOFLAGS=-tags=externaljobs` does not make it render this package.
To check which files actually compile and run its executable examples locally:

```sh
go list -tags=externaljobs -f '{{.GoFiles}}' github.com/ziad-hsn/cpra/sdk/go/worker
go test -tags=externaljobs -run Example ./...
```

The module remains downloadable after publication because build constraints do
not remove its source from a module archive. Package visibility, compilation
with the tag, server authorization, and release qualification are separate
checks.

For publication from the complete repository checkout, follow the
[maintainer publishing guide](publishing.md). The guide is a
repository document outside this nested module archive; the installation and
execution requirements above remain available in the downloaded module.

<!-- Adapted from sdk/go/worker/README.md. -->
