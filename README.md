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

CPRa monitors services and runs the recovery actions you configure. One Go
process schedules checks, tracks incidents, sends notifications and exposes a
React dashboard, an HTTP API and the `cpractl` command-line client. Use files for
read-only operation or named management access to edit monitors, notification
receivers, groups and secrets through the dashboard, API or Go SDK.

## Availability

**CPRa is in development; the first qualified release is not published.** Build
this checkout using the local workspace below. Public application, SDK and worker
module tags, packages, images and the Helm chart remain subject to qualification.
The intended first version is `v0.1.0-rc.1`; the chart uses `0.1.0-rc.1`. SDK and
worker versions are independent modules with the same Go 1.26 minimum.

[Versions and availability](docs/versions.md) is the current support matrix.
Linux amd64 development tests do not qualify all six planned Linux/macOS/Windows
amd64/arm64 targets. Native service, filesystem, provider-account and comparative
performance gates remain open where their environments or evidence are missing.
The 24-hour test is cancelled.

[Documentation](docs/index.md) · [Quickstart](docs/tutorials/quickstart.md) ·
[Configuration](docs/reference/config-schema.md) · [Drivers](docs/reference/jobs-reference.md) ·
[API](docs/reference/api-reference.md) · [Go SDK](sdk/go/README.md)

