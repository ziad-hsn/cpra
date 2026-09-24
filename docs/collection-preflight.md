# Bounded collection preflight

The management package provides `Catalog.ValidateCollection` as a pure
preflight building block. The authenticated `POST /api/v2/collections/preflight`
endpoint and `cpractl diff` now use it for ephemeral validation. Uploads,
activation, cancellation, and durable resume remain separate implementation work.
The [encrypted persistence foundation](collection-persistence.md) documents the
inactive staging and restart boundary already implemented beneath those future
workflows.
The helper uses an explicitly captured catalog view and a bounded slice of
desired resources with opaque source/item tokens. See the
[identity contract](implementation/collection-identity-contract.md) for request
commitments and the [CLI guide](cpractl-management.md#preview-a-complete-collection) for inputs.

The caller must authorize submitted writes and provide a required identity-only
`CanRead` callback. The callback runs before testing whether each queried identity
exists. Denied present and absent references produce the same safe result; an
unauthorized outside consumer is never named in the response. Source/item tokens
are at most 128 ASCII letters, digits, dots, underscores, or hyphens. They are not
paths, URLs, provider errors, or arbitrary diagnostic text.

Preflight validates every submitted resource, including shared-only files, then
resolves references from the staged union and authorized omitted live resources.
An included invalid dependency never falls back to its old value. An omitted
Credential value preserves the original encrypted resource's value for transient
type validation; explicit null and redaction placeholders remain invalid. No
credential value or provider configuration is included in the returned result.
Supplied UID, resource-version, and generation preconditions must match the
captured original. An absent precondition uses that captured original for later
conditional activation rather than overwriting a newer observation.

Shared updates traverse the maintained reverse index and include affected
outside consumers in validation. The result reports safe create/update/unchanged
classifications, source attribution, direct dependencies, impacted authorized
identities, dependency order, original UID/version conditions, reverse-edge
versions, and create-if-absent observations. These are observations, not prepared
executable mutations. A fresh catalog read rejects already-stale results, and a
future executor must still enforce the guards at activation. It must adjust only
its own confirmed earlier changes and block a consumer when an included
dependency fails. The current helper neither allocates replacement versions nor
creates operation handles.

A valid final union is insufficient when dependency-first application would
break an old consumer at an intermediate step. Preflight simulates the order in
memory, retaining old consumers until their own turn and updating an in-memory
reverse index. It rejects an unsafe prefix. Each step validates only changed
resources, their transitive current consumers, and required dependencies;
independent monitor additions therefore do not repeatedly validate the whole
fleet. No intermediate configuration becomes active.

The synchronous limits are 1 MiB per resource, 10,000 retained desired/live
resource versions, and 32 MiB of accounted payload and copied identity/metadata
cost. Old and proposed versions both consume the budget. This is an accounting
bound, not a total process-RSS guarantee; temporary decoder/runtime allocations
also exist. A separate 100,000 graph traversal/resource-validation visit limit bounds
dense prefix work and expanded notification-group routing. Shared definitions
are validated once per affected set; invalid-monitor attribution does not repeat
that shared validation. Cancellation is checked within routing expansion.
Exceeding either bound fails explicitly before any active change. These bounds are distinct from transport chunk limits and future
incremental encrypted staging for much larger collections.

All work remains request-local. The helper reads existing encrypted resources
through the configured key backend, but does not seal new data, generate keys,
write files, allocate operations, commit catalog changes, construct executable
jobs, or invoke monitoring/notification/recovery providers. Context cancellation
stops validation. Tests cover preserved ciphertext and caller inputs, a wrapper
that rejects every attempted seal, no target HTTP requests or notification log
creation, authorized live/outside guards, rejected intermediate prefixes,
600 independent monitors, denied-reference non-disclosure, and explicit limits.
These are correctness checks, not a million-monitor performance claim.
