# Interventions selected by check failure

A monitor can select one intervention from a typed failed health check. Rules are
an operator authorization for a specific observed condition; they do not diagnose
its root cause. A refused TCP connection can justify the action you have chosen,
but does not prove that restarting that service is the correct remedy.

Existing monitors with a single recovery action keep that behavior. To opt into
rules, replace the single action with `intervention.rules` in a file, or
`spec.recovery.rules` in a management resource. The two forms cannot be combined.
No match means **no intervention**. Notification policies remain separate.

## Define and test a policy

The [disabled file example](reference/source/examples/intervention-rules/monitor.yaml.txt) includes
an explicit no-intervention rule for HTTP 401/403, Docker recovery for a refused
connection, and A2A recovery for HTTP 503. Use your own designated resources and
protected credential configuration before enabling it. A2A does not run after a
Docker failure: each is an alternative selected from the check observation.

```yaml
intervention:
  taxonomy_version: "1"
  max_attempts: 1
  rules:
    - id: no-auth-restart
      match:
        codes: [unexpected_http_status]
        http_statuses: [401, 403]
      none: true
    - id: restart-refused
      match:
        codes: [network.connection_refused]
        phases: [connect]
      action: docker
      target:
        container: designated-container
        timeout: 10s
        operation_timeout: 60s
```

Rules run in array order and stop at the first match, including `none: true`.
A rule's conditions combine with AND; alternatives inside one condition combine
with OR. Up to 32 rules are allowed. IDs must be stable and unique. Codes are
exact or a terminal dot-delimited prefix, such as `tls.cert.*`. There are no
regular expressions, arbitrary globs, raw-message comparisons or templates.
An invalid action in any branch prevents activation, even if it seems unlikely
to match. Provably unreachable rules are rejected; partial overlaps are warnings.

Management resources use the same matching behavior with camelCase fields:
`taxonomyVersion`, `httpStatuses`, `grpcCodes`, `grpcStatuses`, and a rule's
`driver: {type, config, credentialRefs}` instead of `action` and `target`.
The existing secret-reference and complete-collection validation rules apply.

Run selection tests without a server, storage or provider credentials:

```sh
cpractl rules test -f examples/intervention-rules/monitor.yaml \
  --monitor-id checkout --code network.connection_refused --phase connect \
  --expect-rule restart-refused-container
cpractl rules test -f examples/intervention-rules/monitor.yaml \
  --monitor-id checkout --code unexpected_http_status --phase response \
  --http-status 401 --expect-rule leave-authentication-to-operator
cpractl rules test -f examples/intervention-rules/monitor.yaml \
  --monitor-id checkout --code unexpected_http_status --phase response \
  --http-status 404 --expect-rule @none -o json
```

`--expect-rule @none` asserts that no rule matched. An explicit no-intervention
rule is asserted by its ID. The command tests a single observation and explains
its match; it does not simulate threshold qualification or grant execution.
Only local files, explicitly traversed directories and stdin are accepted.

## Classification and qualification

Discovery advertises the installed taxonomy, supported driver/code mappings,
phases and allowlisted protocol details. `taxonomyVersion: "1"` pins prefix
expansion to version 1. A newer binary cannot silently broaden that policy.
Changing the taxonomy requires a reviewed configuration application.

CPRa classifies typed errors and explicit protocol observations. Generic
unclassified failures remain visible but cannot authorize a rule action. A DNS
temporary error is not assumed to be SERVFAIL. Authentication rejection is not
proof of a bad configuration. Cancellation, missing observations and local
preparation/resource failures cannot authorize an intervention. Public messages
are canonical and bounded; remote response bodies and arbitrary error strings
are not policy inputs.

The existing unhealthy threshold also qualifies a rule. With a threshold of
three, three consecutive eligible failures matching the **same rule** qualify
it. Their codes may differ. Failures matching different rules can still open an
incident, but do not combine to qualify an intervention. Success, unavailable
observations, configuration replacement and relevant suppression transitions
reset qualification. After maintenance or Ignore ends, fresh evidence is needed.

