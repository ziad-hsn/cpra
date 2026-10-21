# Release evidence and live verification

Implementation tests, local provider operations, account-backed verification,
comparative performance are separate evidence gates. A 24-hour endurance run is
outside the requested work and mandatory completion checks as of 1 October 2026.
An HTTP acceptance response is not proof of delivered notification or completed
recovery. Local fixtures do not certify a cloud production account. See
[provider test environments](provider-testing.md) for supported socket fixtures,
actual local services, Moto and optional sandbox configuration, with explicit
evidence classification and reproducible commands.

## Frozen build verification

Use the extracted source and private dependency cache from the approved release.
The source directory excludes the generated `RELEASE.json`; pass the matching
metadata separately. Select a new output name outside the source and cache.
With `--security`, dependency verification and build exclusion run before the
long correctness matrix; release-compiler vulnerability findings stop that
matrix while preserving the original scanner output. JSON scanner exit zero
alone is not a passing security result.

```sh
python3 -B scripts/release/build_campaign.py \
  --release /private/assets/RELEASE.json \
  --source-dir /private/dependencies/source \
  --dependencies /private/dependencies \
  --go /path/to/go1.26.10/bin/go \
  --security \
  --a2a-reference-go /path/to/go1.27.3/bin/go \
  --out /private/builds/build-minimum.json

GOBIN=/private/security-tools GOWORK=off GOENV=off GOFLAGS= GOTOOLCHAIN=local \
  /path/to/go1.27.3/bin/go install golang.org/x/vuln/cmd/govulncheck@v1.7.0

python3 -B scripts/release/build_campaign.py \
  --release /private/assets/RELEASE.json \
  --source-dir /private/dependencies/source \
  --dependencies /private/dependencies \
  --go /path/to/go1.27.3/bin/go \
  --security --govulncheck /private/security-tools/govulncheck \
  --a2a-reference-go /path/to/go1.27.3/bin/go \
  --pnpm /path/to/pinned/pnpm \
  --out /private/builds/build-release.json

python3 -B scripts/release/build_evidence.py \
  --release /private/assets/RELEASE.json \
  --campaign /private/builds/build-minimum.json \
  --campaign /private/builds/build-release.json \
  --out /private/assessments/build.json

python3 -B scripts/release/security_evidence.py \
  --release /private/assets/RELEASE.json \
  --campaign /private/builds/build-minimum.json \
  --campaign /private/builds/build-release.json

python3 -B scripts/release/management_evidence.py \
  --release /private/assets/RELEASE.json \
  --campaign /private/builds/build-minimum.json \
  --campaign /private/builds/build-release.json

python3 -B scripts/release/a2a_evidence.py \
  --release /private/assets/RELEASE.json \
  --campaign /private/builds/build-minimum.json \
  --campaign /private/builds/build-release.json
```

The Go matrix selects every package in each independent module. Application
variants are default, all built-in drivers, `externaljobs`, and their combined
build; SDK variants are base and external; the worker module requires the tag.
Both compilers run race-enabled tests and vet. The recorder enables the real CLI
fixtures; their separately built binaries retain the CGO-free installation
recipe and verify their compiler and tag selection. Race coverage applies to
the test process, not automatically to that CGO-free CLI executable.

Each variant retains package selection, original Go completion events, stderr,
exact invocations, verified module graphs, the SDK archive and unchanged source
fingerprints. The matching metadata is retained separately and checked again at
completion. Checksum exceptions, automatic toolchain selection and
workspaces cannot silently change the recorded Go policy. A narrowed command,
missing nested module, missing combined variant or incomplete package completion
cannot satisfy the reader.

The recorder also preserves the original API operation inventory, base contract,
base models and optional worker schema in private files alongside each campaign.
The management reader compares these bytes with the frozen source and the SDK
archive, then checks both generated schema projections. Every selected SDK
operation must have its own passing inventory subtest; a passing parent alone is
insufficient. Required server/controller lifecycle, SDK presence/security/batch
and CLI contracts remain separate. The five real CLI scenarios must identify
the compiler and tags actually used by their helper binaries in every application
variant. Missing contracts or skipped coverage produce an incomplete result;
changed inputs and contradictory identities are invalid.

