# Collection inventory commitments, format v1

`commitment` supplies bounded HMAC-SHA-256 calculations for the collection
upload protocol. Callers own input parsing, staging, key storage, and HTTP
requests; this package computes and verifies commitments over their exact bytes.

The caller generates one fresh, cryptographically random 32-byte key for one
frozen collection. The key is private, shared only with authorized verification
code and encrypted staging. It must never enter operation responses, history,
source labels, URLs, logs, or ordinary diagnostics. A public digest without this
key does not let a reader test guesses at a low-entropy credential value.

The helper neither generates randomness nor performs file or network I/O. It
commits to bytes, not a JSON canonicalization scheme. A Go caller freezes
`json.Marshal(api.Resource)` output. A browser would freeze its serializer's
actual resource-object bytes. A receiver extracts the raw `resource` object span
before decoding; it must not calculate a replacement MAC by marshaling the
decoded object. Different keys or different bytes deliberately produce different
identities, even if the decoded resources are equivalent.

## Exact framing

The format identifier is `cpra.collection.hmac-sha256-json-bytes.v1`.
Domains and source tokens below are ASCII bytes; resource identities are UTF-8.
Digests and keys are raw 32-byte values in
calculations; the fixed vectors encode them as lowercase hexadecimal for display.
The framing is independent of host endianness, JSON spacing, and Go struct layout.

Define:

```text
U64(n)       = n as exactly 8 unsigned big-endian bytes
F(bytes)     = U64(length in bytes) || bytes
N(n)         = F(U64(n))
M(domain, x) = HMAC-SHA-256(key, F(ASCII(domain)) || x)
```

Each numeric field is therefore 16 bytes: an eight-byte length prefix equal to
eight, followed by its eight-byte value. `||` denotes byte concatenation.

For a source ordinal `j`, `token(j)` is `source.` followed by exactly 20 decimal
digits, padded on the left with zeroes. Ordinals start at one. Tokens disclose
only ordered position, never the filename, directory, URL, hostname, or query.

```text
sourceBytesMAC(j) = M("cpra.collection.source-bytes.v1", rawSourceBytes(j))

sourceFingerprint = M("cpra.collection.sources.v1",
    N(sourceCount) ||
    for each source j in ascending ordinal order:
        N(j) || F(token(j)) || N(rawSourceByteLength(j)) || F(sourceBytesMAC(j)))

itemMAC(i) = M("cpra.collection.item.v1",
    N(i) || F(Kind/ID) || F(sourceToken) || N(document) || N(itemInDocument) ||
    F(exactResourceObjectBytes))

inventoryMAC = M("cpra.collection.inventory.v1",
    N(itemCount) || F(sourceFingerprint) ||
    for each item i in ascending ordinal order:
        N(i) || F(Kind/ID) || F(itemMAC(i)))
```

The inner source MAC has exactly one terminal byte stream after its framed domain.
Its outer entry records the byte length and source boundary. This allows
incremental writes without buffering each file or treating concatenated files as
one ambiguous source. Stream chunk boundaries have no effect. Empty source files
are valid and distinct from an absent source. Source fingerprints remain private
in the protocol; they describe client input identity, not independently
verified source provenance.

## Bounds and state transitions

| Input | Bound |
| --- | --- |
| Key | Exactly 32 bytes; caller supplies random material |
| Resource object bytes | 1 byte through 1 MiB, inclusive |
| Items and item ordinals | At most 10,000,000; positions start at one |
| Sources and source ordinals | At most 1,000,000; positions start at one |
| Source byte quota | Explicit total quota, at most 1 TiB |
| Document / item coordinates | One through 10,000,000, inclusive |
| Kind | 1–64 ASCII letters or digits; first character a letter |
| Resource ID | 1–256 valid UTF-8 bytes; not `.` or `..`; excludes `/`, `\`, `?`, `#`, `%`, NUL, CR, LF |
| Identity | Exactly `Kind/ID`, with one slash |

The ID envelope preserves the management API's shared-resource identifiers;
kind-specific validation may impose tighter limits. Zero items and zero sources are
well-defined local no-op identities; that does not require the operation
creation endpoint to accept an empty operation. Source byte limits count raw
input bytes across all sources, including the currently open source. A write
that exceeds its quota consumes no bytes and invalidates the accumulator.

`SourceAccumulator` requires `Begin`, zero or more `Write` calls, then `End` for
each successive canonical token. `Accumulator.Add` requires each successive item
ordinal exactly once. `Finish` succeeds once only after the declared count has
been supplied. Early completion, invalid input, count overruns, and order errors
invalidate the accumulator. An initialized accumulator cannot be reset or reused.
Zero values are invalid. Both types retain constant-size state, independent of
the number of items or bytes processed; neither hides a resource-identity set.

The staging layer must reject duplicate `Kind/ID` identities and validate the
coordinate syntax and exact keyed binding of every item. Without original source
streams, it cannot prove that coordinates refer to actual source documents; those
positions remain client assertions. The helper
does not decode JSON, compare resource metadata with the supplied identity, check
resource schemas or authorization, validate references, or perform mutations.
`VerifyItem` and `Accumulator.Verify` use `hmac.Equal`; callers must reject an
error before admitting the data.

## Key ownership and cancellation

Use one owner for each accumulator, do not copy an initialized value, and do not
call methods concurrently. Constructors copy any caller-owned key material they
need. They never retain resource or source input buffers after the call returns.
Always defer `Close` after constructing an accumulator:

```go
sources, err := commitment.NewSourceAccumulator(key, sourceCount, byteQuota)
if err != nil {
    return err
}
defer sources.Close()
// Begin/Write/End each source, then Finish.
```

`Close` is idempotent, clears the source accumulator's explicitly retained key,
and drops MAC references. Successful finalization and poisoned validation paths
do the same. The library cannot promise that compiler, crypto-library, or Go
runtime copies have been erased. The caller still owns and must protect its
original key, frozen resource bytes, and any private source labels.

Formatting an accumulator value or pointer emits a fixed redacted description;
JSON marshaling fails. Errors contain no input data. These are protections against
ordinary accidental logging, not isolation against hostile reflection or code
inside the process. A key or resource byte slice that the caller prints directly
is outside those protections.

## Interoperability evidence

[`testdata/v1.json`](testdata/v1.json) contains a public test key, exact resource
and source bytes in hex, item MACs, source fingerprint, inventory MAC, and empty
inventory vectors. [`testdata/generate_vectors.py`](testdata/generate_vectors.py)
implements the framing independently with Python's standard `hmac`, `hashlib`,
and integer-byte operations. Browser implementations should consume those vectors
without treating JSON object-key order or decoded numeric values as the input.

The neighboring `inventory_serialization_test.go` exercises the existing public
SDK `Operations.Upload` over real local TLS. It compares the receiver's raw object
span with the bytes from `json.Marshal(Resource)`, including HTML characters,
U+2028/U+2029, escaped keys, integer values above JavaScript's exact integer range,
and retained numeric spellings. A separate case deliberately changes escaping
and requires rejection without a mutation retry. This proves the tested serializer
boundary; it does not claim implemented server collection admission or provider
configuration validity for the synthetic payloads.
