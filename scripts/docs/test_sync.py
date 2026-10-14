import copy
import hashlib
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
        for prefix in sync.ARCHIVE_PREFIXES:
            self.assertIn("  /" + prefix + "**\n", self.config.read_text())
        self.assertIn("not_found: warn", self.config.read_text())

    def test_archive_patterns_replace_stale_settings_without_weakening_link_validation(self):
        original = self.config.read_text()
        for setting in ("not_in_nav: |\n  /**", "not_in_nav: /**"):
            for ending in ("\n", ""):
                with self.subTest(setting=setting, ending=ending):
                    self.config.write_text(original + setting + ending)
                    sync.sync(self.site)
                    text = self.config.read_text()
                    self.assertEqual(text.count("not_in_nav:"), 1)
                    self.assertEqual(text.partition("not_in_nav: |\n")[2].splitlines(),
                                     ["  /" + prefix + "**" for prefix in sync.ARCHIVE_PREFIXES])
                    self.assertIn("not_found: warn", text)
                    self.assertTrue(sync.sync(self.site, check=True)["copies_match"])

    def test_duplicate_archive_settings_are_rejected_before_mutation(self):
        original = self.config.read_text()
        for second in ("not_in_nav: |\n  /two\n", "not_in_nav: |\n  /two", "not_in_nav: /two"):
            with self.subTest(second=second):
                text = original + "not_in_nav: |\n  /one\n" + second
                self.config.write_text(text)
                with self.assertRaisesRegex(ValueError, "at most one not_in_nav"):
                    sync.sync(self.site)
                self.assertEqual(self.config.read_text(), text)
                self.assertFalse((self.site / "_sources/docs").exists())

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


class FrozenSourceSyncTests(NavigationSyncTests):
    def setUp(self):
        super().setUp()
        tools = self.root / "scripts/docs"
        tools.mkdir(parents=True)
        self.nav.rename(tools / "navigation.json")
        self.nav = tools / "navigation.json"
        self.announcement = tools / "announcement.html.j2"
        self.announcement.write_bytes(sync.ANNOUNCEMENT.read_bytes())
        (tools / "sync.py").write_text("# isolated rendering input\n")
        for module in ("go.mod", "sdk/go/go.mod", "sdk/go/worker/go.mod"):
            path = self.root / module
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("module example.com/test\n\ngo 1.25.0\n")
        assets = self.root / "internal/httpserver/assets"
        assets.mkdir(parents=True)
        (assets / "index.html").write_text("<main>fixture</main>")
        persistence = self.root / "internal/persistence"
        persistence.mkdir(parents=True)
        (persistence / "extensions_base.go").write_text(
            "//go:build !externaljobs\n\npackage persistence\nconst LatestFormatVersion = " +
            str(sync.release.RECIPE["storage_format_version"]) + "\n")
        for target, name, value in ((sync, "NAVIGATION", self.nav),
                                    (sync, "ANNOUNCEMENT", self.announcement),
                                    (sync.release, "ROOT", self.root)):
            patcher = patch.object(target, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.manifest = {"schema_version": 1, "candidate": False, "dirty": False,
            "version": "v0.1.0-rc.1", "commit": "c" * 40, "source_date_epoch": 123,
            "source_date": "1970-01-01T00:02:03Z", "recipe": copy.deepcopy(sync.release.RECIPE),
            "toolchain_archives": copy.deepcopy(sync.release.TOOLCHAINS[sync.release.RECIPE["go_version"]]),
            "storage_format_version": sync.release.RECIPE["storage_format_version"],
            "dashboard_sha256": sync.release.tree_digest(assets),
            "source_files": {p.relative_to(self.root).as_posix():
                {"mode": "100644", "sha256": hashlib.sha256(p.read_bytes()).hexdigest()}
                for p in self.root.rglob("*") if p.is_file()}}
        self.release_path = self.root.parent / "RELEASE.json"
        self.release_path.write_bytes(sync.release.encoded(self.manifest))

    def test_release_metadata_renders_without_changing_source(self):
        before = {p: p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        result = sync.sync(self.site, release_path=self.release_path)
        self.assertEqual(result["release_identity"], sync.release.evidence_identity(self.manifest))
        metadata = self.site / "_sources/source-version.json"
        actual = json.loads(metadata.read_bytes())
        self.assertEqual(actual["source_commit"], "c" * 40)
        self.assertEqual(actual["updated"], "1970-01-01")
        self.assertEqual(actual["release_version"], "v0.1.0-rc.1")
        self.assertFalse(actual["uncommitted_changes"])
        self.assertIn("pending", actual["release_status"])
        self.assertEqual(actual["release_candidate_commit"], "b" * 40)
        self.assertEqual(metadata.read_bytes(), (self.site / "_sources/docs/source-version.json").read_bytes())
        self.assertIn('source_commit: "' + "c" * 40 + '"', self.config.read_text())
        self.assertEqual(sync.sync(self.site, check=True, release_path=self.release_path), result)
        self.assertEqual({p: p.read_bytes() for p in before}, before)

    def test_wrong_or_changed_release_inputs_fail_before_site_mutation(self):
        mutations = (
            lambda m: m.update(dirty=True), lambda m: m.update(candidate=True),
            lambda m: m.update(source_date="wrong"),
            lambda m: m["source_files"]["docs/index.md"].update(sha256="0" * 64),
            lambda m: m["source_files"].pop("scripts/docs/sync.py"),
            lambda m: m["source_files"].pop("docs/index.md"),
        )
        original = self.config.read_bytes()
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                manifest = copy.deepcopy(self.manifest)
                mutate(manifest)
                self.release_path.write_bytes(sync.release.encoded(manifest))
                with self.assertRaises(ValueError):
                    sync.sync(self.site, release_path=self.release_path)
                self.assertEqual(self.config.read_bytes(), original)
                self.assertFalse((self.site / "_sources/docs").exists())

    def test_unrecorded_archived_document_is_rejected(self):
        extra = self.docs / "implementation/unrecorded.md"
        extra.parent.mkdir()
        extra.write_text("# Not part of the selected source\n")
        with self.assertRaisesRegex(ValueError, "documentation files differ"):
            sync.sync(self.site, release_path=self.release_path)
        self.assertFalse((self.site / "_sources/docs").exists())

    def test_release_check_rejects_site_drift_without_repair(self):
        sync.sync(self.site, release_path=self.release_path)
        metadata = self.site / "_sources/source-version.json"
        metadata.write_text("{}")
        with self.assertRaisesRegex(ValueError, "source_metadata_changed"):
            sync.sync(self.site, check=True, release_path=self.release_path)
        self.assertEqual(metadata.read_text(), "{}")
        sync.sync(self.site, release_path=self.release_path)
        content = self.site / "_sources/docs/index.md"
        content.write_text("# Changed after generation\n")
        with self.assertRaisesRegex(ValueError, "documentation copies differ"):
            sync.sync(self.site, check=True, release_path=self.release_path)
        self.assertEqual(content.read_text(), "# Changed after generation\n")


if __name__ == "__main__":
    unittest.main()
