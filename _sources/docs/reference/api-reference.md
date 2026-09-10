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
| GET | `/api/v1/monitors/{id}` | One monitor by numeric ID, with additive stable `monitor_id`. |
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

Liveness returns process health, not target health. Readiness requires available durable storage and a nonempty projection no more than 30 seconds old; it does not require all monitors to be healthy.

The durable runtime maintains an incremental monitor index. Monitor and incident responses are paginated and capped at 500 rows; filtering runs on the HTTP goroutine. This does not constitute a million-monitor performance claim.

A fresh process may also lack a snapshot. Treat unavailable responses as unavailable data, not as an empty healthy fleet.

## Dashboard semantics

The dashboard offers Overview, Monitors, Alerts, System, Settings, and monitor detail views. Views refresh periodically from the incremental index.

The healthy-sample percentage uses committed cumulative check counters. It is not an external SLA measurement. Incident, recovery and notification events are retained for 30 days; raw check records are not retained. Pool and queue histories are separate, bounded, in-memory series.

[CLI reference](cli.md) · [Response types in source](https://github.com/ziad-hsn/cpra/blob/370a60b22dcbea3b7552de987ca6a2c5bfaaf671/internal/web/server/types.go)

## Durable state, history and SLOs

| Method | Route | Contract |
| --- | --- | --- |
| GET | `/api/v1/state` | Redacted storage health, process and storage usage. Optional `monitor_id` returns its revision and action states. |
| GET | `/api/v1/history?monitor_id=ID&limit=100&cursor=TOKEN` | Stable monitor event ordering, 100 default and 500 maximum events, 30-day retention and opaque next cursor. |
| GET | `/api/v1/slo` | Five-minute p50/p95/p99, exact threshold attainment, timeouts, missed/pending/overdue checks and recovery coverage. |

These routes use the existing bearer/Basic authentication and reject mutations.
Unavailable history returns 503. Numeric monitor routes remain compatible;
`monitor_id` is the stable identity used by history and recovery state.
History cursors preserve the initial upper committed position during pagination.
See [durability](../durability.md) and [SLO definitions](../slo.md).
