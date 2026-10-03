# Native service lifecycle verification

`service_lifecycle.py` runs actual local installation and supervisor commands.
System tests require a disposable runner. A local Linux user test is available
with the explicit mode below. Its production service name and default paths are
intentional: the test checks the same integration an operator uses.
It refuses existing CPRa configuration, state, executables, service registration,
package units, backup directories, and identities it would otherwise create.

The input directory must contain the candidate `cpra`, `cpractl`, and matching
`RELEASE.json`. Python 3.11 or newer is required. The script verifies application
version and full commit before installation, and records both executable hashes.

On Linux or macOS GitHub Actions runners, run the system test elevated:

```sh
sudo env GITHUB_ACTIONS=true CI=true python3 scripts/native/service_lifecycle.py \
  --stage "$PWD/stage" --scope system --allow-system-changes \
  --out "$PWD/evidence/service-system.json"
```

Run the user test separately as the logged-in runner user, without `sudo`:

```sh
python3 scripts/native/service_lifecycle.py \
  --stage "$PWD/stage" --scope user --allow-system-changes \
  --out "$PWD/evidence/service-user.json"
```

Linux user tests explicitly provision a user service manager and lingering only
when required, then restore the previous state. The local installer itself does
not enable lingering. macOS user tests require an actual graphical launchd
session; an absent session fails the test. The macOS system test creates a hidden
dedicated `_cpra_ci` account/group using an unused UID/GID and deletes only those
new identities during cleanup.

On an elevated Windows GitHub Actions runner:

```powershell
python3 scripts/native/service_lifecycle.py --stage stage --scope system `
  --allow-system-changes --out evidence/service-windows.json
```

Windows uses actual SCM registration with LocalService and CPRa's service SID.
User foreground execution is covered separately by
`scripts/release/native_smoke.py`; Windows has no CPRa user SCM service.

The lifecycle test checks fresh installation remaining stopped, authenticated
readiness, the running service identity, executable and state permissions,
operator configuration edits, running and stopped updates, retained startup
mode, deliberate stop without automatic restart, uninstall, reinstall, and a
stopped complete-directory backup restored into the real service state path.
On Linux it also forcibly terminates the managed main process and verifies a new
systemd-managed owner, authenticated readiness and the original Raft identity.
The manifest remains intentionally empty. The readiness command contacts the
real API and no monitor/provider jobs are configured or dispatched.

Unix permission observations include the executable and its parent directory,
configuration directory, starter monitor/runtime files, API token, state
directory, and supervisor definition. The report retains observed UID, GID, mode
and file type at initial startup, after the running update, and after restoration.
System-service tests also execute access checks as the actual service account:
state must be writable; administrator-managed configuration, executables and
their directories must not be. A summary containing only an executable owner
does not qualify this permission contract.

Updates replace the service's managed executable using the exact staged
candidate. This establishes installer stop/copy/start behavior and preservation
of configuration and state. It does **not** establish compatibility with every
older released storage format; cross-version upgrades require separate evidence.

For supplemental Linux validation, a disposable Docker/Podman container can use
`--isolated-container` in place of CI markers. The container must have systemd as
PID 1 and real container identity markers. Keep a private cgroup namespace and
do not mount the host cgroup tree or service/configuration directories. Such a
run reports `execution_environment: linux-systemd-container`; it cannot satisfy
the native-host release gate.

For private Linux user verification outside CI, use an already-running user
service manager and an unused CPRa installation:

```sh
python3 scripts/native/service_lifecycle.py \
  --stage /absolute/path/to/stage --scope user --local-user \
  --allow-system-changes --out /absolute/path/to/evidence/service-user.json
