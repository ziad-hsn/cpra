# Public collection activation

The private candidate now connects `POST /api/v2/operations/{id}/activate` to
the original validated plan and the existing background executor. The dashboard,
SDK and CLI use this same operation. This does not publish or qualify a release.

## Admission and reconciliation

Activation is a bodyless request for an existing operation. The server requires
the original operator and current read/create/replace permissions for all
supported resource kinds. It verifies the sealed successful validation, original
plan descriptor, capability profile and input identity. It neither decrypts the
inputs nor recompiles them in the request handler. Resource version conditions
remain those captured during validation.

Current committed operator authority and process admission are checked at the
write. Artifact reads occur outside the authorization lock. A successful response
is HTTP 200 with the original `X-Operation-ID` and `Location`; an applying result
also has `Retry-After: 5`. The response acknowledges admission, not completion.
No schema, storage format or replacement parent operation is introduced by this route.

Before returning success, the server captures the original protected receipt
outside the policy lock and rechecks its epoch, ownership, storage health and
retention during final authorization. That last check reads metadata only.
An authorization wait cannot turn an expired or restored observation into a
successful current response.

Repeated requests reconcile the original activation. A lost reply or competing
admission can cause one protected read of the original operation; the server
does not submit a second activation command. Canceled and completed activations
cannot resume execution through this route. Expired or restored identities,
unavailable storage and revoked authority fail explicitly.

## Consumer behavior

The Go SDK's `collection.Apply` freezes and stages all inputs, requests whole-
collection validation, waits for that original sealed verdict, and then requests
activation. It returns the operation handle on admission. An uncertain activation
reply permits a read of the same content-bound operation, with no write retry.
`collection.Wait` performs only reads, preserves the last verified counts on
interruption, and returns one bounded first result page. Canceling the wait does
not cancel the operation.

```sh
cpractl apply -f service.yaml -f recipients.yaml
cpractl apply -f service.yaml -f recipients.yaml --wait --timeout=5m
cpractl apply -f service.yaml -f recipients.yaml --dry-run=server
cpractl wait operation/OPERATION_ID --timeout=5m
cpractl get operation OPERATION_ID --results --limit 100
```

`OPERATION_ID` denotes the exact returned handle. Dry-run uses ephemeral server
preflight and allocates no durable operation. A stopped or partial apply retains
the original handle and known counts; the CLI does not silently create a new
collection or reverse accepted changes.

The dashboard requires successful sealed validation before enabling activation
review. Confirmation identifies the operation, validation result and resource
count. Closing confirmation submits nothing. An uncertain reply requires reading
the original operation before another explicit activation. The import view and
operation detail separate configuration decisions from controller outcomes,
retain one displayed result page, and allow cancellation while applying.
Canceled parents can still have pending child results, which remain readable.

## Verification boundaries and remaining work

Native tests use the real application startup, TLS server, single-node Raft,
background executor and controller. The browser test imports two actual files
through the embedded Worker/WASM parser, validates, explicitly activates and
checks both applied results. The CLI test applies the same dependency pattern
through the public SDK. Both verify the original result after owner restart;
disabled monitors cause no provider requests. These tests do not certify a
provider account or the million-monitor workload.

Current targeted regression commands and source hashes are recorded in the
[private verification manifest](../../bin/verification/collection-public-activation-2026-09-24/result.json).
The required browser campaign includes
`TestMainManagementCollectionActivationBrowser`; a missing harness or skipped
required test fails that campaign.

The SDK's existing `Resume` still needs the original open `Frozen`. Cross-process
CLI resumption and browser-refresh reselection need the separate
[protected reselection protocol](collection-browser-reselection.md). Reopening
identical files with a new key cannot resume an old upload. No misleading resume
flag or key export is provided. Maximum-input resource measurements, optional
external-worker integration and the full private shipping gates remain open.
