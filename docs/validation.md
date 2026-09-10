# Release evidence and live verification

Implementation tests, local provider operations, account-backed verification,
comparative performance and uninterrupted endurance are separate evidence gates.
An HTTP acceptance response is not proof of delivered notification or completed
recovery. Local fixtures do not certify a cloud production account.

## Configured providers

Build the runner using the same optional tags as the candidate:

```sh
go build -tags 'redis postgres mysql mongo rabbitmq kafka kubernetes aws systemd teams twilio' -o bin/cpra-verify ./cmd/cpra-verify
./bin/cpra-verify -config /path/to/user/live.yaml -live -out provider-report.json
```

Copy `examples/verification/live.yaml` into a private location. It contains the
existing monitor/endpoint schema and one case for each of 14 checks, five
recoveries and 14 notifications. Enable a case only after configuring its
designated resource and independent observer. Credentials belong in this user
configuration or the provider's existing credential files/environment, never
in the published evidence. AWS uses the SDK credential chain, including
`AWS_PROFILE`, shared configuration and credential files, or workload roles.
Kubernetes accepts supported token/certificate kubeconfig or in-cluster
configuration; executable credential plugins remain unsupported. SMTP uses the
existing relay configuration, without an added SMTP password mode.

Observers are executable argument arrays, not shell strings. Each is invoked
with `CPRA_VERIFY_RUN_ID` and `CPRA_VERIFY_PHASE=before|after`. The after invocation
receives the before JSON on standard input and must return JSON with
`observed: true` only when the independently queried resource or destination
establishes the requested effect. Use a unique receipt or state transition and
the run ID where the provider supports it. A static successful observer is not
valid evidence. The runner creates a private `<report>.evidence/` directory, retaining numeric
and boolean evidence and resource fingerprints. Each record includes an evidence
filename and SHA-256 digest. Arbitrary observer text, credentials, URLs and
provider errors are excluded. Keep original provider records privately for review;
the retained source hashes allow comparison with those originals.

| Driver | Concrete independent observation |
| --- | --- |
| HTTP | Isolated target request counter and expected HTTP response |
| TCP | Connection accepted by the designated listener |
| ICMP | Echo request/reply capture or designated target packet accounting |
| DNS | Actual resolver query and expected answer for the test record |
| UDP | Echo server receipt and reply of the configured datagram |
| gRPC | TCP listener acceptance; this driver verifies port reachability |
| Docker check | Real daemon inspection of the isolated running container |
| TLS | Real handshake and expected certificate/expiry at the designated endpoint |
| Redis | Real Redis PING and isolated command-statistics increment |
| PostgreSQL | Real database connection/ping and database-side session/query evidence |
| MySQL | Real COM_PING and server-side command/session evidence |
| MongoDB | Real ping command and isolated server command evidence |
| RabbitMQ | Real AMQP connection/channel observed by the broker |
| Kafka | Real broker metadata request and broker-side request evidence |
| Docker recovery | Changed container start time/process identity on the real daemon |
| Webhook recovery | Independent target state change recorded by the receiver |
| Kubernetes recovery | Changed workload revision and completed isolated rollout/scale |
| AWS recovery | Designated EC2 reboot operation plus changed guest boot identity |
| systemd recovery | Changed invocation/process identity of a dedicated test unit |
| Log | One new JSON event with the verification monitor/run ID |
| Slack | Message retrieved from the designated test channel |
| PagerDuty | Incident/event visible in the designated integration/account |
| Email | Message observed in the designated recipient mailbox |
| Webhook notification | Payload received by the designated receiver |
| Telegram | Message received in the configured test chat |
| Discord | Message retrieved from the designated test channel |
| Opsgenie | Alert visible in the designated account/integration |
| Mattermost | Message retrieved from the configured test channel |
| VictorOps | Event visible in the designated routing destination |
| Pushover | Notification/receipt observed for the configured user/device |
| Datadog | Event queried from the configured account |
| Teams | Workflow completes and message appears in the designated channel |
| Twilio | Provider delivery status plus receipt at the designated test number |

Unconfigured cases report `not_configured`; a configured operation without
independent completion proof fails verification. Exit code 2 means the inventory
is incomplete, not a full pass. Ordinary CI never needs provider credentials.

`scripts/verification/local.py --binary bin/cpra-verify --out provider-report.json`
runs isolated HTTP, webhook recovery, webhook notification and file-log cases
using the production jobs. It cannot verify the other provider accounts.

## Scale campaign

The agreed host is the current Ubuntu WSL installation. Its virtual disk is on
C:. The campaign checks physical host free space and requires at least 30 GiB
plus the selected fixture allowance (default 5 GiB), independently of apparent
free space inside the guest. Disk cleanup and additional infrastructure are
external prerequisites. The harness does not substitute a smaller monitor count
or another machine when the prerequisite fails.

