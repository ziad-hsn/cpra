# Private browser collection preparation

The dashboard's **Import files** page (`/import`) connects this private parsing,
inventory and bounded serialization layer to the generated collection API
contracts. Server-assisted reselection after a refresh remains unimplemented.
The page requires an operator identity and intersects its permissions with the
server's registered discovery operations. A server that only offers preflight
can preview a collection, but cannot activate it through this page.

The one-tab flow is: choose files, review a source-attributed identity page,
optionally preview on the server, create an inactive operation, upload, validate
the complete collection, and explicitly confirm activation. The entire durable
flow stays disabled until preparation, creation, upload, validation, activation
and original-operation reads are advertised. Per-resource activation and its
server gates must be implemented and qualified separately; the presence of UI
controls is not proof that the server supports those steps.

`BrowserCollection.prepare(files, { signal, onProgress })` starts one dedicated
module Worker. The Worker loads the pinned Go/WASM parser, validates all selected
files with the public SDK's `collection.Decode`, and freezes a keyed inventory.
A malformed final file discards the entire private preparation; earlier callbacks
are staging only. Parsing and HMAC work run away from the main browser thread.

```ts
const frozen = await BrowserCollection.prepare(selectedFiles, { signal });
try {
  const summary = frozen.summary; // counts and opaque content digest
  const firstPage = await frozen.page(0); // at most 100 identities/positions
  const localName = frozen.sourceName(1); // client-local attribution only
  // The import page displays these fields and requests supported server steps.
} finally {
  frozen.close();
}
```

The shared parser supplies the existing Go behavior for multi-document YAML/JSON,
resource arrays and Lists, legacy manifests and driver normalization, duplicate
keys and identities, and source document/item positions. It rejects YAML aliases.
JavaScript does not reconstruct numeric resource values. JSON numeric tokens
retain their exact spelling. YAML decimal spellings normalize textually without
rounding. Fractional or exponent notation for integer controls is rejected just
as it is by the direct API. Exact normalized bytes are then frozen. Native/browser
parity tests cover integers above JavaScript's exact-integer range and rejection
of fractional and underflow values that previously rounded into valid controls.

Source names are sorted by UTF-8 bytes to match Go string ordering. Relative
selection names are carried explicitly because structured cloning a File must not
be relied on to preserve `webkitRelativePath`. Repeated names are rejected even
if their contents match. Names and raw source documents stay client-local; wire
attribution uses `source.` plus a twenty-digit ordinal. Explicit preflight and
upload send normalized resources, including supplied credentials, to CPRa's
authenticated same-origin API. Credentials are excluded from rendered previews,
query caches and diagnostics. There is no browser source-URL loader.

## Limits and lifetime

| Boundary | Limit |
| --- | --- |
| Selected files | 1,000 |
| Aggregate raw source bytes | 64 MiB |
| Resources | 10,000 |
| Encoded resource | 1 MiB |
| Aggregate normalized buffers | 512 MiB implementation safety quota |
| Preview page | 100 items |
| Upload body | 256 items and 4 MiB, whichever is reached first |
| Ephemeral preflight body | 4 MiB |
| Worker initialization | 30 seconds |
| One worker request | 120 seconds |

The normalized-buffer quota is separate from the source quota. JSON escaping can
expand input significantly, so a 64 MiB source is not a 64 MiB memory promise.
The Go decoder also retains its existing per-line/resource and document-metadata
limits. A quota failure is local and precedes any API request. Collections above
4 MiB can remain frozen for bounded staged uploads; they cannot be squeezed into
the ephemeral preflight operation.

Each resource has one retained normalized byte representation in the Worker.
Parsing temporarily holds the current source and Go parser memory; conversion
and wire assembly operate on one resource/chunk at a time. Web Crypto has no
streaming HMAC API, so source hashing also needs a bounded temporary buffer.

`createBody`, `preflightBody` and `uploadBody` are explicit, private serialization
boundaries. They return a `PrivateCollectionBody` holding a transferred buffer.
Its `take()` method transfers ownership once to the caller, which must pass it
directly to authenticated transport and then clear/drop it. Do not JSON-parse and
re-serialize that buffer, place it in React/query state, or log it. These classes
reject implicit JSON serialization and expose no key through object spread.
The creation/preflight request contains the private identity key by protocol;
normal summaries and pages contain neither that key nor the source fingerprint.

