---
title: Monitor Configuration Schema
parent: Reference
---

# Monitor Configuration Schema

CPRA monitors are defined in a YAML file. Each monitor specifies health checks, automated recovery actions, and alerting rules.

## Quick Reference

```yaml
monitors:
  - name: my-api                    # Required: Unique monitor name
    enabled: true                   # Optional: Enable/disable (default: true)
    
    pulse_check:                    # Required: Health check configuration
      type: http                    # http | tcp | icmp
      interval: 30s                 # How often to check
      timeout: 5s                   # Max wait time per check
      config:                       # Type-specific settings
        url: https://api.example.com/health
    
    intervention:                   # Optional: Auto-recovery
      action: docker
      target:
        type: restart               # restart | stop | start | kill | pause | unpause | scale
        container: my-api
    
    codes:                          # Optional: Alerting
      red:
        dispatch: true
        notify: slack
```

---

## Monitor

The top-level monitor object.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | Yes | — | Unique identifier for this monitor |
| `enabled` | bool | No | `true` | Set `false` to load but never schedule |
| `pulse_check` | object | Yes | — | Health check configuration |
| `intervention` | object | No | — | Auto-recovery when unhealthy |
| `codes` | map | No | — | Alert rules by severity color |

### Example: Minimal Monitor

```yaml
- name: my-api
  pulse_check:
    type: http
    interval: 30s
    timeout: 5s
    config:
      url: https://api.example.com/health
```

### Example: Full Monitor

```yaml
- name: production-api
  enabled: true
  
  pulse_check:
    type: http
    interval: 30s
    timeout: 5s
    unhealthy_threshold: 3        # 3 failures → unhealthy
    healthy_threshold: 2          # 2 successes → healthy again
    config:
      method: GET
      url: https://api.example.com/health
      retries: 2
  
  intervention:
    action: docker
    retries: 2
    target:
      type: restart
      container: api-container
      timeout: 30s
  
  codes:
    red:
      dispatch: true
      notify: pagerduty
      config:
        url: https://events.pagerduty.com/v2/enqueue
    yellow:
      dispatch: true
      notify: slack
      config:
        hook: https://hooks.slack.com/services/XXX
```

---

## pulse_check

Health check configuration. Runs at `interval`, fails after `timeout`.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `type` | string | Yes | — | Check type: `http`, `tcp`, or `icmp` |
| `interval` | duration | Yes | — | How often to run (e.g., `30s`, `5m`) |
| `timeout` | duration | Yes | — | Max time per check (e.g., `5s`) |
| `unhealthy_threshold` | int | No | `1` | Consecutive failures to become unhealthy |
| `healthy_threshold` | int | No | `1` | Consecutive successes to recover |
| `config` | object | Yes | — | Type-specific configuration |

### HTTP Check (`type: http`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | Yes | — | Full URL to check |
| `method` | string | No | `GET` | HTTP method |
| `headers` | list | No | — | Headers as `"Name: Value"` strings |
| `retries` | int | No | `0` | Retry attempts on failure |

```yaml
pulse_check:
  type: http
  interval: 30s
  timeout: 5s
  config:
    method: GET
    url: https://api.example.com/health
    headers:
      - "Authorization: Bearer token"
    retries: 2
```

### TCP Check (`type: tcp`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | Yes | — | Hostname or IP |
| `port` | int | Yes | — | Port number |
| `retries` | int | No | `0` | Retry attempts |

```yaml
pulse_check:
  type: tcp
  interval: 30s
  timeout: 5s
  config:
    host: database.internal
    port: 5432
    retries: 1
```

### ICMP Check (`type: icmp`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | Yes | — | Hostname or IP to ping |
| `count` | int | No | `1` | Number of ping packets |
| `retries` | int | No | `0` | Retry attempts |

```yaml
pulse_check:
  type: icmp
  interval: 60s
  timeout: 10s
  config:
    host: gateway.internal
    count: 3
```

---

## intervention

Automated recovery action triggered when `unhealthy_threshold` is reached.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `action` | string | Yes | — | Action type: `docker` |
| `retries` | int | No | `0` | Retry attempts per intervention |
| `max_failures` | int | No | `1` | Failures before triggering alerts |
| `target` | object | Yes | — | Action-specific configuration |

### Docker Actions (`action: docker`)

| `target.type` | Description | Required Fields |
|---------------|-------------|-----------------|
| `restart` | Stop + start container (default) | `container` |
| `stop` | Graceful shutdown (SIGTERM) | `container` |
| `start` | Start stopped container | `container` |
| `kill` | Force stop (SIGKILL) | `container` |
| `pause` | Freeze processes | `container` |
| `unpause` | Resume processes | `container` |
| `scale` | Change Swarm replicas | `service`, `replicas` |