The current inventory contains 71 base operations and 11 additional operations
under `externaljobs`. Its transport fixtures do not prove deployed server
interoperability. The named server and controller integration tests cover their
individual real scenarios, while browser behavior, accessibility, external
worker execution, packaged binaries and independent review require their own
evidence. This reader cannot authorize publication or start an endurance run.

The explicit A2A mode builds the upstream SDK reference server with the pinned
release compiler, while each interoperability test runs with the selected CPRa
compiler. The reference module declares the common Go 1.26.10 minimum; it is a separate test process
and never enters CPRa's application dependency graph. The recorder keeps
the original reference sources, module verification and graph, vet output,
executable, build information, package selection and full jobs-package test
events. Source and executable fingerprints must remain unchanged. Only this
separate reference build downloads its public, checksum-verified dependencies;
CPRa test dependencies remain offline and readonly.

The A2A reader requires all 21 upstream cases: seven responses through transport,
intervention and notification paths. It also checks the named application,
controller, persistence, management and verification contracts in all four
application variants on both compilers. Required process-kill boundaries,
original-target restart observation and confirmed-failure escalation have their
own case requirements. A passing parent cannot hide a missing or skipped case.
Protocol interoperability is reported as a local upstream fixture; it does not
prove completed recovery or delivered investigation context at a designated agent.

