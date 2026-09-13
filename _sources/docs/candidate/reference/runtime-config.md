---
title: Candidate · Candidate runtime configuration
description: Candidate · Exact storage, history and measured-SLO defaults, constraints and path precedence in the release candidate.
cpra_scope: release_candidate
---

> **Release candidate:** these settings are implemented at `410fbfb` and are unavailable in current main. See [versions and availability](../../versions.md).

# Runtime configuration

`cpra -runtime-config PATH` reads one YAML runtime document, separately from the
monitor manifest selected by `-yaml` or its `-config` alias. Unknown fields and
additional documents are rejected. Input is bounded to 1 MiB. Omitting the file
uses the defaults below; it does not select disposable memory storage.

## Storage and history

| Field | Default | Accepted value or behavior |
| --- | --- | --- |
| `storage.mode` | `raft` | `raft` or explicit `memory` |
| `storage.directory` | platform user state directory | Explicit `-data-dir` takes precedence; relative explicit paths resolve from the working directory |
| `storage.batch_delay` | `5ms` | Positive and no greater than `5ms` |
| `storage.batch_size` | `1000` | Integer from 1 to 1000 |
| `storage.snapshot_interval` | `5m` | At least one second |
| `storage.snapshot_retain` | `3` | At least one completed snapshot |
| `history.retention_days` | `30` | Must remain 30 for this storage format |

Run `cpractl local paths --scope user` to inspect platform defaults. Linux system
services pass `/var/lib/cpra` explicitly. An existing legacy `./cpra-data` with
neither explicit path makes startup refuse a new store. Select it deliberately
or migrate the complete stopped store. `examples/runtime.yaml` explicitly selects
`./cpra-data`; that example value is different from the automatic default.

Memory mode follows the same state-transition path without writing a store.
Storage initialization failure never silently switches to memory.

## Measured latency

| Field | Default | Constraint |
| --- | --- | --- |
| `slo.queue_target` | `250ms` | Positive duration |
| `slo.result_target` | `5s` | Greater than the queue target |
| `slo.window` | `5m` | Exactly five minutes |
| `slo.control_window` | `30s` | Exactly 30 seconds |
| `slo.evaluation_interval` | `5s` | Exactly five seconds |
| `slo.minimum_samples` | `1000` | At least 1000 |
| `slo.healthy_hold` | `60s` | At least 60 seconds |

These configure observation and feedback. They do not establish capacity or an
unconditional latency guarantee. [Measured latency](../slo.md) explains bucket
resolution, missed/overdue work, coverage gaps and the feedback conditions.

## Example and validation

```yaml
storage:
  mode: raft
  directory: /absolute/path/cpra-state
  batch_delay: 5ms
  batch_size: 1000
  snapshot_interval: 5m
  snapshot_retain: 3
history:
  retention_days: 30
slo:
  queue_target: 250ms
  result_target: 5s
  window: 5m
  control_window: 30s
  evaluation_interval: 5s
  minimum_samples: 1000
  healthy_hold: 60s
```

```sh
cpra -validate -yaml /absolute/path/monitors.yaml -runtime-config /absolute/path/runtime.yaml
```

Successful validation does not open storage or contact providers. Protect runtime
and manifest files using the [native installation](../native-installation.md)
procedure. [Persistence and backup](../durability.md) explains the complete store
and why a Raft snapshot alone is insufficient.

Source: [runtimeconfig/config.go at the reviewed commit](https://github.com/ziad-hsn/cpra/blob/410fbfb0092d01277b3884cd04151c27443a4226/internal/runtimeconfig/config.go).