#### Docker Target Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `type` | string | No | `restart` | Action type (see table above) |
| `container` | string | Conditional | — | Container name/ID (for container ops) |
| `service` | string | Conditional | — | Swarm service name (for `scale`) |
| `replicas` | int | Conditional | — | Target replica count (for `scale`) |
| `signal` | string | No | `SIGKILL` | Signal for `kill` action |
| `docker_host` | string | No | env | Docker daemon address |
| `timeout` | duration | No | — | Operation timeout |

#### Example: Restart (default)

```yaml
intervention:
  action: docker
  retries: 2
  target:
    type: restart               # Can omit, restart is default
    container: my-api
    timeout: 30s
```

#### Example: Graceful Stop

```yaml
intervention:
  action: docker
  target:
    type: stop
    container: my-api
    timeout: 10s                # Time before SIGKILL
```

#### Example: Force Kill

```yaml
intervention:
  action: docker
  target:
    type: kill
    container: my-api
    signal: SIGTERM             # Optional, default: SIGKILL
```

#### Example: Pause/Unpause

```yaml
# Pause
intervention:
  action: docker
  target:
    type: pause
    container: my-api

# Unpause
intervention:
  action: docker
  target:
    type: unpause
    container: my-api
```

#### Example: Scale Swarm Service

```yaml
intervention:
  action: docker
  target:
    type: scale
    service: web-frontend       # Swarm service name
    replicas: 5                 # Target count
    timeout: 60s
```

---

## codes

Alert rules mapped by severity "color". Triggered after `max_failures` interventions fail.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `dispatch` | bool | Yes | — | Enable this alert |
| `notify` | string | Yes | — | Channel: `log`, `slack`, `pagerduty`, `email`, `webhook` |
| `config` | object | Yes | — | Channel-specific configuration |

### Log (`notify: log`)

```yaml
codes:
  red:
    dispatch: true
    notify: log
    config:
      file: /var/log/cpra-alerts.log
```

### Slack (`notify: slack`)

```yaml
codes:
  yellow:
    dispatch: true
    notify: slack
    config:
      hook: https://hooks.slack.com/services/T00/B00/XXX
```

### PagerDuty (`notify: pagerduty`)

```yaml
codes:
  red:
    dispatch: true
    notify: pagerduty
    config:
      url: https://events.pagerduty.com/v2/enqueue
```

---

## Duration Format

Durations use Go syntax:

| Example | Meaning |
|---------|---------|
| `5s` | 5 seconds |
| `30s` | 30 seconds |
| `5m` | 5 minutes |
| `1h` | 1 hour |
| `1m30s` | 1 minute 30 seconds |

---

## Environment Variables

Runtime configuration via environment:

| Variable | Description | Default |
|----------|-------------|---------|
| `CPRA_ENV` | Environment mode (`production` reduces logging) | — |
| `CPRA_SIZING_TAU_MS` | Expected job execution time (ms) | Auto |
| `CPRA_SIZING_SLO_MS` | Target latency SLO (ms) | Auto |
| `CPRA_SIZING_HEADROOM_PCT` | Worker pool safety margin | `15` |
| `GOGC` | GC frequency (higher = less frequent) | `100` |
| `GOMEMLIMIT` | Soft memory limit (Go 1.19+) | — |

### Production Setup

```bash
source scripts/production-env.sh
./cpra -yaml monitors.yaml
```

---

## Complete Example

```yaml
monitors:
  # HTTP API with full recovery pipeline
  - name: production-api
    pulse_check:
      type: http
      interval: 30s
      timeout: 5s
      unhealthy_threshold: 3
      healthy_threshold: 2
      config:
        method: GET
        url: https://api.example.com/health
        retries: 2
    
    intervention:
      action: docker
      retries: 2
      max_failures: 3
      target:
        type: restart
        container: api-container
        timeout: 30s
    
    codes:
      red:
        dispatch: true
        notify: pagerduty
        config:
          url: https://events.pagerduty.com/v2/enqueue
      yellow:
        dispatch: true
        notify: slack
        config:
          hook: https://hooks.slack.com/services/XXX

  # Database TCP check
  - name: postgres-primary
    pulse_check:
      type: tcp
      interval: 15s
      timeout: 3s
      config:
        host: db.internal
        port: 5432
    codes:
      red:
        dispatch: true
        notify: log
        config:
          file: /var/log/cpra-db-alerts.log

  # Network gateway ping
  - name: gateway-ping
    pulse_check:
      type: icmp
      interval: 60s
      timeout: 10s
      unhealthy_threshold: 5
      config:
        host: 10.0.0.1
        count: 3
