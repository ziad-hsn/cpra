from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import check


class DocumentationLinksTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.docs = self.root / "docs"
        (self.docs / "implementation").mkdir(parents=True)
        (self.root / "internal").mkdir()
        (self.root / "internal/server.go").write_text("package server\n")
        self.root_patch = patch.object(check, "ROOT", self.root)
        self.root_patch.start()
        self.addCleanup(self.root_patch.stop)

    def check_document(self, name, target):
        document = self.docs / name
        document.write_text(f"[Reference]({target})\n")
        errors, evidence = [], []
        check.check_links(document, self.docs, errors, evidence)
        return errors, evidence

    def test_implementation_links_require_existing_repository_source(self):
        self.assertEqual(self.check_document("implementation/record.md", "../../internal/server.go"), ([], []))
        errors, evidence = self.check_document("implementation/record.md", "../../internal/missing.go")
        self.assertEqual(len(errors), 1)
        self.assertEqual(evidence, [])

    def test_local_evidence_availability_is_reported_without_requiring_artifacts(self):
        target = "../../bin/verification/campaign/result.json"
        for available in (False, True):
            with self.subTest(available=available):
                if available:
                    artifact = self.root / "bin/verification/campaign/result.json"
                    artifact.parent.mkdir(parents=True)
                    artifact.write_text("{}")
                errors, evidence = self.check_document("implementation/record.md", target)
                self.assertEqual(errors, [])
                self.assertEqual(evidence, [{"document": "docs/implementation/record.md",
                                             "target": target, "available": available}])

    def test_public_pages_cannot_link_outside_docs(self):
        for target in ("../internal/server.go", "../bin/verification/campaign/result.json"):
            with self.subTest(target=target):
                errors, evidence = self.check_document("guide.md", target)
                self.assertEqual(len(errors), 1)
                self.assertEqual(evidence, [])

    def test_missing_docs_links_are_rejected_in_both_scopes(self):
        for name in ("guide.md", "implementation/record.md"):
            with self.subTest(name=name):
                errors, evidence = self.check_document(name, "missing.md")
                self.assertEqual(len(errors), 1)
                self.assertEqual(evidence, [])

    def test_implementation_exception_cannot_escape_repository(self):
        for target in ("../../../outside.md", "../../bin/verification/%2e%2e/%2e%2e/%2e%2e/outside.md",
                       str(self.root / "internal/server.go")):
            with self.subTest(target=target):
                errors, evidence = self.check_document("implementation/record.md", target)
                self.assertEqual(len(errors), 1)
                self.assertEqual(evidence, [])

    def test_symlink_cannot_escape_evidence_directory_or_repository(self):
        evidence = self.root / "bin/verification"
        evidence.mkdir(parents=True)
        (evidence / "outside").symlink_to(self.root.parent, target_is_directory=True)
        errors, local_evidence = self.check_document("implementation/record.md", "../../bin/verification/outside")
        self.assertEqual(len(errors), 1)
        self.assertEqual(local_evidence, [])


class SourceLayoutTests(unittest.TestCase):
    def test_current_layout_and_pre_refactor_candidate_are_supported(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            legacy = root / "internal/loader/schema"
            current = root / "internal/manifest"
            with self.assertRaises(FileNotFoundError):
                check.source_path(root, "internal/manifest", "internal/loader/schema")
            legacy.mkdir(parents=True)
            self.assertEqual(check.source_path(root, "internal/manifest", "internal/loader/schema"), legacy)
            current.mkdir()
            self.assertEqual(check.source_path(root, "internal/manifest", "internal/loader/schema"), current)


if __name__ == "__main__":
    unittest.main()
