"""Boundary tests for the separately compiled browser parser release inputs."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import dashboard_build
import package
import sbom

SPEC = importlib.util.spec_from_file_location(
    'build_collection_parser', Path(__file__).resolve().parents[1] / 'dashboard/build_collection_parser.py')
parser_build = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(parser_build)


class DashboardParserTests(unittest.TestCase):
    def test_normalization_gate_qualifies_base_wasm_against_base_and_tagged_native(self):
        fixture_bytes = (parser_build.ROOT / 'sdk/go/collection/testdata/file-base-v1.json').read_bytes()
        outcome = {'result': 'passed', 'profile': 'cpra.file.base.v1', 'fixedCases': len(json.loads(fixture_bytes)['cases']), 'nativeBuilds': 2, 'wasmBuilds': 1}
        environment = parser_build.build_environment()
        with tempfile.TemporaryDirectory() as temporary, \
             patch.object(parser_build.subprocess, 'check_output', side_effect=[json.dumps({'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64'}), json.dumps(outcome)]) as output, \
             patch.object(parser_build.subprocess, 'run') as run:
            digest = parser_build.qualify_normalization('go', 'node', environment, Path('/toolchain'), Path('/base.wasm'), temporary)
        self.assertEqual(digest, hashlib.sha256(fixture_bytes).hexdigest())
        self.assertEqual(run.call_count, 2)
        for call, tag in zip(run.call_args_list, ('-tags=', '-tags=externaljobs')):
            self.assertIn(tag, call.args[0])
            self.assertEqual(call.kwargs['env']['GOOS'], 'linux')
            self.assertEqual(call.kwargs['env']['GOARCH'], 'amd64')
            self.assertEqual(call.kwargs['env']['GOWORK'], 'off')
            self.assertNotIn('NODE_OPTIONS', call.kwargs['env'])
        self.assertEqual(environment['GOOS'], 'js')
        self.assertIn('/base.wasm', output.call_args.args[0])

    def test_normalization_gate_rejects_incomplete_or_mismatched_report(self):
        for result in ({'result': 'failed'}, {'result': 'passed', 'profile': 'other'},
                       {'result': 'passed', 'profile': 'cpra.file.base.v1', 'fixedCases': 0, 'nativeBuilds': 2, 'wasmBuilds': 1}):
            with self.subTest(result=result), tempfile.TemporaryDirectory() as temporary, \
                 patch.object(parser_build.subprocess, 'check_output', side_effect=[json.dumps({'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64'}), json.dumps(result)]), \
                 patch.object(parser_build.subprocess, 'run'), self.assertRaisesRegex(ValueError, 'qualification did not complete'):
                parser_build.qualify_normalization('go', 'node', parser_build.build_environment(), Path('/toolchain'), Path('/base.wasm'), temporary)

    def test_frontend_version_checks_also_exclude_node_preloads(self):
        observed = []

        def version(command, **kwargs):
            observed.append(kwargs['env'])
            key = 'node_version' if command[0] == 'node' else 'pnpm_version'
            return dashboard_build.release.RECIPE['frontend'][key] + '\n'

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'dashboard').mkdir()
            with patch.dict(os.environ, {'NODE_OPTIONS': '--require /private/preload.js'}), \
                 patch('sys.argv', ['dashboard_build.py']), \
                 patch.object(dashboard_build.release, 'ROOT', root), \
                 patch.object(dashboard_build.subprocess, 'check_output', side_effect=version), \
                 patch.object(dashboard_build.subprocess, 'run') as run:
                dashboard_build.main()
                observed.extend(call.kwargs['env'] for call in run.call_args_list)
        self.assertGreater(len(observed), 2)
        self.assertTrue(all('NODE_OPTIONS' not in environment for environment in observed))

    def test_parser_environment_excludes_ambient_execution_and_credentials(self):
        with patch.dict(os.environ, {
            'NODE_OPTIONS': '--require /private/inject.js', 'GODEBUG': 'something=1',
            'GOFLAGS': '-tags=externaljobs', 'GOWORK': '/private/go.work',
            'GOEXPERIMENT': 'something', 'GOOS': 'linux', 'GOARCH': 'amd64',
            'AWS_SECRET_ACCESS_KEY': 'must-not-be-a-build-input', 'VITE_TOKEN': 'private',
        }):
            environment = parser_build.build_environment()
        for name in ('NODE_OPTIONS', 'GODEBUG', 'AWS_SECRET_ACCESS_KEY', 'VITE_TOKEN'):
            self.assertNotIn(name, environment)
        self.assertEqual(environment['GOWORK'], 'off')
        self.assertEqual(environment['GOFLAGS'], '')
        self.assertEqual(environment['GOEXPERIMENT'], '')
        self.assertEqual(environment['GOOS'], 'js')
        self.assertEqual(environment['GOARCH'], 'wasm')

    def test_unshipped_foundation_is_not_reported_as_embedded(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(package, 'ROOT', Path(directory)):
            self.assertEqual(package.dashboard_wasm_inventory(), ([], {}))

    def fixture(self, root):
        generated = root / 'dashboard/src/import/generated'
        generated.mkdir(parents=True)
        dist = root / 'dashboard/dist/assets'
        dist.mkdir(parents=True)
        dependencies = [
            {'ecosystem': 'go', 'scope': 'dashboard-wasm', 'name': 'github.com/ziad-hsn/cpra/sdk/go',
             'version': 'source', 'source_sha256': '1' * 64, 'declared_license': 'MIT'},
            {'ecosystem': 'go', 'scope': 'dashboard-wasm', 'name': 'gopkg.in/yaml.v3',
             'version': 'v3.0.1', 'module_sum': 'h1:recorded'},
        ]
        files = {'collection-parser.wasm': b'qualified-wasm', 'wasm_exec.js': b'matching-runtime',
                 'GO-LICENSE.txt': b'go-license', 'LICENSES.txt': b'linked-license-texts',
                 'DEPENDENCIES.json': json.dumps(dependencies).encode()}
        for name, data in files.items():
            (generated / name).write_bytes(data)
        metadata = {'target': 'js/wasm', 'externaljobs': False, 'sdk_source_sha256': '1' * 64,
                    'files': {name: {'bytes': len(data), 'sha256': hashlib.sha256(data).hexdigest()}
                              for name, data in files.items()}}
        (generated / 'BUILD.json').write_text(json.dumps(metadata))
        artifact = dist / 'collection-parser-fingerprint.wasm'
        artifact.write_bytes(files['collection-parser.wasm'])
        return generated, artifact, metadata

    def test_actual_wasm_inventory_reaches_spdx_with_source_and_module_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, artifact, _ = self.fixture(root)
            with patch.object(package, 'ROOT', root):
                dependencies, notices = package.dashboard_wasm_inventory()
            self.assertEqual(notices, {'dashboard-wasm/LICENSES.txt': b'linked-license-texts'})
            document = {'SPDXID': 'SPDXRef-DOCUMENT'}
            sbom.append_frontend(document, dependencies)
            entries = {entry['name']: entry for entry in document['packages']}
            sdk = entries['github.com/ziad-hsn/cpra/sdk/go']
            self.assertEqual(sdk['versionInfo'], 'source')
            self.assertIn('no published module version', sdk['sourceInfo'])
            self.assertIn('h1:recorded', entries['gopkg.in/yaml.v3']['sourceInfo'])
            self.assertEqual(entries['CPRa collection parser']['checksums'][0]['checksumValue'],
                             hashlib.sha256(artifact.read_bytes()).hexdigest())
            self.assertEqual(len(document['relationships']), len(dependencies))

    def test_changed_asset_or_linked_notice_fails_inventory(self):
        for changed in ('artifact', 'DEPENDENCIES.json', 'LICENSES.txt', 'wasm_exec.js'):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                generated, artifact, _ = self.fixture(root)
                (artifact if changed == 'artifact' else generated / changed).write_bytes(b'changed')
                with patch.object(package, 'ROOT', root), self.assertRaisesRegex(RuntimeError, 'differs'):
                    package.dashboard_wasm_inventory()

    def test_missing_or_unsafe_file_catalog_is_rejected(self):
        for change in ('missing', 'unsafe', 'externaljobs'):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                generated, _, metadata = self.fixture(root)
                if change == 'missing':
                    del metadata['files']['LICENSES.txt']
                elif change == 'unsafe':
                    metadata['files']['../private'] = {'bytes': 1, 'sha256': '0' * 64}
                else:
                    metadata['externaljobs'] = True
                (generated / 'BUILD.json').write_text(json.dumps(metadata))
                with patch.object(package, 'ROOT', root), self.assertRaises(RuntimeError):
                    package.dashboard_wasm_inventory()


if __name__ == '__main__':
    unittest.main()
