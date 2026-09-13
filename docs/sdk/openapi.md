---
title: SDK candidate · CPRa SDK contract sources
description: SDK candidate · CPRa SDK contract sources for the reviewed CPRa source; see the version and availability notice.
cpra_scope: sdk
---

> **Unpublished SDK candidate:** this package guide reflects the 13 September source snapshot. See [availability and source](../versions.md#go-sdk-and-approved-management-plan) before running candidate commands.

# CPRa SDK contract sources

These files define the **draft management v2 client contract**. They do not claim
that the current CPRa server implements v2. The existing v1 API remains separately
qualified through the public SDK `legacy` package and repository server tests.

- `models.base.json` defines the canonical public types, including concrete data
  structures for all 33 built-in driver configurations. Optional mutable fields
  retain presence. Duration configuration uses Go duration strings, not numeric
  nanoseconds or ISO-8601 durations. Driver envelopes preserve raw unknown data
  for observation; SDK mutation validation selects a concrete category/type.
- `contract.base.json` defines normal API paths and HTTP operations using the
  model document as an external reference. The API resource version is
  `cpra.io/v2`; the SDK's independently published Go module version remains v0.
- `externaljobs.json` is the only extension overlay. It adds JobTypes, the external
  configuration payload, worker protocol messages, and extension HTTP paths.
  Generation combines it only for `externaljobs` builds. Default compiled types,
  clients, and embedded OpenAPI contain none of those extension definitions.
- `operations.json` maps each draft operation to a public SDK method and named
  contract test. The test inventory is copied into the SDK module so downloaded
  source tests never need the application module or repository-relative imports.

OpenAPI is pinned to 3.1.2. Generation uses local `oapi-codegen` 2.8.0 and the
shipped runtime is pinned to 1.6.0. Generator dependencies are isolated in the
`tools/sdkgen` module; ordinary SDK consumers do not need the generator.

From the repository root:

```sh
GO=/path/to/go1.27.1/bin/go python3 tools/sdkgen/generate.py
GO=/path/to/go1.27.1/bin/go python3 tools/sdkgen/generate.py --check
```

The second command generates only in temporary storage and fails on differences;
it does not rewrite tracked files. Toolchain selection is local, module resolution
is readonly, and no generation platform account is required. `sdk/go/generation.json`
records SHA-256 identities for schema inputs, generator files/dependency locks,
and generated outputs. Generated transport and schema files carry mutually
exclusive build constraints. Handwritten convenience methods and validation stay
separate from generated transport/types; do not edit generated output directly.

`TestOperationInventory` and `TestExternalOperationInventory` exercise every
inventoried draft method against an HTTP contract fixture. They check the
**client's method/path serialization and decoding boundary**, not Raft admission,
server authorization, controller behavior, external delivery, or provider effects.
Separate SDK tests check retries, response bounds, optional field presence,
preconditions, cancellation, and build exclusion. Publication requires actual
server v2 and worker-protocol qualification in addition to these tests.

<!-- Imported from api/openapi/README.md; preserve candidate scope and reconcile with original before regenerating. -->
