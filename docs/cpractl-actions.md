# Guarded recovery and action review with cpractl

These commands use the public Go SDK and authenticated v2 API. Read the
[connection setup](cpractl-management.md) first: authenticated HTTPS is the
default, and the CLI uses the named principal granted by the server. Readers can
inspect actions; recovery and review need their respective operation permissions.
Notification contacts do not grant management access.

## Request a recovery

Read the monitor configuration and current observation, then supply exactly its
`status.controlRevision`. This is distinct from `metadata.resourceVersion` used
for configuration edits. The CLI never fetches a replacement version for you.

```bash
cpractl get monitor/service-api -o json
cpractl recover monitor/service-api \
  --control-revision OBSERVED_CONTROL_REVISION \
  --reason-file recovery-reason.txt -o json
```

Write the operator reason into the named UTF-8 file, or use `--reason-file -` to
read stdin. Audit text is not accepted inline in command arguments. A reason is
required and retained in history; keep credentials out of it.

The server checks the installed target and credentials, unhealthy observation,
enabled/snooze/maintenance state, configured recovery, attempt budget, cooldown,
verification, existing interventions and operator rate policy. A request cannot
bypass an unresolved action hold or dispatch against an unapplied configuration.

The returned operation is an admitted intent. An initial `applied: 0` is neither
proof that the controller installed it nor evidence of provider success:

```bash
cpractl get operation RETURNED_OPERATION_ID -o json
cpractl get actions --monitor-id service-api --limit 100 -o json
```

The CLI makes one conditional mutation and never retries it automatically. After
a conflict, review a fresh observation before deciding what to do. After a lost
reply, inspect any returned operation identity and the original target/actions;
do not issue another recovery merely because the first reply was lost.

## Inspect an action before reviewing it

```bash
cpractl get actions --monitor-id service-api --limit 100 -o json
cpractl get actions --monitor-id service-api --limit 100 \
  --cursor RETURNED_CURSOR -o json
cpractl describe action/ACTION_ID
cpractl get action/ACTION_ID -o json
```

Each list fetches one page: 100 items by default and at most 500. Retain the
original monitor selection and limit when continuing a cursor. The CLI never
loads the whole fleet or follows evidence links. A missing creation time is
reported as unavailable.

Read the monitor's retained audit events, including authenticated actors, notes
and evidence references, through the v2 history view:

```bash
cpractl get events --monitor-id service-api --limit 100 -o json
cpractl get events --monitor-id service-api --limit 100 \
  --cursor RETURNED_CURSOR -o json
```

The monitor ID is required. Each request returns one frozen page, with no
implicit timeline collection or action-ID filtering. An expired cursor requires
a fresh list. `get history service-api` and `get events --monitor-id service-api`
read the same event contract.

`state` and `outcome` are provider observations. `held` indicates whether an
unknown outcome still blocks safe progress. `executorFenced` indicates whether
the original executor is known to have returned or belongs to an earlier process
session excluded by storage ownership. The action's `reviewRevision` identifies
the exact observation to review; it is separate from `executionRevision`.

## Record an operator assertion

```bash
cpractl review action/ACTION_ID \
  --review-revision OBSERVED_REVIEW_REVISION \
  --resolution inconclusive \
  --reason-file review-reason.txt -o json
```

Choose the assertion that your independent evidence supports:

| Resolution | Meaning |
| --- | --- |
| `accepted` | The operator has evidence supporting acceptance of the original action. A conclusive review can release its hold only when the server's executor and evidence gates pass. |
| `rejected` | The operator has evidence supporting rejection of the original action, subject to the same server gates. |
| `inconclusive` | The outcome still cannot be established; the hold remains. |

The response keeps the original provider facts, including `state: unknown` and
its unknown outcome. The separate `review` records the authenticated actor,
assertion, reason and time. Review never replays an action and cannot turn an
unknown provider observation into a known success. Conflicting retained evidence
prevents a conclusive assertion. A timeout or cancellation alone does not prove
that the original code stopped.

Optional audit information can accompany a review:

```bash
cpractl review action/ACTION_ID \
  --review-revision OBSERVED_REVIEW_REVISION \
  --resolution accepted --reason-file review-reason.txt \
  --note-file review-note.txt --evidence-file evidence.json -o json
```

`evidence.json` contains opaque references, for example:

```json
["ticket:incident-123", "provider-receipt:456"]
```

There may be at most eight unique references, each a nonempty UTF-8 string of at
most 2,048 bytes without control characters. They are audit identifiers, not
URLs for CPRa to retrieve. The complete JSON file is capped at 128 KiB. Reason
and note files are each capped at 4,096 UTF-8 bytes, without NUL or carriage
returns. Only one of the audit inputs may consume stdin. These fields appear in
the authorized audit response, so do not put secrets in them.

The receipt goes to stderr while JSON or YAML stdout remains a standalone API
response. Read the operation to distinguish durable admission from controller
application. Single explicit targets are supported; selectors, `--all` and
multiple targets are rejected. No check-now operation is provided.
