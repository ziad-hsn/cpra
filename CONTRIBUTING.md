# Contributing to CPRa

Discuss substantial behavior or public API changes in an issue before opening
a large pull request. For a bug, provide a minimal reproduction and the source
commit, build tags and environment. Keep credentials and private infrastructure
out of reports; vulnerabilities follow [SECURITY.md](SECURITY.md).

## Development

Use Go 1.25 or newer for source compatibility. Official artifacts use the exact
compiler and build recipe in `scripts/release/recipe.json`. The dashboard uses
the pinned Node and pnpm versions in the same file. Do not change dependencies
or generated files just to make an unrelated change pass.

From the repository root:

```sh
make dev-workspace
make fmt-check
make vet
make test
make sdk-check
```

The development workspace composes the application, SDK and worker modules
locally. Root `go test ./...` does not test nested modules. `make sdk-check`
checks the SDK and the tagged worker module separately. Local workspace success
does not establish that versioned modules have been published or downloaded.

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
editing generated output by hand.

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
