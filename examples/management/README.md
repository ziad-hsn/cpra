# Empty management dashboard on a Linux user installation

This example starts a new encrypted, single-node Raft catalog with no monitors.
It uses named operator/reader identities and the normal application startup path.
It creates no provider accounts, invokes no check/recovery/notification driver,
and does not register a system service or change system certificate trust.

Requirements: a built `cpra` binary, Python 3, OpenSSL, and a trusted existing
parent directory owned by your user. Run this example as that user on Linux.

```bash
python3 examples/management/init-user.py "$HOME/cpra-management-example"
```

The destination must not exist. Initialization creates private files and refuses
to replace an existing directory. It generates distinct 256-bit random operator
and reader tokens, their SHA-256 verifiers, a separate 32-byte binary wrapping
key, and a seven-day certificate for `localhost` and `127.0.0.1`. Plaintext tokens
and keys are never printed. JSON-formatted generated files use `.yaml` names
because JSON is valid YAML and the normal runtime/policy loaders accept it.

The runtime file contains absolute paths. Wrapping keys, token files, the policy
and TLS sources live under `private`; durable state will live under the separate
`state` directory. Initialization leaves `state` absent.

```bash
cpra -runtime-config "$HOME/cpra-management-example/runtime.yaml" \
  -yaml "$HOME/cpra-management-example/monitors.yaml" -allow-empty \
  -web.addr 127.0.0.1:8060 -validate

cpra -runtime-config "$HOME/cpra-management-example/runtime.yaml" \
  -yaml "$HOME/cpra-management-example/monitors.yaml" -allow-empty \
  -web.addr 127.0.0.1:8060
```

Use `https://127.0.0.1:8060`. This is a locally generated certificate: explicitly
trust that certificate in the client used for this example, or replace it with
your organization's trusted certificate for a real installation. Do not disable
certificate verification in production clients. The SDK can load this example's
`private/server.pem` as a custom trust root. Open `private/operator.token` locally
and enter its content in the dashboard for the current tab; use
`private/reader.token` for observation-only access. Do not put either token in a
URL, tracked configuration, a screenshot, or command history.

The policy stores `SHA256(printable_token_bytes)`, excluding the file's trailing
newline. It never stores the bearer token itself. The generator demonstrates the
derivation directly. The application authenticates the received token against
that verifier; using a hash of raw random bytes while sending their hex text would
produce a different verifier and fail authentication.

The [runtime template](runtime.yaml), [policy template](principals.yaml), and
[policy JSON Schema](principals.schema.json) describe the editable fields. Template
placeholders are deliberately unusable. The schema is an editor aid; startup also
checks duplicate principal IDs/verifiers, reserved IDs, native file protection,
and exact transport policy. The policy is imported once into Raft; changing it
later does not replace committed grants. Use stopped local authentication
administration to issue, rotate or revoke credentials. The optional `expires_at`
field is an absolute RFC 3339 timestamp; omitted expiration means non-expiring.
Keep the wrapping-key lifecycle separate from login-token administration.

For a system service, use explicit `policy_reader_group_id`, TLS
`reader_group_id`, and local-key `reader_group_id` with root-owned sources and the
real service group. Windows uses the corresponding service SID fields; this
Linux-only generator does not configure Windows ACLs. The current macOS protected
file adapter fails closed because its native ACL verification is not implemented.
See [management startup](../../docs/management-startup.md) for those descriptors,
trusted-proxy configuration, authoritative restart and shutdown behavior.

Stop CPRa before copying the complete state directory for backup. Preserve the
wrapping key and required prior keys separately. Restart against the same state
restores dashboard changes and does not reapply the original empty file. Unknown
external outcomes remain subject to CPRa's conservative recovery policy. Remote
OpenBao/Vault/AWS wrapping backends have separate configuration and verification
requirements; these local files establish no production KMS certification.
