# Starting the management dashboard

Management is an explicit runtime setting. When enabled, the normal `cpra`
application uses the encrypted durable catalog as the configuration source and
exposes the authenticated management API used by the dashboard and SDK. Existing
read-only startup remains available for stores that have never acquired a managed
catalog.

For a fresh Linux user instance, the [empty dashboard example](https://github.com/ziad-hsn/cpra/blob/4c6baf58df8bf7fd4e02e0399fbe092ba867b20f/examples/management/README.md)
generates private local sources and a test certificate, demonstrates verifier
derivation, and starts without an existing state directory. Its templates include
the runtime file and a verifier-only policy schema.

This example shows a Linux system service's source locations. Provision the
wrapping key, named-token verifier policy, and TLS certificate/key before starting
the service. All source files remain outside `/var/lib/cpra` and must be readable
under their explicitly configured native ownership policy.

```yaml
storage:
  mode: raft
  directory: /var/lib/cpra

management:
  enabled: true
  policy_file: /etc/cpra/private/principals.yaml
  # Set the real service group ID for a root-owned policy source.
  policy_reader_group_id: 1001
  tls:
    cert_file: /etc/cpra/private/server.pem
    key_file: /etc/cpra/private/server.key
    reader_group_id: 1001
  encryption:
    backend: local
    local:
      active_key_file: /etc/cpra/private/wrapping.key
      reader_group_id: 1001
```

The `1001` values are example group identities, not a requirement or an automatic
account change. An owner-only user installation omits reader-group settings. On
Windows, the corresponding `policy_reader_sid`, TLS `reader_sid`, and local-key
`reader_sid` fields select the actual service SID. Wrong-platform reader fields
are rejected. Source loading never repairs permissions or changes group
membership. Local wrapping keys contain exactly 32 random binary bytes; they are
not passwords or text-encoded values.

The policy file contains named readers and operators, for example:

```yaml
principals:
  - id: platform/oncall
    role: operator
    token_sha256: REPLACE_WITH_64_HEX_CHARACTERS
  - id: platform/observer
    role: reader
    token_sha256: REPLACE_WITH_64_HEX_CHARACTERS
```

Replace each placeholder with the SHA-256 verifier of a distinct token generated
from at least 256 random bits. The placeholders intentionally fail validation.
Keep the plaintext tokens with their owners and enter the appropriate token in
the dashboard for the current tab. Recipients are notification contacts; they do
not create management principals or grant login access. The policy parser rejects
unknown fields, duplicate identities/verifiers and extra YAML documents. Policy
and TLS files are bounded and checked with their configured ownership. TLS is checked before storage is opened; bootstrap policy is read only after the store proves that no committed authority exists.

The policy file is a **one-time bootstrap input**. Its verifiers, named roles,
revocation flags and optional `expires_at: 2027-01-01T00:00:00Z` timestamps are
committed before an owner or API listener starts. A missing expiration explicitly
means a non-expiring token. Expiration is checked again at mutation admission, so
a request authorized earlier cannot commit after its credential expires.

On subsequent starts, the committed authority wins. CPRa does not reread the
old policy file, `-web.auth-file`, or an environment token to replace it. The
`policy_file` may be removed from runtime configuration after bootstrap or when
credentials were provisioned through stopped local administration. Changing a
bootstrap file does not rotate or revoke a credential; use the stopped local
authentication operations. Legacy read-only Bearer/Basic credentials are committed
as verifiers too, with no plaintext fallback around revoked management access.
An initialized policy with no valid credentials denies access. Explicit anonymous
legacy mode remains restricted to loopback and cannot be enabled by revocation.

Explicit backup restoration resets named and legacy credentials and prevents
normal startup until stopped local reprovisioning completes. Old policy files
cannot undo that reset. `-validate` checks only supplied bootstrap inputs,
runtime settings and manifests; it neither opens the store nor validates the
committed authority, and reports that limitation.

For an existing TLS-terminating proxy, replace the `tls` block with:

```yaml
trusted_proxy:
  public_origin: https://cpra.example.com
```

Bind CPRa's web listener to loopback in this mode. The proxy must remove incoming
forwarding headers and set exactly `X-Forwarded-Proto: https`. The configured HTTPS
origin is authoritative; forwarded host headers cannot replace it. Management
rejects `-web=false` and a supplied `-web.cors` setting. Direct TLS may bind to a
non-loopback address because named management authentication remains required.

Start an initial import with the existing manifest flags:

```bash
cpra -runtime-config /etc/cpra/runtime.yaml \
  -yaml /etc/cpra/monitors.yaml -web.addr 127.0.0.1:8060
```

`-config` remains an alias for `-yaml`. YAML, JSON and gzip-compressed manifest
files use the shared collection parser. The source is opened lazily only when a
fresh catalog requires its initial import. Input is normalized, inline legacy
credentials are extracted, and the complete graph is validated in encrypted
staging before the catalog becomes active. An intentionally empty first
configuration requires `-allow-empty`; `-yaml "" -allow-empty` supplies an explicit
empty source.

Once the catalog is active, committed dashboard/SDK changes remain authoritative.
Restart does not reopen or reapply the original manifest, and deleting all managed
resources does not restore its contents. A frozen interrupted import resumes its
original encrypted stage. An incomplete input-loading stage produces an explicit
recovery error; replacing that stage automatically with changed input is not
permitted. Preserve the complete stopped state directory, including any required
`management-bootstrap` stage, when performing backup or recovery.

Turning management off while that store retains catalog identity, including
tombstones or an empty activated catalog, fails startup. This prevents an old
manifest from silently overriding committed changes. An explicit disposable
`storage.mode: memory` configuration may omit encryption and use an ephemeral
wrapping key; neither its catalog nor that key survives process restart. A
configured backend still fails on missing keys or unavailable services even in
memory mode. See the [encryption setup contract](https://github.com/ziad-hsn/cpra/blob/4c6baf58df8bf7fd4e02e0399fbe092ba867b20f/internal/encryptionsetup/README.md)
for previous local keys, OpenBao/Vault Transit and AWS KMS.

`-validate` shares collection, graph and compiled-driver validation. It uses a
temporary encrypted stage with an ephemeral local key, without opening Raft or
contacting the selected wrapping backend or monitor providers. Successful input
validation does not prove that existing state can be decrypted, that provider
credentials work, or that a remote wrapping service is available.

Shutdown first makes API readiness unavailable and rejects new mutations, while
reads and diagnostics remain accessible. It joins accepted HTTP admission
callbacks and then places a barrier in the durable command stream, including
commands whose HTTP responses became uncertain. The controller drains after that
barrier. HTTP, storage and encryption dependencies close after their owner stops.
If the admission or shutdown deadline expires, the process reports failure and
preserves ownership until process termination; it does not close Raft under a
still-running owner. Existing ambiguous external actions retain their conservative
recovery policy.

The normal startup integration is exercised with a real local TLS listener and
single-node Raft, SDK writes, controller operation application, restart without
the original source file, write-only secret recovery, and disabled-monitor
non-dispatch. This evidence is for the tested Linux process boundary. Native
service identities, additional platforms, provider accounts, packaging, and the
million-monitor endurance campaign retain their separate release gates.
The current macOS protected-file adapter fails closed pending native ACL support;
the Linux example does not establish macOS support or production KMS certification.
