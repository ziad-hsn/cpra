---
title: Add a check or action
description: Extend CPRa through schema, execution and verification while respecting contexts and incident ownership.
cpra_scope: development
---

See [versions and availability](../versions.md) for current source and
qualification boundaries. Operator-owned handlers use the separate
[external-worker library](../sdk/worker-library.md); they run outside CPRa and
require the externaljobs tag in both consumer and server builds.


# Add a check or action

Start with an existing integration whose protocol and lifecycle are similar. The integration contract spans configuration, execution, and result handling.

1. Define the wire settings in `api/openapi/models.base.json`, and implement pure
   shape/semantic rules in `sdk/go/api`. These validators must not construct a
   provider client, open storage, read a credential file or contact a target.
   Keep provider SDK dependencies in the application module.
2. Add file-manifest fields and copy behavior in `internal/manifest`. Update both
   YAML and JSON decoders, file-to-resource normalization in `sdk/go/collection`,
   and typed-resource-to-runtime mapping in `internal/management`. Preserve
   omitted versus explicit false/zero values. Test the same valid and invalid
   cases through file validation, collection preflight and the API.
3. Register compiled/platform availability through the existing job capability
   registration and `internal/manifestcheck`. An unavailable driver must fail
   before opening storage. Optional built-in implementations keep their build
   constraints; `externaljobs` remains a distinct opt-in extension, never part
   of the all-built-in-driver tag list.
4. Implement execution in `internal/jobs`, the appropriate check/recovery/
   notification factory, and entity preparation in `internal/controller/entities`.
   The controller owns scheduling, start admission, retry policy and result
   commits. Do not add a second dispatch or side-effect replay path.
5. Define effective connection identity, deadline and cancellation behavior,
   bounded safe diagnostics and result classification. Include every TLS option
   in connection-pool identity. Keep clients, credentials and executable jobs
   out of Raft records, history, logs and public observations.
6. Extend production-path contract fixtures and the verification registry in
   `internal/drivertest`. Document real-provider effect evidence and configuration
   prerequisites separately from local mocks. Add configuration examples and
   field tables in the driver reference.
7. Regenerate both base/tagged SDK contracts and the dashboard contract, then
   rebuild the shared browser parser, embedded assets and notices. Add an
   operation-inventory entry only when introducing a new public HTTP operation.
   A driver setting alone does not need its own HTTP endpoint.

~~~sh
python3 tools/sdkgen/generate.py
python3 tools/sdkgen/generate.py --check
python3 scripts/dashboard/generate_api.py
make dashboard-build dashboard-check
make docs-references docs-check
~~~

Use the pinned compiler and frontend tools described in the development guide.
Review generated changes alongside the handwritten configuration/execution code.
When durable semantics change, update command validation, format handling,
canonical digests, deterministic replay and snapshots together. New job execution
never belongs in replay or snapshot restoration.

## Checks

Return the target result to the controller. Keep driver I/O bounded by the check context and close owned connections. A warning that does not mean a failed check must preserve that distinction through the result path.

## Recovery actions

The controller owns admission and verification. Do not create an independent retry loop that silently duplicates side effects. A deadline or lost response can leave the external action outcome uncertain.

## Notifications

Test provider-required fields and response classification. Distinguish retryable HTTP rejection from accepted delivery. Do not equate an HTTP acceptance with a human acknowledging the alert.

## Verification scope

Run the affected tests and `make test-all-drivers` for tagged code. Positive fixtures matter as well as timeout cases: rejecting every input is not a valid fix for a deadline bug.

Use the [source layout](../developer-guide.md) to locate current interfaces. Older template files and archived documentation may describe contracts that are no longer present.
