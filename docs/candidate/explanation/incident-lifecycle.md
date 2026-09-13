---
title: Candidate · Incident lifecycle
description: Candidate · Understand failure thresholds, recovery admission, verification, maintenance and notification delivery in CPRa.
cpra_scope: release_candidate
---

> **Release candidate:** this page describes [`410fbfb`](https://github.com/ziad-hsn/cpra/commit/410fbfb0092d01277b3884cd04151c27443a4226), which is separate from `main`. See [version and availability](../../versions.md).


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

## Recovery is admitted once per incident

When configured and eligible, one recovery operation is admitted. More failed checks during that incident do not continuously replay it. A successful action response is followed by the configured number of consecutive healthy checks before recovery is confirmed.

A lost response or deadline expiry can leave the action's external outcome unknown. Restart holds interrupted started actions as unknown; inspect the provider before deciding how to resolve them. This conservative policy does not establish provider-independent exactly-once execution.

## Warnings and maintenance

TLS `warn_days` raises a yellow warning and degraded state without starting recovery. `critical_days` fails the check and follows the normal failure policy.

Maintenance windows keep checks running while suppressing notifications and new recovery admissions. They do not roll back actions already running at the target.

## Delivery and cooldown

Red, yellow, green, cyan, and gray notification rules are configuration keys. Configure only the transitions and transports you intend to use. Group members run as separate committed endpoint actions. Inspect each endpoint outcome; a success neither repeats a completed endpoint nor completes an unfinished one.

Retryable HTTP rejections can receive up to three delivery attempts. Transport acceptance does not confirm that a human read the alert. The default alert cooldown is five minutes; confirmed green recovery notifications can bypass it.

## Restart and ownership

Incident, cooldown, verification and per-endpoint delivery state are committed in local Raft storage by default. Restart rebuilds runtime jobs from current configuration and resumes safe queued work. Independent instances do not share monitor ownership or coordinate actions; use one owner for a monitor configuration.
