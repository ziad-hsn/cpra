---
title: Candidate · Command-line reference
description: Candidate · Use cpra server flags and cpractl commands to inspect monitors, incidents, queues, pools, configuration and health.
cpra_scope: release_candidate
---

> **Release candidate:** this page describes [`410fbfb`](https://github.com/ziad-hsn/cpra/commit/410fbfb0092d01277b3884cd04151c27443a4226), which is separate from `main`. See [version and availability](../../versions.md).


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
| `-runtime-config` | empty | Separate storage/history/SLO YAML file. |
| `-data-dir` | empty | Override durable storage directory. |
| `-validate` | false | Validate manifests and compiled drivers without opening storage or providers. |
| `-capabilities` | false | Print compiled driver capabilities as JSON. |
| `-shutdown-timeout` | `45s` | Application drain budget; Windows service caps it at `15s`. |

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

## Local administration and probe commands

`cpra -capabilities` reports compiled driver availability. `cpra -validate -yaml
/path/monitors.yaml -runtime-config /path/runtime.yaml` validates configuration
before opening storage or providers. Explicit `-data-dir` overrides configured
storage and platform defaults; `-shutdown-timeout` bounds application shutdown.

~~~sh
cpractl --token-file /path/auth.token --request-timeout 2s ready
cpractl local paths --scope user
cpractl local init --scope user
cpractl local service render --scope user
cpractl local service install --scope user --binary /path/cpra
cpractl local service update --scope user --binary /path/new-cpra
cpractl local service uninstall --scope user
cpractl local backup --data-dir /path/state --output /path/new-backup
cpractl local restore --backup /path/new-backup --data-dir /path/new-state
~~~

`ready` and `health` make bounded API requests only; they never open Raft or
invoke providers. Local commands operate on the local machine and do not use a
remote API. Service operations preserve installer ownership and use explicit
scope. Backup requires a stopped owner; restore requires an absent destination.
See [native administration](../native-installation.md) for platform permissions,
configuration preservation and credential handling.
