---
title: Deploy To Production
---

# Deploy To Production

This guide covers production deployment options for CPRA: running a compiled binary, Docker containers, and basic Kubernetes notes. It assumes Go 1.25+ and a monitors YAML file.

## Prerequisites

- Go 1.25+
- A monitors YAML file (for example `mock-servers/test_10k.yaml`)
- Optional: Docker (for containerized deployment)

## Option A: Deploy the Compiled Binary

1. Build a static binary:
   ```bash
   cd cpra
   go build -trimpath -ldflags "-s -w" -o cpra .
   ```

2. Copy the binary and your YAML file to the target host, then run:
   ```bash
   ./cpra --yaml /path/to/monitors.yaml
   ```

3. (Optional) Enable/disable profiling flags at runtime:
   ```bash
   ./cpra --yaml /path/to/monitors.yaml --pprof=true --pprof.addr localhost:6060
   ```

4. (Optional) systemd unit example:
   ```ini
   [Unit]
   Description=CPRA Monitoring
   After=network.target

   [Service]
   ExecStart=/opt/cpra/cpra --yaml /opt/cpra/monitors.yaml --pprof=false
   Restart=always
   User=cpra
   Group=cpra
   LimitNOFILE=1048576

   [Install]
   WantedBy=multi-user.target
   ```

## Option B: Deploy with Docker

1. Build the image:
   ```bash
   docker build -f docker/Dockerfile -t cpra .
   ```

   Note: If the build fails due to a missing `samples/` directory referenced in the Dockerfile, either create `cpra/samples` with your YAML files, or remove the `COPY samples samples` line from `docker/Dockerfile` before building.

2. Run with a mounted YAML file:
   ```bash
   docker run -d --name cpra \
     -v $(pwd)/mock-servers/test_10k.yaml:/app/monitors.yaml:ro \
     --restart unless-stopped \
     cpra \
     ./cpra --yaml /app/monitors.yaml
   ```

3. Control logging and memory via env if needed:
   ```bash
   docker run -d --name cpra \
     -e GOMEMLIMIT=1073741824 \
     -e GOGC=100 \
     -v $(pwd)/mock-servers/test_10k.yaml:/app/monitors.yaml:ro \
     cpra \
     ./cpra --yaml /app/monitors.yaml --debug
   ```

## Option C: Kubernetes (basic outline)

Use a Deployment with a ConfigMap or Secret for your YAML, and a small PVC if you want to persist logs.

Example Deployment snippet:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cpra
spec:
  replicas: 1
  selector:
    matchLabels: { app: cpra }
  template:
    metadata:
      labels: { app: cpra }
    spec:
      containers:
        - name: cpra
          image: cpra:latest
          args: ["./cpra", "--yaml", "/config/monitors.yaml", "--pprof=false"]
          volumeMounts:
            - name: config
              mountPath: /config
              readOnly: true
      volumes:
        - name: config
          configMap:
            name: cpra-monitors
```

## Troubleshooting

- High memory usage: reduce worker max or queue capacity in code/config; consider `GOMEMLIMIT` and lower `GOGC`.
- No monitors loaded: confirm YAML path and file permissions; run with `--debug`.
- Slow processing: adjust `SizingServiceTime`, `SizingSLO`, and worker min/max in config.

## Related Guides

- [Common Tasks](./common-tasks.md)
- [API Reference](../reference/api-reference.md)
