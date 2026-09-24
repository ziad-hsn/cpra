#!/usr/bin/env python3
"""Native local-installer lifecycle gate for disposable GitHub Actions machines.

This deliberately uses the production service name and platform paths. It refuses
existing CPRa resources and requires both CI markers and an explicit mutation flag.
System tests run elevated; Linux/macOS user tests run in the actual user session.
No provider transport is replaced: the generated manifest contains zero monitors.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import re
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


LABEL = "io.github.ziad-hsn.cpra"
ACCOUNT = "_cpra_ci"


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def inventory(root):
    return {str(p.relative_to(root)): digest(p) for p in sorted(root.rglob("*")) if p.is_file()}


def psquote(value):
    return "'" + str(value).replace("'", "''") + "'"


class Harness:
    def __init__(self, args):
        self.args = args
        self.os = platform.system()
        self.scope = args.scope
        self.stage = args.stage.resolve(strict=True)
        suffix = ".exe" if self.os == "Windows" else ""
        self.binary = self.stage / ("cpra" + suffix)
        self.cli = self.stage / ("cpractl" + suffix)
        self.env = dict(os.environ)
        self.env["LC_ALL"] = "C"
        for name in ("CPRA_AUTH_TOKEN", "CPRA_AUTH_TOKEN_FILE", "CPRA_SERVER", "CPRA_ENTITY_THRESHOLD"):
            self.env.pop(name, None)
        self.record = {"status": "failed", "scope": f"native {self.scope} service lifecycle",
                       "os": self.os, "architecture": platform.machine(), "os_version": platform.platform(),
                       "steps": [], "provider_operations": 0, "manifest_monitor_count": 0,
                       "update_boundary": "same candidate replaces its managed executable; cross-version storage compatibility is a separate gate"}
        self.owned = False
        self.created_account = False
        self.created_group = False
        self.user_manager_started = False
        self.enabled_linger = False
        self.fixture = None
        self.backup_key = None
        self.layout = None
        self.roots = []

    def run(self, argv, *, acceptable=(0,), timeout=120, log=True):
        argv = [str(arg) for arg in argv]
        result = subprocess.run(argv, env=self.env, text=True, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, timeout=timeout, check=False)
        if log:
            with self.args.out.with_suffix(".log").open("a", encoding="utf-8") as stream:
                stream.write(json.dumps(argv) + f" -> {result.returncode}\n" + result.stdout[-12000:] + "\n")
        if result.returncode not in acceptable:
            raise RuntimeError(f"{Path(argv[0]).name} failed ({result.returncode}): {result.stdout[-4000:]}")
        return result

    def ps(self, body):
        script = "$ErrorActionPreference = 'Stop'; " + body
        encoded = base64.b64encode(script.encode("utf-16le")).decode("ascii")
        return self.run(["powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded]).stdout.strip()

    def ctl(self, *args, acceptable=(0,)):
        return self.run([self.cli, "local", "--scope", self.scope, *args], acceptable=acceptable)

    def systemctl(self, *args, acceptable=(0,)):
        return self.run(["systemctl", *( ["--user"] if self.scope == "user" else []), *args], acceptable=acceptable)

    @property
    def domain(self):
        return "system" if self.scope == "system" else f"gui/{os.getuid()}"

    @property
    def target(self):
        return self.domain + "/" + LABEL

    def mark(self, name, **details):
        self.record["steps"].append({"name": name, "status": "pass", **details})
        print(f"PASS: {name}", flush=True)
        self.save()

    def save(self):
        self.args.out.write_text(json.dumps(self.record, indent=2) + "\n", encoding="utf-8")

    def guard(self):
        container_file = Path("/run/systemd/container")
        container = self.os == "Linux" and (Path("/.dockerenv").is_file() or Path("/run/.containerenv").is_file())
        if self.args.isolated_container:
            if not container or not container_file.is_file() or container_file.read_text().strip() not in ("docker", "podman"):
                raise RuntimeError("--isolated-container requires a booted Linux Docker/Podman systemd container")
            self.record["execution_environment"] = "linux-systemd-container"
        else:
            if container or os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("CI") != "true":
                raise RuntimeError("requires CI=true and GITHUB_ACTIONS=true on a disposable native runner")
            self.record["execution_environment"] = "native-host"
        if not self.args.allow_system_changes:
            raise RuntimeError("requires explicit --allow-system-changes")
        if self.os not in ("Linux", "Darwin", "Windows"):
            raise RuntimeError("unsupported native supervisor platform")
        if self.os == "Windows":
            if self.scope != "system":
                raise RuntimeError("Windows user mode is foreground execution, not an SCM user service")
            elevated = self.ps("$p = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent(); $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)")
            if elevated != "True":
                raise RuntimeError("Windows SCM gate requires an elevated administrator runner")
        elif (os.geteuid() == 0) != (self.scope == "system"):
            raise RuntimeError("system scope requires root; user scope must run as the session user without sudo")
        metadata = json.loads((self.stage / "RELEASE.json").read_text(encoding="utf-8"))
        if not re.fullmatch(r"[a-f0-9]{40}", metadata["commit"]):
            raise RuntimeError("release metadata must contain the complete source commit")
        self.record.update({"version": metadata["version"], "commit": metadata["commit"],
                            "source_candidate": metadata.get("candidate", False), "source_dirty": metadata.get("dirty", False),
                            "binary_sha256": digest(self.binary), "cli_sha256": digest(self.cli)})
        for program, flag in ((self.binary, "-version"), (self.cli, "--version")):
            text = self.run([program, flag]).stdout
            if metadata["version"] not in text or metadata["commit"] not in text:
                raise RuntimeError("candidate executable identity does not match RELEASE.json")
        self.layout = json.loads(self.ctl("paths").stdout)
        self.config = Path(self.layout["config_dir"])
        self.state = Path(self.layout["state_dir"])
        self.bin_dir = Path(self.layout["bin_dir"])
        if not all(path.is_absolute() for path in (self.config, self.state, self.bin_dir)):
            raise RuntimeError("installer returned a relative persistent/executable path")
        if self.os == "Linux":
            if self.scope == "system":
                expected = (Path("/etc/cpra"), Path("/var/lib/cpra"))
            else:
                def xdg(name, fallback):
                    value = Path(self.env.get(name, ""))
                    return value if value.is_absolute() else Path.home() / fallback
                expected = (xdg("XDG_CONFIG_HOME", ".config") / "cpra", xdg("XDG_STATE_HOME", ".local/state") / "cpra")
        elif self.os == "Darwin":
            base = Path("/Library") if self.scope == "system" else Path.home() / "Library"
            expected = (base / "Application Support/CPRa/config", base / "Application Support/CPRa/state")
        else:
            base = Path(self.ps("[Environment]::GetFolderPath([Environment+SpecialFolder]::CommonApplicationData)")) / "CPRa"
            expected = (base / "config", base / "state")
        if (self.config, self.state) != expected:
            raise RuntimeError(f"installer paths violate the {self.os} {self.scope} location contract")
        self.installed = self.bin_dir / self.binary.name
        self.unit = Path(self.layout["service_file"]) if self.layout.get("service_file") else None
        self.roots = [self.config, self.state, self.bin_dir, self.state.with_name(self.state.name + ".pre-restore")]
        if self.layout.get("log_dir"):
            self.roots.append(Path(self.layout["log_dir"]))
        if self.os in ("Darwin", "Windows"):
            self.roots.append(self.config.parent)
        if self.unit:
            self.roots.append(self.unit)
        self.preexisting_backups = list(self.state.parent.glob(self.state.name + "-backup-*"))
        for path in self.roots + self.preexisting_backups:
            if path.exists() or path.is_symlink():
                raise RuntimeError(f"refusing pre-existing CPRa resource: {path}")
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 8060))
        if self.os == "Linux":
            if Path("/proc/1/comm").read_text().strip() != "systemd":
                raise RuntimeError("native systemd PID 1 required; container simulation is not this gate")
            if self.scope == "system":
                for command in (["getent", "passwd", "cpra"], ["getent", "group", "cpra"]):
                    if self.run(command, acceptable=(0, 2)).returncode == 0:
                        raise RuntimeError("refusing pre-existing cpra identity")
            for path in (Path("/usr/lib/systemd/system/cpra.service"), Path("/lib/systemd/system/cpra.service")):
                if path.exists():
                    raise RuntimeError("refusing installed CPRa package unit")
            self.prepare_linux_user_manager()
            state = self.systemctl("show", "cpra.service", "--property=LoadState", "--value", acceptable=(0, 1)).stdout.strip()
            if state not in ("not-found", ""):
                raise RuntimeError(f"refusing pre-existing systemd registration: {state}")
        elif self.os == "Darwin":
            self.run(["/bin/launchctl", "print", self.domain])
            existing = self.run(["/bin/launchctl", "print", self.target], acceptable=(0, 113, 3))
            if existing.returncode == 0:
                raise RuntimeError("refusing pre-existing CPRa launchd registration")
            disabled = self.run(["/bin/launchctl", "print-disabled", self.domain]).stdout
            if re.search(r'"' + re.escape(LABEL) + r'"\s*=>\s*true', disabled):
                raise RuntimeError("refusing pre-existing disabled CPRa launchd override")
            if self.scope == "system":
                for kind in ("Users", "Groups"):
                    names = self.run(["/usr/bin/dscl", ".", "-list", "/" + kind]).stdout.splitlines()
                    if ACCOUNT in names:
                        raise RuntimeError(f"refusing existing {ACCOUNT} identity")
        else:
            if self.ps("[bool](Get-Service -Name CPRa -ErrorAction SilentlyContinue)") != "False":
                raise RuntimeError("refusing pre-existing CPRa SCM registration")
        self.fixture = Path(tempfile.mkdtemp(prefix="cpra-native-service-")).resolve()
        self.mark("clean supervisor environment and source identity", paths=self.layout)

    def prepare_backup_key(self):
        # This independent test-only key stays in the disposable private fixture,
        # outside state and backup inventories. Its bytes never enter argv/logs.
        directory = self.fixture / "backup-keys"
        directory.mkdir(mode=0o700)
        self.backup_key = directory / "authentication.key"

        def protect_windows(path, is_directory):
            kind = "DirectorySecurity" if is_directory else "FileSecurity"
            flags = "ContainerInherit, ObjectInherit" if is_directory else "None"
            self.ps(
                "$owner = [Security.Principal.WindowsIdentity]::GetCurrent().User; "
                f"$acl = New-Object Security.AccessControl.{kind}; "
                "$acl.SetOwner($owner); $acl.SetAccessRuleProtection($true, $false); "
                "foreach ($sid in @($owner, [Security.Principal.SecurityIdentifier]::new('S-1-5-18'), "
                "[Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'))) { "
                "$rule = [Security.AccessControl.FileSystemAccessRule]::new($sid, "
                "[Security.AccessControl.FileSystemRights]::FullControl, "
                f"[Security.AccessControl.InheritanceFlags]'{flags}', "
                "[Security.AccessControl.PropagationFlags]::None, "
                "[Security.AccessControl.AccessControlType]::Allow); $acl.AddAccessRule($rule) }; "
                f"Set-Acl -LiteralPath {psquote(path)} -AclObject $acl")

        if self.os == "Windows":
            protect_windows(directory, True)
        with self.backup_key.open("xb") as stream:
            stream.write(os.urandom(32))
        if self.os == "Windows":
            protect_windows(self.backup_key, False)
        else:
            self.backup_key.chmod(0o600)
        if self.os == "Darwin":
            self.record["pending_backup_requirement"] = (
                "Darwin native ACL qualification for protected backup authentication keys is pending; "
                "the key reader currently fails closed")

    def prepare_linux_user_manager(self):
        if self.os != "Linux" or self.scope != "user":
            return
        uid = str(os.getuid())
        self.env["XDG_RUNTIME_DIR"] = f"/run/user/{uid}"
        self.env["DBUS_SESSION_BUS_ADDRESS"] = f"unix:path=/run/user/{uid}/bus"
        running = self.run(["systemctl", "is-active", f"user@{uid}.service"], acceptable=(0, 3, 4)).returncode == 0
        linger = self.run(["loginctl", "show-user", uid, "--property=Linger", "--value"], acceptable=(0, 1)).stdout.strip()
        if not running:
            if linger != "yes":
                self.run(["sudo", "-n", "loginctl", "enable-linger", os.environ["USER"]])
                self.enabled_linger = True
            self.run(["sudo", "-n", "systemctl", "start", f"user@{uid}.service"])
            self.user_manager_started = True
        self.systemctl("show-environment")

    def create_mac_account(self):
        if self.os != "Darwin" or self.scope != "system":
            return
        used = set()
        for kind, field in (("Users", "UniqueID"), ("Groups", "PrimaryGroupID")):
            for line in self.run(["/usr/bin/dscl", ".", "-list", "/" + kind, field]).stdout.splitlines():
                value = line.rsplit(None, 1)[-1]
                if value.isdigit():
                    used.add(int(value))
        number = next((n for n in range(400, 500) if n not in used), None)
        if number is None:
            raise RuntimeError("no unused dedicated fixture UID/GID in 400..499")
        group, user = f"/Groups/{ACCOUNT}", f"/Users/{ACCOUNT}"
        self.run(["/usr/bin/dscl", ".", "-create", group])
        self.created_group = True
        self.run(["/usr/bin/dscl", ".", "-create", group, "PrimaryGroupID", number])
        self.run(["/usr/bin/dscl", ".", "-create", user])
        self.created_account = True
        for field, value in (("UniqueID", number), ("PrimaryGroupID", number), ("UserShell", "/usr/bin/false"),
                             ("NFSHomeDirectory", "/var/empty"), ("IsHidden", "1"),
                             ("AuthenticationAuthority", ";DisabledUser;"), ("RealName", "CPRa CI service fixture")):
            self.run(["/usr/bin/dscl", ".", "-create", user, field, value])

    def status(self):
        if self.os == "Linux":
            text = self.systemctl("show", "cpra.service", "--property=ActiveState,UnitFileState,MainPID,LoadState", acceptable=(0, 1)).stdout
            fields = dict(line.split("=", 1) for line in text.splitlines() if "=" in line)
            return {"running": fields.get("ActiveState") in ("active", "activating", "reloading"),
                    "pid": int(fields.get("MainPID", "0")), "startup": fields.get("UnitFileState", ""),
                    "registered": fields.get("LoadState") not in ("not-found", None)}
        if self.os == "Windows":
            value = self.ps("$s=Get-CimInstance Win32_Service -Filter \"Name='CPRa'\"; if ($null -eq $s) { '{\"registered\":false,\"running\":false,\"pid\":0,\"startup\":\"\"}' } else { @{registered=$true;running=($s.State -ne 'Stopped');pid=$s.ProcessId;startup=$s.StartMode;account=$s.StartName} | ConvertTo-Json -Compress }")
            return json.loads(value)
        result = self.run(["/bin/launchctl", "print", self.target], acceptable=(0, 113, 3))
        pid = re.search(r"^\s*pid = (\d+)\s*$", result.stdout, re.MULTILINE)
        disabled = self.run(["/bin/launchctl", "print-disabled", self.domain]).stdout
        override = re.search(r'"' + re.escape(LABEL) + r'"\s*=>\s*(true|false)', disabled)
        return {"registered": result.returncode == 0, "running": bool(pid), "pid": int(pid.group(1)) if pid else 0,
                "startup": "disabled" if override and override.group(1) == "true" else "enabled"}

    def mode(self, enabled):
        if self.os == "Linux":
            self.systemctl("enable" if enabled else "disable", "cpra.service")
        elif self.os == "Windows":
            self.run(["sc.exe", "config", "CPRa", "start=", "demand" if enabled else "disabled"])
        else:
            self.run(["/bin/launchctl", "enable" if enabled else "disable", self.target])

    def start(self):
        if self.os == "Linux":
            self.systemctl("start", "cpra.service")
        elif self.os == "Windows":
            self.ps("Start-Service CPRa; (Get-Service CPRa).WaitForStatus('Running', [TimeSpan]::FromSeconds(60))")
        else:
            self.run(["/bin/launchctl", "bootstrap", self.domain, self.unit])
        self.wait_ready()

    def stop(self):
        former_pid = 0
        if self.os == "Linux":
            if self.status()["registered"]:
                self.systemctl("stop", "cpra.service")
        elif self.os == "Windows":
            self.ps("$s=Get-Service CPRa -ErrorAction SilentlyContinue; if ($s) { Stop-Service CPRa; $s.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(60)) }")
        elif self.status()["registered"]:
            former_pid = self.status()["pid"]
            self.run(["/bin/launchctl", "bootout", self.target])
        self.wait_stopped()
        # bootout can remove the registration before its old owner exits. Test
        # the real process lifetime before attempting stopped backup/restore.
        deadline = time.monotonic() + 65
        while former_pid:
            try:
                os.kill(former_pid, 0)
            except ProcessLookupError:
                break
            if time.monotonic() > deadline:
                raise RuntimeError("launchd removed the registration but its former process did not stop")
            time.sleep(.2)

    def wait_stopped(self):
        deadline = time.monotonic() + 65
        while self.status()["running"]:
            if time.monotonic() > deadline:
                raise RuntimeError("supervisor did not stop the owner within its allowance")
            time.sleep(.2)

    def request(self, path):
        token = (self.config / "auth.token").read_text().strip()
        req = urllib.request.Request("http://127.0.0.1:8060/api/v1/" + path, headers={"Authorization": "Bearer " + token})
        with urllib.request.urlopen(req, timeout=2) as response:
            return json.load(response)

    def wait_ready(self):
        deadline = time.monotonic() + 90
        while True:
            try:
                self.request("readyz")
                break
            except (urllib.error.URLError, TimeoutError, ConnectionError):
                if time.monotonic() > deadline:
                    raise RuntimeError("native service did not become ready")
                time.sleep(.25)
        self.run([self.cli, "--server", "http://127.0.0.1:8060", "--allow-insecure-http", "--token-file", self.config / "auth.token", "--request-timeout", "2s", "ready"])
        if self.request("overview").get("total") != 0:
            raise RuntimeError("fixture unexpectedly admitted monitors/provider operations")
        if not self.status()["running"]:
            raise RuntimeError("readiness response came from a process not managed by the tested supervisor")

    def verify_permissions(self):
        current = self.status()
        if digest(self.installed) != self.record["binary_sha256"]:
            raise RuntimeError("service executable is not the exact staged candidate")
        if self.os == "Windows":
            result = json.loads(self.ps("$result=@{}; " + "; ".join(
                f"$a=Get-Acl -LiteralPath {psquote(path)}; $result[{psquote(name)}]=@{{owner=$a.Owner;protected=$a.AreAccessRulesProtected;rules=@($a.Access | ForEach-Object {{ @{{sid=$_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value;rights=[int]$_.FileSystemRights;allow=($_.AccessControlType -eq 'Allow')}} }})}}"
                for name, path in (("binary", self.installed), ("config", self.config / "auth.token"), ("state", self.state)))
                + "; $result.service_sid=([Security.Principal.NTAccount]'NT SERVICE\\CPRa').Translate([Security.Principal.SecurityIdentifier]).Value; $result | ConvertTo-Json -Depth 8 -Compress"))
            sid = result["service_sid"]
            write_bits = 2 | 4 | 16 | 256 | 65536 | 262144 | 524288
            for name in ("binary", "config", "state"):
                acl = result[name]
                if not acl["protected"]:
                    raise RuntimeError(f"{name} inherits an uncontrolled ACL")
                service_rights = 0
                for rule in acl["rules"]:
                    if not rule["allow"]:
                        continue
                    if rule["sid"] == sid:
                        service_rights |= rule["rights"]
                    elif rule["sid"] not in ("S-1-5-18", "S-1-5-32-544") and rule["rights"] & write_bits:
                        raise RuntimeError(f"{name} writable by an unexpected principal")
                if name != "state" and service_rights & write_bits:
                    raise RuntimeError("service SID can modify administrator-managed executable/configuration")
                if name == "state" and not service_rights & 2:
                    raise RuntimeError("service SID cannot write durable state")
                if name != "state" and "CPRa" in acl["owner"]:
                    raise RuntimeError("service identity owns administrator-managed files")
            if current.get("account", "").lower() != "nt authority\\localservice":
                raise RuntimeError("SCM did not use the requested LocalService identity")
            self.mark("service ACLs and LocalService identity", acl=result)
            return
        expected_uid = os.geteuid()
        if self.scope == "system":
            import pwd
            expected_uid = pwd.getpwnam("cpra" if self.os == "Linux" else ACCOUNT).pw_uid
        expected_owner = 0 if self.scope == "system" else os.geteuid()
        if self.installed.stat().st_uid != expected_owner or stat.S_IMODE(self.installed.stat().st_mode) & 0o022:
            raise RuntimeError("managed executable ownership/mode allows unintended modification")
        if self.state.stat().st_uid != expected_uid or stat.S_IMODE(self.state.stat().st_mode) != 0o700:
            raise RuntimeError("durable state ownership/mode is not restricted to the service identity")
        pid_uid = int(self.run(["ps", "-o", "uid=", "-p", current["pid"]]).stdout.strip())
        if pid_uid != expected_uid:
            raise RuntimeError("native service executes under the wrong user")
        if self.scope == "system":
            account = "cpra" if self.os == "Linux" else ACCOUNT
            as_service = ["runuser", "-u", account, "--"] if self.os == "Linux" else ["sudo", "-n", "-u", account]
            self.run([*as_service, "test", "-r", self.config / "auth.token"])
            self.run([*as_service, "test", "-w", self.state])
            result = self.run([*as_service, "test", "-w", self.installed], acceptable=(0, 1))
            if result.returncode == 0:
                raise RuntimeError("service identity can overwrite its executable")
        self.mark("native process identity and filesystem ownership", service_uid=expected_uid, binary_uid=expected_owner)

    def record_environment(self):
        if self.os == "Linux":
            self.record["filesystem"] = self.run(["stat", "-f", "-c", "%T", self.state]).stdout.strip()
            self.record["supervisor_version"] = self.run(["systemd", "--version"]).stdout.splitlines()[0]
        elif self.os == "Darwin":
            device = self.run(["/bin/df", "-P", self.state]).stdout.splitlines()[-1].split()[0]
            data = plistlib.loads(self.run(["/usr/sbin/diskutil", "info", "-plist", device]).stdout.encode())
            self.record["filesystem"] = data.get("FilesystemType", data.get("FilesystemName", "unknown"))
            self.record["supervisor_version"] = self.run(["/bin/launchctl", "version"]).stdout.strip()
        else:
            self.record["filesystem"] = self.ps(f"(Get-Volume -FilePath {psquote(self.state / 'raft.db')}).FileSystemType.ToString()")
            self.record["supervisor_version"] = self.record["os_version"]
        if self.record["filesystem"] in ("", "unknown"):
            raise RuntimeError("could not identify the tested filesystem")

    def execute(self):
        self.guard()
        self.owned = True
        self.prepare_backup_key()
        self.create_mac_account()
        self.created_account |= self.os == "Linux" and self.scope == "system"
        self.created_group |= self.os == "Linux" and self.scope == "system"
        install_args = ["--binary", self.binary]
        if self.os == "Darwin" and self.scope == "system":
            install_args.extend(("--account", ACCOUNT))
        self.ctl("service", "install", *install_args)
        if self.status()["running"]:
            raise RuntimeError("fresh installation unexpectedly started the service")
        self.mark("fresh installation remains stopped")
        self.mode(True)
        self.start()
        identity = json.loads((self.state / "identity.json").read_text())["id"]
        self.record_environment()
        self.verify_permissions()
        self.mark("native supervisor start and authenticated readiness", node_id=identity)
        manifest = self.config / "monitors.yaml"
        manifest.write_text("# Operator edit must survive update and removal.\nmonitors: []\n", encoding="utf-8")
        config_inventory = {name: digest(self.config / name) for name in ("monitors.yaml", "runtime.yaml", "auth.token")}
        old_pid, startup = self.status()["pid"], self.status()["startup"]
        self.ctl("service", "update", "--binary", self.binary, "--backup-auth-key", self.backup_key)
        self.wait_ready()
        if self.status()["pid"] == old_pid or self.status()["startup"] != startup:
            raise RuntimeError("running update did not replace the process while preserving startup mode")
        if json.loads((self.state / "identity.json").read_text())["id"] != identity:
            raise RuntimeError("running update replaced durable identity")
        if config_inventory != {name: digest(self.config / name) for name in config_inventory}:
            raise RuntimeError("running update changed operator configuration")
        self.mark("running update preserves configuration, state and startup mode", startup=startup)
        self.stop()
        # The longest first retry delay is shorter than this observation. A
        # deliberate stop must not be converted into automatic crash recovery.
        end = time.monotonic() + 21
        while time.monotonic() < end:
            if self.status()["running"]:
                raise RuntimeError("deliberately stopped service restarted")
            time.sleep(.5)
        self.mark("deliberate stop remains stopped", observed_seconds=21)
        self.mode(False)
        startup = self.status()["startup"]
        self.ctl("service", "update", "--binary", self.binary, "--backup-auth-key", self.backup_key)
        if self.status()["running"] or self.status()["startup"] != startup:
            raise RuntimeError("stopped update changed stopped/disabled state")
        if config_inventory != {name: digest(self.config / name) for name in config_inventory}:
            raise RuntimeError("stopped update changed configuration")
        self.mark("stopped update preserves disabled state and edited configuration", startup=startup)
        backup = self.fixture / "backup"
        self.ctl("backup", "--data-dir", self.state, "--output", backup, "--config", manifest, "--backup-auth-key", self.backup_key)
        stopped_inventory = inventory(self.state)
        self.ctl("service", "uninstall")
        deadline = time.monotonic() + 15
        while self.status()["registered"]:
            if time.monotonic() > deadline:
                raise RuntimeError("uninstall left a supervisor registration behind")
            time.sleep(.25)
        if self.installed.exists() or (self.unit and self.unit.exists()):
            raise RuntimeError("uninstall retained a managed executable/service file")
        if inventory(self.state) != stopped_inventory or config_inventory != {name: digest(self.config / name) for name in config_inventory}:
            raise RuntimeError("uninstall modified durable data or edited configuration")
        self.mark("uninstall removes owned integration and preserves complete state/configuration")
        self.ctl("service", "install", *install_args)
        self.mode(True)
        self.start()
        if json.loads((self.state / "identity.json").read_text())["id"] != identity:
            raise RuntimeError("reinstall failed to resume preserved state")
        self.stop()
        self.ctl("service", "uninstall")
        # Keep the stopped original until restoration has passed. Never remove
        # it to make an inconsistent backup appear usable.
        original = self.state.with_name(self.state.name + ".pre-restore")
        self.state.rename(original)
        self.ctl("restore", "--backup", backup, "--data-dir", self.state, "--backup-auth-key", self.backup_key)
        # Restore intentionally removes previous authority. Do not weaken that
        # boundary to keep the old native lifecycle fixture reporting a pass.
        restored_access = json.loads(self.ctl("auth", "list", "--data-dir", self.state).stdout)
        if restored_access.get("resetRequired"):
            self.record["pending_restore_requirement"] = (
                "Native lifecycle fixture must reprovision fresh named authentication and configure "
                "its management/TLS transport before restarting restored state")
            raise RuntimeError(self.record["pending_restore_requirement"])
        self.ctl("service", "install", *install_args)
        self.mode(True)
        self.start()
        if json.loads((self.state / "identity.json").read_text())["id"] != identity:
            raise RuntimeError("backup restoration failed to preserve identity")
        self.verify_permissions()
        self.stop()
        self.mark("reinstall and stopped backup restore recover under the service identity", node_id=identity)
        self.record["status"] = "pass"

    def cleanup(self):
        errors = []
        if self.owned:
            try:
                self.stop()
                if (self.config / "install.json").exists():
                    self.ctl("service", "uninstall")
                elif self.os == "Windows" and self.status()["registered"]:
                    self.run(["sc.exe", "delete", "CPRa"])
                if self.os == "Darwin":
                    self.run(["/bin/launchctl", "enable", self.target])
                candidates = self.roots + list(self.state.parent.glob(self.state.name + "-backup-*")) + [self.state.with_name(self.state.name + ".pre-restore")]
                for path in sorted(set(candidates), key=lambda p: len(p.parts), reverse=True):
                    if path.is_symlink():
                        raise RuntimeError(f"cleanup refuses unexpected symlink: {path}")
                    if path.is_dir():
                        shutil.rmtree(path)
                    elif path.exists():
                        path.unlink()
                if self.os == "Linux":
                    self.systemctl("daemon-reload")
            except Exception as exc:
                errors.append(str(exc))
        if self.created_account:
            try:
                if self.os == "Darwin":
                    self.run(["/usr/bin/dscl", ".", "-delete", f"/Users/{ACCOUNT}"])
                elif self.os == "Linux" and self.run(["getent", "passwd", "cpra"], acceptable=(0, 2)).returncode == 0:
                    self.run(["userdel", "cpra"])
            except Exception as exc:
                errors.append(str(exc))
        if self.created_group:
            try:
                if self.os == "Darwin":
                    self.run(["/usr/bin/dscl", ".", "-delete", f"/Groups/{ACCOUNT}"])
                elif self.os == "Linux" and self.run(["getent", "group", "cpra"], acceptable=(0, 2)).returncode == 0:
                    self.run(["groupdel", "cpra"])
            except Exception as exc:
                errors.append(str(exc))
        if self.fixture:
            shutil.rmtree(self.fixture, ignore_errors=True)
        if self.enabled_linger:
            try:
                self.run(["sudo", "-n", "loginctl", "disable-linger", os.environ["USER"]])
            except Exception as exc:
                errors.append(str(exc))
        if self.user_manager_started:
            try:
                self.run(["sudo", "-n", "systemctl", "stop", f"user@{os.getuid()}.service"])
            except Exception as exc:
                errors.append(str(exc))
        if errors:
            self.record["status"] = "failed"
            self.record["cleanup_errors"] = errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--stage", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--scope", choices=("system", "user"), default="system")
    parser.add_argument("--allow-system-changes", action="store_true")
    parser.add_argument("--isolated-container", action="store_true", help="allow a booted Linux systemd container; evidence is not native-host qualification")
    args = parser.parse_args()
    args.out = args.out.resolve()
    args.out.parent.mkdir(parents=True, exist_ok=True)
    harness = Harness(args)
    try:
        harness.execute()
    except Exception as exc:
        harness.record["error"] = str(exc)
        print(f"FAIL: {exc}", file=sys.stderr, flush=True)
    finally:
        harness.cleanup()
        harness.save()
    return 0 if harness.record["status"] == "pass" else 1


if __name__ == "__main__":
    sys.exit(main())
