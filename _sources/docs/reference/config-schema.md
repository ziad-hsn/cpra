---
title: "Monitor configuration"
description: "Configure CPRa YAML manifests, check thresholds, alert groups, maintenance windows and recovery targets."
---

# Monitor configuration

Pass a YAML or JSON monitor manifest to `cpra -yaml PATH`. `-config` is an alias for the same manifest; it does not select a separate server-settings file. Configuration is loaded at startup. Changes require restarting the process.

## A complete minimal manifest

~~~yaml
monitors:
  - name: example-service
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
| `id` | Optional stable ID; otherwise a deterministic hash of `name`. Duplicate effective IDs are rejected. |
| `name` | Required monitor name; use a descriptive name you can search for. |
| `enabled` | Whether the monitor runs; defaults to true when omitted. |
| `tags` | Optional list of labels attached to the monitor. |
| `pulse_check.type` | Check implementation, such as `http` or `tls`. |
| `pulse_check.interval` | Positive duration between scheduled checks, such as `30s`. |
| `pulse_check.timeout` | Check deadline, such as `5s`. |
| `pulse_check.unhealthy_threshold` | Consecutive unsuccessful checks needed for failure. |
| `pulse_check.healthy_threshold` | Consecutive successful checks needed to verify recovery. |
| `pulse_check.config` | Fields specific to the check type. |
| `codes` | Notification rules for incident colors. |
| `intervention` | Optional recovery action and target. |
| `maintenance` | Optional scheduled suppression windows. |

Use the threshold fields above for new configurations. Legacy `max_failures` and per-driver `retries` fields are compatibility inputs, not a promise to replay a recovery action. Consult the [incident lifecycle](../explanation/incident-lifecycle.md) for the actual behavior.

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

Cron expressions have five fields. Duration must be positive and at most 366 days; timezone is an IANA name and defaults to UTC. During a window, checks continue while notifications and new recovery admissions are suppressed.

## Recovery

Add an `intervention` field to a monitor only when the intended action is appropriate for that target:

~~~yaml
intervention:
  action: webhook
  target:
    url: https://recovery.example.com/restart
    method: POST
    timeout: 10s
~~~

`recovery.example.com` is a placeholder. CPRa admits one recovery operation per incident; a timeout can leave the external result unknown. The target object is named `target`, not `config`. [Recovery fields](jobs-reference.md#recovery-actions)

## Parsing and limits

YAML monitors must be a block sequence. Each monitor entry and metadata section has a 1 MiB limit. JSON is decoded incrementally. Both paths have a decompressed-input budget; streaming does not make memory use independent of monitor count.

Missing, malformed, and semantically invalid manifests fail startup. An empty manifest requires `-allow-empty`. The programmatic streaming loader has an optional strict unknown-field mode; the server does not expose a `--strict` flag.

Treat configuration as trusted input: it authorizes outbound requests, notifications, and actions with the process account's permissions. Keep credentials in private manifests and token files.

## Separate runtime configuration

`-runtime-config PATH` selects storage, history and SLO settings without changing
`-yaml` or the `-config` manifest alias. Default storage is single-node Raft in
`./cpra-data`. Explicit `storage: {mode: memory}` selects disposable state.
See [runtime settings and complete backups](../durability.md) and
[latency targets](../slo.md). Changing targets updates the configuration revision
and cancels old unsent work; unknown outcomes and retained history survive.
