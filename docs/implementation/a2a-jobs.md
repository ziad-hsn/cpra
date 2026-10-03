---
title: A2A jobs implementation
description: A2A configuration, committed execution, operator observations, and remaining escalation qualification.
cpra_scope: plan
---

# A2A intervention and notification jobs

Updated: 2026-09-30. This extends the approved
[A2A backlog](dashboard-shipping-plan.md#remaining-addition-a2a-agent-intervention-and-notifications).
The protocol transport, persisted action/receipt model and controller execution
are implemented locally. The later configuration checkpoint below adds manifest/API registration, encrypted secret references and consumer models. The inventory now includes 35 driver roles. The later escalation checkpoint connects confirmed recovery failure to agent notification. Configured agent effects, post-restart remote-task resolution, complete qualification and independent review remain open.

## Protocol and build decisions

Use A2A 1.0 HTTP+JSON as specified by the
[v1.0.1 protocol definition](https://github.com/a2aproject/A2A/blob/v1.0.1/specification/a2a.proto)
and [binding specification](https://github.com/a2aproject/A2A/blob/v1.0.1/docs/specification.md).
Every request carries `A2A-Version: 1.0`. The configured URL selects an explicit
agent interface; SendMessage and GetTask append their binding paths to it.
An optional tenant is carried in the send body and task query.

A2A is a built-in outbound driver in the existing jobs package. It invokes
an external agent over HTTP; it does not load operator handlers into CPRa or
enable the optional external-worker protocol. The `externaljobs` build boundary
continues to apply to custom JobTypes and worker execution.

The current upstream [Go SDK v2.6.0](https://github.com/a2aproject/a2a-go/releases/tag/v2.6.0)
requires Go 1.26 and includes dependency security updates. CPRa's transport uses
the standard library so the application can retain Go 1.25 compatibility without
selecting an older SDK. The pinned current SDK runs in a separate test module
as the reference server. The test-only `a2ainterop` tag does not enable a driver.

## Implemented transport boundary

[a2a.go](../review/source/internal/jobs/a2a.go.txt) implements two operations:

- Send one message using an identity supplied by the durable action owner.
  Request an immediate response and zero history. A response is exactly one
  task or agent message; a message does not establish task completion.
- Read the original task, again requesting zero history. Require the same task
  and, when previously supplied, context identity. Polling never submits a new
  message and a failed lookup never authorizes replay.

SendMessage deduplication is optional in the protocol. CPRa therefore makes one
send attempt, disables transparent transport replay, and sets no idempotency
header. A lost response, local deadline, or cancellation does not establish the
remote outcome and does not cancel remote work. No automatic cancellation or
additional authentication exchange is implemented.

The client requires HTTPS unless insecure HTTP is explicitly selected for the
configured destination. It validates TLS using normal trust roots, rejects
redirects, sends a dedicated bearer credential, uses no ambient proxy or cookie
jar, and honors CPRa's configured SSRF protection. URLs cannot include userinfo,
queries or fragments. Tokens and remote error bodies are absent from errors.

The caller's context and a bounded request timeout apply to every request.
Requests are limited to 64 KiB and four parts; responses to 256 KiB. JSON parsing
rejects duplicate fields, conflicting protobuf field aliases and excessive
nesting. Task and message identities are bounded to 256 bytes. Only task,
context, message identity and state enter a receipt; agent text, artifacts and
arbitrary metadata are discarded. Artifact URLs are never fetched. Unknown
task-state names remain explicit observations rather than being mapped to success.

These bounds apply to this protocol profile, not to every possible A2A agent.
Streaming, push callbacks and automatic Agent Card discovery are not selected
for the initial driver. The operator must configure an HTTP+JSON 1.0 interface.

## Persisted agent actions

[Agent action storage](../review/source/internal/persistence/agent_action.go.txt) uses the
existing action and history records. A start with `Driver: "a2a"` records an
`agent` executor and the local process session before any agent request. The
controller selects this start before calling the transport. An ordinary local
start is not sufficient for this driver.

The original action pins the monitor incarnation, configuration revision,
incident and notification position. Its identity supplies the outgoing message
ID. `LocalExecution.RecordAgentReceipt` commits only the returned task/context
or message identity, task state, observation time and sequence. Provider
configuration, credentials, text, artifacts and arbitrary response metadata
are excluded from the record. Each task observation preserves the original task
identity, uses the expected previous sequence and rejects backwards time.
An identical retry creates no extra history event. Terminal task states cannot
be replaced with a different observation.

Returning from the local request does not prove that remote work stopped. The
local completion marker still releases the process claim, but conclusive review
also requires a recorded terminal agent task. A missing receipt, a message-only
reply or a nonterminal task cannot supply that proof after process restart.
An interrupted action stays unknown; restoring snapshots and replaying logs
perform no agent requests. A completed task receipt does not itself resolve an
unknown action or change an operator review.

Intervention success requires a committed `TASK_STATE_COMPLETED` receipt before
the result can start CPRa's ordinary health-check verification. Notification
success records acceptance of investigation context through an agent message or
a submitted, working or completed task. Neither establishes that the monitored
service recovered. Failed, canceled, rejected and unrecognized task states do
not permit automatic repetition of a side-effecting action. Late failed-task
evidence cannot be converted into a claim that all external effects were absent.

Receipts from an invocation that is still locally active may be attached to its
original action after configuration replacement. They cannot attach to the new
monitor revision or change its incident. The old process session cannot submit
fresh observations after restart. The separate read authority described in
[reading retained tasks](#reading-retained-tasks-after-restart) binds a new
process to the original encrypted connection and exact action observation.

The initial agent-storage checkpoint used common format 31 and optional combined
format 32, which default builds reject. The later escalation checkpoint uses
formats 33 and 34. Formats 15–30 remain
optional-worker formats; the higher common format does not make them readable
without `externaljobs`. Snapshot framing, history cloning, command validation
and the frozen operation-digest projection include this boundary. The release
recipe now records format 35 for the ordinary built-in-driver build. The later
restart-observation checkpoint below introduces common format 35 and combined
worker format 36. These are local format changes, not a published
upgrade-compatibility claim.

## Controller execution

[A2AJob](../review/source/internal/jobs/a2a_job.go.txt) runs through the existing controller
action queues. Dispatch requires a matching committed start, original action
context, invocation-local receipt writer and completion callback. Calling the
job directly performs no network operation. The connected boundaries are:

1. Commit the action intent and start marker, including its monitor incarnation,
   configuration revision and original endpoint binding, before sending.
2. Use the committed action identity as the outbound message identity. Send a
   bounded, sanitized incident envelope with stable monitor, incident and action
   IDs. Never send the monitor manifest, resolved provider secrets or raw errors.
3. Commit the returned task/context reference and observation time before polling
   it. Snapshot/log replay must execute no network operations. A failed receipt
   commit leaves the action uncertain; it cannot trigger a second SendMessage.
4. Keep notification acceptance, completed agent work and observed monitor
   recovery separate. Recovery verification remains CPRa's health-check policy.
   A failed or canceled agent task does not prove all side effects were absent;
   it must not be repeated automatically.
5. Retain interrupted actions as unknown across restart. Configuration changes
   must not redirect an old message or task lookup to another target. Append
   later evidence under the original action rather than replacing an outcome.

The internal job options default to a 60-second task budget and five-second
polling, with task budgets from one second to five minutes and polling from
one to 30 seconds within that budget. Each HTTP request also retains its
bounded request timeout and the caller's earlier cancellation. The task budget
begins after the committed start. The action worker remains occupied during
this bounded operation; no unbounded polling goroutine is started. Unchanged
observations do not create additional history records. Notification acceptance
returns without polling. The configuration checkpoint below exposes the supported settings in manifests and management resources.

Cancellation of a receipt write leaves the action unknown; it does not by itself
mark healthy storage unavailable. The separately committed local completion
marker still controls executor release. A genuine failed store prevents that
completion and further admission through the existing storage-health path.

The notification role sends investigation context and grants no recovery
authority. The intervention role uses a separately configured, limited agent
identity. The remote agent must enforce its allowed operations; a prompt alone
is not an authorization boundary, and an agent response cannot mutate CPRa
configuration or bypass operator permissions.

For failure escalation, the initial default is after configured recovery
attempts are exhausted with confirmed failures. An explicit per-failed-attempt
policy can opt into earlier escalation. Correlate the escalation with the
original failed action and incident, record the chosen policy in history, and
prevent notification failures from recursively escalating themselves. A timeout
or lost Docker reply remains unknown and cannot masquerade as a confirmed
failure. Dismiss, snooze and disable apply at the existing admission boundaries;
acknowledgement records ownership without suppressing work.

## Verification checkpoint

The [local record](../review/local-evidence.md#record-d99bd72ce2a4027cdfb1181a)
records the checks for this component and their unchanged source hashes.
Local HTTP fixtures cover malformed/oversized input and responses, redirects,
lost replies, TLS rejection, cancellation, task identity and redaction. The
[upstream server fixture](../review/source/internal/jobs/testdata/a2aagent/README.md.txt)
uses the official SDK's HTTP+JSON handler over verified local TLS in a separate
process. It counts invocations independently across all seven response scenarios.
The CI job builds this fixture with a separate pinned compiler and runs the CPRa
tests with Go 1.25 and Go 1.27.1. Missing fixtures fail the selected campaign.
Both local compiler runs pass all 50 top-level jobs tests, including 14 A2A tests
and seven upstream-server subcases. Go 1.27.1 uses race instrumentation. Vet,
application dependency exclusion, the missing-fixture negative check and all
six private-publication policy tests pass. The workflow itself has not run on
GitHub. Tenant encoding has local contract coverage; routing in a configured
multi-tenant agent still requires its own interoperability evidence.
The reference-server module also passes dependency verification and vet. Its
symbol-level vulnerability scan reports no findings with Go 1.27.1 and the
recorded vulnerability database snapshot. This scan covers the fixture module;
it does not replace the final application and optional-driver scans.

The subsequent [storage record](../review/local-evidence.md#record-a9ec5149c084a9b33e33ba6b)
contains 66 selected default-build and 82 selected tagged persistence tests on
each compiler, with Go 1.27.1 race detection. Five actual process-kill boundaries
run in each compiler/build combination: started without a receipt, receipt
committed, snapshot followed by a later observation, result committed and local
completion without remote completion. Restart retains the original references,
rejects a second start and preserves the history without duplicate observations.
The controller systems package also passes all 32 default and 60 tagged tests
with race detection. Vet, 83 release-tool tests, six publication-policy tests,
default/all-built-in worker exclusion and local CGO-free application/CLI builds
pass. These source builds are not release artifacts.

The storage record retains initial failures and the final source inventory.
Regression cases reproduced a task receipt overriding an explicitly ambiguous
result and configuration carrying agent state outside its dedicated commands.
Both are rejected by the corrected implementation. Existing operation digests
remain unchanged, and storage-format tests explicitly reject common format 31
for optional worker data while admitting combined format 32.

This evidence does not close the A2A feature. Storage tests exercise Raft and
the action state machine; they do not invoke a configured agent.
The later controller-execution checkpoint below supersedes that integration
boundary. Public driver registration, failure escalation, schemas and credential
references, SDK/CLI/dashboard support, real configured agent effects, independent
review and the complete release gates remain required.


## Controller execution checkpoint

The [execution record](../review/local-evidence.md#record-ec9b4f3323df6787d2342d3d)
adds real controller dispatch through Raft-backed starts, receipt commits and
result processing. Local network fixtures verify start-before-send and
receipt-before-poll ordering, distinct notification/intervention outcomes,
cancellation, original-target correlation and safe queued-work resumption.
Six separate-process kill boundaries cover before admission, after the committed
start, after the remote send but before its receipt commit, after that receipt,
after task completion but before result commit, and after the committed result.
Only work killed before admission is resumed; uncertain actions are not sent
again. A completed task without a committed action result remains unknown.

The upstream SDK fixture now runs all seven protocol scenarios through the
transport, intervention job and notification job: 21 cases with independent
send/cancellation accounting. These are verified local TLS operations against
the upstream handler, not a hosted agent or a proof of its recovery effects.

A regression test first reproduced receipt cancellation permanently poisoning
usable storage. The corrected path commits an unknown action after local
completion, preserves storage health and sends no replacement message. Other
cases cover a failed receipt writer, receipt-writer panic, an unfinished task
hitting its deadline, and unchanged polling observations producing no new
history entries. Initial failing runs remain in the record.

Each of the four compiler/build-tag combinations passes all 58 jobs tests and
39 default or 67 tagged controller-system tests. Go 1.27.1 uses race detection;
Go 1.25 verifies minimum compatibility. The selected agent/local-executor
persistence race suites pass 23 default and 24 tagged tests. Vet, local CGO-free
application/CLI builds and private-publication policy checks pass. The recorded
source inputs remain unchanged during these checks.

The driver factory, public configuration and credential references, failure
escalation, SDK/CLI/dashboard contracts, actual configured agent effects and
independent review remain open. The internal execution tests do not qualify a
manifest-to-provider application run or complete the A2A release gate.

## Configuration and observations checkpoint

The local candidate registers `a2a` for recovery and notification. Its Go 1.25 configuration types live in `internal/manifest/a2a.go`; inert validation is shared with job construction. Validation and construction perform no provider I/O. Recovery exposes request/task deadlines and a polling interval. Notification exposes request and overall delivery deadlines without task polling settings.

API `credentialRefs.url` and `credentialRefs.token` resolve encrypted, write-only credentials. Both roles reject inline protected fields in managed resources. Manifest import extracts them through the normal encrypted catalog path. Use separate credentials for intervention and notification authority; a notification grants no CPRa management permission.

Canonical OpenAPI generates both SDK build variants and dashboard fields. Collection loading accepts YAML/JSON duration strings and normalizes numeric manifest durations before API submission. The dashboard provides recovery and notification forms with secret references. Configuration validation policy 3 prevents reuse of an earlier collection decision under changed semantics.

Actions and retained events expose `agentReceipt`: decimal-string sequence, observation time, original task/context or message ID, and the last committed task state. The API, SDK, action description in `cpractl`, dashboard actions and timeline retain these facts without agent text, artifacts, supplied links or credentials. Receipt state never changes an unknown action's hold or remote-fencing requirements.

The initial normal-startup test reproduced an integration defect: entity-bound jobs hid the A2A type, allowing the normal wrapper to call direct `Execute`, which correctly refused uncommitted execution. Dispatch now recognizes the private wrapped template and still requires its committed start and receipt writer. The repeated startup test exercises both API and manifest sources, each with intervention and notification roles. It verifies delivery, correlated history, resumed checks after restart, and no plaintext agent connection in the local Raft/history files.

Verification is retained in `bin/verification/a2a-configuration-2026-09-30/`. Earlier failed attempts remain alongside the passing runs. The full compiler/tag/race and rebuilt-dashboard results are recorded in that directory's final report when complete. This is local fixture evidence; escalation, the configured-provider evidence adapter, post-restart remote-task authority, independent review and the complete release remain unfinished.


## Frozen release assessment

The private release workflow now records the upstream SDK fixture build and its
interoperability tests as part of both compiler campaigns. It builds the fixture
with the separately pinned release compiler; the Go 1.25 application still uses
its own transport and does not import the upstream SDK. Original fixture sources,
verified module graph and checksums, vet output, executable and build information
are retained alongside the selected jobs package and original Go test events.

`scripts/release/a2a_evidence.py` consumes these records without running providers.
It requires 21 upstream cases across transport, intervention and notification,
plus 114 named lifecycle cases in each of the four application variants on both
compilers. The lifecycle requirements include configured startup, original
connection observation after restart, task receipt and unknown-outcome retention,
confirmed recovery-failure escalation and explicit process-kill boundaries.
Missing or skipped required cases remain incomplete; changed source, altered
executables, narrowed invocations and contradictory checksums are invalid.

The optional frozen provider build and retained reports must be supplied
together. Both designated-agent roles require independently observed effects
tied to their original committed actions. Protocol acceptance, a local fixture
or a sandbox cannot satisfy this requirement. The other provider roles remain
separate requirements. The report always leaves release approval and independent
review open. This addition implements evidence collection and inspection; it
does not establish a completed frozen campaign or actual configured-agent effects.
See [recording and reading the evidence](../validation.md#frozen-build-verification).
No endurance run is required or started.

## Confirmed recovery failure escalation

The local implementation adds an optional notification `trigger`:

| Value | Admission rule |
| --- | --- |
| Omitted or `state_change` | Existing health-transition notification behavior. |
| `recovery_failed` | Notify after a committed, confirmed failed intervention attempt. |
| `recovery_exhausted` | Notify only after the configured intervention attempt limit is reached by a confirmed failure. |

Recovery triggers require a red notification rule and configured recovery. API
`recovery.maxAttempts` and manifest `intervention.max_attempts` select the attempt
limit; omission preserves the one-attempt default. An unknown result, operator
review or late evidence cannot create an escalation. A notification failure
cannot recursively invoke recovery or another escalation.

Use `recovery_exhausted` when the agent should investigate after automated
recovery has finished its allowed attempts. For example, this API resource uses
an existing recipient whose endpoint is A2A; configure its credential references
separately before applying the monitor:

```yaml
apiVersion: cpra.io/v2
kind: Monitor
metadata:
  id: checkout
spec:
  enabled: false
  check:
    interval: 60s
    timeout: 5s
    driver:
      type: tcp
      config:
        host: checkout.internal
        port: 8080
  recovery:
    maxAttempts: 2
    driver:
      type: docker
      config:
        container: checkout
        timeout: 30s
  notifications:
    red:
      trigger: recovery_exhausted
      notifyType: a2a
      recipientRefs: [investigation-agent]
```

Acknowledgement does not suppress escalation. Dismissal, snooze and disablement
preserve their existing admission rules. A destination with a queued, started or
unknown notification keeps that original action; another failed attempt does not
replace its cause or bypass its hold. Notifications still honor cooldowns.

The committed notification and retained events pin the original intervention
identity, driver, attempt, attempt limit, failure time and selected trigger.
API/SDK `recoveryFailure`, CLI action descriptions and dashboard observations
expose that context. The A2A message carries the same bounded facts and the
explicit `failure` classification. Provider responses, targets and credentials
are excluded. The agent still receives notification authority only.

Docker's [restart contract](https://docs.docker.com/reference/api/engine/version/v1.52/#tag/Container/operation/ContainerRestart)
defines HTTP 404 as a missing container. That response becomes a confirmed
rejection in CPRa. HTTP 500, transport failure and lost replies remain unknown;
a restart can have partially affected a container before an error. This
classification is deliberately narrower than treating every HTTP error as a
confirmed failure.

Common snapshot format 33 stores the failure policy and context; format 34
combines it with optional worker state. Formats 31 and 32 remain readable under
their existing build boundaries. Frozen snapshots and retained history clone the
cause. The five new process-kill cases cover before the recovery result, queued
notifications, started delivery, accepted delivery and snapshot plus later log
replay. They execute storage transitions, not providers.

The storage-format checks now pass 137 default and 160 tagged tests on Go 1.27.1
and Go 1.25.0, with release-compiler race detection. The original format-expectation
failures and corrected reports are retained in
`bin/verification/a2a-escalation-2026-09-30/`. The normal-startup campaign covers
API and manifest admission, exhausted and earlier escalation, unknown Docker
outcomes, uncertain agent delivery and restart. All six scenarios pass on both
compilers with and without `externaljobs`; the release compiler uses race
detection. The earlier failing first-attempt case permitted another failed check
to replace the inspected notification. Its corrected fixture establishes explicit
ordering and asserts exactly one recovery attempt. The failed runs remain in the
[record](../review/local-evidence.md#record-6d71220e33fdd57019a7cf45). These are isolated HTTP fixtures using the
production jobs; actual Docker/agent effects, remote-task recovery authority and
independent review remain separate open gates.

## Configured-agent verification

The provider runner now uses real Raft intent/start/receipt/result commits for
A2A. Its separate verification store represents a synthetic incident and only
the selected designated operation; no health check or general scheduler runs.
Reusing a campaign directory fails before provider invocation, and unknown
outcomes remain in that directory after interruption. Provider configuration,
credentials and response content never enter the verification records.

The [configuration and observer contract](../provider-testing.md#a2a-candidate-evidence)
includes disabled examples for recovery and notification. Independent observers
must correlate the original run, monitor and action, and distinguish completed
recovery effects from receipt of investigation context. The report aggregator
also checks the retained evidence against the committed action identity and
successful disposition. A local fixture pass does not establish a deployed
agent's authority or actual recovery effects.

The [verification record](../review/local-evidence.md#record-ede657ded5f96f511b411110)
contains 20 passing Go tests for default/tagged builds on Go 1.27.1 with race
detection and Go 1.25.0. Seven provider scenarios and one real process-kill
scenario run in each combination; a separate example check preserves the normal
monitor schema and disabled defaults. The 48 Python tests include independent
receipt matching, stale/wrong identities, repeated effects, malformed records
and report/action mismatch. A CGO-free full-driver executable passes both roles
through the actual CLI, Python observer and report aggregator with independently
counted local requests. Reusing its state fails before another request. All
of these provider effects are controlled fixtures. Native platforms, configured
agent effects, remote-task reconciliation and independent review remain open.

## Reading retained tasks after restart

Both managed and file-only runtimes commit the encrypted original agent connection
with the action's started marker, before sending its message. The frozen payload
contains only the endpoint, bearer token, tenant, request timeout and explicit
HTTP allowance. Its authenticated binding includes the store identity, original
monitor incarnation, configuration revision and action identity. Credentials,
message content and executable objects are excluded from public action/history
responses. Startup authenticates retained connections before opening admission;
a wrong key, damaged envelope or unsupported payload stops startup explicitly.

The observer reads an existing task after its local invocation has stopped.
It uses the retained connection even when the monitor is disabled, reconfigured
or removed. A one-time manifest adoption preserves the connection's original
binding. It never substitutes newly configured credentials or follows an
agent-supplied URL. Revoked credentials or a missing task leave the action held;
they are not proof that the original external operation did not happen.

Each pass examines at most 100 indexed actions and performs at most four serial
GET requests. The process uses a five-second ticker and a five-second limit on
each observation, additionally bounded by the original request timeout. Its
cursor advances after failed reads. Shutdown cancels and joins the observer
before closing encryption or storage. These bounds are implementation limits,
not a guaranteed fleet-wide observation interval.

A changed task state is committed against the exact action observation read
before network I/O. Concurrent operator reviews or another observation cause a
conflict; a stale process cannot use the new process's observation authority.
An identical receipt retry is idempotent. An unchanged read creates no history
entry or Raft update. Provider errors, redirects, mismatched task identities and
cancellation preserve the existing action and do not mark otherwise usable
storage unhealthy. Stored-payload authentication failure remains a storage
integrity error.

Task observations never send or cancel work, replace a terminal receipt, clear
an operator review, or automatically resolve an unknown action. Terminal remote
evidence only permits a later explicit conditional review. An accepted
notification records delivery of investigation context; it does not become a
recovery operation whose eventual task result changes monitor health. A lost
initial response without a retained task ID cannot be recovered by sending the
message again.

Common storage format 35 retains encrypted connections and the separate
observation command. Combining those records with optional worker state uses
format 36; ordinary builds reject that combined format. Snapshots freeze the
connection and receipt before later updates, and replay performs no agent I/O.

Verification records are in
[`a2a-observation-2026-09-30`](../review/local-evidence.md#record-b8aad943f74b66719eebe4d1).
They retain failed fixture runs as well as their corrections. The campaigns
exercise real Raft process termination, snapshot/log replay, original-target
reads through the production HTTP path, reviewed actions, bounded polling and
shutdown. Local HTTP endpoints supply controlled effects; this is not evidence
from an operator's deployed agent. Exact compiler, build tag, source hashes and
results belong to each recorded run.

File-only Raft operation requires an explicitly initialized wrapping backend under
`storage.encryption` when its manifest selects A2A. The manifest preflight rejects
missing encryption before opening state or constructing jobs. Retained connections
are authenticated before controller admission even if all A2A monitors have been
removed from the current manifest. Missing, wrong or unavailable keys stop startup;
there is no fallback to plaintext, a new target or memory storage. Explicit memory
mode uses an ephemeral key when no source is configured and provides no recovery
across process restarts.

`storage.encryption` accepts the same local, Transit and KMS source descriptors as
`management.encryption`. Configure exactly one location. Managed operation also
accepts the storage-level setting, and key sources must remain outside the resolved
data/backup directory. Previous wrapping keys must remain configured while retained
connections use them. Selecting a new active wrapping key does not re-encrypt older
records. See [storage encryption configuration](../management-startup.md#storage-encryption-for-file-only-monitoring).

The normal file loader selects the actual immutable job connection. It is sealed
before the committed start; the start still checks the original configuration
revision and current controls. Removed or changed file configuration cannot redirect
a retained observation. Both the v1 state endpoint and v2 action views omit the
internal connection envelope and its wrapping-key identity.

The file-only campaign is recorded in
[`a2a-file-2026-09-30`](../review/local-evidence.md#record-c4b745a68bce630710f75645).
It exercises real application processes and forced termination, both job roles,
unchanged/changed/removed manifests, snapshot recovery, wrapping-key rotation,
missing and wrong keys, and read-only response/log/state-file confidentiality.
The initial fixture filename correction remains in its failed-run record. Final
compiler/tag results and source identities belong to that campaign's report.
Actual configured-agent effects, full candidate qualification and independent
review remain open.
