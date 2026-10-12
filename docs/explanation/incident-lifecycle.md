---
title: Incident lifecycle
description: Understand failure thresholds, recovery admission, verification, maintenance and notification delivery in CPRa.
cpra_scope: development
---

See [versions and availability](../versions.md) for implementation and
qualification boundaries.


# Incident lifecycle

CPRa schedules checks, classifies their results, optionally admits a recovery action, and verifies subsequent health. Notifications report configured transitions.

## From an unknown monitor to an incident

A newly loaded monitor has no observed result yet. Successful checks establish health. Consecutive unsuccessful checks accumulate toward its unhealthy threshold. On the first failure, a configured yellow notification can be admitted. At the unhealthy threshold, an eligible recovery is attempted before opening a red incident. Without an available recovery, on a rejected recovery, or after a failed verification check, the incident opens and its configured red notification is admitted. A successful recovery emits configured cyan and starts healthy-check verification; verified recovery emits configured green. Notification rules and maintenance still govern actual delivery.

| Displayed state | How to interpret it |
| --- | --- |
| `unknown` | No completed observation is available yet. |
| `up` | The monitor's current health state is healthy. |
| `degraded` | A warning is active, such as a TLS certificate approaching expiry. |
| `down` / `incident` | Failure or an incident is represented by the current snapshot. |
| `verifying` | Healthy observations are being used to verify recovery. |
| `disabled` | The monitor is disabled in configuration. |

Read the state together with the most recent check and pending notification. An empty incident list alone does not establish that every monitor is healthy.

## Recovery admission and verification

Eligible recovery is subject to the configured per-incident attempt limit,
which defaults to one. Additional failures do not continuously replay the same
action. Completion means **health verification pending**; configured successful
checks must confirm health before restored-health wording is emitted.

A lost response, interrupted start or deadline can leave the external outcome
unknown. That hold survives restart. Check observations resume, while the
interrupted action is not automatically repeated. Inspect provider evidence and
use the audited review procedure. This does not provide provider-independent
exactly-once execution.

## Warnings and maintenance

TLS `warn_days` raises a yellow warning and degraded state without starting recovery. `critical_days` fails the check and follows the normal failure policy.

Maintenance keeps checks running while suppressing new notifications and
recovery admissions. Covered queued notifications are canceled, including work
that waited through the entire window. Started, completed and unknown actions
retain their evidence. Ending maintenance does not release accumulated alerts.

## Delivery and cooldown

Red, yellow, green, cyan and gray rules are configuration keys. Endpoint outcomes
are tracked independently with stable logical delivery identities. Confirmed
delivery to one destination is not repeated because another was unfinished.

Only proven retryable provider rejection can authorize another HTTP attempt,
up to three within the original deadline. Generic server errors remain unknown.
Ordinary HTTP notifications use a 45-second overall budget; documented delays
do not extend it. Provider acceptance does not establish observed delivery.
Cooldown suppresses repeated notifications within one episode. A new outage or
distinct severity transition receives its first eligible notification promptly.

## Restart and ownership

Raft restores committed incidents, cooldowns, verification, attempt limits,
controls and delivery outcomes. Replay performs no provider operations. Each
data directory has one exclusively locked owner. Separate instances do not
coordinate a shared monitor fleet; single-node Raft is persistence, not failover.
Memory mode is explicit and disposable. Back up the complete stopped directory,
including retained history and generation metadata.
