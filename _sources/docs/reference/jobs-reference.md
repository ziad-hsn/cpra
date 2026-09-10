---
title: Checks, recovery and notifications
description: Current CPRa driver availability and YAML configuration fields for health checks, recovery actions and notification destinations.
---

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
| `grpc` | TCP port reachability only | default |
| `docker` | Inspect whether a container is running | default |
| `tls` | TLS handshake and certificate-expiry thresholds | default |
| `redis` | RESP PING, with optional authentication/database selection | `redis` |
| `postgres` | PostgreSQL connection and ping | `postgres` |
| `mysql` | MySQL connection and ping | `mysql` |
| `mongo` | MongoDB ping using a direct URI | `mongo` |
| `rabbitmq` | AMQP connection/channel | `rabbitmq` |
| `kafka` | Broker connection and metadata request | `kafka` |

The `grpc` check does not call the gRPC health service; the `service` field does not add that behavior. UDP needs a payload and response. MongoDB requires `mongodb://`; SRV discovery with `mongodb+srv://` is rejected because its initial discovery cannot be bounded by the check deadline.

TLS `warn_days` produces a yellow warning and degraded state without recovery. `critical_days` fails the check and follows the configured incident policy. Leave certificate verification enabled for normal use.

### http

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `url` | string |
| `method` | string |
| `headers` | string map |
| `body` | string |
| `expected_status` | list of integers |
| `insecure_skip_verify` | boolean |
| `retries` | integer |

### tcp

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `port` | integer |
| `retries` | integer |

### icmp

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `ignore_privilege` | boolean |
| `count` | integer |
| `retries` | integer |

### dns

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `server` | string |
| `retries` | integer |

### udp

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `port` | integer |
| `payload` | string |
| `retries` | integer |

### grpc

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `port` | integer |
| `service` | string |
| `retries` | integer |

### docker

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `container` | string |
| `retries` | integer |

### tls

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/pulse_types_new.go)

| YAML field | Value type |
| --- | --- |
| `host` | string |
| `port` | integer |
| `server_name` | string |
| `warn_days` | integer |
| `critical_days` | integer |
| `insecure_skip_verify` | boolean |
| `retries` | integer |

### redis

Build: `redis`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/pulse_types_new.go)

| YAML field | Value type |
| --- | --- |
| `addr` | string |
| `password` | string |
| `username` | string |
| `db` | integer |
| `retries` | integer |

### postgres

Build: `postgres`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/pulse_types_new.go)

| YAML field | Value type |
| --- | --- |
| `dsn` | string |
| `host` | string |
| `port` | integer |
| `user` | string |
| `password` | string |
| `database` | string |
| `sslmode` | string |
| `retries` | integer |

### mysql

Build: `mysql`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/pulse_types_new.go)

| YAML field | Value type |
| --- | --- |
| `dsn` | string |
| `host` | string |
| `port` | integer |
| `user` | string |
| `password` | string |
| `database` | string |
| `retries` | integer |

### mongo

Build: `mongo`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/pulse_types_new.go)

| YAML field | Value type |
| --- | --- |
| `uri` | string |
| `retries` | integer |

### rabbitmq

Build: `rabbitmq`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/pulse_types_new.go)

| YAML field | Value type |
| --- | --- |
| `url` | string |
| `retries` | integer |

### kafka

Build: `kafka`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/pulse_types_new.go)

| YAML field | Value type |
| --- | --- |
| `brokers` | list of strings |
| `retries` | integer |

## Recovery actions

Set `intervention.action` and put fields under `intervention.target`.

| Action | Operation | Build tag |
| --- | --- | --- |
| `docker` | Restart a container | default |
| `webhook` | HTTP request to an external recovery system | default |
| `kubernetes` | Restart or scale a Deployment, StatefulSet, or ReplicaSet | `kubernetes` |
| `aws` | EC2 instance reboot | `aws` |
| `systemd` | Restart a local systemd unit | `systemd` |

