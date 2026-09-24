# Register monitors from a service-registration queue

This example turns a registration message into a CPRa HTTP monitor. It models a
Redis Streams consumer with a pending delivery and an explicit acknowledgment.
The queue is an in-memory fixture loaded from JSON Lines; it does not connect to
Redis or implement the Redis wire protocol.

The important sequence is: read a message, establish that the intended monitor
exists, then acknowledge the message. If the result is uncertain or an existing
monitor belongs to someone else, the message stays pending.

The local demo works now. Configured CPRa access requires the management v2 server,
which is not implemented in the current application. The demo's HTTP fixture
does not run a scheduler, providers, or health checks.

## Run the lesson

Use the [examples setup guide](index.md), then run from `examples/sdk`:

```sh
go run ./queue-registration -demo
go test ./queue-registration
```

The demo delivers the same registration twice. The first delivery creates
`service-payments`; the second reads the same monitor and acknowledges the
registration without another write. The output includes:

```text
service=payments monitor=service-payments outcome=created acknowledged=true
service=payments monitor=service-payments outcome=already-present acknowledged=true
messages_acknowledged=2 monitors=1
```

Both the queue and CPRa fixture disappear when the process exits. No credentials
or accounts are needed for this mode.

## Find the parts you will adapt

| File or function | Responsibility | When developing your integration |
| --- | --- | --- |
| [main.go](source/examples/sdk/queue-registration/main.go.txt): `run` | Loads input, configures the SDK client, and gives the consumer a cancellable context | Set your service's credentials, shutdown policy, and overall deadline here |
| `registration`, `loadQueue`, `validateRegistration` | Define and validate the producer's message contract | Add a field here before using it to construct a monitor |
| `mockQueue.Next`, `mockQueue.Ack` | Demonstrate pending delivery and acknowledgment | Replace these with your broker operations |
| `desiredMonitor` | Converts a registration into a typed `api.Monitor` | Select your driver, cadence, stable identity, and ownership labels here |
| `reconcile` | Reads existing state and conditionally creates a monitor | Preserve the identity and ownership checks when adapting it |
| `consume` | Orders reconciliation before acknowledgment | Keep this ordering when replacing the queue |
| [main_test.go](source/examples/sdk/queue-registration/main_test.go.txt) | Exercises redelivery, conflicts, lost replies, and rejected input | Add cases for your producer and driver changes |

This directory is a runnable `package main`, not an importable SDK package. Your
application imports `github.com/ziad-hsn/cpra/sdk/go` as `cpra` and the leaf
`github.com/ziad-hsn/cpra/sdk/go/api` package for resource types. The example's
`internal` helpers belong to this example module; an external application should
configure `cpra.New` itself. Follow the [module setup guide](index.md) for the
current unpublished candidate.

## Construct a typed check

These excerpts come from `main.go`; they use variables from their surrounding
functions and are not standalone programs. In `desiredMonitor`, `e` is the
validated registration:

```go
driver, err := api.Driver("check", "http", api.PulseHTTPConfig{URL: &e.HealthURL})
if err != nil {
    return api.Monitor{}, err
}
spec := api.MonitorSpec{Check: api.CheckSpec{Driver: driver, Interval: "60s", Timeout: "5s"}}
```

`api.Driver` selects the `http` variant of the check-driver union and validates
the supplied configuration. `PulseHTTPConfig` provides the Go fields for that
variant. Its `URL` field is a pointer because optional configuration fields must
distinguish omission from an explicit value. Durations use CPRa duration strings,
so the check runs on a `60s` cadence with a `5s` timeout.

The remaining function sets `apiVersion`, `kind`, and metadata, and computes a
digest of the generated specification. `serviceID`, rather than the delivery's
`eventID`, determines the monitor ID: multiple deliveries for one service must
refer to the same monitor. Ownership labels record which integration and service
created it. The digest is an additional comparison value; it is not permission
to overwrite an existing resource.

## Read before deciding to write

In `reconcile`, `want` is the monitor produced by `desiredMonitor`. The SDK returns
typed errors, so the code uses `errors.Is` instead of matching error strings:

```go
current, err := c.Monitors.Get(ctx, want.Metadata.ID)
if errors.Is(err, cpra.ErrNotFound) {
    _, err = c.Monitors.Create(ctx, want)
    if err == nil {
        return "created", nil
    }
    if !errors.Is(err, cpra.ErrConflict) && !errors.Is(err, cpra.ErrAmbiguous) {
        return "", err
    }
    // Reconcile a lost create response or a competing create by reading the
    // original stable ID. Never issue a second blind mutation.
    current, err = c.Monitors.Get(ctx, want.Metadata.ID)
}
```

`ctx` controls cancellation and deadlines for each request. `Create` is
conditional on the resource being absent. `ErrConflict` means another write may
have won that condition. `ErrAmbiguous` means the client cannot establish the
mutation's outcome; for example, the server may have committed before the
connection closed. Both paths read the original ID and then verify its content.
A successful HTTP read alone does not satisfy the registration.

The consumer acknowledges only after that verification succeeds:

```go
state, err := reconcile(ctx, c, e)
if err != nil {
    return fmt.Errorf("registration failed; message remains pending: %w", err)
}
if err = q.Ack(e.EventID); err != nil {
    return err
}
```

Here `e.EventID` identifies the pending delivery, while `e.ServiceID` identifies
the resource. Keeping these roles separate lets a replay reuse the resource
without losing the broker's delivery identity.

## Write a registration message

The checked-in [registrations.jsonl](source/examples/sdk/queue-registration/registrations.jsonl) contains two Services
and one repeated delivery. Each line has four fields:

