# Stopped local authentication administration

`cpractl local auth` manages the durable named reader/operator policy for one
stopped CPRa installation. It does not call the management API, read a monitor
manifest, obtain a decryption key, load jobs or invoke providers. It takes the
same exclusive storage lock as CPRa; an active owner causes an explicit error.
Run it as the local account authorized to administer that state directory.

All commands require an explicit absolute `--data-dir`. The directory must
already exist; an empty directory may be used for initial bootstrap. For example,
use the state path returned by `cpractl local paths`, or the explicit path in
your service configuration. Stop the service before continuing. The examples
below use a Linux user installation:

Only `bootstrap` may initialize an empty directory. Other commands require the
existing identity, database, history catalog and snapshot directory, and reject
empty or incomplete state before opening Raft or publishing a token.

```bash
cpractl local auth list --data-dir /home/you/.local/state/cpra
```

Output is safe JSON metadata: policy epoch/revision, named IDs, roles, revocation
and optional expiry, plus whether anonymous loopback or legacy read access is
present. It contains neither bearer tokens nor their verifiers. Notification
recipients remain separate contact resources; adding a recipient does not grant
access to CPRa.

## Bootstrap and issue named credentials

Initialize a never-initialized policy with an explicitly selected first principal:

```bash
cpractl local auth bootstrap team/oncall --role operator \
  --data-dir /home/you/.local/state/cpra \
  --token-output /home/you/.config/cpra-access/oncall.token
```

Then issue another identity without replacing the existing policy:

```bash
cpractl local auth issue team/observer --role reader \
  --data-dir /home/you/.local/state/cpra \
  --token-output /home/you/.config/cpra-access/observer.token
```

Roles are exactly `reader` or `operator`, with no implicit default. Named IDs are
unique, at most 128 UTF-8 bytes and contain no whitespace or control characters.
The policy retains at most 1,024 named principal records, including revoked ones.
An existing or revoked ID cannot be silently reused. `legacy-read` is reserved
for the compatibility credential described below.

Every token contains 256 freshly generated random bits, encoded as a URL-safe
bearer value with a final newline. Only its SHA-256 verifier enters Raft. The raw
token goes directly to the explicitly selected **new file outside the complete
state directory**. Token values are never returned through stdout, logs or errors.
No command accepts an existing token value in arguments.

The immediate output directory may be created privately when its parent already
exists. Existing permissions are verified rather than repaired. Linux requires
the intended owner-only directory and file protections; extended access ACLs and
unsafe writable ancestors are rejected. Windows uses the existing protected
owner/SYSTEM/Administrators DACL policy. New output paths cannot traverse symbolic
links or alias the state directory. A file that already exists is never replaced.
Native execution evidence for these commands is currently Linux/amd64; other
platform release qualification remains separate. Unsupported native ACL policies
fail explicitly instead of weakening protection.

Connect to the configured HTTPS API using the token file after restarting CPRa:

```bash
cpractl --server https://cpra.example.test \
  --token-file /home/you/.config/cpra-access/observer.token \
  get monitor-configurations -o json
```

Transport/TLS settings remain runtime configuration. An existing bootstrap policy
file or legacy token source is consumed once; it is not a source for resetting a
durable policy on subsequent starts. Issuing the first named principal into an
anonymous-only installation disables anonymous access. There is no local command
that enables anonymous access again.

## Expiry, rotation and revocation

Issuance has no default expiry. Select an absolute RFC3339 time when wanted:

```bash
cpractl local auth issue temporary-observer --role reader \
  --expires-at 2026-12-31T23:59:59Z \
  --data-dir /home/you/.local/state/cpra \
  --token-output /home/you/.config/cpra-access/temporary.token
```

The selected expiry must be in the future. It is checked by server authorization
at request admission, including the mutation admission boundary.

Rotate a principal into another absent file:

```bash
cpractl local auth rotate team/observer \
  --data-dir /home/you/.local/state/cpra \
  --token-output /home/you/.config/cpra-access/observer-next.token
```

Rotation preserves the principal's role and expiry. It commits a replacement
verifier with **no overlap**: the old token stops authorizing when the replacement
policy is in use. An explicit `--expires-at` changes expiry; `--expires-at never`
removes it. Rotation does not change other named or legacy grants. Distribute the
new private file through your chosen secure process and update the client before
resuming normal use; the command does not copy credentials to another account.

```bash
cpractl local auth revoke team/observer \
  --data-dir /home/you/.local/state/cpra
```

Revocation retains the record and its revoked status. It creates no token file.
If a consumed shared legacy read credential is present, revoke both its Basic and
Bearer compatibility access explicitly:

```bash
cpractl local auth revoke legacy-read \
  --data-dir /home/you/.local/state/cpra
```

This reserved compatibility identity cannot be issued or rotated into a named
principal. Use an explicitly named `reader` to migrate its consumers.

## Uncertain commits and output files

The private output is fully written and flushed before its verifier is submitted.
Each operation submits one conditional policy change, bound to the exact current
epoch and revision read while holding the exclusive lock. It never retries or
generates a replacement token automatically.

If a commit reply is lost, the command returns an `unconfirmed` outcome with an
`intendedRevision` and keeps the original token file. Stop/resolve any remaining
owner, then inspect:

```bash
cpractl local auth list --data-dir /home/you/.local/state/cpra
```

A matching current revision identifies the committed attempt. A differing
revision is not permission to overwrite the token file or recreate the original
request: retain the original output and establish the durable result first. No
token is echoed for comparison. A later write can supersede that revision, so
retain the original command's safe metadata when coordinating administrators.

A publication failure may leave its selected file present but never submits a
verifier. A rejected or uncertain commit also preserves any published file.
Cleanup is an explicit operator decision after reconciliation. Reported commit
success and a later close error remain separate; inspect the reported revision
before starting another action.

## Provision a restored store explicitly

The stopped complete-directory restore procedure creates a new authentication
epoch and invalidates restored named, legacy and anonymous grants. Normal startup
requires explicit provisioning of that restored epoch. Old policy files and
token sources cannot restore access automatically.

```bash
cpractl local auth list --data-dir /absolute/restored-state
cpractl local auth reprovision restored-oncall --role operator \
  --data-dir /absolute/restored-state \
  --token-output /home/you/.config/cpra-access/restored-oncall.token
```

`reprovision` applies only to a store marked as awaiting restored authentication.
It binds the current reset epoch and revision and installs only the explicitly
selected new principal; issue the remaining named principals with fresh tokens.
Ordinary `bootstrap`, `issue`, `rotate` and `revoke` cannot bypass that reset.
Reopening an ordinary stopped store for access administration does not recover
incidents, mark executors finished or dispatch work. Completing a previously
committed explicit restore reset remains part of opening that restored store.
