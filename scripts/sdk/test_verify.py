import tempfile
from pathlib import Path
import unittest
import zipfile

from verify import (CORE, WORKER, VERSION, module_files, stage_module,
                    verify_archive_identity, verify_documentation_archive)


class ArchiveContractTests(unittest.TestCase):
    def test_archive_excludes_nested_worker_secrets_and_generated_binaries(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "sdk"
            root.mkdir()
            (root / "go.mod").write_text("module " + CORE + "\n\ngo 1.25.0\n")
            (root / "client.go").write_text("package cpra\n")
            (root / "LICENSE").write_text("fixture license\n")
            (root / ".env.local").write_text("not for distribution")
            (root / "private.pem").write_text("not for distribution")
            (root / "worker").mkdir()
            (root / "worker/go.mod").write_text("module " + CORE + "/worker\n")
            (root / "worker/runner.go").write_text("package worker\n")
            proxy = Path(temp) / "proxy"
            first = stage_module(proxy, CORE, root)
            second = stage_module(proxy, CORE, root)
            self.assertEqual(first, second)
            with zipfile.ZipFile(proxy / CORE / "@v" / (VERSION + ".zip")) as archive:
                paths = [name.split("/" + VERSION + "/")[-1] for name in archive.namelist()]
                self.assertEqual(len(paths), 3)
                self.assertFalse(any("worker/" in name or "private" in name or ".env" in name for name in paths))

    def test_symlink_is_not_followed(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "secret.go").symlink_to(root / "outside")
            with self.assertRaisesRegex(ValueError, "symlink"):
                list(module_files(root))

    def test_local_replace_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "go.mod").write_text("module example.com/test\n  replace example.com/x => ../x\n")
            with self.assertRaisesRegex(ValueError, "replace"):
                stage_module(root / "proxy", CORE, root)

    def test_downloaded_content_must_match_every_candidate_file(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "source"
            source.mkdir()
            (source / "go.mod").write_text("module " + CORE + "\n\ngo 1.25.0\n")
            (source / "client.go").write_text("package cpra\n")
            proxy = root / "proxy"
            stage_module(proxy, CORE, source)
            candidate = proxy / CORE / "@v" / (VERSION + ".zip")
            verify_archive_identity(proxy, CORE, str(candidate))
            for changed in (False, True):
                downloaded = root / "downloaded.zip"
                with zipfile.ZipFile(candidate) as original, zipfile.ZipFile(downloaded, "w") as archive:
                    for name in original.namelist():
                        data = original.read(name)
                        if changed and name.endswith("client.go"):
                            data += b"// changed\n"
                        archive.writestr(name, data)
                    if not changed:
                        archive.writestr(CORE + "@" + VERSION + "/unexpected.txt", "extra")
                with self.assertRaisesRegex(RuntimeError, "differs"):
                    verify_archive_identity(proxy, CORE, str(downloaded))

    def test_documentation_must_be_in_the_downloaded_archive(self):
        with tempfile.TemporaryDirectory() as temp:
            archive_path = Path(temp) / "module.zip"
            for module in (CORE, WORKER):
                names = {"README.md", "LICENSE", "go.mod", "go.sum", "doc.go", "example_test.go"}
                if module == CORE:
                    names.update(f"{package}/{name}" for package in ("api", "collection")
                                 for name in ("doc.go", "example_test.go"))
                for missing in (None, "README.md", "LICENSE", "example_test.go"):
                    with zipfile.ZipFile(archive_path, "w") as archive:
                        for name in names - {missing}:
                            archive.writestr(module + "@" + VERSION + "/" + name, "fixture\n")
                    if missing is None:
                        self.assertEqual(verify_documentation_archive(module, archive_path), sorted(names))
                    else:
                        with self.assertRaisesRegex(ValueError, "missing documentation input"):
                            verify_documentation_archive(module, archive_path)

    def test_empty_package_documentation_fails(self):
        with tempfile.TemporaryDirectory() as temp:
            archive_path = Path(temp) / "module.zip"
            with zipfile.ZipFile(archive_path, "w") as archive:
                for name in ("README.md", "LICENSE", "go.mod", "go.sum", "doc.go", "example_test.go"):
                    archive.writestr(WORKER + "@" + VERSION + "/" + name, "\n" if name == "doc.go" else "fixture\n")
            with self.assertRaisesRegex(ValueError, "empty documentation input: doc.go"):
                verify_documentation_archive(WORKER, archive_path)


if __name__ == "__main__":
    unittest.main()
