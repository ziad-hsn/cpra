---
title: Monitor Configuration Schema
parent: Reference
---

# Monitor Configuration Schema

The CPRA system is configured via a single YAML file that defines a list of monitors. This document provides a complete reference for the `Monitor` object schema.

## Top-Level Monitor Object

| Field | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `name` | `string` | Yes | A unique, human-readable name for the monitor. |
| `enabled` | `boolean` | No | If `false`, the monitor is loaded but never scheduled. Defaults to `true`. |
| `pulse` | `PulseConfig` | Yes | Configuration for the health check pipeline. |
| `intervention` | `InterventionConfig` | No | Configuration for the automated remediation pipeline. |
| `codes` | `map[string]CodeConfig` | No | Configuration for the alerting pipeline, mapped by alert "color" (e.g., Red, Yellow). |

## PulseConfig (Health Check)

Defines the parameters for the health check.

| Field | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `type` | `string` | Yes | The type of check: `http`, `tcp`, or `script`. |
| `interval` | `duration` | Yes | How often the check should run (e.g., `30s`, `5m`). |
| `timeout` | `duration` | Yes | Maximum time to wait for the check to complete (e.g., `5s`). |
| `unhealthy_threshold` | `integer` | No | Number of consecutive failures before the monitor is considered unhealthy and triggers Intervention. Defaults to `1`. |
| `healthy_threshold` | `integer` | No | Number of consecutive successes required to transition from unhealthy to healthy. Defaults to `1`. |
| `config` | `map[string]any` | Yes | Type-specific configuration (e.g., `http` details). |

### `config` Examples

**HTTP Check:**
```yaml
config:
  method: GET
  url: https://api.example.com/health
  headers:
    - "Authorization: Bearer token"
  expected_status: 200
```

**TCP Check:**
```yaml
config:
  host: database.internal
  port: 5432
```

## InterventionConfig (Remediation)

Defines the automated action to take when the `unhealthy_threshold` is met.

| Field | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `action` | `string` | Yes | The action to perform: `script` or `webhook`. |
| `max_failures` | `integer` | No | Maximum number of times to attempt the intervention before giving up and triggering the Code pipeline. Defaults to `1`. |
| `config` | `map[string]any` | Yes | Action-specific configuration. |

### `config` Example (Script Action)

```yaml
config:
  path: /usr/local/bin/restart_service.sh
  args: ["--force", "api-service"]
```

## CodeConfig (Alerting)

Defines the alerting policy. The map key (e.g., `Red`) is the name of the alert "color" or severity.

| Field | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `dispatch` | `string` | Yes | The trigger condition: `failure` (on Intervention failure) or `always` (on any Pulse failure). |
| `notify` | `string` | Yes | The notification method: `webhook`, `email`, or `slack`. |
| `config` | `map[string]any` | Yes | Notification-specific configuration. |

### `config` Example (Webhook Notification)

```yaml
config:
  url: https://hooks.slack.com/services/T00000000/B00000000/XXX
  payload:
    text: "Critical API is DOWN. Intervention failed."
```
