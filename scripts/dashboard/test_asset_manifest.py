import json
from pathlib import Path
import tempfile
import unittest

import asset_manifest


class AssetManifestTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        for name in ("dashboard/src/App.tsx", "dashboard/pnpm-lock.yaml", "internal/httpserver/assets/index.html",
                     "sdk/go/collection/parse.go", "LICENSES/dashboard.txt", "brand/palette.json", "brand/dist/palette.css"):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("original")
        (self.root / asset_manifest.MANIFEST).write_text(json.dumps(asset_manifest.snapshot(self.root)))

    def test_current_inputs_pass_without_node_or_go(self):
        asset_manifest.verify(self.root)

    def test_changes_additions_and_deletions_are_rejected(self):
        for name in ("dashboard/src/App.tsx", "dashboard/pnpm-lock.yaml", "internal/httpserver/assets/index.html",
                     "sdk/go/collection/parse.go", "LICENSES/dashboard.txt", "dashboard/src/New.tsx"):
            with self.subTest(name=name):
                path = self.root / name
                old = path.read_bytes() if path.exists() else None
                path.write_text("modified")
                with self.assertRaisesRegex(ValueError, "stale"):
                    asset_manifest.verify(self.root)
                path.unlink()
                if old is not None:
                    with self.assertRaises(ValueError):
                        asset_manifest.verify(self.root)
                    path.write_bytes(old)

    def test_shared_palette_changes_require_dashboard_rebuild(self):
        for name in ("brand/palette.json", "brand/dist/palette.css"):
            with self.subTest(name=name):
                path = self.root / name
                old = path.read_bytes()
                path.write_text("changed palette")
                with self.assertRaisesRegex(ValueError, "stale"):
                    asset_manifest.verify(self.root)
                path.write_bytes(old)

    def test_missing_manifest_requires_regeneration(self):
        (self.root / asset_manifest.MANIFEST).unlink()
        with self.assertRaisesRegex(ValueError, "manifest is missing"):
            asset_manifest.verify(self.root)


if __name__ == "__main__":
    unittest.main()
