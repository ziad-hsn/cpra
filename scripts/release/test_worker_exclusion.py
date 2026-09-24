import subprocess
import unittest

from verify_worker_exclusion import builtin_tags, json_stream, require_symbol_absent


class WorkerExclusionOracleTests(unittest.TestCase):
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
            "excluded.CatalogRecord{}.JobTypeReferencesExtra undefined "
            "(type persistence.CatalogRecord has no field or method JobTypeReferencesExtra)",
            "excluded.CatalogRecord{}.JobTypeReferences undefined "
            "(type persistence.OtherRecord has no field or method JobTypeReferences)",
            "excluded.OtherRecord{}.JobTypeReferences undefined "
            "(type persistence.CatalogRecord has no field or method JobTypeReferences)",
        ):
            with self.subTest(diagnostic=diagnostic), self.assertRaises(RuntimeError):
                require_symbol_absent(subprocess.CompletedProcess([], 1, "", diagnostic), "JobTypeReferences")

    def test_multiple_go_list_documents_are_not_silently_ignored(self):
        self.assertEqual(list(json_stream(' {"ImportPath":"a"}\n{"ImportPath":"b"} ')),
                         [{"ImportPath": "a"}, {"ImportPath": "b"}])
        with self.assertRaises(ValueError):
            list(json_stream('{"ImportPath":"a"}\ninvalid'))


if __name__ == "__main__":
    unittest.main()
