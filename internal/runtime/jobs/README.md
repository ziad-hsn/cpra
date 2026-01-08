# Jobs Package

This package provides job types and execution logic for CPRA monitor operations.

## File Organization

| Prefix | Purpose | Examples |
|--------|---------|----------|
| `base`, `types`, `factory` | Core infrastructure | Job interface, Result, errors |
| `pulse_*` | Health check jobs | HTTP, TCP, ICMP |
| `intervention_*` | Recovery action jobs | Docker restart/stop/start/kill/pause/unpause/scale |
| `code_*` | Alert notification jobs | Log, Slack, PagerDuty |
| `pool_*` | Connection pools | HTTP clients, TCP dialers |
| `pool` | Job struct pools | sync.Pool definitions |
| `dial_limiter` | Rate/concurrency limiting | Prevents CPU spikes |
| `TEMPLATE` | New job template | Copy for new jobs |

## Quick Reference

```
internal/jobs/
├── README.md           # This file
├── TEMPLATE.go         # Template for new jobs
│
├── base.go             # BaseJob, BaseNetworkJob
├── types.go            # Job interface, Result, errors
├── factory.go          # CreatePulseJob, CreateInterventionJob, CreateCodeJob
├── dial_limiter.go     # Global rate/concurrency limiting
│
├── pulse_http.go       # HTTP health checks (fasthttp)
├── pulse_tcp.go        # TCP connection checks
├── pulse_icmp.go       # ICMP ping checks
│
├── intervention_docker.go  # Docker: restart, stop, start, kill, pause, unpause, scale
│
├── code_log.go         # JSON log file alerts
├── code_slack.go       # Slack notifications
├── code_pagerduty.go   # PagerDuty alerts
├── code_email.go       # Email notifications
├── code_webhook.go     # Webhook notifications
│
├── pool.go             # sync.Pool for job structs
├── pool_http.go        # fasthttp client pool
├── pool_tcp.go         # TCP dialer with SO_REUSEADDR
├── pool_docker.go      # Docker client pool
│
├── log_writer.go       # Async log writer
└── doc.go              # Package documentation
```

## Adding New Jobs

1. Copy `TEMPLATE.go` to a new file (e.g., `pulse_dns.go`)
2. Follow the safety checklist in the template
3. Add sync.Pool functions to `pool.go`
4. Add factory case to `factory.go`
5. Add predeclared errors to `types.go`

See `docs/how-to/adding-new-jobs.md` for the complete guide.

## Safety Patterns

All network jobs must:

- **Dial Limiter**: Call `GetDialLimiter().Acquire(ctx)` before network I/O
- **Context Checks**: Check `ctx.Done()` before each retry
- **Predeclared Errors**: Use errors from `types.go` to avoid allocations
- **Object Pooling**: Use `sync.Pool` for job structs (see `pool.go`)
