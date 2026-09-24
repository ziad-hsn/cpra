# Disable an AWS target's existing monitor

This service consumes load-balancer deregistration events, finds the mapped monitor through the CPRa SDK, and disables it after AWS confirms that the target is no longer registered. It supports target groups used by Application, Network, and Gateway Load Balancers, plus instances registered with Classic Load Balancers.

QUIC/TCP_QUIC targets need an additional `QuicServerId` identity. This first example does not map that field: an event containing `quicServerId` is rejected without a CPRa read or write and stays queued for operator review. Do not remove the field to force a match. Extending this case requires carrying it through the mapping, event match, AWS query, and evidence comparison. [AWS target identity reference](https://docs.aws.amazon.com/elasticloadbalancing/latest/APIReference/API_TargetDescription.html).

The local demo works now. The private CPRa working branch implements the v2 monitor reads and conditional changes used here; configured mode still requires discovery and qualification against the selected server. The example and its tests exercise HTTP contracts and local AWS fixtures. They do not prove delivery in an AWS account.

## Run the local lesson

Start in `examples/sdk`, using the source workspace described in [the examples guide](../README.md):

```sh
go run ./elb-deregistration -demo
```

The command starts a temporary HTTP fixture, creates a monitor with `client.Monitors.Create`, reads its assigned UID and resource version, and processes the same deregistration event twice. The AWS registration answer is an explicit local fixture. No AWS credentials, account, or provider requests are involved.

Expected output:

```text
fixture mode: in-memory CPRa HTTP contract and local AWS absence; no live providers
disabled monitor payments-node after AWS registration confirmation
monitor payments-node is already disabled; no change
verified through SDK GET: enabled=false; duplicate event made no further change
```

The process then exits and removes its temporary HTTP fixture. There are no retained resources to clean up.

Read [demo.go](demo.go) first. It constructs a client, creates one monitor, and feeds an event to the same processor that configured SQS mode uses. Then read [processor.go](processor.go), where the decision to disable is made.

## Find the code you want to change

This directory is a command (`package main`). Copy its integration pattern into
your own application; import the public SDK rather than this command's private
functions.

| File and entry point | Responsibility |
| --- | --- |
| [main.go](main.go), `run` | Reads flags and mapping, constructs the CPRa client, verifies AWS account identity, and chooses SQS or one-event input. |
| [events.go](events.go), `decodeEvent` and `event.validate` | Decodes and validates the event before it can select a monitor. |
| [processor.go](processor.go), `process` and `matches` | Matches a successful deregistration event to an approved target binding. |
| [processor.go](processor.go), `disable` | Reads CPRa state, checks identity/version, obtains AWS evidence, and submits the conditional mutation. |
| [aws.go](aws.go), `awsRegistration.absent` and `consume` | Implements AWS registration reads and SQS acknowledgement timing. |
| [demo.go](demo.go), `runDemo` | Provides a finite exercise without account configuration. |
| [processor_test.go](processor_test.go) and [review_test.go](review_test.go) | Exercise duplicate delivery, ambiguous replies, concurrent edits, and input boundaries. |

The Go snippets below are excerpts from these files, with imports and surrounding
setup omitted. Run the complete command above; the excerpts are not standalone
programs.

### Construct one client for the consumer

`run` builds a client once and passes it to `processor`. The shared example
helper supplies a token-source callback that rereads the private token file for
each request:

```go
clientConfig, err := clientconfig.FromTokenFile(*server, *tokenFile, *allowHTTP)
if err != nil {
    return err
}
client, err := cpra.New(clientConfig)
if err != nil {
    return err
}
defer client.CloseIdleConnections()
```

In your own module, import `cpra "github.com/ziad-hsn/cpra/sdk/go"` and supply a
`cpra.Config` with `BaseURL` and your own `TokenSource`. `clientconfig` is private
to the example module and cannot be imported by another module. The configured
origin uses HTTPS by default. Every SDK call receives the processing context, so
the queue's work deadline also limits requests to CPRa.

### Protect the monitor before changing it

After `Monitors.Get` succeeds, `disable` examines the returned data:

```go
if monitor.Data.Metadata.UID != m.MonitorUID {
    return errors.New("mapped monitor incarnation changed; review mapping")
}
if monitor.Data.Spec.Enabled != nil && !*monitor.Data.Spec.Enabled {
    fmt.Fprintf(p.output, "monitor %s is already disabled; no change\n", m.MonitorID)
    return nil
}
if monitor.Data.Metadata.ResourceVersion != m.ResourceVersion {
    return errors.New("mapped monitor version changed; review mapping")
}
```

`monitorID` names the resource. `monitorUID` identifies this creation of that
resource, so deleting and recreating the same ID cannot reuse the old binding.
`resourceVersion` protects the approved configuration against later edits. Treat
UIDs and versions as opaque strings; obtain them from API responses.

`Enabled` is a pointer because omission and an explicit Boolean value have
different wire representations. The condition above recognizes an explicit
`false`. It runs after the UID check but before the version check: a duplicate
delivery can observe the already-disabled original monitor without needing a
second write using its old version.

Once AWS establishes absence, the only mutation is:

```go
_, err = p.cpra.Monitors.Disable(ctx, m.MonitorID, m.ResourceVersion)
if err != nil {
    return fmt.Errorf("disable mapped monitor: %w", err)
}
```

The method maps to a conditional monitor patch. A version conflict is returned to
the caller; the SDK does not fetch a newer version and retry the change. The
`%w` wrapping preserves the underlying error for callers using `errors.Is` or
`errors.As`.

### Acknowledge the event after the result is known

`consume` owns SQS delivery. An error from `process` takes this branch before
`DeleteMessage` can run:

```go
err := p.process(workCtx, aws.ToString(message.Body))
cancel()
if err != nil {
    fmt.Fprintf(output, "event retained for retry or DLQ: %v\n", err)
    continue
}
```

After a successful decision, the consumer deletes the delivery using its SQS
receipt handle. If CPRa committed the disable but the HTTP reply was lost, the
first attempt takes the error branch. On redelivery, `Monitors.Get` can establish
that the same monitor is disabled and finish without another mutation. A lost
SQS deletion reply can also cause a duplicate delivery, which follows that same
read-before-write path. This is how the example handles ambiguous replies; it
does not assume either system provides exactly-once delivery.

## Follow one event

The event describes an AWS API call. A successful `DeregisterTargets` call can arrive while connections are still draining. AWS also returns success if the requested target does not exist. The service therefore makes a separate registration read before changing CPRa. [AWS DeregisterTargets reference](https://docs.aws.amazon.com/elasticloadbalancing/latest/APIReference/API_DeregisterTargets.html).

1. Check the EventBridge source, CloudTrail operation, account, region, event identity, and call result. Rejected deregistration calls cause no monitor change.
2. Find an exact configured target match. Target-group ARN, target ID, optional port override, and availability zone are part of that match. A Classic mapping uses load-balancer name and instance ID.
3. Read the monitor by its stable CPRa ID. A missing monitor is a completed no-op. The service never creates a monitor in response to a deregistration event.
4. Compare the returned incarnation UID and configuration version with the mapping. A changed monitor requires an operator to review the mapping. An already-disabled monitor needs no write.
5. Ask AWS for current registration evidence. For target groups, only `unused` with reason `Target.NotRegistered` establishes absence. Draining or still-registered targets keep the SQS message for retry. An unhealthy, stopped, or unused-in-another-zone target is not enough evidence to disable it. [AWS target states](https://docs.aws.amazon.com/elasticloadbalancing/latest/APIReference/API_TargetHealth.html).
6. Call `client.Monitors.Disable(ctx, monitorID, resourceVersion)`. The SDK sends a merge patch setting `spec.enabled` to `false`, with `If-Match` protecting the version read earlier.
7. Delete the SQS message only after processing succeeds. A timeout, version conflict, or incomplete mutation reply leaves it available for retry or the queue's dead-letter policy.

Classic load balancers use `DescribeLoadBalancers` to check the remaining registered instances, as recommended in the [Classic deregistration reference](https://docs.aws.amazon.com/elasticloadbalancing/2012-06-01/APIReference/API_DeregisterInstancesFromLoadBalancer.html).

A Classic instance may remain registered while draining. Its event stays in SQS until a later registration read establishes absence. A Classic instance that was re-registered can therefore send a stale event to the dead-letter queue for review. [Classic connection draining](https://docs.aws.amazon.com/elasticloadbalancing/latest/classic/config-conn-drain.html).

## Prepare configured operation

You need a server that implements the v2 routes used here, an existing CPRa monitor, and AWS infrastructure under your control. Give the service a CPRa identity permitted to read and disable only its designated monitors.

Create a CloudTrail trail covering the relevant management events. Add an EventBridge rule on the regional default event bus using [event-pattern.json](event-pattern.json), and target a dedicated SQS queue. Deliver the entire EventBridge event body without an input transformer or SNS wrapper. AWS documents CloudTrail setup in its [API-event tutorial](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-log-api-call.html).

The pattern includes both source names AWS documents: `aws.elasticloadbalancing` and `aws.elb`. The processor additionally checks the corresponding `eventSource` pair. [ELBv2 EventBridge reference](https://docs.aws.amazon.com/eventbridge/latest/ref/events-ref-elasticloadbalancing.html), [ELB EventBridge reference](https://docs.aws.amazon.com/eventbridge/latest/ref/events-ref-elb.html).

Use [queue-policy.example.json](queue-policy.example.json) as a starting point for the queue's resource policy. Replace the account, region, queue, and rule ARN. Merge the statement with existing policy statements you intend to preserve. Restrict queue writers: JSON fields supplied by another queue writer are not proof that AWS emitted an event. The exact rule ARN condition follows [EventBridge's SQS policy guidance](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-use-resource-based.html).

The consumer needs `sqs:ReceiveMessage`, `sqs:DeleteMessage`, and the two ELB describe operations shown in [consumer-policy.example.json](consumer-policy.example.json). Remove the Classic describe operation if you use target groups only, or remove the target-health operation for Classic-only use. Additional KMS permissions may be needed for a queue encrypted with your own key. Use your normal AWS SDK credential chain, such as a configured profile or workload identity. The program checks `GetCallerIdentity` and refuses to proceed if the account differs from the mapping.

Configure a consumer redrive policy and a monitored dead-letter queue. This program receives one event at a time, long-polls for 20 seconds, gives processing 45 seconds, and sets visibility to 60 seconds. Each retained retry provides another opportunity to observe completed draining. Choose a retry count that covers your target group's configured deregistration delay and expected outages. See [SQS long polling](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-short-and-long-polling.html).

## Bind a target to a monitor

Copy [mapping.example.json](mapping.example.json) and replace every fixture value. Read the monitor through the SDK and copy `metadata.id`, `metadata.uid`, and `metadata.resourceVersion` into the mapping. Set `eventsAfter` to the time you approved this binding. Older queued events will be ignored.

```json
{
  "targetGroupARN": "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/payments/abc",
  "targetID": "i-123",
  "port": 8080,
  "monitorID": "payments-node",
  "monitorUID": "copy-the-current-uid",
  "resourceVersion": "copy-the-current-version",
  "eventsAfter": "2026-09-13T12:00:00Z"
}
```

Include `port` when the deregistration request uses a port override. Omit it when the request uses the target group's default. Include `availabilityZone` when it is part of the registered target and deregistration request. These fields match exactly; this example does not infer a missing override from a node ID.

A Classic mapping replaces `targetGroupARN` with `loadBalancerName` and omits `port` and `availabilityZone`. One mapping may own one monitor, and one monitor may appear only once. Sharing a monitor across several load balancer registrations needs a different policy for deciding when all registrations are absent.

Pinning the version has an operational cost: after editing or re-enabling the monitor, review and refresh its mapping before expecting new automation. This prevents a delayed duplicate event from disabling a monitor that someone deliberately changed. The service does not silently accept a newer version after a conflict.

Run configured mode from `examples/sdk`:

```sh
AWS_PROFILE=cpra-events go run ./elb-deregistration \
  -server https://cpra.example.com \
  -token-file /run/secrets/cpra-management-token \
  -mapping /etc/cpra-events/mapping.json \
  -queue-url https://sqs.us-east-1.amazonaws.com/123456789012/cpra-deregister
```

The application accepts standard regional HTTPS SQS queue URLs, including the `.amazonaws.com.cn` suffix for China regions. Custom endpoints, older SQS hostname forms, and cross-account consumption are outside this example. Before calling STS, the program inspects the resolved STS, SQS, ELB, and ELBv2 client options and rejects effective global or per-service endpoint overrides, including values from environment variables and shared AWS configuration. This prevents a local endpoint from being presented as AWS registration evidence. An override that the SDK is explicitly configured to ignore has no effect. Credentials stay in their configured provider chain and token file; they are not put in the mapping. [AWS endpoint configuration](https://docs.aws.amazon.com/sdkref/latest/guide/feature-ss-endpoints.html).

For a one-event diagnostic run, replace `-queue-url` with `-event-file /path/to/captured-event.json`. This still queries AWS. Adding `-fixture-aws-absent` explicitly substitutes local absence evidence and prints that limitation; use it with fixtures only.

## Interpret failures and delivery limits

| Result | Meaning and next step |
| --- | --- |
| `does not exist; no change` | The mapped monitor is absent. No monitor is created. |
| `already disabled; no change` | A prior delivery or operator already disabled it. No additional patch is sent. |
| `version changed` or `incarnation changed` | Inspect the monitor and queued event, then update the mapping only if that binding is still intended. |
| `still draining` | AWS has accepted deregistration but it is incomplete. SQS retains the event for another attempt. |
| Target-group target `remains registered` | Current AWS state does not establish deregistration. The event remains queued for retry or dead-letter review, including after re-registration. |
| `Classic instance remains registered` | The registration list cannot yet prove absence. The event remains queued for retry or dead-letter review. |
| `mutation outcome is uncertain` | The write reply was incomplete or lost. On redelivery, read current state first. An already-disabled monitor completes without another write. |
| `does not provide an available CPRa v2 API` | Check the server version, readiness, authorization, and network path. A server exposing only v1 cannot run this configured workflow. |
| `custom AWS service endpoints are unsupported` | Remove the effective endpoint override from the AWS profile/environment, or run the explicit local demo. No STS or provider operation was invoked. |
| `QUIC/TCP_QUIC target identity is unsupported` | Retain the message for review. This example cannot safely match the additional target identity. |

CloudTrail delivery is best effort and its events are not an ordered log. This listener cannot guarantee detection of every registration change. A deployed integration that needs reconciliation should periodically compare its approved mappings with AWS state and record the evidence used for changes. [CloudTrail event ordering](https://docs.aws.amazon.com/elasticloadbalancing/latest/userguide/cloudtrail-logs.html), [EventBridge delivery boundary](https://docs.aws.amazon.com/eventbridge/latest/ref/events-ref-elasticloadbalancing.html).

AWS registration and CPRa configuration have no shared transaction. A target can be re-registered after the confirmation read and before the conditional patch. The pinned CPRa version prevents concurrent CPRa edits from being overwritten; it cannot fence AWS changes. This example does not automatically re-enable monitors or resume old events against new identities.

Stop the service with Ctrl-C or SIGTERM. A partially processed delivery remains in SQS. For a configured exercise, disable the example EventBridge rule, inspect retained messages and the dead-letter queue, and remove only infrastructure created for the exercise. Re-enable a disabled monitor explicitly after reviewing whether its target should be checked again.

## Develop your own event integration

Start by changing `metadata.id` in [monitor.fixture.json](monitor.fixture.json)
and rerunning the demo. `runDemo` obtains the created monitor's ID, UID, and
version from the response, so the binding follows the new ID. The final SDK read
must show that monitor disabled, and the duplicate must still produce no
additional patch. Then choose the layer that owns your change:

1. To accept another event source, add its decoding and validation in `events.go`
   and an exact target match in `processor.go`. Keep untrusted event fields out of
   the CPRa server origin and authentication configuration.
2. To prove a different provider condition, implement `registrationReader.absent`
   for that condition. Its `(bool, error)` result separates established absence
   from an unavailable or inconclusive observation. Test both cases before using
   the result to authorize a mutation.
3. To change what a monitor does, use the appropriate SDK method with the current
   approved version. Define an explicit policy for old events and operator edits;
   replacing the pinned version with the latest one would change that policy.
4. Keep acknowledgement in the consumer, after the processor's decision. Add a
   test for a committed mutation with a lost reply before adding retries or more
   concurrent message handling.

Useful starting tests are `TestConfirmedDeregistrationAndDuplicate`,
`TestLostDisableReplyDoesNotRepeatMutation`,
`TestMissingChangedAndReenabledMonitors`, and
`TestQueueAcknowledgesOnlySuccessfulProcessing`. They assert observable reads,
writes, and acknowledgements rather than only checking an error string.

## Verify the code

```sh
go test -race ./elb-deregistration
```

Tests cover target groups and Classic load balancers; omitted and overridden ports; unrelated, stale, failed, duplicate, and malformed events; missing and recreated monitors; operator edits; draining and unhealthy targets; version conflicts; a committed disable with a lost reply; SQS acknowledgement; unsupported QUIC identity; endpoint override rejection before network or credential retrieval; and the finite demo. One test uses the actual AWS Go SDK serializer and XML decoder against a local HTTP fixture. These are local contract and behavior tests, not live AWS verification.
