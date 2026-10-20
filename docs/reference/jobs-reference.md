---
title: Checks, recovery and notifications
description: Current CPRa driver availability and YAML configuration fields for health checks, recovery actions and notification destinations.
cpra_scope: main
---

> **Current development checkout:** this reference describes the current source. See [version and availability](../versions.md) for its verification and publication status.


# Checks, recovery and notifications

Default integrations are included in a normal build. Optional integrations require the build tag shown below:

~~~sh
make BUILD_TAGS='redis postgres kubernetes'
~~~

The field tables list the YAML configuration names from the current schema. A listed field is not necessarily required. Validate your target and credentials against the implementation; provider accounts and external service permissions are outside the configuration parser.

## Checks

Set `pulse_check.type` and put driver-specific fields under `pulse_check.config`.

| Type | Operation | Build tag |
| --- | --- | --- |
| `http` | HTTP request and status check | default |
| `tcp` | TCP connection | default |
| `icmp` | ICMP reachability; OS privileges may be needed | default |
| `dns` | DNS resolution | default |
| `udp` | Send a payload and await a reply | default |
| `grpc` | TCP reachability by default; explicit unary health mode | default |
| `docker` | Inspect whether a container is running | default |
| `tls` | TLS handshake and certificate-expiry thresholds | default |
| `redis` | RESP PING, with optional authentication/database selection | `redis` |
| `postgres` | PostgreSQL connection and ping | `postgres` |
| `mysql` | MySQL connection and ping | `mysql` |
| `mongo` | MongoDB ping using a direct URI | `mongo` |
| `rabbitmq` | AMQP connection/channel | `rabbitmq` |
| `kafka` | Broker connection and metadata request | `kafka` |

