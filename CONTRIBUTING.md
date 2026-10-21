# Contributing to CPRa

Discuss substantial behavior or public API changes in an issue before opening
a large pull request. For a bug, provide a minimal reproduction and the source
commit, build tags and environment. Keep credentials and private infrastructure
out of reports; vulnerabilities follow [SECURITY.md](SECURITY.md).

## Development

Use Go 1.26.10 or Go 1.27.3 (or a newer supported, patched release). Go
1.27.0–1.27.2 are affected by
[GO-2026-6809](https://vuln.go.dev/ID/GO-2026-6809.json), even though they satisfy
the common `go 1.26.10` module minimum. Official artifacts use the exact
compiler and build recipe in `scripts/release/recipe.json`. The dashboard uses
the pinned Node and pnpm versions in the same file. Do not change dependencies
or generated files just to make an unrelated change pass.

From the repository root:

```sh
make dev-workspace
make test-fast
make fmt-check
make vet
make test
make sdk-check
```

The development workspace composes the application, SDK and worker modules
locally. Root `go test ./...` does not test nested modules. `make sdk-check`
checks the SDK and the tagged worker module separately. Local workspace success
does not establish that versioned modules have been published or downloaded.

Make also needs Python 3. Release tools pin their own versions; use the exact
Node/pnpm versions from `scripts/release/recipe.json` for frontend work. Race
checks need a supported native C compiler, even though production binaries use
`CGO_ENABLED=0`. Documentation dependencies are pinned in the separate site
checkout's `_sources/requirements.txt`.

For direct Go commands or an editor, select the generated workspace explicitly:

```sh
make dev-workspace
export GOWORK="$PWD/bin/cpra-sdk.work"
go test ./internal/loader
# Launch your editor from this environment, or set its Go tools' GOWORK
# environment to the absolute path printed by make dev-workspace.
```

Restart the editor's Go language server after changing its environment. Configure
its build tags explicitly when working on `externaljobs` or optional built-in
files. Keep local workspace/editor files uncommitted; do not add `replace`
directives to distributed modules. Go's [workspace reference](https://go.dev/ref/mod#workspaces)
and [gopls workspace guide](https://go.dev/gopls/workspace) explain these settings.
To include integration lessons, generate a separate workspace with
`python3 scripts/sdk/workspace.py --examples --output bin/cpra-examples.work`
and select that absolute path only for those examples.

`test-fast` covers pure settings, manifests, projections, SLO/path/version helpers
and SDK collections without race instrumentation. It does not replace the full
matrix above. Release and downloaded-consumer verification use `GOWORK=off` and
qualified published dependencies; local workspace success does not satisfy that
gate or justify publishing tags early.

For frontend work:

```sh
cd dashboard
pnpm install --frozen-lockfile
pnpm exec tsc --noEmit
pnpm lint
pnpm test
cd ..
make dashboard-build
make docs-check
```

Rebuild embedded assets with their sources and notices. Remove local dashboard
dotenv files before a release build; credentials are runtime configuration, not
frontend inputs. API changes also require regenerated base/tagged contracts and
the operation inventory. Follow the existing generation commands rather than
editing generated output by hand:

```sh
python3 tools/sdkgen/generate.py
python3 tools/sdkgen/generate.py --check
python3 scripts/dashboard/generate_api.py
make dashboard-build dashboard-check
make docs-references docs-check
```

Set `GO` to the selected compiler for generation. The shared browser collection
parser is rebuilt by `dashboard-build`; changing SDK validators requires that
rebuild even when no TypeScript changed. Generated asset freshness prevents
shipping an older dashboard with newer server source. See the [driver-authoring
path](docs/how-to/adding-new-jobs.md) for schema, capability, execution and fixture
changes needed for a new driver.

## Change and verification scope

Use focused regression tests for behavior changes, then the checks appropriate
to the affected modules and build tags. State exactly what ran, including
failures and unavailable environments. Fixtures, provider acceptance, observed
delivery, native execution and release qualification are different evidence.
Live-provider tests require explicit invocation and designated configuration;
ordinary tests must not use personal accounts or production resources.

Preserve deterministic replay, synchronous durable writes, stable identities,
conditional resource updates and conservative treatment of unknown effects.
Do not include secrets or raw provider responses in diagnostics. A new field
that affects replay needs validation, format handling, snapshot coverage and
API/SDK contract updates together.

Pull requests should explain the concrete problem, resulting behavior and
relevant verification. Keep unrelated refactors and package renames separate.
Release publication has its own private gates; a passing pull request is not
permission to publish tags, assets or images.

## Licensing and conduct

Code contributions use the project's [MIT license](LICENSE). CPRa artwork uses
[CC BY 4.0 with attribution](brand/ATTRIBUTION.md); third-party licenses stay
intact. By contributing, you confirm that you have permission to submit the
work under its applicable license. Participation follows the
[code of conduct](CODE_OF_CONDUCT.md).
