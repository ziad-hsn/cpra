#!/usr/bin/env python3
"""Run every required local browser security fixture and reject skipped coverage."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[2]
TESTS = {
    "github.com/ziad-hsn/cpra": ["TestMainManagementControlsBrowser", "TestMainManagementRecoveryReviewBrowser", "TestMainManagementCollectionBrowser", "TestMainManagementCollectionExecutionBrowser", "TestMainManagementCollectionActivationBrowser", "TestMainManagementCollectionReselectionBrowser", "TestMainManagementCollectionContinuationBrowser"],
    "github.com/ziad-hsn/cpra/internal/httpserver": ["TestManagementBrowser"],
}


def verify_results(events):
    passed = {(event.get("Package"), event.get("Test")) for event in events if event.get("Action") == "pass"}
    required = {(package, name) for package, names in TESTS.items() for name in names}
    if not required.issubset(passed):
        raise ValueError("required browser tests did not pass: " + ", ".join(name for _, name in sorted(required - passed)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--node", default=os.environ.get("CPRA_BROWSER_NODE", "node"))
    parser.add_argument("--module", default=os.environ.get("CPRA_BROWSER_MODULE", str(ROOT / "scripts/dashboard/browser/node_modules/playwright")))
    parser.add_argument("--browser", default=os.environ.get("CPRA_BROWSER_EXECUTABLE"))
    parser.add_argument("--out", type=Path, default=ROOT / "bin/verification/browser-campaign.json")
    args = parser.parse_args()
    args.out.unlink(missing_ok=True)
    module = Path(args.module).resolve()
    node = shutil.which(args.node)
    if node is None or not (module / "package.json").is_file():
        parser.error("installed Node and the pinned Playwright module are required")
    expected = json.loads((ROOT / "scripts/dashboard/browser/package.json").read_text())["dependencies"]["playwright"]
    version = json.loads((module / "package.json").read_text())["version"]
    if version != expected:
        parser.error("Playwright version differs from the campaign pin")
    browser = args.browser or subprocess.check_output([node, "-e", "process.stdout.write(require(process.argv[1]).chromium.executablePath())", str(module)], text=True)
    if not Path(browser).is_absolute() or not Path(browser).is_file():
        parser.error("an installed Chromium executable is required")
    environment = dict(os.environ, CPRA_BROWSER_MODULE=str(module), CPRA_BROWSER_NODE=node,
                       CPRA_BROWSER_EXECUTABLE=browser, CPRA_BROWSER_REQUIRED="1")
    pattern = "^(" + "|".join(name for names in TESTS.values() for name in names) + ")$"
    command = [args.go, "test", "-race", "-count=1", "-timeout=15m", "-json", "-run", pattern, ".", "./internal/httpserver"]
    events = []
    with subprocess.Popen(command, cwd=ROOT, env=environment, stdout=subprocess.PIPE, text=True) as process:
        for line in process.stdout:
            event = json.loads(line)
            if event.get("Action") in ("pass", "fail", "skip"):
                events.append(event)
            if "Output" in event:
                print(event["Output"], end="", flush=True)
        returncode = process.wait()
    if returncode != 0:
        raise SystemExit(returncode)
    verify_results(events)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps({"result": "passed", "required_tests": TESTS, "playwright": version,
                                   "node": subprocess.check_output([node, "--version"], text=True).strip(),
                                   "chromium": subprocess.check_output([browser, "--version"], text=True).strip(),
                                   "test_results": events}, indent=2) + "\n")


if __name__ == "__main__":
    main()
