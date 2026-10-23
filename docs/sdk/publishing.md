---
title: Prepare the SDK for Go packages and publication
description: Prepare the SDK for Go packages and publication.
cpra_scope: sdk
---

# Prepare the SDK for Go packages and publication

This guide applies the official Go package-documentation and module-publishing
rules to CPRa. It distinguishes documentation that we can verify locally from
public module downloads and hosted documentation that require published tags.

**Current status:** both SDK modules are unpublished candidates. The private
working branch has real TLS/Raft integration for v2 resource management,
controls, action review, operation reads and observations. Resumable collection
application and external-worker server integration are implemented and locally
tested. Frozen module installation and complete release qualification remain
open. See the [current availability](../versions.md)
for the exact executed scope. Earlier published servers are not qualified by
working-branch tests. Candidates stay private until every agreed gate passes;
improving a README or passing a fixture does not complete that gate.

## Understand the modules

| Module | Importable packages | First candidate tag in this repository |
| --- | --- | --- |
| `github.com/ziad-hsn/cpra/sdk/go` | `cpra`, `api`, `collection`, `collection/commitment` | `sdk/go/v0.1.0-rc.1` |
| `github.com/ziad-hsn/cpra/sdk/go/worker` | `worker`, with `externaljobs` | `sdk/go/worker/v0.1.0-rc.1` |

These are libraries. A consumer adds a published version with `go get`; `go
install` installs the separately distributed `cpra` and `cpractl` commands. The
worker has its own dependency graph and version. It imports the core SDK, and the
core SDK never imports the worker or the application. The examples module owns
AWS and Kubernetes dependencies.

