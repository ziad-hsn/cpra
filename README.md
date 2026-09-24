<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/dist/svg/cpra-horizontal-dark.svg">
    <img src="brand/dist/svg/cpra-horizontal-color.svg" alt="CPRa" width="360">
  </picture>
</p>

<p align="center"><strong>Continuous Pulse and Recovery Agent</strong><br>
Checks services, sends alerts, and runs the recovery actions you configure.</p>

<p align="center">
  <a href="https://github.com/ziad-hsn/cpra/actions/workflows/ci.yml"><img src="https://github.com/ziad-hsn/cpra/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-1a262e" alt="MIT license"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/go-1.25%2B-1a262e" alt="Go 1.25+"></a>
  <a href="https://ziad-hsn.github.io/cpra/"><img src="https://img.shields.io/badge/docs-ziad--hsn.github.io%2Fcpra-e5a51f" alt="Documentation"></a>
</p>

CPRa is a self-hosted monitoring and recovery agent written in Go. It runs
health checks against your services on a schedule, opens and closes incidents
against configurable thresholds, sends notifications, and executes a recovery
action — restart a container, call a webhook, restart or scale a Kubernetes
workload, reboot an EC2 instance, restart a systemd unit — when a service
fails. It ships as a single server binary with an embedded read-only dashboard,
an HTTP API, and the `cpractl` command-line client. It is MIT-licensed.

**Documentation:** [ziad-hsn.github.io/cpra](https://ziad-hsn.github.io/cpra/) —
[quickstart](https://ziad-hsn.github.io/cpra/tutorials/quickstart/) ·
[monitor configuration](https://ziad-hsn.github.io/cpra/reference/config-schema/) ·
[drivers](https://ziad-hsn.github.io/cpra/reference/jobs-reference/) ·
[HTTP API](https://ziad-hsn.github.io/cpra/reference/api-reference/) ·
[deployment](https://ziad-hsn.github.io/cpra/how-to/deploy-to-production/) ·
[FAQ](https://ziad-hsn.github.io/cpra/faq/)

## Quick start

Requires Go 1.25 or later, Make, and Python 3 for the source workspace below.
The repository already contains the built dashboard assets.

Make creates an ignored `bin/cpra-sdk.work` for the application and its local
SDK modules, so the unpublished SDK candidate can be built from this checkout.
The integrations example module remains opt-in. An explicit `GOWORK` path or
`GOWORK=off` takes precedence; Make never changes the selected external workspace.

```sh
make
cp examples/monitors.yaml monitors.yaml
# Set the service address in monitors.yaml.
./bin/cpra -yaml monitors.yaml
```

For direct Go commands, select the workspace explicitly after `make dev-workspace`:

```sh
GOWORK="$PWD/bin/cpra-sdk.work" go test ./internal/cpractl/cli
```

Official release builds retain `GOWORK=off` and require separately qualified
module dependencies. Local workspace builds do not establish public module
availability or release readiness.

State is durable by default in the platform's user state directory (`cpractl local paths`); Linux system services explicitly use `/var/lib/cpra`. Keep that directory across restarts. An explicit `-data-dir` overrides runtime configuration and the platform default. A legacy `./cpra-data` requires an explicit path or a stopped migration. Use `-runtime-config examples/runtime-memory.yaml` for a disposable run. [Persistence and recovery](docs/durability.md) describes identities, unknown outcomes and complete backups.

Open `http://localhost:8060` using the configured API credentials. The
[management setup](docs/management-startup.md) enables current SDK commands such
as `./bin/cpractl get monitors`; those commands use stable resource IDs. Missing or malformed configuration stops startup. Empty configurations require `-allow-empty`.

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
- `/api/v2/healthz` reports liveness. `/api/v2/readyz` requires initialized admission, controller progress and available storage; explicitly empty configurations can be ready. Dashboard projection freshness is reported separately. Provider outages do not make the process dead.
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

Rebuilding the dashboard requires Node.js 24.21.0, pnpm 11.22.0, and Python 3:

```sh
make dashboard-build dashboard-check
make release VERSION=v0.1.0
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

The [Go SDK](sdk/go/README.md) and optional
[external worker library](sdk/go/worker/README.md) are independent modules.
`cpractl` uses the public SDK for management and observation requests, with stable
resource IDs and cursor pagination. The worker protocol still requires its
separate server qualification; neither module has been published by this work.
The [four SDK integration lessons](examples/sdk/README.md) demonstrate queue
registration, AWS deregistration, Kubernetes Service discovery, and a DAO/SMS
worker. The [SDK guide and full API reference](docs/sdk/index.md) describe their
methods, fields, and verification boundaries.

[SDK status](docs/implementation/go-sdk-status.md) records the remaining gates,
and [SDK verification](docs/implementation/go-sdk-verification.md) distinguishes
executed tests from pending server and release evidence. Root `go test ./...`
does not include the nested modules; use `make sdk-check` with the source
workspace above. Official releases keep `GOWORK=off` and require the SDK versions
to be published first.

The durable implementation is a release candidate. Full-provider verification and the one-million-monitor 24-hour endurance gate require completed evidence before a full release-readiness claim. User-configured live verification covers 33 driver types and never passes a missing configuration. [Validation instructions](docs/validation.md) distinguish local operations, provider accounts, comparisons and endurance.

[Provider test environments](docs/provider-testing.md) cover local services,
notification contract mocks, the Moto EC2 emulator and optional provider sandboxes.
Reports distinguish these results from real-account delivery and recovery.

## License

CPRa is licensed under [MIT](LICENSE). [Dashboard dependency notices](LICENSES/dashboard.txt) accompany the embedded assets. Binary archives include notices for the dependencies in the selected build.

The CPRa name and mark are not covered by the MIT licence. Brand assets, usage rules, and their (pending) licence live in [`brand/`](brand/BRANDING.md).
