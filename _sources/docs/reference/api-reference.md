---
title: "HTTP API and dashboard"
description: "Read CPRa fleet state, monitor details, incidents, queues, worker pools and metrics through the current API."
---

# HTTP API and dashboard

The default server is `http://localhost:8060`. The dashboard and API inspect state; they do not create monitors, change configuration, or trigger recovery commands.

## Authentication

When a token is configured, use HTTP Bearer authentication for API requests. Browser login uses username `cpra` and the token as the password. The authentication middleware also protects health, readiness, and metrics endpoints.

For remote requests, use an HTTPS reverse proxy and `cpractl --token-file` where possible. A non-loopback listener requires a token. See [deployment](../how-to/deploy-to-production.md).

## Endpoints

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/v1/healthz` | Process liveness. |
| GET | `/api/v1/readyz` | Snapshot readiness. |
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
| GET | `/metrics` | Prometheus text exposition. |

## Monitor filtering

~~~sh
curl 'http://localhost:8060/api/v1/monitors?status=down&page=1&size=50'
curl 'http://localhost:8060/api/v1/monitors?type=http&q=example'
~~~

These examples assume the default local listener without token authentication.

| Parameter | Meaning |
| --- | --- |
| `page` | One-based page number; defaults to 1. |
| `size` | Page size; defaults to 50 and is capped at 500. |
| `status` | Match the monitor status. |
| `type` | Match the pulse type. |
| `code` | Match the pending notification color. |
| `q` | Case-insensitive substring of the monitor name. |

## Readiness and unavailable data

Liveness returns process health, not target health. Readiness requires a nonempty snapshot no more than 30 seconds old; it does not require all monitors to be healthy.

The detailed snapshot index is capped at 1,000,000 entries. Above that cap, monitor and incident list requests return HTTP 503. Overview aggregates remain available. This cap is an implementation bound, not a supported-capacity benchmark.

A fresh process may also lack a snapshot. Treat unavailable responses as unavailable data, not as an empty healthy fleet.

## Dashboard semantics

The dashboard offers Overview, Monitors, Alerts, System, Settings, and monitor detail views. Fleet snapshots refresh every five seconds by default.

The healthy-sample percentage is based on snapshots observed during the current process run. It is not an external SLA measurement. Per-monitor historical results are not retained. Pool and queue histories are separate, bounded, in-memory series.

[CLI reference](cli.md) · [Response types in source](https://github.com/ziad-hsn/cpra/blob/a370969b041b399c0778318d8915ce059fd74294/internal/web/server/types.go)
