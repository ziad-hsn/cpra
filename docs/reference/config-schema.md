---
title: Monitor configuration
description: Configure CPRa YAML manifests, check thresholds, alert groups, maintenance windows and recovery targets.
cpra_scope: development
---

See [versions and availability](../versions.md) for implementation and
qualification boundaries.


# Monitor configuration

Pass a YAML or JSON monitor manifest to `cpra -yaml PATH`. `-config` is an alias
for the same manifest. `-runtime-config` selects separate server/storage settings.
Management imports its configured bootstrap manifest once; later edits use
explicit [apply operations](../cpractl-management.md). Source files do not reload
automatically. File-only operation reconciles its manifest at startup.

## A complete minimal manifest

~~~yaml
monitors:
  - id: example-service
    name: example-service
    enabled: true
    pulse_check:
      type: http
      interval: 30s
      timeout: 5s
      unhealthy_threshold: 3
      healthy_threshold: 2
      config:
        url: http://127.0.0.1:8080/health
    codes:
      red:
        dispatch: true
        notify: log
        config: {file: alerts.jsonl}
      green:
        dispatch: true
        notify: log
        config: {file: alerts.jsonl}
~~~

## Monitor and check fields

| Field | Meaning |
| --- | --- |
| `id` | Optional stable monitor identity. Keep it across renames; otherwise identity derives deterministically from `name`. Duplicate effective IDs are rejected. |
| `name` | Required monitor name; use a descriptive name you can search for. |
| `enabled` | Whether the monitor runs; defaults to true when omitted. |
| `tags` | Optional list of labels attached to the monitor. |
| `pulse_check.type` | Check implementation, such as `http` or `tls`. |
| `pulse_check.interval` | Duration between scheduled checks, at least `5ms`, such as `30s`. |
| `pulse_check.timeout` | Check deadline, such as `5s`. |
| `pulse_check.unhealthy_threshold` | Consecutive unsuccessful checks needed for failure. |
| `pulse_check.healthy_threshold` | Consecutive successful checks needed to verify recovery. |
| `pulse_check.config` | Fields specific to the check type. |
| `codes` | Notification rules for incident colors. |
| `intervention` | Optional recovery action and target. |
| `intervention.max_attempts` | Maximum recovery attempts per incident; defaults to one. An unknown outcome still blocks conflicting intervention. |
| `auto_ignore` | Optional explicit incident count and rolling window; omission disables automatic Ignore. |
| `maintenance` | Optional scheduled suppression windows. |

Omitted or zero healthy/unhealthy thresholds normalize to one. For file input,
a positive `max_failures` supplies the unhealthy threshold when its explicit value
is zero; a positive explicit unhealthy threshold wins. These are consecutive
check thresholds, separate from `intervention.max_attempts` and check retries.

For the TLS certificate-expiry check, omitted/zero `warn_days` and `critical_days`
disable those optional age thresholds. Certificate-chain and hostname verification
remain enabled unless explicitly skipped. Skipping them does not disable configured
expiry thresholds.

Use the threshold fields above for new configurations. Accepted `max_failures`
and per-driver `retries` inputs do not grant permission to repeat ambiguous
external actions. Consult the [incident lifecycle](../explanation/incident-lifecycle.md)
for attempt limits, provider rejection and verification behavior.
The timeout bounds the whole check, including configured check retries. A timeout
longer than the cadence does not create overlapping executions for the monitor;
missed scheduled slots remain visible. A `5ms` accepted cadence is a scheduling
resolution, not a throughput or latency guarantee.

## Automatic Ignore

For example, this monitor policy enters Ignore on the fourth distinct outage
reaching its unhealthy threshold in two hours:

~~~yaml
auto_ignore:
  incidents: 4
  window: 2h
~~~

Both fields are required. `incidents` must be 2–1,000; `window` must be a positive
Go duration at most `720h` (30 days). Management resources use `spec.autoIgnore`
with the same `incidents` and `window` fields. Ordinary recoveries before Ignore
do not reset the rolling count. Ignore continues checks and health/history
updates while suppressing ordinary alerts and interventions. Verified recovery
clears Ignore and its count and admits eligible configured green without waiting
for successful delivery. See [Ignore behavior](../explanation/incident-lifecycle.md#ignore-and-repeated-outages).

## Alert destinations and groups

A notification rule can use `notify` plus `config`, as above, or a named `notify_group`. Define reusable destinations at the top level:

~~~yaml
endpoints:
  local-log:
    type: log
    config:
      file: alerts.jsonl
notification_groups:
  operators:
    - local-log
monitors:
  - name: grouped-service
    pulse_check:
      type: http
      interval: 30s
      timeout: 5s
      config:
        url: http://127.0.0.1:8080/health
    codes:
      red:
        dispatch: true
        notify_group: operators
      green:
        dispatch: true
        notify_group: operators
~~~

A group succeeds when at least one destination accepts delivery. It does not require every destination to succeed. `dispatch` defaults to true for a declared rule; set it to false to suppress that rule.

## Maintenance

Add this field to a monitor:

~~~yaml
maintenance:
  - cron: "0 2 * * *"
    duration: 30m
    timezone: UTC
~~~

Cron expressions have five fields. Duration must be positive and at most 366 days;
timezone is an IANA name and defaults to UTC. During a window, checks continue
while ordinary notifications and new recovery admissions are suppressed.
Configured gray enable/disable notices remain eligible during maintenance and
Ignore. Suppressed ordinary work is not released when the window ends.

## Recovery

Add an `intervention` field to a monitor only when the intended action is appropriate for that target:

~~~yaml
intervention:
  max_attempts: 1
  action: webhook
  target:
    url: https://recovery.example.com/restart
    method: POST
    timeout: 10s
~~~

`recovery.example.com` is a placeholder. The per-incident recovery attempt limit
defaults to one. A timeout can leave the external result unknown and held across
restart. The manifest field is `target`, not `config`.
[Recovery fields](jobs-reference.md#recovery-actions)

## Parsing and limits

YAML monitors must be a block sequence. Each monitor entry and metadata section has a 1 MiB limit. JSON is decoded incrementally. Both paths have a decompressed-input budget; streaming does not make memory use independent of monitor count.

Missing, malformed, and semantically invalid manifests fail startup. Unknown
fields, duplicate keys, wrong types, required-setting omissions and unavailable
compiled/platform drivers are rejected. An empty manifest requires
`-allow-empty`. `cpra -validate` performs static validation without opening
storage, constructing operational clients or contacting providers.

Configuration authorizes outbound requests, notifications and actions with the
process account's permissions. In management mode, use protected Credential
resources and references rather than embedding values in shared manifests.
Operators with credential-use/configuration permissions are trusted to select
destinations. Keep bearer tokens and wrapping keys in protected files outside
the state directory; include retained keys separately in your backup procedure.
