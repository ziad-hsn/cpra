# Monitor and incident controls

The management API separates saved monitor configuration, current incident
attention, and temporary monitor control state. Each has its own version.
Management authentication, HTTPS/origin rules, and shutdown admission limits
apply to every write. A notification Recipient is not a management identity.

| Intent | Operation | Required version | Behavior |
| --- | --- | --- | --- |
| Record who is investigating | `POST /api/v2/incidents/{id}/acknowledge` | Incident `revision` | Records the authenticated principal, time, and optional note; checks and notifications continue. |
| Stop notifications for one incident | `POST /api/v2/incidents/{id}/dismiss` | Incident `revision` | Requires a reason; cancels unsent notifications for this exact incident. Checks and recovery continue. |
| Resume notifications for that incident | `POST /api/v2/incidents/{id}/reopen` | Incident `revision` | Requires the same still-active incident; cancelled or completed deliveries are not replayed. |
| Pause a monitor temporarily | `POST /api/v2/monitors/{id}/snooze` | `status.controlRevision` | Requires a reason and positive Go duration, at most `720h` (30 days). Pauses checks, notifications, and new recovery. |
| End that temporary pause | `POST /api/v2/monitors/{id}/unsnooze` | `status.controlRevision` | Ends snooze without enabling a disabled monitor or bypassing maintenance. |
| Disable or enable a monitor | `PATCH /api/v2/monitors/{id}` | `metadata.resourceVersion` | Conditionally writes `spec.enabled`; state and event history remain retained. |

Control POST requests use a single strong `If-Match` header, such as
`If-Match: "opaque-control-revision"`. The JSON `revision` is the same opaque
value **without** its HTTP quotes. For example:

```json
{
  "revision": "opaque-control-revision",
  "duration": "30m",
  "reason": "Scheduled database maintenance"
}
```

The Go SDK accepts opaque revisions and quotes the HTTP header itself:

```go
monitor, err := client.Monitors.Get(ctx, "checkout")
if err != nil {
    return err
}
result, err := client.Monitors.Snooze(ctx, "checkout", api.ControlRequest{
    Revision: monitor.Data.Status.ControlRevision,
    Duration: "30m",
    Reason:   "Scheduled database maintenance",
})
if err != nil {
    // Do not automatically retry an uncertain mutation. Preserve any returned
    // operation identity and inspect its receipt and the current monitor.
    return err
}
operationID := result.Data.ID
_ = operationID // Inspect client.Operations.Get(ctx, operationID).
```

An empty control revision means the matching controller configuration has not
been observed yet. Reload the monitor rather than substituting its resource
version or execution fingerprint. A stale version or recreated monitor produces
a precondition failure. The server never silently adopts a newer revision.

The server derives the actor from the authenticated principal. Clients cannot
submit an actor field. Reasons and notes are at most 4,096 UTF-8 bytes; these are
retained operator comments, so do not include credentials. The current enable
and disable patch contract records the principal in its operation receipt; it
does not provide a separate reason field.

## Admission and application

Snooze and unsnooze return an Operation. Incident attention returns the updated
Incident and supplies its receipt identity in `X-Operation-ID`. Both expose
`X-CPRa-Admission: committed` and the committed index. These establish that the
request was saved. They do not claim that the owner loop has applied the new
projection. Query `GET /api/v2/operations/{id}` to observe application; a newer
control can supersede an older receipt.

An uncertain HTTP result does not roll back a committed command. Retain the
original operation identity when it is available. Do not infer failure from a
lost response or create a new request automatically. During shutdown, new
mutations are unavailable while current reads and receipt diagnostics remain
available.

## Current incident navigation

`GET /api/v2/incidents/{id}` returns the active or latest closed incident retained
for its monitor. `GET /api/v2/incidents` returns a bounded page (100 default, 500
maximum). Use `monitorID` for an indexed lookup of one monitor's latest incident.
Earlier incidents remain in retained event history; this list is not an
unlimited incident archive. An incident's `state` is `open` or `closed`.

`GET /api/v2/history?monitorID=checkout` supplies that event timeline, including
the original event, incident, and action IDs and the recorded actor, reason, and
note. It returns 100 events by default and accepts at most 500 per page. Use the
SDK's `client.History(ctx, cpra.ListOptions{MonitorID: "checkout"})` method and
continue with the returned cursor and the same monitor ID and page limit. No
provider configuration, credential value, or internal operation record is
included. The v1 history response is preserved.