The [published website](https://ziad-hsn.github.io/cpra/) is updated separately;
use documentation from the same checkout as your executable.

## What CPRa does

- Runs health checks at a configured cadence with unhealthy and healthy thresholds.
  It retains incident and action events for 30 days, rather than every raw check.
- Commits incident state and action intent to single-node Raft before dispatch.
  Restarts retain confirmed outcomes and hold ambiguous interventions for review.
  Health observations continue while a conflicting intervention is held.
- Sends notifications to configured destinations and can restart containers,
  call a webhook, operate designated Kubernetes workloads, reboot EC2 instances,
  restart systemd units or ask an A2A agent to investigate.
- Supports acknowledge, dismiss, snooze, disable and Ignore as distinct controls.
  Ignore continues checks while suppressing ordinary alerts and interventions;
  verified recovery clears it. Optional automatic Ignore uses an explicit outage
  count and rolling window. Gray notices describe actual enable/disable changes.
- Lets operators apply collections from files, directories or explicitly requested
  URLs. Validation precedes activation; operation receipts preserve progress and
  item failures. Interrupted application requires explicit continuation.
- Reports observed health, action outcomes, queues, worker saturation and measured
  SLO attainment. Worker sizing starts with Erlang C/Allen–Cunneen and adjusts
  within configured bounds. Percentiles and targets are measurements, not an SLA.

![CPRa dashboard showing monitor state, notification queues and management navigation](docs/assets/dashboard-overview.png)

Current development dashboard, captured from a local fixture with one disabled
monitor. This illustration is not production capacity evidence.

## Build and run locally

Install Go 1.26 or newer, Make and Python 3. The checkout includes the embedded
dashboard; Node and pnpm are needed only when its build inputs change.

```sh
make
export PATH="$PWD/bin:$PATH"
cpra -version
cpractl version
cpractl local init --management
```

Make generates an ignored `bin/cpra-sdk.work` containing the application, SDK and
worker modules. It respects an explicitly selected `GOWORK`, including `off`.
Do not publish module tags or edit dependency versions to work around development
workspace setup. See [contributor setup](CONTRIBUTING.md) for direct Go commands
and editors.

Initialization creates an empty manifest, a named operator token and hash policy,
a wrapping key, localhost TLS certificates and runtime settings in
platform-standard locations. It configures a notification-log directory, which
the server creates with private permissions at startup. It prints exact start and client
commands, never the bearer value. It refuses existing files and starts no service.
System installation requires a protected operator-token output outside
service-owned configuration and state.

Use the printed server command. Trust the generated local CA explicitly in your
browser, open `https://localhost:8060` and sign in with the token from its protected
file. The CLI uses `--server`, `--ca-file` and `--token-file`; the corresponding
`CPRA_SERVER`, `CPRA_CA_FILE` and `CPRA_AUTH_TOKEN_FILE` variables make connections
repeatable. Keep token values out of URLs and command arguments.

Follow the [quickstart](docs/tutorials/quickstart.md) to run a local HTTP target,
validate and apply `examples/monitors.yaml`, inspect `monitor/example-service`,
and restart against the same state. Management imports bootstrap configuration
once; later edits require explicit `apply`. SIGHUP reports that reload is
unsupported; SIGINT and SIGTERM begin graceful shutdown.

## Drivers and configuration

| Function | Default build | Additional built-in tags |
| --- | --- | --- |
| Checks | HTTP, TCP, ICMP, DNS, UDP, TLS, Docker, gRPC | `redis postgres mysql mongo rabbitmq kafka` |
| Recovery | Docker, HTTP webhook, A2A | `kubernetes aws systemd` |
| Notifications | Log, Slack, PagerDuty, email, webhook, Telegram, Discord, Opsgenie, Mattermost, VictorOps, Pushover, Datadog, A2A | `teams twilio` |

```sh
make BUILD_TAGS='redis postgres kubernetes'
cpra -capabilities
cpra -validate -yaml examples/monitors.yaml -runtime-config /absolute/path/from/init/runtime.yaml
```

Replace the runtime path with the one printed by initialization.
Validation checks schemas, references and compiled capabilities without opening
storage or contacting targets. File log notifications require a process-configured
root directory; monitor files select paths inside it. An omitted filename writes
to stdout. Pass the initialized runtime
configuration when validating a manifest that uses log notifications.

TLS checks verify certificates by default. The explicit per-monitor
`insecure_skip_verify` option disables certificate-chain and hostname verification
only for that monitor. It does not disable recovery health checks or API
authentication. Configured certificate-expiry checks still run.

The gRPC check defaults to TCP reachability; `mode: health` calls the gRPC health
service and requires `SERVING`. MongoDB SRV discovery, Kubernetes exec credential
plugins and SMTP username/password authentication are currently unsupported.
[Driver reference](docs/reference/jobs-reference.md) describes required settings,
transport choices, deadlines and platform restrictions.

The separately tagged [external-worker library](sdk/go/worker/README.md) runs
operator-owned Go handlers outside CPRa. It requires `externaljobs` on both sides,
explicit server enablement and scoped credentials. It is not a sandbox for
untrusted code.

## Persistence, operation and safety

User and system installations have different platform-standard paths. Linux
system services explicitly store state in `/var/lib/cpra`; containers and Helm
require retained storage. Use `cpractl local paths` to inspect local defaults.
Explicit `-data-dir` wins over runtime configuration and user defaults. A legacy
working-directory `cpra-data` requires an explicit path or stopped migration.

Single-node Raft provides durable state, not cross-instance failover. Keep one
owner per store. A complete stopped backup includes node identity, Raft data,
snapshots, history and collection generations. Storage errors stop unsafe
admission; restoring or repairing state is an explicit operator action.

Recovery attempts are limited per incident by `intervention.max_attempts`
(default one). Successful action completion means health verification is pending.
Only the configured healthy threshold confirms recovery. An unknown delivery
holds that delivery, without suppressing unrelated later incidents or destinations.
Transport acceptance alone does not prove delivery to a person.

Configuration authors can choose destinations and actions using the process
account's permissions. Treat them as trusted operators. Management is not a
multi-tenant security boundary. Use [management startup](docs/management-startup.md)
for HTTPS, authority, credential storage and rotation.

[Native installation](docs/native-installation.md) ·
[Containers and Helm](docs/container-helm.md) ·
[Backup and recovery](docs/durability.md) · [SLOs](docs/slo.md)

## Development and verification

```sh
make dev-workspace
make test-fast
make check
make test-all-drivers
make sdk-check
```

`test-fast` is a short edit/test loop. Full race, process, browser, build-tag and
release matrices remain separate. Root `go test ./...` excludes nested modules.
The [contributor guide](CONTRIBUTING.md) covers tools, generation and verification.

The [provider verification guide](docs/provider-testing.md) separates local
fixtures, provider acceptance and independently observed effects. Unconfigured
accounts remain unverified. The [release guide](docs/release-engineering.md)
describes reproducible artifacts and private qualification gates. Passing a
source test does not authorize publication or establish a capacity claim.

## License and community

Code is [MIT-licensed](LICENSE). CPRa-authored artwork by Ziad Hassan uses
[CC BY 4.0](brand/ATTRIBUTION.md), with a separate name and endorsement note.
Third-party licenses remain in the [dashboard notices](LICENSES/dashboard.txt)
and packaged dependency notices.

Read [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md) and the
[code of conduct](CODE_OF_CONDUCT.md). Support is through voluntary donations;
there are no subscription-only features or promised response times.
