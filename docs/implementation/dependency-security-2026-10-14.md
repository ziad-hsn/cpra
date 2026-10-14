# Compiler and dependency security repair — 14 October 2026

Scope: private development checks after source checkpoint
`73d479794fb3112db0dd53c3920c6d8ade068fba`. This is not frozen release
qualification, a reproduced exploit, or permission to publish.

## Common minimum and HTTP/2 repair

The user approved **Go 1.26.0 as the common minimum for every repository Go
module** on 14 October. The application, SDK, worker, integration examples,
generator, module-archive tool and A2A reference fixture now use that minimum.
The release compiler remains Go 1.27.2. The release recipe records both values;
compiler matrices, evidence readers and current installation documentation use
the common minimum. A contract test checks all seven module declarations and
the six official minimum-compiler archive pins against that policy.

The application and integration examples now select `x/net v0.60.0` and its
required dependency versions, including `x/text v0.42.0`. Go recalculated the
private SDK archive and module-definition checksums after its `go.mod` changed;
the preparation tool now refreshes both explicitly, while preserving unrelated
checksum entries. Public module verification remains enabled. An independent
reviewer verified the private archive contents, all four SDK/worker checksum
values, selected public module checksums and official compiler archive pins.

Nine fresh source/symbol scans with Go 1.27.2 and `govulncheck v1.7.0` report no
affected imported-package or called-symbol findings: application default,
all built-in drivers, `externaljobs`, combined tags, SDK default/tagged, worker
tagged and examples default/tagged. The five reachable HTTP/2 findings from the
earlier checkpoint are absent. The all-built-in and combined application
selections retain GO-2026-5932 at required-module level: `x/crypto` is present,
but its affected OpenPGP packages are not imported. The advisory has no fixed
version; it is not suppressed. See the
[upstream OpenPGP advisory](https://pkg.go.dev/vuln/GO-2026-5932).

The scan report and original JSON streams are retained privately. Their
database timestamp is `2026-10-08T22:31:09Z`; the scan wrapper log SHA-256 is
`962dec289c3c0080cedb6184e1839cae971d5817346bdc49569cf9cc955e2ce0`.
These are working-source development results, not final frozen-commit release
qualification or proof of exploitation. Historical checks below retain their
original compilers and dependency graphs.

### Verification of the common minimum

Both Go 1.26.0 and Go 1.27.2 pass the SDK default/tagged suites (943/1,085
test and subtest pass events), the tagged worker suite (126), and the jobs,
authentication and HTTP-server suites in default/tagged builds (684/852).
Each server suite excludes the opt-in browser campaign; this is recorded as
one skip per build and does not qualify browser behavior. These are ordinary
Linux/WSL amd64 builds, not a new race or native-platform matrix.

Both compilers also pass the examples in default/tagged builds (105/135) and
the module-archive tool's eight tests. The A2A reference fixture compiles on
both compilers and has no package tests; this does not
establish agent interoperability. SDK regeneration from two separate source
roots matches all ten checked-in outputs on each compiler. The dashboard
rebuild passes 541 tests, type checking and lint; its embedded assets and
notices match their input/output inventory.

The preparation tests pass all 48 cases, including changed SDK archive and
module-definition checksums. The 15 release-policy, 39 documentation and
48 benchmark-tool contract tests pass. The complete release-tool suite passes
703 tests after correcting its negative compiler fixture to reject Go 1.25;
the first stale-fixture failure remains retained.

Additional unsuccessful attempts remain separate evidence. The initial cache
copy mistakenly omitted source-controlled `Gopkg.lock` files, and Go's module
verification rejected it. The corrected copy retains those files and passes
verification. Running the generator dependency's own tests fails because its
published module omits the test's external example fixture; CPRa's two-root
schema regeneration provides the direct generator check. Two concurrent
Go 1.27.2 checks were stopped by the scratch guard during compilation. Their
completed SDK/worker subcommands remain distinguishable from the interrupted
examples/application commands. After task-cache cleanup, the application
default/tagged suites and remaining examples/tool checks pass sequentially;
the original interrupted reports are not rewritten as passes.

The independent review verifies all seven module declarations, six official
compiler pins, final SDK/worker archive contents and checksums, 188 dashboard
inputs, 43 outputs, and all nine scanner streams. It found no actionable source
defect. This finite review did not execute tests or qualify a frozen release.

## Earlier findings and selected updates

A retained `govulncheck v1.7.0` source/symbol scan of the default application
build found 14 distinct advisories with called-symbol traces. The scan used
Go 1.27.1 and the database last-modified timestamp
`2026-10-08T22:31:09Z`. Its JSON-mode exit code was zero; the original finding
messages, rather than that exit code, establish the failed security gate.

The repair updates the release recipe, six official compiler archive checksums,
workflow compiler selections and developer image index to Go 1.27.2. The
browser parser, embedded dashboard and dependency notices are regenerated with
that compiler. Historical evidence remains labeled with its actual compiler.
The application updates gRPC to `v1.83.2` and `x/text` to `v0.41.0`, with the
versions selected by their dependency graph. The integration examples also
update `x/text` to `v0.41.0`. Public module checksum verification remains enabled;
no dependency replacement or vulnerability suppression is added.

The direct gRPC receive-path finding reaches CPRa's production health RPC. The
new patch contains the upstream fragmentation/OOM fix. Other scanner traces are
conservative reachability evidence: server, client and reverse-proxy advisories
have different prerequisites and are not five demonstrated attacks on CPRa's
management listener. Sources: [Go 1.27.2 release history](https://go.dev/doc/devel/release#go1.27.2),
[gRPC fragmentation advisory](https://github.com/grpc/grpc-go/security/advisories/GHSA-vp52-pcj8-j9qc),
[gRPC 1.83.2](https://github.com/grpc/grpc-go/releases/tag/v1.83.2) and
[`x/text` advisory](https://pkg.go.dev/vuln/GO-2026-6629).

## Earlier compatibility decision, now resolved

At the earlier checkpoint the application declared Go 1.25.0. The updated gRPC dependency selected
`x/net v0.58.0`, which is the latest inspected upstream release supporting
Go 1.25. A fresh Go 1.27.2 default-build scan removes the compiler/gRPC findings
but still reports called-symbol findings for GO-2026-6603, GO-2026-6610,
GO-2026-6611, GO-2026-6612 and GO-2026-6617 in `x/net`.

The upstream fixes require `x/net v0.60.0`, whose module declares Go 1.26.0 and
selects `x/text v0.42.0` and `x/sys v0.48.0`. An independent reviewer checked
upstream tags and the Go proxy metadata and found no published patched
Go 1.25-compatible release. The original plan preserved Go 1.25, so this change
required the user's decision. The subsequent approval applies Go 1.26.0 to all
modules; the proposed split application/SDK minimum was rejected and is not the
implemented policy.

Sources: [HTTP/2 advisory](https://pkg.go.dev/vuln/GO-2026-6611),
[`x/net v0.58.0` module](https://github.com/golang/net/blob/v0.58.0/go.mod),
[`x/net v0.60.0` module](https://github.com/golang/net/blob/v0.60.0/go.mod) and
[upstream tags](https://go.googlesource.com/net/+refs/tags).
Source compatibility does not extend [Go's security support period](https://go.dev/doc/devel/release#policy).

## Verification and retained failures

- The complete jobs, authentication and HTTP-server package suites pass on
  Go 1.27.2 and Go 1.25.0, including real local gRPC health and notification
  receiver paths. The minimum-compiler run retains a metadata correction:
  its wrapper initially labeled the PATH compiler instead of the explicit
  Go 1.25.0 executable in the recorded command. That annotation is separate
  from the original record and is not a second test run.
- SDK base/tagged and worker tagged tests pass with race detection on
  Go 1.27.2. Separate source/symbol scans of those three module selections
  produce zero findings. These checks use `GOWORK=off` and the private prepared
  SDK cache; they do not prove public module downloads.
- The integration examples pass their base tests on Go 1.27.2 and their
  external-worker tests on Go 1.27.2 with race detection and Go 1.25.0 without
  race instrumentation. Their first dependency-update attempt fails at the
  public proxy because the SDK is unpublished. The successful attempt uses the
  existing private proxy, with checksum exceptions limited to the two private
  modules. The reviewer confirms their recorded checksums and unchanged SDK
  sources. These are local fixture tests; the examples still select an affected
  `x/net` version and have no full security or live-provider qualification.
- The full dashboard rebuild passes 541 tests, type checking and lint. A
  second parser generation matches the first byte-for-byte; the embedded
  asset freshness check passes.
- The first 702-test release-tool run fails two negative fixtures because
  their formerly different compiler value became the selected compiler.
  Those fixtures now derive a different patch version. Their focused
  regressions pass; the complete rerun passes all 703 tests after adding the
  security-order regression. The original failure log is retained.
- The security gate now executes after dependency verification and before
  the long correctness matrix. A regression first fails against the old
  order and passes after the change, confirming that a security rejection
  retains a failed report without starting the matrix.
- The 47 packaging-tool contract tests pass. They are separate from executing
  packages, images or a Kubernetes deployment.

An independent reviewer checked the compiler checksums against official Go
metadata, the developer-image index against the official registry, updated
public module checksums, all 188 dashboard inputs and 43 outputs, the parser
and notice bytes, and the failure ordering. That finite review found no new
actionable issue. It did not run tests or establish native/container execution,
and it did not close the remaining HTTP/2 security gate.

The earlier `73d4797` matrix remains incomplete. Its scratch supervisor treated
a disappearing test directory as a failed disk-usage command and terminated
the child. The supervisor now tolerates concurrent directory removal. The
original report/logs retain 4,345 test-pass events, 14 skips and no observed
test failure before termination; that prefix is not a complete matrix pass.

Release/compiler, native, provider, deployment and comparative-capacity gates
remain open. The 24-hour endurance run remains cancelled. No public promotion
is performed by this repair.

## Isolated tools and reference fixture

A subsequent scan at `ddc2210` covers the separately pinned build tools and
A2A reference fixture, which are outside the application's module traversal.
The scans use Go 1.27.2 and `govulncheck v1.7.0`; their evidence remains private.
No module versions or suppressions change in this follow-up.

| Selection | Scan | Result |
| --- | --- | --- |
| `internal/jobs/testdata/a2aagent` | Required modules | Zero findings; this is not agent interoperability or provider-effect evidence. |
| Pinned `oapi-codegen` command in `tools/sdkgen` | Source/symbol | Five required-module advisories: GO-2026-6095, GO-2026-6112, GO-2026-6179, GO-2026-6180 and GO-2026-6629. No affected imported-package or called-symbol findings in this command. |
| `tools/modulezip`, including tests | Source/symbol | Two required-module advisories: GO-2026-6179 and GO-2026-6180. No affected imported-package or called-symbol findings in this tool. |

The generator's initial invocations failed because its module root contains no
Go package and because module-only scanning does not accept package patterns.
Those invocations are retained as unsuccessful attempts; the command-specific
source scan above supplies the reachability assessment. JSON exit status alone
does not establish an empty finding inventory. Those finite tool results did not
close the application's then-reachable HTTP/2 findings. The later application
scans above assess the repaired graph separately; a frozen release remains
unqualified.
