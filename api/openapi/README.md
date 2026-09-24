# CPRa SDK contract sources

These files define the **management v2 client contract for the private candidate**.
The working branch implements and tests resource management, incident controls,
action review, and observations through the normal server, SDK and dashboard.
Collection staging/activation and the external-worker protocol still have
outstanding server gates. The
[implementation progress](../../docs/implementation/dashboard-implementation-progress.md)
records the implemented boundaries and execution evidence; generated code alone
does not establish server coverage. The SDK uses the current management and observation contract.

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
- `operations.json` maps each operation to a public SDK method and named
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

The six original-file reselection operations are marked `implemented` after the
working-branch TLS/SDK and normal-startup/restart tests passed. Routes and runtime
discovery remain conditional on the server reselection manager; normal managed
web startup configures it. Older published servers may not provide these routes.
`TestReselectionOperationInventory` covers their distinct 201/202/204 status codes
and bounded `application/octet-stream` source body. Generated models and browser
operation descriptors describe wire contracts; they do not advertise runtime
availability or grant permission. Attempts contain only bounded progress and safe
failure codes, never source paths, inventory keys, or private commitments.

The shared contact additions use `Recipient.spec.endpointRefs` as an ordered list
of existing notification endpoints. Groups may contain endpoint and recipient
references, with at least one member and no nested groups. A Code's `notifyType`
selects the actual driver key; `recipientRefs` and `groupRef` provide destinations.
That typed mode excludes inline drivers and direct rule endpoint references.
Local SDK validation checks shapes and duplicate references; matching destination
methods, reverse dependencies and delivery deduplication require the authoritative
server catalog. Legacy endpoint-only group fanout remains supported.

`GET /api/v2/self` returns `AccessInfo` with `principalId`, `role`, and an explicit
`permissions` array. Permissions exactly match case-sensitive OpenAPI operation IDs
such as `CreateMonitor`. Role names and `discovery.resourceOperations` do not grant
permission: discovery lists supported operations, while self describes access for
the authenticated request. An empty permission array grants no operations.
`GET /api/v2/operations` and recipient list routes use bounded cursor pagination.