Go requires a subdirectory prefix on tags for a module below the repository root.
An application tag such as `v0.1.0` does not publish either nested SDK module.
The HTTP path `/api/v2` does not require a `/v2` Go import suffix; that suffix is
about the module's own semantic major version. See the
[Go module version rules](https://go.dev/ref/mod#vcs-version) and
[major-version suffix rules](https://go.dev/ref/mod#major-version-suffixes).

## Write documentation that ships with the library

Each public package has one `doc.go` overview. It starts with `Package <name>`,
explains the package's purpose and lifecycle, and links to relevant exported
symbols. Exported declarations explain their inputs, results, limits, version
requirements, and ownership where applicable. This makes the documentation
available in editors and `go doc` as well as on pkg.go.dev. These conventions
follow the [Go doc-comment guide](https://go.dev/doc/comment).

The module-root README gives installation status, imports, a usable first example,
and links to deeper explanations. It must remain useful when the reader has only
the module ZIP. Repository-only generation and integration commands are explicitly
identified; tools, the application, and `examples/sdk` are not dependencies a
library consumer must download to build their own program.

Both module roots contain the project's MIT `LICENSE`. Include it in the actual
module payload; a repository-root license alone is not the evidence checked here.
pkg.go.dev applies license recognition before displaying documentation. Its
published-version page must be inspected after indexing; local file-presence
checks do not prove the hosted service's classification.
[pkg.go.dev documentation and licenses](https://pkg.go.dev/about).

## Keep code examples checked

Executable documentation belongs in external-test packages such as `cpra_test`,
using public imports. `ExampleNew`, `ExampleMonitorsService_Get`, and similar
functions attach examples to Go documentation. An `Output:` comment makes Go run
the example and compare the result; examples without one compile but do not run.
We label network usage snippets that intentionally have no output assertion.
The runnable examples use finite local fixtures or pure construction and never
require an account or contact a production service.
[Testable examples in Go](https://go.dev/blog/examples).

Run these commands from a repository checkout with Go 1.26.10 or Go 1.27.3 (or a
newer supported, patched release) on `PATH`. See [compiler compatibility](../versions.md).

```bash
export GOTOOLCHAIN=local
python3 scripts/sdk/workspace.py --output /tmp/cpra-sdk.work
export GOWORK=/tmp/cpra-sdk.work
go doc github.com/ziad-hsn/cpra/sdk/go
go doc github.com/ziad-hsn/cpra/sdk/go/collection
go test -count=1 -run '^Example' -v ./sdk/go/...
go test -count=1 -tags=externaljobs -run '^Example' -v ./sdk/go/...
go test -count=1 -tags=externaljobs -run '^Example' -v ./sdk/go/worker/...
```

The four [integration lessons](index.md) explain larger programs through file maps,
source excerpts, request flows, expected output, and developer exercises. Their
canonical READMEs live under `examples/sdk`. Run `python3
scripts/sdk/sync_guides.py` after editing them; the documentation-site copies and
linked source assets must match. Run `python3 scripts/sdk/reference.py` after
changing public declarations or schemas.

## Preserve the optional-worker build boundary

Every worker Go file, including `doc.go` and its examples, requires `externaljobs`.
The core SDK selects mutually exclusive generated model and transport variants.
Do not add an untagged worker package or public custom-job stub to improve its
appearance in a documentation index. Untagged consumers must fail to compile
custom-job references.

The hosted pkg.go.dev service chooses supported OS/architecture contexts, rather
than arbitrary application build tags. Its current package loader does not supply
`externaljobs`. Consequently, expect hosted symbol documentation for the default
core SDK; use the worker README and the
[tagged Go reference](go-reference.md) for the extension. This conclusion follows
the documented [pkg.go.dev build contexts](https://pkg.go.dev/about#build-context)
and the official [package loader source](https://go.googlesource.com/pkgsite/+/master/internal/fetch/load.go).

The `go doc` command also does not apply `GOFLAGS=-tags=externaljobs` to its
documentation selection in the earlier Go 1.25/1.27 development checks. The archive check
therefore uses the standard-library `go/doc` renderer on the exact source files
selected by `go list -tags=externaljobs`. This validates tagged overviews locally
without pretending that hosted pkg.go.dev will display those symbols.

Source files with build tags remain in downloaded module archives. Exclusion means
they are absent from the selected packages and exported APIs, not hidden source.
The server must independently require its build tag, runtime enablement, and
authorization. See [Go build constraints](https://pkg.go.dev/cmd/go#hdr-Build_constraints).

## Prepare dependency changes before freezing source

The application and worker must resolve the exact SDK module bytes that will
ship. A development workspace can hide missing checksums or module declarations.
Prepare those declarations before selecting the final release commit:

```bash
CPRA_PREPARATION=/absolute/path/to/a/new-private-directory
python3 scripts/sdk/prepare_dependencies.py \
  --go /absolute/path/to/go \
  --worker-version v0.1.0-rc.1 \
  --out "$CPRA_PREPARATION"

python3 scripts/sdk/verify.py \
  --go /absolute/path/to/go \
  --sdk-version v0.1.0-rc.1 \
  --worker-version v0.1.0-rc.1 \
  --prepared "$CPRA_PREPARATION" \
  --out "$CPRA_PREPARATION/consumer-release.json"
```

Select versions explicitly and repeat consumer verification with Go 1.26.10 using
a different output name. The preparer captures current tracked and nonignored
source files into a private directory outside the checkout. It rejects links,
private dotenv inputs, changed source, module replacements and version drift.
Go resolves dependencies and generates the proposed `go.mod` and `go.sum` edits;
`module-files.diff`, command logs, original input hashes and canonical module
payloads remain available for review. The checkout is not changed by this command.

Preparation runs `go mod tidy` followed by the complete dependency-graph download
before archiving each module. The SDK is prepared first because its final files,
including `go.sum` and README content, determine the checksums used by the
application and worker. Frozen dependency preparation must then leave all three
modules unchanged.

If an **unpublished private candidate** has changed SDK files while its dependent
checksum still identifies earlier contents, rerun the preparer with
`--refresh-sdk-checksum`. This explicitly removes only the stale SDK content row
from the copied application and worker checksum files. Go generates its
replacement from the source-derived archive. The previous rows, archive identity
and resulting diff remain in the preparation report; the SDK's module-definition
checksum and unrelated entries retain normal verification. Without this option,
a checksum mismatch remains an error. Never use this procedure to change the
contents of a published module version; select a new version instead.

Apply reviewed proposals only while their original file hashes still match.
Any subsequent SDK source change requires new dependency preparation because
module checksums cover its complete selected payload. The preparation report is
`working_tree_dependency_preparation`; consumer reports made with `--prepared`
are `working_tree_module_archive`. Neither is accepted as frozen release or
public-download evidence. `--prepared` cannot be combined with `--published`.

Go versions may compress identical module contents into different ZIP bytes.
The consumer verifier requires matching Go content and `go.mod` hashes across
compilers, matching ZIP bytes under the same compiler recipe, and exact agreement
between each staged ZIP and its corresponding download.

## Verify the distribution before publishing

From a clean committed checkout, the archive verifier stages the candidate modules in a private temporary proxy,
then creates an external consumer with `GOWORK=off`, no `replace`, and a fresh
module cache. Source selection uses the commit archive and Go's upstream module
ZIP implementation, with each nested module staged separately. The verifier checks:

- Every downloaded name and byte matches the selected candidate module payload.
- README, license, package overviews, and executable example sources are included.
- The client, API model and collection package overviews render in default and tagged builds;
  the worker overview renders in its tagged build.
- Documentation examples execute from the downloaded modules, without borrowing
  the repository's fixtures or implementation packages.
- Default and tagged consumers compile as intended; forbidden worker APIs fail
  to compile without the tag and default dependencies exclude application internals.

From the repository root:

```bash
python3 scripts/sdk/verify.py --go /path/to/go --out evidence/local/sdk-consumer.json
python3 scripts/sdk/regenerate_check.py --go /path/to/go --out evidence/local/sdk-generation.json
```

Repeat with the minimum Go 1.26.10 compiler and the selected release compiler. The
normal SDK CI matrix runs these checks and the full default/tagged tests, race
checks, and vet. The generator check rebuilds outputs in two independent source
roots. It includes the postprocessing that gives the public `api` package one
handwritten overview while preserving generated-code markers.

With `--out`, the consumer verifier creates the report exclusively and retains
its module inputs in an adjacent directory, such as
`sdk-consumer.json.modules/`. For each module it keeps both the source-derived
and Go-downloaded `.zip`, `.mod`, and `.info` files, with byte counts and SHA256
digests in the report. On Unix the report and payload files are mode `0600`, and
the payload directory is mode `0700`. Failed verification retains the failure
report and any payloads already copied. Choose a fresh output name for a new
campaign; existing reports or payload directories are never adopted or replaced.

After preparing the private dependency cache and completing both compiler runs
for the same clean source and explicitly selected SDK/worker versions, inspect
their retained inputs:

```bash
python3 scripts/release/sdk_evidence.py \
  --release /absolute/private-release/RELEASE.json \
  --dependencies /absolute/private-dependencies \
  --consumer /absolute/private-evidence/consumer-go1.26.10.json \
  --consumer /absolute/private-evidence/consumer-go1.27.3.json \
  --out /absolute/private-evidence/sdk-consumer-evidence.json
```

The reader checks both compiler identities, the frozen source inventory, the
application/worker dependency versions, downloaded Go-reported checksums,
documentation/example coverage, and every retained payload. ZIP compression may
differ, but distributed filenames and file contents must match. Input sizes are
bounded, unsafe/duplicate paths are rejected, and inputs are hashed again before
the consistency result is issued. The archive limits follow the
[Go module ZIP contract](https://pkg.go.dev/golang.org/x/mod@v0.38.0/zip).

Its successful result is `consistent_pending_review`, with
`release_gate_passed: false`. It does not qualify server interoperability, replace
the full SDK/worker tests and generation checks, establish public downloads, or
open the publication gate. The private release workflow retains the payloads
and runs this reader after both compiler jobs. Those workflow paths and reader
fixtures are locally tested; execution against the final frozen candidate
remains required.

A completed private-proxy run proves consumption of the selected committed payload. It does not
prove a public proxy accepted the tag, pkg.go.dev
rendered it, or a real v2 server honored the contract. Preserve those boundaries
in [verification reports](verification.md).

## Publish only after the remaining gates pass

Use the following sequence when the candidate passes its existing implementation,
security, platform, licensing, and actual-server acceptance gates:

1. Review the public API, examples, OpenAPI inventory, dependency notices, and
   compatibility record. Record the exact clean source commit. Run module tidy
   and dependency verification under the selected toolchain; investigate changes
   to `go.mod` or `go.sum`, then commit approved changes and rerun checks.
2. Publish the core SDK's reviewed prerelease tag first. Never move or reuse a tag
   after it has been published. Go's module ecosystem treats versions as immutable.
   Use a new version for a correction. Follow the
   [official publishing procedure](https://go.dev/doc/modules/publishing).
3. Download that exact core version from the public proxy into an external
   project. Verify its archive contents, examples, dependencies, and compatibility.
4. Verify the worker against that published core dependency with `GOWORK=off`;
   then publish the worker's independently versioned prerelease tag.
5. Run the public-download verifier below against the exact candidate checkout.
   Inspect the core SDK's versioned pkg.go.dev pages, package summaries, examples,
   source links, and license classification. Verify worker documentation through
   its distributed README and tagged tools. Indexing may take time.
6. Publish an application version that requires the verified SDK dependencies only
   after their versions resolve publicly. Promote to `v0.1.0` only after prerelease
   qualification. Keep the SDK, worker protocol, journal format, and server API
   compatibility matrix tied to the actual artifacts.

This is the command to use **after both candidate tags exist**; it is expected to
fail while they remain unpublished:

```bash
python3 scripts/sdk/verify.py --published --go /path/to/go --out evidence/local/sdk-published.json
```

The verifier defaults to `v0.1.0-rc.1` for each module. Set `--sdk-version` and
`--worker-version` explicitly when qualifying other versions; the worker's
dependency must match the selected SDK version. Select versions and consumer
requirements before verification; substituting `@latest` could test a different
candidate. The [module qualification](publishing.md)
records unexecuted Go-tool and consumer gates for the latest local implementation.

After publication, a new consumer project can add the library as follows:

```bash
mkdir cpra-integration
cd cpra-integration
go mod init example.com/team/cpra-integration
GOWORK=off GOTOOLCHAIN=local GOPROXY=https://proxy.golang.org go get github.com/ziad-hsn/cpra/sdk/go@v0.1.0-rc.1
```

An external-worker application additionally adds
`github.com/ziad-hsn/cpra/sdk/go/worker@v0.1.0-rc.1` and uses
`go build -tags=externaljobs ./...`. Core read and management applications need no
worker dependency. Downloading a published version through the proxy can also
trigger pkg.go.dev indexing; indexing itself is not a compatibility test.
[Adding packages to pkg.go.dev](https://pkg.go.dev/about#adding-a-package).

No tags, releases, or public-index requests are created by the local checks above.
Production-provider verification and comparative capacity qualification remain
separate from SDK packaging and documentation evidence.
