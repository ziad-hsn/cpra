# Native installation and durable service operations

CPRa provides a Go installation route, native server/client archives, and Linux
DEB/RPM packages. The service installer copies the chosen executable to a stable
location. Updating a developer's `GOBIN` does not replace a running service.
Installation and update first copy the candidate into a private temporary
directory under the managed configuration directory. The installer validates
and installs that same copy, so a change to the original path cannot replace
the validated executable. It removes the temporary copy on completion or
failure and reports cleanup errors.
The [release recipe](release-engineering.md) defines artifact identity and
verification; the [container and Helm guide](container-helm.md) covers volumes
and Kubernetes ownership.

## Go installation and capabilities

Replace `VERSION` with an actual published version from a verified release:

```sh
go install github.com/ziad-hsn/cpra@VERSION
go install github.com/ziad-hsn/cpra/cmd/cpractl@VERSION
```

Go 1.26 source compatibility is tested separately from the supported compiler
used for official binaries. The embedded dashboard and starter files require
no Node, pnpm, Git, or generation step during installation. Plain Go installation
retains the default driver subset. To compile every applicable driver:

```sh
go install -tags 'redis postgres mysql mongo rabbitmq kafka kubernetes aws systemd teams twilio' github.com/ziad-hsn/cpra@VERSION
cpra -capabilities
cpra -validate -yaml /absolute/path/monitors.yaml -runtime-config /absolute/path/runtime.yaml
```

The systemd recovery driver is Linux-only, including when the tag is supplied on
another OS. Capability inspection reports available and omitted drivers.
Validation checks configuration and compiled capabilities before opening Raft
or constructing provider clients; it does not prove credential or target access.
Local initialization intentionally writes an empty manifest, requiring
`-allow-empty` until monitors are added.

## Path contract

`cpractl local paths --scope user` reports the actual resolved paths. Use
`--scope system` to inspect machine-service paths without creating anything.

| Mode | Configuration | State |
| --- | --- | --- |
| Linux system | `/etc/cpra` | `/var/lib/cpra` |
| Linux user | `$XDG_CONFIG_HOME/cpra` | `$XDG_STATE_HOME/cpra` |
| macOS system | `/Library/Application Support/CPRa/config` | `/Library/Application Support/CPRa/state` |
| macOS user | `~/Library/Application Support/CPRa/config` | `~/Library/Application Support/CPRa/state` |
| Windows system | `%ProgramData%\CPRa\config` | `%ProgramData%\CPRa\state` |
| Windows user foreground | `%LocalAppData%\CPRa\config` | `%LocalAppData%\CPRa\state` |

