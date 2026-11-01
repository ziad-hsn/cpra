#!/usr/bin/env python3
"""Run every required local browser security fixture and reject skipped coverage."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time

import asset_manifest

ROOT = Path(__file__).resolve().parents[2]
TESTS = {
    "github.com/ziad-hsn/cpra": ["TestMainManagementControlsBrowser", "TestMainRecoveryRulesBrowser", "TestMainManagementRecoveryReviewBrowser", "TestMainReviewNotificationsBrowser", "TestMainManagementCollectionBrowser", "TestMainManagementCollectionExecutionBrowser", "TestMainManagementCollectionActivationBrowser", "TestMainManagementCollectionReselectionBrowser", "TestMainManagementCollectionContinuationBrowser"],
    "github.com/ziad-hsn/cpra/internal/httpserver": ["TestManagementBrowser"],
}
SCREENSHOTS = (
    'recovery-rules-selected.png', 'recovery-rules-mobile.png',
    'dashboard-populated-demo.png', 'review-blue-opening.png', 'review-blue-health-closure.png',
    'review-blue-unknown-closure.png', 'offline-route.png', 'review-blue-mobile.png',
    'monitor-check-unavailable.png', 'monitor-table-desktop.png', 'monitor-table-large-text.png', 'monitor-table-zoom-reflow.png', 'monitor-table-mobile.png',
    'dashboard-overview.png',
    'guarded-recovery-dialog.png', 'unknown-review-inconclusive-dialog.png',
    'unknown-review-accepted-dialog.png', 'mobile-reader-incidents-scrolled.png',
    "operator-review.png", "reader-operations.png", "reader-monitor-state.png",
    "reader-incidents.png", "reader-metrics.png", "mobile-reader-incidents.png",
    "mobile-reader-metrics.png",
)
MAX_LINE_BYTES = 1024 * 1024
MAX_LOG_BYTES = 64 * 1024 * 1024


def verify_results(events):
    terminal = {}
    for event in events:
        if not isinstance(event, dict) or event.get("Action") not in ("pass", "fail", "skip"):
            raise ValueError("invalid terminal browser test event")
        if not isinstance(event.get("Package"), str) or (event.get("Test") is not None and not isinstance(event["Test"], str)):
            raise ValueError("invalid browser test identity")
        key = (event.get("Package"), event.get("Test"))
        if key in terminal:
            raise ValueError("duplicate terminal browser test event")
        terminal[key] = event["Action"]
        if event["Action"] != "pass":
            raise ValueError("required browser tests did not pass: failed or skipped coverage")
    required = {(package, name) for package, names in TESTS.items() for name in names}
    passed = {key for key, action in terminal.items() if action == "pass"}
    if not required.issubset(passed):
        raise ValueError("required browser tests did not pass: " + ", ".join(name for _, name in sorted(required - passed)))
    if not {(package, None) for package in TESTS}.issubset(passed):
        raise ValueError("browser package completion is missing")


def parse_event(line):
    def fields(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("duplicate browser event field")
            value[key] = item
        return value

    def constant(_):
        raise ValueError("non-finite browser event value")

    event = json.loads(line, object_pairs_hook=fields, parse_constant=constant)
    if not isinstance(event, dict) or not isinstance(event.get("Action"), str):
        raise ValueError("browser test output is not an event object")
    for name in ("Package", "Test", "Output"):
        if name in event and not isinstance(event[name], str):
            raise ValueError("invalid browser event field type")
    return event


def sha(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def source_inventory(root, source_files=None):
    names = source_files if source_files is not None else subprocess.check_output(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], cwd=root
    ).decode().split("\0")
    prefixes = ("internal/", "cmd/", "sdk/go/", "api/", "examples/", "scripts/dashboard/", "scripts/release/")
    selected = set(name for name in names if name and (
        name.startswith(prefixes) or ("/" not in name and name.endswith(".go"))
        or name in ("go.mod", "go.sum", ".github/workflows/ci.yml")))
    dashboard = asset_manifest.snapshot(root)
    selected.update(dashboard["inputs"])
    selected.update(dashboard["outputs"])
    selected.add(asset_manifest.MANIFEST)
    return {name: sha(root / name) for name in sorted(selected) if (root / name).is_file()}


def build_environment(module, node, browser, screenshots, base=None):
    base = os.environ if base is None else base
    allowed = ("PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "SystemRoot", "COMSPEC", "PATHEXT",
               "TEMP", "TMP", "TMPDIR", "GOCACHE", "GOMODCACHE", "GOPATH")
    environment = {name: base[name] for name in allowed if name in base}
    environment.update(
        GOENV="off", GOTOOLCHAIN="local", GOWORK=base.get("GOWORK", "off"),
        GOFLAGS="-mod=readonly -p=1", GOEXPERIMENT="", CGO_ENABLED="1",
        GOPROXY="https://proxy.golang.org", GOSUMDB="sum.golang.org",
        GOMAXPROCS="2", GOMEMLIMIT="768MiB", TZ="UTC",
        CPRA_BROWSER_MODULE=str(module), CPRA_BROWSER_NODE=node,
        CPRA_BROWSER_EXECUTABLE=str(browser), CPRA_BROWSER_REQUIRED="1",
        CPRA_BROWSER_EVIDENCE_DIR=str(screenshots),
    )
    return environment


def write_report(stream, report):
    stream.seek(0)
    stream.truncate()
    stream.write(json.dumps(report, indent=2) + "\n")
    stream.flush()
    os.fsync(stream.fileno())


def public_summary(report):
    """Identify failed coverage without publishing fixture output or artifacts."""
    summary = {key: report[key] for key in ("result", "seconds", "error") if key in report}
    events = report.get("test_results", [])
    summary["failed_tests"] = [
        name for package, names in TESTS.items() for name in names
        if any(event.get("Package") == package and event.get("Test") == name
               and event.get("Action") in ("fail", "skip") for event in events)
    ]
    summary["failed_packages"] = [
        package for package in TESTS
        if any(event.get("Package") == package and event.get("Test") is None
               and event.get("Action") in ("fail", "skip") for event in events)
    ]
    return summary


def campaign(args, root=ROOT, *, source_files=None, source_commit=None, environment=None):
    output = args.out.resolve()
    artifacts = output.with_name(output.stem + ".artifacts")
    if artifacts.exists():
        raise FileExistsError("browser campaign artifacts already exist; choose a new output")
    output.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    report = {"result": "running", "recorded_at": datetime.now(timezone.utc).isoformat(),
              "required_tests": TESTS, "release_qualified": False,
              "scope": "Local browser fixtures against normal startup and the HTTP server; not full candidate or distribution qualification."}
    started = time.monotonic()
    with os.fdopen(descriptor, "w") as stream:
        write_report(stream, report)
        try:
            artifacts.mkdir(mode=0o700)
            screenshots = artifacts / "screenshots"
            screenshots.mkdir(mode=0o700)
            module = Path(args.module).resolve()
            node, go = shutil.which(args.node), shutil.which(args.go)
            if node is None or go is None or not (module / "package.json").is_file():
                raise ValueError("installed Go, Node and the pinned Playwright module are required")
            expected = json.loads((root / "scripts/dashboard/browser/package.json").read_text())["dependencies"]["playwright"]
            version = json.loads((module / "package.json").read_text())["version"]
            if version != expected:
                raise ValueError("Playwright version differs from the campaign pin")
            browser = args.browser or subprocess.check_output([node, "-e", "process.stdout.write(require(process.argv[1]).chromium.executablePath())", str(module)], text=True, timeout=20)
            browser = Path(browser)
            if not browser.is_absolute() or not browser.is_file():
                raise ValueError("an installed Chromium executable is required")
            environment = build_environment(module, node, browser, screenshots, environment)
            recipe = json.loads((root / "scripts/release/recipe.json").read_text())
            versions = {"go": subprocess.check_output([go, "env", "GOVERSION"], env=environment, text=True, timeout=20).strip(),
                        "node": subprocess.check_output([node, "--version"], env=environment, text=True, timeout=20).strip(),
                        "chromium": subprocess.check_output([str(browser), "--version"], env=environment, text=True, timeout=20).strip(),
                        "playwright": version}
            report.update(versions)
            if versions["go"] not in (recipe["minimum_go_version"], recipe["go_version"]):
                raise ValueError("browser compiler differs from the supported verification matrix")
            if versions["node"] != recipe["frontend"]["node_version"]:
                raise ValueError("Node differs from the release pin")
            asset_manifest.verify(root)
            before = source_inventory(root, source_files)
            source_path = artifacts / "source.json"
            source_path.write_text(json.dumps(before, indent=2) + "\n")
            report.update(source_commit=source_commit or subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
                          source_inventory=str(source_path.relative_to(output.parent)),
                          source_inventory_sha256=sha(source_path), tags=args.tags, race=args.race,
                          tool_sha256={name: sha(Path(path)) for name, path in (("go", go), ("node", node), ("chromium", browser))})
            workspace = environment["GOWORK"]
            report["workspace"] = {"path": workspace, "sha256": sha(Path(workspace)) if workspace != "off" else None}
            pattern = "^(" + "|".join(name for names in TESTS.values() for name in names) + ")$"
            command = [go, "test", "-count=1", "-timeout=" + args.timeout, "-json", "-tags=" + args.tags, "-run", pattern]
            if args.race:
                command.append("-race")
            command += [".", "./internal/httpserver"]
            report["command"] = command
            write_report(stream, report)
            events, invalid, total = [], None, 0
            log_path, error_path = artifacts / "go.jsonl", artifacts / "stderr.log"
            with log_path.open("xb") as log, error_path.open("xb") as errors:
                with subprocess.Popen(command, cwd=root, env=environment, stdout=subprocess.PIPE, stderr=errors) as process:
                    while line := process.stdout.readline(MAX_LINE_BYTES + 1):
                        total += len(line)
                        if len(line) > MAX_LINE_BYTES or total > MAX_LOG_BYTES:
                            invalid = "browser test output exceeds the recording limit"
                            process.terminate()
                            try:
                                process.wait(timeout=10)
                            except subprocess.TimeoutExpired:
                                process.kill()
                            break
                        log.write(line)
                        try:
                            event = parse_event(line)
                        except (ValueError, UnicodeError):
                            invalid = "browser test output contains an invalid event"
                            continue
                        if event["Action"] in ("pass", "fail", "skip"):
                            events.append(event)
                    returncode = process.wait()
            report.update(exit_code=returncode, test_results=events,
                          logs={str(path.relative_to(output.parent)): {"bytes": path.stat().st_size, "sha256": sha(path)} for path in (log_path, error_path)})
            after = source_inventory(root, source_files)
            report["source_changed"] = [name for name in sorted(set(before) | set(after)) if before.get(name) != after.get(name)]
            if returncode != 0 or invalid:
                raise ValueError(invalid or "browser Go command failed")
            verify_results(events)
            if report["source_changed"]:
                raise ValueError("browser campaign inputs changed during execution")
            images = {}
            for name in SCREENSHOTS:
                path = screenshots / name
                if not path.is_file() or path.is_symlink():
                    raise ValueError("required browser screenshot is missing or invalid: " + name)
                with path.open("rb") as image:
                    if image.read(8) != b"\x89PNG\r\n\x1a\n":
                        raise ValueError("required browser screenshot is missing or invalid: " + name)
                images[str(path.relative_to(output.parent))] = {"bytes": path.stat().st_size, "sha256": sha(path)}
            report.update(result="passed", screenshots=images)
        except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
            report.update(result="failed", error=str(error))
        finally:
            report["seconds"] = round(time.monotonic() - started, 2)
            write_report(stream, report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--node", default=os.environ.get("CPRA_BROWSER_NODE", "node"))
    parser.add_argument("--module", default=os.environ.get("CPRA_BROWSER_MODULE", str(ROOT / "scripts/dashboard/browser/node_modules/playwright")))
    parser.add_argument("--browser", default=os.environ.get("CPRA_BROWSER_EXECUTABLE"))
    parser.add_argument("--tags", default="")
    parser.add_argument("--timeout", default="45m")
    parser.add_argument("--race", action=argparse.BooleanOptionalAction, default=True)
    parser.add_argument("--out", type=Path, default=ROOT / "bin/verification/browser-campaign.json")
    args = parser.parse_args()
    try:
        report = campaign(args)
    except OSError as error:
        parser.error(str(error))
    print(json.dumps(public_summary(report)))
    if report["result"] != "passed":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
