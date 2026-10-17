"""Protected publisher wiring and exact index tags; remote accounts are not used."""
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import yaml

import publish
import release
from test_evidence import manifest_fixture
from test_registry import fixture, ReadbackFixture, UploadFixture


def workflow_run(manifest, number=42):
    return {'id': number, 'run_attempt': 1, 'workflow_id': 17, 'head_sha': manifest['commit'],
            'head_branch': 'main', 'path': '.github/workflows/release.yml',
            'event': 'workflow_dispatch', 'status': 'completed',
            'repository': {'id': 1, 'full_name': publish.REPOSITORY},
            'head_repository': {'id': 1, 'full_name': publish.REPOSITORY}}


class ImageTagTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.manifest, self.reports, self.assembly, self.subjects = fixture(self.root)
        self.server = ReadbackFixture()
        self.addCleanup(self.server.close)
        self.remote = UploadFixture(self.server)
        self.progress = self.root / 'image-tag.jsonl'
        publish.transfer_images(self.assembly, self.reports, self.manifest, self.remote,
                                '/unused/oras', self.root/'images.jsonl', self.root)
        self.remote.uploads.clear()

    def tag(self, **kwargs):
        return publish.tag_images(self.assembly, self.reports, self.manifest,
                                  self.remote, '/unused/oras', self.progress, self.root, **kwargs)

    def index(self):
        return self.server.objects[('manifest', self.subjects['digest'])]

    def test_version_has_the_original_index_bytes_and_resume_does_not_write(self):
        result = self.tag()
        self.assertFalse(result['reused'])
        self.assertFalse(result['release_gate_passed'])
        self.assertEqual(self.server.objects[('manifest', self.manifest['version'])], self.index())
        self.assertEqual(len(self.remote.uploads), 1)
        self.server.requests.clear()
        self.assertTrue(self.tag()['reused'])
        self.assertEqual(len(self.remote.uploads), 1)
        self.assertTrue(any(self.subjects['digest'] in row for row in self.server.requests))
        self.assertEqual(self.progress.stat().st_mode & 0o777, 0o600)

    def test_lost_tag_reply_resumes_original_version_without_replacement(self):
        self.remote.fail_after = True
        with self.assertRaisesRegex(RuntimeError, 'lost upload reply'):
            self.tag()
        self.assertIn('started', self.progress.read_text())
        self.assertNotIn('verified', self.progress.read_text())
        self.assertTrue(self.tag()['reused'])
        self.assertEqual(len(self.remote.uploads), 1)

    def test_confirmed_absence_after_started_marker_can_finish(self):
        original = self.remote.upload
        def reject(*args, **kwargs):
            raise RuntimeError('fixture before external write')
        with patch.object(self.remote, 'upload', side_effect=reject), self.assertRaises(RuntimeError):
            self.tag()
        self.assertFalse(self.tag()['reused'])
        self.assertEqual(len(self.remote.uploads), 1)
        self.assertEqual(self.remote.upload, original)

    def test_existing_matching_version_without_original_intent_is_rejected(self):
        self.server.objects[('manifest', self.manifest['version'])] = self.index()
        with self.assertRaisesRegex(ValueError, 'without original'):
            self.tag()
        self.assertEqual(self.remote.uploads, [])

    def test_missing_verified_tag_and_changed_remote_bytes_never_get_replaced(self):
        self.tag()
        key = ('manifest', self.manifest['version'])
        row, body = self.server.objects.pop(key)
        with self.assertRaisesRegex(ValueError, 'missing; replacement'):
            self.tag()
        self.server.objects[key] = (row, b'x' * len(body))
        with self.assertRaisesRegex(ValueError, 'bytes differ'):
            self.tag()
        self.assertEqual(len(self.remote.uploads), 1)

    def test_missing_dependency_and_uncertain_http_stop_before_new_tag(self):
        self.server.objects.pop(('manifest', self.subjects['platforms']['arm64']))
        with self.assertRaisesRegex(ValueError, 'complete verified'):
            self.tag()
        self.assertEqual(self.remote.uploads, [])
        self.assertNotIn('started', self.progress.read_text())
        self.server.failure = (500, b'{}', {})
        with self.assertRaises(ValueError):
            self.tag()
        self.assertEqual(self.remote.uploads, [])

    def test_preflight_checks_name_before_transfer_without_publishing(self):
        self.server.objects.clear()
        self.server.requests.clear()
        self.assertTrue(self.tag(preflight_only=True)['available'])
        self.assertEqual(self.remote.uploads, [])
        self.assertNotIn('started', self.progress.read_text())
        self.assertEqual(len(self.server.requests), 1)

    def test_invalid_progress_and_changed_input_stop_before_tag_write(self):
        self.tag()
        data = self.progress.read_bytes().splitlines(keepends=True)
        self.progress.write_bytes(b''.join([data[0], data[1], data[1], data[2]]))
        with self.assertRaisesRegex(ValueError, 'out of order'):
            self.tag()
        self.progress.write_bytes(b''.join(data))
        (self.assembly/'RELEASE.json').write_bytes(b'changed')
        with self.assertRaises(ValueError):
            self.tag()
        self.assertEqual(len(self.remote.uploads), 1)

    def test_inputs_cannot_change_during_readback_before_tag_upload(self):
        verify = self.remote.verify
        def mutate(row, **kwargs):
            result = verify(row, **kwargs)
            (self.assembly/'OCI.json').write_bytes(b'changed')
            return result
        with patch.object(self.remote, 'verify', side_effect=mutate), self.assertRaises(ValueError):
            self.tag()
        self.assertEqual(self.remote.uploads, [])


class ProgressRestoreTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.manifest = manifest_fixture()
        self.previous = workflow_run(self.manifest)
        self.current = workflow_run(self.manifest, 43)
        self.source = self.root/'artifact'
        self.source.mkdir()
        self.owner = publish.progress_owner(self.manifest, self.previous)
        (self.source/'publication.json').write_bytes(publish.release_bytes(self.owner))
        self.data = publish.release_bytes({'schema_version': 1,
                                          'identity': {'release_identity': release.evidence_identity(self.manifest)}})
        # Retain even a partial final record unchanged; TransferProgress repairs
        # the last incomplete line under its own lock during actual readback.
        self.data += b'{"partial"'
        (self.source/'images.jsonl').write_bytes(self.data)
        self.destination = self.root/'progress'

    def restore(self):
        return publish.prepare_progress(self.manifest, self.current, self.destination,
                                        incoming=self.source, previous_run=self.previous)

    def test_restore_preserves_original_journals_and_changes_only_run_owner(self):
        result = self.restore()
        self.assertEqual(result['run_id'], 43)
        self.assertEqual((self.destination/'images.jsonl').read_bytes(), self.data)
        self.assertEqual((self.source/'publication.json').read_bytes(), publish.release_bytes(self.owner))
        self.assertEqual(self.destination.stat().st_mode & 0o777, 0o700)
        self.assertTrue(all(path.stat().st_mode & 0o777 == 0o600 for path in self.destination.iterdir()))
        # Another interrupted run can reuse the same journal without changing
        # the immutable release identity or remote ownership assumptions.
        current = workflow_run(self.manifest, 44)
        again = self.root/'again'
        publish.prepare_progress(self.manifest, current, again,
                                 incoming=self.destination, previous_run=self.current)
        self.assertEqual((again/'images.jsonl').read_bytes(), self.data)

    def test_new_progress_needs_no_network_or_credentials(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(subprocess, 'run', side_effect=AssertionError('remote')):
            publish.prepare_progress(self.manifest, self.current, self.destination)
        self.assertEqual({path.name for path in self.destination.iterdir()}, {'publication.json'})

    def test_chart_progress_is_preserved_for_remote_identity_verification(self):
        (self.source/'chart.jsonl').write_bytes(self.data)
        self.restore()
        self.assertEqual((self.destination/'chart.jsonl').read_bytes(), self.data)
        self.assertEqual((self.destination/'chart.jsonl').stat().st_mode & 0o777, 0o600)

    def test_prior_run_must_match_source_repository_workflow_and_event(self):
        changes = (
            lambda r: r.update(status='in_progress'), lambda r: r.update(head_sha='b'*40),
            lambda r: r.update(head_branch='unreviewed'), lambda r: r.update(event='pull_request'),
            lambda r: r.update(path='.github/workflows/ci.yml'), lambda r: r.update(workflow_id=18),
            lambda r: r.update(id=43), lambda r: r['head_repository'].update(id=2),
            lambda r: r['repository'].update(full_name='other/repository'),
        )
        for change in changes:
            original = copy.deepcopy(self.previous)
            change(self.previous)
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.restore()
            self.previous = original
            self.assertFalse(self.destination.exists())

    def test_source_or_owner_changes_fail_before_destination_creation(self):
        for name in ('publication.json', 'images.jsonl'):
            path = self.source/name
            original = path.read_bytes()
            value = json.loads(original.split(b'\n')[0])
            if name == 'publication.json':
                value['run_id'] = 99
            else:
                value['identity']['release_identity']['version'] = 'v9.0.0'
            path.write_bytes(publish.release_bytes(value))
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, 'differs|different'):
                self.restore()
            self.assertFalse(self.destination.exists())
            path.write_bytes(original)

    def test_links_extra_files_and_quotas_fail_without_copy(self):
        path = self.source/'images.jsonl'
        for kind in ('symlink', 'hardlink', 'directory', 'oversize', 'unexpected'):
            path.unlink()
            if kind == 'symlink':
                path.symlink_to(self.source/'publication.json')
            elif kind == 'hardlink':
                os.link(self.source/'publication.json', path)
            elif kind == 'directory':
                path.mkdir()
            elif kind == 'oversize':
                with path.open('wb') as stream:
                    stream.truncate(publish.PROGRESS_LIMIT + 1)
            else:
                path.write_bytes(self.data)
                (self.source/'credentials.txt').write_text('fixture value')
            with self.subTest(kind=kind), self.assertRaisesRegex(ValueError, 'linked|nonregular|oversized|unexpected'):
                self.restore()
            self.assertFalse(self.destination.exists())
            if kind == 'directory':
                path.rmdir()
                path.write_bytes(self.data)
            if kind == 'unexpected':
                (self.source/'credentials.txt').unlink()

    def test_existing_output_is_never_overwritten_and_parent_must_be_private(self):
        self.destination.mkdir()
        (self.destination/'owned').write_text('keep')
        with self.assertRaisesRegex(ValueError, 'already exists'):
            self.restore()
        self.assertEqual((self.destination/'owned').read_text(), 'keep')
        self.destination = self.root/'second'
        self.root.chmod(0o755)
        with self.assertRaisesRegex(ValueError, 'private owned'):
            self.restore()
        self.root.chmod(0o700)

    def test_invalid_run_and_unpaired_input_are_controlled_errors(self):
        for run in (None, [], {}, {'id': True}):
            with self.subTest(run=run), self.assertRaises(ValueError):
                publish.prepare_progress(self.manifest, run, self.destination)
        with self.assertRaisesRegex(ValueError, 'original progress'):
            publish.prepare_progress(self.manifest, self.current, self.destination, previous_run=self.previous)
        with self.assertRaisesRegex(ValueError, 'authenticated previous'):
            publish.prepare_progress(self.manifest, self.current, self.destination, incoming=self.source)

    def test_disk_failure_removes_only_incomplete_destination(self):
        original = (self.source/'images.jsonl').read_bytes()
        with patch.object(publish.os, 'fsync', side_effect=OSError('fixture write failure')), self.assertRaises(OSError):
            self.restore()
        self.assertFalse(self.destination.exists())
        self.assertEqual((self.source/'images.jsonl').read_bytes(), original)


