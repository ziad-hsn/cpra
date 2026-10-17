"""Chart byte identity, version ownership and interrupted-publication contracts."""
import copy
import gzip
import io
import json
from pathlib import Path
import signal
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import chart
import oci
import package
import publish
import registry
import release
import yaml
from test_evidence import manifest_fixture
from test_registry import ReadbackFixture, UploadFixture


def fixture(root):
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    manifest = manifest_fixture()
    digest = 'sha256:' + 'd' * 64
    metadata = {'apiVersion': 'v2', 'name': 'cpra', 'type': 'application',
                'description': 'Local chart publication fixture',
                'version': manifest['chart_version'], 'appVersion': manifest['version'],
                'annotations': {'cpra.io/source-commit': manifest['commit']}}
    values = {'image': {'repository': 'ghcr.io/ziad-hsn/cpra', 'tag': '', 'digest': digest}}
    path = root / ('cpra-chart-' + manifest['chart_version'] + '.tgz')
    entries = {'cpra/Chart.yaml': (yaml.safe_dump(metadata).encode(), False),
               'cpra/values.yaml': (yaml.safe_dump(values).encode(), False),
               'cpra/values.schema.json': (b'{"type":"object"}', False)}
    package.archive(path, entries, manifest['source_date_epoch'])
    return manifest, path, digest, entries


def integration(tools, origin, root, environment):
    """Real registry writes, two process deaths and actual Helm 3/4 consumers."""
    tools, root = Path(tools), Path(root)
    manifest, path, image_digest, _ = fixture(root / 'chart-source')
    manifest_path = root / 'chart-release.json'
    manifest_path.write_bytes(release.encoded(manifest))
    progress = root / 'chart.jsonl'
    pins = json.loads(Path(__file__).with_name('tools.json').read_text())
    child = (
        "import json,os,signal,sys\nfrom pathlib import Path\nimport chart,registry\n"
        "root=Path(sys.argv[1]);tools=Path(sys.argv[2]);boundary=sys.argv[4]\n"
        "manifest=json.loads((root/'chart-release.json').read_text())\n"
        "class Interrupted(registry.Registry):\n"
        " def upload(self,oras,row,path,work,**kwargs):\n"
        "  super().upload(oras,row,path,work,**kwargs)\n"
        "  if (boundary=='config' and row['media_type']==chart.CONFIG_MEDIA) or (boundary=='tag' and kwargs.get('tag')):\n"
        "   os.kill(os.getpid(),signal.SIGKILL)\n"
        "remote=Interrupted(chart.REPOSITORY,origin=sys.argv[3],allow_loopback_http=True)\n"
        "chart.publish_chart(manifest,root/'chart-source'/('cpra-chart-'+manifest['chart_version']+'.tgz'),"
        "'sha256:'+'d'*64,remote,tools/'oras',root/'chart.jsonl',root)\n")
    env = dict(environment, PYTHONPATH=str(Path(__file__).parent), PYTHONDONTWRITEBYTECODE='1')
    for boundary, count in (('config', 2), ('tag', 8)):
        result = subprocess.run([sys.executable, '-B', '-c', child, str(root), str(tools), origin, boundary],
                                env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=45)
        if result.returncode != -signal.SIGKILL:
            raise ValueError('Chart process-crash boundary was not reached: ' + boundary + ': '
                             + result.stderr.decode(errors='replace')[-4000:])
        rows = [json.loads(line) for line in progress.read_bytes().splitlines()]
        if len(rows) != count or rows[-1]['state'] != 'started':
            raise ValueError('Chart crash did not preserve the expected committed intent: ' + boundary)
    remote = registry.Registry(chart.REPOSITORY, origin=origin, allow_loopback_http=True)
    result = chart.publish_chart(manifest, path, image_digest, remote, tools/'oras', progress, root)
    again = chart.publish_chart(manifest, path, image_digest, remote, tools/'oras', progress, root)
    if (result['uploaded'] != 0 or result['reused'] != 3 or result['tag_reused'] is not True
            or again != result or result['archive_sha256'] != release.sha(path)):
        raise ValueError('Actual chart continuation did not reuse the complete original graph')
    with remote.response('tags', 'list') as response:
        if registry.metadata(response, 3).get('tags') != [manifest['chart_version']]:
            raise ValueError('Chart repository contains an unexpected version')
    tools_record, consumers = {}, []
    for name in ('helm3', 'helm4'):
        helm = tools/name
        if helm.is_symlink() or not helm.is_file():
            raise ValueError('Real chart consumers require the pinned Helm executable')
        tools_record[name] = {'version': pins[name]['version'], 'sha256': release.sha(helm),
                              'archive_sha256': pins[name]['sha256']}
        helm_home = root/name
        helm_home.mkdir(mode=0o700)
        helm_env = dict(environment, HOME=str(helm_home), HELM_CACHE_HOME=str(helm_home/'cache'),
                        HELM_CONFIG_HOME=str(helm_home/'config'), HELM_DATA_HOME=str(helm_home/'data'))
        version = subprocess.check_output([str(helm), 'version', '--template', '{{.Version}}'],
                                          env=helm_env, timeout=10, text=True)
        if version != 'v' + pins[name]['version']:
            raise ValueError('Helm version differs from the recorded release tool')
        for reference in ('version', 'digest'):
            destination = helm_home/reference
            destination.mkdir(mode=0o700)
            url = 'oci://' + remote.host + '/' + chart.REPOSITORY
            arguments = ['--version', manifest['chart_version']] if reference == 'version' else []
            if reference == 'digest':
                url += '@' + result['manifest_digest']
            process = subprocess.run([str(helm), 'pull', url, '--plain-http', '--destination', str(destination), *arguments],
                                     stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=helm_env, timeout=30)
            if process.returncode != 0 or len(process.stdout) + len(process.stderr) > 64 * 1024:
                raise ValueError('Actual Helm chart pull failed: ' + name + ': ' + reference + ': '
                                 + process.stderr.decode(errors='replace')[-4000:])
            filename = ('cpra-' + manifest['chart_version'] if reference == 'version'
                        else 'cpra@sha256-' + result['manifest_digest'][7:]) + '.tgz'
            downloaded = destination/filename
            if not downloaded.is_file() or downloaded.read_bytes() != path.read_bytes():
                diagnostic = {'tool': name, 'reference': reference, 'original_sha256': release.sha(path),
                              'expected_file': downloaded.name,
                              'files': {entry.name: release.sha(entry) for entry in destination.iterdir() if entry.is_file()},
                              'stdout': process.stdout.decode(errors='replace'),
                              'stderr': process.stderr.decode(errors='replace')}
                with tempfile.NamedTemporaryFile(prefix='chart-consumer-failure-', suffix='.json', dir=root.parent, delete=False) as record:
                    record.write(release.encoded(diagnostic))
                raise ValueError('Helm consumer archive mismatch: ' + name + ': ' + reference + ': ' + json.dumps(diagnostic['files']))
            consumers.append({'tool': name, 'reference': reference, 'filename': filename,
                              'archive_sha256': release.sha(downloaded),
                              'manifest_digest': result['manifest_digest']})
    return {'status': 'pass', 'publication': result, 'tools': tools_record, 'consumers': consumers,
            'checks': ['sigkill_after_chart_config_accept', 'sigkill_after_chart_version_accept',
                       'resume_reuses_all_original_objects', 'second_resume_rechecks_every_byte',
                       'helm3_version_and_digest_downloads', 'helm4_version_and_digest_downloads'],
            'fixture': 'local minimal Helm chart; not CPRa Kubernetes execution or GHCR qualification',
            'release_gate_passed': False, 'public_objects_created': False}


class ChartPublicationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.manifest, self.path, self.digest, self.entries = fixture(self.root)
        self.progress = self.root / 'chart.jsonl'
        self.server = ReadbackFixture()
        self.addCleanup(self.server.close)
        self.remote = UploadFixture(self.server, chart.REPOSITORY)

    def publish(self, **kwargs):
        return chart.publish_chart(self.manifest, self.path, self.digest, self.remote,
                                   '/fixture-oras', self.progress, self.root, **kwargs)

    def objects(self):
        from qualification import Inputs
        with tempfile.TemporaryDirectory(dir=self.root) as temporary:
            objects = chart.publication_objects(self.manifest, self.path, self.digest,
                                                 Path(temporary), Inputs())
            return [(row, path.read_bytes()) for row, path in objects]

    def write(self):
        package.archive(self.path, self.entries, self.manifest['source_date_epoch'])

    def test_preserves_archive_and_deterministic_envelope_in_dependency_order(self):
        result = self.publish()
        objects = self.objects()
        self.assertEqual(self.remote.uploads, [row for row, _ in objects] + [objects[-1][0]])
        self.assertEqual(result['archive_sha256'], release.sha(self.path))
        self.assertEqual(result['objects_verified'], 3)
        self.assertFalse(result['release_gate_passed'])
        config, layer, envelope = [body for _, body in objects]
        self.assertEqual(layer, self.path.read_bytes())
        self.assertEqual(json.loads(config), yaml.safe_load(self.entries['cpra/Chart.yaml'][0]))
        self.assertEqual(json.loads(envelope)['annotations']['org.opencontainers.image.created'], self.manifest['source_date'])
        again = self.publish()
        self.assertEqual(again['uploaded'], 0)
        self.assertEqual(again['reused'], 3)
        self.assertTrue(again['tag_reused'])
        self.assertEqual(len(self.remote.uploads), 4)

    def test_two_work_roots_produce_identical_descriptors_and_bytes(self):
        expected = self.objects()
        with tempfile.TemporaryDirectory(dir=self.root) as temporary:
            root = Path(temporary)
            _, path, _, _ = fixture(root)
            from qualification import Inputs
            objects = chart.publication_objects(self.manifest, path, self.digest, root, Inputs())
            self.assertEqual([(row, path.read_bytes()) for row, path in objects], expected)

    def test_preflight_reserves_no_external_operation(self):
        result = self.publish(preflight_only=True)
        self.assertTrue(result['available'])
        self.assertEqual(self.remote.uploads, [])
        self.assertEqual(len(self.progress.read_bytes().splitlines()), 1)

    def test_lost_reply_at_each_upload_resumes_without_duplicate_accepted_writes(self):
        for boundary in range(1, 5):
            with self.subTest(boundary=boundary):
                self.server.objects.clear()
                self.remote.uploads.clear()
                self.progress.unlink(missing_ok=True)
                upload = self.remote.upload
                def lost_reply(*args, **kwargs):
                    upload(*args, **kwargs)
                    if len(self.remote.uploads) == boundary:
                        raise RuntimeError('lost accepted upload reply')
                with patch.object(self.remote, 'upload', side_effect=lost_reply), self.assertRaises(RuntimeError):
                    self.publish()
                self.publish()
                self.publish()
                self.assertEqual(len(self.remote.uploads), 4)

    def test_confirmed_absence_after_unaccepted_upload_can_finish(self):
        self.remote.fail_before = True
        with self.assertRaises(RuntimeError):
            self.publish()
        self.publish()
        self.assertEqual(len(self.remote.uploads), 4)

    def test_existing_matching_version_requires_original_intent(self):
        row, body = self.objects()[-1]
        self.server.objects[('manifest', self.manifest['chart_version'])] = row, body
        with self.assertRaisesRegex(ValueError, 'without original'):
            self.publish()
        self.assertEqual(self.remote.uploads, [])

    def test_existing_verified_version_is_allowed_by_preflight_on_resume(self):
        self.publish()
        self.assertFalse(self.publish(preflight_only=True)['available'])
        self.assertEqual(len(self.remote.uploads), 4)

    def test_preflight_rechecks_every_previously_verified_object(self):
        self.publish()
        for row, _ in self.objects():
            key = row['kind'], row['digest']
            with self.subTest(key=key):
                original = self.server.objects.pop(key)
                with self.assertRaisesRegex(ValueError, 'missing; replacement'):
                    self.publish(preflight_only=True)
                self.server.objects[key] = original
        self.assertEqual(len(self.remote.uploads), 4)

    def test_preexisting_identical_digests_can_be_reused_before_new_version(self):
        for row, body in self.objects():
            self.server.objects[(row['kind'], row['digest'])] = row, body
        result = self.publish()
        self.assertEqual(result['reused'], 3)
        self.assertEqual(result['uploaded'], 0)
        self.assertEqual(len(self.remote.uploads), 1)
        self.publish()

    def test_missing_verified_object_or_version_cannot_be_replaced(self):
        for key in [('manifest', self.manifest['chart_version'])] + [(row['kind'], row['digest']) for row, _ in self.objects()]:
            with self.subTest(key=key):
                self.publish()
                original = self.server.objects.pop(key)
                count = len(self.remote.uploads)
                with self.assertRaisesRegex(ValueError, 'missing; replacement'):
                    self.publish()
                self.assertEqual(len(self.remote.uploads), count)
                self.server.objects[key] = original

    def test_conflicting_or_uncertain_version_never_gets_overwritten(self):
        row, body = self.objects()[-1]
        self.server.objects[('manifest', self.manifest['chart_version'])] = row, b'x' * len(body)
        with self.assertRaisesRegex(ValueError, 'bytes differ'):
            self.publish()
        self.server.objects.clear()
        self.server.failure = (503, b'{}', {})
        with self.assertRaisesRegex(ValueError, 'uncertain'):
            self.publish()
        self.assertEqual(self.remote.uploads, [])

    def test_source_or_archive_change_cannot_resume_old_publication(self):
        self.publish()
        for change in ('source', 'archive'):
            with self.subTest(change=change):
                manifest = copy.deepcopy(self.manifest)
                original = self.path.read_bytes()
                if change == 'source':
                    self.manifest['source_files']['main.go']['sha256'] = '1' * 64
                else:
                    self.entries['cpra/README.md'] = b'changed source', False
                    self.write()
                with self.assertRaisesRegex(ValueError, 'different source, graph'):
                    self.publish()
                self.manifest = manifest
                self.path.write_bytes(original)
        self.assertEqual(len(self.remote.uploads), 4)

    def test_source_change_during_readback_stops_before_admission(self):
        verify = self.remote.verify
        def change(*args, **kwargs):
            present = verify(*args, **kwargs)
            with self.path.open('ab') as stream:
                stream.write(b'changed')
            return present
        with patch.object(self.remote, 'verify', side_effect=change), self.assertRaises(ValueError):
            self.publish()
        self.assertEqual(self.remote.uploads, [])

    def test_out_of_order_duplicate_or_receipt_progress_is_rejected(self):
        self.publish()
        original = self.progress.read_bytes()
        lines = original.splitlines()
        for record in (lines[2], lines[-1], publish.release_bytes({'kind': 'blob', 'digest': 'sha256:' + 'e' * 64, 'state': 'started'}).strip()):
            with self.subTest(record=record):
                self.progress.write_bytes(original + record + b'\n')
                with self.assertRaises(ValueError):
                    self.publish()
        self.progress.write_bytes(original)
        self.assertEqual(len(self.remote.uploads), 4)

    def test_locked_progress_cannot_start_another_owner(self):
        from module_publication import marker
        self.publish(preflight_only=True)
        owner = json.loads(self.progress.read_bytes().splitlines()[0])['identity']
        entries = owner['objects'] + [marker('chart-tag', owner)]
        with publish.TransferProgress(self.progress, owner, entries):
            with self.assertRaisesRegex(ValueError, 'locked'):
                self.publish()
        self.assertEqual(self.remote.uploads, [])

    def test_verified_version_requires_its_original_started_marker(self):
        self.publish()
        lines = self.progress.read_bytes().splitlines()
        self.progress.write_bytes(b'\n'.join(lines[:-2] + [lines[-1]]) + b'\n')
        self.server.requests.clear()
        with self.assertRaisesRegex(ValueError, 'original started intent'):
            self.publish()
        self.assertEqual(self.server.requests, [])
        self.assertEqual(len(self.remote.uploads), 4)

    def test_partial_final_record_is_repaired_under_the_original_lock(self):
        self.publish()
        original = self.progress.read_bytes()
        with self.progress.open('ab') as stream:
            stream.write(b'{"incomplete"')
        result = self.publish()
        self.assertEqual(result['uploaded'], 0)
        self.assertEqual(self.progress.read_bytes(), original)

    def test_quota_stops_before_external_writes(self):
        with patch.object(publish, 'PROGRESS_LIMIT', 64), self.assertRaises(ValueError):
            self.publish()
        self.assertEqual(self.remote.uploads, [])

    def test_final_tag_change_during_dependency_readback_is_reported(self):
        verify = self.remote.verify
        reads = 0
        def change(row, **kwargs):
            nonlocal reads
            present = verify(row, **kwargs)
            if row['kind'] == 'blob':
                reads += 1
                if reads == 5:
                    self.server.objects.pop(('manifest', self.manifest['chart_version']), None)
            return present
        with patch.object(self.remote, 'verify', side_effect=change), self.assertRaisesRegex(ValueError, 'final readback'):
            self.publish()
        self.assertNotEqual(json.loads(self.progress.read_bytes().splitlines()[-1])['state'], 'verified')

    def test_dirty_source_wrong_repository_and_metadata_fail_before_http(self):
        original = copy.deepcopy(self.manifest)
        for key, value in (('dirty', True), ('candidate', True), ('chart_version', 'latest'), ('source_date', 'wrong')):
            with self.subTest(key=key):
                self.manifest[key] = value
                with self.assertRaises(ValueError):
                    self.publish()
                self.manifest = copy.deepcopy(original)
        self.remote.repository = 'ziad-hsn/cpra'
        with self.assertRaisesRegex(ValueError, 'canonical chart'):
            self.publish()
        self.assertEqual(self.server.requests, [])

    def test_chart_and_image_identity_mismatches_fail_before_http(self):
        for name, field, value in (('cpra/Chart.yaml', 'name', 'other'), ('cpra/Chart.yaml', 'version', '0.2.0'),
                                   ('cpra/Chart.yaml', 'appVersion', 'v9.0.0'), ('cpra/Chart.yaml', 'annotations', {}),
                                   ('cpra/Chart.yaml', 'dependencies', [{'name': 'extra'}]),
                                   ('cpra/values.yaml', 'image', {'repository': 'other', 'digest': self.digest, 'tag': ''})):
            with self.subTest(field=field):
                original = self.entries[name]
                metadata = yaml.safe_load(original[0])
                metadata[field] = value
                self.entries[name] = yaml.safe_dump(metadata).encode(), False
                self.write()
                with self.assertRaisesRegex(ValueError, 'identity differs|image differs'):
                    self.publish()
                self.entries[name] = original
        self.assertEqual(self.server.requests, [])

    def test_metadata_duplicates_aliases_and_oversize_are_rejected(self):
        original = self.entries['cpra/Chart.yaml']
        for data in (original[0] + b'name: cpra\n', original[0] + b'alias: &a [x]\ncopy: *a\n', b'x' * (chart.CHART_METADATA_LIMIT + 1)):
            with self.subTest(size=len(data)):
                self.entries['cpra/Chart.yaml'] = data, False
                self.write()
                with self.assertRaises(ValueError):
                    self.publish()
        self.entries['cpra/Chart.yaml'] = original
        self.assertEqual(self.server.requests, [])

    def test_compressed_archive_and_metadata_complexity_are_bounded_before_http(self):
        self.path.write_bytes(b'')
        with self.path.open('r+b') as stream:
            stream.truncate(chart.CHART_LIMIT + 1)
        with self.assertRaisesRegex(ValueError, 'at most 16 MiB'):
            self.publish()
        nested = 'value'
        for _ in range(40):
            nested = {'level': nested}
        self.entries['cpra/Chart.yaml'] = json.dumps(nested).encode(), False
        self.write()
        with self.assertRaisesRegex(ValueError, 'too complex'):
            self.publish()
        self.assertEqual(self.server.requests, [])

    def test_physical_archive_expansion_is_bounded_before_tar_parsing(self):
        self.path.write_bytes(gzip.compress(b'\0' * (2 * chart.CHART_LIMIT + 1)))
        with self.assertRaisesRegex(ValueError, 'expansion limit'):
            self.publish()
        self.assertEqual(self.server.requests, [])

    def test_archive_links_duplicates_traversal_and_missing_schema_are_rejected(self):
        for kind in ('link', 'duplicate', 'traversal', 'schema-directory', 'missing-schema'):
            with self.subTest(kind=kind):
                with tarfile.open(self.path, 'w:gz') as archive:
                    for name, (data, _) in self.entries.items():
                        if name.endswith('schema.json') and kind in ('schema-directory', 'missing-schema'):
                            if kind == 'schema-directory':
                                member = tarfile.TarInfo(name)
                                member.type = tarfile.DIRTYPE
                                archive.addfile(member)
                            continue
                        member = tarfile.TarInfo(name)
                        member.size = len(data)
                        archive.addfile(member, io.BytesIO(data))
                    if kind in ('link', 'duplicate', 'traversal'):
                        member = tarfile.TarInfo('cpra/Chart.yaml' if kind == 'duplicate' else 'cpra/../escape' if kind == 'traversal' else 'cpra/link')
                        if kind == 'link':
                            member.type, member.linkname = tarfile.SYMTYPE, '/outside'
                        archive.addfile(member, io.BytesIO(b''))
                with self.assertRaises(ValueError):
                    self.publish()
        self.assertEqual(self.server.requests, [])

    def test_chart_tags_are_scoped_and_never_expand_image_version_rules(self):
        row, _ = self.objects()[-1]
        registry.validate_tag(row, '0.1.0', repository=chart.REPOSITORY)
        registry.validate_tag(row, '0.1.0-rc.1', repository=chart.REPOSITORY)
        for version, repository in (('v0.1.0', chart.REPOSITORY), ('latest', chart.REPOSITORY),
                                     ('0.1.0', 'ziad-hsn/cpra'), ('0.1.0', 'unknown')):
            with self.subTest(version=version, repository=repository), self.assertRaises(ValueError):
                registry.validate_tag(row, version, repository=repository)
        with self.assertRaises(ValueError):
            registry.validate_tag(dict(row, media_type=oci.INDEX), '0.1.0', repository=chart.REPOSITORY)

    def test_ambient_provenance_file_cannot_change_the_oci_graph(self):
        expected = self.objects()
        self.path.with_suffix('.tgz.prov').write_bytes(b'ambient file')
        self.assertEqual(self.objects(), expected)


if __name__ == '__main__':
    unittest.main()
