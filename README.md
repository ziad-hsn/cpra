# CPRa

CPRa (Continuous Pulse and Recovery Agent) checks services, sends alerts, and runs configured recovery actions. It includes a read-only dashboard, an HTTP API, and the `cpractl` command-line client.

## Build and run

Build with Go 1.25 or later and Make. The source includes the dashboard assets.

```sh
make
cp examples/monitors.yaml monitors.yaml
# Set the service address in monitors.yaml.
./bin/cpra -yaml monitors.yaml
```

State is durable by default in `./cpra-data`. Keep that directory across restarts. Use `-runtime-config examples/runtime.yaml` to set its location, or `-runtime-config examples/runtime-memory.yaml` for an explicitly disposable run. [Persistence and recovery](docs/durability.md) describes identities, unknown outcomes and complete backups.

Open `http://localhost:8060` or run `./bin/cpractl get monitors`. Missing or malformed configuration stops startup. Empty configurations require `-allow-empty`.

The example checks an HTTP endpoint and writes incident transitions to `alerts.jsonl`. Each monitor can specify a check interval, timeout, failure threshold, recovery threshold, notification destinations, and a recovery action. Maintenance windows suppress alerts and recovery while checks continue; they use five-field cron expressions, a duration, and an IANA timezone.

## Drivers

| Function | Default build | Optional build tags |
| --- | --- | --- |
| Checks | HTTP, TCP, ICMP, DNS, UDP, TLS, Docker, gRPC port reachability | `redis postgres mysql mongo rabbitmq kafka` |
| Recovery | Docker, HTTP webhook | `kubernetes aws systemd` |
| Alerts | Log, Slack, PagerDuty, email, webhook, Telegram, Discord, Opsgenie, Mattermost, VictorOps, Pushover, Datadog | `teams twilio` |

```sh
make BUILD_TAGS='redis postgres kubernetes'
```

The `grpc` check tests the TCP port; it does not call the gRPC health service. UDP checks require a payload and a reply. PagerDuty requires an Events API v2 routing key. Email uses an SMTP relay with STARTTLS; SMTP username/password authentication is not implemented.

TLS `warn_days` produces a yellow alert and a degraded monitor status without starting recovery; `critical_days` fails the check and follows the normal recovery policy. Pushover emergency priority accepts `retry` and `expire` in seconds, defaulting to 60 and 1800. Docker recovery preserves the daemon's stop grace when its timeout is omitted.

MongoDB checks require a direct `mongodb://` URI. The selected driver cannot bound initial `mongodb+srv://` discovery by the check deadline, so CPRa rejects that mode. Kubernetes recovery supports token, certificate, and in-cluster credentials; kubeconfig exec credential plugins are rejected because they can outlive the recovery deadline.

## Access

The server listens on loopback by default. Other bind addresses require an authentication token. Keep the token in a file readable by the service account:

```sh
./bin/cpra -yaml monitors.yaml -web.addr 0.0.0.0:8060 -web.auth-file /run/secrets/cpra-token
./bin/cpractl --server https://monitor.example.com --token-file /run/secrets/cpra-token get monitors
```

Browser login uses username `cpra` and the token as the password. The API accepts a Bearer token. The server and CLI also read `CPRA_AUTH_TOKEN` and `CPRA_AUTH_TOKEN_FILE`. Remote access needs an HTTPS reverse proxy; the built-in listener serves HTTP.

Manifests authorize checks, notification destinations, and recovery actions with the process account's permissions. Use trusted configuration. `-ssrf-protect` blocks non-public HTTP destinations at connection time; it does not restrict other protocols or local actions. Profiling is opt-in. Keep token files and manifests containing credentials outside version control.

## Behavior and limits

- Single-node Raft commits incident state and action intent before dispatch. Started external actions with interrupted results are held as unknown after restart; health checks resume. This is one-node persistence, without distributed failover.
- Each incident admits one recovery operation. Successful recovery must be followed by the configured consecutive successful checks. Inspect provider records for unknown actions; restarting does not automatically repeat them.
- Notification endpoint outcomes are separate. A successful endpoint is not repeated because another endpoint failed. Confirmed retryable rejections allow at most three attempts per endpoint. Transport acceptance does not confirm delivery to a person.
- `/api/v1/healthz` reports liveness. `/api/v1/readyz` requires available storage and a recent projection containing monitors. It does not require all targets to be healthy.
- Fleet views update incrementally, retain numeric routes and add stable `monitor_id` values. Pages are bounded. `/api/v1/history` retains incident and action events for 30 days; `/api/v1/state` exposes persistence and unknown actions; `/api/v1/slo` exposes measured latency distributions. All remain read-only and use the existing authentication.
- Raw health-check history is not retained. Current counters and the aggregate SLO window survive restart; gaps in measurement coverage are explicit. See [measured SLO definitions](docs/slo.md).
- JSON is decoded incrementally. YAML requires a block sequence for monitors and limits each entry and the metadata to 1 MiB. Both formats have a decompressed-input budget. Memory use grows with monitor count; capacity depends on check intervals, targets, and host resources.
- Worker scaling uses Erlang C with an Allen-Cunneen variability adjustment and 15% default headroom. Arrival intervals and execution times use the latest 256 observations. The model estimates mean latency. Observed percentile feedback uses a 30-second control window, bounded growth and a healthy hold before reducing capacity. The five-minute health-check targets are p99 queue delay ≤250 ms and scheduled-to-committed-result ≤5 seconds. Worker limits remain; these are measured targets rather than an SLA guarantee.
- Configuration is loaded before startup. Live reload and runtime queue migration are not supported.

## Check and package

```sh
make check
make test-all-drivers
```

Rebuilding the dashboard requires Node.js 24, pnpm 11.22.0, and Python 3:

```sh
make dashboard-build dashboard-check
make release VERSION=0.1.0
```

`make release` creates Linux amd64 and arm64 server/CLI archives, a source archive, dependency notices, and SHA-256 checksums in `dist/release`. Set `VERSION` to the version being released. The command does not publish artifacts.

Build a container with `docker build -f docker/Dockerfile -t cpra:local .`. It runs as UID 1001 and expects a manifest at `/etc/cpra/monitors.yaml`. Mount the manifest read-only, retain `/var/lib/cpra` on a persistent volume owned by UID 1001, and give the process write access to configured log destinations. `docker compose -f docker/docker-compose.yml up --build` runs the example with its HTTP listener disabled; service addresses must be reachable from inside the container.

## Release evidence

The durable implementation is a release candidate. Full-provider verification and the one-million-monitor 24-hour endurance gate require completed evidence before a full release-readiness claim. User-configured live verification covers 33 driver types and never passes a missing configuration. [Validation instructions](docs/validation.md) distinguish local operations, provider accounts, comparisons and endurance.

## License

CPRa is licensed under [MIT](LICENSE). [Dashboard dependency notices](LICENSES/dashboard.txt) accompany the embedded assets. Binary archives include notices for the dependencies in the selected build.
