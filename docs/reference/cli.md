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
| `-web.addr` | runtime `web.address`, otherwise `localhost:8060` | Explicit listener override; a remote bind requires authentication. |
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
| `-validate` | false | Validate the supplied input and compiled drivers without applying it, opening storage or contacting providers. Incidental client authentication environment values are ignored; explicit authentication options are validated. |
| `-capabilities` | false | Print compiled driver capabilities as JSON and exit. |
| `-shutdown-timeout` | `45s` | Maximum shutdown time; Windows service caps it at `15s`. |

For initial observation authentication, a token file overrides a direct or environment token. An unreadable or empty token file fails startup. Once authentication authority is committed, stopped local authentication operations manage credentials; changing bootstrap files does not rotate them. See [management startup](../management-startup.md). `CPRA_ENTITY_THRESHOLD` optionally influences startup queue selection; there is no live queue migration.

## Client commands

~~~sh
./bin/cpractl version
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

Replace `service-api` with a stable monitor resource ID returned by your instance. Current `cpractl` uses access discovery and bounded v2 observations in either
management or observation mode. Configuration writes require management and a
permitted named identity; a failed write never falls back to observation mode. See [management commands](../cpractl-management.md) for resource writes, collections, transport setup, and local administration.

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

## Signals and repeated connections

On Unix, SIGINT/SIGTERM begin bounded graceful shutdown and log its start.
SIGHUP records an unsupported-reload notice and does not reload files or stop
CPRa. Apply configuration explicitly. A second termination signal allows the
supervisor/operator to terminate a stuck process. Machine timestamps use UTC;
the dashboard presents times in the browser's local zone.

Use `CPRA_SERVER`, `CPRA_CA_FILE` and `CPRA_AUTH_TOKEN_FILE` for repeatable client
connections. The initializer prints exact non-secret paths. Prefer the token
file over `CPRA_AUTH_TOKEN`; the latter is read after flag parsing and never
included in help/error defaults. Keep tokens out of URLs and shell arguments.
Use `cpractl local --help` and `cpractl local service --help` for local operations;
these are separate from API requests. Probe commands perform API reads only.

`cpractl local init --management --port 19060` selects a different loopback API
port for a fresh installation. The default is 8060. Initialization writes
`web.address` in runtime configuration and prints matching connection commands;
generated services read that value at startup. An explicit server `-web.addr`
still takes precedence. Initialization never overwrites an existing runtime file.

CLI errors distinguish local input/token/CA files, TLS verification, authentication
(401), authorization (403), throttling (429), and unavailable readiness (503).
An unconfirmed mutation retains its operation handle and uncertainty warning;
these diagnostics do not enable an automatic retry.

## Process logs

Ordinary foreground and service runs use JSON at info level with UTC timestamps.
Set `CPRA_ENV=development` explicitly for verbose console diagnostics, or select
`CPRA_LOG_LEVEL` and `CPRA_LOG_FORMAT` directly. The first single-node Raft election
is informational; later election warnings remain visible. No output format adds
terminal colour to JSON or file records.