The `grpc` check preserves TCP reachability when `mode` is omitted. With `mode: health`, it calls `grpc.health.v1.Health/Check` for the configured `service` and accepts only `SERVING`. An absent health RPC or unknown service fails; there is no TCP fallback. Health mode uses verified TLS by default; use `allow_insecure: true` only for explicitly selected cleartext targets. This follows the [gRPC health contract](https://grpc.io/docs/guides/health-checking/). UDP needs a payload and response. MongoDB requires `mongodb://`; SRV discovery with `mongodb+srv://` is rejected because its initial discovery cannot be bounded by the check deadline.

TLS `warn_days` produces a yellow warning and degraded state without recovery. `critical_days` fails the check and follows the configured incident policy. Leave certificate verification enabled for normal use.

**TLS policy.**

TLS-capable checks support a monitor-specific `ca_file`, `server_name`, and
`insecure_skip_verify`. The CA file must be a readable regular PEM file of at most
1 MiB; it supplies that monitor's trust roots. Static validation does not read the
file. The service identity must be able to read it when executing the check.
Relative CA paths resolve from the process working directory; service manifests
should use absolute paths. Combining a CA file (or CA credential reference) with
`insecure_skip_verify: true` is rejected rather than silently ignoring trust roots.

A missing, unreadable or invalid CA file is a monitor configuration problem.
CPRa preserves the last observed health and incident facts, suppresses new
interventions, and retries local preparation at the normal check cadence. It
does not contact the target or count a preparation failure as a service outage
or recovery. Queued recovery keeps its original deadline while held; expiry
cancels that unsent action. Fixing the file allows the next real check to update
health. Missed work remains visible in SLO accounting.

Certificate verification is enabled by default. Explicit
`insecure_skip_verify: true` disables certificate-chain and hostname verification
for that monitor; it does not select cleartext transport, disable configured TLS
expiry thresholds, skip recovery health verification, or change CPRa API
authentication. Check summaries identify disabled certificate verification.

For Redis, PostgreSQL, MySQL, MongoDB, RabbitMQ and Kafka, using these typed TLS
settings requires an explicit `tls: true`. `tls: false` selects cleartext and
cannot be combined with trust, server-name or skip-verification settings. Existing
DSN/URI transport choices remain supported, but contradictory typed and DSN/URI
settings are rejected. For example, PostgreSQL `tls: true` with verification
requires `sslmode: verify-full`; `sslmode: disable` contradicts it. HTTPS is
required for HTTP TLS settings. gRPC TLS settings require `mode: health` and
cannot be combined with `allow_insecure: true`. Checks without TLS semantics
reject these settings.

### http

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `url` | string |
| `method` | string |
| `headers` | string map |
| `body` | string |
| `expected_status` | list of integers |
| `ca_file` | optional PEM trust-root file |
| `server_name` | optional TLS server name |
| `insecure_skip_verify` | boolean |
| `retries` | integer |

By default, any `2xx` response is healthy. A nonempty `expected_status` list
replaces that rule: only the listed status codes are healthy. For example,
`expected_status: [302]` checks that the configured URL returns `302`.
CPRa never follows the response's `Location` redirect, including when that
redirect status is explicitly accepted. Configure the destination URL directly
to monitor its health. HTTP checks connect directly and do not use ambient
proxy environment variables such as `HTTP_PROXY` or `HTTPS_PROXY`.

### tcp

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `port` | integer |
| `retries` | integer |

### icmp

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `ignore_privilege` | boolean |
| `count` | integer |
| `retries` | integer |

`ignore_privilege: true` selects UDP-based unprivileged ICMP on Linux or macOS.
Linux must permit the service account's group in `net.ipv4.ping_group_range`.
The default uses privileged mode; Windows requires that library mode even when
the operating system does not require elevation. Other unprivileged platform
combinations are rejected before dispatch. CPRa does not grant capabilities or
change host settings. See the [pro-bing platform requirements](https://github.com/prometheus-community/pro-bing/tree/v0.9.1#supported-operating-systems).

### dns

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `server` | string |
| `retries` | integer |

### udp

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `port` | integer |
| `payload` | string |
| `retries` | integer |

### grpc

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `port` | integer |
| `service` | string |
| `retries` | integer |
| `mode` | `tcp` or `health`; omitted selects `tcp` |
| `allow_insecure` | boolean; health mode only |
| `server_name` | optional TLS server name; health mode only |
| `ca_file` | optional PEM trust roots, at most 1 MiB; health mode only |
| `insecure_skip_verify` | boolean; health mode only |

```yaml
monitors:
  - name: billing-health
    pulse_check:
      type: grpc
      interval: 60s
      timeout: 5s
      config:
        host: billing.example.com
        port: 443
        mode: health
        service: billing.v1.Billing
```

Validation checks the configuration without reading `ca_file` or making an RPC.
The service identity must be able to read an explicitly configured CA file at
execution. The check timeout bounds the whole RPC and its configured attempts.

### docker

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `container` | string |
| `retries` | integer |

### tls

Build: default. [Schema](source/internal/manifest/pulse_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `port` | integer |
| `server_name` | string |
| `warn_days` | integer |
| `critical_days` | integer |
| `ca_file` | optional PEM trust-root file |
| `insecure_skip_verify` | boolean |
| `retries` | integer |

### redis

Build: `redis`. [Schema](source/internal/manifest/pulse_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `addr` | string |
| `password` | string |
| `username` | string |
| `db` | integer |
| `tls` | optional boolean; explicit transport choice |
| `ca_file` | optional PEM trust-root file |
| `server_name` | optional TLS server name |
| `insecure_skip_verify` | boolean; requires TLS |
| `retries` | integer |

### postgres

Build: `postgres`. [Schema](source/internal/manifest/pulse_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `dsn` | string |
| `host` | string |
| `port` | integer |
| `user` | string |
| `password` | string |
| `database` | string |
| `sslmode` | string |
| `tls` | optional boolean; explicit transport choice |
| `ca_file` | optional PEM trust-root file |
| `server_name` | optional TLS server name |
| `insecure_skip_verify` | boolean; requires TLS |
| `retries` | integer |

### mysql

Build: `mysql`. [Schema](source/internal/manifest/pulse_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `dsn` | string |
| `host` | string |
| `port` | integer |
| `user` | string |
| `password` | string |
| `database` | string |
| `tls` | optional boolean; explicit transport choice |
| `ca_file` | optional PEM trust-root file |
| `server_name` | optional TLS server name |
| `insecure_skip_verify` | boolean; requires TLS |
| `retries` | integer |

### mongo

Build: `mongo`. [Schema](source/internal/manifest/pulse_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `uri` | string |
| `tls` | optional boolean; explicit transport choice |
| `ca_file` | optional PEM trust-root file |
| `server_name` | optional TLS server name |
| `insecure_skip_verify` | boolean; requires TLS |
| `retries` | integer |

### rabbitmq

Build: `rabbitmq`. [Schema](source/internal/manifest/pulse_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `url` | string |
| `tls` | optional boolean; explicit transport choice |
| `ca_file` | optional PEM trust-root file |
| `server_name` | optional TLS server name |
| `insecure_skip_verify` | boolean; requires TLS |
| `retries` | integer |

### kafka

Build: `kafka`. [Schema](source/internal/manifest/pulse_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `brokers` | list of strings |
| `tls` | optional boolean; explicit transport choice |
| `ca_file` | optional PEM trust-root file |
| `server_name` | optional TLS server name |
| `insecure_skip_verify` | boolean; requires TLS |
| `retries` | integer |

## Recovery actions

Set `intervention.action` and put fields under `intervention.target`.

| Action | Operation | Build tag |
| --- | --- | --- |
| `docker` | Restart a container | default |
| `webhook` | HTTP request to an external recovery system | default |
| `kubernetes` | Restart or scale a Deployment | `kubernetes` |
| `aws` | EC2 instance reboot | `aws` |
| `systemd` | Restart a local systemd unit | `systemd` |
| `a2a` | Request and observe an agent intervention | default; local candidate |

Docker recovery always restarts the container; the schema's `type` field does not select start or stop operations. Omitting Docker `timeout` preserves the daemon's stop grace. Its separate `operation_timeout` defaults to the larger of 60s and explicit stop grace plus 20s. An explicitly shorter overall budget is rejected. Interrupted operations retain an unknown outcome and are not repeated automatically. Kubernetes recovery currently supports `kind: deployment`. Omitting `replicas` requests a rollout restart; setting it requests scaling. Token, certificate, and in-cluster credentials are supported. Kubeconfig exec credential plugins are rejected.

AWS supports `operation: reboot-instance`. Use the process's configured AWS credential chain. Systemd needs access to the host's system bus and permission for the unit operation.

`intervention.max_attempts` limits recovery attempts per incident and defaults to
one. A response timeout can leave the external outcome uncertain and block
conflicting interventions regardless of the remaining attempt limit; see the
[incident lifecycle](../explanation/incident-lifecycle.md).

### docker

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `type` | string |
| `container` | string |
| `timeout` | duration, such as 10s |
| `operation_timeout` | overall duration, such as 60s |

### webhook

Build: default. [Schema](source/internal/manifest/intervention_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `type` | string |
| `url` | string |
| `method` | string |
| `headers` | string map |
| `body` | string |
| `timeout` | duration, such as 10s |

### kubernetes

Build: `kubernetes`. [Schema](source/internal/manifest/intervention_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `type` | string |
| `kubeconfig_path` | string |
| `namespace` | string |
| `kind` | string |
| `name` | string |
| `replicas` | optional integer |
| `timeout` | duration, such as 10s |

### aws

Build: `aws`. [Schema](source/internal/manifest/intervention_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `type` | string |
| `region` | string |
| `operation` | string |
| `instance_id` | string |
| `timeout` | duration, such as 10s |

### systemd

Build: `systemd`. [Schema](source/internal/manifest/intervention_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `type` | string |
| `unit` | string |
| `mode` | string |
| `timeout` | duration, such as 10s |

### a2a

Build: default in the local, unreleased candidate. Uses an explicitly configured A2A 1.0 HTTP+JSON interface. This addition has not been published to main. See [A2A driver settings](jobs-reference.md).

| Field | Meaning |
| --- | --- |
| `url` | Agent interface base URL; HTTPS required by default. Management resources use a secret reference. |
| `token` | Optional dedicated agent bearer token; management resources use a secret reference. |
| `tenant` | Optional tenant, at most 256 bytes without control characters. |
| `request_timeout` | Per-request deadline, default `10s`, at most `5m`. |
| `task_timeout` | Overall intervention deadline, including polling and receipt commits; default `60s`, range `1s`–`5m`. |
| `poll_interval` | Task observation interval, `1s`–`30s`, no greater than task timeout. Default is the smaller of `5s` and task timeout. |
| `allow_insecure_http` | Explicitly permit HTTP to this trusted endpoint; default false. |

The smaller of the remaining overall deadline and the request deadline applies to each call. CPRa sends one message, records the task reference before polling, and verifies monitor health after reported task completion. A lost response or expired deadline holds the outcome as unknown. Agent output cannot request another CPRa operation. Automatic escalation after another recovery driver's failure remains under implementation.

## Notifications

A rule uses `notify` plus `config`, or references a reusable `notify_group`. Endpoint objects use `type` plus `config`. See the [group example](config-schema.md#alert-destinations-and-groups).

All listed notifications are included by default except `teams` and `twilio`, which require their respective tags.

- PagerDuty needs an Events API v2 routing key.

PagerDuty trigger and resolve requests use the committed incident and endpoint
identity, so retries and recovery keep the same `dedup_key`. The configured
Code severity maps to `critical`, `warning` or `info`. Opsgenie uses the matching
incident/endpoint alias for create and close, maps critical/warning/informational
Codes to P1/P3/P5, and checks the asynchronous request status after a 202 receipt.
An unreadable or unsuccessful processing receipt holds the outcome as unknown;
it does not authorize repetition of the accepted request. These boundaries follow
[PagerDuty's event contract](https://docs.pagerduty.com/developer/send-alert-event)
and the [Opsgenie alert API](https://docs.opsgenie.com/docs/alert-api).
Provider processing success does not prove a human received the notification.
- Email uses an SMTP relay with STARTTLS. Username/password SMTP authentication is not implemented.
- Teams uses a Workflow incoming webhook.
- Pushover emergency priority needs `retry` and `expire` in seconds, defaulting to 60 and 1800.
- HTTP notification server/gateway errors, partial transmission and lost replies
  leave the external outcome unknown and held. Connection refusal before sending,
  certificate rejection during the handshake, and local file rejection before
  writing are definite no-effect failures. Only a confirmed transient rejection
  or no-effect failure permits another controller-owned attempt, up to three
  attempts. Bad local configuration and trust failures need correction. Provider
  acceptance does not confirm a human received the alert.
- Ordinary notifications receive one 45-second budget when their intent is
  committed. Each HTTP call is bounded by ten seconds and the remaining budget.
  Retry, queue delay and restart never renew that budget; expired queued work
  cannot start. Agent and external-worker notifications retain their configured
  overall budgets. During storage upgrade, an old queued notification lacking a
  recorded original deadline is canceled instead of acquiring a new budget.
- `intervention.max_attempts` caps confirmed attempts within the original
  recovery budget. It cannot authorize replay of an unknown external effect.
  A separately authorized manual recovery has a new budget and still requires
  the existing review, health and executor-fencing conditions.
- Intervention completion reports “health verification pending.” A recovered
  health notification requires the configured successful-check threshold.

### log

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `file` | string |

### slack

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `hook` | string |

### pagerduty

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `routing_key` | string |
| `url` | string |

### email

Build: default. [Schema](source/internal/manifest/groups.go.txt)

| YAML field | Value type |
| --- | --- |
| `allow_insecure` | boolean |
| `to` | string |
| `from` | string |
| `server` | string |
| `subject` | string |

### webhook

Build: default. [Schema](source/internal/manifest/groups.go.txt)

| YAML field | Value type |
| --- | --- |
| `url` | string |
| `method` | string |
| `headers` | string map |

### telegram

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `url` | string; optional complete operation URL |
| `test_mode` | boolean; Telegram test environment, mutually exclusive with `url` |
| `bot_token` | string |
| `chat_id` | string |

### discord

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `webhook_url` | string |

### opsgenie

Build: default. [Schema](source/internal/manifest/manifest.go.txt)

| YAML field | Value type |
| --- | --- |
| `api_key` | string |
| `url` | string |

### mattermost

Build: default. [Schema](source/internal/manifest/notification_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `webhook_url` | string |
| `channel` | string |
| `username` | string |

### victorops

Build: default. [Schema](source/internal/manifest/notification_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `url` | string; optional complete operation URL |
| `rest_endpoint_key` | string |
| `routing_key` | string |
| `message_type` | string |
| `entity_id` | string |

### pushover

Build: default. [Schema](source/internal/manifest/notification_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `url` | string; optional complete operation URL |
| `app_token` | string |
| `user_key` | string |
| `title` | string |
| `priority` | integer |
| `priority_by_color` | map of notification color to integer priority, -2 through 2 |
| `retry` | integer |
| `expire` | integer |
| `sound` | string |

A shared emergency `priority: 2` applies to yellow/red/cyan notifications. Green
and gray use normal priority unless explicitly overridden by `priority_by_color`.
For example, `priority_by_color: {green: 2}` deliberately makes recovery notices
emergency pages. Emergency retry defaults to 60 seconds (minimum 30); expiry
defaults to 1,800 seconds and must be between 1 and 10,800 seconds.

### datadog

Build: default. [Schema](source/internal/manifest/notification_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `url` | string; optional complete operation URL |
| `api_key` | string |
| `app_key` | string |
| `site` | string |
| `tags` | list of strings |

### teams

Build: `teams`. [Schema](source/internal/manifest/notification_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `webhook_url` | string |

### twilio

Build: `twilio`. [Schema](source/internal/manifest/notification_types_new.go.txt)

| YAML field | Value type |
| --- | --- |
| `url` | string; optional complete operation URL |
| `account_sid` | string |
| `auth_token` | string |
| `from` | string |
| `to` | string |

### a2a

Build: default in the local, unreleased candidate. Delivers bounded incident context for investigation. A received message or accepted task records delivery acceptance; it does not establish recovery. Notifications never poll or cancel a remote task. Use a credential with only the intended notification/investigation authority.

| Field | Meaning |
| --- | --- |
| `url` | Explicit A2A HTTP+JSON base URL; management resources use a secret reference. |
| `token` | Optional notification-specific bearer token; management resources use a secret reference. |
| `tenant` | Optional tenant, at most 256 bytes without control characters. |
| `request_timeout` | Per-request deadline, default `10s`, at most `5m`. |
| `timeout` | Overall delivery/receipt deadline, default `60s`, range `1s`–`5m`. |
| `allow_insecure_http` | Explicitly permit HTTP to this trusted endpoint; default false. |

For both A2A roles, manifests use the snake_case names above. SDK/API fields use `requestTimeout`, `taskTimeout`, `pollInterval`, and `allowInsecureHttp`. The API's driver `credentialRefs.url` and `credentialRefs.token` select separately stored encrypted secrets. URL userinfo, queries, fragments, redirects, and ambient proxies are unsupported.
