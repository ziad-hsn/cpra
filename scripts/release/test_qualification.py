"""Private assessment regressions; synthetic inputs are not release evidence."""
import copy
import io
import json
import os
import shutil
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import qualification
import release
import oci_assembly
from test_evidence import manifest_fixture
import test_packaging_evidence as packaging_fixtures
import test_sdk_evidence as sdk_fixtures
import test_worker_evidence as worker_fixtures
import test_build_evidence as build_fixtures
import test_security_evidence as security_fixtures
import test_management_evidence as management_fixtures
import test_a2a_evidence as a2a_fixtures
import test_accessibility_evidence as accessibility_fixtures
import test_storage_evidence as storage_fixtures
import test_review_evidence as review_fixtures
import test_publication_evidence as publication_fixtures
import test_image_sbom as image_sbom_fixtures


def reader_manifest():
    manifest = manifest_fixture()
    for name in qualification.READER_FILES:
        path = release.ROOT / name
        manifest['source_files'][name] = {
            'mode': '100755' if path.stat().st_mode & 0o111 else '100644',
            'sha256': release.sha(path),
        }
    return manifest


class QualificationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.manifest = reader_manifest()
        self.config = {'schema_version': 1, 'components': {}}
        self.component_reader = qualification.inspect_component

    def fixture(self, cls):
        instance = cls(next(name for name in dir(cls) if name.startswith('test_')))
        instance.setUp()
        self.addCleanup(instance.doCleanups)
        return instance

    def assess(self, config=None, manifest=None):
        return qualification.assess(manifest or self.manifest, config or self.config, self.root)

    def test_empty_assessment_lists_every_requirement_and_cannot_pass(self):
        with patch.object(qualification.publish, 'run', side_effect=AssertionError('publication')):
            result = self.assess()
        self.assertEqual(result['status'], 'incomplete')
        self.assertEqual(set(result['components']), set(qualification.COMPONENTS))
        self.assertEqual(set(result['remaining']), set(qualification.COMPONENTS))
        self.assertFalse(result['release_gate_passed'])
        self.assertTrue(result['publication_locked'])
        self.assertFalse(result['endurance']['required'])
        self.assertFalse(result['endurance']['performed_by_this_assessment'])
        self.assertFalse(result['endurance']['claim_qualified'])
        for name in qualification.COMPONENTS:
            self.assertEqual(result['components'][name]['status'],
                             'missing' if name in qualification.READERS else 'not_implemented')

    def test_all_readers_exist_but_missing_actual_evidence_keeps_every_gate_closed(self):
        self.assertEqual(set(qualification.COMPONENTS), qualification.READERS)
        self.assertEqual(len(qualification.READERS), 14)
        self.assertEqual(list(qualification.COMPONENTS)[-1], 'independent_review')
        result = self.assess()
        self.assertTrue(all(row['status'] == 'missing' for row in result['components'].values()))
        self.assertTrue(result['publication_locked'])

    def test_review_reader_checks_real_signatures_without_qualifying_missing_execution(self):
        fixture = self.fixture(review_fixtures.ReviewEvidenceTests)
        fixture.manifest = self.manifest
        fixture.identity = release.evidence_identity(self.manifest)
        fixture.assessment = self.assess()
        fixture.assessment['input_sha256'][str(fixture.original)] = release.sha(fixture.original)
        fixture.components = {name: row for name, row in fixture.assessment['components'].items()
                              if name != 'independent_review'}
        fixture.records[0]['release_identity'] = fixture.identity
        fixture.records[0]['components'] = sorted(fixture.components)
        fixture.save()
        config = {'schema_version': 1, 'components': {'independent_review': {
            'assessment': str(fixture.assessment_path), 'policy': str(fixture.trust_path),
            'records': list(map(str, fixture.paths))}}}
        result = self.assess(config)
        review = result['components']['independent_review']
        self.assertEqual(review['status'], 'incomplete')
        self.assertEqual(review['approved_reviewers'], ['reviewer@example.invalid'])
        self.assertIn(str(fixture.paths[0]) + '.sig', result['input_sha256'])
        self.assertTrue(result['publication_locked'])
        fixture.original.write_bytes(b'replaced original execution bytes')
        result = self.assess(config)
        self.assertEqual(result['components']['independent_review']['status'], 'invalid_evidence')

    def decision_fixture(self, *, incomplete=None):
        """Controlled component readers plus real offline approval signatures."""
        fixture = self.fixture(review_fixtures.ReviewEvidenceTests)
        fixture.manifest = self.manifest
        fixture.identity = release.evidence_identity(self.manifest)
        original = self.component_reader
        rows = {name: {'status': 'consistent_pending_review', 'release_identity': fixture.identity,
                       'release_gate_passed': False,
                       'input_sha256': {str(fixture.original): release.sha(fixture.original)}}
                for name in qualification.COMPONENTS if name != 'independent_review'}
        if incomplete is not None:
            rows[incomplete]['status'] = 'incomplete'

        def inspect(name, manifest, entry, paths, inputs, components=None):
            if name == 'independent_review':
                return original(name, manifest, entry, paths, inputs, components)
            inputs.merge(rows[name]['input_sha256'])
            return copy.deepcopy(rows[name])

        self.enterContext(patch.object(qualification, 'inspect_component', side_effect=inspect))
        directory, record = str(self.root / 'distribution'), str(fixture.original)
        self.config = {'schema_version': 1, 'components': {
            'sdk': {'dependencies': str(self.root / 'dependencies'), 'consumers': [record]},
            **{name: {'campaigns': [record]} for name in
               ('management', 'external_worker', 'a2a', 'browser', 'accessibility', 'security', 'build_matrix')},
            'packaging': {'directory': directory, 'assembly': str(self.root / 'assembly')},
            'storage_upgrade': {'campaigns': [record], 'previous_release': record,
                                'previous_distribution': directory},
            'providers': {'build': directory, 'reports': [record]},
            'benchmark': {'builds': directory, 'comparison': directory, 'faults': directory},
            'publication_plan': {'plan': record, 'dependencies': str(self.root / 'dependencies')},
        }}
        fixture.assessment = self.assess()
        fixture.components = rows
        fixture.records[0]['release_identity'] = fixture.identity
        fixture.records[0]['components'] = sorted(rows)
        fixture.save()
        self.config['components']['independent_review'] = {
            'assessment': str(fixture.assessment_path), 'policy': str(fixture.trust_path),
            'records': list(map(str, fixture.paths)),
        }
        return fixture

    def decision(self, fixture, **kwargs):
        return qualification.publication_decision(self.manifest, self.config, self.root,
            policy=fixture.trust_path, policy_sha256=release.sha(fixture.trust_path), **kwargs)

    def test_private_decision_requires_every_reader_and_real_independent_approval(self):
        fixture = self.decision_fixture()
        with patch.object(qualification.publish, 'run', side_effect=AssertionError('public action')):
            result = self.decision(fixture)
        self.assertEqual(result['status'], 'ready_for_publication')
        self.assertTrue(result['prepublication_gate_passed'])
        self.assertEqual(result['remaining'], [])
        self.assertEqual(result['components']['independent_review']['approved_reviewers'],
                         ['reviewer@example.invalid'])
        self.assertFalse(result['release_gate_passed'])
        self.assertTrue(result['publication_locked'])
        self.assertFalse(result['public_actions_performed'])
        self.assertFalse(result['endurance']['required'])
        self.assertTrue(result['postpublication_required'])

    def test_incomplete_reader_cannot_be_overridden_by_an_authentic_approval(self):
        for name in qualification.COMPONENTS:
            if name == 'independent_review':
                continue
            with self.subTest(name=name):
                fixture = self.decision_fixture(incomplete=name)
                result = self.decision(fixture)
                self.assertFalse(result['prepublication_gate_passed'])
                self.assertIn(name, result['remaining'])
                self.assertIn('independent_review', result['remaining'])
                self.assertEqual(result['status'], 'incomplete')

    def test_candidate_cannot_select_another_trust_policy_or_rewrite_operator_pin(self):
        fixture = self.decision_fixture()
        with self.assertRaisesRegex(ValueError, 'trust policy changed'):
            qualification.publication_decision(self.manifest, self.config, self.root,
                policy=fixture.trust_path, policy_sha256='0' * 64)
        self.config['components']['independent_review']['policy'] = str(self.root / 'candidate-policy.json')
        with self.assertRaisesRegex(ValueError, 'operator trust policy'):
            self.decision(fixture)

    def test_changed_original_bytes_keep_publication_closed(self):
        fixture = self.decision_fixture()
        fixture.original.write_bytes(b'changed execution after independent review')
        result = self.decision(fixture)
        self.assertFalse(result['prepublication_gate_passed'])
        self.assertEqual(result['status'], 'invalid_evidence')
        self.assertEqual(result['components']['independent_review']['status'], 'invalid_evidence')
        self.assertIn('retained input changed', result['components']['management']['reason'])
        self.assertIn('outcomes differ', result['components']['independent_review']['reason'])

    def test_revoked_reviewer_or_unresolved_finding_keep_publication_closed(self):
        fixture = self.decision_fixture()
        fixture.trust['reviewers'] = [fixture.keys['second']]
        fixture.save()
        result = self.decision(fixture)
        self.assertFalse(result['prepublication_gate_passed'])
        self.assertEqual(result['components']['independent_review']['status'], 'invalid_evidence')
        fixture.trust['reviewers'] = [fixture.keys['reviewer']]
        fixture.records[0]['findings'] = [{'id': 'open-issue', 'component': 'security', 'status': 'open',
                                          'notes': 'Unresolved disposable test finding',
                                          'evidence': {str(fixture.original): release.sha(fixture.original)}}]
        fixture.save()
        result = self.decision(fixture)
        self.assertFalse(result['prepublication_gate_passed'])
        self.assertEqual(result['components']['independent_review']['status'], 'invalid_evidence')
        self.assertIn('unresolved findings', result['components']['independent_review']['reason'])
        fixture.records[0]['conclusion'] = 'changes_requested'
        fixture.save()
        result = self.decision(fixture)
        self.assertFalse(result['prepublication_gate_passed'])
        self.assertEqual(result['components']['independent_review']['status'], 'incomplete')
        self.assertEqual(result['components']['independent_review']['changes_requested_by'],
                         ['reviewer@example.invalid'])

    def test_release_decision_does_not_accept_a_stored_ready_summary(self):
        fixture = self.decision_fixture()
        self.config['prepublication_gate_passed'] = True
        with self.assertRaisesRegex(ValueError, 'configuration'):
            self.decision(fixture)

    def test_missing_review_and_noncanonical_trust_pin_cannot_authorize_publication(self):
        fixture = self.decision_fixture()
        del self.config['components']['independent_review']
        result = self.decision(fixture)
        self.assertFalse(result['prepublication_gate_passed'])
        self.assertIn('independent_review', result['remaining'])
        for value in ('', 'A' * 64, True, '0' * 63):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, 'trust policy pin'):
                qualification.publication_decision(self.manifest, self.config, self.root,
                    policy=fixture.trust_path, policy_sha256=value)

    def publisher(self, fixture, **overrides):
        directory = self.root / 'distribution'
        directory.mkdir(exist_ok=True)
        (directory / 'RELEASE.json').write_bytes(release.encoded(self.manifest))
        config_path = self.root / 'qualification.json'
        config_path.write_bytes(release.encoded(self.config))
        paths = dict(out=directory, assembly=self.root / 'assembly', dependencies=self.root / 'dependencies')
        paths.update(overrides)
        return qualification.publish.require_qualification(self.manifest, config_path, fixture.trust_path,
            release.sha(fixture.trust_path), **paths)

    def test_publisher_rereads_original_readers_and_binds_selected_transfer_inputs(self):
        fixture = self.decision_fixture()
        inputs, result = self.publisher(fixture)
        self.assertTrue(result['prepublication_gate_passed'])
        self.assertIn(str(self.root / 'qualification.json'), inputs.files)
        self.assertIn(str(self.root / 'distribution/RELEASE.json'), inputs.files)
        self.assertIn(str(fixture.paths[0]) + '.sig', inputs.files)
        for name in ('out', 'assembly', 'dependencies'):
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, 'publication inputs differ'):
                self.publisher(fixture, **{name: self.root / 'another'})

    def test_publisher_rejects_partial_signed_qualification_and_changed_inputs(self):
        fixture = self.decision_fixture(incomplete='providers')
        with self.assertRaisesRegex(ValueError, 'Public publication is locked.*providers'):
            self.publisher(fixture)
        fixture = self.decision_fixture()
        inputs, _ = self.publisher(fixture)
        fixture.original.write_bytes(b'changed after decision')
        with self.assertRaisesRegex(ValueError, 'retained input changed'):
            inputs.unchanged()

    def test_publication_reader_uses_retained_graph_without_qualifying_fake_package_bytes(self):
        fixture = self.fixture(publication_fixtures.PublicationEvidenceTests)
        for name in qualification.READER_FILES:
            fixture.manifest['source_files'][name] = self.manifest['source_files'][name]
        fixture.sdk.dependency['release_identity'] = release.evidence_identity(fixture.manifest)
        fixture.sdk.save()
        fixture.plan['release_identity'] = release.evidence_identity(fixture.manifest)
        (fixture.out / 'RELEASE.json').write_bytes(release.encoded(fixture.manifest))
        fixture.save()
        config = {'schema_version': 1, 'components': {
            'packaging': {'directory': str(fixture.out)},
            'publication_plan': {'plan': str(fixture.plan_path), 'dependencies': str(fixture.sdk.cache)},
        }}
        result = self.assess(config, fixture.manifest)
        procedure = result['components']['publication_plan']
        self.assertEqual(procedure['status'], 'incomplete')
        self.assertFalse(procedure['publication_executed'])
        self.assertFalse(procedure['publisher_implementation_qualified'])
        self.assertTrue(result['publication_locked'])
        self.assertIn(str(fixture.out / 'SHA256SUMS'), result['input_sha256'])
        fixture.plan['resume']['replace_published_objects'] = True
        fixture.save()
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['publication_plan']['status'], 'invalid_evidence')

    def test_review_and_publication_configuration_reject_summaries_and_ambiguous_records(self):
        invalid = [('independent_review', {'status': 'approved'}),
                   ('independent_review', {'assessment': 'one', 'policy': 'two', 'records': []}),
                   ('independent_review', {'assessment': 'one', 'policy': 'two', 'records': ['one', 'one']}),
                   ('independent_review', {'assessment': 'one', 'policy': 'two', 'records': ['one'] * 17}),
                   ('publication_plan', {'status': 'pass'}),
                   ('publication_plan', {'plan': 'one'}),
                   ('publication_plan', {'plan': 'one', 'dependencies': 'two', 'publish': True})]
        for name, entry in invalid:
            with self.subTest(name=name, entry=entry), self.assertRaises(ValueError):
                self.assess({'schema_version': 1, 'components': {name: entry}})

    def test_storage_reader_binds_final_archives_and_keeps_platform_gaps_open(self):
        fixture = self.fixture(storage_fixtures.StorageEvidenceTests)
        for name in qualification.READER_FILES:
            fixture.manifest['source_files'][name] = self.manifest['source_files'][name]
        stage = fixture.root / 'candidate-stage-linux_amd64'
        programs = {name: (stage / name).read_bytes() for name in ('cpra', 'cpractl')}
        inventory = {'target': 'linux_amd64', 'binaries': {name: release.sha(stage / name) for name in programs}}
        entries = {name: (data, True) for name, data in programs.items()}
        entries.update({'RELEASE.json': (release.encoded(fixture.manifest), False),
                        'DEPENDENCIES.json': (release.encoded(inventory), False)})
        release.package.archive(fixture.distribution / qualification.storage_evidence.archive_name(fixture.manifest, 'linux_amd64'),
                                entries, fixture.manifest['source_date_epoch'])
        (fixture.distribution / 'RELEASE.json').write_bytes(release.encoded(fixture.manifest))
        config = {'schema_version': 1, 'components': {
            'packaging': {'directory': str(fixture.distribution)},
            'storage_upgrade': {'campaigns': list(map(str, fixture.paths)),
                                'previous_release': str(fixture.previous_release),
                                'previous_distribution': str(fixture.previous_distribution)},
        }}
        result = self.assess(config, fixture.manifest)
        upgrade = result['components']['storage_upgrade']
        self.assertEqual(upgrade['status'], 'incomplete')
        self.assertEqual(len(upgrade['remaining_targets']), 5)
        self.assertTrue(result['publication_locked'])
        self.assertFalse(result['release_gate_passed'])
        original = fixture.paths[0].with_name(fixture.paths[0].name + '.inputs') / 'log/older-owner.log'
        self.assertIn(str(original), result['input_sha256'])
        original.write_bytes(b'replaced process log')
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['storage_upgrade']['status'], 'invalid_evidence')

    def test_storage_reader_requires_the_final_distribution(self):
        fixture = self.fixture(storage_fixtures.StorageEvidenceTests)
        entry = {'campaigns': fixture.paths, 'previous_release': fixture.previous_release,
                 'previous_distribution': fixture.previous_distribution}
        with self.assertRaisesRegex(ValueError, 'final distribution'):
            qualification.inspect_component('storage_upgrade', fixture.manifest, entry, {}, qualification.Inputs())

    def test_dirty_unfrozen_or_stale_reader_source_is_rejected_before_components(self):
        prefix, patch_version = self.manifest['recipe']['go_version'].rsplit('.', 1)
        different_compiler = prefix + '.' + str(int(patch_version) + 1)
        cases = [lambda m: m.update(dirty=True), lambda m: m.update(candidate=True),
                 lambda m: m['source_files'].pop(qualification.READER_FILES[0]),
                 lambda m: m['source_files'][qualification.READER_FILES[0]].update(sha256='a' * 64),
                 lambda m: m['toolchain_archives'].update(linux_amd64='a' * 64),
                 lambda m: m['recipe'].update(go_version=different_compiler)]
        for index, change in enumerate(cases):
            with self.subTest(index=index):
                manifest = copy.deepcopy(self.manifest)
                change(manifest)
                with patch.object(qualification, 'inspect_component', side_effect=AssertionError('reader invoked')):
                    with self.assertRaises(ValueError):
                        self.assess(manifest=manifest)

    def test_unknown_fields_unimplemented_readers_and_summary_substitutes_are_rejected(self):
        cases = [dict(self.config, release_gate_passed=True),
                 {'schema_version': True, 'components': {}},
                 {'schema_version': 1, 'components': {'worker': None}},
                 {'schema_version': 1, 'components': {'external_worker': {'status': 'pass'}}},
                 {'schema_version': 1, 'components': {'sdk': {'status': 'pass'}}},
                 {'schema_version': 1, 'components': {'benchmark': {
                     'builds': 'builds', 'comparison': 'comparison', 'faults': 'faults', 'soak': 'soak'}}},
                 {'schema_version': 1, 'components': {'browser': {'campaigns': ['one', 'one']}}},
                 {'schema_version': 1, 'components': {'packaging': {'directory': '../outside'}}}]
        for config in cases:
            with self.subTest(config=config), self.assertRaises(ValueError):
                self.assess(config=config)

    def test_actual_sdk_reader_inspects_payloads_and_stale_source_stays_invalid(self):
        fixture = self.fixture(sdk_fixtures.SDKEvidenceTests)
        for name in qualification.READER_FILES:
            fixture.manifest['source_files'][name] = self.manifest['source_files'][name]
        fixture.dependency['release_identity'] = release.evidence_identity(fixture.manifest)
        fixture.save()
        config = {'schema_version': 1, 'components': {'sdk': {
            'dependencies': str(fixture.cache), 'consumers': list(map(str, fixture.paths))}}}
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['status'], 'incomplete')
        self.assertEqual(result['components']['sdk']['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])
        retained = fixture.reports[0]['retainedModules'][sdk_fixtures.CORE]['downloaded']['Zip']['file']
        path = Path(str(fixture.paths[0]) + '.modules') / retained
        self.assertIn(str(path), result['input_sha256'])
        path.write_bytes(b'changed module payload; not an installable module')
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['status'], 'invalid_evidence')
        self.assertEqual(result['components']['sdk']['status'], 'invalid_evidence')
        self.assertTrue(result['publication_locked'])

    def test_worker_reader_inspects_original_sources_logs_and_module_payloads(self):
        fixture = self.fixture(worker_fixtures.WorkerEvidenceTests)
        for name in qualification.READER_FILES:
            fixture.manifest['source_files'][name] = self.manifest['source_files'][name]
        for path, report in zip(fixture.paths, fixture.reports):
            report['release_identity'] = release.evidence_identity(fixture.manifest)
            source = fixture.root / report['source_inventory']
            source.write_bytes(release.encoded(qualification.worker_evidence.source_inputs(fixture.manifest)))
            report['source_inventory_sha256'] = release.sha(source)
        fixture.save()
        config = {'schema_version': 1, 'components': {'external_worker': {'campaigns': list(map(str, fixture.paths))}}}
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['external_worker']['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])
        self.assertTrue(result['publication_locked'])
        original = fixture.paths[0].with_name(fixture.paths[0].stem + '.artifacts') / 'go.jsonl'
        self.assertIn(str(original), result['input_sha256'])
        original.write_bytes(original.read_bytes() + b'\n')
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['external_worker']['status'], 'invalid_evidence')
        self.assertTrue(result['publication_locked'])

    def test_worker_configuration_requires_bounded_distinct_original_campaign_paths(self):
        for campaigns in ([], ['one', 'one'], ['one', 'two', 'three']):
            with self.subTest(campaigns=campaigns), self.assertRaises(ValueError):
                self.assess({'schema_version': 1, 'components': {'external_worker': {'campaigns': campaigns}}})

    def test_security_reader_requires_original_scans_exclusion_and_contract_events(self):
        fixture = self.fixture(security_fixtures.SecurityEvidenceTests)
        for name in qualification.READER_FILES:
            fixture.manifest['source_files'][name] = self.manifest['source_files'][name]
        fixture.save()
        config = {'schema_version': 1, 'components': {'security': {'campaigns': list(map(str, fixture.paths))}}}
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['security']['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])
        self.assertTrue(result['publication_locked'])
        original = fixture.paths[1].with_name(fixture.paths[1].stem + '.artifacts') / 'worker-exclusion.json'
        self.assertIn(str(original), result['input_sha256'])
        original.write_bytes(original.read_bytes() + b'\n')
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['security']['status'], 'invalid_evidence')
        self.assertTrue(result['publication_locked'])

    def test_security_configuration_rejects_summary_and_duplicate_or_unbounded_campaigns(self):
        for entry in ({'status': 'pass'}, {'campaigns': []}, {'campaigns': ['one', 'one']},
                      {'campaigns': ['one', 'two', 'three']}):
            with self.subTest(entry=entry), self.assertRaises(ValueError):
                self.assess({'schema_version': 1, 'components': {'security': entry}})

    def test_management_reader_binds_original_api_inputs_and_server_sdk_cli_results(self):
        fixture = self.fixture(management_fixtures.ManagementEvidenceTests)
        for name in qualification.READER_FILES:
            fixture.manifest['source_files'][name] = self.manifest['source_files'][name]
        fixture.save()
        config = {'schema_version': 1, 'components': {'management': {'campaigns': list(map(str, fixture.paths))}}}
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['management']['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])
        self.assertTrue(result['publication_locked'])
        original = fixture.paths[1].with_name(fixture.paths[1].stem + '.artifacts') / 'contracts/operations.json'
        self.assertIn(str(original), result['input_sha256'])
        original.write_bytes(original.read_bytes() + b'\n')
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['management']['status'], 'invalid_evidence')
        self.assertTrue(result['publication_locked'])

    def test_management_configuration_rejects_summary_duplicate_and_unbounded_reports(self):
        for entry in ({'status': 'pass'}, {'campaigns': []}, {'campaigns': ['one', 'one']},
                      {'campaigns': ['one', 'two', 'three']}):
            with self.subTest(entry=entry), self.assertRaises(ValueError):
                self.assess({'schema_version': 1, 'components': {'management': entry}})

    def test_a2a_reader_requires_original_protocol_and_designated_effect_evidence(self):
        fixture = self.fixture(a2a_fixtures.A2AEvidenceTests)
        provider = fixture.provider()
        for name in qualification.READER_FILES:
            fixture.manifest['source_files'][name] = self.manifest['source_files'][name]
        provider.build['source_files'] = copy.deepcopy(fixture.manifest['source_files'])
        provider.build['release_identity'] = release.evidence_identity(fixture.manifest)
        provider.save_build()
        fixture.save()
        entry = {'campaigns': list(map(str, fixture.paths))}
        config = {'schema_version': 1, 'components': {'a2a': entry}}
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['a2a']['status'], 'incomplete')
        entry.update(build=str(provider.build_directory), reports=[str(provider.path)])
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['a2a']['status'], 'consistent_pending_review')
        self.assertTrue(result['publication_locked'])
        self.assertFalse(result['release_gate_passed'])
        original = fixture.paths[0].with_name(fixture.paths[0].stem + '.artifacts') / 'a2a-agent'
        self.assertIn(str(original), result['input_sha256'])
        original.write_bytes(original.read_bytes() + b'altered')
        self.assertEqual(self.assess(config, fixture.manifest)['components']['a2a']['status'], 'invalid_evidence')

    def test_a2a_configuration_rejects_summary_unpaired_effects_and_narrowed_reports(self):
        for entry in ({'status': 'pass'}, {'campaigns': []}, {'campaigns': ['one', 'one']},
                      {'campaigns': ['one', 'two', 'three']}, {'campaigns': ['one'], 'build': 'build'},
                      {'campaigns': ['one'], 'reports': ['report']},
                      {'campaigns': ['one'], 'build': 'build', 'reports': []},
                      {'campaigns': ['one'], 'build': 'build', 'reports': ['report', 'report']}):
            with self.subTest(entry=entry), self.assertRaises(ValueError):
                self.assess({'schema_version': 1, 'components': {'a2a': entry}})

    def test_build_matrix_reads_original_module_tag_and_frontend_results(self):
        fixture = self.fixture(build_fixtures.BuildEvidenceTests)
        for name in qualification.READER_FILES:
            fixture.manifest['source_files'][name] = self.manifest['source_files'][name]
        fixture.save()
        config = {'schema_version': 1, 'components': {'build_matrix': {'campaigns': list(map(str, fixture.paths))}}}
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['build_matrix']['status'], 'consistent_pending_review')
        self.assertEqual(len(result['components']['build_matrix']['reports']), 2)
        self.assertTrue(result['publication_locked'])
        self.assertFalse(result['release_gate_passed'])
        original = fixture.paths[1].with_name(fixture.paths[1].stem + '.artifacts') / 'dashboard-tests.json'
        self.assertIn(str(original), result['input_sha256'])
        original.write_bytes(original.read_bytes() + b'\n')
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['build_matrix']['status'], 'invalid_evidence')

    def test_accessibility_reader_keeps_manual_review_open_and_binds_original_records(self):
        fixture = self.fixture(accessibility_fixtures.AccessibilityEvidenceTests)
        original_hashes = [release.sha(path) for path in fixture.paths]
        for name in qualification.READER_FILES:
            fixture.manifest['source_files'][name] = self.manifest['source_files'][name]
        for path, report in zip(fixture.paths, fixture.reports):
            source = fixture.root / report['source_inventory']
            source.write_bytes(release.encoded(qualification.browser_evidence.source_inputs(fixture.manifest)))
            report['source_inventory_sha256'] = release.sha(source)
            report['release_identity'] = release.evidence_identity(fixture.manifest)
        fixture.fixture.save()
        renamed = dict(zip(original_hashes, (release.sha(path) for path in fixture.paths)))
        fixture.manual['release_identity'] = release.evidence_identity(fixture.manifest)
        fixture.manual['campaign_sha256'] = sorted(renamed.values())
        for finding in fixture.manual['scan_reviews']:
            finding['campaign_sha256'] = renamed[finding['campaign_sha256']]
        fixture.save_review()
        entry = {'campaigns': list(map(str, fixture.paths))}
        config = {'schema_version': 1, 'components': {'accessibility': entry}}
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['accessibility']['status'], 'incomplete')
        self.assertTrue(result['publication_locked'])
        entry['review'] = str(fixture.manual_path)
        result = self.assess(config, fixture.manifest)
        self.assertEqual(result['components']['accessibility']['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])
        note = fixture.root / 'manual-notes.txt'
        self.assertIn(str(note), result['input_sha256'])
        note.write_bytes(b'changed review evidence')
        self.assertEqual(self.assess(config, fixture.manifest)['components']['accessibility']['status'], 'invalid_evidence')

    def test_accessibility_configuration_rejects_summaries_and_unbounded_inputs(self):
        for entry in ({'status': 'pass'}, {'campaigns': []}, {'campaigns': ['one', 'one']},
                      {'campaigns': ['one', 'two', 'three', 'four', 'five']},
                      {'campaigns': ['one'], 'review': '../outside.json'}):
            with self.subTest(entry=entry), self.assertRaises(ValueError):
                self.assess({'schema_version': 1, 'components': {'accessibility': entry}})

    def test_build_matrix_inputs_are_bounded_and_never_accept_summary_substitutes(self):
        for entry in ({'status': 'pass'}, {'campaigns': []}, {'campaigns': ['one', 'one']},
                      {'campaigns': ['one', 'two', 'three']}):
            with self.subTest(entry=entry), self.assertRaises(ValueError):
                self.assess({'schema_version': 1, 'components': {'build_matrix': entry}})

    def test_packaging_without_final_assembly_retains_binary_hashes_but_is_incomplete(self):
        fixture = self.fixture(packaging_fixtures.PackagingEvidenceTests)
        inputs = qualification.Inputs()
        paths = {'packaging': {'directory': fixture.out}}
        result = qualification.inspect_component('packaging', fixture.manifest, paths['packaging'], paths, inputs)
        self.assertEqual(result['status'], 'incomplete')
        self.assertTrue(any('private OCI' in item for item in result['remaining']))
        self.assertEqual(len(result['reports']), 34)
        self.assertEqual(set(result['binary_sha256']), set(release.RECIPE['targets']))
        self.assertTrue(inputs.files)
        self.assertFalse(result['release_gate_passed'])
        inputs.unchanged()

    def test_packaging_reader_binds_final_subjects_to_every_retained_oci_byte(self):
        fixture = self.fixture(packaging_fixtures.PackagingEvidenceTests)
        assembly = self.root / 'assembly'
        record = oci_assembly.assemble(fixture.out / 'RELEASE.json', fixture.out, assembly)
        (fixture.out / 'OCI.json').write_bytes(release.encoded(record['subjects']))
        inputs = qualification.Inputs()
        entry = {'directory': fixture.out, 'assembly': assembly}
        paths = {'packaging': entry}
        result = qualification.inspect_component('packaging', fixture.manifest, entry, paths, inputs)
        self.assertEqual(result['status'], 'incomplete')
        self.assertTrue(any('original scanner' in item for item in result['remaining']))
        self.assertEqual(result['oci_subjects'], record['subjects'])
        self.assertIn(str(assembly / 'layout/index.json'), inputs.files)
        self.assertFalse(result['release_gate_passed'])
        subjects = dict(record['subjects'], digest='sha256:' + 'f' * 64)
        (fixture.out / 'OCI.json').write_bytes(release.encoded(subjects))
        with self.assertRaisesRegex(ValueError, 'final image subjects'):
            qualification.inspect_component('packaging', fixture.manifest, entry, paths, qualification.Inputs())

    def test_private_assembly_artifacts_are_kept_separate_from_final_distribution(self):
        fixture = self.fixture(packaging_fixtures.PackagingEvidenceTests)
        artifacts, assets = self.artifact_fixture()
        original = artifacts / 'container-evidence-amd64'
        original.mkdir()
        for path in fixture.out.glob('image-repro-*'):
            shutil.copyfile(path, original / path.name)
        assembly = artifacts / 'private-oci-assembly'
        record = oci_assembly.assemble(fixture.out / 'RELEASE.json', original, assembly)
        # The fixture asset helper uses the same manifest but a different JSON
        # encoding; preserve the actual assembly metadata during this test.
        shutil.copyfile(assembly / 'RELEASE.json', assets / 'RELEASE.json')
        chart = artifacts / 'private-chart-package'
        chart.mkdir()
        (chart / 'cpra-chart-0.1.0.tgz').write_bytes(b'fixture chart package')
        (chart / 'chart-package.json').write_bytes(b'fixture record')
        inventories = artifacts / 'private-image-sboms'
        inventories.mkdir()
        for arch in ('amd64', 'arm64'):
            (inventories / ('cpra-image-linux-' + arch + '.spdx.json')).write_bytes(b'fixture inventory')
            (inventories / (arch + '.syft.json')).write_bytes(b'fixture original scanner output')
        (inventories / 'image-sbom.json').write_bytes(b'fixture record')
        stage = self.root / 'stage'
        config = qualification.stage_artifacts(artifacts, stage, qualification.Inputs())
        self.assertEqual(config['components']['packaging']['assembly'], str(assembly))
        self.assertEqual(json.loads((stage / 'distribution/OCI.json').read_bytes()), record['subjects'])
        self.assertTrue((stage / 'distribution/cpra-chart-0.1.0.tgz').exists())
        self.assertFalse((stage / 'distribution/layout').exists())
        self.assertFalse((stage / 'distribution/oci-assembly.json').exists())
        self.assertFalse((stage / 'distribution/chart-package.json').exists())
        self.assertEqual(config['components']['packaging']['image_sboms'], str(inventories))
        self.assertTrue((stage / 'distribution/cpra-image-linux-arm64.spdx.json').exists())
        self.assertFalse((stage / 'distribution/arm64.syft.json').exists())
        self.assertFalse((stage / 'distribution/image-sbom.json').exists())

    def test_packaging_links_final_inventory_to_original_scan_bytes(self):
        fixture = self.fixture(image_sbom_fixtures.ImageSBOMTests)
        distribution = self.root / 'distribution'; distribution.mkdir()
        (distribution / 'RELEASE.json').write_bytes(fixture.metadata.read_bytes())
        (distribution / 'OCI.json').write_bytes(release.encoded(fixture.subjects))
        for path in fixture.reports.iterdir(): shutil.copyfile(path, distribution / path.name)
        for arch in ('amd64', 'arm64'):
            path = fixture.out / ('cpra-image-linux-' + arch + '.spdx.json')
            shutil.copyfile(path, distribution / path.name)
        entry = {'directory': distribution, 'assembly': fixture.assembly, 'image_sboms': fixture.out}
        # Isolate this reader's byte linkage; these fixtures do not qualify packages.
        with patch.object(qualification.publish, 'evidence', return_value={
                'status': 'consistent_pending_review', 'remaining': [], 'release_gate_passed': False,
                'release_identity': release.evidence_identity(fixture.manifest)}):
            result = qualification.inspect_component('packaging', fixture.manifest, entry,
                                                       {'packaging': entry}, qualification.Inputs())
            self.assertEqual(set(result['image_sbom_sha256']), {'amd64', 'arm64'})
            (distribution / 'cpra-image-linux-arm64.spdx.json').write_bytes(b'replaced final inventory')
            with self.assertRaisesRegex(ValueError, 'retained input changed'):
                qualification.inspect_component('packaging', fixture.manifest, entry,
                                                  {'packaging': entry}, qualification.Inputs())

    def test_private_assembly_workflow_does_not_have_publication_credentials(self):
        import yaml
        jobs = yaml.safe_load((release.ROOT / '.github/workflows/release.yml').read_text())['jobs']
        job = jobs['private-assembly']
        self.assertNotIn('permissions', job)
        text = '\n'.join(step.get('run', '') for step in job['steps'])
        for public in ('docker push', 'helm push', 'gh release', 'cosign sign', 'git push'):
            self.assertNotIn(public, text)
        self.assertIn('private-assembly', jobs['private-assessment']['needs'])
        self.assertIn('image_sbom.py --verify', text)
        self.assertTrue(any(step.get('with', {}).get('name') == 'private-image-sboms' for step in job['steps']))

    def test_missing_evidence_and_invalid_evidence_remain_distinct(self):
        config = {'schema_version': 1, 'components': {'sdk': {
            'dependencies': 'missing', 'consumers': ['one.json', 'two.json']}}}
        result = self.assess(config)
        self.assertEqual(result['components']['sdk']['status'], 'missing')
        # A file supplied where a directory is required is invalid, not missing.
        (self.root / 'missing').write_bytes(b'not a directory')
        result = self.assess(config)
        self.assertEqual(result['components']['sdk']['status'], 'invalid_evidence')
        self.assertEqual(result['status'], 'invalid_evidence')

    def test_reader_cannot_turn_its_result_into_publication_authorization(self):
        entry = {'dependencies': self.root, 'consumers': [self.root / 'one', self.root / 'two']}
        for path in entry['consumers']:
            path.write_bytes(b'fixture')
        for change in ({'release_gate_passed': True}, {'status': 'pass'},
                       {'release_identity': {'commit': 'b' * 40}}):
            with self.subTest(change=change):
                result = {'status': 'consistent_pending_review', 'release_gate_passed': False,
                          'release_identity': release.evidence_identity(self.manifest), 'input_sha256': {}}
                result.update(change)
                with patch.object(qualification.sdk_evidence, 'verify_sdk_consumers', return_value=result):
                    with self.assertRaises(ValueError):
                        qualification.inspect_component('sdk', self.manifest, entry, {}, qualification.Inputs())

    def test_corrupt_retained_archive_is_recorded_as_invalid_evidence(self):
        paths = [self.root / 'one.json', self.root / 'two.json']
        for path in paths:
            path.write_bytes(b'fixture')
        config = {'schema_version': 1, 'components': {'sdk': {
            'dependencies': str(self.root), 'consumers': list(map(str, paths))}}}
        with patch.object(qualification.sdk_evidence, 'verify_sdk_consumers', side_effect=zipfile.BadZipFile('corrupt archive')):
            result = self.assess(config)
        self.assertEqual(result['status'], 'invalid_evidence')
        self.assertEqual(result['components']['sdk']['status'], 'invalid_evidence')
        self.assertIn('corrupt archive', result['components']['sdk']['reason'])
        self.assertFalse(result['release_gate_passed'])

    def test_changed_remembered_files_and_oversized_inputs_cannot_pass(self):
        path = self.root / 'evidence.json'
        path.write_bytes(b'{"status":"pass"}')
        inputs = qualification.Inputs()
        inputs.remember(path)
        path.write_bytes(b'{"status":"fail"}')
        with self.assertRaisesRegex(ValueError, 'changed'):
            inputs.unchanged()
        with patch.object(qualification, 'MAX_INPUT_BYTES', 4), self.assertRaisesRegex(ValueError, 'exceeds'):
            qualification.Inputs().remember(path)
        with patch.object(qualification, 'MAX_TOTAL_BYTES', 4), self.assertRaisesRegex(ValueError, '64 GiB'):
            qualification.Inputs().remember(path)
        with patch.object(qualification, 'MAX_INPUT_FILES', 0), self.assertRaisesRegex(ValueError, 'too many'):
            qualification.Inputs().remember(path)

    def test_links_and_nonregular_files_fail_without_being_read(self):
        target = self.root / 'target'; target.write_bytes(b'fixture')
        link = self.root / 'link'; link.symlink_to(target)
        with self.assertRaisesRegex(ValueError, 'link'):
            qualification.Inputs().remember(link)
        fifo = self.root / 'fifo'; os.mkfifo(fifo)
        with self.assertRaisesRegex(ValueError, 'regular'):
            qualification.Inputs().remember(fifo)
        with self.assertRaisesRegex(ValueError, 'link|regular'):
            qualification.Inputs().directory(self.root)

    def run_cli(self, out, config=None, manifest=None, options=()):
        release_path, config_path = self.root / 'RELEASE.json', self.root / 'qualification.json'
        release_path.write_bytes(release.encoded(manifest or self.manifest))
        config_path.write_bytes(release.encoded(config or self.config))
        with patch.object(sys, 'argv', ['qualification.py', '--release', str(release_path),
                                      '--config', str(config_path), '--out', str(out), *options]), \
                patch('sys.stdout', new_callable=io.StringIO):
            return qualification.main()

    def test_cli_decision_rechecks_signed_inputs_and_retains_separate_publication_boundary(self):
        fixture = self.decision_fixture()
        out = self.root / 'decision.json'
        options = ('--decision', '--review-policy', str(fixture.trust_path),
                   '--review-policy-sha256', release.sha(fixture.trust_path))
        self.assertEqual(self.run_cli(out, options=options), 0)
        result = json.loads(out.read_bytes())
        self.assertTrue(result['prepublication_gate_passed'])
        self.assertFalse(result['release_gate_passed'])
        self.assertTrue(result['publication_locked'])
        self.assertFalse(result['public_actions_performed'])
        self.assertEqual(out.stat().st_mode & 0o777, 0o600)
        fixture.original.write_bytes(b'changed original')
        self.assertEqual(self.run_cli(self.root / 'rejection.json', options=options), 1)
        rejection = json.loads((self.root / 'rejection.json').read_bytes())
        self.assertFalse(rejection['release_gate_passed'])
        self.assertTrue(rejection['publication_locked'])
        self.assertFalse(rejection['prepublication_gate_passed'])

    def test_cli_rejects_incomplete_decision_options_without_creating_output(self):
        out = self.root / 'decision.json'
        for options in (('--decision',), ('--review-policy', '/missing-policy'),
                        ('--review-policy-sha256', '0' * 64),
                        ('--decision', '--review-policy', '/missing-policy')):
            with self.subTest(options=options), patch('sys.stderr', new_callable=io.StringIO), \
                    self.assertRaises(SystemExit) as stopped:
                self.run_cli(out, options=options)
            self.assertEqual(stopped.exception.code, 2)
            self.assertFalse(out.exists())

    def test_cli_cannot_overwrite_pinned_operator_trust(self):
        fixture = self.decision_fixture()
        before = fixture.trust_path.read_bytes()
        with self.assertRaisesRegex(ValueError, 'output cannot replace the operator trust policy'):
            self.run_cli(fixture.trust_path, options=(
                '--decision', '--review-policy', str(fixture.trust_path),
                '--review-policy-sha256', release.sha(fixture.trust_path)))
        self.assertEqual(fixture.trust_path.read_bytes(), before)

    def test_cli_records_incomplete_assessment_and_refuses_overwrite(self):
        out = self.root / 'assessment.json'
        self.assertEqual(self.run_cli(out), 2)
        before = out.read_bytes()
        self.assertFalse(json.loads(before)['release_gate_passed'])
        self.assertEqual(out.stat().st_mode & 0o777, 0o600)
        with self.assertRaises(FileExistsError):
            self.run_cli(out)
        self.assertEqual(out.read_bytes(), before)

    def test_cli_preserves_invalid_candidate_rejection(self):
        manifest = copy.deepcopy(self.manifest); manifest['dirty'] = True
        out = self.root / 'rejection.json'
        self.assertEqual(self.run_cli(out, manifest=manifest), 1)
        result = json.loads(out.read_bytes())
        self.assertEqual(result['status'], 'invalid_evidence')
        self.assertFalse(result['release_gate_passed'])

    def test_cli_never_writes_output_into_a_retained_input_directory(self):
        inputs = self.root / 'packaging'; inputs.mkdir()
        out = inputs / 'assessment.json'
        config = {'schema_version': 1, 'components': {'packaging': {'directory': str(inputs)}}}
        with patch('sys.stderr', new_callable=io.StringIO), self.assertRaises(SystemExit) as stopped:
            self.run_cli(out, config=config)
        self.assertEqual(stopped.exception.code, 2)
        self.assertFalse(out.exists())

    def test_benchmark_commit_does_not_replace_source_or_final_binary_binding(self):
        builds = self.root / 'builds'; builds.mkdir()
        source = {name: {'kind': 'file', 'mode': int(record['mode'][-3:], 8), 'sha256': record['sha256']}
                  for name, record in self.manifest['source_files'].items()}
        metadata = {'recipe': self.manifest['recipe'], 'tags': self.manifest['recipe']['build_tags'],
                    'release': self.manifest, 'release_identity': release.evidence_identity(self.manifest),
                    'toolchain': {'target': 'linux_amd64'}, 'builds': [
                        {'name': 'candidate', 'source_date_epoch': self.manifest['source_date_epoch'], 'sha256': 'a' * 64}]}
        source_path, metadata_path = builds / 'candidate-source.json', builds / 'builds.json'
        source_path.write_bytes(release.encoded(source)); metadata_path.write_bytes(release.encoded(metadata))
        entry = {'builds': builds, 'comparison': self.root / 'comparison', 'faults': self.root / 'faults'}
        with patch.object(qualification.publish, 'archive_programs', return_value={'cpra': 'a' * 64}):
            qualification.benchmark_source(self.manifest, entry, self.root, qualification.Inputs())
            source['main.go']['sha256'] = 'c' * 64
            source_path.write_bytes(release.encoded(source))
            with self.assertRaisesRegex(ValueError, 'source file differs'):
                qualification.benchmark_source(self.manifest, entry, self.root, qualification.Inputs())
        source['main.go']['sha256'] = self.manifest['source_files']['main.go']['sha256']
        source_path.write_bytes(release.encoded(source))
        with patch.object(qualification.publish, 'archive_programs', return_value={'cpra': 'b' * 64}):
            with self.assertRaisesRegex(ValueError, 'controller bytes differ'):
                qualification.benchmark_source(self.manifest, entry, self.root, qualification.Inputs())

    def test_benchmark_reader_is_called_without_any_endurance_input(self):
        entry = {'builds': self.root / 'builds', 'comparison': self.root / 'comparison', 'faults': self.root / 'faults'}
        paths = {'benchmark': entry, 'packaging': {'directory': self.root / 'artifacts'}}
        for path in [*entry.values(), paths['packaging']['directory']]:
            path.mkdir()
        result = {'status': 'consistent_pending_review', 'release_gate_passed': False,
                  'candidate_commit': self.manifest['commit'], 'inputs': {'builds': {}, 'compare': {}, 'faults': {}}}
        with patch.object(qualification, 'benchmark_source'), \
                patch.object(qualification.benchmark_evidence, 'verify', return_value=result) as reader:
            qualification.inspect_component('benchmark', self.manifest, entry, paths, qualification.Inputs())
        reader.assert_called_once_with(entry['builds'], entry['comparison'], entry['faults'], commit=self.manifest['commit'])

    def artifact_fixture(self):
        artifacts = self.root / 'artifacts'; artifacts.mkdir()
        assets = artifacts / 'release-assets'; assets.mkdir()
        (assets / 'RELEASE.json').write_bytes(release.encoded(self.manifest))
        (assets / 'cpra-v0.1.0-rc.1-linux-amd64.tar.gz').write_bytes(b'archive fixture, not an executable release')
        return artifacts, assets

    def test_upgrade_artifacts_keep_original_logs_and_previous_archives_separate(self):
        artifacts, _ = self.artifact_fixture()
        fixture = self.fixture(storage_fixtures.StorageEvidenceTests)
        job = artifacts / 'storage-upgrade-linux_amd64'; job.mkdir()
        previous = job / 'previous'; previous.mkdir()
        for path in fixture.previous_distribution.iterdir():
            shutil.copyfile(path, previous / path.name)
        campaign = job / 'upgrade-linux_amd64.json'
        shutil.copyfile(fixture.paths[0], campaign)
        shutil.copytree(fixture.paths[0].with_name(fixture.paths[0].name + '.inputs'),
                        campaign.with_name(campaign.name + '.inputs'))
        stage = self.root / 'stage'
        config = qualification.stage_artifacts(artifacts, stage, qualification.Inputs())
        entry = config['components']['storage_upgrade']
        self.assertEqual(entry['campaigns'], [str(campaign)])
        self.assertEqual(Path(entry['previous_release']).read_bytes(), fixture.previous_release.read_bytes())
        self.assertEqual(set(path.name for path in Path(entry['previous_distribution']).iterdir()),
                         set(path.name for path in previous.iterdir()))
        self.assertFalse((stage / 'distribution' / 'upgrade-linux_amd64.json').exists())
        self.assertFalse((stage / 'distribution' / 'cpra-v0.0.9-linux-amd64.tar.gz').exists())

    def test_job_artifacts_keep_sdk_payloads_separate_and_copy_distribution_bytes(self):
        artifacts, assets = self.artifact_fixture()
        reports = artifacts / 'native-evidence-linux_amd64'; reports.mkdir()
        report = reports / 'native-linux_amd64.json'; report.write_bytes(b'{"status":"pass"}')
        (reports / 'unselected.log').write_bytes(b'not a required packaging input')
        summary = artifacts / 'sdk-consumer-evidence'; summary.mkdir()
        (summary / 'sdk-consumer-evidence.json').write_bytes(b'{"status":"pass"}')
        inputs = qualification.Inputs()
        stage = self.root / 'stage'
        config = qualification.stage_artifacts(artifacts, stage, inputs)
        self.assertEqual((stage / 'distribution' / report.name).read_bytes(), report.read_bytes())
        self.assertFalse((stage / 'distribution' / 'unselected.log').exists())
        self.assertFalse((stage / 'distribution' / 'sdk-consumer-evidence.json').exists())
        self.assertIn('sdk-consumer-go1.26.0/consumer-go1.26.0.json', config['components']['sdk']['consumers'][0])
        self.assertIn('worker-evidence-go1.26.0/worker-go1.26.0.json',
                      config['components']['external_worker']['campaigns'][0])
        self.assertIn('build-evidence-go1.26.0/build-go1.26.0.json',
                      config['components']['build_matrix']['campaigns'][0])
        self.assertEqual(config['components']['management'], config['components']['build_matrix'])
        self.assertEqual(config['components']['a2a'], config['components']['build_matrix'])
        self.assertEqual(len(config['components']['browser']['campaigns']), 4)
        self.assertEqual(config['components']['accessibility']['campaigns'], config['components']['browser']['campaigns'])
        self.assertIn('browser-evidence-go1.26.0-default/browser-go1.26.0-default.json',
                      config['components']['browser']['campaigns'][0])
        self.assertFalse((stage / 'distribution' / 'worker-go1.26.0.json').exists())
        self.assertEqual((stage / 'distribution' / 'RELEASE.json').read_bytes(), (assets / 'RELEASE.json').read_bytes())
        inputs.unchanged()

    def test_conflicting_artifacts_never_overwrite_already_copied_evidence(self):
        artifacts, assets = self.artifact_fixture()
        filename = 'native-linux_amd64.json'
        first = artifacts / 'native-evidence-one'; first.mkdir()
        second = artifacts / 'native-evidence-two'; second.mkdir()
        (first / filename).write_bytes(b'first evidence')
        (second / filename).write_bytes(b'contradictory evidence')
        stage = self.root / 'stage'
        with self.assertRaisesRegex(ValueError, 'conflicting'):
            qualification.stage_artifacts(artifacts, stage, qualification.Inputs())
        self.assertEqual((stage / 'distribution' / filename).read_bytes(), b'first evidence')
        self.assertEqual((second / filename).read_bytes(), b'contradictory evidence')

    def test_identical_duplicate_artifacts_are_lossless_but_aliases_are_rejected(self):
        artifacts, assets = self.artifact_fixture()
        for name in ('native-evidence-one', 'native-evidence-two'):
            directory = artifacts / name; directory.mkdir()
            (directory / 'native-linux_amd64.json').write_bytes(b'same retained evidence')
        qualification.stage_artifacts(artifacts, self.root / 'stage', qualification.Inputs())
        alias = assets / 'release.json'; alias.write_bytes((assets / 'RELEASE.json').read_bytes())
        with self.assertRaisesRegex(ValueError, 'conflicting'):
            qualification.stage_artifacts(artifacts, self.root / 'alias-stage', qualification.Inputs())

    def test_artifact_staging_cannot_reuse_or_write_into_raw_input(self):
        artifacts, assets = self.artifact_fixture()
        with self.assertRaisesRegex(ValueError, 'separate'):
            qualification.stage_artifacts(artifacts, artifacts / 'stage', qualification.Inputs())
        stage = self.root / 'stage'
        qualification.stage_artifacts(artifacts, stage, qualification.Inputs())
        with self.assertRaises(FileExistsError):
            qualification.stage_artifacts(artifacts, stage, qualification.Inputs())
        link = artifacts / 'linked'; link.symlink_to(assets, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, 'link'):
            qualification.stage_artifacts(artifacts, self.root / 'link-stage', qualification.Inputs())

    def test_staging_alias_is_rejected_before_creating_parent_directories(self):
        artifacts, assets = self.artifact_fixture()
        parent = self.root / 'must-not-be-created'
        alias = parent / '..' / 'artifacts' / 'stage'
        with self.assertRaisesRegex(ValueError, 'alias'):
            qualification.stage_artifacts(artifacts, alias, qualification.Inputs())
        self.assertFalse(parent.exists())
        self.assertFalse((artifacts / 'stage').exists())

    def test_cli_artifact_collection_reports_missing_requirements_without_endurance(self):
        artifacts, assets = self.artifact_fixture()
        stage, out = self.root / 'stage', self.root / 'assessment.json'
        with patch.object(sys, 'argv', ['qualification.py', '--release', str(assets / 'RELEASE.json'),
                                      '--artifacts', str(artifacts), '--stage', str(stage), '--out', str(out)]), \
                patch('sys.stdout', new_callable=io.StringIO):
            self.assertEqual(qualification.main(), 2)
        report = json.loads(out.read_bytes())
        self.assertEqual(report['components']['packaging']['status'], 'missing')
        self.assertEqual(report['components']['sdk']['status'], 'missing')
        self.assertFalse(report['endurance']['required'])
        self.assertFalse(report['release_gate_passed'])

    def test_selected_release_metadata_cannot_be_replaced_by_another_job(self):
        fixture = self.fixture(packaging_fixtures.PackagingEvidenceTests)
        metadata = copy.deepcopy(fixture.manifest)
        metadata['version'] = 'v0.2.0'
        (fixture.out / 'RELEASE.json').write_bytes(release.encoded(metadata))
        paths = {'packaging': {'directory': fixture.out}}
        with self.assertRaisesRegex(ValueError, 'distribution metadata differs'):
            qualification.inspect_component('packaging', fixture.manifest, paths['packaging'], paths, qualification.Inputs())


if __name__ == '__main__':
    unittest.main()
