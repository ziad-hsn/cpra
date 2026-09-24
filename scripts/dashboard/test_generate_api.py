"""Tests for the local browser wire-model generation boundary."""
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("generate_api.py")
ROOT = SCRIPT.resolve().parents[2]
spec = importlib.util.spec_from_file_location("browser_generator", SCRIPT)
generator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(generator)


class BrowserGenerationTests(unittest.TestCase):
    def test_optional_values_do_not_become_required_defaults(self):
        result = generator.wire_type({"type": "object", "properties": {"enabled": {"type": "boolean"}, "count": {"type": ["integer", "null"]}}, "additionalProperties": False}, set())
        self.assertIn('"enabled"?: boolean', result)
        self.assertIn('"count"?: number | null', result)

    def test_unknown_reference_or_type_fails(self):
        for value in [{"$ref": "https://remote.example/schema"}, {"type": "imaginary"}]:
            with self.assertRaises(ValueError):
                generator.wire_type(value, set())

    def test_default_projection_excludes_external_job_operations(self):
        _, content = generator.generate()
        for symbol in ["export type JobType", "WorkerAssignment", '"RegisterJobType"']:
            self.assertNotIn(symbol, content)
        self.assertIn("export type AccessInfo", content)
        self.assertIn('"value"?: string', content)
        self.assertIn("Write-only input", content)

    def test_models_and_descriptors_reproduce_in_distinct_roots_without_git(self):
        config = json.loads((ROOT / "dashboard/api-generation.json").read_text())
        paths = ["dashboard/api-generation.json", config["models"], config["operations"], config["contract"], "scripts/dashboard/generate_api.py"]
        outputs = []
        with tempfile.TemporaryDirectory() as temporary:
            for name in ["first", "different-root"]:
                root = Path(temporary) / name
                for relative in paths:
                    target = root / relative
                    target.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(ROOT / relative, target)
                subprocess.run([sys.executable, str(root / "scripts/dashboard/generate_api.py")], check=True)
                subprocess.run([sys.executable, str(root / "scripts/dashboard/generate_api.py"), "--check"], check=True)
                outputs.append((root / config["output"]).read_bytes())
                self.assertFalse((root / ".git").exists())
        self.assertEqual(outputs[0], outputs[1])

    def test_checked_output_matches_current_inputs(self):
        output, content = generator.generate()
        self.assertEqual(output.read_text(), content)

    def test_create_precondition_comes_from_http_contract(self):
        _, content = generator.generate()
        operations = content.split("export const operationContracts = ", 1)[1].split(" as const;", 1)[0]
        operations = json.loads(operations)
        self.assertTrue(operations["CreateMonitor"]["createOnly"])
        self.assertTrue(operations["CreateCredential"]["createOnly"])
        self.assertFalse(operations["AcknowledgeIncident"]["createOnly"])


if __name__ == "__main__":
    unittest.main()
