---
title: Your First Custom Monitor
parent: Tutorials
---

# Your First Custom Monitor

This tutorial walks you through creating a single, comprehensive monitor that utilizes all three of CPRA's processing pipelines: **Pulse**, **Intervention**, and **Code**.

## Monitor Structure

A CPRA monitor is defined in a YAML file and consists of three main sections:

1.  **`pulse`**: Defines the health check (e.g., HTTP GET, TCP ping).
2.  **`intervention`**: Defines the automated action to take on failure (e.g., run a script, restart a service).
3.  **`codes`**: Defines the alerting policy (e.g., send an alert after 3 failures).

## Step 1: Create the Monitor YAML

Create a new file named `my-monitor.yaml` and add the following content. This monitor checks an HTTP endpoint every 30 seconds. If it fails 3 times, it attempts an intervention, and if the intervention fails, it sends an alert.

```yaml
monitors:
  - name: "critical-api-health-check"
    enabled: true
    pulse:
      type: http
      interval: 30s
      timeout: 5s
      unhealthy_threshold: 3 # Fail after 3 consecutive failures
      config:
        method: GET
        url: http://my-critical-api.internal/health
        expected_status: 200
    intervention:
      action: script
      max_failures: 1 # Trigger intervention on the first failure after threshold
      config:
        path: /usr/local/bin/restart_api.sh
        args: ["--force"]
    codes:
      # The 'Red' code is typically for critical alerts
      Red:
        dispatch: failure
        notify: webhook
        config:
          url: https://pagerduty.com/api/v2/alerts
          payload:
            service: "critical-api"
            status: "down"
```

## Step 2: Understand the Pipeline Flow

When this monitor is loaded, it will follow this flow:

1.  **Pulse Pipeline:** Executes the `http` check every 30 seconds.
2.  **Failure Condition:** If the check fails, the `unhealthy_threshold` counter increments.
3.  **Intervention Trigger:** After 3 consecutive failures, the **Intervention Pipeline** is triggered. It executes the `/usr/local/bin/restart_api.sh` script.
4.  **Code Trigger:** If the Intervention fails, or if the Pulse check continues to fail after the Intervention, the **Code Pipeline** is triggered, dispatching the `Red` alert via the configured webhook.

## Step 3: Run with Your Monitor

To run CPRA with your new monitor, use the same command as the Quickstart, but point to your new file:

```bash
$ docker run -it --rm \
  -v $(pwd)/my-monitor.yaml:/app/monitors.yaml \
  cpra:latest \
  ./cpra --yaml /app/monitors.yaml
```

---

### **Next Steps**

*   **[Monitor Configuration Schema](reference/config-schema.md)**: Explore all available options for `pulse`, `intervention`, and `codes`.
*   **[Architecture Overview](explanation/architecture-overview.md)**: Understand the ECS core that powers this high-performance flow.