Build the baseline commit `a370969b041b399c0778318d8915ce059fd74294` and the candidate
with identical Go version, build tags and optimized build flags. The preparation
script checks the baseline revision and records compiler settings and binary
hashes. Large campaigns reject an uncommitted candidate or mismatched binaries.

```sh
python3 scripts/benchmark/prepare.py --go /path/to/go1.25/bin/go --out /path/to/prepared-builds
```


```sh
python3 scripts/benchmark/campaign.py --mode preflight --out evidence/local/preflight
python3 scripts/benchmark/campaign.py --mode compare --builds /path/to/prepared-builds/builds.json --candidate /path/to/candidate --baseline /path/to/baseline --target bin/cpra-target --out evidence/local/comparison
python3 scripts/benchmark/campaign.py --mode soak --builds /path/to/prepared-builds/builds.json --candidate /path/to/candidate --target bin/cpra-target --out evidence/local/soak
```

Comparison uses 10,000, 100,000 and 1,000,000 distinct monitor configurations,
60-second cadence, five-minute warmup, fifteen-minute measurement and three
repetitions of each build. Baseline internal percentiles remain unavailable.
The soak uses one million monitors and 24 hours of actual measurement following
warmup. The target performs real network I/O with per-monitor bounded counters
and digests. Samples record rate, CPU, RSS, descriptors, worker/queue statistics,
SLOs, persistence timing and bounded dashboard reads. Logs rotate; individual
check histories are not retained. Sleep, process interruption and missing fleet
rows produce an incomplete result.

`--mode smoke` is a ten-monitor harness check and is explicitly excluded from
release evidence. Campaign output remains `measured_pending_review` until
resource stability, faults, operation reconciliation and all required evidence
have been reviewed. It never promotes a smoke run to scale or endurance proof.
The release gate must not pass while provider or full-duration evidence is
missing. Source and documentation publication must identify the same reviewed
candidate commit.

## Additional local and account scenarios

- `docker_local.py --binary bin/cpra-verify --out docker-report.json` creates one
  uniquely named, disposable container from an already installed image. The
  independent observer checks its daemon state and changed start timestamp.
  The fixture uses an init process so its workload receives shutdown signals.
- `database_local.py --binary bin/cpra-verify --driver redis --out redis-report.json`
  starts one actual server profile from `examples/verification/fixtures.compose.yaml`.
  Repeat with postgres, mysql, mongo, rabbitmq and kafka. Images are not pulled
  unless `--pull` is explicitly supplied. The physical-space gate runs before
  startup or downloads. A separate TCP relay forwards real protocol bytes to
  the real server and records fresh connections and bidirectional traffic.
- Apply `examples/verification/kubernetes.yaml` to the operator-designated test
  context, then configure the Kubernetes case and use `observe_effect.py
  kubernetes deployment/cpra-verification --namespace cpra-verification --context
  YOUR_CONTEXT`. It compares workload generation and waits for availability.
- Install `examples/verification/cpra-verification.service` as a dedicated test
  unit; configure the systemd case and `observe_effect.py systemd
  cpra-verification.service`. It requires a changed invocation and active state.
- For AWS, designate an expendable EC2 instance in the manifest and an SSH alias
  in existing user SSH configuration. `observe_effect.py boot-id YOUR_SSH_ALIAS`
  requires the guest kernel boot ID to change and the guest to become reachable.
  An EC2 API acknowledgement alone cannot pass this observation.
- Notifications can use `observe_receipt.py DRIVER /private/receipts.jsonl` with
  a user-configured account/mailbox/device reader. The reader must retrieve the
  actual destination message and append the run ID, driver, provider message ID,
  receive time and `delivered: true`. CPRa acceptance responses are insufficient.
  This adapter does not provision or invent account-specific receipt readers.

Fixtures requiring unavailable accounts, permissions, images or disk space remain
not verified. The manual `live-verification.yml` workflow requires a dedicated
self-hosted runner and an operator-owned argument file; pull-request CI cannot
trigger it. The repository's normal workflow runs only isolated local HTTP/file
operations. Live results are not automatically published.

`--mode faults` runs burst release, slow targets, outage, recovery traffic and
process-kill/restart campaigns. The soak introduces a 100-monitor outage cohort
for 125 seconds approximately hourly, recording those windows and their recovery
periods. Healthy SLO assertions exclude these declared fault windows; all samples
remain in the evidence. The target tracks repeated intervention per incident
phase; retained history is reconciled against target-side effects. Heap,
goroutines, disk/history/snapshot sizes and target saturation are recorded in
addition to process and controller measurements.
