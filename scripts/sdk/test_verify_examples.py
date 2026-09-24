import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("verify_examples", Path(__file__).with_name("verify_examples.py"))
VERIFY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VERIFY)


class ExampleEvidenceTests(unittest.TestCase):
    def run_check(self, code, expected=(), timeout=5):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        out = Path(temporary.name) / "report.json"
        report = {"status": "running", "checks": []}
        command = [sys.executable, "-u", "-c", code]
        return report, out, lambda: VERIFY.run_check(report, out, os.environ.copy(), "fixture", command, expected,
                                                    cwd=temporary.name, timeout=timeout)

    def test_missing_output_is_a_failed_row_even_with_zero_exit(self):
        _, out, run = self.run_check("print('unrelated')", ["required observation"])
        with self.assertRaises(RuntimeError):
            run()
        saved = json.loads(out.read_text())
        self.assertEqual(saved["status"], "failed")
        self.assertEqual(saved["checks"][0]["exit_code"], 0)
        self.assertEqual(saved["checks"][0]["status"], "failed")
        self.assertEqual(saved["checks"][0]["missing_expected_output"], ["required observation"])

    def test_timeout_keeps_partial_output_and_failed_command(self):
        _, out, run = self.run_check("import time; print('before timeout'); time.sleep(20)", timeout=0.3)
        with self.assertRaises(RuntimeError):
            run()
        row = json.loads(out.read_text())["checks"][0]
        self.assertTrue(row["timed_out"])
        self.assertEqual(row["status"], "failed")
        self.assertIn("before timeout", row["output"])

    def test_interruption_is_saved(self):
        _, out, run = self.run_check("pass")
        with mock.patch.object(VERIFY.subprocess, "Popen", side_effect=KeyboardInterrupt):
            with self.assertRaises(KeyboardInterrupt):
                run()
        self.assertEqual(json.loads(out.read_text())["status"], "interrupted")

    def test_large_output_is_bounded_in_report(self):
        _, out, run = self.run_check("print('x' * 40000)")
        run()
        row = json.loads(out.read_text())["checks"][0]
        self.assertTrue(row["output_truncated"])
        self.assertEqual(len(row["output"]), VERIFY.REPORT_OUTPUT_BYTES)
        self.assertEqual(row["output_bytes"], 40001)
        self.assertEqual(row["status"], "passed")

    def test_source_hash_includes_recipe_and_module_but_excludes_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name in ["go.mod", "scripts/sdk/verify_examples.py", "docs/sdk/verification.md"]:
                file = root / name
                file.parent.mkdir(parents=True, exist_ok=True)
                file.write_text("first")
            first = VERIFY.digest(root)
            (root / "docs/sdk/verification.md").write_text("new measured evidence")
            self.assertEqual(first, VERIFY.digest(root))
            (root / "go.mod").write_text("different dependencies")
            self.assertNotEqual(first, VERIFY.digest(root))
            second = VERIFY.digest(root)
            (root / "scripts/sdk/verify_examples.py").write_text("different recipe")
            self.assertNotEqual(second, VERIFY.digest(root))

    def test_wrong_local_module_cannot_pass_under_current_source_hash(self):
        selected = json.dumps({"Path": "github.com/ziad-hsn/cpra/sdk/go", "Dir": "/wrong/checkout"})
        with self.assertRaises(RuntimeError):
            VERIFY.check_module_directories(selected)


if __name__ == "__main__":
    unittest.main()
