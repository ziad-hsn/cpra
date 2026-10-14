"""Release identity checks use small fixtures, not platform qualification claims."""
import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import yaml

import native_smoke
import publish
import release


def manifest_fixture():
    files = ('go.mod', 'go.sum', 'main.go', 'sdk/go/go.mod', 'sdk/go/go.sum',
             'sdk/go/client.go', 'sdk/go/worker/go.mod', 'sdk/go/worker/go.sum',
             'sdk/go/worker/worker.go')
    return {
        'schema_version': 1, 'version': 'v0.1.0-rc.1', 'commit': 'a' * 40,
        'candidate': False, 'dirty': False,
        'source_date_epoch': 123, 'source_date': '1970-01-01T00:02:03Z',
        'recipe': copy.deepcopy(release.RECIPE),
        'toolchain_archives': copy.deepcopy(release.TOOLCHAINS[release.RECIPE['go_version']]),
        'dashboard_sha256': 'b' * 64,
        'storage_format_version': release.RECIPE['storage_format_version'],
        'chart_version': '0.1.0', 'package_revision': '1',
        'source_files': {name: {'mode': '100644', 'sha256': hashlib.sha256(name.encode()).hexdigest()}
                         for name in files},
    }


class EvidenceIdentityTests(unittest.TestCase):
    def test_identity_is_deterministic_across_json_order_and_formatting(self):
        manifest = manifest_fixture()
        reordered = json.loads(json.dumps(manifest, sort_keys=True))
        self.assertEqual(release.evidence_identity(manifest), release.evidence_identity(reordered))

    def test_same_commit_cannot_reuse_evidence_after_any_release_input_changes(self):
        original = manifest_fixture()
        identity = release.evidence_identity(original)
        prefix, patch_version = original['recipe']['go_version'].rsplit('.', 1)
        different_compiler = prefix + '.' + str(int(patch_version) + 1)
        mutations = (
            ('SDK implementation', lambda m: m['source_files']['sdk/go/client.go'].update(sha256='1' * 64)),
            ('worker implementation', lambda m: m['source_files']['sdk/go/worker/worker.go'].update(sha256='2' * 64)),
            ('SDK dependency', lambda m: m['source_files']['sdk/go/go.mod'].update(sha256='3' * 64)),
            ('file mode', lambda m: m['source_files']['main.go'].update(mode='100755')),
            ('compiler', lambda m: m['recipe'].update(go_version=different_compiler)),
            ('build settings', lambda m: m['recipe']['build_flags'].append('-race')),
            ('compiler archives', lambda m: m['toolchain_archives'].update({'linux_amd64': '4' * 64})),
            ('dashboard', lambda m: m.update(dashboard_sha256='c' * 64)),
            ('storage format', lambda m: m.update(storage_format_version=99)),
            ('chart version', lambda m: m.update(chart_version='0.2.0')),
            ('application version', lambda m: m.update(version='v0.1.0')),
        )
        for name, mutate in mutations:
            with self.subTest(name=name):
                changed = copy.deepcopy(original)
                mutate(changed)
                self.assertEqual(original['commit'], changed['commit'])
                self.assertNotEqual(identity, release.evidence_identity(changed))

    def test_unfrozen_or_incomplete_metadata_cannot_issue_release_identity(self):
        mutations = (
            ('boolean format version', lambda m: m.update(schema_version=True)),
            ('dirty', lambda m: m.update(dirty=True)),
            ('development candidate', lambda m: m.update(candidate=True)),
            ('missing clean flag', lambda m: m.pop('dirty')),
            ('missing candidate flag', lambda m: m.pop('candidate')),
            ('short commit', lambda m: m.update(commit='abc123')),
            ('empty source inventory', lambda m: m.update(source_files={})),
            ('missing SDK module', lambda m: m['source_files'].pop('sdk/go/go.mod')),
            ('missing worker module', lambda m: m['source_files'].pop('sdk/go/worker/go.mod')),
            ('invalid hash', lambda m: m['source_files']['main.go'].update(sha256='invalid')),
            ('unknown mode', lambda m: m['source_files']['main.go'].update(mode='160000')),
            ('escaping path', lambda m: m['source_files'].update({'../outside': m['source_files']['main.go']})),
            ('directory entry', lambda m: m['source_files'].update({'.': m['source_files']['main.go']})),
            ('missing recipe', lambda m: m.pop('recipe')),
            ('missing compiler pins', lambda m: m.pop('toolchain_archives')),
            ('nonfinite metadata', lambda m: m.update(source_date_epoch=float('nan'))),
        )
        for name, mutate in mutations:
            with self.subTest(name=name):
                manifest = manifest_fixture()
                mutate(manifest)
                with self.assertRaises(ValueError):
                    release.evidence_identity(manifest)

    def test_native_harness_rejects_unbound_source_before_executing_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            manifest = manifest_fixture()
            manifest['source_files'] = {}
            (stage / 'RELEASE.json').write_text(json.dumps(manifest))
            argv = ['native_smoke.py', '--stage', str(stage), '--out', str(stage / 'report.json')]
            with patch.object(sys, 'argv', argv), \
                    patch.object(native_smoke.subprocess, 'check_output', side_effect=AssertionError('executed artifact')):
                with self.assertRaisesRegex(ValueError, 'source inventory'):
                    native_smoke.main()
            self.assertFalse((stage / 'report.json').exists())

    def test_packaging_reader_rejects_same_commit_stale_or_missing_identity(self):
        manifest = manifest_fixture()
        changed = copy.deepcopy(manifest)
        changed['source_files']['sdk/go/client.go']['sha256'] = 'd' * 64
        for identity in (None, release.evidence_identity(changed)):
            with self.subTest(identity=identity), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                report = {'status': 'pass', 'commit': manifest['commit']}
                if identity is not None:
                    report['release_identity'] = identity
                (root / 'reproducibility.json').write_text(json.dumps(report))
                with self.assertRaisesRegex(ValueError, 'release identity'):
                    publish.evidence(root, manifest)

    def test_matching_identity_still_requires_all_reports(self):
        manifest = manifest_fixture()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'reproducibility.json').write_text(json.dumps({
                'status': 'pass', 'commit': manifest['commit'],
                'release_identity': release.evidence_identity(manifest),
            }))
            with self.assertRaises(FileNotFoundError):
                publish.evidence(root, manifest)

    def test_each_packaging_report_is_checked_even_when_other_reports_match(self):
        manifest = manifest_fixture()
        identity = release.evidence_identity(manifest)
        base = {'status': 'pass', 'commit': manifest['commit'], 'release_identity': identity,
                'source_candidate': False, 'source_dirty': False, 'source': manifest,
                'binary_sha256': 'a' * 64, 'cli_sha256': 'b' * 64}
        reports = {
            'reproducibility.json': dict(base, packages_compared=True, unsigned_payloads=[{}, {}]),
            'documentation.json': dict(base),
        }
        for target in release.RECIPE['targets']:
            goos, arch = target.split('_')
            native = dict(base, os={'linux': 'Linux', 'darwin': 'Darwin', 'windows': 'Windows'}[goos],
                          architecture={'amd64': 'x86_64', 'arm64': 'arm64'}[arch],
                          execution_environment='native-host')
            for prefix in ('native', 'go-install', 'service'):
                reports[f'{prefix}-{target}.json'] = dict(native)
            if goos != 'windows':
                reports[f'service-user-{target}.json'] = dict(native)
        for arch in ('amd64', 'arm64'):
            for category in ('container', 'image-repro'):
                reports[f'{category}-{arch}.json'] = dict(base)
        for chart in ('helm3-to-4', 'helm4'):
            reports[f'chart-{chart}.json'] = dict(base, csi_rwop_fencing='pass')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for arch in ('amd64', 'arm64'):
                for package_format in ('deb', 'rpm'):
                    package = root / f"cpra-{manifest['version']}-linux-{arch}.{package_format}"
                    package.write_bytes(b'package fixture')
                    reports[f'package-{package_format}-{arch}.json'] = dict(
                        base, format=package_format, arch=arch, package_sha256=release.sha(package))
            for name, report in reports.items():
                (root / name).write_text(json.dumps(report))
            # Scenario and archive checks use complete fixtures in
            # test_packaging_evidence; isolate each identity check here.
            with patch.object(publish, 'archive_programs', return_value={
                'cpra': base['binary_sha256'], 'cpractl': base['cli_sha256'],
            }), patch.object(publish, 'verify_packaging_outcomes'):
                publish.evidence(root, manifest)
                for name, report in reports.items():
                    with self.subTest(report=name):
                        without_identity = dict(report)
                        without_identity.pop('release_identity')
                        (root / name).write_text(json.dumps(without_identity))
                        with self.assertRaisesRegex(ValueError, 'release identity'):
                            publish.evidence(root, manifest)
                        (root / name).write_text(json.dumps(report))

    def test_documentation_report_records_the_private_build_manifest(self):
        workflow = yaml.safe_load((release.ROOT / '.github/workflows/release.yml').read_text())
        job = workflow['jobs']['documentation']
        self.assertIn('build', job['needs'])
        downloads = [step['with'] for step in job['steps'] if step.get('uses', '').startswith('actions/download-artifact@')]
        self.assertIn({'name': 'release-metadata', 'path': 'release-metadata'}, downloads)
        uploads = [step['with'] for step in workflow['jobs']['build']['steps']
                   if step.get('uses', '').startswith('actions/upload-artifact@')]
        self.assertTrue(any(item.get('name') == 'release-metadata'
                            and item.get('path') == 'dist/release/RELEASE.json' for item in uploads))
        script = next(step['run'] for step in job['steps'] if step.get('name') == 'Verify source identity, strict build and local links')
        self.assertEqual(script.splitlines()[0],
                         'python3 scripts/docs/sync.py --site docs-site --release release-metadata/RELEASE.json --check')
        body = script.split("python3 - <<'PY'\n", 1)[1].split('\nPY', 1)[0]
        manifest = manifest_fixture()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'release-metadata').mkdir()
            (root / 'release-metadata/RELEASE.json').write_text(json.dumps(manifest))
            (root / 'evidence').mkdir()
            subprocess.run(['git', 'init', '-q', str(root / 'docs-site')], check=True)
            subprocess.run(['git', '-C', str(root / 'docs-site'), '-c', 'user.name=Test',
                            '-c', 'user.email=test@example.invalid', 'commit', '--allow-empty', '-qm', 'fixture'], check=True)
            env = dict(os.environ, PYTHONPATH=str(release.ROOT / 'scripts/release'),
                       PYTHONDONTWRITEBYTECODE='1', GITHUB_SHA=manifest['commit'])
            run = subprocess.run([sys.executable, '-B', '-c', body], cwd=root, env=env,
                                 text=True, capture_output=True, timeout=10)
            self.assertEqual(run.returncode, 0, run.stderr)
            report = root / 'evidence/documentation.json'
            recorded = json.loads(report.read_text())
            self.assertEqual(recorded['release_identity'], release.evidence_identity(manifest))
            self.assertEqual(recorded['docs_commit'], subprocess.check_output(
                ['git', '-C', str(root / 'docs-site'), 'rev-parse', 'HEAD'], text=True).strip())
            report.unlink()
            env['GITHUB_SHA'] = 'f' * 40
            mismatch = subprocess.run([sys.executable, '-B', '-c', body], cwd=root, env=env,
                                      text=True, capture_output=True, timeout=10)
            self.assertNotEqual(mismatch.returncode, 0)
            self.assertIn('different source commit', mismatch.stderr)
            self.assertFalse(report.exists())


if __name__ == '__main__':
    unittest.main()
