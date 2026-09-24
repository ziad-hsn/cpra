# Collection file normalization and upload fencing

This private checkpoint implements the normalization and persistence
prerequisites for [original-file reselection](collection-browser-reselection.md).
It does not expose a refreshed-browser or restarted-CLI resume operation.

## File compatibility

`collection.NormalizeFile` is the shared browser/native decoder for
`cpra.file.base.v1`. It rejects invalid UTF-8, streams supported YAML/JSON and
legacy inputs, and supplies the exact normalized JSON plus document/item
coordinates. Callers must not reserialize those bytes before computing their
inventory commitment. The existing `Decode`, `Freeze` and `FreezeResources`
contracts remain separate; they do not implicitly claim this file profile.

The profile supports the base resource contract and all 33 built-in driver
configuration names. External JobTypes and external check, recovery,
notification and notification-selector variants remain excluded when the
normalizer is linked with `externaljobs`. These are parsing/configuration tests,
not provider execution evidence.

The profile caps a source at 64 MiB, a serialized resource at 1 MiB and emitted
resources at 10,000. The browser additionally limits total selected source bytes
to 64 MiB. The 16 MiB document setting bounds the existing parser's metadata
accounting: YAML metadata lines and JSON non-list field values. Streamed lists
have independent per-resource limits, so this is not a total document or heap
bound. Smaller caller limits can tighten the contract. HTML escaping that would
expand an emitted resource past its limit is rejected before its callback.

Seventy-one fixed input/output vectors pin acceptance, numeric representations,
serialization and coordinates. The parser build runs those vectors against its
compiled Wasm and independent native base/tagged adapters. Build metadata records
the profile and fixture digest alongside source provenance. Changing v1 behavior
requires a new profile; regenerating fixtures to hide drift would violate the
contract.

## Admission and storage

New browser Prepare/Create requests carry `normalizationProfile`. The server
binds it into the private admission HMAC and encrypted collection identity.
Omitted legacy profiles preserve the original HMAC and envelope binding exactly.
Explicit JSON null, empty or unsupported values are rejected before admission.
An original creation ticket cannot be relabeled, and a lost creation response
still reconciles the same operation.

Storage format **14** records the profile through headers, consumed tickets,
snapshots, replay, execution results and source cleanup. New profile/fence fields
are rejected in older command formats. Ordinary legacy commands keep their prior
minimum formats and replay rules. A binary without format-14 support cannot
restore format-14 state; ordinary artifact rollback does not downgrade storage.

The optional internal `CollectionUploadFence` captures uploaded count, encoded
bytes, prefix digest and operator authority. The FSM checks all of them in the
same step as appending the next row. Revoked authority, a different owner epoch,
expired work or an advanced prefix rejects the write without renewing activity.
An accepted row whose response was lost must be reconciled by protected read of
its original ciphertext. The fence does not authorize active configuration.

Browser operation readers keep the recorded profile through validation and
result pages. Unknown future profiles remain readable as observations but do not
enable the browser's mutation flow. SDK Apply/Resume reject a substituted profile
for an ordinary unprofiled `Frozen`; result wait/pagination also retain the
original profile identity.

## Executed checks

The [private evidence manifest](../../bin/verification/collection-normalization-2026-09-24/result.json)
records commands, logs and the [selected source snapshot](../../bin/verification/collection-normalization-2026-09-24/source-manifest.json).
This is a format-14 normalization checkpoint, not qualification of the whole
working tree or the complete dashboard goal. Commands recovered from an agent's
execution record are identified separately from freshly recorded invocations.
The separate
[normalizer record](../../bin/verification/file-normalization-2026-09-24/result.json)
contains the six-way SDK matrix and compiled fixed-vector qualification.

- Go 1.27 default/tagged SDK tests and races; Go 1.25 default/tagged SDK tests.
- Format gates, encrypted binding, stale/revoked authority, exact prefix checks,
  native Raft replay, snapshots and retained receipts on Go 1.27 race / Go 1.25.
- Admission and HTTP collection regressions, including explicit null/empty
  profile rejection, on Go 1.27 race / Go 1.25.
- Pinned dashboard build, type checks, lint and **433 tests**.
- Embedded dashboard with real file selection, Worker/Wasm, TLS/Raft and normal
  application startup. Explicit activation preserves the profile and original
  result after restart. Disabled imported monitors make zero target requests.
- Real CLI Apply/dry-run/wait through the public SDK and the same server startup;
  original results survive restart. These fixtures run on Linux amd64 WSL only.
- Reproducible SDK generation, applicable built-in-driver checks and release
  script tests.
- Final admission authorization after key wrapping and before response headers
  or error publication, including rejection if the authenticated principal
  changes; Go 1.27 race **6.115 s** and the fresh Go 1.25 admission HTTP group
  **4.754 s**. SDK reference and source-guide synchronization checks also pass.

The first browser run exposed a dropped profile between the retained validation
view and the current-operation read. That kept activation disabled. The fix has
a regression test and passing connected rerun; the initial failed log remains
recorded rather than being replaced.

The final connected race group passes in **65.190 s**; the minimum-Go activation
browser and CLI group passes in **49.906 s**. The earlier successful dashboard
run had 432 tests; its final replacement has 433. The evidence also preserves
the initial admission-test pointer-type compile failure, the matching initial
vet failure, and an older vet failure caused by an incomplete temporary-spool
edit. The corrected admission race and final affected-package vet pass; an empty
vet log is recorded with the executing agent's confirmed exit status rather
than treated as proof by itself.

The encrypted temporary-spool primitive has its own
[independent race record](../../bin/verification/collection-reselection-spool-2026-09-24/independent-race.json)
and minimum-Go log. Those isolated checks do not establish a connected
reselection workflow. The separately recorded
[source/proof components](collection-reselection-proof.md) have their own
acceptance boundary. The normalization source snapshot deliberately excludes
`collection_reselection_sources*.go`, `collection_reselection_proof*.go` and
`collection_upload_verify*.go`; their separate evidence is not folded into this
record.
Connected source/inventory proof, authenticated runtime-attempt ownership and routing,
resumed suffix transfer, refreshed-browser/restarted-CLI consumer flows and
maximum-input resource measurements remain outside this checkpoint and still
need connected acceptance.
This checkpoint makes no provider-account, million-monitor, endurance or release
publication claim.
