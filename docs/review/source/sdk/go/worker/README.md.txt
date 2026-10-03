# CPRa external worker library

This independently versioned Go 1.25 module runs operator-owned Go handlers in a
separate process. Every Go implementation file requires `-tags externaljobs`.
Importing this package without the tag fails at compile time. The separate SDK
contains the tagged low-level protocol client; normal SDK applications do not
depend on this module or bbolt.

**Qualification:** the private process harness exercises the real SDK runner and
CPRa startup in separate Linux processes. The current 37-case campaign passes on
Go 1.25.0 and Go 1.27.1, with both processes race-instrumented on the latter. It
covers normal checks/recovery/notifications, interrupted Start replies, handler
crashes, saved outcomes, lost receipts, combined restart, uncooperative shutdown,
late evidence and revocation. Ten capacity cases distinguish reserved/live bytes
from allocated database space and retain unknown actions across restart. Raising
an exhausted limit permits new work without repeating the original action. The
[process report](../../../docs/implementation/worker-dispatch.md#separate-process-verification)
records exact sources and earlier failed attempts. Those cases use small
configured limits. A separate [five-minute mixed-load test](../../../docs/implementation/worker-dispatch.md#sustained-mixed-worker-load)
passes on both compilers at the default limits after the
[journal-accounting correction](../../../docs/implementation/worker-dispatch.md#journal-accounting-under-load).
The Go 1.27.1 race run records 2,143 independently observed invocations; Go 1.25
records 2,188. Both complete five measurement minutes, reach 16 active handlers,
drain the journal, preserve original outcome/receipt identities and report zero
unknown actions, repeated invocations or unreconciled outcomes. Retired checks
receive the specific expired-check response after restart. Earlier load failures
and the first outer report-writer failure remain recorded. The refreshed 37-case
crash campaign also passes on both compilers against the accounting change. Final qualification and independent review remain open.
Test executables and local network targets do not establish published artifact
or provider-account qualification.

The private server now implements JobType registration and
[stopped worker credential provisioning](../../../docs/worker-authentication.md).
Private adapters for assignment, Start, result receipts, heartbeats and late
evidence have SDK/TLS/Raft coverage. Automatic server finalization also preserves
original receipts across expiry and restart. Normal startup now exposes explicitly
enabled [worker HTTP routes](../../../docs/implementation/worker-http.md), with
separate new-work and completion barriers during shutdown.
Private [external-check SLO accounting](../../../docs/implementation/worker-slo.md)
now measures server confirmation after result commits and preserves missing timing.
Controller dispatch and pre-admission schedule accounting are connected. The
[public configuration checkpoint](../../../docs/implementation/external-configuration.md)
now accepts external check, recovery and notification configuration through the
API, collection validation and bootstrap when explicitly enabled. Its focused
tests pass on both compilers; the ordinary process/crash cases now create monitors
through the public SDK, and all 37 refreshed scenarios pass. Capacity and load fixtures still
seed encrypted configuration in the stopped store. Provisioning a token alone
does not enable external monitors. The sustained-load campaign has local passing
evidence; the final candidate and independent review still require qualification. See the
[server integration status](../../../docs/implementation/external-worker-server-next-steps.md).

The private [check-retention contract](../../../docs/implementation/worker-check-retention.md)
allows the server to expire completed check receipts. When Start reconciliation
or check Result returns the exact HTTP 410 `workerCheckExpired` problem, the SDK
reports `cpra.ErrWorkerCheckExpired` and the runner removes that check's replay
record. It does not invoke the handler again. Generic conflicts, missing records,
network failures and unavailable storage do not authorize removal. Intervention
and notification outcomes, including unknown actions and their evidence, remain
retained. Scoped checks pass against the private server adapters and journal;
production worker integration and independent review remain open.

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

The worker module ships its own [MIT license](LICENSE), package overview, and
examples. Every Go file, including [doc.go](doc.go) and
[example_test.go](example_test.go), requires the build tag. The server separately
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
`terminal` confirms the retained receipt and carries no executable grant or
deadline. Uncertain recovery and notification entries remain held after an
unknown-result receipt, including across restart and repeated matching terminal
responses. A different receipt is an identity error. No local timeout or missing
response releases that hold; late evidence remains attached to the original receipt.

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
cannot silently adopt the journal. These checks do not fence a remote provider
or establish complete controller/runner interoperability.

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

To deliver retained outcomes without accepting new assignments, pass an empty
`NewRegistry()` to `New` with the original journal, key, worker UID and server
identity. `Run` skips polling when no handlers are registered, while its journal
delivery and reconciliation loop continues. It requires current credentials;
authentication rejection leaves saved outcomes pending. Cancel its context to
stop it. A zero `PendingOutcomes` count does not mean every record was retired:
inspect `Records` and `UnknownActions`, since unknown actions retain their holds.

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
[maintainer publishing guide](../../../docs/sdk/publishing.md). The guide is a
repository document outside this nested module archive; the installation and
execution requirements above remain available in the downloaded module.
