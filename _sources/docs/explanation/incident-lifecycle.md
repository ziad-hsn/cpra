---
title: "Incident lifecycle"
description: "Understand failure thresholds, recovery admission, verification, maintenance and notification delivery in CPRa."
---

# Incident lifecycle

CPRa schedules checks, classifies their results, optionally admits a recovery action, and verifies subsequent health. Notifications report configured transitions.

## From an unknown monitor to an incident

A newly loaded monitor has no observed result yet. Successful checks establish health. Consecutive unsuccessful checks accumulate toward its unhealthy threshold. When the threshold is reached, the configured incident and recovery behavior applies.

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

Red, yellow, green, cyan, and gray notification rules are configuration keys. Configure only the transitions and transports you intend to use. A group counts as delivered when at least one destination accepts it.

Retryable HTTP rejections can receive up to three delivery attempts. Transport acceptance does not confirm that a human read the alert. The default alert cooldown is five minutes; confirmed green recovery notifications can bypass it.

## Restart and ownership

Incident, cooldown, verification and per-endpoint delivery state are committed in local Raft storage by default. Restart rebuilds runtime jobs from current configuration and resumes safe queued work. Independent instances do not share monitor ownership or coordinate actions; use one owner for a monitor configuration.