```

This mode refuses root, system scope, other operating systems, containers, and
existing CPRa resources. It does not start a user manager or enable lingering.
It creates, enables, tests and removes the CPRa user service; it removes only the
configuration, state, executable, backups and temporary sources created by this
run. It requires a new report and adjacent log so an earlier result is preserved.

Unfrozen inputs require matching explicit candidate metadata and version output.
Their report contains a diagnostic metadata fingerprint, no release identity,
and `release_qualified: false`. The ordinary CI path still rejects unfrozen
inputs. A local passing result cannot stand in for all native platforms, a
frozen artifact, a package upgrade or an independent review.

Linux reports identify the mounted filesystem through `findmnt`, retain the
filesystem magic type separately, and record actual supervisor settings. Cleanup
confirms that owned paths and the supervisor registration are absent before an
owned user manager is stopped. No manager environment values are logged.

Reports are written after every completed assertion and on failure. Cleanup
errors fail the run. The adjacent log records commands and bounded output, with
tokens kept in files rather than command arguments. Publish passing native-host
reports only for the exact artifact hashes tested; retain failed reports when
diagnosing a candidate.

## Two-version storage upgrades

`upgrade.py` runs two separately built CPRa executables and their matching
`cpractl` executables against local HTTP targets. It currently requires native
Linux, Python 3 and OpenSSL. It runs foreground processes without installing a
service or changing an existing installation. Supply reviewed source identities
and the same compiler, driver tags and build settings for both versions.

Create one private temporary parent. The fixture directory and report must not
already exist:

```sh
CPRA_UPGRADE_WORK=$(mktemp -d)
python3 scripts/native/upgrade.py \
  --previous-binary /absolute/path/previous/cpra \
  --previous-cli /absolute/path/previous/cpractl \
  --candidate-binary /absolute/path/candidate/cpra \
  --candidate-cli /absolute/path/candidate/cpractl \
  --work-dir "$CPRA_UPGRADE_WORK/fixtures" \
  --out "$CPRA_UPGRADE_WORK/upgrade.json"
```

The fixture uses verified TLS and a named operator. It creates one monitor, a
recovery action and two notification endpoints through the public API. One
endpoint completes while recovery and the other notification remain started.
After acknowledging the incident, the fixture forcibly terminates the previous
owner. It repeats the scenario with log-only recovery and with a completed
snapshot plus later log entries.

The candidate must preserve node, monitor, incident and action identities,
acknowledgement ownership and retained event ordering. Interrupted actions become
unknown. Repeated failed checks must leave the completed endpoint succeeded and
must not repeat either unknown action. Independent target counters detect extra
recovery or notification requests.

Stopped-backup checks include rejection while an owner holds the database lock,
restoration of the previous version's backup, authentication reprovisioning and
rejection of the old token after restore. The previous administration tool must
restore its own backup successfully and reject the candidate's incompatible
inventory. The previous server must refuse upgraded state without calling a
provider. These controls distinguish an incompatible upgrade from a broken
previous executable or restore command.

New built-in observations use state format 38; observations from optional
external workers use format 39. Historical log envelopes retain their original
transition semantics. Opening old state is supported by the new reader;
reopening state written by the new observer requires a compatible reader.
Retain the previous stopped backup and matching administration tool if rollback
may be necessary.

The schema-2 report records both executable hashes and version output, initial
committed identities and history, the fixture source hash, each scenario's
actual results and `release_gate_passed: false`. It copies process logs to
`upgrade.json.inputs/` with restrictive permissions and records their sizes and
hashes. Token files, keys, configuration and databases are excluded. Retain the
report and this log directory together. Development binaries do not
qualify a frozen release. This fixture also does not establish system-service,
package, Windows, macOS, other architecture, filesystem or power-loss behavior.

For frozen artifacts, validate the staged executables against their native
archives before running the fixture:

```sh
python3 -B scripts/release/storage_evidence.py --check-executables \
  --release /private/candidate/RELEASE.json \
  --previous-release /private/previous/RELEASE.json \
  --distribution /private/candidate-archives \
  --previous-distribution /private/previous-archives \
  --candidate-stage /private/candidate \
  --previous-stage /private/previous \
  --out /private/upgrade-inputs.json
```

`storage_evidence.py` then reads the original report and logs with `--campaign`
instead of the stage arguments. It compares all four executable hashes with the
selected archives and checks incident ownership, immutable history, held unknown
actions and independent target counts. An older schema-1 diagnostic report is
not frozen release evidence. Missing native platforms remain explicit, and this
fixture does not qualify same-format upgrades or optional worker state.

The private release workflow accepts an optional `previous_run_id` for these
Linux amd64/arm64 checks. It checks the previous dispatch, repository, source and
successful build before invoking either owner. The previous workflow as a whole
need not have passed its release gates. An omitted run leaves storage-upgrade
qualification pending. Publication remains locked.

The caller must retain the report and copied non-secret logs, then remove
the temporary parent. Fixture state includes disposable authentication keys,
tokens and certificates; do not publish or preserve the entire directory as
release evidence. Apply a disk quota to the caller's temporary workspace and
remove compiler/build caches after the run.
