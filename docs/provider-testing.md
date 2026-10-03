# Provider test environments

The [provider research](provider-testing-research.md) identifies supported test
options for the original 33 drivers and links their official documentation. Local services,
mock endpoints, provider sandboxes and real accounts establish different evidence.

## Reporting

Each case accepts `evidence_type`: `local_integration`, `mock_contract`,
`provider_sandbox`, or `live_account`. Omission preserves the original
`live_account` behavior. New local fixtures explicitly declare their boundary.
Cases normally require independent before/after observation of the requested
effect. Twilio's official test credentials alone allow
`observation_boundary: api_acceptance`: these test SMS messages are not delivered,
so that report always has `observed: false`.

Reports retain `configured`, `operation_invoked`, `accepted`, `observed`, timing,
classification and evidence hashes. Typed HTTP rejections include `http_status`;
network failures do not invent a status.

Each CLI invocation requires a new report path and a new adjacent `.evidence`
directory. Both are reserved before observers or drivers run. An existing report,
linked report or retained evidence directory prevents admission; retry with a new
path and preserve the earlier campaign. The report is flushed before successful
completion and records `binary_sha256` for the actual verification executable.
The evidence aggregator retains that identity and rejects contradictory hashes
in adjacent suite or fixture records. A binary hash identifies bytes; frozen
source provenance and independent review remain separate release requirements.

For a frozen release, build the verification executable from its recorded inputs:

```bash
python3 -B scripts/release/provider_evidence.py build \
  --manifest dist/release/RELEASE.json --go /absolute/path/to/go \
  --dependencies /private/cpra-dependencies --out /private/provider-build
/private/provider-build/cpra-verify -live -require-configured-pass \
  -config /private/provider-cases.yaml -out /private/campaign-001.json
python3 -B scripts/release/provider_evidence.py verify \
  --manifest dist/release/RELEASE.json --build /private/provider-build \
  --reports /private/campaign-001.json --out /private/provider-inspection.json
```

The build command invokes no providers. It records native Linux target and
compiler identity, the full source inventory, unchanged inputs, exact build
arguments, dependency verification output, executable build information and
executable digest. The release workflow retains this artifact separately from
the public distribution archives. Account-backed execution remains an explicit
operator action using designated targets and private configuration.

Inspection checks retained executable and log bytes, source/version/compiler
agreement, actual report fingerprints, unique run identities and every retained
effect. Missing configuration produces an incomplete 35-role matrix and exit
code 2; corrupt or mismatched evidence produces exit code 1. Even all live-account
effects produce `consistent_pending_review`, with `release_gate_passed: false`.
Independent review and the other release gates remain required. Output paths
must be fresh; the reader cannot replace an earlier inspection.

Before freezing source, `build --working-tree --cache /private/prepared-cache`
can exercise the same compiler settings against a captured copy. These builds
identify the base commit separately and do not inject it as the current source
commit. Their `working_tree_provider_build` reports cannot satisfy frozen-release
inspection. The cache is optional and does not disable Go checksum verification
or change the public proxy policy. Public downloads remain a separate gate.

The [1 October integrity checkpoint](implementation/STATUS.md)
records both compiler default/all-driver package matrices and a real CLI process
test. The unconfigured run returns exit code 2, records all 35 missing providers,
invokes no provider operation and cannot be overwritten by a second invocation.
These checks protect report integrity; they do not verify provider accounts.

Aggregate flags distinguish:

| Field | Passing requirement |
| --- | --- |
| `all_configured_passed` | A nonempty configured subset passed. |
| `all_drivers_tested` | All 35 registered driver operations ran. |
| `all_drivers_passed` | All 35 passed at their declared test boundary. |
| `all_providers_verified` | All 35 passed with `live_account` effect evidence. |

Default CLI exit code 2 still means full live verification is incomplete.
`-require-configured-pass` explicitly accepts a nonempty passing configured subset.
Missing credentials and disabled cases stay `not_configured`. Local or mock passes
cannot complete the real-account gate.

## Socket fixtures without accounts

```sh
make verify-local
make verify-contracts verify-protocols BUILD_TAGS='teams twilio'
python3 -m unittest discover -s scripts/verification -p 'test_*.py'
```

The notification suite tests all 12 HTTP drivers: Slack, PagerDuty, webhook,
Telegram, Discord, Opsgenie, Mattermost, VictorOps, Pushover, Datadog, Teams and
Twilio. Each runs success, HTTP 400/429/500, malformed HTTP framing, disconnect and
timeout cases. Receivers inspect the method, path, headers, credentials, payload
fields and unique run marker over real loopback connections. These are
`mock_contract` results, not provider delivery. They do not assert parsing of
successful JSON response bodies that the production drivers do not interpret.

The protocol suite runs positive and negative HTTP, TCP, UDP, DNS, TLS, SMTP and
gRPC scenarios against actual local listeners. TLS verification stays enabled
using a temporary trusted certificate. The current gRPC driver verifies TCP
reachability, not the gRPC Health protocol. SMTP verifies the envelope and message
at the local receiver, not Internet mailbox delivery. DNS `server` accepts a host,
`host:port`, or IPv6 address, with port 53 as the default.

