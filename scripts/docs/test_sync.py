import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import sync


class NavigationSyncTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name) / "app"
        self.root.mkdir()
        self.docs = self.root / "docs"
        self.docs.mkdir()
        (self.docs / "index.md").write_text("# Home\n")
        self.nav = self.root / "navigation.json"
        self.nav.write_text(json.dumps([{"Home": "index.md"}]))
        (self.docs / "source-version.json").write_text(json.dumps({"source_commit": "a" * 40,
            "release_candidate_commit": "b" * 40, "updated": "2026-10-01"}))
        self.site = Path(self.directory.name) / "pages"
        (self.site / "_sources").mkdir(parents=True)
        (self.site / "_sources/overrides").mkdir()
        (self.site / "_sources/overrides/main.html").write_text("{% extends 'base.html' %}\n{% block extrahead %}preserved{% endblock %}\n{% block announce %}old{% endblock %}\n")
        self.config = self.site / "_sources/mkdocs.yml"
        self.config.write_text("site_name: CPRa\nnav:\n  - Previous: old.md\nextra:\n  source_commit: preserved\nvalidation:\n  links:\n    not_found: warn\n")
        for name, value in (("ROOT", self.root), ("NAVIGATION", self.nav)):
            patcher = patch.object(sync, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_sync_covers_pages_and_preserves_other_site_settings(self):
        result = sync.sync(self.site)
        self.assertEqual(result["navigation_pages"], 1)
        self.assertEqual((self.site / "_sources/docs/index.md").read_bytes(), (self.docs / "index.md").read_bytes())
        self.assertIn('source_commit: "' + "a" * 40 + '"', self.config.read_text())
        self.assertIn("not_found: warn", self.config.read_text())
        self.assertNotIn("old.md", self.config.read_text())
        self.assertEqual((self.site / "_sources/source-version.json").read_bytes(), (self.docs / "source-version.json").read_bytes())
        self.assertIn("{% block extrahead %}preserved{% endblock %}", (self.site / "_sources/overrides/main.html").read_text())
        self.assertIn("Unreleased SDK working source", (self.site / "_sources/overrides/main.html").read_text())
        self.assertEqual(sync.sync(self.site, check=True), result)

    def test_stale_navigation_is_rejected_in_check_mode_without_mutation(self):
        sync.sync(self.site)
        original = self.config.read_bytes()
        self.nav.write_text(json.dumps([{"Changed title": "index.md"}]))
        with self.assertRaisesRegex(ValueError, "configuration_changed"):
            sync.sync(self.site, check=True)
        self.assertEqual(self.config.read_bytes(), original)

    def test_missing_unlisted_duplicate_and_escape_pages_are_rejected(self):
        invalid = [[{"Missing": "missing.md"}], [{"Home": "index.md"}, {"Again": "index.md"}],
                   [{"Escape": "../index.md"}], [{"Home": "index.md#fragment"}]]
        original = self.config.read_bytes()
        for entries in invalid:
            with self.subTest(entries=entries):
                self.nav.write_text(json.dumps(entries))
                with self.assertRaises(ValueError):
                    sync.sync(self.site)
                self.assertEqual(self.config.read_bytes(), original)
        self.nav.write_text(json.dumps([{"Home": "index.md"}]))
        (self.docs / "new.md").write_text("# New\n")
        with self.assertRaisesRegex(ValueError, "unlisted_pages"):
            sync.sync(self.site)

    def test_site_config_and_docs_symlinks_are_rejected(self):
        (self.docs / "unsafe.txt").symlink_to(self.root.parent / "outside.txt")
        with self.assertRaisesRegex(ValueError, "symlink"):
            sync.sync(self.site)

    def test_engineering_records_remain_available_outside_user_navigation(self):
        for name in ("implementation/review.md", "review/version-review.md", "candidate/index.md"):
            path = self.docs / name
            path.parent.mkdir(exist_ok=True)
            path.write_text("# Dated engineering record\n")
        result = sync.sync(self.site)
        self.assertEqual(result["navigation_pages"], 1)
        self.assertTrue((self.site / "_sources/docs/implementation/review.md").is_file())
        self.assertNotIn("Dated engineering", self.config.read_text())

    def test_source_version_and_announcement_drift_are_rejected_without_overwrite(self):
        sync.sync(self.site)
        metadata = self.site / "_sources/source-version.json"
        metadata.write_text("{}")
        with self.assertRaisesRegex(ValueError, "source_metadata_changed"):
            sync.sync(self.site, check=True)
        self.assertEqual(metadata.read_text(), "{}")
        sync.sync(self.site)
        template = self.site / "_sources/overrides/main.html"
        template.write_text(template.read_text().replace("Unreleased SDK working source", "incorrect scope"))
        with self.assertRaisesRegex(ValueError, "announcement_changed"):
            sync.sync(self.site, check=True)

    def test_target_symlinks_are_rejected_before_outside_writes(self):
        outside = self.root / "outside"
        outside.mkdir()
        (self.site / "_sources/docs").symlink_to(outside, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlinks"):
            sync.sync(self.site)
        self.assertEqual(list(outside.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
