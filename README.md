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

State is durable by default in the platform's user state directory (`cpractl local paths`); Linux system services explicitly use `/var/lib/cpra`. Keep that directory across restarts. An explicit `-data-dir` overrides runtime configuration and the platform default. A legacy `./cpra-data` requires an explicit path or a stopped migration. Use `-runtime-config examples/runtime-memory.yaml` for a disposable run. [Persistence and recovery](docs/durability.md) describes identities, unknown outcomes and complete backups.

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
- `/api/v1/healthz` reports liveness. `/api/v1/readyz` requires initialized admission, controller progress and available storage; explicitly empty configurations can be ready. Dashboard projection freshness is reported separately. Provider outages do not make the process dead.
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

The [recorded release recipe](docs/release-engineering.md) uses a checksum-pinned
Go 1.27.1 compiler and explicit source identity. It produces unstripped,
optimized server/client archives for Linux, macOS and Windows on amd64/arm64,
Linux packages, a source archive and evidence inventories. Go 1.25 compatibility
is a separate test. Native execution gates qualify each platform; a successful
cross-build alone does not establish support.

Versioned installation needs only Go and the committed embedded assets:

```sh
go install github.com/ziad-hsn/cpra@VERSION
go install github.com/ziad-hsn/cpra/cmd/cpractl@VERSION
cpractl local init
cpra -capabilities
```

Replace `VERSION` with an actual published version. [Native installation](docs/native-installation.md)
documents full-driver builds, standard paths, native supervisors, local ownership
and stopped backup/restore. Initial configuration is deliberately empty.

The production image consumes the same staged release executables; source builds
use `docker/Dockerfile.dev`. Production Compose uses a stable named volume,
read-only configuration, UID 1001, bounded logs and a loopback-published API.
The Helm chart uses one StatefulSet owner and retained storage, supports large
file-backed manifests, and distinguishes Helm 3 and 4 operations. Follow the
[container and Helm guide](docs/container-helm.md) to prepare authentication and
configuration before starting either route.

## Release evidence

The durable implementation is a release candidate. Full-provider verification and the one-million-monitor 24-hour endurance gate require completed evidence before a full release-readiness claim. User-configured live verification covers 33 driver types and never passes a missing configuration. [Validation instructions](docs/validation.md) distinguish local operations, provider accounts, comparisons and endurance.

[Provider test environments](docs/provider-testing.md) cover local services,
notification contract mocks, the Moto EC2 emulator and optional provider sandboxes.
Reports distinguish these results from real-account delivery and recovery.

## License

CPRa is licensed under [MIT](LICENSE). [Dashboard dependency notices](LICENSES/dashboard.txt) accompany the embedded assets. Binary archives include notices for the dependencies in the selected build.