The page first prepares a server admission ticket and keeps it privately with
the frozen collection. Creation attaches that ticket to the otherwise unchanged
creation envelope. Once creation has been attempted, an uncertain response can
only be retried explicitly with the original ticket and frozen identity; no new
ticket or collection is silently created. An uncertain upload stops the chunk
loop. Reading the original operation reconciles the uploaded prefix before a
user explicitly resumes. No mutation is retried automatically. A successful
ephemeral preview is never substituted for durable whole-collection validation.

Waiting uses bounded operation reads at least five seconds apart, honoring a
longer server delay. **Stop waiting** stops local reads; **Cancel original
operation** is a distinct, confirmed server request. Neither leaving nor
canceling promises rollback of committed configuration or external actions.

Close or abort on navigation, sign-out, completion or abandonment. Close terminates
the dedicated Worker, drops local source names, and rejects pending work. Abort
can terminate an active synchronous Go parse. Explicit close clears owned arrays
inside inventory tests, but JavaScript, Go and browser runtime copies are not
cryptographic erasure guarantees. No localStorage, sessionStorage, IndexedDB,
Cache Storage or plaintext filesystem spool is used.

A new preparation currently creates a new key even when files are identical.
The approved ability to reselect original inputs and resume the original server
operation still needs a reviewed, authenticated server-assisted protocol. Do not
persist the key or treat a new preparation as the original operation to bridge
that gap.

## Build and verification

The bridge is `scripts/dashboard/collectionwasm/main.go`, compiled directly from
the SDK module with `GOWORK=off`. Its build constraint excludes `externaljobs`.
It imports no CPRa application module or provider execution library. The Go
compiler version comes from `scripts/release/recipe.json`; ambient GOFLAGS,
workspace settings and experiments are disabled. The matching `wasm_exec.js` and
Go license are generated alongside the unstripped optimized Wasm binary.

```sh
python3 scripts/dashboard/build_collection_parser.py --go /path/to/pinned/go --node /path/to/pinned/node
python3 scripts/dashboard/build_collection_parser.py --go /path/to/pinned/go --node /path/to/pinned/node --check
```

Generated Wasm/runtime/license/build metadata are committed build inputs. The
application's `go install` path uses completed embedded dashboard assets without
invoking this generator. `make dashboard-build` now regenerates and qualifies
these inputs before building the frontend; release `--check` rejects stale files.
The compiler and Node helper are pinned, ambient build settings are excluded,
and the SDK module files must remain unchanged.

The generator executes the exact compiled Wasm with its matching Go runtime to
read static `runtime/debug` build information. `DEPENDENCIES.json` and
`LICENSES.txt` contain its actual linked modules, local SDK source digest and Go
runtime license. The local unpublished SDK is identified as source, never as an
invented module release. When the parser is present in completed Vite output,
staging and packaged SBOMs include this verified artifact and its linked
dependencies. The import route references the Worker, so the next integrated
dashboard build must include and verify its parser assets and dependency notices.
Source-level UI tests alone do not establish packaged or native-browser evidence.

`tests/WorkerInventory.test.ts` checks the independent Go/Python cryptographic
vectors, quotas, bounded chunks, private ownership, ordering and rejection paths.
`scripts/dashboard/verify_import_worker.cjs` runs the production Worker in Chrome
through an isolated Vite server and compares normalized bytes with the native Go
decoder. It also measures ordinary and escaped 64 MiB inputs, verifies cancellation
after parsing starts, rejects a real parser-asset load failure, and checks that no
API call or browser storage write occurred. Result files belong to private local
qualification and do not certify server activation, provider accounts or fleet
capacity.

References: [Go WebAssembly build/runtime contract](https://go.dev/wiki/WebAssembly),
[SDK YAML node parser](https://pkg.go.dev/gopkg.in/yaml.v3#Node),
[Worker messages and transferable buffers](https://developer.mozilla.org/en-US/docs/Web/API/Worker/postMessage).
