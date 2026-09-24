---
title: Command-line reference
description: Use cpra server flags and cpractl commands to inspect monitors, incidents, queues, pools, configuration and health.
cpra_scope: main
---

> **Current development checkout:** this reference follows the merged runtime source. See [version and availability](../versions.md) for the distinction between current development and historical candidate guides.


# Command-line reference

## Server

~~~sh
./bin/cpra -yaml monitors.yaml
./bin/cpra -version
./bin/cpra -help
~~~

| Flag | Default | Purpose |
| --- | --- | --- |
| `-yaml` | `monitors.yaml` | YAML or JSON monitor manifest. |
| `-config` | empty | Alias overriding `-yaml`. |
| `-allow-empty` | false | Permit a deliberately empty manifest. |
| `-web` | true | Enable dashboard, API, and metrics; management writes require runtime configuration. |
| `-web.addr` | `localhost:8060` | Listener address; a remote bind requires authentication. |
| `-web.auth-file` | `CPRA_AUTH_TOKEN_FILE` | Read the authentication token from a file. |
| `-web.auth` | `CPRA_AUTH_TOKEN` | Set a token directly; prefer a file to command-line exposure. |
| `-web.cors` | empty | Comma-separated allowed origins; intended for development. |
| `-debug` | false | Enable debug logging. |
| `-pprof` | false | Enable profiling; use a loopback address. |
| `-pprof.addr` | `localhost:6060` | Profiling listener address. |
| `-ssrf-protect` | false | Block non-public destinations for HTTP(S) paths at connection time. |
| `-version` | false | Print build information and exit. |
| `-runtime-config` | empty | Runtime storage, history, SLO, authentication, and management configuration. |
| `-data-dir` | empty | Override durable storage directory. |
| `-validate` | false | Validate configuration and compiled drivers without opening storage or providers. |
| `-capabilities` | false | Print compiled driver capabilities as JSON and exit. |
| `-shutdown-timeout` | `45s` | Maximum shutdown time; Windows service caps it at `15s`. |

For initial legacy authentication, a token file overrides a direct or environment token. An unreadable or empty token file fails startup. Once authentication authority is committed, stopped local authentication operations manage credentials; changing bootstrap files does not rotate them. See [management startup](../management-startup.md). `CPRA_ENTITY_THRESHOLD` optionally influences startup queue selection; there is no live queue migration.

## Client commands

~~~sh
./bin/cpractl health
./bin/cpractl ready
./bin/cpractl get monitors --limit 100
./bin/cpractl get monitor service-api -o json
./bin/cpractl get incidents
./bin/cpractl get systems
./bin/cpractl get queues
./bin/cpractl get pools
./bin/cpractl get config
./bin/cpractl metrics
~~~

Replace `service-api` with a stable monitor resource ID returned by your instance. Current `cpractl` uses v2 management APIs and cursor pagination; configure a management server and named authentication before these queries. See [management commands](../cpractl-management.md) for resource writes, collections, transport setup, and local administration.

| Global option | Default |
| --- | --- |
| `--server` | `CPRA_SERVER` or `http://localhost:8060` |
| `--token-file` | `CPRA_AUTH_TOKEN_FILE` |
| `--ca-file` | `CPRA_CA_FILE` |
| `--allow-insecure-http` | false; explicitly permits authenticated HTTP for the configured origin |
| `--request-timeout` | `10s` |
| `--output` / `-o` | `table`; also accepts `wide`, `json`, and `yaml` |

The client also reads `CPRA_AUTH_TOKEN`. `health` checks liveness; it is not a readiness or fleet-health command. Failed API requests produce an error and an unsuccessful exit status. An HTTP 503 listing response must not be interpreted as zero incidents.

[API semantics](api-reference.md)
