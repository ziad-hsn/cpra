#!/usr/bin/env python3
"""Run SDK lessons and record bounded, explicitly local evidence."""
from __future__ import annotations

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
EXAMPLES = ROOT / "examples/sdk"
REPORT_OUTPUT_BYTES = 32 * 1024
COMMAND_OUTPUT_BYTES = 8 * 1024 * 1024
SOURCE_ROOTS = ("examples/sdk", "sdk/go", "docs/sdk", "api/openapi", "scripts/sdk")
SOURCE_FILES = ("go.mod", "go.sum", "Makefile", ".github/workflows/ci.yml")
SOURCE_SUFFIXES = {".go", ".mod", ".sum", ".json", ".md", ".yaml", ".yml", ".jsonl", ".txt", ".xml", ".py"}
SOURCE_EXCLUDES = {"docs/sdk/verification.md"}  # Evidence narrative is an output.


def digest(root=ROOT):
    """Hash selected source, dependency declarations, and verification recipes."""
    paths = {root / name for name in SOURCE_FILES if (root / name).is_file()}
    for name in SOURCE_ROOTS:
        for path in (root / name).rglob("*"):
            if path.is_file() and path.suffix in SOURCE_SUFFIXES and not any(
                    part.startswith(".") or part == "__pycache__" for part in path.relative_to(root / name).parts):
                paths.add(path)
    result = hashlib.sha256()
    for path in sorted(paths):
        relative = path.relative_to(root).as_posix()
        if relative in SOURCE_EXCLUDES:
            continue
        if path.is_symlink():
            raise ValueError("verification input is a symbolic link: " + relative)
        result.update(relative.encode() + b"\0" + path.read_bytes() + b"\0")
    return result.hexdigest()


def save(report, out):
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, indent=2) + "\n")


def stop_process(process):
    if os.name == "posix":
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    elif process.poll() is None:
        process.kill()
    process.wait()


def run_check(report, out, env, name, command, expected=(), *, cwd=EXAMPLES, timeout=600):
    """Save failed, timed-out, and interrupted commands with bounded output."""
    started = time.monotonic()
    row = {"name": name, "command": command, "status": "running"}
    report["checks"].append(row)
    save(report, out)
    error = None
    interrupt = None
    with tempfile.TemporaryFile() as capture:
        process = None
        try:
            process = subprocess.Popen(command, cwd=cwd, env=env, stdout=capture,
                                       stderr=subprocess.STDOUT, start_new_session=os.name == "posix")
            while process.poll() is None:
                if time.monotonic() - started >= timeout:
                    row["timed_out"] = True
                    error = f"command exceeded {timeout:g} seconds"
                    stop_process(process)
                    break
                if os.fstat(capture.fileno()).st_size > COMMAND_OUTPUT_BYTES:
                    error = "command output exceeded 8 MiB"
                    stop_process(process)
                    break
                try:
                    process.wait(timeout=min(0.1, max(0.001, timeout - (time.monotonic() - started))))
                except subprocess.TimeoutExpired:
                    pass
            row["exit_code"] = process.returncode
        except KeyboardInterrupt as caught:
            interrupt = caught
            row["interrupted"] = True
            error = "verification interrupted"
            if process is not None:
                stop_process(process)
                row["exit_code"] = process.returncode
        except Exception as caught:
            error = str(caught)
            if process is not None and process.poll() is None:
                stop_process(process)
            row["exit_code"] = process.returncode if process is not None else None
        size = os.fstat(capture.fileno()).st_size
        capture.seek(0)
        raw = capture.read(COMMAND_OUTPUT_BYTES + 1)
        if size > COMMAND_OUTPUT_BYTES:
            error = "command output exceeded 8 MiB"
        output = raw.decode("utf-8", errors="replace")
        row.update(seconds=round(time.monotonic() - started, 3), output_bytes=size,
                   output=raw[-REPORT_OUTPUT_BYTES:].decode("utf-8", errors="replace"),
                   output_truncated=size > REPORT_OUTPUT_BYTES)
        if size <= COMMAND_OUTPUT_BYTES:
            row["output_sha256"] = hashlib.sha256(raw).hexdigest()
        if error is None and row["exit_code"] != 0:
            error = "command returned a nonzero exit code"
        if error is None:
            missing = [fragment for fragment in expected if fragment not in output]
            if missing:
                row["missing_expected_output"] = missing
                error = "command omitted expected demonstration output"
        row["status"] = "interrupted" if interrupt else "failed" if error else "passed"
        if error:
            row["error"] = error
            report["status"] = row["status"]
        save(report, out)
    print(name + ": " + row["status"], flush=True)
    if interrupt:
        raise interrupt
    if error:
        raise RuntimeError(name + " failed: " + error + "; see " + str(out))
    return output


