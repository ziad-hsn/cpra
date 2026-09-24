# Monitor a DAO's RPC and notify an internal SMS gateway

A DAO's governance application needs an RPC endpoint to read proposals, voting
state, and contract code. An HTTP 200 alone cannot tell you whether that endpoint
is on the expected chain or serving an old head block. This example adds a CPRa
check for those conditions, then sends an alert through an internal SMS service.
DAO means *decentralized autonomous organization*; here the monitored dependency
is the execution RPC used by its governance application.

You will register two custom JobTypes, connect them to a monitor, and run their
compiled Go handlers in a separate worker. The check issues read requests only.
It never signs transactions, reads a wallet key, or changes a contract.

**Server prerequisite:** CPRa's v2 management API and worker dispatcher are still
pending. The default demo runs local protocol fixtures. The `register` and
`worker` modes contain the real SDK call paths, but need the corresponding server
contracts before they can operate against CPRa. A successful demo proves the
example's behavior against fixtures, including actual loopback HTTP and encrypted
worker storage; it does not prove production-server integration or SMS delivery.

## Run the lesson locally

Follow the parent [SDK examples setup](../README.md) to select the Go workspace.
Then, from `examples/sdk`, run:

```sh
go run -tags externaljobs ./dao-sms
```

Every Go file in this directory requires `externaljobs`, as does the worker
library. An untagged build omits the example entirely. The default `demo` mode
starts a mock management API and two loopback HTTP servers; it uses no account,
cloud endpoint, wallet, or phone number.

The output reports these steps:

```text
Registered 2 JobTypes, 1 SMS endpoint, 1 notification group, and 1 monitor (...).
check: success — expected chain, synced execution client, recent head, and governor code observed
check: failure — RPC head is older than the configured threshold
notification: accepted — internal gateway accepted the SMS; handset delivery is not verified
RPC requests: 7; SMS requests: 1; outcome submissions: 4 (one receipt was lost).
Encrypted outbox drained. Temporary fixture state and wrapping key are removed when the demo exits.
```

The first check reads a healthy fixture. The second reads a stale head. The local
dispatcher then schedules an SMS notification. It deliberately loses the first
receipt for the worker's notification outcome. The worker submits the same saved
outcome again, while the SMS request count stays at one. That is the distinction
between retrying an outcome report and sending another notification.

## Find the implementation

This example is a command with three modes. `register` writes resource
descriptions, `worker` executes locally compiled handlers, and `demo` joins them
through explicit fixtures. Your own application imports the public SDK and
worker modules; it does not import this `package main` or its private helpers.

| File and function | What to learn from it |
| --- | --- |
| [resources.go](resources.go), `resources` | Constructs JobTypes, external drivers, endpoint/group references, and the monitor. |
| [resources.go](resources.go), `register` | Freezes the resource collection, validates references, applies it, and waits using the returned operation ID. |
| [main.go](main.go), `registry` | Binds each JobType/version/category to a compiled handler. |
| [main.go](main.go), `localCredentials` and `run` | Resolves worker-owned credentials and constructs the runner with its encrypted journal. |
| [handlers.go](handlers.go), `daoHealth` and `internalSMS` | Validates assignments, invokes the designated provider, and returns an observed outcome. |
| [handlers.go](handlers.go), `rpcCall` and `providerClient` | Applies HTTP deadlines, response limits, redirect policy, and RPC envelope checks. |
| [demo.go](demo.go), `demo` | Runs the full local lesson and deliberately loses an outcome receipt. |
| [handlers_test.go](handlers_test.go) and [review_test.go](review_test.go) | Verifies provider responses, resource references, credential boundaries, and encrypted outbox delivery behavior. |

The following snippets are excerpts, with imports and surrounding setup omitted.
The complete files supply those definitions and error paths.

## Read the check

Start with `daoHealth` in [handlers.go](handlers.go). Each assignment gets one
four-second context for the complete check. The HTTP client also refuses
redirects, sets connection/header timeouts, and bounds each response to 1 MiB.

The handler makes these calls in order:

| Call | What the handler checks |
| --- | --- |
| `eth_chainId` | The configured chain ID matches the endpoint. |
| `eth_syncing` | The execution client reports `false`, rather than sync progress. |
| `eth_getBlockByNumber("latest", false)` | The latest block timestamp is within the chosen age threshold. |
| `eth_getCode(governor, blockNumber)` | The configured governor address has nonempty bytecode at the inspected block height. |

