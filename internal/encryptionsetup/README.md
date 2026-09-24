# Management encryption setup

`encryptionsetup.Open` constructs the process-owned encryption dependencies used
before management admission. It loads existing key sources and verifies the
selected remote key contract. It does not open Raft, activate resources, create
keys, change file permissions, or contact monitor/notification providers.

```go
handle, err := encryptionsetup.Open(ctx, encryptionsetup.Options{
    StorageMode:   config.Storage.Mode,
    DataDirectory: resolvedDataDirectory,
    Encryption:    config.Management.Encryption,
})
if err != nil {
    return err
}
defer handle.Close()

sealer := handle.Sealer()
// Give sealer to encrypted staging/catalog construction. Authenticate all
// recovered records before admitting writes or starting reconciled work.
```

The snippet shows application integration order; it does not claim that calling
the factory has authenticated an existing catalog. The application owns readiness,
catalog verification, admission, and shutdown sequencing. Close the handle after
those consumers finish. `Close` is idempotent and safe for concurrent calls. It
releases owned idle HTTP connections; it neither interrupts active requests nor
promises erasure of Go cipher schedules.

## Backend selection and failure behavior

| Configuration | Behavior |
| --- | --- |
| Explicit `memory`, no encryption descriptor | Generate one process-local wrapping key in memory. A new handle cannot recover the previous handle's envelopes. |
| `raft`, no encryption descriptor | Return `ErrEncryptionRequired`; do not generate a key or fall back to memory. |
| Either mode with a descriptor | Validate and use exactly that configured backend. A missing source or unavailable backend is an error. |
| Unknown or omitted storage mode | Return `ErrConfiguration`. |

The resolved data directory must be absolute for Raft or any configured backend.
Encryption descriptors are validated before source reads or backend requests.
Bootstrap sources must remain outside the complete state/backup directory. The
loader verifies canonical paths and native file protection in addition to the
runtime configuration's lexical containment check.

Errors expose classified causes without source paths, tokens, profile parse
contents, provider response bodies, or key material. `Options` and `Handle`
formatting is redacted. Callers must still avoid logging plaintext resources,
decoded source bytes, or the original input configuration.

## Local keys

The active file contains exactly 32 random bytes. Provisioning is a separate,
explicit `secureconfig.GenerateLocalKeyFile` operation; `Open` only loads existing
files. `previous_key_files` supplies historical wrapping keys needed to read
existing envelopes, while new writes use `active_key_file`. Duplicate key material
under distinct file names is rejected by the immutable key ring.

Files and their containing directory must meet the native protection policy:

- Linux user mode requires an owner-only regular file and private directory.
- An explicit Unix `reader_group_id` permits a root-owned source readable by that
  service group. The loader does not change ownership or group membership.
- Windows uses protected DACLs; `reader_sid` declares the allowed service SID.
  Unix group options on Windows and Windows SID options on Unix are rejected.
- Safe projected-secret aliases can be read, but state-directory aliases,
  hard-linked sources, changed file identities, and unsupported native ACL
  verification fail explicitly. The current non-Linux Unix ACL adapter fails
  closed; a macOS build is not evidence of native key-file support.

Keep previous keys while any retained catalog, staging, snapshot, or backup needs
them. Selecting a new key does not re-encrypt existing records. Key identity is
authenticated in both envelope layers, so changing only the recorded key ID is
invalid; moving records between wrapper identities requires opening and sealing
the plaintext through a separately controlled migration.

## OpenBao and Vault Transit

`openbao-transit` and `vault-transit` both use the existing-key Transit protocol.
The descriptor provides one HTTPS origin, mount, key name, protected token file,
optional protected CA bundle, optional namespace, and optional explicit reader
group/SID. Tokens and CA bundles use the same native file integrity policy as
local keys. Token reads are bounded, accept one nonempty printable token plus
surrounding whitespace, and reject embedded control characters. The token file is
reread and revalidated for each request, allowing an atomic protected-file token
replacement. Custom CA certificates are loaded at handle construction; trust-root
changes require constructing a new handle through the application's lifecycle.

Initialization reads key metadata, encrypts a random data key, decrypts it with
the correct associated data, and confirms modified associated data is rejected.
This rejects servers that silently ignore the binding field. It requires an
existing non-derived, non-convergent `aes256-gcm96` key with encryption and
decryption support. Provision policy with read on `keys/<name>` and update on
`encrypt/<name>` and `decrypt/<name>`; omit create capability to prevent accidental
key creation. The adapter never creates, rotates, exports, or deletes keys. See
the official [OpenBao Transit API](https://openbao.org/api-docs/secret/transit/)
and [Vault Transit API](https://developer.hashicorp.com/vault/api-docs/secret/transit).

Requests use owned HTTP transport configuration, bounded bodies, ten-second
timeouts, no redirects, and no automatic retries. Cancellation remains visible.
The stable wrapper identity includes the configured origin/namespace/mount/name;
provider key versions remain in the wrapped ciphertext. Preserve that identity
and provider key history across restarts and restoration.

## AWS KMS

The factory uses the official AWS SDK for Go v2 configuration and credential
chain. It passes the configured region, optional profile, and optional ordered
shared configuration/credential file lists to `config.LoadDefaultConfig`.
Explicit files are bounded and checked under the external-to-state, owner-only
protection policy before the SDK loads them. Other SDK-managed sources, profile
helpers, environment credentials, and credential refresh follow the official
chain; the factory does not copy those sources into Raft or implement a separate
credential resolver. Operators remain responsible for securing those sources
under the actual service identity. See [AWS SDK configuration and credential
sources](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html).

Use an exact customer-managed symmetric encryption key ARN in the configured
region. Aliases, mismatched regions, disabled keys, unsupported key types, and
configured endpoint overrides are rejected. Initialization calls `DescribeKey`.
Wrapping uses `Encrypt` and `Decrypt` with the exact ARN and a non-secret digest of
the envelope binding in the KMS encryption context. The factory disables SDK
request logging, automatic retries, and redirects. Credential-acquisition HTTP
responses are bounded at 256 KiB and wrapping responses use the adapter's bounded
decoder. SDK-owned credential refresh retains its bounded HTTP client for the
handle's lifetime.

The factory does not implement provider-account creation, permissions changes,
key provisioning, or cross-backend key retirement. Region, credentials and HTTP
transport reach the private KMS adapter; custom SDK endpoint configuration,
middleware and logging do not become part of its wrapping client.

## Verification boundary

Tests exercise local key restart and previous-key recovery, explicit ephemeral
mode, missing/corrupt files, permissions, redaction, descriptor rejection, and
transport ownership. Transit tests perform real TLS requests to a local protocol
fixture, including associated-data validation and protected token rotation. KMS
factory tests load actual selected profile/configuration files, use the production
AWS endpoint resolver and signer, and route the socket to a local TLS fixture.
Credential-response tests verify overflow detection and body cleanup.

These are executed local contract tests. They do not establish an OpenBao/Vault
deployment or AWS production-account certification. Native service identities,
native platform ACL behavior, external accounts, whole-application startup and
restart, and catalog admission require their respective integration evidence.