def check_module_directories(output, root=ROOT):
    expected = {"github.com/ziad-hsn/cpra/examples/sdk": root / "examples/sdk",
                "github.com/ziad-hsn/cpra/sdk/go": root / "sdk/go",
                "github.com/ziad-hsn/cpra/sdk/go/worker": root / "sdk/go/worker"}
    decoder = json.JSONDecoder()
    modules = []
    while output.strip():
        value, end = decoder.raw_decode(output.lstrip())
        output = output.lstrip()[end:]
        path = value.get("Path")
        if path not in expected or Path(value.get("Dir", "")).resolve() != expected[path].resolve():
            raise RuntimeError("workspace selected an unexpected SDK/example module directory")
        if any(module["path"] == path for module in modules):
            raise RuntimeError("duplicate module selection")
        modules.append({"path": path, "directory": str(expected[path]), "version": value.get("Version"),
                        "go_version": value.get("GoVersion")})
    if len(modules) != len(expected):
        raise RuntimeError("workspace does not select all three candidate modules")
    return modules


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default=os.environ.get("GO", "go"))
    parser.add_argument("--out", default="evidence/local/sdk-examples.json")
    parser.add_argument("--race", action="store_true")
    args = parser.parse_args()
    env = os.environ.copy()
    env.update(GOTOOLCHAIN="local", GOENV="off", GOFLAGS="", GOEXPERIMENT="")
    if not env.get("GOWORK") or env["GOWORK"] == "off":
        raise SystemExit("Set GOWORK using scripts/sdk/workspace.py --examples for unpublished candidate modules")
    workspace = Path(env["GOWORK"]).resolve()
    workspace_hash = hashlib.sha256(workspace.read_bytes()).hexdigest()
    before = digest()
    report = {"classification": "local_integration_and_mock_contract", "source_sha256": before,
              "source_hash_scope": {"roots": SOURCE_ROOTS, "additional_files": SOURCE_FILES,
                                    "suffixes": sorted(SOURCE_SUFFIXES), "excluded_outputs": sorted(SOURCE_EXCLUDES)},
              "workspace": {"path": str(workspace), "sha256": workspace_hash},
              "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "status": "running", "checks": [],
              "production_v2_server_verified": False, "live_aws_verified": False, "live_kubernetes_verified": False,
              "live_blockchain_verified": False, "sms_delivery_verified": False}
    out = ROOT / args.out
    save(report, out)

    def run(name, command, expected=()):
        return run_check(report, out, env, name, command, expected)

    try:
        report["go"] = run("compiler", [args.go, "version"]).strip()
        selected = run("candidate module directories", [args.go, "list", "-m", "-json",
                       "github.com/ziad-hsn/cpra/examples/sdk", "github.com/ziad-hsn/cpra/sdk/go",
                       "github.com/ziad-hsn/cpra/sdk/go/worker"])
        report["candidate_modules"] = check_module_directories(selected)
        graph = run("selected module graph", [args.go, "list", "-m", "all"])
        report["module_graph_sha256"] = hashlib.sha256(graph.encode()).hexdigest()
        for tags in [[], ["-tags=externaljobs"]]:
            variant = "externaljobs" if tags else "default"
            run(variant + " tests", [args.go, "test", "-mod=readonly", *(["-race"] if args.race else []), *tags, "./..."])
            run(variant + " vet", [args.go, "vet", "-mod=readonly", *tags, "./..."])
        packages = run("default package selection", [args.go, "list", "-mod=readonly", "./..."])
        if "/dao-sms" in packages:
            raise RuntimeError("default build included tagged DAO worker")
        for tags in [[], ["-tags=externaljobs"]]:
            variant = "externaljobs" if tags else "default"
            deps = run(variant + " compiled dependencies", [args.go, "list", "-mod=readonly", "-deps", *tags, "./..."])
            if "github.com/ziad-hsn/cpra/internal/" in deps:
                raise RuntimeError("examples imported application internals")
            if not tags and "github.com/ziad-hsn/cpra/sdk/go/worker" in deps:
                raise RuntimeError("default examples imported worker module")
        run("queue demo", [args.go, "run", "-mod=readonly", "./queue-registration", "-demo"], ["messages_acknowledged=2 monitors=1"])
        run("AWS demo", [args.go, "run", "-mod=readonly", "./elb-deregistration", "-demo"], ["verified through SDK GET: enabled=false; duplicate event made no further change"])
        run("Kubernetes demo", [args.go, "run", "-mod=readonly", "./kubernetes-services", "-demo"], ["Demo complete: 5 monitors; replay created no duplicates."])
        run("DAO SMS demo", [args.go, "run", "-mod=readonly", "-tags=externaljobs", "./dao-sms", "-mode=demo"], ["RPC requests: 7; SMS requests: 1; outcome submissions: 4", "Encrypted outbox drained"])
        if digest() != before or hashlib.sha256(workspace.read_bytes()).hexdigest() != workspace_hash:
            raise RuntimeError("source, dependency declarations, recipe, or workspace changed during verification; rerun final candidate")
        if run("final module graph", [args.go, "list", "-m", "all"]) != graph:
            raise RuntimeError("selected dependency graph changed during verification")
        report["status"] = "passed"
    except KeyboardInterrupt:
        report.update(status="interrupted", error="verification interrupted; incomplete evidence")
        raise
    except Exception as error:
        report.update(status="failed", error=str(error))
        raise
    finally:
        report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        save(report, out)
    print(str(out))


if __name__ == "__main__":
    main()