Linux ignores relative XDG settings and falls back to `~/.config/cpra` and
`~/.local/state/cpra`. Windows resolves Known Folders through the Windows API.
These follow [XDG](https://specifications.freedesktop.org/basedir/latest/),
[FHS persistent state](https://refspecs.linuxfoundation.org/FHS_3.0/fhs/ch05s08.html),
[Apple Application Support](https://developer.apple.com/library/archive/documentation/FileManagement/Conceptual/FileSystemProgrammingGuide/MacOSXDirectories/MacOSXDirectories.html),
and [Windows Known Folders](https://learn.microsoft.com/en-us/windows/win32/api/shlobj_core/nf-shlobj_core-shgetknownfolderpath).

Storage precedence is explicit `-data-dir`, explicit `storage.directory` in the
runtime configuration, then the platform user default. `-yaml` and its `-config`
alias still select the monitor manifest. Services pass absolute paths; an
elevated foreground process does not silently select system-service paths.
Memory mode remains explicit through `storage.mode: memory`.

When a legacy `./cpra-data` exists and neither explicit storage option is set,
startup refuses to select a new empty store. Continue using it with
`-data-dir /absolute/path/cpra-data`, or stop the old owner and migrate the
complete directory using the backup/restore procedure below.

## Initialize a local encryption key

Persistent management and file-configured A2A actions require a wrapping source.
For the local backend, create a new key with `cpractl local encryption init`.
Both the key path and the state-directory boundary must be explicit and absolute.
Use `cpractl local paths` to identify the installation's actual locations.

For a Linux user installation using the default XDG locations, initialize its
configuration directory first, then create and inspect the key:

```sh
cpractl local init
cpractl local encryption init \
  --key-file "$HOME/.config/cpra/keys/wrapping.key" \
  --data-dir "$HOME/.local/state/cpra"
cpractl local encryption inspect \
  --key-file "$HOME/.config/cpra/keys/wrapping.key" \
  --data-dir "$HOME/.local/state/cpra"
```

When XDG paths are overridden, use the absolute locations returned by
`local paths`. The immediate key directory may be created; its parent must exist and
satisfy the native ownership checks. On Linux and macOS, user keys are owner-only
files in private directories. Existing keys, directories, permissions and
configuration are never replaced or repaired. Key generation rejects symlink
paths and keys inside the state directory, including aliases of that directory.

For an installed Linux system service, provision as an administrator with the
service's actual group. The `cpra` account must already exist:

```sh
sudo cpractl local encryption init \
  --key-file /etc/cpra/keys/wrapping.key \
  --data-dir /var/lib/cpra \
  --reader-group-id "$(id -g cpra)"
sudo cpractl local encryption inspect \
  --key-file /etc/cpra/keys/wrapping.key \
  --data-dir /var/lib/cpra \
  --reader-group-id "$(id -g cpra)"
```

The shared Unix policy creates a root-owned key readable by that group; the
service cannot write it. For a macOS system service, select its actual service
group and the macOS paths instead. On Windows, use the paths returned by
`local paths --scope system` and pass `--reader-sid` with the registered service SID
from `sc.exe showsid CPRa`, in an elevated terminal. Windows creates protected
DACLs with service read access. User foreground installations omit both reader
options. `--scope` does not infer a reader identity, and reader options for the
wrong operating system are rejected. Native platform qualification remains a
separate release gate.

Both commands emit JSON containing only `key_id`, a non-secret identity. They
never print the 32 binary key bytes, contact a CPRa API, open Raft, or activate
runtime settings. If initialization is interrupted or returns an output error,
use `inspect` with the same paths and reader options. Preserve any existing key;
initialization never overwrites it. Successful inspection validates the file's
contents and protection, not its ability to decrypt an existing store.

Configure the generated path under `storage.encryption.local.active_key_file`
or `management.encryption.local.active_key_file`, with the same reader policy.
Configure exactly one source, as described in [management startup](management-startup.md).
Changing the active wrapping key is an explicit configuration change: generate a
new file, retain old paths in `previous_key_files`, then validate and restart.
Selecting a new active key does not re-encrypt earlier records. Keep old keys
while retained state or backups need them, and back up keys separately from the
state directory.

## Initialize and install a service

```sh
cpractl local init
cpractl local service render
cpractl local service install --binary /absolute/path/to/cpra
```

Initialization creates absent `monitors.yaml`, `runtime.yaml`, and a random
`auth.token`. It preserves existing files and does not copy credentials from
the installing user's environment. The native installer creates a supervisor
definition but leaves a fresh service stopped. Review configuration and provide
the intended credential files before starting it.

For a Linux system service, use an administrator terminal:

```sh
sudo cpractl local service install --scope system --binary /absolute/path/to/cpra
sudo systemctl enable --now cpra.service
sudo cpractl --server http://127.0.0.1:8060 --allow-insecure-http --token-file /etc/cpra/auth.token ready
```

The local installer uses `/usr/local/lib/cpra/cpra`; DEB/RPM use `/usr/bin/cpra`.
The system account `cpra` has no interactive login. Executables, configuration,
and service definitions are administrator-managed; the service account can
read configuration and write state. Local tooling records its ownership in
`install.json` and refuses package-managed or unrecorded existing files.
Use the same installation route for upgrades.

User services use `~/.local/lib/cpra/cpra` and the XDG systemd user unit directory:

```sh
systemctl --user enable --now cpra.service
```

Linux initialization and stopped service installation/update repair the modes of
managed objects while preserving their contents. User configuration, state, log,
and executable directories use `0700`; starter configuration and token files use
`0600`, the executable uses `0700`, and the user unit uses `0600`. System state
belongs to the service account with `0700` directories and `0600` files.
Administrator-owned system configuration uses `0750`/`0640`, the executable
directory and binary use `0755`, and the system unit uses `0644`.

Existing stores must be stopped and exclusively locked before permission repair.
Linked managed paths and non-regular state entries are rejected. The installer
changes only its managed configuration/executable objects and state/log trees;
provider helper scripts retain their modes and shared parent directories remain
under operator administration. Ordinary application startup does not recursively
change ownership or permissions.

User lingering is an explicit system administration decision. It is not enabled
by the installer. Units use `Type=notify`, bounded restart frequency,
`Restart=on-failure`, a 60-second stop allowance, `KillMode=mixed`, and private
state permissions. Readiness notification follows controller initialization.
See [systemd service semantics](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html)
and [termination behavior](https://www.freedesktop.org/software/systemd/man/latest/systemd.kill.html).

On macOS, user installation creates a LaunchAgent in the current user's session;
a system LaunchDaemon requires an existing dedicated non-root account (default
`_cpra`, or `--account NAME`). The installer does not create directory-service
identities. Use an absolute executable path. LaunchAgents are session services;
they do not promise operation while the user is logged out.

Start or deliberately unload a user LaunchAgent in the logged-in session:

```sh
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/io.github.ziad-hsn.cpra.plist"
launchctl bootout "gui/$(id -u)/io.github.ziad-hsn.cpra"
```

For a system LaunchDaemon:

```sh
sudo launchctl bootstrap system /Library/LaunchDaemons/io.github.ziad-hsn.cpra.plist
sudo launchctl bootout system/io.github.ziad-hsn.cpra
```

Deliberate unloading prevents failure-restart behavior from relaunching the job
while it is under maintenance.

On Windows, run system installation in an elevated terminal. It creates the
native `CPRa` SCM service, normally under `NT AUTHORITY\LocalService`, with a
service SID and ACLs separating administrator-managed executables/configuration
from writable state. `--account` supports an existing account that does not need
an installer-supplied password; password-bearing accounts require separate SCM
administration. User mode supports foreground execution, not an SCM user service.
Use `Start-Service CPRa` and `Stop-Service CPRa` for deliberate control.
Sigstore verification does not imply Windows Authenticode or Apple notarization.

## Readiness, shutdown and privileges

```sh
cpractl --server http://127.0.0.1:8060 --allow-insecure-http --token-file /absolute/path/auth.token --request-timeout 2s ready
cpractl --server http://127.0.0.1:8060 --allow-insecure-http --token-file /absolute/path/auth.token --request-timeout 2s health
```

These commands explicitly permit bearer authentication over the local loopback
HTTP listener. Remote clients use HTTPS. The commands only make API requests;
they do not load manifests, open Raft,
or invoke a provider. Readiness requires initialized admission, controller
progress and available storage. Explicit empty configurations can be ready.
Provider outages and unknown actions do not make the process dead. Dashboard
projection freshness is reported separately from storage health.

On termination CPRa marks readiness unavailable, stops admission, drains or
cancels accepted work within its budget, commits known outcomes and closes its
dependencies. Diagnostics remain available while the controller drains.
The Unix/container application budget defaults to 45 seconds inside a
60-second supervisor allowance. Windows service handling caps this at 15 seconds
and does not modify global shutdown timeouts. Deadline expiry is an error;
interrupted started actions remain unknown after restart. A manual successful
stop does not trigger an automatic failure restart.

No default installation grants Docker socket access, systemd recovery rights,
privileged containers, arbitrary Kubernetes RBAC, or access to the installing
user's cloud profiles. Test required files, certificate stores, local sockets,
ICMP and helpers as the actual service identity before enabling those drivers.

## Stopped backup, restore and upgrade

Stop the owning process or supervisor before backing up. On Kubernetes, first
enter maintenance and wait until the old owner has exited and released storage;
account for any GitOps reconciler. A live copy is not an application-consistent
backup, and a Raft snapshot alone does not include the retained timeline.

```sh
cpractl local backup --data-dir /absolute/path/state --output /absolute/path/backup --config /absolute/path/monitors.yaml --backup-auth-key /absolute/path/backup-keys/authentication.key
cpractl local restore --backup /absolute/path/backup --data-dir /absolute/path/restored-state --backup-auth-key /absolute/path/backup-keys/authentication.key
```

Both destination directories must be absent and their parents must exist.
Backup retains the exclusive database lock, validates storage format and
history, copies every state file, and records SHA-256 hashes in `BACKUP.json`.
Format 2 authenticates the complete inventory and its metadata with HMAC-SHA256.
New inventories record `state_format`, reconstructed from the committed snapshot
and subsequent Raft commands. It is distinct from `storage_format`, which
identifies the base storage container, and from the maximum format supported by
the executable. Restore rejects an authenticated `state_format` that disagrees
with the backed-up state before publishing a destination. Existing authenticated
format-2 inventories without this field still undergo complete offline validation.
The field is covered by the manifest MAC; removing it does not bypass authentication.
Older administration tools may reject the additional field. Retain the matching
tool and pre-upgrade backup. This metadata does not establish rollback compatibility:
the candidate must validate the complete logs, snapshots and retained history.
Provision a separate cryptographically random 32-byte binary authentication key
through your secret-management arrangement. Its path must be absolute and outside
both the state and backup/destination directories, including aliases. The key is
never copied into the backup. Keep it with independent access controls and retain
the matching key for every backup you need to restore; use a separate key from
management encryption keys. File access uses the native protected-file policy:
owner-only files/directories on Linux and a protected owner/administrator DACL on
Windows. The unreleased macOS adapter retains the Unix owner/mode policy, permits
deny-only ACLs, and rejects grants or unavailable extended-permission metadata.
Its native compilation, permission, crash and backup/restore checks remain pending;
macOS protected backup support is not yet qualified. See the
[macOS implementation checkpoint](implementation/macos-protected-files.md).
Other Unix platforms still reject protected key access.

Restore requires that independently trusted key before reading or publishing state.
Changing data and recomputing the file hashes cannot authenticate a replacement
manifest. Unsigned format 1 backups are rejected; create a new authenticated backup
from the original stopped store. There is no automatic unsigned fallback. Manifest
authentication proves possession of the key, not that a backup is the newest one;
choose the intended recovery point separately.

The inventory includes node identity, Raft database, snapshots, history catalog,
and retained segments. It records the running tool's artifact identity and an
optional matching configuration fingerprint; keep the exact runtime
configuration and source artifact identity with the backup. Credentials remain
in a separate protected arrangement.

An unfinished configuration migration is not accepted as a complete backup.
Resume its original encrypted staging data, complete activation, then stop the
owner and retry the backup. Offline validation checks committed snapshot and log
state without opening Raft, activating staged input, decrypting secrets, or
invoking providers; it does not treat an uncommitted or rejected activation as
completed migration.

Restore verifies the inventory and storage before publishing a new directory.
It never overwrites an existing store or invokes a recovery action. Restore as
the intended owner, or explicitly assign the newly restored directory to that
owner while stopped. Resume with matching configuration and explicit data path;
check readiness, history and unknown outcomes. A backup may still contain a
started action: recovery conservatively marks it unknown.

A stopped backup can also contain an admitted, unfinished deletion of an old
collection ledger generation. Explicit restore finishes that one deletion in
its authenticated private copy before publishing the destination. It protects
the current and previous generations, verifies the copied files, and updates
the generation catalog before removing the cleanup record. The backup remains
unchanged. Incomplete or unverifiable cleanup records fail restore; ordinary
startup does not reinterpret copied filesystem identities or delete unknown
files. Backup inspection itself performs no cleanup.

Restore also starts a new authentication epoch. Keep the owner stopped, inspect
the restored policy, and provision fresh operator access before starting it:

```sh
cpractl local auth list --data-dir /absolute/path/restored-state
cpractl local auth reprovision oncall --role operator --data-dir /absolute/path/restored-state --token-output /absolute/path/operator-access/new.token
```

The token output must be a new protected file outside the state directory. Check
that the command reports a committed revision and that a subsequent `auth list`
observes that revision with `resetRequired: false`. An uncertain commit requires
inspection of its original revision and token file; do not issue another token
automatically. The previous named tokens and startup read token must not regain
access. See [local authentication](local-authentication.md).

Retain the matching runtime configuration, TLS sources and encryption wrapping
key separately from the complete state backup. Service installation repairs
restored state ownership while the owner is stopped. Verify the fresh principal
over authenticated TLS after startup, as well as readiness and recovered state.

Local installer upgrades validate and retain the backup authentication key before
running the candidate or stopping the old service, then take a complete sibling
backup when state exists, replace its managed executable,
and restart only if the service was previously running:

```sh
cpractl local service update --binary /absolute/path/new-cpra --backup-auth-key /absolute/path/backup-keys/authentication.key
cpractl local service uninstall
```

Use `--scope system` consistently for system installations. A failed update
leaves an explicit error and may leave the service stopped for inspection.
Uninstall removes recorded executable/service files while preserving state,
configuration, backups and the service account. Package lifecycle details,
including Debian purge's preserved configuration copy, are documented in the
[release guide](release-engineering.md).

Rollback requires a binary that understands the current storage format.
Reinstalling an older package or using Helm rollback does not reverse committed
state or external interventions. Native crash/restart tests establish the
tested OS/filesystem boundary; they do not claim universal power-loss durability.

New monitor configuration, check and action transitions use delivery format 40;
combining this state with optional external-worker state uses format 41. The
recorded format determines replay behavior: supported earlier commands retain
their original transition semantics. The reader must support both the stored
format and any optional worker state. An older executable must reject a state
format it does not understand. Restore
the previous stopped backup with a compatible administration tool before
attempting such a rollback. Do not delete the newer store or silently replace
it with an empty directory.

## Verification boundary

The release workflow distinguishes native execution from cross compilation and
emulation. A platform is release-qualified only after its native artifact and
service lifecycle gates pass. Refer to the candidate's evidence for OS versions,
architectures, filesystems and tool versions. Production provider accounts and
comparative performance remain separate evidence gates. The 24-hour endurance
test is outside the requested work and is not a mandatory completion gate.

The separate two-version storage fixture uses different server and CLI
executables, real committed logs, snapshots and stopped backups. It verifies
delivery preservation, unknown-action holds, acknowledged incident ownership,
retained history, independent provider request counts and authentication reset
after restore. Its Linux foreground result is separate from native service
and package lifecycle evidence. The report names the exact tested executable
hashes; development builds remain unqualified for release.

The service lifecycle fixture uses an isolated OpenSSL certificate, verified TLS,
and a named operator from its first start. It records the OpenSSL version and
executable hash; `--openssl` can select that fixture tool explicitly. Tests preserve
protected configuration through running/stopped updates and removal, then verify
a new authentication epoch and principal after restore and HTTP 401 rejection of
both old credential sources. The fixture contains no monitors and performs no
provider operations. Python fixture tests do not qualify native supervisors;
the actual staged release must complete this flow on each claimed platform.

The fixture also offers an explicit Linux `--local-user` mode for private
verification with an existing user service manager. It refuses existing CPRa
resources and never enables lingering or provisions a user session. Linux runs
exercise forced process termination and systemd recovery in addition to the
installation/update/removal and authenticated restore sequence. Unfrozen input
reports remain diagnostic and cannot qualify a release. From the source checkout,
with an unused CPRa installation and matching staged executables/metadata:

```sh
python3 scripts/native/service_lifecycle.py \
  --stage /absolute/path/to/stage --scope user --local-user \
  --allow-system-changes --out /absolute/path/to/evidence/service-user.json
```

The report and adjacent log must be new paths. The fixture tests and removes only
the integration, configuration, state, backups and temporary sources it creates.
The source file `scripts/native/README.md` describes the CI and platform modes.
