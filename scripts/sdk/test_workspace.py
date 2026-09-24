"""Developer workspace selection must not weaken release/module checks."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


class WorkspaceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="cpra-workspace-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.helper = self.root / "scripts/sdk/workspace.py"
        self.helper.parent.mkdir(parents=True)
        shutil.copyfile(ROOT / "scripts/sdk/workspace.py", self.helper)
        shutil.copyfile(ROOT / "Makefile", self.root / "Makefile")
        manifest_helper = self.root / "scripts/dashboard/asset_manifest.py"
        manifest_helper.parent.mkdir(parents=True)
        shutil.copyfile(ROOT / "scripts/dashboard/asset_manifest.py", manifest_helper)
        (self.root / "dashboard/src").mkdir(parents=True)
        assets = self.root / "internal/httpserver/assets"
        assets.mkdir(parents=True)
        (assets / "index.html").write_text("fixture dashboard")
        self.modules = [self.root / path for path in (".", "sdk/go", "sdk/go/worker", "examples/sdk")]
        for index, module in enumerate(self.modules):
            module.mkdir(parents=True, exist_ok=True)
            (module / "go.mod").write_text(f"module example.com/fixture{index}\n\ngo 1.25.0\n")
            (module / "go.sum").write_text("unchanged fixture\n")
        (self.modules[0] / "go.mod").write_text("module example.com/fixture0\n\ngo 1.25.0\n\nrequire example.com/fixture1 v0.2.0-rc.8\n")
        (self.modules[2] / "go.mod").write_text("module example.com/fixture2\n\ngo 1.26.0\n\nrequire (\n example.com/fixture1 v0.2.0-rc.9\n)\n")
        (self.modules[3] / "go.mod").write_text("module example.com/fixture3\n\ngo 1.25.0\n\nrequire example.com/fixture2 v0.3.0\n")
        subprocess.run([sys.executable, str(manifest_helper), "--write"], check=True)
        self.module_bytes = {path: path.read_bytes() for module in self.modules
                             for path in (module / "go.mod", module / "go.sum")}
        self.github_env = self.root / "github-env"
        self.github_env.write_text("EXISTING=value\n")
        self.calls = self.root / "go-calls.jsonl"
        self.env = dict(os.environ, GITHUB_ENV=str(self.github_env), GO_CALLS=str(self.calls))
        for name in ("GOWORK", "MAKEFLAGS", "MFLAGS", "MAKEOVERRIDES", "FAIL_OFF"):
            self.env.pop(name, None)
        self.fake_go = self.root / "fake_go.py"
        self.fake_go.write_text('''import json, os, sys
from pathlib import Path
workspace = os.environ.get("GOWORK")
record = {"args": sys.argv[1:], "workspace": workspace,
          "content": Path(workspace).read_text() if workspace and workspace != "off" else None,
          "cgo": os.environ.get("CGO_ENABLED")}
fd = os.open(os.environ["GO_CALLS"], os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
os.write(fd, (json.dumps(record) + "\\n").encode())
os.close(fd)
if workspace == "off" and os.environ.get("FAIL_OFF"):
    sys.exit(17)
''')

    def make(self, *arguments, env=None, expected=0):
        command = ["make", "--no-print-directory", f"PYTHON={sys.executable}",
                   f"GO={sys.executable} {self.fake_go}", *arguments]
        completed = subprocess.run(command, cwd=self.root, env=env or self.env,
                                   text=True, capture_output=True, timeout=20)
        self.assertEqual(completed.returncode, expected, completed.stdout + completed.stderr)
        return completed

    def records(self):
        return [json.loads(line) for line in self.calls.read_text().splitlines()]

    def assert_modules_unchanged(self):
        self.assertEqual(self.module_bytes, {path: path.read_bytes() for path in self.module_bytes})

    def test_parallel_default_builds_share_workspace_without_examples_or_ci_export(self):
        result = self.make("-j2", "build", "build-ctl")
        self.assertEqual(result.stdout.count("scripts/sdk/workspace.py"), 1)
        records = self.records()
        self.assertEqual(len(records), 2)
        for record in records:
            self.assertEqual(record["workspace"], str(self.root / "bin/cpra-sdk.work"))
            self.assertNotIn(str(self.root / "examples/sdk"), record["content"])
            for module in self.modules[:3]:
                self.assertIn(json.dumps(str(module)), record["content"])
            self.assertEqual(record["cgo"], "0")
            self.assertIn("-trimpath", record["args"])
        self.assertEqual(self.github_env.read_text(), "EXISTING=value\n")
        self.assert_modules_unchanged()

    def test_explicit_custom_workspace_is_not_rewritten(self):
        workspace = self.root / "custom workspace.work"
        workspace.write_text("operator-selected workspace\n")
        before = workspace.stat().st_mtime_ns
        self.make("build-ctl", env=dict(self.env, GOWORK=str(workspace)))
        self.assertEqual(self.records()[0]["workspace"], str(workspace))
        self.assertEqual(workspace.stat().st_mtime_ns, before)
        self.assertFalse((self.root / "bin/cpra-sdk.work").exists())
        self.assertEqual(self.github_env.read_text(), "EXISTING=value\n")

    def test_explicit_off_failure_never_falls_back(self):
        for via_argument in (False, True):
            with self.subTest(via_argument=via_argument):
                env = dict(self.env, FAIL_OFF="1")
                arguments = ["build-ctl"]
                if via_argument:
                    arguments.append("GOWORK=off")
                else:
                    env["GOWORK"] = "off"
                self.make(*arguments, env=env, expected=2)
                self.assertEqual(self.records()[-1]["workspace"], "off")
                self.assertFalse((self.root / "bin/cpra-sdk.work").exists())
        self.assert_modules_unchanged()

    def test_check_targets_use_the_selected_workspace(self):
        self.make("-j2", "vet", "test", "test-all-drivers", "sdk-check", "build-verification")
        self.assertEqual(len(self.records()), 11)
        self.assertTrue(all(record["workspace"] == str(self.root / "bin/cpra-sdk.work")
                            for record in self.records()))
        self.assert_modules_unchanged()

    def test_repeated_setup_preserves_complete_unchanged_workspace(self):
        self.make("dev-workspace")
        workspace = self.root / "bin/cpra-sdk.work"
        before = (workspace.read_bytes(), workspace.stat().st_mtime_ns)
        self.make("dev-workspace")
        self.assertEqual((workspace.read_bytes(), workspace.stat().st_mtime_ns), before)

    def test_replacements_follow_selected_module_requirements(self):
        self.make("dev-workspace")
        content = (self.root / "bin/cpra-sdk.work").read_text()
        self.assertIn("go 1.26.0\n", content)
        self.assertIn("replace example.com/fixture1 v0.2.0-rc.8 =>", content)
        self.assertIn("replace example.com/fixture1 v0.2.0-rc.9 =>", content)
        self.assertNotIn("v0.1.0-rc.1", content)
        self.assertNotIn("replace example.com/fixture2", content)
        (self.modules[0] / "go.mod").write_text("module example.com/fixture0\n\ngo 1.25.0\nrequire example.com/fixture1 v0.4.0\n")
        self.make("dev-workspace")
        self.assertIn("replace example.com/fixture1 v0.4.0 =>", (self.root / "bin/cpra-sdk.work").read_text())

    def test_build_rejects_stale_dashboard_before_compiler(self):
        (self.root / "dashboard/src/new.ts").write_text("export const changed = true")
        result = self.make("build", expected=2)
        self.assertIn("embedded dashboard is stale", result.stderr)
        self.assertFalse(self.calls.exists())

    def test_explicit_helper_can_still_enable_examples_and_export_to_ci(self):
        workspace = self.root / "examples.work"
        subprocess.run([sys.executable, str(self.helper), "--output", str(workspace), "--examples"],
                       env=self.env, text=True, capture_output=True, check=True, timeout=20)
        self.assertIn(str(self.root / "examples/sdk"), workspace.read_text())
        self.assertEqual(self.github_env.read_text(), f"EXISTING=value\nGOWORK={workspace}\n")

    def test_release_target_does_not_initialize_or_select_development_workspace(self):
        result = self.make("-n", "release-build")
        self.assertIn("scripts/release/release.py build", result.stdout)
        self.assertNotIn("workspace.py", result.stdout)
        self.assertNotIn("GOWORK=", result.stdout)
        self.assertFalse((self.root / "bin/cpra-sdk.work").exists())


if __name__ == "__main__":
    unittest.main()
