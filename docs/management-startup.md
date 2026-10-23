# Starting the management dashboard

For a fresh local installation, run:

```sh
cpractl local init --management
```

Use the exact server and CLI commands printed by initialization. The initializer
creates a named operator verifier, protected bearer file, wrapping key outside
state, an empty starter manifest and localhost TLS files. It never prints the
bearer, replaces files, converts an existing store, starts a service or installs
browser trust. Trust `localhost-ca.pem` explicitly in your browser before opening
the printed HTTPS address. The local CA signing key is discarded after signing
the localhost certificate; the leaf certificate is valid for one year. Certificate
renewal/replacement is a stopped operator procedure, not an implicit init rerun.

System initialization requires `--scope system` and a protected `--token-output`
outside service configuration and state. That operator bearer is not passed to
the service. Mode-aware service rendering omits read-only bootstrap-token
arguments for management configuration.

API tokens need at least 32 printable, non-whitespace characters. Generated tokens
use 32 random bytes encoded as hexadecimal. Length validation does not establish
entropy. Existing short tokens require stopped local rotation. Committed
authority wins on restart, and supplied bootstrap sources that are ignored cause
a non-sensitive warning; obsolete bootstrap files are not needed for restart.

`GET /api/v2/access` reports only management/observation mode and whether
authentication is required. It grants no access. Authentication failure remains
401 for ordinary failures, or 429 with bounded `Retry-After` when failed
authentication is throttled. Valid current credentials bypass failure throttling.
Access discovery never
changes a write into an observation request. Local anonymous observations and
profiling require approved loopback Host/origin identities. Profiling does not
serve process command-line arguments.

An operator with credential-use and configuration privileges is trusted to
select destinations. CPRa does not isolate destinations per credential or claim
multi-tenant security. Deployment permissions still determine access to local
sockets, executables and provider accounts.

The remaining manual configuration sections also cover existing installations.

Optional external-job configuration requires a server and consumer built with
`externaljobs`, explicit runtime enablement, retained JobTypes and separately
provisioned worker grants. The [worker configuration](sdk/worker-library.md)
covers API and file/URL collections, startup recovery and offline validation's
unverified-contract count. A successful offline shape check does not establish
that a stored JobType exists or authorize a worker to execute it.

## Storage encryption for file-only monitoring

A file-configured A2A intervention or notification needs an initialized wrapping
key with Raft storage, even when the management API is disabled:

```yaml
storage:
  mode: raft
  directory: /var/lib/cpra
  encryption:
    backend: local
    local:
      active_key_file: /etc/cpra/private/wrapping.key
      # Set this to the service's actual Unix group when using a root-owned key.
      reader_group_id: 1001
management:
  enabled: false
```

The source descriptors and protection rules below apply to `storage.encryption`
too. Configure either it or `management.encryption`, never both. Management may
use the storage-level source; providing a key does not enable management APIs.
File-only runs without A2A do not require a key unless their retained state still
contains encrypted agent connections. Explicit memory mode can use an ephemeral
key and does not recover those records after restart.

The key stays outside the state directory and its backups. Provision it with
`cpractl local encryption init --key-file /absolute/path/to/wrapping.key --data-dir /absolute/path/to/state`,
using the same reader options as the runtime source. The
[native key instructions](native-installation.md#initialize-a-local-encryption-key)
cover user and service ownership, inspection, and interrupted initialization.
Application startup only loads existing keys. A new active key can be configured with the older
paths in `previous_key_files`. Keep those previous keys while retained connections
still reference them. Startup verifies retained connections even after the monitor
has been removed from the file. Missing or wrong keys stop admission.

The original connection is encrypted before an A2A action starts. After restart,
CPRa may read that original task; it never resends an ambiguous action or uses a
new target from an edited manifest. An observed terminal task does not automatically
resolve a held unknown action. See [A2A driver settings](reference/jobs-reference.md).

## Enabling management

Management is an explicit runtime setting. When enabled, the normal `cpra`
application uses the encrypted durable catalog as the configuration source and
exposes the authenticated management API used by the dashboard and SDK. Existing
read-only startup remains available for stores that have never acquired a managed
catalog.

For a fresh Linux user instance, the [empty dashboard example](reference/source/examples/management/README.md.txt)
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
resources does not restore its contents. First-import input is read once into the
private encrypted `.management-bootstrap-loading` directory on the state volume.
Only a fully validated, frozen inventory is published as `management-bootstrap`,
without replacing an existing stage. An ordinary invalid input is rejected before
admission and its newly owned loading stage is removed; correct the supplied file
and start again. Failed writes or unverifiable cleanup retain the stage and fail
closed.

A frozen interrupted import resumes its exact original encrypted inventory,
including a frozen temporary stage whose publication was interrupted. It never
rereads changed source input. A loading stage left by process termination, or by
an older first-import implementation, requires stopped inspection:

```sh
cpractl local bootstrap inspect --data-dir /var/lib/cpra \
  --runtime-config /etc/cpra/runtime.yaml --stage temporary
```

Select `--stage published` for the older `management-bootstrap` location (the
command default). Inspect authenticates the complete encrypted inventory with the
original key and holds exclusive storage ownership. To abandon an unadmitted
loading stage, use the exact `identity` from that inspection:

```sh
cpractl local bootstrap discard --data-dir /var/lib/cpra \
  --runtime-config /etc/cpra/runtime.yaml --stage temporary \
  --identity ID_FROM_INSPECTION --actor local/admin \
  --reason 'Correct invalid first-import input'
```

Discard refuses frozen or admitted stages, corruption, wrong keys, concurrent
owners, links, unexpected files and changed identities. It never removes Raft.
It writes a protected audit intent outside state before removal, then a separate
completion record. The default audit location is `DATA-DIR-bootstrap-audit`
(`/var/lib/cpra-bootstrap-audit` for the data directory in this example);
`--audit-dir` selects another protected absolute directory outside state. Missing
completion means the discard is unconfirmed. Preserve those records separately.
The commands start no controller, scheduler or monitor provider; an explicitly
configured remote wrapping backend may be contacted to authenticate its key.
Native identity/removal checks currently require Linux statx mount/birth identity,
local APFS on macOS, or local NTFS on Windows. Unverifiable filesystems fail closed;
native platform qualification remains separate.

Preserve the complete stopped state directory, including either temporary or
published required bootstrap stage, when performing backup or recovery. Do not
manually delete a frozen stage or reset Raft to bypass an error.

Turning management off while that store retains catalog identity, including
tombstones or an empty activated catalog, fails startup. This prevents an old
manifest from silently overriding committed changes. An explicit disposable
`storage.mode: memory` configuration may omit encryption and use an ephemeral
wrapping key; neither its catalog nor that key survives process restart. A
configured backend still fails on missing keys or unavailable services even in
memory mode. See the [encryption setup contract](reference/source/internal/encryptionsetup/README.md.txt)
for previous local keys, OpenBao/Vault Transit and AWS KMS.

`-validate` shares collection, graph and compiled-driver validation. It uses a
temporary encrypted stage with an ephemeral local key, without opening Raft or
contacting the selected wrapping backend or monitor providers. It reports at most
ten ordered static field errors with source labels and original coordinates; a
truncation notice does not claim to count every remaining error. Syntax, quota and
I/O failures can stop input inspection earlier. Successful input
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
comparative capacity campaign retain their separate release gates.
The local macOS protected-file adapter now inspects native ACLs and uses exclusive
file publication. Its native permission, crash and administration checks remain
unqualified; see the [platform qualification](versions.md).
The Linux example does not establish macOS support or production KMS certification.
