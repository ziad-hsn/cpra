from pathlib import Path
import json
import shutil
import tempfile
import unittest

import references


class PortableReferenceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.docs = self.root / "docs"
        (self.docs / "implementation").mkdir(parents=True)
        (self.root / "internal").mkdir()
        self.source = self.root / "internal/server.go"
        self.source.write_bytes(b"// Exact source bytes.\npackage server\n")
        self.record = self.docs / "implementation/record.md"

    def test_sources_remain_exact_and_preparation_is_repeatable_without_git(self):
        self.record.write_text("[Server](../../internal/server.go)\n")
        result = references.prepare(self.root)
        asset = self.docs / "review/source/internal/server.go.txt"
        self.assertEqual(asset.read_bytes(), self.source.read_bytes())
        self.assertIn("../review/source/internal/server.go.txt", self.record.read_text())
        self.assertEqual(references.prepare(self.root, check=True), result)
        clone = self.root / "other-root"
        clone.mkdir()
        shutil.copytree(self.docs, clone / "docs")
        shutil.copytree(self.root / "internal", clone / "internal")
        self.assertEqual(references.prepare(clone, check=True), result)

    def test_private_availability_never_copies_contents_or_verifies_results(self):
        artifact = self.root / "bin/verification/test/result.json"
        artifact.parent.mkdir(parents=True)
        secret = "private credential must never be published"
        artifact.write_text(secret)
        self.record.write_text("[Present](../../bin/verification/test/result.json)\n[Missing](../../bin/verification/missing.json)\n")
        result = references.prepare(self.root)
        self.assertEqual(result["private_references"], 2)
        self.assertFalse(result["private_results_verified"])
        for path in (self.docs / "review").rglob("*"):
            if path.is_file():
                self.assertNotIn(secret.encode(), path.read_bytes())
        index = (self.docs / references.EVIDENCE_INDEX).read_text()
        self.assertIn("present (file)", index)
        self.assertIn("missing (missing)", index)
        shutil.rmtree(self.root / "bin")
        self.assertEqual(references.prepare(self.root, check=True), result)

    def test_stale_source_download_and_new_evidence_fail_check(self):
        self.record.write_text("[Server](../../internal/server.go)\n")
        references.prepare(self.root)
        self.source.write_text("package changed\n")
        with self.assertRaisesRegex(ValueError, "references differ"):
            references.prepare(self.root, check=True)
        references.prepare(self.root)
        self.record.write_text(self.record.read_text() + "[New](../../bin/verification/new.json)\n")
        with self.assertRaisesRegex(ValueError, "not prepared"):
            references.prepare(self.root, check=True)

    def test_repository_escape_and_credential_files_are_rejected_before_writes(self):
        for link in ("../../../outside.go", "../../.env", "../../internal/credentials/key.json"):
            with self.subTest(link=link):
                self.record.write_text(f"[Unsafe]({link})\n")
                with self.assertRaises(ValueError):
                    references.prepare(self.root)
                self.assertFalse((self.docs / "review").exists())

    def test_source_and_output_symlinks_are_rejected(self):
        self.record.write_text("[Server](../../internal/server.go)\n")
        self.source.unlink()
        self.source.symlink_to(self.root.parent / "outside.go")
        with self.assertRaisesRegex(ValueError, "symlink"):
            references.prepare(self.root)
        self.source.unlink()
        self.source.write_text("package server\n")
        (self.docs / "review").symlink_to(self.root / "internal", target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlink"):
            references.prepare(self.root)

    def test_code_fences_are_not_rewritten_and_obsolete_owned_assets_are_removed(self):
        code = "```md\n[Example](../../internal/server.go)\n```\n"
        self.record.write_text(code + "[Server](../../internal/server.go)\n")
        references.prepare(self.root)
        self.assertTrue(self.record.read_text().startswith(code))
        unrelated = self.docs / "review/unrelated.txt"
        unrelated.write_text("keep")
        self.record.write_text(code)
        references.prepare(self.root)
        self.assertFalse((self.docs / "review/source/internal/server.go.txt").exists())
        self.assertEqual(unrelated.read_text(), "keep")

    def test_binary_and_oversized_sources_are_rejected_before_writes(self):
        self.record.write_text("[Server](../../internal/server.go)\n")
        for content in (b"\0package server", b"a" * (references.MAX_SOURCE_BYTES + 1)):
            with self.subTest(size=len(content)):
                self.source.write_bytes(content)
                with self.assertRaises(ValueError):
                    references.prepare(self.root)
                self.assertFalse((self.docs / "review").exists())

    def test_unrecorded_assets_cannot_silently_enter_the_site(self):
        self.record.write_text("[Server](../../internal/server.go)\n")
        references.prepare(self.root)
        (self.docs / "review/source/unrecorded.txt").write_text("must review")
        with self.assertRaisesRegex(ValueError, "unrecorded source download"):
            references.prepare(self.root, check=True)

    def test_inconsistent_private_metadata_is_rejected(self):
        self.record.write_text("[Missing](../../bin/verification/missing.json)\n")
        references.prepare(self.root)
        manifest = self.docs / references.MANIFEST
        data = json.loads(manifest.read_bytes())
        data["private"][0]["available_when_prepared"] = True
        manifest.write_text(json.dumps(data))
        with self.assertRaisesRegex(ValueError, "availability metadata"):
            references.prepare(self.root, check=True)


if __name__ == "__main__":
    unittest.main()
