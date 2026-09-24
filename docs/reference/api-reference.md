---
title: HTTP API and dashboard
description: Read CPRa fleet state, monitor details, incidents, queues, worker pools and metrics through the current API.
cpra_scope: main
---

> **Current development checkout:** this reference follows the merged runtime source. See [version and availability](../versions.md) for the distinction between current development and historical candidate guides.


# HTTP API and dashboard

The default listener is `localhost:8060`. The v1 endpoints below provide compatibility observations. With management enabled, the dashboard and v2 API can manage configuration and operator controls. See [management startup](../management-startup.md), [management observations](../management-observations.md), and the [SDK API reference](../sdk/api-reference.md).

## Authentication

Management uses named Bearer credentials and a TLS or explicitly configured trusted-proxy origin. Enter a named token in the dashboard for the current tab. The authentication middleware protects the v1 health, readiness, and metrics routes as well. Legacy read-only authentication accepts Bearer tokens or Basic authentication with username `cpra`; anonymous legacy access requires explicit loopback configuration.

Use `cpractl --token-file` and the TLS settings in [management startup](../management-startup.md). Initial authentication inputs bootstrap committed authority; changing those files does not rotate existing credentials.

## Endpoints

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/v1/healthz` | Process liveness. |
| GET | `/api/v1/readyz` | Controller admission and storage readiness; projection freshness reported separately. |
| GET | `/api/v1/overview` | Fleet counts, status breakdown, and index-cap indicator. |
| GET | `/api/v1/monitors` | Filtered, paginated active-monitor details. |
| GET | `/api/v1/monitors/{id}` | One monitor from the detailed snapshot. |
| GET | `/api/v1/incidents` | Current incident information. |
| GET | `/api/v1/systems` | Controller-system telemetry. |
| GET | `/api/v1/queues` | Current queue telemetry. |
| GET | `/api/v1/queues/history` | In-memory queue history. |
| GET | `/api/v1/pools` | Worker-pool capacity, observations, and sizing state. |
| GET | `/api/v1/pools/history` | In-memory pool history. |
| GET | `/api/v1/config` | Public runtime settings. |
| GET | `/api/v1/history` | Retained monitor events; accepts `monitor_id`, `limit` (1–500, default 100), and an opaque `cursor`. |
| GET | `/api/v1/slo` | Committed check and recovery measurements. |
| GET | `/api/v1/state` | Storage health, process/storage usage, and optional per-monitor revision and action states via `monitor_id`. |
| GET | `/metrics` | Prometheus text exposition. |

## Monitor filtering

~~~sh
curl 'http://localhost:8060/api/v1/monitors?status=down&page=1&size=50'
curl 'http://localhost:8060/api/v1/monitors?type=http&q=example'
~~~

These examples assume explicitly enabled anonymous legacy loopback access. Authenticated deployments require the configured credentials and HTTPS origin.

| Parameter | Meaning |
| --- | --- |
| `page` | One-based page number; defaults to 1. |
| `size` | Page size; defaults to 50 and is capped at 500. |
| `status` | Match the monitor status. |
| `type` | Match the pulse type. |
| `code` | Match the pending notification color. |
| `q` | Case-insensitive substring of the monitor name. |

## Readiness and unavailable data

Liveness returns process health, not target health. Readiness checks controller admission, recent progress, and storage availability. An explicitly empty configuration can be ready. The response reports `projection_fresh` separately; target health and projection freshness are distinct from readiness.

The durable runtime uses an incremental monitor index with bounded, paginated responses. Legacy snapshot fallback can return HTTP 503 when its detailed index is unavailable. This behavior does not establish a supported-capacity benchmark.

A fresh process may also lack a snapshot. Treat unavailable responses as unavailable data, not as an empty healthy fleet.

## Dashboard semantics

The dashboard offers runtime observations and, when management is enabled, configuration and operator controls. Views refresh periodically from server observations.

The healthy-sample percentage uses committed cumulative check counters. It is not an external SLA measurement. Incident, recovery, and notification history is retained according to runtime history configuration; raw check records are not retained. Pool and queue histories are separate, bounded, in-memory series. Unavailable durable state, history, or SLO data returns HTTP 503.

[CLI reference](cli.md) · [Response types in source](https://github.com/ziad-hsn/cpra/blob/4c6baf58df8bf7fd4e02e0399fbe092ba867b20f/internal/httpserver/types.go)

## Management contracts

The [SDK API reference](../sdk/api-reference.md) describes v2 resource and operation contracts. Server availability depends on runtime management configuration and compiled capabilities. The [historical candidate API](../candidate/reference/api-reference.md) remains pinned to its documented revision.

## Appearance

Settings offers System, Light and Dark. System follows the operating-system preference; explicit choices persist across reloads. The top-bar toggle switches the current appearance. Shared roles come from `brand/palette.json` on main.
