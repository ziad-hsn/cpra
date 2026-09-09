---
title: "Troubleshooting"
description: "Diagnose startup, authentication, unavailable snapshots, missing optional drivers, notifications and recovery behavior."
---

# Troubleshooting

## Startup stops immediately

Check that the manifest exists, is readable, and contains a `monitors` block sequence. Missing or malformed configuration is an error. Intentional empty instances require `-allow-empty` and are not ready until a nonempty snapshot exists.

Use `cpra -help` for flags. `-config` and `-yaml` both identify the monitor manifest.

## The listener refuses a remote address

A non-loopback bind requires `-web.auth-file` or an authentication token. An empty or unreadable token file fails startup. Place remote access behind HTTPS.

If the browser asks for credentials, use username `cpra` and your configured token. The CLI token file is specified with `--token-file`.

## A monitor remains unknown or the dashboard looks stale

Allow the first check interval and the next snapshot refresh. Snapshots update every five seconds by default. Inspect `cpractl get monitors -o json` and the target's logs.

Readiness checks snapshot freshness, not whether every target is healthy. A failed current health request should be treated as a connection problem even if the browser still has older data.

## Lists return 503

A snapshot may not be ready, or the detailed index may be capped. Check `/api/v1/overview` for aggregate counts and `index_capped`. Do not turn an unavailable response into a successful empty list.

## An optional integration is unavailable

Build its tag and run the resulting binary:

~~~sh
make BUILD_TAGS='redis postgres'
~~~

MongoDB requires a direct `mongodb://` URI; `mongodb+srv://` is rejected. Kubernetes exec credential plugins are unsupported; use token, certificate, or in-cluster credentials.

## Recovery did not run again

CPRa admits one recovery operation per incident. More failures do not continuously replay the action. Maintenance can suppress new admission. A successful action still needs consecutive healthy checks.

Inspect the target before manually repeating an action or restarting CPRa, especially after a timeout.

## A notification was not received

Check the rule's `dispatch` setting, group references, maintenance windows, and destination credentials. A group succeeds when at least one destination accepts the notification; this does not prove that all recipients received it.

SMTP authentication is not implemented; use an appropriate relay. PagerDuty needs an Events API v2 routing key. Pushover emergency notifications need valid retry and expiry values.

## Workers grow or latency rises

Inspect `cpractl get queues` and `cpractl get pools -o json`. Check target latency, queued work, current capacity, observation counts, and sizing model before increasing limits. [Sizing and performance](performance-tuning.md)

For a bug report, include your source commit, build tags, a redacted manifest, expected behavior, and relevant logs. Never paste access tokens or private target credentials into a public issue.
