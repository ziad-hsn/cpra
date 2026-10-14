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
  <a href="go.mod"><img src="https://img.shields.io/badge/go-1.26%2B-1a262e" alt="Go 1.26+"></a>
  <a href="https://ziad-hsn.github.io/cpra/"><img src="https://img.shields.io/badge/docs-ziad--hsn.github.io%2Fcpra-8A5A00" alt="Documentation"></a>
</p>

CPRa is a self-hosted monitoring and recovery agent written in Go. It runs
health checks against your services on a schedule, opens and closes incidents
against configurable thresholds, sends notifications, and executes a recovery
action — restart a container, call a webhook, restart or scale a Kubernetes
workload, reboot an EC2 instance, restart a systemd unit — when a service
fails. It ships as a single server binary with an embedded dashboard, an HTTP
API, and the `cpractl` command-line client. Named management access enables
monitor, destination, group and secret editing; observation mode remains
read-only. Application code is MIT-licensed; [CPRa artwork](brand/ATTRIBUTION.md)
uses CC BY 4.0.

**Documentation:** [ziad-hsn.github.io/cpra](https://ziad-hsn.github.io/cpra/) —
[quickstart](https://ziad-hsn.github.io/cpra/tutorials/quickstart/) ·
[monitor configuration](https://ziad-hsn.github.io/cpra/reference/config-schema/) ·
[drivers](https://ziad-hsn.github.io/cpra/reference/jobs-reference/) ·
[HTTP API](https://ziad-hsn.github.io/cpra/reference/api-reference/) ·
[deployment](https://ziad-hsn.github.io/cpra/how-to/deploy-to-production/) ·
[FAQ](https://ziad-hsn.github.io/cpra/faq/)

## Documentation and current development

The [documentation source](docs/index.md) includes current development references
and explicitly dated guides for earlier revisions. The
[published site](https://ziad-hsn.github.io/cpra/) is updated separately.
[Versions and availability](docs/versions.md) identifies those boundaries:

- This source includes Raft persistence, history/SLOs, encrypted management resources,
  dashboard forms, and SDK/CLI collection workflows.
- The [implementation progress](docs/implementation/dashboard-implementation-progress.md)
  records completed checks and remaining external-worker integration and release gates.
- The [Go SDK](docs/sdk/index.md) documents the current source contracts; public
  module-version and downloaded-consumer qualification remain pending.
- [Earlier candidate guides](docs/candidate/index.md) describe the pinned `410fbfb`
  revision and are not a release qualification for this development branch.

The [shipping plan](docs/implementation/dashboard-shipping-plan.md) governs
publication, together with the [approved review repairs](docs/implementation/release-repair-plan.md).
Source availability does not establish provider-account, native-platform or
comparative-performance qualification. The 24-hour run is cancelled.

## Quick start

Requires Go 1.26 or later, Make, and Python 3 for the source workspace below.
The repository already contains the built dashboard assets.

Make creates an ignored `bin/cpra-sdk.work` for the application and its local
SDK modules, so the unpublished SDK candidate can be built from this checkout.
The integrations example module remains opt-in. An explicit `GOWORK` path or
`GOWORK=off` takes precedence; Make never changes the selected external workspace.

```sh
make
export PATH="$PWD/bin:$PATH"
cpractl local init --management
# Use the exact start and client commands printed by initialization.
```

Initialization creates an intentionally empty manifest, a named operator token,
its hash policy, a wrapping key, and localhost TLS material in the platform's
configuration directory. It prints paths and commands, never the bearer value.
It refuses existing configuration or state and starts no service. System scope
requires an explicit protected `--token-output` outside service configuration and
state. [Management startup](docs/management-startup.md) explains the files and
stopped rotation.

Start CPRa using the printed command, then manually trust the generated local CA
in your browser and open `https://localhost:8060`. Read the protected operator
token file locally for browser sign-in. The initializer does not install trust
roots. The CLI uses `--ca-file` and `--token-file`; credentials belong in neither
URLs nor command arguments. Add monitors through the dashboard or explicit
`cpractl apply -f FILE` after startup. Management imports bootstrap manifests once;
subsequent file edits need an explicit apply.

For direct Go commands, select the workspace explicitly after `make dev-workspace`:

```sh
GOWORK="$PWD/bin/cpra-sdk.work" go test ./internal/cpractl/cli
```

Official release builds retain `GOWORK=off` and require separately qualified
module dependencies. Local workspace builds do not establish public module
availability or release readiness.

State is durable by default in the platform's user state directory (`cpractl local paths`); Linux system services explicitly use `/var/lib/cpra`. Keep that directory across restarts. An explicit `-data-dir` overrides runtime configuration and the platform default. A legacy `./cpra-data` requires an explicit path or a stopped migration. Use `-runtime-config examples/runtime-memory.yaml` for a disposable run. [Persistence and recovery](docs/durability.md) describes identities, unknown outcomes and complete backups.

The [management setup](docs/management-startup.md) enables current SDK commands
such as `cpractl get monitors`; those commands use stable resource IDs. Missing
or malformed configuration stops startup. Empty configurations require
`-allow-empty`, included in the initialized start command.

The [sample manifest](examples/monitors.yaml) checks an HTTP endpoint and writes incident transitions to `alerts.jsonl`; replace its target before applying it. The initialized manifest is empty. Each monitor can specify a check interval, timeout, failure threshold, recovery threshold, notification destinations, and a recovery action. Maintenance windows suppress alerts and recovery while checks continue; they use five-field cron expressions, a duration, and an IANA timezone.

## Drivers

| Function | Default build | Optional build tags |
| --- | --- | --- |
| Checks | HTTP, TCP, ICMP, DNS, UDP, TLS, Docker, gRPC | `redis postgres mysql mongo rabbitmq kafka` |
| Recovery | Docker, HTTP webhook, A2A agent task | `kubernetes aws systemd` |
| Alerts | Log, Slack, PagerDuty, email, webhook, Telegram, Discord, Opsgenie, Mattermost, VictorOps, Pushover, Datadog, A2A | `teams twilio` |

```sh
make BUILD_TAGS='redis postgres kubernetes'
```

The `grpc` check preserves TCP reachability when `mode` is omitted. Explicit
`mode: health` calls the unary gRPC health service and requires `SERVING`; it
uses verified TLS unless cleartext is explicitly selected. UDP checks require
a payload and a reply. PagerDuty requires an Events API v2 routing key. Email
uses an SMTP relay with STARTTLS; SMTP username/password authentication is not
implemented. See [driver configuration](docs/reference/jobs-reference.md) and
[notification response contracts](docs/implementation/notification-response-contracts.md).

TLS `warn_days` produces a yellow alert and a degraded monitor status without starting recovery; `critical_days` fails the check and follows the normal recovery policy. Pushover emergency priority accepts `retry` and `expire` in seconds, defaulting to 60 and 1800. Docker recovery preserves the daemon's stop grace when its timeout is omitted.

MongoDB checks require a direct `mongodb://` URI. The selected driver cannot bound initial `mongodb+srv://` discovery by the check deadline, so CPRa rejects that mode. Kubernetes recovery supports token, certificate, and in-cluster credentials; kubeconfig exec credential plugins are rejected because they can outlive the recovery deadline.

## Access

The server listens on loopback by default. The management quickstart uses native
HTTPS and a named Bearer token. Enter the operator token in the dashboard; no
username is required. Use the initializer's HTTPS address, `--ca-file` and
`--token-file` arguments for CLI access. The operator bearer stays with the
operator, outside service-owned configuration and state.

For remote management, configure a certificate for the intended hostname or an
explicitly trusted HTTPS proxy using [management startup](docs/management-startup.md).
The generated localhost certificate is only for local access. Do not pass a
named management token through `-web.auth-file`; management loads its committed
named authority after the initial policy import.

File-configured observation-only deployments can instead use `-web.auth-file`
for a shared read-only token. This mode accepts Bearer authentication or Basic
authentication with username `cpra` and the token as the password; it does not
enable management commands. A plaintext listener needs an HTTPS reverse proxy
before remote use. The server and CLI also support `CPRA_AUTH_TOKEN` and
`CPRA_AUTH_TOKEN_FILE`. Keep bearer values out of URLs and command arguments.

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
make release VERSION=v0.1.0-rc.1
```

The [recorded release recipe](docs/release-engineering.md) uses a checksum-pinned
Go 1.27.2 compiler and explicit source identity. It produces unstripped,
optimized server/client archives for Linux, macOS and Windows on amd64/arm64,
Linux packages, a source archive and evidence inventories. Go 1.26 compatibility
is a separate test. Native execution gates qualify each platform; a successful
cross-build alone does not establish support.

Versioned installation needs only Go and the committed embedded assets:

```sh
go install github.com/ziad-hsn/cpra@VERSION
go install github.com/ziad-hsn/cpra/cmd/cpractl@VERSION
cpractl local init --management
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

The implementation is still undergoing release qualification. Full-provider verification and comparative million-monitor performance require completed evidence before a full release-readiness claim. The 24-hour endurance test is outside the requested work and is not a mandatory completion gate. The current verification inventory contains 35 driver roles, including the local A2A additions. Configured A2A provider-evidence integration is still pending; missing configuration never passes. [Validation instructions](docs/validation.md) distinguish local operations, provider accounts, comparisons and endurance.

[Provider test environments](docs/provider-testing.md) cover local services,
notification contract mocks, the Moto EC2 emulator and optional provider sandboxes.
Reports distinguish these results from real-account delivery and recovery.

## License

CPRa code is licensed under [MIT](LICENSE). [Dashboard dependency notices](LICENSES/dashboard.txt) accompany the embedded assets. Binary archives include notices for the dependencies in the selected build.

CPRa-authored artwork by Ziad Hassan is licensed under [CC BY 4.0](brand/ATTRIBUTION.md), with a separate note about the name and endorsement. Third-party fonts retain their own licenses. Brand assets and usage guidance live in [`brand/`](brand/BRANDING.md).

See [CONTRIBUTING.md](CONTRIBUTING.md) for development and verification guidance,
[SECURITY.md](SECURITY.md) for private vulnerability reporting, and the
[code of conduct](CODE_OF_CONDUCT.md) for participation.