Positive results are in the requested report. Adjacent `*.suite.json` files also
evaluate expected failures. Negative reports intentionally contain `status: fail`;
their suite verdict requires proof that the driver ran and the expected receiver
observation and failure occurred. Oracle tests ensure setup failures cannot pass.

`scripts/verification/record_fixture.py` can wrap existing local-service fixture
commands to retain the actual command, candidate hash, installed image identity,
exit status and report hash in an adjacent `.fixture.json`. Use
`scripts/verification/summarize.py --reports REPORT... --out MATRIX.json --require-all`
with positive reports to verify evidence hashes and assemble the 35-driver matrix.
It rejects conflicting binary/source metadata and identifies missing fingerprints.
Negative scenarios remain in the adjacent suite files. Full test coverage does
not change `all_providers_verified` to true for local or mocked evidence.

## Local services and the EC2 emulator

Build a static binary with all optional drivers:

```sh
CGO_ENABLED=0 go build -trimpath \
  -tags 'redis postgres mysql mongo rabbitmq kafka kubernetes aws systemd teams twilio' \
  -o bin/cpra-verify ./cmd/cpra-verify
python3 scripts/verification/docker_local.py --binary bin/cpra-verify --out evidence/local/docker.json
for driver in redis postgres mysql mongo rabbitmq kafka; do
  python3 scripts/verification/database_local.py --binary bin/cpra-verify \
    --driver "$driver" --out "evidence/local/$driver.json" || exit
done
docker pull mattermost/mattermost-preview
python3 scripts/verification/mattermost_local.py --binary bin/cpra-verify --out evidence/local/mattermost.json
python3 -m venv /path/to/cpra-moto-venv
/path/to/cpra-moto-venv/bin/pip install -r scripts/verification/requirements-moto.txt
/path/to/cpra-moto-venv/bin/python scripts/verification/moto_local.py \
  --binary bin/cpra-verify --out evidence/local/moto.json
```

Docker checks an isolated running container and confirms changed start time after
recovery. Database fixtures run actual Redis, PostgreSQL, MySQL, MongoDB, RabbitMQ
and Kafka servers. A forwarding relay records fresh bidirectional traffic and the
production driver must return success. This establishes connection/protocol
activity, not a decoded server-side audit of each command. Fixtures remove their
own containers, volumes and networks. Database downloads require explicit `--pull`
and the physical host free-space preflight.

Cached-image fixtures use a separate short-run admission check: 4 GiB of physical
host free space remains reserved, plus 128 MiB for Docker, 256 MiB for Redis,
1 GiB for PostgreSQL/MongoDB/RabbitMQ/Kafka, or 2 GiB for MySQL. They also require
available memory for the configured container limit plus a 1 GiB reserve.
This check does not qualify a large benchmark. Downloads still require the
large preflight of 30 GiB plus 5 GiB fixture headroom.