class PublisherBoundaryTests(unittest.TestCase):
    def workflow(self):
        return yaml.safe_load((release.ROOT/'.github/workflows/release.yml').read_text())

    def test_workflow_uses_original_assembly_module_inputs_and_protected_progress(self):
        workflow = self.workflow()
        job = workflow['jobs']['publish']
        self.assertEqual(job['environment'], 'release')
        self.assertIn('private-assessment', job['needs'])
        self.assertEqual(job['permissions']['actions'], 'read')
        body = '\n'.join(step.get('run', '') for step in job['steps'])
        for forbidden in ('docker load', 'docker push', 'docker tag', 'imagetools', 'scripts/release/chart.py'):
            self.assertNotIn(forbidden, body)
        for phase in ('images', 'release', 'image-tag', 'chart', 'image-signatures',
                      'image-provenance-prepare', 'image-provenance-capture', 'image-attestations'):
            line = next(line for line in body.splitlines() if 'publish.py --phase '+phase+' ' in line)
            for flag in ('--dependencies', '--go', '--image-assembly', '--oras', '--progress-dir', '--work'):
                self.assertIn(flag, line)
        downloads = [step['with'] for step in job['steps'] if step.get('uses', '').startswith('actions/download-artifact@')]
        self.assertIn({'name': 'dependency-preparation-build', 'path': 'private/dependencies'}, downloads)
        self.assertIn({'name': 'private-chart-package', 'path': 'private/chart'}, downloads)
        restored = next(row for row in downloads if 'run-id' in row)
        self.assertEqual(restored['run-id'], '${{ inputs.previous_publication_run_id }}')
        self.assertEqual(restored['github-token'], '${{ github.token }}')
        retention = next(step for step in job['steps'] if step.get('name', '').startswith('Retain source-bound progress'))
        self.assertEqual(retention['if'], 'always()')
        self.assertNotIn('auth', retention['with']['path'])
        self.assertEqual(job['steps'][-1]['if'], 'always()')

    def test_workflow_publishes_chart_after_release_and_image_without_helm_rebuild(self):
        step = next(row for row in self.workflow()['jobs']['publish']['steps']
                    if row.get('name') == 'Sign, stage, download, verify and publish')
        lines = step['run'].splitlines()
        self.assertTrue(all(line.startswith('python3 -B scripts/release/publish.py --phase ') for line in lines))
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            commands = root/'commands'
            commands.mkdir()
            log = root/'commands.jsonl'
            python = commands/'python3'
            python.write_text('#!' + sys.executable + '\nimport json,os,sys\n'
                              'with open(os.environ["CPRA_WORKFLOW_COMMAND_LOG"],"a") as log:\n'
                              ' log.write(json.dumps(sys.argv[1:])+"\\n")\n')
            python.chmod(0o700)
            go = commands/'go'
            go.write_text('#!/bin/sh\nexit 0\n')
            go.chmod(0o700)
            env = {'PATH': str(commands)+':/usr/bin:/bin', 'RUNNER_TEMP': str(root),
                   'CPRA_WORKFLOW_COMMAND_LOG': str(log)}
            subprocess.run(['/bin/bash', '-e', '-c', step['run']], env=env, cwd=root, check=True, timeout=10)
            rows = [json.loads(line) for line in log.read_text().splitlines()]
            self.assertEqual([row[row.index('--phase')+1] for row in rows], ['release', 'image-tag', 'chart'])
            for row in rows:
                self.assertEqual(row[row.index('--progress-dir')+1], str(root/'cpra-publish-work/progress'))
                self.assertEqual(row[row.index('--oras')+1], str(root/'bin/release-tools/oras'))

    def test_workflow_local_payload_staging_copies_the_tested_chart_without_rebuilding(self):
        step = next(step for step in self.workflow()['jobs']['publish']['steps']
                    if step.get('name') == 'Verify and stage the privately assembled payloads')
        script = step['run'].split("python3 -B - <<'PYTHON'\n", 1)[1].split('\nPYTHON', 1)[0]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            manifest = manifest_fixture()
            (root/'dist/release').mkdir(parents=True)
            (root/'dist/release/RELEASE.json').write_bytes(release.encoded(manifest))
            for name in ('chart', 'oci', 'image-sboms'):
                (root/'private'/name).mkdir(parents=True)
            subjects = {'digest': 'sha256:'+'a'*64}
            (root/'private/oci/OCI.json').write_bytes(release.encoded(subjects))
            chart = root/'private/chart/cpra-chart-0.1.0.tgz'
            chart.write_bytes(b'original chart fixture')
            record = {'release_identity': release.evidence_identity(manifest),
                      'image_digest': subjects['digest'], 'chart_version': manifest['chart_version'],
                      'archive': chart.name, 'size': chart.stat().st_size, 'sha256': release.sha(chart)}
            (chart.parent/'chart-package.json').write_bytes(release.encoded(record))
            for arch in ('amd64', 'arm64'):
                (root/f'private/image-sboms/cpra-image-linux-{arch}.spdx.json').write_bytes(b'validated inventory fixture')
            environment = dict(os.environ, PYTHONPATH=str(release.ROOT/'scripts/release'), PYTHONDONTWRITEBYTECODE='1',
                               PYTHONOPTIMIZE='1')
            first = subprocess.run([sys.executable, '-B', '-c', script], cwd=root, env=environment,
                                   capture_output=True, text=True, timeout=10)
            self.assertEqual(first.returncode, 0, first.stderr)
            self.assertEqual((root/'dist/release'/chart.name).read_bytes(), chart.read_bytes())
            second = subprocess.run([sys.executable, '-B', '-c', script], cwd=root, env=environment,
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(second.returncode, 0, second.stderr)
            chart.write_bytes(b'changed chart')
            changed = subprocess.run([sys.executable, '-B', '-c', script], cwd=root, env=environment,
                                     capture_output=True, text=True, timeout=10)
            self.assertNotEqual(changed.returncode, 0)
            self.assertEqual((root/'dist/release'/chart.name).read_bytes(), b'original chart fixture')

    def test_workflow_authenticates_metadata_before_selecting_a_prior_artifact(self):
        step = next(step for step in self.workflow()['jobs']['publish']['steps']
                    if step.get('name') == 'Record authenticated workflow identities before restoring progress')
        script = step['run'].split("python3 -B - <<'PYTHON'\n", 1)[1].split('\nPYTHON', 1)[0]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root/'dist/release').mkdir(parents=True)
            (root/'cpra-publish-work').mkdir()
            manifest = manifest_fixture()
            (root/'dist/release/RELEASE.json').write_bytes(release.encoded(manifest))
            current, previous = workflow_run(manifest, 43), workflow_run(manifest)
            for name, run in (('current-run.json', current), ('previous-run.json', previous)):
                (root/'cpra-publish-work'/name).write_bytes(release.encoded(run))
            output = root/'output'
            environment = dict(os.environ, PYTHONPATH=str(release.ROOT/'scripts/release'), PYTHONDONTWRITEBYTECODE='1',
                               PYTHONOPTIMIZE='1', RUNNER_TEMP=str(root), GITHUB_OUTPUT=str(output),
                               GITHUB_RUN_ID='43', GITHUB_REPOSITORY=publish.REPOSITORY,
                               GITHUB_SHA=manifest['commit'], CPRA_PREVIOUS_PUBLICATION_RUN='42')
            result = subprocess.run([sys.executable, '-B', '-c', script], cwd=root, env=environment,
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(output.read_text(), 'artifact=publication-progress-42-1\n')
            output.unlink()
            for key, bad in (('GITHUB_RUN_ID', '44'), ('GITHUB_REPOSITORY', 'unrelated/repo'),
                             ('GITHUB_SHA', 'b'*40), ('CPRA_PREVIOUS_PUBLICATION_RUN', '41')):
                changed = dict(environment, **{key: bad})
                result = subprocess.run([sys.executable, '-B', '-c', script], cwd=root, env=changed,
                                        capture_output=True, text=True, timeout=10)
                with self.subTest(key=key):
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(output.exists())

    def test_every_mutating_phase_is_locked_before_reading_inputs(self):
        for phase in ('images', 'release', 'image-tag', 'chart', 'asset-signature', 'image-signatures',
                      'image-provenance-prepare', 'image-provenance-capture', 'image-attestations'):
            command = [sys.executable, '-B', str(release.ROOT/'scripts/release/publish.py'),
                       '--phase', phase, '--out', '/missing-cpra-fixture']
            result = subprocess.run(command, capture_output=True, text=True, timeout=10)
            with self.subTest(phase=phase):
                self.assertEqual(result.returncode, 2)
                self.assertIn('Public publication is locked', result.stderr)
                self.assertNotIn('Traceback', result.stderr)

    def test_workflow_restores_and_seals_original_bundles_before_remote_release(self):
        steps = self.workflow()['jobs']['publish']['steps']
        names = [step.get('name') for step in steps]
        restore = names.index('Restore original checksum and provenance bytes before resuming')
        transfer = names.index('Transfer and read back original OCI blobs and index')
        capture = names.index('Preserve original signed bytes before release operations')
        mutation = names.index('Sign, stage, download, verify and publish')
        self.assertLess(restore, transfer)
        self.assertLess(capture, mutation)
        roots = steps[names.index('Record offline trust roots alongside the already tested chart')]
        self.assertEqual(roots['if'], "steps.signing.outputs.inputs_ready != 'true'")
        assets = ('Preserve original inputs and prepare asset provenance generation', 'Attest checksummed release assets',
                  'Preserve original asset provenance before checksum signing', 'Generate and preserve the original checksum signature')
        self.assertEqual([names.index(name) for name in assets], sorted(names.index(name) for name in assets))
        self.assertLess(names.index(assets[-1]), capture)
        asset_attest = steps[names.index(assets[1])]
        self.assertEqual(asset_attest['if'], "steps.asset_provenance.outputs.generate == 'true'")
        self.assertIs(asset_attest['with']['push-to-registry'], False)
        self.assertIs(asset_attest['with']['create-storage-record'], False)
        self.assertIn('always()', steps[names.index(assets[2])]['if'])
        self.assertIn('--phase asset-signature', steps[names.index(assets[-1])]['run'])
        sequence = ('Generate and seal original image signature bundles', 'Prepare original image provenance generation',
                    'Generate original multiarch provenance bundle', 'Seal original multiarch provenance before registry writes',
                    'Transfer and verify original image signatures and provenance')
        self.assertEqual([names.index(name) for name in sequence], sorted(names.index(name) for name in sequence))
        self.assertLess(names.index(sequence[-1]), capture)
        attest = steps[names.index(sequence[2])]
        self.assertIs(attest['with']['push-to-registry'], False)
        self.assertIs(attest['with']['create-storage-record'], False)
        self.assertEqual(attest['if'], "steps.image_provenance.outputs.generate == 'true'")
        self.assertEqual(steps[names.index(sequence[3])]['if'], attest['if'])
        self.assertIn('--bundle "$IMAGE_PROVENANCE_BUNDLE"', steps[names.index(sequence[3])]['run'])
        prepare = steps[names.index(sequence[1])]['run']
        script = prepare.split("python3 -B - <<'PYTHON'\n", 1)[1].split('\nPYTHON', 1)[0]
        # Execute actual workflow state handling. Original-bundle generation and
        # crypto/recovery are covered independently by image-signing contracts.
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); work = root/'cpra-publish-work'; work.mkdir()
            for generate in (False, True):
                log = root/('output-'+str(generate))
                (work/'image-provenance-state.json').write_text(json.dumps({'generate': generate}))
                environment = dict(os.environ, PYTHONPATH=str(release.ROOT/'scripts/release'), PYTHONDONTWRITEBYTECODE='1',
                                   RUNNER_TEMP=str(root), GITHUB_OUTPUT=str(log))
                result = subprocess.run([sys.executable, '-B', '-c', script], cwd=root, env=environment,
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(log.read_text(), 'generate='+str(generate).lower()+'\n')
            (work/'image-provenance-state.json').write_text('{"generate":"true"}')
            result = subprocess.run([sys.executable, '-B', '-c', script], cwd=root, env=environment,
                                    capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
        final = steps[mutation]['run']
        self.assertNotIn('sign-blob', final)
        self.assertNotIn('provenance.sigstore.json', final)

    def test_local_prepare_command_executes_without_publication_or_compiler(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            manifest = manifest_fixture()
            (root/'RELEASE.json').write_bytes(release.encoded(manifest))
            (root/'run.json').write_bytes(release.encoded(workflow_run(manifest)))
            result = subprocess.run([sys.executable, '-B', str(release.ROOT/'scripts/release/publish.py'),
                                     '--phase', 'prepare', '--out', str(root), '--run-record', str(root/'run.json'),
                                     '--progress-dir', str(root/'progress')], capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((root/'progress/publication.json').is_file())


if __name__ == '__main__':
    unittest.main()