History cursors retain the original committed upper bound. Events committed
later belong to a fresh query. A retention pass invalidates the old view with
HTTP 410 instead of silently changing a retried page. History shares the cursor
lifetime and principal quotas below. Memory mode may return an empty page with
a continuation when its bounded scan crosses other monitors' events; continue
until the cursor is empty. Responses are also capped at 8 MiB, so a page with
long notes can contain fewer than the requested number of events.

Cursors freeze the compact incident index and bind it to the authenticated
principal, policy generation, page limit, and monitor filter. Reusing a cursor
does not advance shared state; it returns the same page. Cursor lifetime is five
minutes, with shared limits of 64 retained catalog, incident, action, and history views globally and
16 per principal. Expired cursors require a fresh list. Label-selector filtering
is currently unavailable and returns an explicit error rather than ignoring the
filter.

Monitor pages freeze desired configuration while their health is a current
observation. A page for an old configuration or incarnation does not receive
control versions from its replacement. An unavailable check latency remains
explicitly unavailable. `observedGeneration` is populated only after the owner
has installed the matching jobs, ECS components, and schedules. A committed
configure alone cannot set it. This marker is process-local: a restart must
install the projection again, and a changed monitor incarnation, configuration,
or referenced dependency makes the old marker unavailable. Unrelated catalog
edits preserve a verified marker after bounded dependency revalidation. The
operation receipt remains the progress record for each specific mutation.

## Guarded recovery and action review

`POST /api/v2/monitors/{id}/recover` uses the canonical operation permission
`RecoverMonitor`. It requires the exact current monitor `controlRevision` in both
the strong `If-Match` header and request body, plus a nonempty reason. The server
checks the installed configuration and its complete reference closure, current
unhealthy observation, enabled/snooze/maintenance state, configured recovery,
active or held interventions, verification, attempt budget, cooldown, and the
operator request rate policy. A changed target or credential must be installed by
the owner before manual admission is possible.

A successful response contains a durably committed operation receipt with
`applied: 0`. It is an admitted recovery intent; the controller later reports
whether it installed that intent. It is not evidence that the provider accepted
an action or that the target recovered. Read the original operation and action
instead of issuing a fresh request after a lost response. No check-now route is
provided.

`GET /api/v2/actions` supports an exact `monitorID`, cursor, and page limit. It uses
an ordered action index, including a separate per-monitor index, and frozen
principal-bound views. `GET /api/v2/actions/{id}` returns `reviewRevision` as its
strong ETag. Provider `state` and `outcome` remain recorded facts. `held` reports
whether an unknown outcome still blocks safe progress. Creation time is omitted
when no actual creation timestamp was retained; a scheduling bound is never
reported as creation time. Optional `lateEvidence` and `conflictingEvidence`
contain only the recorded outcome, execution interval, and receipt time. They
remain distinct from both the original unknown state and the operator assertion.
`updatedAt` includes later evidence and executor-completion records and is omitted
when no actual change timestamp is available.

`POST /api/v2/actions/{id}/review` requires the exact fetched `reviewRevision`, a
reason, and one of `accepted`, `rejected`, or `inconclusive`. An optional note and
up to eight opaque evidence references are audit information, never instructions
to fetch a URL. Each reference is at most 2,048 UTF-8 bytes, contains no control
characters, and is unique. Reason and note are each at most 4,096 UTF-8 bytes.

Only an unknown action may be reviewed. A conclusive assertion requires the
original local executor to have returned and committed its completion marker, or
to belong to an earlier process session fenced by exclusive storage ownership.
Cancellation and a timeout alone are not proof that code stopped. An
unclassified/external executor cannot have its hold cleared by this endpoint.
The review never changes an unknown provider outcome to a successful one and
never replays the action. Contradictory retained provider evidence prevents a
conclusive assertion. Inconclusive review retains the hold.

The review response includes an operation handle for its controller projection.
`reviewRevision` is the version of the complete action observation used for the
next conditional review; `review.revision` identifies the saved assertion and its
operation receipt. `receiptID` retains the original manual-recovery operation
identity when one exists. These identifiers are not interchangeable.
Its separate audit object records the authenticated principal, assertion,
reason, note, and evidence references. Timeline events preserve the original
monitor, incident, action, execution/control revisions and safe delivery fields.
Neither read endpoint returns provider configuration, credentials, executor
session identifiers, or executable payloads. Retained history reports unavailable
when a required segment is unreadable or the store has closed.
