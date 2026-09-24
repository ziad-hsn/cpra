---
title: Candidate · Native installation and durable service operations
description: Candidate · Native installation and durable service operations for the reviewed CPRa source; see the version and availability notice.
cpra_scope: release_candidate
---

> **Release candidate:** this page describes [`410fbfb`](https://github.com/ziad-hsn/cpra/commit/410fbfb0092d01277b3884cd04151c27443a4226), which is separate from `main`. See [version and availability](../versions.md).

# Native installation and durable service operations

CPRa provides a Go installation route, native server/client archives, and Linux
DEB/RPM packages. The service installer copies the chosen executable to a stable
location. Updating a developer's `GOBIN` does not replace a running service.
The [release recipe](release-engineering.md) defines artifact identity and
verification; the [container and Helm guide](container-helm.md) covers volumes
and Kubernetes ownership.

## Go installation and capabilities

Replace `VERSION` with an actual published version from a verified release:

```sh
go install github.com/ziad-hsn/cpra@VERSION
go install github.com/ziad-hsn/cpra/cmd/cpractl@VERSION
```

Go 1.25 source compatibility is tested separately from the supported compiler
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
sudo cpractl --token-file /etc/cpra/auth.token ready
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
cpractl --server http://127.0.0.1:8060 --token-file /absolute/path/auth.token --request-timeout 2s ready
cpractl --server http://127.0.0.1:8060 --token-file /absolute/path/auth.token --request-timeout 2s health
```

These commands only make API requests. They do not load manifests, open Raft,
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
cpractl local backup --data-dir /absolute/path/state --output /absolute/path/backup --config /absolute/path/monitors.yaml
cpractl local restore --backup /absolute/path/backup --data-dir /absolute/path/restored-state
```

Both destination directories must be absent and their parents must exist.
Backup retains the exclusive database lock, validates storage format and
history, copies every state file, and records SHA-256 hashes in `BACKUP.json`.
The inventory includes node identity, Raft database, snapshots, history catalog,
and retained segments. It records the running tool's artifact identity and an
optional matching configuration fingerprint; keep the exact runtime
configuration and source artifact identity with the backup. Credentials remain
in a separate protected arrangement.

Restore verifies the inventory and storage before publishing a new directory.
It never overwrites an existing store or invokes a recovery action. Restore as
the intended owner, or explicitly assign the newly restored directory to that
owner while stopped. Resume with matching configuration and explicit data path;
check readiness, history and unknown outcomes. A backup may still contain a
started action: recovery conservatively marks it unknown.

Local installer upgrades validate the candidate, stop the old service, take a
complete sibling backup when state exists, replace its managed executable,
and restart only if the service was previously running:

```sh
cpractl local service update --binary /absolute/path/new-cpra
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

## Verification boundary

The release workflow distinguishes native execution from cross compilation and
emulation. A platform is release-qualified only after its native artifact and
service lifecycle gates pass. Refer to the candidate's evidence for OS versions,
architectures, filesystems and tool versions. Production provider accounts and
the one-million-monitor, 24-hour campaign remain separate evidence gates.
