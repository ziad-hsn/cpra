import os
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import reference
import sync_guides


class GuideCopiesTests(unittest.TestCase):
    def fixture(self, root):
        examples = root / "examples/sdk"
        examples.mkdir(parents=True)
        (examples / "README.md").write_text("# Setup\n")
        for lesson in sync_guides.LESSONS:
            directory = examples / lesson
            directory.mkdir()
            (directory / "main.go").write_text("package main\n")
            (directory / "README.md").write_text("[Setup](../README.md) [Code](main.go)\n")
        worker = root / "sdk/go/worker"
        worker.mkdir(parents=True)
        (worker / "README.md").write_text("# Worker journal\n")
        with (examples / "dao-sms/README.md").open("a") as out:
            out.write("[Worker](../../../sdk/go/worker/README.md)\n")

    def test_source_assets_are_local_exact_and_included_in_site(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "repo"
            self.fixture(root)
            site = Path(temporary) / "site/sdk"
            with patch.object(sync_guides, "ROOT", root):
                sync_guides.sync(site=site)
                sync_guides.sync(check=True, site=site)
                text = (site / "queue-registration.md").read_text()
                self.assertIn("[Code](source/examples/sdk/queue-registration/main.go.txt)", text)
                self.assertIn("[Setup](index.md)", text)
                self.assertNotIn("github.com", text)
                asset = site / "source/examples/sdk/queue-registration/main.go.txt"
                self.assertEqual(asset.read_bytes(), b"package main\n")
                self.assertTrue((site / "source/sdk/go/worker/README.md.txt").is_file())
                self.assertEqual(list((root / "docs/sdk").rglob("*.go")), [])
                asset.write_text("changed\n")
                with self.assertRaisesRegex(ValueError, "stale site"):
                    sync_guides.sync(check=True, site=site)

    def test_removed_links_remove_only_recorded_generated_assets(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "repo"
            self.fixture(root)
            site = Path(temporary) / "site/sdk"
            with patch.object(sync_guides, "ROOT", root):
                sync_guides.sync(site=site)
                (root / "examples/sdk/queue-registration/README.md").write_text("[Setup](../README.md)\n")
                with self.assertRaisesRegex(ValueError, "stale"):
                    sync_guides.sync(check=True, site=site)
                sync_guides.sync(site=site)
                self.assertFalse((site / "source/examples/sdk/queue-registration/main.go.txt").exists())
                sync_guides.sync(check=True, site=site)

    def test_missing_or_outside_source_links_fail(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "repo"
            self.fixture(root)
            with patch.object(sync_guides, "ROOT", root):
                path = root / "examples/sdk/queue-registration/README.md"
                path.write_text("[Absent](absent.go)\n")
                with self.assertRaisesRegex(ValueError, "broken"):
                    sync_guides.generated()
                path.write_text("[Outside](../../../../private.txt)\n")
                with self.assertRaisesRegex(ValueError, "outside"):
                    sync_guides.generated()


class WorkspaceTests(unittest.TestCase):
    def test_examples_are_explicit_to_avoid_application_mvs_changes(self):
        script = Path(__file__).with_name("workspace.py")
        env = os.environ.copy()
        env.pop("GITHUB_ENV", None)
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "go.work"
            for flag in ([], ["--examples"]):
                subprocess.run([sys.executable, str(script), "--output", str(path), *flag], env=env, check=True, capture_output=True)
                text = path.read_text()
                self.assertEqual("/examples/sdk\"" in text, bool(flag))
                self.assertIn("sdk/go/worker", text)


class SchemaDescriptionTests(unittest.TestCase):
    def test_unconstrained_json_is_not_described_as_an_object(self):
        self.assertEqual(reference.typename({"x-go-type": "json.RawMessage"}), "any JSON value")
        self.assertEqual(reference.typename({"type": ["string", "null"]}), "string or null")

    def test_operation_count_is_derived_from_inventory(self):
        for count in (1, 3):
            with self.subTest(count=count), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                inputs = root / "api/openapi"
                inputs.mkdir(parents=True)
                operations = [{"operationID": f"Read{index}", "sdkMethod": f"Client.Read{index}",
                               "method": "GET", "path": f"/api/v2/item{index}", "request": "",
                               "schema": "Health", "cas": False, "buildTag": ""}
                              for index in range(count)]
                values = {"operations.json": operations,
                          "contract.base.json": {"paths": {op["path"]: {"get": {}} for op in operations}},
                          "models.base.json": {"components": {"schemas": {}}},
                          "externaljobs.json": {"paths": {}, "schemas": {}}}
                for name, value in values.items():
                    (inputs / name).write_text(json.dumps(value))
                declarations = [{"package": "", "name": op["sdkMethod"], "tag": "", "doc": "",
                                 "signature": "func (*Client) " + op["operationID"] + "()"}
                                for op in operations]
                with patch.object(reference, "ROOT", root), patch.object(
                        reference.subprocess, "check_output", return_value=json.dumps(declarations)):
                    pages = reference.generate("fixture-go")
                self.assertIn(f"outside the {count} generated v2 operations", pages["api-reference.md"])


if __name__ == "__main__":
    unittest.main()