Each database profile limits CPU, memory, process count and log rotation and
places disposable data in size-bounded tmpfs. The Docker check/restart fixture
uses a read-only root filesystem. These controls follow the [Compose service
settings](https://docs.docker.com/reference/compose-file/services/); they are
test fixture limits, not production sizing recommendations. Startup uses native
clients. PostgreSQL requires an authenticated `SELECT 1` and verifies the
configured database, rather than treating an open socket as readiness. See
[psql](https://www.postgresql.org/docs/17/app-psql.html). The Redis check requires
an authenticated [PING response](https://redis.io/docs/latest/commands/ping/).
The `.environment.json` sidecar retains the actual image and container identity,
effective resource limits, preflight and checked cleanup. `record_fixture.py`
binds that sidecar to its passing report; the summary and frozen-source reader
reject changed environment evidence or incomplete cleanup.

Native readiness commands also have a timeout inside the container, so an
interrupted Docker client does not leave its command running. Kafka uses
different internal and host-facing listeners, with reachable advertised addresses
for both clients; see [Kafka listener configuration](https://kafka.apache.org/39/generated/kafka_config.html#advertised.listeners).
Its Java processes use the fixture's one-CPU limit and bounded heap. A startup
deadline is a fixture failure before the CPRa driver runs, not a passing check.

Mattermost creates a local server, temporary user/team/channel and webhook. Its
observer retrieves the channel posts and checks the run marker; a separate case
checks invalid-webhook rejection. Reports retain image identity and discard
temporary credentials. Moto uses dummy credentials and a forced loopback EC2
endpoint. It validates the signed reboot request, EC2 XML response and invalid
instance rejection. It never boots or reboots an EC2 guest.

For Kubernetes, systemd and ICMP, create a dedicated disposable kind node with
Python, kubectl, systemd and D-Bus available, and load `ubuntu:latest` into it:

```sh
kind create cluster --name cpra-provider-tests --kubeconfig /tmp/cpra-provider-tests-kubeconfig
kind load docker-image ubuntu:latest --name cpra-provider-tests
# Required only if the disposable node image lacks the system bus.
docker exec cpra-provider-tests-control-plane sh -c 'apt-get update && apt-get install -y dbus'
python3 scripts/verification/cluster_local.py --binary bin/cpra-verify \
  --node cpra-provider-tests-control-plane --out evidence/local/cluster.json
kind delete cluster --name cpra-provider-tests
```

The script requires a `cpra-` prefixed kind node. It creates a unique Deployment
and systemd unit, confirms a completed rollout and changed service invocation,
and removes those resources. ICMP uses the quiet node's kernel echo request/reply
counters; unrelated ping traffic must not share that namespace. ICMP permission
and D-Bus startup changes apply only inside the disposable node and are restored.
This does not test Kubernetes executable credential plugins or cloud clusters.

## Optional provider sandboxes

Copy [sandboxes.yaml](https://github.com/ziad-hsn/cpra/blob/4c6baf58df8bf7fd4e02e0399fbe092ba867b20f/examples/verification/sandboxes.yaml) to a private file.
Its disabled examples cover Twilio test credentials, Telegram's test environment,
Slack developer sandboxes and Teams developer tenants. Operators supply credentials,
destinations and receipt observers where required, then enable configured cases:

```sh
bin/cpra-verify -live -require-configured-pass \
  -config /private/sandboxes.yaml -out evidence/local/sandboxes.json
```

Telegram `test_mode: true` selects `/bot<TOKEN>/test/sendMessage`; it cannot be
combined with explicit `url`. Telegram, VictorOps, Pushover, Datadog and Twilio also
accept an optional full operation `url` for designated test endpoints. Existing
production defaults and URL/SSRF/redirect policies remain in effect. Existing
webhook-configurable drivers already accept local test URLs.

Sandbox/mock results do not complete account-backed or comparative-performance
gates. The 24-hour endurance test is outside the requested work and is not a
mandatory completion gate. See [validation](validation.md) for those requirements.

## A2A candidate evidence

The local candidate adds recovery and notification A2A entries, bringing the inventory to 35. The configured application tests exercise real HTTP requests through normal startup, encrypted Raft configuration, controller dispatch and restart against a local agent fixture. The separate upstream A2A SDK fixture tests protocol interoperability. Neither establishes effects in an operator's agent deployment.

The configured runner now gives each A2A case a dedicated Raft store and commits
intent and start before invoking the production driver. Copy
`examples/verification/a2a.yaml` to a private file, select separate recovery and
notification identities, configure the designated agent interfaces and observers,
and enable the required cases. Choose an **unused absolute `state_directory`** in
an existing private parent for each campaign. Reusing a directory fails before
any configured provider operation; an interrupted campaign is never implicitly
resumed or replaced. Keep its state for outcome review.

The verification store contains a synthetic incident, action identities, bounded
task receipts and outcomes. Its synthetic failed observation does not contact the
manifest's check target. Provider URLs, credentials and response contents remain
outside Raft. The runner is an explicit driver verification operation; ordinary
controller scheduling, configuration changes and recovery-failure escalation
have separate application tests. The notification verification scenario uses a
red incident and invokes only the selected notification destination.

The independent observer receives `CPRA_VERIFY_RUN_ID`, `CPRA_VERIFY_MONITOR_ID`
and `CPRA_VERIFY_ACTION_ID` before and after invocation. These identify the exact
outgoing message and committed action. The included `observe_agent.py` reads
JSON-lines records exported by the designated destination or recovery target.
Each record must have the original identities, its role (`intervention` or
`notification`), `observed: true`, a Unix UTC `observed_at`, a nonempty
`provider_id`, and the relevant effect:

- `recovery_completed`: independently observed recovery of the designated target.
  A completed A2A task alone is insufficient.
- `incident_received`: independently observed receipt of the incident at the
  designated agent. This does not establish completed investigation or recovery.

The exporter can take `run_id` from the verification message's
`configurationRevision`, `monitor_id` from `monitorId`, and `action_id` from
`actionId`/`messageId`. It must observe the effect independently, rather than
generating a receipt from CPRa's result. Wrong identities, stale observations,
multiple distinct effects and malformed evidence do not pass. Retained reports
contain bounded action identities and evidence digests, without raw provider IDs
or arbitrary agent output. Observer input is limited to 16 MiB per export and
64 KiB per record; its default wait is 60 seconds, with a 120-second maximum.

```sh
bin/cpra-verify -live -require-configured-pass \
  -config /private/a2a.yaml -out evidence/local/a2a.json
```

Both A2A entries remain missing from the full provider gate until configured
agent effects pass. Local tests cover committed starts, unknown outcomes,
refusal to reuse state, a real process termination and independent-observer
rejection. They use isolated HTTP fixtures and do not certify an operator's
agent deployment. See the [A2A implementation record](implementation/a2a-jobs.md).