The wire encodings and RPC method meanings come from the official
[Ethereum JSON-RPC documentation](https://ethereum.org/en/developers/docs/apis/json-rpc/).
The use of all four as one health check, the 120-second example threshold, and the
15-second future-clock tolerance are choices made by this example.

A failed condition returns `failure`. A malformed reply, transport error, or
unusable clock observation returns `noData`. Neither is success. Provider error
bodies and credential values are excluded from the returned diagnostic.

`daoHealth` returns a `worker.Handler`: a Go function that receives a context and
one `worker.Job`, then returns an `api.Outcome` and an ordinary Go error. Its
opening lines show where execution inputs come from:

```go
return func(parent context.Context, job worker.Job) (api.Outcome, error) {
    ctx, cancel := context.WithTimeout(parent, 4*time.Second)
    defer cancel()
    credentials, ok := job.Credentials.(rpcCredentials)
    if !ok || credentials.URL == "" {
        return api.Outcome{
            Status:     "noData",
            Diagnostic: "RPC credential profile is unavailable",
        }, nil
    }
    // Assignment validation and RPC calls follow in handlers.go.
}
```

The runner supplies `job.Credentials` through the local resolver. The assignment
supplies parameter JSON such as the expected chain and governor address. The
handler validates that JSON before contacting the provider. A handler must still
validate its input even though the JobType declares a schema.

An unhealthy observation is a successful *measurement* with a negative result:
the handler returns the result as data and a `nil` Go error. For a stale block,
the relevant return is:

```go
return api.Outcome{
    Status:     "failure",
    Diagnostic: "RPC head is older than the configured threshold",
}, nil
```

This lets the worker record the observed condition. Returning an HTTP error body
as the diagnostic would expose provider details and would leave its meaning
unclear. The example uses short, bounded descriptions and reserves structured
`Data` for the declared result schema.

This check establishes a useful dependency signal, with specific limits:

- `latest` can move during a reorganization, and a block number does not pin a
  block hash. The code lookup uses the inspected height; a reorganization can
  still replace that block between calls. This does not prove finality.
- Nonempty code does not prove that it is the intended governor implementation,
  that voting works, or that the contract is secure. Add a reviewed code hash or
  a read-only contract call if your deployment needs those checks.
- A fresh timestamp relies on the worker's clock. Use your chain's observed
  cadence when selecting `-max-block-age`; 120 seconds is not a universal target.
- The four calls use one configured RPC origin. The result does not establish
  agreement between independent providers or consensus-client health.

## Register the types and the monitor

[resources.go](resources.go) builds one collection with these stable IDs:

| Kind | ID | Purpose |
| --- | --- | --- |
| JobType | `dao-rpc-health` | Version `1` check with a typed parameter schema. |
| JobType | `internal-sms` | Version `1` notification with a typed parameter schema. |
| NotificationEndpoint | `dao-sms` | Selects the SMS JobType and worker-local `governance-sms` profile. |
| NotificationGroup | `dao-oncall` | Refers to `dao-sms`. |
| Monitor | `dao-governance-rpc` | Runs the check every `60s`, with a `5s` timeout, and routes its `red` alert to the group. |

The monitor's external driver contains the JobType ID, version, parameter JSON,
and opaque credential-profile name. It contains no RPC URL, RPC token, SMS token,
or recipient number. The two JobType JSON schemas describe parameters and result
data; they do not upload handler code. The worker must already contain the
matching Go handlers.

`resources` connects a monitor check to a JobType using the tagged SDK type:

```go
check, err := api.Driver("check", "external", api.ExternalConfig{
    JobTypeID:         rpcJobType,
    Version:           jobVersion,
    CredentialProfile: rpcProfile,
    Parameters:        rpcRaw,
})
if err != nil {
    return nil, err
}
```

Here `rpcRaw` is JSON produced from validated `rpcParameters`, `rpcJobType` is
`dao-rpc-health`, and `rpcProfile` is `governance-rpc`. The resulting driver is
placed in `api.CheckSpec{Driver: check, Interval: "60s", Timeout: "5s"}`. The SMS
endpoint uses the same construction with category `notification`, JobType
`internal-sms`, and profile `governance-sms`.

The server-side JobType describes the accepted parameters and outcome data. The
worker registry supplies the executable implementation. This excerpt from
`registry` shows the two bindings:

```go
registry := worker.NewRegistry()
if err := registry.Register(rpcJobType, jobVersion, "check", daoHealth(client, time.Now)); err != nil {
    return nil, err
}
if err := registry.Register(smsJobType, jobVersion, "notification", internalSMS(client)); err != nil {
    return nil, err
}
```

Those IDs, versions, and categories must agree with the registered resources.
Construct the registry before starting the runner; `Run` freezes it. A JobType
registration alone does not install code, and a compiled handler alone does not
authorize the server to dispatch it.

`register` freezes all five resources and verifies their local references before
starting an operation. The SDK stages them, requests whole-collection validation,
then asks the server to activate them. The server owns dependency ordering and
per-resource version checks. Waiting ends with either success or a visible
terminal/transport error; the operation handle is printed whenever one exists.

The first part of that collection flow is:

```go
frozen, err := collection.FreezeResources(ctx, collection.Slice(items), collection.Options{
    MaxResources:    5,
    MaxStagingBytes: 1 << 20,
})
if err != nil {
    return collection.Result{}, err
}
defer frozen.Close()
if _, err = collection.ValidateReferences(ctx, frozen, nil); err != nil {
    return collection.Result{}, err
}
result, err := collection.Apply(ctx, client.Operations, frozen)
```

`FreezeResources` fixes the content used for validation and upload, with explicit
resource and byte limits. `Close` removes the client staging files. Keep both the
returned `result` and `err`: a server operation may exist even when waiting or a
later request fails. `register` subsequently calls
`collection.Wait(ctx, client.Operations, result)` and checks the immutable
execution summary outcome. The helper pins the original content identity and
returns one bounded first result page with aggregate counts.
Whole-collection validation does not imply collection-wide rollback; inspect
item outcomes if activation is partial.

Once the server prerequisite is satisfied, use a management principal with the
required JobType, endpoint, group, and monitor permissions:

```sh
go run -tags externaljobs ./dao-sms \
  -mode register \
  -server https://cpra.internal.example \
  -token-file /etc/cpra-dao-worker/operator.token \
  -chain-id 0x1 \
  -governor 0xYOUR_40_HEXADECIMAL_CHARACTERS \
  -max-block-age 120
```

Replace the governor placeholder with your designated deployed contract. The
example rejects a malformed address before staging. These initial resources have
no resource versions: they are intended for fresh creation. To change existing
objects, read their current versions and use conditional replace/patch, or prepare
a versioned collection. Do not discard a conflict by fetching and overwriting
someone else's change automatically. A stopped wait does not cancel its server
operation; retain the printed handle and inspect it before another registration.

## Run the worker with local credentials

The worker needs its own principal, an expected server store/restore identity,
and a local credential file. Server build inclusion, explicit runtime enablement,
and scoped authorization are independent requirements. A worker token must not
have operator JobType-management authority simply because its process knows a
handler.

Copy [worker-config.example.json](worker-config.example.json) to a private file
owned by the worker's service account, then set the two HTTPS URLs, token-file
paths, and a designated SMS test destination. `rpc.tokenFile` may be empty for an
RPC origin that needs no bearer token. SMS requires a token. The URLs come only
from this local file; assignment parameters cannot redirect the worker to a
different origin. The example refuses URL user information, query strings, and
fragments, so provider keys cannot be supplied through a URL.

The resolver reads provider token files at startup. Restart deliberately after
rotating them. The separate CPRa token-source callback reads its token file for
each request. Configure your internal CA in the worker's operating-system trust
store; the example does not disable TLS verification.

Provision a raw 32-byte wrapping key outside the state directory. For a Unix user
install, this creates the file exclusively and fails if it already exists:

```sh
umask 077
mkdir -p "$HOME/.config/cpra-dao-worker" "$HOME/.local/state/cpra-dao-worker"
python3 - <<'PY'
import os
from pathlib import Path
key = Path.home() / ".config/cpra-dao-worker/wrapping.key"
with key.open("xb") as output:
    output.write(os.urandom(32))
PY
```

Do not replace this key on restart. Use resolved absolute paths, a private
`0700` state directory, and a `0600` key file. See the
[worker storage requirements](../../../sdk/go/worker/README.md) for Windows ACLs
and paths containing symbolic links. Run this command as the identity that owns
the state and key:

```sh
go run -tags externaljobs ./dao-sms \
  -mode worker \
  -server https://cpra.internal.example \
  -token-file /etc/cpra-dao-worker/worker.token \
  -server-id VERIFIED_STORE_RESTORE_IDENTITY \
  -worker-id dao-worker \
  -worker-uid PROVISIONED_WORKER_UID \
  -local-config /etc/cpra-dao-worker/worker.json \
  -state-dir "$HOME/.local/state/cpra-dao-worker" \
  -key-file "$HOME/.config/cpra-dao-worker/wrapping.key"
```

Use the `protocolServerID` and worker `uid` returned by local worker-auth
provisioning for `-server-id` and `-worker-uid`. These identify the server restore
epoch and the worker incarnation; a display name is insufficient. Preserve them
with the encrypted journal across ordinary restarts and token rotation. Restoring
a server changes its protocol identity and requires explicit reprovisioning. The
execution routes remain unavailable in the normal server while that protocol is
being implemented.

The example allows two concurrent handlers. It inherits the worker library's
bounded encrypted outbox and reserves outcome capacity before requesting a start
grant. A lost start reply never authorizes execution. Interrupted handlers are
not restarted; an uncertain notification remains unknown for operator review.
Cancellation requests cooperative shutdown with a 45-second drain budget. The
library retains the journal lock while a handler is still active, so a supervisor
must provide the final process deadline.

## Adapt the SMS gateway contract

`internalSMS` sends one request to the locally configured URL:

```http
POST /v1/messages
Authorization: Bearer <worker-local token>
Content-Type: application/json

{"to":"<locally resolved recipient>","text":"CPRa: ...","executionID":"<original execution>"}
```

For this example gateway, a valid acceptance is:

```http
HTTP/1.1 202 Accepted
Content-Type: application/json

{"id":"gateway-message-42","status":"accepted"}
```

The handler reports `accepted` and retains the bounded gateway acceptance ID in
result data. It does not report `delivered`: that needs independent evidence from
your gateway or handset delivery receipt. Delayed evidence belongs in the worker
library's separate `QueueLateEvidence` operation, tied to the original execution
and receipt, rather than a second SMS submission.

This is an illustrative internal service protocol, not an SMS industry standard.
Its contract defines 400, 401, 403, and 422 as rejection *before acceptance*.
Keep that classification only if your gateway provides the same guarantee.
Other statuses, malformed 202 replies, redirects, and lost connections return
`unknown`. The handler performs no automatic send retry. The `executionID` is a
correlation field; do not assume gateway deduplication unless you implement and
test that behavior in the gateway.

Only the `dao-oncall` recipient alias travels through CPRa. Phone numbers and
gateway bearer tokens stay in worker-local configuration. The message body is
deliberately generic because the draft assignment contract does not define a
dynamic incident-message template.

## Verify changes and stop the example

From `examples/sdk`:

```sh
go test -tags externaljobs ./dao-sms
go test -race -tags externaljobs ./dao-sms
```

The tests cover the four RPC observations, malformed and oversized replies,
unusable clocks, SMS acceptance/rejection/ambiguity, redirect refusal, URL
selection, credential redaction, bounded token reads, complete resource
references, and the encrypted worker demo with a lost outcome receipt. The
fixtures send real HTTP requests; they do not contact an Ethereum provider or
an SMS account.

The demo removes its temporary state and key when it returns. For a configured
worker, stop the process before backing up the complete state directory; retain
the matching wrapping key separately. Inspect unknown actions before deleting
state. To remove registered resources, use conditional deletion with current
versions, beginning with the monitor, then the group and endpoint, and finally
unused JobTypes. The example never deletes configured monitors or provider data
as part of shutdown.

## Develop another handler

Use this example's separation between resource declarations, handler code, and
worker-local configuration when adding your own integration:

1. Define the smallest parameter struct needed by the handler. Add its validation
   and matching JobType JSON schema together. For a reviewed governor code hash,
   for example, add the expected hash as a parameter; keep the RPC URL and token
   in `localCredentials`.
2. Extend the handler with a bounded read or operation. Reuse the assignment's
   context, close response bodies, and give each outcome a precise meaning.
   Return `noData` when a check cannot establish an observation. For a side
   effect, return `unknown` when the provider may have accepted it.
3. Update the declared result schema if the handler adds structured outcome
   data. Choose a new JobType version when changing a published contract and
   deploy a worker containing that version before selecting it in resources.
4. Register the compiled handler under the matching ID, version, and category.
   Add an external driver reference to the resource collection and validate all
   endpoint/group dependencies before applying it.
5. Extend `TestDAOHealthOverHTTP` or `TestSMSAcceptanceAndAmbiguity` with real
   loopback HTTP responses for the new behavior. Include malformed data, timeout,
   and ambiguous side-effect cases. Keep
   `TestAssignmentCannotRedirectProvider` and the lost-receipt demo passing.

For SMS, keep acceptance and delivery distinct when adapting the gateway. A
usable `202` reply returns `api.Outcome{Status: "accepted", ...}`. A lost request
reply returns `api.Outcome{Status: "unknown", ...}`. The handler should not turn
the second case into a new send; the worker journal manages delivery of the
recorded outcome to CPRa. If you later ingest a delivery receipt, attach it as
late evidence to the original execution rather than replacing the original
observation.

Build and test every file that imports these custom-job APIs with
`-tags externaljobs`. Ordinary SDK consumers remain independent of the worker
module. The [SDK guide](../../../docs/sdk/index.md) explains module imports and
the full API, while the [worker guide](../../../sdk/go/worker/README.md) documents
runner ownership, storage limits, shutdown, and publication status.
