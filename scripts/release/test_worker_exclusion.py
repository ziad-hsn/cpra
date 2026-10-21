import json
import subprocess
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import verify_worker_exclusion as exclusion
from verify_worker_exclusion import (MODULE, SDK_MODULE, builtin_tags, json_stream,
                                    require_symbol_absent, require_symbols_absent, selected_source_files)


class WorkerExclusionOracleTests(unittest.TestCase):
    def test_timed_out_build_retains_failure_and_partial_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'Makefile').write_text('ALL_DRIVER_TAGS = redis\n')
            go = root / 'fixture-go'
            go.write_text('compiler identity fixture; never executed')
            output = root / 'result.json'

            def run(command, **options):
                if command[1] == 'version':
                    self.assertEqual(options['timeout'], 180)
                    return subprocess.CompletedProcess(command, 0, 'go version go1.27.3 linux/amd64\n', '')
                self.assertEqual(command[1], 'build')
                self.assertEqual(options['timeout'], 900)
                raise subprocess.TimeoutExpired(command, options['timeout'],
                    output=b'compiler progress\n', stderr=b'undefined: excluded.WorkerAuthority\n')

            with patch.object(exclusion, 'ROOT', root), patch.object(subprocess, 'run', side_effect=run), \
                    self.assertRaises(subprocess.TimeoutExpired):
                exclusion.verify(str(go), 'off', output)
            report = json.loads(output.read_text())
            self.assertEqual(report['status'], 'failed')
            self.assertEqual(len(report['checks']), 2)
            failure = report['checks'][-1]
            self.assertTrue(failure['timedOut'])
            self.assertIsNone(failure['exitCode'])
            self.assertEqual(failure['timeoutSeconds'], 900)
            self.assertEqual(failure['stdout'], 'compiler progress\n')
            self.assertEqual(failure['stderr'], 'undefined: excluded.WorkerAuthority\n')
            self.assertNotIn('expectedUnavailableSymbols', failure)
            self.assertIn('sourceSHA256', failure)
            self.assertEqual(list((root / 'bin').iterdir()), [])

    def test_downloaded_sdk_sources_use_the_repository_source_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "source"
            root.mkdir()
            for module, base, relative, expected in (
                (MODULE, root, "internal/httpauth", "internal/httpauth/auth.go"),
                (SDK_MODULE, Path(directory) / "module-cache/sdk@v0.1.0", "api", "sdk/go/api/auth.go"),
                (SDK_MODULE, root / "sdk/go", ".", "sdk/go/auth.go"),
            ):
                package_dir = base / relative
                package_dir.mkdir(parents=True, exist_ok=True)
                source = package_dir / "auth.go"
                source.write_text("package fixture\n")
                package = {"Module": {"Path": module, "Dir": str(base)},
                           "ImportPath": module + ("/" + relative if relative != "." else ""),
                           "Dir": str(package_dir), "GoFiles": ["auth.go"]}
                self.assertEqual(selected_source_files(package, root), {expected: source})

    def test_source_mapping_rejects_foreign_packages_aliases_and_unframed_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "fixture.go"
            source.write_text("package fixture\n")
            package = {"Module": {"Path": MODULE, "Dir": str(root)},
                       "ImportPath": MODULE, "Dir": str(root), "GoFiles": [source.name]}
            cases = [dict(package, ImportPath=MODULE + "/other"),
                     dict(package, GoFiles=["../fixture.go"]),
                     dict(package, GoFiles=["fixture.go", "fixture.go"]),
                     dict(package, Module={"Path": "example.com/foreign", "Dir": str(root)}),
                     dict(package, Module={"Path": MODULE, "Dir": str(root / "other")})]
            for bad in cases:
                with self.subTest(package=bad), self.assertRaises((ValueError, OSError)):
                    selected_source_files(bad, root)
            link = root / "linked.go"
            link.symlink_to(source)
            with self.assertRaises(ValueError):
                selected_source_files(dict(package, GoFiles=[link.name]), root)

    def test_builtin_tag_recipe_is_explicit_and_excludes_extension(self):
        self.assertEqual(builtin_tags("ALL_DRIVER_TAGS = redis systemd\n"), "redis systemd")
        for bad in ("", "ALL_DRIVER_TAGS = redis externaljobs", "ALL_DRIVER_TAGS = $(TAGS)",
                    "ALL_DRIVER_TAGS = redis redis", "ALL_DRIVER_TAGS = redis\nALL_DRIVER_TAGS = mongo"):
            with self.subTest(value=bad), self.assertRaises(ValueError):
                builtin_tags(bad)

    def test_negative_compile_requires_the_exact_missing_symbol(self):
        for symbol, diagnostic in (
            ("WorkerAuthority", "fixture.go:3:16: undefined: excluded.WorkerAuthority\n"),
            ("AuthorizeWorker", "fixture.go:3:25: (*excluded.Authorizer).AuthorizeWorker undefined "
             "(type *httpauth.Authorizer has no field or method AuthorizeWorker)\n"),
            ("JobTypeReference", "fixture.go:3:16: undefined: excluded.JobTypeReference\n"),
            ("JobTypeVersionView", "fixture.go:3:16: undefined: excluded.JobTypeVersionView\n"),
            ("CanonicalJobTypeReferences", "fixture.go:3:18: undefined: excluded.CanonicalJobTypeReferences\n"),
            ("LookupJobTypeVersion", "fixture.go:3:25: (*excluded.Store).LookupJobTypeVersion undefined "
             "(type *persistence.Store has no field or method LookupJobTypeVersion)\n"),
            ("LookupBootstrapJobTypeVersion", "fixture.go:3:25: (*excluded.Store).LookupBootstrapJobTypeVersion undefined "
             "(type *persistence.Store has no field or method LookupBootstrapJobTypeVersion)\n"),
            ("ExternalJobsEnabled", "fixture.go:3:35: excluded.CatalogOptions{}.ExternalJobsEnabled undefined "
             "(type management.CatalogOptions has no field or method ExternalJobsEnabled)\n"),
            ("Workers", "fixture.go:3:28: (*excluded.Client).Workers undefined "
             "(type *cpra.Client has no field or method Workers)\n"),
            ("IterateWorkers", "fixture.go:3:28: (*excluded.Client).IterateWorkers undefined "
             "(type *cpra.Client has no field or method IterateWorkers)\n"),
            ("JobTypeReferences", "fixture.go:3:35: excluded.CatalogRecord{}.JobTypeReferences undefined "
             "(type persistence.CatalogRecord has no field or method JobTypeReferences)\n"),
        ):
            require_symbol_absent(subprocess.CompletedProcess([], 1, "", diagnostic), symbol)
        for code, diagnostic in ((0, ""), (1, "missing go.sum entry"), (1, "undefined: excluded.WrongName"),
                                 (1, "undefined: excluded.WorkerAuthorityExtra"),
                                 (1, "undefined: persistence.WorkerAuthority")):
            with self.subTest(code=code, diagnostic=diagnostic), self.assertRaises(RuntimeError):
                require_symbol_absent(subprocess.CompletedProcess([], code, "", diagnostic), "WorkerAuthority")
        for diagnostic in (
            "fixture.go:3:18: undefined: excluded.CanonicalJobTypeReferencesExtra\n",
            "fixture.go:3:25: (*excluded.Store).LookupJobTypeVersion undefined "
            "(type *persistence.Store has no field or method LookupJobTypeVersions)\n",
        ):
            symbol = "LookupJobTypeVersion" if "LookupJobTypeVersion" in diagnostic else "CanonicalJobTypeReferences"
            with self.subTest(diagnostic=diagnostic), self.assertRaises(RuntimeError):
                require_symbol_absent(subprocess.CompletedProcess([], 1, "", diagnostic), symbol)
        for diagnostic in (
            "(*excluded.Client).WorkersExtra undefined (type *cpra.Client has no field or method WorkersExtra)",
            "(*excluded.Client).Workers undefined (type *cpra.Client has no field or method WorkersExtra)",
            "(*excluded.Client).Workers undefined (type *cpra.Store has no field or method Workers)",
            "(*excluded.OtherClient).Workers undefined (type *cpra.OtherClient has no field or method Workers)",
        ):
            with self.subTest(diagnostic=diagnostic), self.assertRaises(RuntimeError):
                require_symbol_absent(subprocess.CompletedProcess([], 1, "", diagnostic), "Workers")
        for diagnostic in (
            "excluded.CatalogRecord{}.JobTypeReferencesExtra undefined "
            "(type persistence.CatalogRecord has no field or method JobTypeReferencesExtra)",
            "excluded.CatalogRecord{}.JobTypeReferences undefined "
            "(type persistence.OtherRecord has no field or method JobTypeReferences)",
            "excluded.OtherRecord{}.JobTypeReferences undefined "
            "(type persistence.CatalogRecord has no field or method JobTypeReferences)",
        ):
            with self.subTest(diagnostic=diagnostic), self.assertRaises(RuntimeError):
                require_symbol_absent(subprocess.CompletedProcess([], 1, "", diagnostic), "JobTypeReferences")
        for diagnostic in (
            "excluded.CatalogOptions{}.ExternalJobsEnabledExtra undefined "
            "(type management.CatalogOptions has no field or method ExternalJobsEnabledExtra)",
            "excluded.CatalogOptions{}.ExternalJobsEnabled undefined "
            "(type management.CatalogRecord has no field or method ExternalJobsEnabled)",
            "excluded.CatalogRecord{}.ExternalJobsEnabled undefined "
            "(type management.CatalogOptions has no field or method ExternalJobsEnabled)",
        ):
            with self.subTest(diagnostic=diagnostic), self.assertRaises(RuntimeError):
                require_symbol_absent(subprocess.CompletedProcess([], 1, "", diagnostic), "ExternalJobsEnabled")

    def test_multiple_go_list_documents_are_not_silently_ignored(self):
        self.assertEqual(list(json_stream(' {"ImportPath":"a"}\n{"ImportPath":"b"} ')),
                         [{"ImportPath": "a"}, {"ImportPath": "b"}])
        with self.assertRaises(ValueError):
            list(json_stream('{"ImportPath":"a"}\ninvalid'))

    def test_grouped_compilation_requires_every_symbol(self):
        output = ("fixture.go:3:16: undefined: excluded.WorkerAuthority\n"
                  "fixture.go:4:16: undefined: excluded.WorkerOffer\n")
        failed = subprocess.CompletedProcess([], 1, "", output)
        require_symbols_absent(failed, ("WorkerAuthority", "WorkerOffer"))
        # Another missing API cannot conceal one that leaked into this build.
        with self.assertRaises(RuntimeError):
            require_symbols_absent(failed, ("WorkerAuthority", "Store"))
        with self.assertRaises(RuntimeError):
            require_symbols_absent(subprocess.CompletedProcess([], 0, "", output),
                                   ("WorkerAuthority", "WorkerOffer"))
        for names in ((), ("WorkerAuthority", "WorkerAuthority")):
            with self.subTest(names=names), self.assertRaises(ValueError):
                require_symbols_absent(failed, names)


if __name__ == "__main__":
    unittest.main()
