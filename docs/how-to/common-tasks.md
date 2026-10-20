---
title: Troubleshooting
description: Diagnose startup, authentication, unavailable snapshots, missing optional drivers, notifications and recovery behavior.
cpra_scope: development
---

See [versions and availability](../versions.md) for implementation and
qualification boundaries.


# Troubleshooting

## Startup stops immediately

Check that the manifest exists, is readable and contains the expected monitor
sequence. Unknown fields, duplicate keys and invalid driver settings fail
validation. Intentional empty file configurations require `-allow-empty`.
Management imports its bootstrap manifest once; later changes use explicit
apply. An initialized empty instance can be ready when empty operation was
explicitly allowed; unavailable storage or admission still makes it unready.

Use `cpra -help` for flags. `-config` and `-yaml` both identify the monitor manifest.

## The listener refuses a remote address

A non-loopback bind requires authentication. Management also requires TLS or
the explicitly configured trusted-proxy contract. Empty/unreadable token files
and presented tokens shorter than 32 printable characters are rejected.
Token length is not proof of randomness. See [protected initialization](../tutorials/quickstart.md).

Management uses a named operator/reader bearer. The dashboard accepts it for
the current tab, and the CLI reads `--token-file`; never put tokens in URLs.
Observation mode intentionally sends a Basic challenge for the HTML page, so
the browser shows its native login prompt. Enter `cpra` as the username and the
configured reader token as the password. API clients may instead send that token
as a bearer. Management uses the dashboard's own named-bearer sign-in form.
Authentication failure remains a failure and does not
downgrade management writes. Trust the generated local CA explicitly in the
browser or configure a trusted certificate; initialization installs no trust roots.

## A monitor remains unknown or the dashboard looks stale

Allow the first scheduled check and committed result. An incremental projection
updates operational rows; configuration can exist before the controller has
observed its generation. Inspect `cpractl get monitors -o json`, describe the
monitor and inspect its target. Disabled/snoozed checks and held unknown actions
are explicit states, not healthy samples.

Readiness reports initialization, admission, storage and controller progress.
Projection freshness is reported separately. Liveness does not fail because
monitored targets are down. A failed current request remains a failure even if
the browser retains older data.

## Lists return 503

Read the safe problem code, request ID and operational state. Leadership
unavailability, temporary contention, unresolved writes and permanent storage
failure have different meanings. History can be unavailable independently of
the current monitor row. Never turn an unavailable response into a successful
empty list. Permanent failures need stopped repair or complete restore, not
automatic deletion or reopening of the data directory.

For a collection rejected before admission, the server's exact contract may
permit explicit continuation of its original handle. Use `cpractl resume
operation/ID -f INPUT` with identical sources; `resume-upload` only transfers
missing chunks. A lost or contradictory mutation response is ambiguous.
See [collection continuation](../cpractl-management.md).

## An optional integration is unavailable

Build its tag and run the resulting binary:

~~~sh
make BUILD_TAGS='redis postgres'
~~~

MongoDB requires a direct `mongodb://` URI; `mongodb+srv://` is rejected. Kubernetes exec credential plugins are unsupported; use token, certificate, or in-cluster credentials.

## Recovery did not run again

The per-incident attempt limit defaults to one; more failures do not
continuously replay the action. Maintenance suppresses new recovery/notification
admission while checks continue. A completed action reports health verification
pending until the configured healthy-check threshold is reached.

An interrupted started action can remain unknown across restart. Inspect the
target/provider evidence and use audited action review; restart does not replay
that uncertain effect. Acknowledge labels a person checking an incident,
dismiss pauses its notifications, snooze pauses checks and notifications for a
period, and disable turns the monitor off until explicitly enabled.

## A notification was not received

Check dispatch settings, contact/group references, maintenance, dismiss/snooze
controls and credential access. Inspect each endpoint's retained action outcome.
Confirmed delivery to one endpoint is not repeated because another is pending.
Provider acceptance does not prove received delivery. Covered queued alerts are
canceled; ending maintenance does not release an accumulated backlog.

SMTP authentication is not implemented; use an appropriate relay. PagerDuty needs an Events API v2 routing key. Pushover emergency notifications need valid retry and expiry values.

## Workers grow or latency rises

Inspect `cpractl get queues` and `cpractl get pools -o json`. Check target latency, queued work, current capacity, observation counts, and sizing model before increasing limits. [Sizing and performance](performance-tuning.md)

For a bug report, include your source commit, build tags, a redacted manifest, expected behavior, and relevant logs. Never paste access tokens or private target credentials into a public issue.
