# Native service lifecycle verification

`service_lifecycle.py` runs actual local installation and supervisor commands.
Use it only on a disposable runner. Its production service name and default
paths are intentional: the test checks the same integration an operator uses.
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
The manifest remains intentionally empty. The readiness command contacts the
real API and no monitor/provider jobs are configured or dispatched.

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

Reports are written after every completed assertion and on failure. Cleanup
errors fail the run. The adjacent log records commands and bounded output, with
tokens kept in files rather than command arguments. Publish passing native-host
reports only for the exact artifact hashes tested; retain failed reports when
diagnosing a candidate.
