---
title: Candidate · Troubleshooting
description: Candidate · Diagnose startup, authentication, unavailable snapshots, missing optional drivers, notifications and recovery behavior.
cpra_scope: release_candidate
---

> **Release candidate:** this page describes [`410fbfb`](https://github.com/ziad-hsn/cpra/commit/410fbfb0092d01277b3884cd04151c27443a4226), which is separate from `main`. See [version and availability](../../versions.md).


# Troubleshooting

## Startup stops immediately

Check that the manifest exists, is readable, and contains a `monitors` block sequence. Missing or malformed configuration is an error. Intentional empty instances require `-allow-empty`; they can become ready after the controller and storage initialize. Run `cpra -validate -yaml PATH` to validate without opening storage or contacting providers.

Use `cpra -help` for flags. `-config` and `-yaml` both identify the monitor manifest.

## The listener refuses a remote address

A non-loopback bind requires `-web.auth-file` or an authentication token. An empty or unreadable token file fails startup. Place remote access behind HTTPS.

If the browser asks for credentials, use username `cpra` and your configured token. The CLI token file is specified with `--token-file`.

## A monitor remains unknown or the dashboard looks stale

Allow the first check interval and the next snapshot refresh. Snapshots update every five seconds by default. Inspect `cpractl get monitors -o json` and the target's logs.

Readiness checks controller admission/progress and storage. The `projection_fresh` response field reports dashboard freshness separately. A failed current health request should be treated as a connection problem even if the browser still has older data.

## Lists return 503

Inspect `/api/v1/readyz` and `/api/v1/state` for initialization and storage health. The candidate uses a complete incremental index with bounded response pages; the old million-row snapshot cap is not its fleet listing mechanism. Unavailable history can mean an unreadable or missing segment. Do not turn an unavailable response into a successful empty list.

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

Check the rule's `dispatch` setting, group references, maintenance windows, and destination credentials. Inspect each endpoint in `cpractl get state MONITOR_ID` and the retained history. A successful endpoint does not prove another endpoint succeeded, and driver acceptance does not prove recipient delivery.

SMTP authentication is not implemented; use an appropriate relay. PagerDuty needs an Events API v2 routing key. Pushover emergency notifications need valid retry and expiry values.

## Workers grow or latency rises

Inspect `cpractl get queues` and `cpractl get pools -o json`. Check target latency, queued work, current capacity, observation counts, and sizing model before increasing limits. [Sizing and performance](performance-tuning.md)

For a bug report, include your source commit, build tags, a redacted manifest, expected behavior, and relevant logs. Never paste access tokens or private target credentials into a public issue.
