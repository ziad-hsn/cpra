# Provider test environments

The [provider research](provider-testing-research.md) identifies supported test
options for all 33 drivers and links their official documentation. Local services,
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
network failures do not invent a status. Aggregate flags distinguish:

| Field | Passing requirement |
| --- | --- |
| `all_configured_passed` | A nonempty configured subset passed. |
| `all_drivers_tested` | All 33 production driver operations ran. |
| `all_drivers_passed` | All 33 passed at their declared test boundary. |
| `all_providers_verified` | All 33 passed with `live_account` effect evidence. |

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
with positive reports to verify evidence hashes and assemble the 33-driver matrix.
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
    --driver "$driver" --pull --out "evidence/local/$driver.json" || exit
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

Copy [sandboxes.yaml](../examples/verification/sandboxes.yaml) to a private file.
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

Sandbox/mock results do not complete account-backed, comparative-performance or
24-hour endurance gates. See [validation](validation.md) for those requirements.
