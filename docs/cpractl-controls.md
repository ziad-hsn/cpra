# Incident attention and monitor controls with cpractl

These commands use the same management HTTPS connection and named identities
as [resource CRUD](cpractl-management.md). They act on one explicit target and
never fetch a newer version, retry a mutation, or select a fleet implicitly.
The server supplies the actor from the authenticated identity. There is no
client `--actor` option.

| Intent | Command | Version from your observation |
| --- | --- | --- |
| Show someone is investigating | `acknowledge incident/ID` | Incident `revision` with `--revision` |
| Suppress that incident's notifications | `dismiss incident/ID` | Incident `revision` with `--revision` |
| Resume future notifications for the same incident | `reopen incident/ID` | Incident `revision` with `--revision` |
| Pause checks, notifications and new recovery temporarily | `snooze monitor/ID` | Monitor `status.controlRevision` with `--control-revision` |
| End that pause early | `unsnooze monitor/ID` | Monitor `status.controlRevision` with `--control-revision` |
| Disable indefinitely or enable again | `disable monitor/ID`, `enable monitor/ID` | Monitor `metadata.resourceVersion` with `--resource-version` |

An incident revision, control revision, configuration resource version, and
execution revision are different identities. Supply the exact field identified
above without HTTP quotes. An empty control revision means the matching
controller configuration has not been observed yet; inspect it again rather
than substituting another version. A version conflict requires another review
of the target and your intended change.

## Read incident identity and attention

Use v2 incident observations to find the exact incident and its current revision:

```bash
cpractl get incident-records --monitor-id service-api -o json
cpractl get incident/INCIDENT_ID -o json
cpractl describe incident/INCIDENT_ID
```

`describe` includes the authenticated person who acknowledged the incident,
acknowledgment time, dismissal state, open/closed state and revision. This list
contains each monitor's latest retained incident; earlier events belong in
retained history. Pages default to 100 and are limited to 500. Continue with
`--cursor`, retaining the original `--limit` and `--monitor-id`. An expired
cursor requires a fresh list. `get incidents` reads the same incident records.

## Acknowledge, dismiss and reopen

Acknowledgment records the person who is investigating. It does not pause
checks, notifications or recovery. An optional UTF-8 note comes from a file or
stdin:

```bash
cpractl acknowledge incident/INCIDENT_ID --revision INCIDENT_REVISION \
  --note-file investigation.txt -o json
```

Dismissal requires a reason and suppresses notifications for this exact incident.
Checks and recovery continue. It cancels unsent notifications and preserves
started or unknown external outcomes. Supply a reason file:

```bash
cpractl dismiss incident/INCIDENT_ID --revision INCIDENT_REVISION \
  --reason-file known-issue.txt -o json
```

To remove dismissal, first observe the resulting new incident revision and
reopen the same still-active incident:

```bash
cpractl reopen incident/INCIDENT_ID --revision NEW_INCIDENT_REVISION -o json
```

Reopening permits future eligible notifications. It does not replay cancelled,
completed, started or unknown deliveries, and cannot reopen a closed or replaced
incident. Each successful attention update produces another revision; do not
reuse the version from before a previous successful edit.

## Snooze and unsnooze

Read the monitor and use `status.controlRevision`:

```bash
cpractl get monitor/service-api -o json
cpractl snooze monitor/service-api --control-revision CONTROL_REVISION \
  --for 30m --reason-file maintenance.txt -o json
```

`--for` and `--duration` are aliases; specify one. Durations use Go syntax such
as `30m`, `2h`, or `1h30m`. The period must be positive and at most `720h`
(30 days). Snooze pauses new checks, notifications and recovery until its
durable expiry. Work already started can remain in progress or become unknown;
snooze does not establish external cancellation.

Observe the new control revision before ending the snooze early:

```bash
cpractl unsnooze monitor/service-api --control-revision NEW_CONTROL_REVISION -o json
```

Neither unsnooze nor expiry enables a disabled monitor or bypasses maintenance.

## Disable and enable

These commands conditionally patch only `spec.enabled`, using the exact
configuration `metadata.resourceVersion` you reviewed:

```bash
cpractl disable monitor/service-api --resource-version CONFIGURATION_VERSION -o json
cpractl get monitor/service-api -o json
cpractl enable monitor/service-api --resource-version NEW_CONFIGURATION_VERSION -o json
```

Disable retains monitor state and event history while stopping new checks,
notifications and recovery. Enable continues to respect snooze and maintenance.
The current configuration patch receipt records the authenticated actor; it
does not support an additional reason field for enable/disable.

`cpractl get slo` reports `PAUSED` and `PAUSE MONITOR-SECONDS` separately from
samples and missed checks. Overlapping disable and snooze count once. Two monitors
paused for one second contribute two monitor-seconds. These owner-observed intervals
add no successful checks and do not erase obligations or misses from before a pause.
Restart retains measured exposure and reports the unobserved gap; older servers
without these fields display `unavailable`, distinct from measured zero.

## Audit text, receipts and uncertainty

Notes and reasons are retained operator comments, visible through audit history;
do not include credentials. `--note-file -` and `--reason-file -` read stdin.
Comments are UTF-8 and at most 4,096 bytes, with no NUL or carriage-return
characters. Save text files with LF line endings. A required reason cannot be
empty or whitespace. Comments are not accepted as command arguments.

JSON/YAML stdout remains a standalone response. Mutation receipt notices are
written to stderr. A receipt establishes committed intent; inspect its operation
to distinguish that from application by the owner loop:

```bash
cpractl get operation RETURNED_OPERATION_ID -o json
```

A lost response produces an unconfirmed-outcome error and no automatic retry.
Keep any returned operation identity and inspect that receipt and the original
target before deciding whether another request is necessary. Reusing the old
version cannot silently overwrite a newer control, but it is not a substitute
for checking an uncertain outcome.

Selectors, `--all`, and multiple control targets are rejected until the separate
bounded collection-control workflow is implemented. No check-now command is
provided. [Guarded recovery and action review](cpractl-actions.md) are separate
commands with their own observed revisions and audited reasons.