```json
{"eventID":"registration-1","serviceID":"payments","name":"Payments API","healthURL":"https://payments.example.test/health"}
```

| Field | Meaning |
| --- | --- |
| `eventID` | Delivery identity. Reusing it with different content is rejected. |
| `serviceID` | Stable service identity used in the monitor ID. Keep it unique across producers sharing a CPRa instance. |
| `name` | Human-readable monitor name. |
| `healthURL` | HTTP or HTTPS target with no embedded credentials, query, or fragment. |

`serviceID` accepts lowercase letters, digits, and hyphens, beginning with a letter
or digit, up to 63 characters. Prefix it with a team or environment when needed,
for example `payments-staging-api`. `eventID` is limited to 128 bytes and `name` to
256 bytes.

The loader validates the whole file before the first CPRa request. It accepts at
most 1,000 messages and 4 MiB of input, with a 64 KiB scanner limit per line. A
malformed final line rejects the file before any monitor is created. Duplicate
JSON keys and unknown fields are errors. Repeated events with identical content
are allowed so you can exercise redelivery.

The producer is trusted to nominate check targets. Restrict who can write to a
real registration stream, and apply your server's target and network policies;
this example is not a general-purpose public URL submission service.

## Follow the acknowledgment decision

Read `consume`, `reconcile`, and `desiredMonitor` in [main.go](source/examples/sdk/queue-registration/main.go.txt):

1. `Next` returns a registration and leaves it pending. Another `Next` returns the
   same pending registration until `Ack` receives its event identity.
2. `desiredMonitor` creates an HTTP check with a 60-second interval and a
   five-second timeout. Its stable ID is `service-` followed by `serviceID`.
3. `reconcile` gets that monitor. If it is missing, `Create` uses
   `If-None-Match: *` so a competing writer cannot be overwritten.
4. If creation conflicts or the response is lost, the program reads the original
   ID. It does not issue another blind create. Matching identity, ownership labels,
   and actual check content establish whether this registration is already present.
5. Only a successful create or a matching existing monitor leads to `Ack`.

Matching a monitor does not enable it. An operator may have disabled it, snoozed
it, or changed its notification settings. This registration consumer leaves those
choices alone. It also compares the actual check, so retaining an old digest label
while editing the target cannot cause an incorrect acknowledgment.

If a registration proposes a different URL for an existing `serviceID`, the
consumer stops with the message pending. Review the change and use an explicitly
versioned management update. This lesson creates missing monitors; it is not a
configuration replacement controller.

## Make a change and verify the consequence

Start with the existing failure tests before changing the integration:

```sh
go test ./queue-registration -run 'Test(DuplicateRegistration|LostCreateResponse|FailedOrForeignRegistrations)' -v
```

They should pass while proving three different outcomes: a duplicate makes no
new write, a lost create reply is followed by a read, and a foreign or changed
monitor keeps its message pending. The lost-reply test commits the create through
the fixture, closes the actual HTTP connection, and counts subsequent requests.

For a first development exercise, change the generated interval in
`desiredMonitor` from `60s` to `90s`, then rerun the demo and package tests. The
demo still ends with `messages_acknowledged=2 monitors=1`, because both deliveries
describe the same newly created check. Add a test that seeds a monitor with the
old interval and submits the new registration: expect an error and zero
acknowledgments. This demonstrates why changing your code does not silently
replace monitors already owned by an operator.

To support another driver, change the typed configuration in `desiredMonitor`
and extend the message validation together. For example, a TCP registration needs
a validated host and port, then `api.Driver("check", "tcp", api.PulseTCPConfig{...})`;
the ellipsis marks fields you must supply. Decide whether this represents the
same service identity before reusing its monitor ID. Keep tests for duplicate
delivery, changed check content, and uncertain create outcomes.

## Connect to management v2 when available

After the server contract is implemented and qualified, use an operator token
that can read and create monitors:

```sh
go run ./queue-registration \
  -input queue-registration/registrations.jsonl \
  -server https://cpra.example.net \
  -token-file /run/secrets/cpra-operator-token
```

Use `-input -` to read standard input. Authenticated HTTP requires the explicit
`-allow-http` option; HTTPS is the default. The shared helper rereads the token
file for requests and fails if the file becomes unavailable. It never falls back
to anonymous requests. The configured run has a ten-minute overall deadline.

## Replace the mock with Redis

Redis `XREADGROUP` delivers messages through a consumer group and tracks deliveries
that have not been acknowledged. `XACK` removes acknowledged entries from that
group's pending list. These are the two behaviors represented by `Next` and `Ack`.
[Redis XREADGROUP](https://redis.io/docs/latest/commands/xreadgroup/),
[Redis XACK](https://redis.io/docs/latest/commands/xack/)

A real adapter would read bounded batches with `XREADGROUP`, call the same
`reconcile` function, and issue `XACK` only after success. It also needs an explicit
policy for recovering abandoned pending deliveries, backing off during CPRa
outages, and recording rejected registrations for an operator. Those broker
lifecycle features are not implemented by the in-memory queue.

Do not equate an acknowledgment with a passing health check. It records that the
registration was accepted or already represented by the intended monitor. Health
results come later from CPRa's observation API.

## What was tested

Tests cover duplicate delivery, disabled-state preservation, conditional creation,
an actual HTTP connection closing after the fixture commits a create, safe
readback after that lost response, wrong ownership, changed checks, unexpected
resource identity, failure remaining pending, input bounds, and the demo output.
They establish these local client and queue behaviors. They do not establish
Redis durability or compatibility with an implemented CPRa v2 server.

<!-- Generated from examples/sdk/queue-registration/README.md by scripts/sdk/sync_guides.py. -->
