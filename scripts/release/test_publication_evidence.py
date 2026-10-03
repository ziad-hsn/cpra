"""Publication graph fixtures; no upload, provider, signing or release occurs."""
import copy
import json
import stat
import sys
import unittest
from unittest.mock import patch

import publication_evidence
import qualification
import release
import test_sdk_evidence as sdk_fixtures


class PublicationEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.sdk = sdk_fixtures.SDKEvidenceTests('test_consistent_bytes_and_both_compilers_remain_pending_review')
        self.sdk.setUp()
        self.addCleanup(self.sdk.doCleanups)
        self.root = self.sdk.root
        self.manifest = self.sdk.manifest
        self.guide = release.ROOT / 'docs/release-engineering.md'
        self.manifest['source_files']['docs/release-engineering.md'] = {'mode': '100644', 'sha256': release.sha(self.guide)}
        self.sdk.dependency['release_identity'] = release.evidence_identity(self.manifest)
        self.sdk.save()
        self.out = self.root / 'distribution'
        self.out.mkdir(mode=0o700)
        self.oci = {'image': 'ghcr.io/ziad-hsn/cpra', 'version': self.manifest['version'],
                    'commit': self.manifest['commit'], 'digest': 'sha256:' + 'b' * 64,
                    'platforms': {'amd64': 'sha256:' + 'c' * 64, 'arm64': 'sha256:' + 'd' * 64}}
        # These named byte fixtures are not executable archives, real SBOMs or
        # OCI subjects. Other component readers remain responsible for execution.
        for target in release.RECIPE['targets']:
            suffix = '.zip' if target.startswith('windows_') else '.tar.gz'
            (self.out / ('cpra-' + self.manifest['version'] + '-' + target.replace('_', '-') + suffix)).write_bytes(b'not an executable archive\n')
            (self.out / ('cpra-' + target + '.spdx.json')).write_bytes(b'not a real SBOM\n')
        for arch in ('amd64', 'arm64'):
            for kind in ('deb', 'rpm'):
                (self.out / ('cpra-' + self.manifest['version'] + '-linux-' + arch + '.' + kind)).write_bytes(b'not a native package\n')
            (self.out / ('cpra-image-linux-' + arch + '.spdx.json')).write_bytes(b'not a real image SBOM\n')
        (self.out / ('cpra-' + self.manifest['version'] + '-source.tar.gz')).write_bytes(b'not a source archive\n')
        (self.out / ('cpra-chart-' + self.manifest['chart_version'] + '.tgz')).write_bytes(b'not a Helm chart\n')
        (self.out / 'trusted_root.jsonl').write_bytes(b'not a trusted production root\n')
        (self.out / 'RELEASE.json').write_bytes(release.encoded(self.manifest))
        (self.out / 'OCI.json').write_bytes(release.encoded(self.oci))
        self.plan_path = self.root / 'publication.json'
        versions = self.sdk.versions
        modules = {}
        for module, root in ((sdk_fixtures.CORE, 'sdk/go'), (sdk_fixtures.WORKER, 'sdk/go/worker')):
            row = self.sdk.dependency['private_modules'][module]
            base = self.sdk.cache / 'modules/cache/download' / module / '@v'
            modules[module] = {'version': versions[module], 'tag': root + '/' + versions[module],
                               'zip_sha256': release.sha(base / (versions[module] + '.zip')),
                               'mod_sha256': release.sha(base / (versions[module] + '.mod')),
                               'info_sha256': release.sha(base / (versions[module] + '.info')),
                               'sum': row['Sum'], 'go_mod_sum': row['GoModSum'],
                               'requires': {sdk_fixtures.CORE: versions[sdk_fixtures.CORE]} if module == sdk_fixtures.WORKER else {}}
        modules[publication_evidence.APP] = {'version': self.manifest['version'], 'tag': self.manifest['version'],
                                            'requires': {sdk_fixtures.CORE: versions[sdk_fixtures.CORE]}}
        self.plan = {'schema_version': 1, 'release_identity': release.evidence_identity(self.manifest),
                     'repository': 'ziad-hsn/cpra', 'procedure': {'path': 'docs/release-engineering.md', 'sha256': release.sha(self.guide)},
                     'artifacts': {}, 'modules': modules,
                     'images': {'repository': self.oci['image'], 'index_digest': self.oci['digest'], 'platform_digests': self.oci['platforms']},
                     'chart': {'repository': 'oci://ghcr.io/ziad-hsn/charts/cpra', 'version': self.manifest['chart_version'],
                               'image_digest': self.oci['digest'], 'archive': 'cpra-chart-' + self.manifest['chart_version'] + '.tgz'},
                     'signing': copy.deepcopy(publication_evidence.SIGNING), 'steps': list(publication_evidence.STEPS),
                     'resume': copy.deepcopy(publication_evidence.RESUME)}
        self.components = {'sdk': {'status': 'consistent_pending_review', 'versions': copy.deepcopy(versions)},
                           'packaging': {'status': 'consistent_pending_review'}}
        self.save()

    def save(self, *, rehash=True):
        if rehash:
            release.checksums(self.out)
            self.plan['artifacts'] = {p.name: release.sha(p) for p in self.out.iterdir()
                                      if p.is_file() and p.name != 'SHA256SUMS' and not p.name.endswith('.sigstore.json')}
        self.plan_path.write_bytes(release.encoded(self.plan))

    def verify(self):
        return publication_evidence.verify_plan(self.manifest, self.plan_path, self.out, self.sdk.cache,
                                                qualification.Inputs(), components=self.components)

    def test_bound_payload_graph_keeps_actual_publication_and_downloads_unqualified(self):
        result = self.verify()
        modules = result['modules']
        self.assertEqual(modules[sdk_fixtures.CORE]['tag'], 'sdk/go/v0.2.0-rc.1')
        self.assertEqual(modules[sdk_fixtures.WORKER]['tag'], 'sdk/go/worker/v0.3.0-rc.1')
        self.assertEqual(modules[publication_evidence.APP]['tag'], 'v0.1.0-rc.1')
        self.assertIn(str(self.out / 'SHA256SUMS'), result['input_sha256'])
        self.assertFalse(result['release_gate_passed'])
        self.assertTrue(result['publication_locked'])
        self.assertFalse(result['publication_executed'])
        self.assertFalse(result['public_downloads_verified'])
        self.assertFalse(result['publisher_implementation_qualified'])

    def test_valid_plan_cannot_qualify_missing_native_or_sdk_execution(self):
        for name in ('sdk', 'packaging'):
            with self.subTest(component=name):
                original = self.components[name]
                self.components[name] = {'status': 'missing'}
                self.assertEqual(self.verify()['status'], 'incomplete')
                self.components[name] = original

    def test_plan_cannot_substitute_different_versions_from_executed_sdk_consumers(self):
        self.components['sdk']['versions'][sdk_fixtures.CORE] = 'v0.9.0'
        with self.assertRaisesRegex(ValueError, 'executed consumer campaigns'):
            self.verify()

    def test_policy_booleans_cannot_be_supplied_as_numeric_flags(self):
        for field, name, number in [('signing', 'offline_verification_required', 1),
                                    ('resume', 'replace_published_objects', 0)]:
            with self.subTest(field=field):
                original = self.plan[field][name]
                self.plan[field][name] = number
                self.save()
                with self.assertRaises(ValueError):
                    self.verify()
                self.plan[field][name] = original

    def test_changed_distribution_bytes_fail_against_original_checksums(self):
        (self.out / 'cpra-linux_amd64.spdx.json').write_bytes(b'changed artifact')
        with self.assertRaisesRegex(ValueError, 'retained input changed'):
            self.verify()

    def test_unlisted_distribution_file_and_duplicate_checksums_are_rejected(self):
        extra = self.out / 'unlisted.txt'
        extra.write_bytes(b'unlisted')
        with self.assertRaisesRegex(ValueError, 'omits or adds'):
            self.verify()
        extra.unlink()
        sums = self.out / 'SHA256SUMS'
        sums.write_text(sums.read_text() + sums.read_text().splitlines()[0] + '\n')
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            self.verify()

    def test_required_archives_packages_sboms_and_deployment_bytes_cannot_be_removed(self):
        for name in ('cpra-v0.1.0-rc.1-darwin-arm64.tar.gz', 'cpra-v0.1.0-rc.1-linux-amd64.deb',
                     'cpra-windows_arm64.spdx.json', 'cpra-image-linux-arm64.spdx.json',
                     'cpra-chart-0.1.0.tgz', 'OCI.json', 'trusted_root.jsonl'):
            path = self.out / name
            original = path.read_bytes()
            with self.subTest(removed=name):
                path.unlink(); self.save()
                with self.assertRaisesRegex(ValueError, 'omits required'):
                    self.verify()
                path.write_bytes(original)
        self.save()

    def test_plan_cannot_include_itself_or_link_to_external_distribution_bytes(self):
        original = self.plan_path
        self.plan_path = self.out / 'publication.json'
        self.save()
        with self.assertRaisesRegex(ValueError, 'outside its own'):
            self.verify()
        self.plan_path.unlink(); self.plan_path = original
        p = self.out / 'cpra-linux_amd64.spdx.json'
        raw = p.read_bytes(); p.unlink()
        elsewhere = self.root / 'elsewhere'; elsewhere.write_bytes(raw); p.symlink_to(elsewhere)
        self.save()
        with self.assertRaisesRegex(ValueError, 'link or alias'):
            self.verify()

    def test_planned_digest_set_must_equal_final_inventory(self):
        self.plan['artifacts'].pop('cpra-linux_amd64.spdx.json')
        self.save(rehash=False)
        with self.assertRaisesRegex(ValueError, 'artifacts differ'):
            self.verify()

    def test_module_versions_nested_tags_and_archive_hashes_are_bound(self):
        for field, wrong in [('version', 'v0.9.0'), ('tag', 'v0.2.0-rc.1'), ('zip_sha256', 'e' * 64)]:
            with self.subTest(field=field):
                original = self.plan['modules'][sdk_fixtures.CORE][field]
                self.plan['modules'][sdk_fixtures.CORE][field] = wrong
                self.save()
                with self.assertRaisesRegex(ValueError, 'planned versions'):
                    self.verify()
                self.plan['modules'][sdk_fixtures.CORE][field] = original

    def test_module_dependency_cycles_and_version_disagreement_fail(self):
        original = copy.deepcopy(self.sdk.dependency['requirements'])
        changes = [lambda r: r[publication_evidence.APP].update({sdk_fixtures.CORE: 'v9.0.0'}),
                   lambda r: r[sdk_fixtures.CORE].update({sdk_fixtures.WORKER: self.sdk.versions[sdk_fixtures.WORKER]}),
                   lambda r: r[sdk_fixtures.WORKER].update({publication_evidence.APP: self.manifest['version']})]
        for change in changes:
            with self.subTest(change=change):
                self.sdk.dependency['requirements'] = copy.deepcopy(original)
                change(self.sdk.dependency['requirements']); self.sdk.save()
                with self.assertRaises(ValueError):
                    self.verify()

    def test_changed_or_uninventoried_private_module_payloads_fail(self):
        files = self.sdk.dependency['private_cache_files']
        name = next(name for name in files if name.endswith('.zip'))
        (self.sdk.cache / 'modules' / name).write_bytes(b'changed ZIP')
        with self.assertRaisesRegex(ValueError, 'retained input changed'):
            self.verify()

    def test_preparation_summary_cannot_replace_original_module_graph(self):
        self.sdk.dependency['private_cache_files'] = {}
        self.sdk.save()
        with self.assertRaisesRegex(ValueError, 'retained payload'):
            self.verify()

    def test_oci_subjects_and_chart_image_digest_must_match(self):
        for change in (lambda p: p['images'].update(index_digest='sha256:' + 'f' * 64),
                       lambda p: p['images']['platform_digests'].pop('arm64'),
                       lambda p: p['chart'].update(image_digest='sha256:' + 'f' * 64),
                       lambda p: p['chart'].update(repository='oci://example.invalid/cpra')):
            original = copy.deepcopy(self.plan)
            with self.subTest(change=change):
                change(self.plan); self.save()
                with self.assertRaises(ValueError):
                    self.verify()
            self.plan = original

    def test_signing_cannot_relax_workflow_issuer_source_digest_or_offline_verification(self):
        for field, wrong in [('workflow_identity', 'https://example.invalid/workflow'),
                             ('oidc_issuer', 'https://example.invalid/issuer'), ('source_ref', 'refs/heads/development'),
                             ('verify_exact_subject_digests', False), ('offline_verification_required', False)]:
            with self.subTest(field=field):
                original = self.plan['signing'][field]; self.plan['signing'][field] = wrong; self.save()
                with self.assertRaisesRegex(ValueError, 'signature verification policy'):
                    self.verify()
                self.plan['signing'][field] = original

    def test_publish_order_requires_dependency_readback_before_application_tag(self):
        for steps in ([step for step in publication_evidence.STEPS if step != 'verify-public-sdk'],
                      list(reversed(publication_evidence.STEPS)), ['publish-application-tag'],
                      [*publication_evidence.STEPS, 'overwrite-tag']):
            with self.subTest(steps=steps):
                self.plan['steps'] = steps; self.save()
                with self.assertRaisesRegex(ValueError, 'publication order'):
                    self.verify()

    def test_resume_cannot_replace_assets_tags_or_ignore_verification_failures(self):
        for field, wrong in [('replace_published_objects', True), ('verify_existing_objects_before_resume', False),
                             ('on_mismatch', 'continue'), ('record_progress_privately', False)]:
            with self.subTest(field=field):
                original = self.plan['resume'][field]; self.plan['resume'][field] = wrong; self.save()
                with self.assertRaisesRegex(ValueError, 'resume could replace'):
                    self.verify()
                self.plan['resume'][field] = original

    def test_source_bound_procedure_cannot_be_replaced_by_unreviewed_text(self):
        self.plan['procedure']['sha256'] = 'f' * 64
        self.save()
        with self.assertRaisesRegex(ValueError, 'procedure is not part'):
            self.verify()

    def test_another_release_and_summary_only_plan_are_rejected(self):
        self.plan['release_identity']['commit'] = 'b' * 40
        self.save()
        with self.assertRaisesRegex(ValueError, 'another release identity'):
            self.verify()
        self.plan_path.write_bytes(release.encoded({'status': 'pass', 'release_gate_passed': True}))
        with self.assertRaisesRegex(ValueError, 'unsupported plan'):
            self.verify()

    def test_collector_reads_recorded_bytes_and_writes_an_exclusive_private_plan(self):
        for name in qualification.READER_FILES:
            source = release.ROOT / name
            self.manifest['source_files'][name] = {'mode': '100755' if source.stat().st_mode & 0o111 else '100644',
                                                  'sha256': release.sha(source)}
        self.sdk.dependency['release_identity'] = release.evidence_identity(self.manifest)
        self.sdk.save()
        (self.out / 'RELEASE.json').write_bytes(release.encoded(self.manifest))
        self.save()
        output = self.root / 'plans/generated.json'
        argv = ['publication_evidence.py', '--release', str(self.out / 'RELEASE.json'),
                '--distribution', str(self.out), '--dependencies', str(self.sdk.cache), '--out', str(output)]
        with patch.object(sys, 'argv', argv):
            publication_evidence.main()
        plan = json.loads(output.read_text())
        self.assertEqual(plan['modules'][sdk_fixtures.WORKER]['tag'], 'sdk/go/worker/v0.3.0-rc.1')
        self.assertEqual(plan['artifacts']['OCI.json'], release.sha(self.out / 'OCI.json'))
        self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)
        original = output.read_bytes()
        with patch.object(sys, 'argv', argv), self.assertRaisesRegex(ValueError, 'already exists'):
            publication_evidence.main()
        self.assertEqual(output.read_bytes(), original)

    def test_collector_cannot_write_inside_original_inputs_or_source(self):
        for output in (self.out / 'new.json', self.sdk.cache / 'new.json', release.ROOT / 'new-publication.json'):
            argv = ['publication_evidence.py', '--release', str(self.out / 'RELEASE.json'),
                    '--distribution', str(self.out), '--dependencies', str(self.sdk.cache), '--out', str(output)]
            with self.subTest(output=output), patch.object(sys, 'argv', argv), \
                    patch.object(qualification, 'reader_source', side_effect=AssertionError('read source')):
                with self.assertRaisesRegex(ValueError, 'overlaps inputs'):
                    publication_evidence.main()
                self.assertFalse(output.exists())
