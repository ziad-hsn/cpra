---
title: "Command-line reference"
description: "Use cpra server flags and cpractl commands to inspect monitors, incidents, queues, pools, configuration and health."
---

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
| `-web` | true | Enable dashboard, API, and metrics. |
| `-web.addr` | `localhost:8060` | Listener address; a remote bind requires authentication. |
| `-web.auth-file` | `CPRA_AUTH_TOKEN_FILE` | Read the authentication token from a file. |
| `-web.auth` | `CPRA_AUTH_TOKEN` | Set a token directly; prefer a file to command-line exposure. |
| `-web.cors` | empty | Comma-separated allowed origins; intended for development. |
| `-debug` | false | Enable debug logging. |
| `-pprof` | false | Enable profiling; use a loopback address. |
| `-pprof.addr` | `localhost:6060` | Profiling listener address. |
| `-ssrf-protect` | false | Block non-public destinations for HTTP(S) paths at connection time. |
| `-version` | false | Print build information and exit. |

A token file overrides a direct or environment token. An unreadable or empty token file fails startup. `CPRA_ENTITY_THRESHOLD` optionally influences startup queue selection; there is no live queue migration.

## Client commands

~~~sh
./bin/cpractl health
./bin/cpractl get overview
./bin/cpractl get monitors --status down
./bin/cpractl get monitors --query example --page 1 --size 50
./bin/cpractl get monitor 2 -o json
./bin/cpractl get incidents
./bin/cpractl get systems
./bin/cpractl get queues
./bin/cpractl get pools
./bin/cpractl get config
./bin/cpractl metrics
~~~

Replace example ID `2` with an ID returned by your instance.

| Global option | Default |
| --- | --- |
| `--server` | `CPRA_SERVER` or `http://localhost:8060` |
| `--token-file` | `CPRA_AUTH_TOKEN_FILE` |
| `--request-timeout` | `10s` |
| `--output` / `-o` | `table`; also accepts `wide`, `json`, and `yaml` |

The client also reads `CPRA_AUTH_TOKEN`. `health` checks liveness; it is not a readiness or fleet-health command. Failed API requests produce an error and an unsuccessful exit status. An HTTP 503 listing response must not be interpreted as zero incidents.

[API semantics](api-reference.md)

## Durable operator queries

```sh
cpractl get state
cpractl get state YOUR_STABLE_MONITOR_ID
cpractl get history YOUR_STABLE_MONITOR_ID --limit 100
cpractl get history YOUR_STABLE_MONITOR_ID --cursor PREVIOUS_NEXT_CURSOR
cpractl get slo -o json
```

Use the existing server and token-file/environment configuration. These commands
are read-only; unknown actions cannot be replayed through the CLI or dashboard.
