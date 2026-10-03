"""Retained SDK report/payload fixtures, not downloaded-module qualification."""
import base64
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import release
import sdk_evidence
from test_evidence import manifest_fixture

CORE, WORKER = sdk_evidence.CORE, sdk_evidence.WORKER


def sha(data):
    return hashlib.sha256(data).hexdigest()


class SDKEvidenceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.manifest = manifest_fixture()
        self.versions = {CORE: 'v0.2.0-rc.1', WORKER: 'v0.3.0-rc.1'}
        self.cache = self.root / 'dependencies'
        self.cache.mkdir()
        self.files = {}
        archives = {}
        cache_files = {}
        for module, relative in sdk_evidence.MODULE_ROOTS.items():
            contents = {name: ('fixture input: ' + module + '/' + name + '\n').encode()
                        for name in sdk_evidence.documentation_inputs(module)}
            contents['go.mod'] = ('module ' + module + '\n\ngo 1.25.0\n' +
                                  (f'require {CORE} {self.versions[CORE]}\n' if module == WORKER else '')).encode()
            contents['client.go' if module == CORE else 'runner.go'] = b'package fixture\n'
            if module == WORKER:
                contents['worker.go'] = b'package fixture\n'
            self.files[module] = contents
            for name, data in contents.items():
                self.manifest['source_files'][relative + '/' + name] = {'mode': '100644', 'sha256': sha(data)}
            target = self.cache / 'modules/cache/download' / module / '@v'
            target.mkdir(parents=True)
            archive = target / (self.versions[module] + '.zip')
            with zipfile.ZipFile(archive, 'w') as output:
                for name, data in contents.items():
                    output.writestr(module + '@' + self.versions[module] + '/' + name, data)
            (target / (self.versions[module] + '.mod')).write_bytes(contents['go.mod'])
            (target / (self.versions[module] + '.info')).write_text(json.dumps(
                {'Version': self.versions[module], 'Time': self.manifest['source_date']}))
            # h1 fields are explicit fixtures. No Go downloads or checksum
            # calculations are performed by these report-reader tests.
            archives[module] = {'Path': module, 'Version': self.versions[module],
                                'Sum': 'h1:' + base64.b64encode(bytes(32)).decode(),
                                'GoModSum': 'h1:' + base64.b64encode(bytes(32)).decode(),
                                'ArchiveSHA256': release.sha(archive),
                                'Requires': {CORE: self.versions[CORE]} if module == WORKER else {}}
            for path in target.iterdir():
                cache_files[path.relative_to(self.cache / 'modules').as_posix()] = release.sha(path)
        self.dependency = {'schema_version': 1, 'status': 'pass', 'evidence_class': 'private_dependency_cache',
                           'release_identity': release.evidence_identity(self.manifest),
                           'bootstrap_checksum_exceptions': [CORE, WORKER],
                           'verification_environment': self.manifest['recipe']['environment'],
                           'private_modules': archives, 'private_cache_files': cache_files,
                           'requirements': {release.RECIPE['module']: {CORE: self.versions[CORE]},
                                            WORKER: {CORE: self.versions[CORE]}, CORE: {}}}
        self.paths = [self.root / 'minimum.json', self.root / 'release.json']
        self.reports = []
        for path, compiler in zip(self.paths, (sdk_evidence.MINIMUM_COMPILER, release.RECIPE['go_version'])):
            directory = path.with_name(path.name + '.modules')
            directory.mkdir(mode=0o700)
            retained = {}
            for module, role in ((CORE, 'sdk'), (WORKER, 'worker')):
                retained[module] = {}
                target = self.cache / 'modules/cache/download' / module / '@v'
                for origin in ('source', 'downloaded'):
                    fields = {}
                    for field, suffix in (('Zip', '.zip'), ('GoMod', '.mod'), ('Info', '.info')):
                        payload = directory / (role + '-' + origin + suffix)
                        if origin == 'downloaded' and field == 'Zip':
                            # Different ZIP compression is harmless when every
                            # module filename and byte remains identical.
                            with zipfile.ZipFile(payload, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
                                for name, data in self.files[module].items():
                                    archive.writestr(module + '@' + self.versions[module] + '/' + name, data)
                        else:
                            shutil.copyfile(target / (self.versions[module] + suffix), payload)
                        fields[field] = {'file': payload.name, 'sha256': release.sha(payload), 'bytes': payload.stat().st_size}
                    retained[module][origin] = fields
            self.reports.append({'status': 'pass', 'evidenceClass': 'local_module_archive',
                                 'publicationReady': False, 'serverQualification': 'pending',
                                 'sourceCommit': self.manifest['commit'], 'versions': self.versions,
                                 'candidateContentIdentityMatched': True,
                                 'compiler': 'go version ' + compiler + ' linux/amd64',
                                 'checks': sorted(sdk_evidence.CHECKS),
                                 'moduleArchives': archives,
                                 'downloadedModules': [{key: value[key] for key in ('Path', 'Version', 'Sum', 'GoModSum')}
                                                       for value in archives.values()],
                                 'documentationArchiveInputs': {module: sorted(sdk_evidence.documentation_inputs(module))
                                                               for module in self.versions},
                                 'packageDocumentation': [{'package': package, 'buildTags': tags,
                                                           'documentationRendered': True, 'examplesExecuted': True}
                                                          for package, tags in sorted(sdk_evidence.DOCUMENTED_PACKAGES)],
                                 'retainedModules': retained})
        self.save()

    def save(self):
        (self.cache / 'dependencies.json').write_bytes(release.encoded(self.dependency))
        for path, report in zip(self.paths, self.reports):
            path.write_bytes(release.encoded(report))

    def verify(self):
        return sdk_evidence.verify_sdk_consumers(self.manifest, self.cache, self.paths)

    def test_consistent_bytes_and_both_compilers_remain_pending_review(self):
        report = self.verify()
        self.assertEqual(report['status'], 'consistent_pending_review')
        self.assertFalse(report['release_gate_passed'])
        self.assertFalse(report['public_download_qualified'])
        self.assertEqual(report['versions'], self.versions)
        self.assertEqual({row['compiler'] for row in report['reports']},
                         {sdk_evidence.MINIMUM_COMPILER, release.RECIPE['go_version']})
        self.assertEqual(len(report['input_sha256']), 33)
        self.assertTrue(all(Path(name).is_file() for name in report['input_sha256']))

    def test_source_archive_compression_may_differ_between_compilers(self):
        report = self.reports[0]
        report['moduleArchives'] = copy.deepcopy(report['moduleArchives'])
        for module, role in ((CORE, 'sdk'), (WORKER, 'worker')):
            record = report['retainedModules'][module]['source']['Zip']
            path = self.paths[0].with_name('minimum.json.modules') / record['file']
            with zipfile.ZipFile(path, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
                for name, data in self.files[module].items():
                    archive.writestr(module + '@' + self.versions[module] + '/' + name, data)
            record.update(sha256=release.sha(path), bytes=path.stat().st_size)
            report['moduleArchives'][module]['ArchiveSHA256'] = record['sha256']
            self.assertNotEqual(record['sha256'], self.dependency['private_modules'][module]['ArchiveSHA256'])
        self.save()
        self.assertEqual(self.verify()['status'], 'consistent_pending_review')

    def test_each_source_archive_must_match_its_own_digest(self):
        self.reports[0]['moduleArchives'] = copy.deepcopy(self.reports[0]['moduleArchives'])
        self.reports[0]['moduleArchives'][CORE]['ArchiveSHA256'] = 'c' * 64
        self.save()
        with self.assertRaisesRegex(ValueError, 'recorded archive digest'):
            self.verify()

    def test_canonical_cache_digest_cannot_contradict_its_archive_record(self):
        self.dependency['private_modules'][CORE]['ArchiveSHA256'] = 'd' * 64
        self.save()
        with self.assertRaisesRegex(ValueError, 'canonical dependency archive digest'):
            self.verify()

    def test_missing_stale_or_incomplete_consumer_claims_cannot_pass(self):
        cases = (
            lambda r: r.update(status='failed'),
            lambda r: r.update(sourceCommit='b' * 40),
            lambda r: r.update(publicationReady=True),
            lambda r: r.update(evidenceClass='published_module'),
            lambda r: r.update(evidenceClass='working_tree_module_archive'),
            lambda r: r.update(candidateContentIdentityMatched=False),
            lambda r: r.update(retainedModules={}),
            lambda r: r['versions'].update({WORKER: 'v0.9.0'}),
            lambda r: r['checks'].remove('worker negative compilation'),
            lambda r: r['checks'].append(r['checks'][0]),
            lambda r: r['packageDocumentation'].pop(),
            lambda r: r['packageDocumentation'][0].update(examplesExecuted=False),
            lambda r: r['packageDocumentation'][0].update(package='example.com/other'),
            lambda r: r.update(documentationArchiveInputs={}),
            lambda r: r['downloadedModules'][0].update(Sum='h1:incorrect'),
            lambda r: r['downloadedModules'].__setitem__(1, r['downloadedModules'][0]),
        )
        original = copy.deepcopy(self.reports[0])
        for mutate in cases:
            with self.subTest(mutate=mutate):
                self.reports[0] = copy.deepcopy(original)
                mutate(self.reports[0]); self.save()
                with self.assertRaises(ValueError):
                    self.verify()
        self.reports[0] = original; self.save()

    def test_same_compiler_or_a_missing_report_is_not_minimum_coverage(self):
        for compiler in ('go version go1.26.0 linux/amd64', self.reports[1]['compiler']):
            with self.subTest(compiler=compiler):
                self.reports[0]['compiler'] = compiler; self.save()
                with self.assertRaisesRegex(ValueError, 'compiler coverage'):
                    self.verify()
        with self.assertRaisesRegex(ValueError, 'two distinct'):
            sdk_evidence.verify_sdk_consumers(self.manifest, self.cache, [self.paths[0]])
        with self.assertRaisesRegex(ValueError, 'two distinct'):
            sdk_evidence.verify_sdk_consumers(self.manifest, self.cache, [self.paths[0], self.paths[0]])

    def test_application_worker_and_archive_versions_must_agree(self):
        original = copy.deepcopy(self.dependency)
        for mutate in (lambda d: d['requirements'][release.RECIPE['module']].update({CORE: 'v0.9.0'}),
                       lambda d: d['private_modules'][WORKER]['Requires'].update({CORE: 'v0.9.0'}),
                       lambda d: d['private_modules'][CORE].update(GoModSum='h1:invalid')):
            with self.subTest(mutate=mutate):
                self.dependency = copy.deepcopy(original)
                mutate(self.dependency); self.save()
                with self.assertRaises(ValueError):
                    self.verify()

    def test_changed_retained_payload_and_changed_cache_fail(self):
        for path in (self.paths[0].with_name('minimum.json.modules') / 'sdk-downloaded.zip',
                     next((self.cache / 'modules').rglob('*.zip'))):
            with self.subTest(path=path):
                original = path.read_bytes()
                path.write_bytes(original + b'changed')
                with self.assertRaises(ValueError):
                    self.verify()
                path.write_bytes(original)

    def test_retained_payload_paths_and_symlinks_cannot_escape_evidence(self):
        record = self.reports[0]['retainedModules'][CORE]['downloaded']['Zip']
        original_name = record['file']
        for name in ('../outside.zip', '/outside.zip', 'worker-downloaded.zip'):
            with self.subTest(name=name):
                record['file'] = name; self.save()
                with self.assertRaisesRegex(ValueError, 'path or size'):
                    self.verify()
        record['file'] = original_name; self.save()
        path = self.paths[0].with_name('minimum.json.modules') / original_name
        external = self.root / 'outside.zip'
        external.write_bytes(path.read_bytes())
        path.unlink(); path.symlink_to(external)
        with self.assertRaisesRegex(ValueError, 'regular file'):
            self.verify()

    def rewrite_download(self, contents):
        record = self.reports[0]['retainedModules'][CORE]['downloaded']['Zip']
        path = self.paths[0].with_name('minimum.json.modules') / record['file']
        with zipfile.ZipFile(path, 'w') as archive:
            for name, data in contents:
                archive.writestr(CORE + '@' + self.versions[CORE] + '/' + name, data)
        record.update(sha256=release.sha(path), bytes=path.stat().st_size)
        self.save()
        return path

    def test_rehashed_archive_cannot_replace_frozen_content_or_hide_missing_inputs(self):
        cases = (
            [(name, b'changed' if name == 'client.go' else data) for name, data in self.files[CORE].items()],
            [(name, data) for name, data in self.files[CORE].items() if name != 'example_test.go'],
            list(self.files[CORE].items()) + [('unexpected.go', b'package other\n')],
            list(self.files[CORE].items()) + [('../outside.go', b'package other\n')],
            list(self.files[CORE].items()) + [('worker/runner.go', b'package fixture\n')],
            list(self.files[CORE].items()) + [('Client.go', b'package fixture\n')],
            [(name, data) for name, data in self.files[CORE].items() if name != 'client.go'],
        )
        for contents in cases:
            with self.subTest(names=[name for name, _ in contents]):
                self.rewrite_download(contents)
                with self.assertRaises(ValueError):
                    self.verify()

    def test_malformed_payload_records_remain_validation_errors(self):
        original = copy.deepcopy(self.reports[0])
        for mutate in (lambda r: r['downloadedModules'][0].update(Path=[]),
                       lambda r: r['retainedModules'][CORE]['source']['Zip'].update(bytes=True),
                       lambda r: r['moduleArchives'][CORE].pop('ArchiveSHA256'),
                       lambda r: r['packageDocumentation'].__setitem__(0, [])):
            with self.subTest(mutate=mutate):
                self.reports[0] = copy.deepcopy(original)
                mutate(self.reports[0]); self.save()
                with self.assertRaises(ValueError):
                    self.verify()

    def test_module_definition_and_info_must_match_archive_and_source_time(self):
        for field, data in (('GoMod', b'module example.com/other\n'),
                            ('Info', json.dumps({'Version': 'v0.9.0', 'Time': self.manifest['source_date']}).encode()),
                            ('Info', json.dumps({'Version': self.versions[CORE], 'Time': '1970-01-01T00:02:03'}).encode()),
                            ('Info', json.dumps({'Version': self.versions[CORE], 'Time': '2026-09-30T00:00:00Z'}).encode())):
            with self.subTest(field=field, data=data):
                record = self.reports[0]['retainedModules'][CORE]['source'][field]
                path = self.paths[0].with_name('minimum.json.modules') / record['file']
                original = path.read_bytes(); previous = copy.deepcopy(record)
                path.write_bytes(data); record.update(sha256=sha(data), bytes=len(data)); self.save()
                with self.assertRaises(ValueError):
                    self.verify()
                path.write_bytes(original); record.update(previous)

    def test_archives_are_bounded_after_decompression(self):
        path = self.rewrite_download(list(self.files[CORE].items()))
        with patch.object(sdk_evidence, 'MAX_ARCHIVE_BYTES', 10):
            with self.assertRaisesRegex(ValueError, 'uncompressed byte limit'):
                sdk_evidence.archive_contents(path, CORE, self.versions[CORE], self.manifest)

    def test_input_change_during_reader_cannot_issue_consistent_report(self):
        original = sdk_evidence.archive_contents
        changed = False
        def contents(*args):
            nonlocal changed
            result = original(*args)
            if not changed:
                changed = True
                dependency = self.cache / 'dependencies.json'
                dependency.write_bytes(dependency.read_bytes() + b' ')
            return result
        with patch.object(sdk_evidence, 'archive_contents', side_effect=contents):
            with self.assertRaisesRegex(ValueError, 'changed during'):
                self.verify()

    def test_duplicate_and_nonfinite_json_fields_fail_closed(self):
        for data in ('{"status":"failed","status":"pass"}', '{"status":"pass","size":NaN}'):
            with self.subTest(data=data):
                self.paths[0].write_text(data)
                with self.assertRaises(ValueError):
                    self.verify()

    def test_cli_records_failure_and_preserves_existing_output(self):
        manifest_path = self.root / 'RELEASE.json'
        manifest_path.write_bytes(release.encoded(self.manifest))
        output = self.root / 'verified.json'
        self.reports[0]['sourceCommit'] = 'c' * 40; self.save()
        args = [sys.executable, '-B', str(Path(sdk_evidence.__file__)), '--release', str(manifest_path),
                '--dependencies', str(self.cache), '--consumer', str(self.paths[0]),
                '--consumer', str(self.paths[1]), '--out', str(output)]
        result = subprocess.run(args, text=True, capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 1, result.stderr)
        report = json.loads(output.read_text())
        self.assertEqual(report['status'], 'invalid_evidence')
        self.assertFalse(report['release_gate_passed'])
        if os.name != 'nt':
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
        before = output.read_bytes()
        self.reports[0]['sourceCommit'] = self.manifest['commit']; self.save()
        result = subprocess.run(args, text=True, capture_output=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(output.read_bytes(), before)


if __name__ == '__main__':
    unittest.main()