To assess actual agent effects, add the frozen provider executable and retained
reports from the [configured A2A scenarios](provider-testing.md#a2a-candidate-evidence):

```sh
python3 -B scripts/release/a2a_evidence.py \
  --release /private/assets/RELEASE.json \
  --campaign /private/builds/build-minimum.json \
  --campaign /private/builds/build-release.json \
  --provider-build /private/provider-build \
  --provider-report /private/providers/agent-effects.json
```

Both recovery and notification require their independently observed original
action effects. Missing configuration remains unverified; task acceptance,
local fixtures and sandbox results cannot satisfy the live-effect requirement.
The provider build and reports must be supplied together. This command only
reads existing records and never contacts an agent. Its result remains pending
independent review, and the other 33 provider roles have separate requirements.

The explicit security mode also compiles every required external-worker symbol
in the tagged build and verifies its absence in default and all-built-in builds
on both compilers. Its original declarations, compiler diagnostics, selected
source hashes and dependency selection remain available for review. Downloaded
SDK files are identified by their canonical repository paths, rather than their
cache location. These records cannot substitute for worker protocol execution.

The production compiler additionally runs pinned `govulncheck` across all seven
module/tag combinations using the CGO-free binary policy. Original scanner bytes,
build identity, package selection, database timestamp, inventory and findings are
retained. JSON output exits successfully even when it contains vulnerabilities;
the reader inspects the messages and keeps reachable findings incomplete.
Required/imported findings remain visible for review. A failed scan, changed
dependency graph or omitted variant cannot become a passing result.
[Govulncheck behavior](https://pkg.go.dev/golang.org/x/vuln@v1.7.0/cmd/govulncheck),
[streaming JSON contract](https://pkg.go.dev/golang.org/x/vuln@v1.7.0/internal/govulncheck).

This is Linux amd64 source-analysis and contract evidence. It does not qualify
other native platforms, browser security, unknown reflection paths or independent
security review. The separate browser, packaging and review requirements remain
mandatory. A build record created without `--security` remains useful build
evidence but cannot satisfy the security reader. The 24-hour test stays excluded.

The release compiler also needs the pinned Node and pnpm from the recipe. It
records SDK generation from two roots and compares every generated output with
the frozen source. Dashboard verification includes types, lint, every Vitest
case and the embedded-asset/license rebuild. Frontend and generation dependencies
may download through their ordinary registries with pinned inputs; they receive
no inherited provider credentials or frontend environment values. Go test and
vet dependency resolution remains offline and readonly.

Skips are explicit. Subprocess entry points and separately required browser and
worker-load fixtures are identified; other skips leave the matrix incomplete.
The browser, worker execution, security, native packaging and independent review
gates still apply. A Go-only record cannot satisfy the generation/dashboard
requirements. Output is bounded, failed attempts remain intact, and a process
deadline or cancellation terminates the owned process group. This procedure
runs no comparative fleet benchmark, 24-hour test or publication operation.

The reader only inspects retained inputs. Its `consistent_pending_review` result
does not authenticate their origin or authorize publication. The private
assessment continues to record missing requirements and keep publication locked.

## Frozen worker verification

Prepare the approved frozen source and its private dependency cache using the
release dependency procedure. The extracted source omits `RELEASE.json`; use the
metadata from the same downloaded release assets. Keep output outside both the
source directory and dependency cache, and select a new filename for each run.

```sh
python3 -B scripts/release/worker_campaign.py \
  --release /private/assets/RELEASE.json \
  --source-dir /private/dependencies/source \
  --dependencies /private/dependencies \
  --go /path/to/go1.26.10/bin/go \
  --out /private/workers/worker-minimum.json

python3 -B scripts/release/worker_campaign.py \
  --release /private/assets/RELEASE.json \
  --source-dir /private/dependencies/source \
  --dependencies /private/dependencies \
  --go /path/to/go1.27.3/bin/go \
  --out /private/workers/worker-release.json

python3 -B scripts/release/worker_evidence.py \
  --release /private/assets/RELEASE.json \
  --campaign /private/workers/worker-minimum.json \
  --campaign /private/workers/worker-release.json \
  --out /private/assessments/worker.json
```

The recorder requires native Linux amd64, the pinned minimum or release compiler,
unchanged frozen inputs and `GOWORK=off`. It disables network module downloads,
uses readonly module resolution, verifies cached dependencies and retains the
SDK archive used by the application and worker. The release compiler instruments
both test executables for races; the minimum compiler runs without race
instrumentation. Both application and helper builds explicitly use `-pgo=off`,
`-trimpath`, `-buildvcs=false` and bounded compiler parallelism; ambient flags cannot
change this recipe. The outer recorder has a bounded process deadline and stops its
fixture process group on cancellation or deadline expiry.

The selected tests cover all 37 worker crash/capacity cases and five actual
minutes of mixed check/recovery/notification load at the default journal limits.
The reader requires original raw test and package completion, no skips, matching
helper compiler observations, independent target counts, progress across handler
categories, zero repeated invocations or unreconciled outcomes, and an empty
drained journal. Source fingerprints, executable compiler identity, native host
details, original module graphs and SDK payloads remain in private artifacts.

Check retention can return an explicit `workerCheckExpired` response before a
late result obtains its first receipt. The audit records that terminal response
with the original outcome and digest, without fabricating a receipt or counting
it as a successful result. `expired_check_receipts` counts previously accepted
checks subsequently retired; `expired_check_finalizations` counts results first
resolved by explicit expiry. Every retired check is submitted again against the
restarted server to confirm that exact expiry code. Generic missing, conflict or
gone responses do not satisfy reconciliation. Recovery and notification effects
still require their original committed receipts.

Go's JSON test output can split a long log line across several events. The
reader reassembles output separately for each test before decoding the load
measurement, so interleaved streams cannot join unrelated observations. Raw
output and reconstructed lines remain bounded; truncated payloads, duplicate
measurements and incomplete test/package completion still fail validation.

The reader executes no jobs and returns `consistent_pending_review` only for
internally consistent retained inputs. It does not authenticate those inputs or
authorize publication. Independent review, the complete build/security matrix,
native distribution execution and the other release gates remain separate.
Missing, interrupted, failed or stale campaigns cannot satisfy this check. This
procedure runs no fleet benchmark and no 24-hour test.

## Browser verification

### Dashboard accessibility

The locked browser tools include `axe-core` 4.13.0. Scans run the WCAG 2.0,
2.1 and 2.2 A/AA rules at the selected management, control, review and reader
checkpoints. Detected violations fail the check; incomplete findings remain
explicitly marked for manual review. Reports retain rule identifiers and
structural element locations, excluding DOM HTML, field values and generated
selectors that could contain private data. Rules are not disabled to obtain a
passing result.

The bounded frontend fixture serves the current embedded assets over loopback
TLS, uses fixed API responses, and checks native dialog modality, forward and
backward Tab navigation, safe initial focus, Escape during a pending conditional
write, unsaved-draft preservation, and the skip-to-main-content link. It also
scans monitor, endpoint, recipient, group and write-only secret forms, including
a mobile snooze dialog. Install the locked dependencies in
`scripts/dashboard/browser` and rebuild with `make dashboard-build` first.

```sh
CPRA_BROWSER_EXECUTABLE=/path/to/installed/chromium \
  node scripts/dashboard/accessibility_campaign.cjs \
  --out /private/frontend-accessibility-new
```

Omit the executable override to use Chromium installed by Playwright. The output
directory must be new. The run has a two-minute browser budget and retains failed
findings. Its report binds the frontend inputs and outputs and records actual
tool versions. This fixture executes no CPRa server, Raft, controller or provider
operation; it does not replace the real-server browser campaigns below. CI runs
it explicitly after installing the locked browser tools. The real-server
campaigns also run the scanner at selected checkpoints.

Automated scanning and Chromium keyboard checks do not establish complete WCAG
conformance. Screen-reader behavior, incomplete findings and the rest of the
manual accessibility requirements remain part of release review. The dialog
behavior follows the [WAI modal-dialog pattern](https://www.w3.org/WAI/ARIA/apg/patterns/dialog-modal/)
and [HTML dialog semantics](https://html.spec.whatwg.org/multipage/interactive-elements.html#the-dialog-element).

### Server browser verification

The required browser campaign runs seven scenarios through normal application
startup and one through the HTTP-server fixture. It checks management controls,
audited action review, collection import and continuation, role boundaries,
token handling, secret redaction, keyboard navigation, and selected mobile views.
It uses real local HTTP targets and Raft; it does not verify production provider
accounts or complete accessibility conformance.

Rebuild and verify the embedded dashboard with the pinned release tools before
running the campaign. Install the locked Playwright dependencies and Chromium
from `scripts/dashboard/browser`, or supply an explicitly selected installed
browser with `--browser`. The report records its actual version.

```sh
python3 -B scripts/dashboard/asset_manifest.py
python3 scripts/sdk/workspace.py --output /private/cpra-sdk.work --no-github-env
GOWORK=/private/cpra-sdk.work python3 -B scripts/dashboard/browser_campaign.py --go /path/to/go1.27.3/bin/go --node /path/to/pinned/node --out evidence/local/browser-release-default.json
GOWORK=/private/cpra-sdk.work python3 -B scripts/dashboard/browser_campaign.py --go /path/to/go1.27.3/bin/go --node /path/to/pinned/node --tags externaljobs --out evidence/local/browser-release-external.json
GOWORK=/private/cpra-sdk.work python3 -B scripts/dashboard/browser_campaign.py --go /path/to/go1.26.10/bin/go --node /path/to/pinned/node --no-race --out evidence/local/browser-minimum-default.json
GOWORK=/private/cpra-sdk.work python3 -B scripts/dashboard/browser_campaign.py --go /path/to/go1.26.10/bin/go --node /path/to/pinned/node --no-race --tags externaljobs --out evidence/local/browser-minimum-external.json
```

The development workspace above is explicit evidence of local source integration.
Official qualification uses the frozen dependency cache with `GOWORK=off`.
CI runs these four compiler/tag combinations and retains artifacts only in
private repositories.

The private release workflow runs `scripts/release/browser_record.py` against
the extracted source archive. It verifies the complete source inventory and
dependency-cache provenance before execution and checks that they remain
unchanged afterward. Git is unnecessary inside that extraction. For example,
after dependency preparation and installation of the locked browser tools:

```sh
python3 -B scripts/release/browser_record.py \
  --release /private/release/RELEASE.json \
  --source-dir /private/dependencies/source \
  --dependencies /private/dependencies \
  --go /path/to/go1.27.3/bin/go \
  --node /path/to/pinned/node \
  --module /private/dependencies/source/scripts/dashboard/browser/node_modules/playwright \
  --browser /path/to/installed/chromium \
  --race --out /private/browser/release-default.json
```

Record the other three compiler/tag combinations separately. The minimum
compiler uses `--no-race`; external builds add `--tags externaljobs`. The recorder
retains the original scanner package, script, archive, lock, dependency record
and individual scan files. It obtains the scanner from the fixed public npm
distribution URL without CPRa authentication, verifies the lock's SHA-512
integrity, and compares the installed package and script to that archive.
Matching a version string alone is insufficient.

Each output is created exclusively with a companion `<report-stem>.artifacts/`
directory. The directory retains the source hashes, raw Go JSON stream, stderr,
and seven required screenshots. A failed or interrupted run retains its available
record; an existing report or artifact directory is never overwritten or adopted.
Skipped tests, duplicate or contradictory terminal results, missing package
completion, missing screenshots, oversized output, and changed inputs cannot
produce a passing campaign. Reports always keep full release qualification false.

### Inspect retained browser evidence

For frozen-source campaigns run with `GOWORK=off`, preserve all four reports and
their companion directories, then inspect them against the original release
metadata:

```sh
python3 -B scripts/release/browser_evidence.py \
  --release /private/release/RELEASE.json \
  --campaign /private/browser/release-default.json \
  --campaign /private/browser/release-external.json \
  --campaign /private/browser/minimum-default.json \
  --campaign /private/browser/minimum-external.json \
  --out /private/browser/browser-evidence.json
```

The reader requires both compilers and tag selections, release-compiler race
detection, the recorded test command and tool digests, and the complete browser
input inventory derived from frozen regular source. It rereads bounded raw Go
events and rejects skipped, failed, duplicate, unexpected or missing completion.
The HTTP fixture's observed dashboard index and compiled Go version must match
the recorded source and compiler. Summary results must agree with that raw
stream. The retained logs and seven screenshots must match their recorded sizes
and hashes; unsafe paths, symlinks and inputs changed during inspection fail.

The output is created exclusively with mode `0600`; invalid attempts retain a
failure report. A consistent result is `consistent_pending_review`, with
`release_gate_passed=false`. Development-workspace results are rejected for this
frozen-source check. The reader does not authenticate report authors, establish
complete accessibility conformance, or independently prove every served browser
asset: those checks, independent review and the other release gates remain
required. No publication or signing operation runs here.

### Inspect retained accessibility evidence

`scripts/release/accessibility_evidence.py` first requires the same four frozen
real-server campaigns. It then verifies the original scanner distribution,
source-bound lock and bounded scan catalog. Required checkpoints cover sign-in,
secrets, conflicts, deletion, incident controls, unknown-action review and reader
pages, including mobile incidents and metrics. Raw DOM HTML and selectors
containing private attributes are rejected. Automatic violations remain open.

```sh
python3 -B scripts/release/accessibility_evidence.py \
  --release /private/release/RELEASE.json \
  --campaign /private/browser/release-default.json \
  --campaign /private/browser/release-external.json \
  --campaign /private/browser/minimum-default.json \
  --campaign /private/browser/minimum-external.json \
  --manual-review /private/review/manual-review.json \
  --out /private/review/accessibility-evidence.json
```

The optional manual review is a JSON object with `schema_version: 1`, the exact
`release_identity`, a reviewer identity, a timestamp with an offset, and
`full_wcag_conformance_claimed: false`. Its `campaign_sha256` array lists the four
original report hashes in sorted order. Each `cases` entry identifies one case
below, its `pass`, `fail` or `not_tested` outcome, the actual tools used, and a
`notes` record containing a relative file path, byte count and SHA-256 of the
original private review notes. Do not put credentials in review records.

| Case ID | Required observation |
| --- | --- |
| `keyboard-navigation` | Reach routes and controls using the keyboard. |
| `form-labels-instructions` | Understand monitor, endpoint, recipient, group and secret fields. |
| `validation-announcements` | Find and hear validation errors and their associated fields. |
| `dialog-focus-and-dismissal` | Enter, navigate, dismiss and leave confirmation/control dialogs. |
| `pending-write-focus` | Retain usable focus while a write is pending. |
| `conflict-and-unknown-announcements` | Identify version conflicts and uncertain outcomes. |
| `reader-navigation` | Navigate the read-only role's available views. |
| `monitor-controls` | Understand acknowledge, dismiss, snooze, disable and other supported controls. |
| `collection-import-errors` | Locate source-attributed import and validation problems. |
| `operation-progress-pagination` | Read progress, outcomes and paginated results. |
| `contrast-all-themes` | Review text/control contrast in every supported theme. |
| `zoom-and-reflow` | Use the interface at increased zoom and narrow widths. |
| `focus-visible-and-not-obscured` | Identify focused controls without obscured focus. |
| `screen-reader-announcements` | Review landmarks, headings, controls and status announcements with a screen reader. |

Every incomplete scanner finding also needs a `scan_reviews` entry identifying
the original campaign hash, scan hash and rule, with the same outcome, tools and
original-notes fields. A pass must record an actual review, not a planned one.
The reader does not authenticate the reviewer's identity; independent review
must establish that provenance. Notes and scans remain private retained inputs.

Omitting `--manual-review`, omitting a required case, or leaving a finding
unreviewed produces `incomplete` and exit code 2. Contradictory or altered inputs
produce `invalid_evidence` and exit code 1. Complete input consistency produces
`consistent_pending_review` and exit code 0, always with
`release_gate_passed=false`; it is not a full WCAG conformance claim. Existing
outputs are preserved. The private assessment accepts the optional review as
`components.accessibility.review`, or from the separately supplied
`accessibility-review/manual-review.json` artifact directory. Its absence cannot
turn an automatic scan into manual review.

## Native CLI verification

The five opt-in CLI cases execute a separately compiled `cpractl` against normal
application startup. They cover management and incident controls, diff,
collection application, input reselection and lost replies. Each CLI child uses
the compiler running its parent test. The child is CGO-free and uninstrumented;
`-race` instruments the parent/server. Targets and provider effects are isolated
local fixtures.

For the unpublished source workspace, select either supported test compiler and
prepare the SDK modules explicitly:

```sh
CPRA_SDK_WORKSPACE="$(pwd)/bin/cpra-cli.work"
python3 scripts/sdk/workspace.py --output "$CPRA_SDK_WORKSPACE"
GOWORK="$CPRA_SDK_WORKSPACE" GOTOOLCHAIN=local CPRA_RUN_CLI_INTEGRATION=1 \
  go test -race -count=1 -timeout=10m \
  -run '^TestMainManagement(RealCLI(Diff)?|Collection(ApplyCLI|ReselectionCLI|ReselectionCLILostReplies))$' .
GOWORK="$CPRA_SDK_WORKSPACE" GOTOOLCHAIN=local CPRA_RUN_CLI_INTEGRATION=1 \
  go test -race -count=1 -timeout=10m -tags=externaljobs \
  -run '^TestMainManagement(RealCLI(Diff)?|Collection(ApplyCLI|ReselectionCLI|ReselectionCLILostReplies))$' .
```

Ordinary application tests skip these cases unless the explicit invocation flag
is set. CI runs all five on both compilers and both server builds. The
[current availability](versions.md)
passes all 20 cases, with no skips or changed captured inputs. Frozen release
qualification still requires `GOWORK=off`, the matching dependency cache and
final installed artifacts; this development workspace cannot satisfy that gate.

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
with identical Go version, build tags and optimized build flags. Preparation uses
the recorded release recipe, currently Go 1.27.3, with symbols and DWARF retained,
PGO disabled, `GOTOOLCHAIN=local` and `GOWORK=off`. Both builds come from committed
source archives; ignored developer files and workspace settings are excluded.
The physical-space check runs before archives, downloads or compilation.

Use a checksum-verified compiler installation and an explicitly selected isolated
module cache containing the frozen dependency versions. Private SDK archive/cache
qualification remains an outstanding release prerequisite; preparation does not
create replacement directives, checksum exceptions or unpublished module versions.
It refuses a dirty checkout, an existing output directory, changed source inputs,
or the wrong compiler. Keep the output and module cache outside the checkout and
separate from each other.

```sh
python3 scripts/benchmark/prepare.py --manifest /path/to/release/RELEASE.json --go /path/to/go1.27.3/bin/go --module-cache /path/to/qualified-module-cache --out /path/to/prepared-builds
```

The default tag set is the release recipe's built-in drivers. An explicit `--tags`
selection applies identically to both CPRa builds and the target server. The
schema-3 `builds.json` records the frozen release metadata/identity, all three
executable digests, source archive and tree digests, compiler identity, source
dates and effective build settings. The candidate and target receive the exact
release version/commit/source-date linker fields; the baseline retains its
published source's default identity fields. Compiler optimizations, driver tags,
CPU baseline and instrumentation settings are the same. Prepared source must
match the release inventory, and the final private assessment compares the
benchmarked controller bytes with the native archive. Older preparation reports
without this binding cannot qualify the current release.
Campaigns verify the actual target binary as well as CPRa; the target must come
from the same candidate source. Older metadata without these identities must be
regenerated. Recorded hashes provide integrity checks, not signed provenance or
proof that a performance campaign passed.

```sh
python3 scripts/benchmark/campaign.py --mode preflight --out evidence/local/preflight
python3 scripts/benchmark/campaign.py --mode compare --builds /path/to/prepared-builds/builds.json --candidate /path/to/prepared-builds/candidate --baseline /path/to/prepared-builds/baseline --target /path/to/prepared-builds/target --out evidence/local/comparison
python3 scripts/benchmark/campaign.py --mode faults --builds /path/to/prepared-builds/builds.json --candidate /path/to/prepared-builds/candidate --target /path/to/prepared-builds/target --out evidence/local/faults
python3 scripts/benchmark/campaign.py --mode soak --builds /path/to/prepared-builds/builds.json --candidate /path/to/prepared-builds/candidate --target /path/to/prepared-builds/target --out evidence/local/soak
```

Each run creates a private, random API token file for the baseline or candidate.
API reads, including every history page, use the bearer header. Target-server
requests receive no API credentials. The loopback HTTP clients reject redirects
and ambient proxies, cap response bodies at 4 MiB, and use a ten-second socket
timeout. This timeout is not a total deadline for a peer that continually sends
small amounts of data. Startup retries connection failures and HTTP 503;
authentication errors, invalid responses and redirects stop the run.

The target has 30 seconds to report its bounded startup address. Cleanup records
process-stop failures and removes the token after CPRa has stopped. An incomplete
trial stops the whole comparison or fault campaign, preserving its completed
trials and failed run instead of starting more work. No automatic pass is issued.

Comparison uses 10,000, 100,000 and 1,000,000 distinct monitor configurations,
60-second cadence, five-minute warmup, fifteen-minute measurement and three
repetitions of each build. Baseline internal percentiles remain unavailable.
An optional operator-invoked soak mode remains available in the harness, but the
24-hour run is outside this task and is not required. No endurance claim follows
from comparison or short-run evidence. The target performs real network I/O with per-monitor bounded counters
and digests. Samples record rate, CPU, RSS, descriptors, worker/queue statistics,
SLOs, persistence timing and bounded dashboard reads. Logs rotate; individual
check histories are not retained. Sleep, process interruption and missing fleet
rows produce an incomplete result.

`--mode smoke` is a ten-monitor harness check and is explicitly excluded from
release evidence. Campaign output remains `measured_pending_review` until
resource stability, faults, operation reconciliation and all required evidence
have been reviewed. It never promotes a smoke run to scale or endurance proof.
The release gate must not pass while required provider or comparative evidence is
missing. Source and documentation publication must identify the same reviewed
candidate commit.

Use a new output directory for every campaign, including preflight-only attempts.
Reusing one is rejected before its earlier evidence can be overwritten. Recovery
reconciliation compares each of the bounded intervention cohort's monitor counts
with committed history; matching fleet totals alone cannot establish success.
Repeated cursors, duplicate events, conflicting terminal records and events for
another monitor stop the audit. Baseline throughput is reported as measured;
the candidate's 99% cadence requirement is not imposed on the baseline.

### Verify retained campaign evidence

After the comparison and fault scenarios finish, set `CPRA_CANDIDATE_COMMIT` to the full approved commit
used by preparation and read back the retained evidence:

```sh
python3 scripts/benchmark/verify.py --builds /path/to/prepared-builds --comparison evidence/local/comparison --faults evidence/local/faults --candidate-commit "$CPRA_CANDIDATE_COMMIT" --out evidence/local/benchmark-verification.json
```

This offline command requires the original prepared executables, source archives,
source inventories, build metadata, all 18 comparison runs and five fault runs.
It checks fixed monitor counts and cadence, recorded
physical-host space, consistent host/build identities, non-overlapping trials,
complete warmup and measurement samples, declared fault timing and recovery
coverage, exact SLO threshold counters, target saturation, per-monitor action
reconciliation, bounded dashboard reads, snapshot/history activity and cleanup.
The baseline's unavailable internal percentiles remain explicitly unavailable.
Comparison duration cannot be replaced by a smoke result. Without an explicitly
supplied `--soak` directory the reader reports endurance evidence as absent and
does not require or qualify it. Supplying an existing directory reads evidence;
it never starts a workload.

JSON and decompressed samples are bounded. Duplicate JSON fields, non-finite
numbers, truncated compression, missing observations and changed files are
errors. The output records the hashes of consumed evidence plus initial, final
and peak resource observations. Each invocation requires a new report filename.

An exit code of zero means `consistent_pending_review`, with
`release_gate_passed: false`. It establishes consistency of recorded observations,
not trusted provenance, resource stability, queue drainage or independent review.
Inspect those remaining conditions against the retained sample streams and host
records, and bind the measured source and executables to the final release
artifacts. The aggregate publication gate remains separate and locked. These
reader tests use synthetic report fixtures and do not establish actual scale or
endurance results.

The current [comparative verification procedure](validation.md)
uses local HTTP, Git and controlled compiler/process fixtures. Actual preparation
of the frozen candidate and comparative runs remain unqualified. No 24-hour
run is required or authorized by this guide.

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
trigger it. The repository's normal workflow runs isolated HTTP/file, notification
contract and local protocol operations without account credentials. Live results
are not automatically published.

`--mode faults` runs burst release, slow targets, outage, recovery traffic and
process-kill/restart campaigns. The soak introduces a 100-monitor outage cohort
for 125 seconds approximately hourly, recording those windows and their recovery
periods. Healthy SLO assertions exclude these declared fault windows; all samples
remain in the evidence. The target tracks repeated intervention per incident
phase; retained history is reconciled against target-side effects. Heap,
goroutines, disk/history/snapshot sizes and target saturation are recorded in
addition to process and controller measurements.
