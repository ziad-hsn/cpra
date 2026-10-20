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

A newly loaded monitor has no observed result yet. Successful checks establish
health. Consecutive failures accumulate toward the unhealthy threshold. The first
failure can admit configured yellow; reaching the threshold can admit an eligible
intervention. Without available recovery, after rejected recovery, or after a
failed verification check, the incident opens and configured red can be admitted.
A successful intervention can admit cyan, meaning **health verification pending**.

The first failed observation and the unhealthy-threshold time are separate
timestamps. An audited manual recovery can escalate an outage before that
threshold; the interface does not invent a threshold time for it. Older stored
episodes without a recorded threshold time show it as unavailable.

Normal green requires both the healthy-check threshold and an actual yellow,
red or cyan notification start in that episode. An unsent, canceled intent does
not qualify. Healthy startup alone does not send green. Ignore recovery has the
explicit exception described below. Notification configuration and suppression
controls still determine eligibility; admission does not prove delivery.

A missing, unreadable or invalid configured CA file is a local configuration
problem. CPRa retains the last target observation, suppresses configuration-induced
recovery and retries preparation at the normal check cadence. Static validation
checks the settings without reading CA files or contacting the target. Relative
CA paths are relative to the process working directory; use absolute paths in
service configurations. `ca_file` and skip-verification cannot be combined.

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
interrupted action is not automatically repeated. An unknown intervention blocks
conflicting interventions, but does not block healthy streaks, incident closure,
later outage detection or eligible notifications. A successful health observation
does not establish that the earlier intervention succeeded. Inspect provider evidence and
use the audited review procedure. This does not provide provider-independent
exactly-once execution.

## Warnings and maintenance

TLS `warn_days` raises a yellow warning and degraded state without starting recovery. `critical_days` fails the check and follows the normal failure policy.

Maintenance keeps checks running while suppressing ordinary notifications and
recovery admissions. Covered queued notifications are canceled, including work
that waited through the entire window. Started, completed and unknown actions
retain their evidence. Ending maintenance does not release accumulated alerts.
An outage that continues after suppression is re-evaluated using fresh checks.
An outage first established inside maintenance must reach the configured failure
threshold outside maintenance before its eligible alert or automatic Ignore count
is admitted. An outage that already recovered has no stale alert to replay.
Configured gray notices for actual enable/disable transitions remain eligible
during maintenance and Ignore. They describe the control change, never recovery.

## Ignore and repeated outages

Ignore retains checks, actual health, incidents and history while suppressing
ordinary notifications and new interventions. It can be entered explicitly with
an audited [Ignore control](../cpractl-controls.md#ignore-and-unignore), or by the
monitor's optional `auto_ignore` count/window policy. Automatic Ignore counts
distinct outages that reach the unhealthy threshold, not individual failed
checks. Ordinary recoveries before the limit is reached do not reset that rolling
count. The triggering outage enters Ignore before its intervention or ordinary
notifications can be admitted.
Outages wholly inside maintenance do not count. A continuing outage counts once
after its threshold is confirmed outside maintenance.

Ignore cancels unsent interventions and ordinary notifications. Started and
unknown work retain their original evidence. Configuration edits do not clear an
active Ignore. Explicit Unignore permits future eligible handling without
releasing suppressed work.
When Ignore is applied while healthy, successful checks alone do not clear it or
send green. A later qualifying outage must recover, or an operator must Unignore.

Verified recovery clears Ignore and its counter and can admit one configured
green notification, even though the ignored outage had no earlier alert start.
Other controls still apply: disabled/snoozed checks cannot establish fresh
recovery, and maintenance can suppress green. Clearing Ignore does not wait for
green delivery to succeed. A failed or uncertain green cannot keep the monitor
ignored or cause that delivery to replay automatically.

## Delivery and cooldown

Red, yellow, green, cyan, blue and gray rules are configuration keys. Endpoint outcomes
are tracked independently with stable logical delivery identities. Confirmed
delivery to one destination is not repeated because another was unfinished.
An unknown delivery holds only its own identity, incarnation, episode and
destination. It does not suppress a later incident's notification or an unrelated
destination.

Only proven transient rejection or a proven pre-send failure can authorize another HTTP attempt,
up to three within the original deadline. Generic server errors remain unknown.
Ordinary HTTP notifications use a 45-second overall budget; documented delays
do not extend it. Provider acceptance does not establish observed delivery.
Cooldown suppresses repeated notifications within one episode. A new outage or
distinct severity transition receives its first eligible notification promptly.

## Blue: recovery outcome unknown

Configure blue destinations explicitly to receive **Recovery outcome unknown —
operator review required**. Blue never inherits red destinations. Missing blue
configuration is valid, but validation and operator views warn that an uncertain
recovery has no external review destination. Ordinary suppression applies to
blue; only gray has the enable/disable exception during maintenance and Ignore.

Each blocking recovery action, outage episode and destination has its own blue
correlation. After verified health recovery, its closure says **Health recovered;
earlier recovery outcome remains unknown**. The action stays unknown and held.
A later qualifying outage can create a new blue condition if the action still
blocks recovery. A conclusive audited review can also close the review-needed
condition, describing the actual health rather than implying recovery.

Provider resolution is a separate delivery. A local condition closing does not
prove the provider resolved its alert. Suppressed, failed and unknown closing
deliveries remain visible. PagerDuty resolution uses the original routing and
deduplication identity; Opsgenie acceptance is asynchronous. Email, chat, log and
webhook destinations receive an explicit closure/update message.

## Restart and ownership

Raft restores committed incidents, cooldowns, verification, attempt limits,
controls and delivery outcomes. Replay performs no provider operations. Each
restored queued intervention requires a fresh check before dispatch. A healthy
target cancels it; a failed check still must satisfy the original identity,
policy and deadline. Removing and later re-adding a file monitor creates a new
incarnation. Its old actions and history remain attached to the original target.
Each data directory has one exclusively locked owner. Separate instances do not
coordinate a shared monitor fleet; single-node Raft is persistence, not failover.
Memory mode is explicit and disposable. Back up the complete stopped directory,
including retained history and generation metadata.
