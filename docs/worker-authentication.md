# Local worker authentication

Worker provisioning is available only in an `externaljobs` build. Ordinary builds,
including the all-built-in-driver build, contain no `local worker-auth` command.
For a development checkout, build the tagged CLI with:

```sh
make build-ctl BUILD_TAGS=externaljobs
```

This interface provisions worker identities and scopes in committed local state.
It does not start a worker, load Go handlers, issue execution grants, or expose
assignment/start/result routes. Those execution features have separate gates.
The runtime setting `external_jobs.enabled` is a separate opt-in; compiling or
provisioning a worker does not turn it on.

## Required ownership and authority

Stop CPRa before every command, including `list`. The CLI opens the existing Raft
store exclusively and refuses a running owner. It cannot initialize an empty
store or bootstrap management authentication. Use the tagged CLI for a store
that contains optional worker policy state; an untagged binary cannot read that
storage format.

Run administration under the account that owns the state directory, with access
to the designated protected input/output directories. Do not change the service's
filesystem ownership as part of credential provisioning.

Every command requires `--data-dir` and `--actor`. The actor must be an eligible,
unexpired, non-revoked named management operator in the store's current policy.
The CLI binds that identity and the observed authentication epoch/revision into
the committed command. Authority is checked again at commit. Selecting an actor
is a local audit operation by the OS administrator who owns the stopped store;
it does not authenticate a remote caller or require an operator bearer token.

No command contacts `--server`, starts providers, or reads a runtime worker
policy file. Committed worker policy is authoritative.

Worker policy uses tagged storage format 17 and is bounded to 1,024 retained
worker identities and 16 MiB of canonical state. Revoked identities remain
retained. Default builds continue to support format 14 and refuse external state.

## Explicit grant files

Use one protected JSON file outside the state directory. It must be an absolute
path to a regular file, with the same native ownership/ACL and symlink checks as
other protected CPRa inputs. On Linux, create it with owner-only permissions:

```sh
umask 077
cat > /etc/cpra/worker-a.grants.json <<'JSON'
{
  "grants": [
    {
      "job_type_id": "service-probe",
      "job_type_uid": "REPLACE_WITH_COMMITTED_JOBTYPE_UID",
      "version": "v1",
      "category": "check",
      "resource_kind": "Monitor",
      "resource_ids": ["service-a", "service-b"]
    }
  ]
}
JSON
```

Replace the example JobType ID, UID and version with an existing committed
JobType version obtained through the tagged SDK. The UID pins its incarnation;
recreating a JobType with the same name does not inherit an old grant. JobType
authoring remains a tagged SDK operation.

The file is limited to 1 MiB, 64 grants, and 64 resource IDs per grant. Unknown or
duplicate fields, invalid UTF-8, trailing documents and implicit/null grant
arrays are rejected. Field names are case-sensitive. `check` and `recovery`
grants select `Monitor` resources; `notification` selects `NotificationEndpoint`.
The only wildcard form is the explicit singleton `"resource_ids": ["*"]`.

Monitor and NotificationEndpoint scopes use stable resource IDs, so they also
cover later incarnations recreated with the same ID. The execution protocol
must separately pin each assignment to its concrete target UID.

An empty array, `{"grants":[]}`, explicitly denies all execution scopes. It is
valid for initial provisioning and for removing a worker's existing scopes.
Grant and resource-ID order are retained. Reordering counts as a grant change.

## Issue, inspect, rotate, update and revoke

These examples assume a tagged `cpractl` is on PATH and CPRa is stopped. All
token-output paths must be absent, absolute and outside `/var/lib/cpra`.

```sh
cpractl local worker-auth issue worker-a \
  --data-dir /var/lib/cpra --actor team/oncall \
  --grants-file /etc/cpra/worker-a.grants.json \
  --token-output /etc/cpra/worker-credentials/worker-a.token

cpractl local worker-auth list \
  --data-dir /var/lib/cpra --actor team/oncall

cpractl local worker-auth rotate worker-a \
  --data-dir /var/lib/cpra --actor team/oncall \
  --token-output /etc/cpra/worker-credentials/worker-a-next.token

cpractl local worker-auth set-grants worker-a \
  --data-dir /var/lib/cpra --actor team/oncall \
  --grants-file /etc/cpra/worker-a.grants.json

cpractl local worker-auth revoke worker-a \
  --data-dir /var/lib/cpra --actor team/oncall
```

`issue` requires a new worker ID. Token generation uses 256 random bits; only its
verifier is submitted to Raft. The complete private token file is flushed and
published before the verifier is submitted. Files are never overwritten, and
neither bearer values nor active/restored verifiers appear in command reports.
Distribute the protected file to the intended worker using your controlled
credential delivery process. Provider credentials remain worker-local.

`rotate` preserves the worker UID, grant revision, grants and existing expiry.
It changes the credential revision and verifier without an overlap period.
`--expires-at` accepts an RFC3339 timestamp or `never`; omission preserves an
existing expiry, and a new worker has no expiry unless one is supplied.
`set-grants` changes no token. Identical grants preserve the grant revision.

`revoke` retains the worker identity as a permanent revoked record. That ID cannot
be reused, rotated or reprovisioned. It can also revoke a worker awaiting restore
reprovisioning without issuing a token. Expiration alone does not remove grant
references; explicitly remove grants or revoke the worker when retiring them.

## Uncertain commits and restored stores

Reports include `protocolServerID`, the policy epoch/revision, intended revision, worker UID,
credential/grant revisions, reset status and outcome. If a commit is
`unconfirmed`, retain the original token file and intended revision. Run `list`
after the owner is stopped and compare the observed revision. Do not repeat
issuance or delete the file merely because the commit reply was lost. A rejected
commit also retains a token file that was already published. There is no automatic
retry or regeneration.

Use `protocolServerID` and the selected worker UID when configuring the worker
library. Ordinary restart and token rotation preserve the protocol identity;
restoring a backup changes it. A failed identity read after a successful policy
commit keeps `outcome: committed`, the intended revision and token path in the
report. Inspect `local worker-auth list` instead of issuing another token.

After backup restoration, first reprovision management authentication using the
existing `local auth reprovision` procedure. Then inspect workers with the new
eligible operator. Restore retains worker IDs/UIDs and revocation status, clears
effective grants and marks retained workers for explicit reprovisioning.

```sh
cpractl local worker-auth reprovision worker-a \
  --data-dir /var/lib/cpra --actor team/restored-oncall \
  --grants-file /etc/cpra/worker-a.grants.json \
  --token-output /etc/cpra/worker-credentials/worker-a-restored.token
```

Reprovisioning refreshes the verifier and both revisions while preserving an
existing worker UID. Each remaining reset worker still needs its own explicit
reprovisioning. New IDs in a restored store also use `reprovision`, rather than
ordinary issuance. `rotate` and `set-grants` reject a worker that is still reset;
they become available for that worker after reprovisioning. Revoked IDs remain
revoked. The immediately pre-restore verifier is retained only as a deny record
and cannot be reintroduced by later rotation in that restored epoch.
