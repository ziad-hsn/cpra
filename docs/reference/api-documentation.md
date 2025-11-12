---
title: CLI & Configuration Reference
---

# CLI & Configuration Reference

This guide aggregates the knobs you use to run CPRA in production: command-line flags, YAML schema basics, environment variables, and container tips. Use it together with the [API Reference](api-reference.md) when wiring CPRA into your own tooling.

## Command-Line Interface

The CPRA binary exposes a concise flag set. All flags are optional; sensible defaults exist so you can start quickly and override as needed.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--yaml` | string | `internal/loader/replicated_test.yaml` | Path to the monitors YAML file. Override with your own config (for example `mock-servers/test_10k.yaml`). |
| `--config` | string | _empty_ | Optional application config file if you externalize controller settings. |
| `--debug` | bool | `false` | Enables verbose logging and queue sizing diagnostics. |
| `--pprof` | bool | `true` | Toggles the embedded profiling server. Disable in locked-down environments. |
| `--pprof.addr` | string | `localhost:6060` | Listen address for the profiling server. |

!!! tip "Profiling in production"
    Keep `--pprof` enabled in staging so you can capture heap/CPU traces with `go tool pprof http://host:6060/debug/pprof/heap`. Disable (or firewall) the endpoint before exposing the binary to the internet.

## YAML Monitor Schema (Quick Primer)

Monitors live in a single YAML document under the `monitors` key. This lightweight schema mirrors the ECS component model. A minimal example:

```yaml
monitors:
  - name: "edge-api-health"
    pulse_check:
      type: http
      interval: 30s
      timeout: 5s
      max_failures: 3
      config:
        method: GET
        url: https://edge.example.com/health
    intervention:
      type: script
      config:
        script: /opt/cpra/scripts/restart-edge.sh
    codes:
      red:
        dispatch: true
        notify: pagerduty
        config:
          url: https://events.pagerduty.com/v2/enqueue
```

Key ideas:

1. **Pulse** defines how to collect health (HTTP/TCP/ICMP/custom command).
2. **Intervention** specifies the automated remediation handler.
3. **Codes** let you map failure tiers to alert transports.

For larger samples open `mock-servers/test_10k.yaml` or generate new datasets with `mock-servers/generate_monitors.py`.

## Environment Variables

Environment variables complement CLI flags when you containerize CPRA.

| Variable | Purpose | Typical Value |
|----------|---------|---------------|
| `GOMEMLIMIT` | Hard cap for Go’s soft memory limit. Helps prevent OOM kills in containers. | `1073741824` (1 GiB) |
| `GOGC` | Target heap growth percentage for GC. Lower values trigger more frequent collections. | `100` (default), `50` for tighter control |
| `CPRA_DEBUG` | Enables debug logging without changing CLI flags in Docker Compose/systemd. | `true`/`false` |
| `YAML_FILE` | Convenience variable used in `docker-compose` examples to point CPRA at a specific monitors file. | `samples/replicated_test_10k.yaml` |

!!! warning "Memory tuning"
    When you shrink `GOMEMLIMIT`, also review `config.WorkerConfig.MaxWorkers` at runtime. Too many workers with a small memory budget can still crash the process even if Go tries to respect the limit.

## Docker & Container Tips

1. **Build** using the maintained multi-stage Dockerfile:
   ```bash
   docker build -f docker/Dockerfile -t cpra:latest .
   ```
2. **Mount monitor configs** read-only so container restarts keep the same dataset:
   ```bash
   docker run -d --name cpra \
     -v $(pwd)/mock-servers/test_10k.yaml:/app/monitors.yaml:ro \
     cpra:latest \
     ./cpra --yaml /app/monitors.yaml
   ```
3. **Propagate env overrides** with `-e GOMEMLIMIT=... -e CPRA_DEBUG=true`.
4. **Expose profiling only on localhost** or behind a reverse proxy when `--pprof` stays enabled.

## Deployment Checklist

- ✅ Verify `./cpra --yaml <file> --debug` locally before promoting configs.
- ✅ Keep a copy of the exact YAML shipped to production (git or artifact storage).
- ✅ Monitor queue metrics via `controller.PrintShutdownMetrics()` during canary runs.
- ✅ Use the [Deploy to Production guide](../how-to/deploy-to-production.md) for binary/Docker/Kubernetes recipes.

With these references you can wire CPRA into automation pipelines without hunting through the top-level README.