The dashboard can edit and reorder rules, show conditional diffs, and explain a
selection. When a check contains fields the dashboard cannot edit safely, use
**Edit intervention rules only**: it patches the understood recovery subtree
with the original resource version. Opaque check settings are never resubmitted.
Possible-overlap editing hints are advisory; the server validates exact coverage.

Monitor observations show the failure, candidate rule, qualifying count,
decision/configuration revisions, admission reason and selected rule. A
candidate is not an execution grant. A guarded recovery request names that
qualified rule and its exact decision, configuration and control revisions,
plus a reason. A stale request fails; it does not fetch newer revisions and retry.

## Execution and uncertain outcomes

CPRa commits action intent and start before invoking the selected job. Only that
branch's runtime client is prepared. A new intervention captures its configured
budget at admission and establishes its absolute deadline at the first committed
start, after execution capacity is available. Existing actions retain their
recorded deadlines. Unsent obsolete work may be cancelled, but cancellation
does not refund attempts or renew an established incident deadline.
The first committed start also pins rule, driver and target for the incident.
A different error cannot select another intervention as an escalation.

If the selected branch cannot be prepared locally, its admission reason is
`preparation_unavailable`; health checks continue. This reason belongs to that
unsent action and clears when it is superseded, cancelled or prepared. It does
not establish a target outage or mean that storage has failed.

The existing controller-owned retry policy remains in force: retry only proven
transient no-effect failures within attempt/deadline limits. Partial writes,
lost replies and an HTTP 500 after a possible effect remain uncertain. A
successful health check does not prove that an earlier intervention succeeded.
Checks and incident progression continue while an unknown action protects
against conflicting interventions. Audited evidence/review is still required.

Maintenance, Ignore, snooze, disable, notification attention, blue review alerts,
gray control notices, green eligibility and cyan verification-pending messages
retain their existing meanings. Verified incident recovery clears rule selection
for a later incident; it does not clear an older unknown-action hold.

## External checks

The [external example](reference/source/examples/intervention-rules/external.yaml.txt) declares
`custom.cache.full` on an immutable protocol-2 check JobType. Both server and
SDK/worker applications require `externaljobs`; runtime enablement and scoped
worker credentials are separate requirements. There is no silent downgrade to
protocol 1. Existing protocol-1 executions and JobTypes retain their contract.

A version may declare at most 32 custom codes under `custom.`. Each is scoped to
that JobType identity and version. The server validates reports against the
original authenticated execution and JobType, supplies the public message, and
performs authoritative rule selection. The worker cannot submit a rule index or
permission flag. Invalid/unavailable reports cannot become actionable target
failures. Handler authors remain trusted: a declared code does not independently
prove the handler observed that condition.

A tagged `cpractl rules test` accepts the same local files, with the exact JobType
included. It reads no live JobType or credentials. This combined file is an offline
simulation input. Register the JobType separately through the tagged SDK before
applying its Monitor document; JobTypes are not generic collection resources. The [worker library guide](sdk/worker-library.md)
explains registration, identical result replay and explicit journal conversion.

## Storage upgrade

Rules use server formats 48 (base) and 49 (`externaljobs`). Take a complete stopped
authenticated backup before upgrading. Supported older records keep their
identities, absolute deadlines, attempt accounting and unknown holds. Activating
rules requires fresh observations. Replay does not reclassify historical errors,
consult the current clock, create clients or invoke providers.

Worker journal format 3 carries structured reports. Format 2 requires explicit
stopped conversion; normal worker startup refuses it. The original encrypted
backup and wrapping key must remain available. Older binaries reject newer server
state. Rollback requires an older-compatible backup, not just an older executable.

These changes do not establish native-platform, provider-account or fleet-scale
qualification. Current availability remains in [the support record](versions.md).