Docker recovery always restarts the container; the schema's `type` field does not select start or stop operations. Omitting Docker `timeout` preserves the daemon's stop grace. For Kubernetes, use `kind` values `deployment`, `statefulset`, or `replicaset`. Omitting `replicas` requests a rollout restart; setting it requests scaling. Token, certificate, and in-cluster credentials are supported. Kubeconfig exec credential plugins are rejected.

AWS supports `operation: reboot-instance`. Use the process's configured AWS credential chain. Systemd needs access to the host's system bus and permission for the unit operation.

The controller admits one recovery operation per incident. A response timeout can leave the external outcome uncertain; see the [incident lifecycle](../explanation/incident-lifecycle.md).

### docker

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `type` | string |
| `container` | string |
| `timeout` | duration, such as 10s |

### webhook

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/intervention_types_new.go)

| YAML field | Value type |
| --- | --- |
| `type` | string |
| `url` | string |
| `method` | string |
| `headers` | string map |
| `body` | string |
| `timeout` | duration, such as 10s |

### kubernetes

Build: `kubernetes`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/intervention_types_new.go)

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

Build: `aws`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/intervention_types_new.go)

| YAML field | Value type |
| --- | --- |
| `type` | string |
| `region` | string |
| `operation` | string |
| `instance_id` | string |
| `timeout` | duration, such as 10s |

### systemd

Build: `systemd`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/intervention_types_new.go)

| YAML field | Value type |
| --- | --- |
| `type` | string |
| `unit` | string |
| `mode` | string |
| `timeout` | duration, such as 10s |

## Notifications

A rule uses `notify` plus `config`, or references a reusable `notify_group`. Endpoint objects use `type` plus `config`. See the [group example](config-schema.md#alert-destinations-and-groups).

All listed notifications are included by default except `teams` and `twilio`, which require their respective tags.

- PagerDuty needs an Events API v2 routing key.
- Email uses an SMTP relay with STARTTLS. Username/password SMTP authentication is not implemented.
- Teams uses a Workflow incoming webhook.
- Pushover emergency priority needs `retry` and `expire` in seconds, defaulting to 60 and 1800.
- HTTP notification retries classify 429 and server errors as retryable. Acceptance does not confirm a human received the alert.

### log

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `file` | string |

### slack

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `hook` | string |

### pagerduty

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `routing_key` | string |
| `url` | string |

### email

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/groups.go)

| YAML field | Value type |
| --- | --- |
| `allow_insecure` | boolean |
| `to` | string |
| `from` | string |
| `server` | string |
| `subject` | string |

### webhook

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/groups.go)

| YAML field | Value type |
| --- | --- |
| `url` | string |
| `method` | string |
| `headers` | string map |

### telegram

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `bot_token` | string |
| `chat_id` | string |

### discord

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `webhook_url` | string |

### opsgenie

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/manifest.go)

| YAML field | Value type |
| --- | --- |
| `api_key` | string |
| `url` | string |

### mattermost

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/notification_types_new.go)

| YAML field | Value type |
| --- | --- |
| `webhook_url` | string |
| `channel` | string |
| `username` | string |

### victorops

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/notification_types_new.go)

| YAML field | Value type |
| --- | --- |
| `rest_endpoint_key` | string |
| `routing_key` | string |
| `message_type` | string |
| `entity_id` | string |

### pushover

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/notification_types_new.go)

| YAML field | Value type |
| --- | --- |
| `app_token` | string |
| `user_key` | string |
| `title` | string |
| `priority` | integer |
| `retry` | integer |
| `expire` | integer |
| `sound` | string |

### datadog

Build: default. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/notification_types_new.go)

| YAML field | Value type |
| --- | --- |
| `api_key` | string |
| `app_key` | string |
| `site` | string |
| `tags` | list of strings |

### teams

Build: `teams`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/notification_types_new.go)

| YAML field | Value type |
| --- | --- |
| `webhook_url` | string |

### twilio

Build: `twilio`. [Schema](https://github.com/ziad-hsn/cpra/blob/19bf028f6b1f780d89c147c27b761f1fff577b21/internal/loader/schema/notification_types_new.go)

| YAML field | Value type |
| --- | --- |
| `account_sid` | string |
| `auth_token` | string |
| `from` | string |
| `to` | string |
