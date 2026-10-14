"""Execute the workflow's private-staging guard without contacting GitHub."""
import os
import json
from pathlib import Path
import re
import shlex
import subprocess
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]


class PrivateReleasePolicyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load((ROOT / '.github/workflows/release.yml').read_text())

    def test_every_module_uses_the_common_minimum_compiler(self):
        recipe = json.loads((ROOT / 'scripts/release/recipe.json').read_text())
        minimum = recipe['minimum_go_version']
        self.assertEqual(minimum, 'go1.26.0')
        pins = json.loads((ROOT / 'scripts/release/toolchains.json').read_text())
        self.assertEqual(set(pins[minimum]), set(recipe['targets']))
        for directory in ('', 'sdk/go', 'sdk/go/worker', 'examples/sdk',
                          'tools/modulezip', 'tools/sdkgen', 'internal/jobs/testdata/a2aagent'):
            with self.subTest(module=directory or '.'):
                definition = (ROOT / directory / 'go.mod').read_text()
                self.assertEqual(re.findall(r'^go (\S+)$', definition, re.MULTILINE),
                                 [minimum.removeprefix('go')])

    def test_provider_verifier_uses_frozen_inputs_and_retains_exact_build(self):
        steps = self.workflow['jobs']['build']['steps']
        build = next(step for step in steps if step.get('name') == 'Build provider verifier from frozen release inputs')
        self.assertIn('scripts/release/provider_evidence.py build', build['run'])
        self.assertIn('--manifest dist/release/RELEASE.json', build['run'])
        self.assertIn('--dependencies "$RUNNER_TEMP/cpra-dependencies"', build['run'])
        self.assertNotIn('--working-tree', build['run'])
        self.assertNotIn('-live', build['run'])
        upload = next(step for step in steps if step.get('name') == 'Retain source-bound provider executable and build evidence')
        self.assertEqual(upload['if'], 'always()')
        for suffix in ('report.json', 'cpra-verify', '*.stdout', '*.stderr'):
            self.assertIn('/cpra-provider-build/' + suffix, upload['with']['path'])
        self.assertEqual(upload['with']['name'], 'provider-verifier-build')

    def test_both_helm_release_campaigns_require_management_and_rwop(self):
        steps = self.workflow['jobs']['charts']['steps']
        campaigns = [line for step in steps for line in step.get('run', '').splitlines()
                     if line.strip().startswith('python3 scripts/packaging/live_helm.py')]
        self.assertEqual(len(campaigns), 2)
        clients, outputs = set(), set()
        for line in campaigns:
            command = shlex.split(line)
            self.assertIn('--management', command)
            self.assertNotIn('--diagnostic', command)
            self.assertEqual(command[command.index('--access-mode') + 1], 'ReadWriteOncePod')
            self.assertEqual(command[command.index('--context') + 1], 'kind-cpra-packaging-test')
            self.assertEqual(command[command.index('--storage-class') + 1], 'cpra-csi-hostpath')
            clients.add(command[command.index('--helm') + 1])
            outputs.add(command[command.index('--out') + 1])
        self.assertEqual(clients, {'bin/release-tools/helm3', 'bin/release-tools/helm4'})
        self.assertEqual(outputs, {'evidence/chart-helm3-to-4.json', 'evidence/chart-helm4.json'})

    def test_actual_guard_rejects_public_internal_unknown_and_publication(self):
        guard = self.workflow['jobs']['private-staging']['steps'][0]
        expected = {
            'CPRA_REPOSITORY_PRIVATE': '${{ github.event.repository.private }}',
            'CPRA_REPOSITORY_VISIBILITY': '${{ github.event.repository.visibility }}',
            'CPRA_PUBLISH_REQUESTED': '${{ inputs.publish }}',
        }
        self.assertEqual(guard['env'], expected)
        cases = [
            ('true', 'private', 'false', True),
            ('false', 'public', 'false', False),
            ('true', 'internal', 'false', False),
            ('true', 'public', 'false', False),
            ('false', 'private', 'false', False),
            ('true', 'private', 'true', False),
            ('true', 'private', '', False),
            ('', 'private', 'false', False),
            ('true', '', 'false', False),
            ('', '', '', False),
            ('True', 'private', 'false', False),
        ]
        for private, visibility, publish, allowed in cases:
            with self.subTest(private=private, visibility=visibility, publish=publish):
                env = dict(zip(expected, (private, visibility, publish)))
                env['PATH'] = os.defpath
                result = subprocess.run(['bash', '-euo', 'pipefail', '-c', guard['run']],
                                        env=env, capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)
                if not allowed:
                    self.assertTrue(result.stderr)

    def test_every_release_job_waits_for_the_policy(self):
        jobs = self.workflow['jobs']
        policy = jobs['private-staging']
        self.assertEqual(policy['permissions'], {})
        self.assertEqual(len(policy['steps']), 1)
        self.assertNotIn('uses', policy['steps'][0])
        self.assertNotIn('if', policy)

        def requires_policy(name, seen):
            if name == 'private-staging':
                return True
            self.assertNotIn(name, seen, 'cyclic workflow dependencies')
            job = jobs[name]
            self.assertNotIn('always()', job.get('if', ''))
            self.assertNotIn('continue-on-error', job)
            needs = job.get('needs', [])
            if isinstance(needs, str):
                needs = [needs]
            return any(requires_policy(parent, seen | {name}) for parent in needs)

        for name in jobs:
            with self.subTest(job=name):
                self.assertTrue(requires_policy(name, set()))
        # The only admitted dispatch is publish=false. This job requires true
        # and cannot bypass the failed policy through always() or dependencies.
        self.assertEqual(jobs['publish']['if'], "inputs.publish && github.ref == 'refs/heads/main'")

    def test_ci_reports_only_upload_to_private_storage(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_text())
        uploads = [step for job in workflow['jobs'].values() for step in job['steps']
                   if step.get('uses', '').startswith('actions/upload-artifact@')]
        self.assertTrue(uploads)
        for step in uploads:
            self.assertEqual(step['if'], "always() && github.event.repository.private == true && github.event.repository.visibility == 'private'")

    def test_ci_invokes_all_opt_in_cli_cases_in_both_server_builds(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_text())
        job = workflow['jobs']['go']
        self.assertEqual(set(job['strategy']['matrix']['go']), {'1.26.0', '1.27.2'})
        required = {'TestMainManagementRealCLI', 'TestMainManagementRealCLIDiff',
                    'TestMainManagementCollectionApplyCLI',
                    'TestMainManagementCollectionReselectionCLI',
                    'TestMainManagementCollectionReselectionCLILostReplies'}
        coverage = {'': set(), 'externaljobs': set()}
        for step in job['steps']:
            if step.get('env', {}).get('CPRA_RUN_CLI_INTEGRATION') != '1':
                continue
            self.assertNotIn('if', step)
            self.assertNotIn('continue-on-error', step)
            command = shlex.split(step['run'])
            self.assertEqual(command[:2], ['go', 'test'])
            self.assertIn('-race', command)
            self.assertIn('-count=1', command)
            self.assertNotIn('-short', command)
            self.assertEqual(command[-1], '.')
            pattern = command[command.index('-run') + 1]
            tags = next((argument.removeprefix('-tags=') for argument in command
                         if argument.startswith('-tags=')), '')
            self.assertIn(tags, coverage)
            coverage[tags].update(name for name in required if re.fullmatch(pattern, name))
        for tags, names in coverage.items():
            with self.subTest(tags=tags):
                self.assertEqual(names, required)

    def test_ci_executes_sustained_worker_load_on_both_compilers(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_text())
        job = workflow['jobs']['go']
        self.assertEqual(set(job['strategy']['matrix']['go']), {'1.26.0', '1.27.2'})
        steps = [step for step in job['steps']
                 if step.get('env', {}).get('CPRA_WORKER_LOAD') == '1']
        self.assertEqual(len(steps), 1)
        step = steps[0]
        self.assertNotIn('if', step)
        self.assertNotIn('continue-on-error', step)
        self.assertEqual(step['env']['CPRA_WORKER_PROCESS_RACE'], '1')
        command = shlex.split(step['run'])
        self.assertEqual(command[:2], ['go', 'test'])
        self.assertIn('-race', command)
        self.assertIn('-count=1', command)
        self.assertIn('-tags=externaljobs', command)
        self.assertNotIn('-short', command)
        self.assertEqual(command[command.index('-run') + 1], '^TestMainWorkerLoad$')
        self.assertEqual(command[-1], '.')

    def test_live_campaign_requires_private_repository_before_runner_allocation(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/live-verification.yml').read_text())
        self.assertEqual(workflow['jobs']['live']['if'],
                         "github.event.repository.private == true && github.event.repository.visibility == 'private'")

    def test_dependency_preparation_exports_cache_paths_without_checksum_exceptions(self):
        import tempfile
        self.assertEqual(self.workflow['env']['GOWORK'], 'off')
        for job in ('build', 'native', 'compatibility'):
            steps = [step for step in self.workflow['jobs'][job]['steps']
                     if 'scripts/release/dependencies.py' in step.get('run', '')]
            self.assertEqual(len(steps), 1)
            writes = [line for line in steps[0]['run'].splitlines() if '>> "$GITHUB_ENV"' in line]
            self.assertEqual(len(writes), 1)
            with self.subTest(job=job), tempfile.TemporaryDirectory() as directory:
                output = Path(directory) / 'environment'
                env = {'PATH': os.defpath, 'RUNNER_TEMP': directory, 'GITHUB_ENV': str(output)}
                subprocess.run(['bash', '-euo', 'pipefail', '-c', writes[0]], env=env, check=True, timeout=5)
                self.assertEqual(output.read_text().splitlines(), [
                    'GOMODCACHE=' + directory + '/cpra-dependencies/modules',
                    'GOPATH=' + directory + '/cpra-dependencies/go-path'])

    def test_compatibility_prepares_dependencies_before_testing_each_module(self):
        import tempfile
        job = self.workflow['jobs']['compatibility']
        self.assertIn('build', job['needs'])
        steps = job['steps']
        preparation = next(index for index, step in enumerate(steps)
                           if 'scripts/release/dependencies.py' in step.get('run', ''))
        compiler = next(index for index, step in enumerate(steps)
                        if 'install_go.py --version "${{ matrix.compiler }}"' in step.get('run', ''))
        self.assertLess(preparation, compiler)
        campaigns = [(index, step) for index, step in enumerate(steps)
                     if 'scripts/release/build_campaign.py' in step.get('run', '')]
        self.assertEqual(len(campaigns), 1)
        index, campaign = campaigns[0]
        self.assertGreater(index, compiler)
        self.assertNotIn('GOWORK=', campaign['run'])
        self.assertNotIn('if', campaign)
        self.assertNotIn('continue-on-error', campaign)
        self.assertEqual(campaign.get('working-directory', '.'), '.')
        self.assertEqual(job['strategy']['matrix']['compiler'], ['go1.26.0', 'go1.27.2'])
        # Execute the workflow wrapper with a recording interpreter. This checks
        # frozen source/cache binding and shell quoting, not Go test execution.
        # test_build_evidence covers each module/tag selection and rejects
        # incomplete or narrowed matrix results.
        with tempfile.TemporaryDirectory(prefix='cpra matrix ') as directory:
            root = Path(directory)
            tools = root / 'tools'; tools.mkdir()
            calls = root / 'called.json'
            python = tools / 'python3'
            python.write_text('#!/usr/bin/python3\nimport json, os, sys\n'
                              'with open(os.environ["CPRA_TEST_CALLS"], "w") as out:\n'
                              '    json.dump({"arguments": sys.argv[1:], "workspace": os.environ["GOWORK"]}, out)\n')
            python.chmod(0o755)
            go = tools / 'go'; go.write_text('#!/bin/sh\nexit 99\n'); go.chmod(0o755)
            env = {'PATH': str(tools) + ':' + os.defpath, 'RUNNER_TEMP': directory,
                   'CPRA_TEST_CALLS': str(calls), 'GOWORK': self.workflow['env']['GOWORK']}
            for selected in job['strategy']['matrix']['compiler']:
                with self.subTest(compiler=selected):
                    calls.unlink(missing_ok=True)
                    command = campaign['run'].replace('${{ matrix.compiler }}', selected)
                    result = subprocess.run(['bash', '-euo', 'pipefail', '-c', command], env=env,
                                            capture_output=True, text=True, timeout=5)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(json.loads(calls.read_text()), {
                        'workspace': 'off',
                        'arguments': ['-B', 'scripts/release/build_campaign.py',
                                      '--release', str(root / 'source-assets/RELEASE.json'),
                                      '--source-dir', str(root / 'cpra-dependencies/source'),
                                      '--dependencies', str(root / 'cpra-dependencies'),
                                      '--go', str(go), '--security',
                                      '--govulncheck', str(root / 'cpra-security-tools/govulncheck'),
                                      '--a2a-reference-go', str(root / 'cpra-bootstrap-go/bin/go'),
                                      '--out', str(root / ('build-evidence/build-' + selected + '.json'))]})
        self.assertEqual(self.workflow['env']['GOSUMDB'], 'sum.golang.org')
        self.assertEqual(self.workflow['env']['GOPROXY'], 'https://proxy.golang.org')
        self.assertEqual(self.workflow['env']['GONOSUMDB'], '')

    def test_private_consumers_select_prepared_versions_and_keep_payloads(self):
        import tempfile
        steps = self.workflow['jobs']['compatibility']['steps']
        consumer = next(step for step in steps if step.get('name') == 'Consume exact private SDK and worker module versions')
        upload = next(step for step in steps if step.get('name') == 'Retain private consumer reports and module payloads')
        self.assertEqual(upload['with']['path'], '${{ runner.temp }}/sdk-consumers/')
        self.assertEqual(upload['with']['name'], 'sdk-consumer-${{ matrix.compiler }}')
        downloads = [step['with'] for step in steps if step.get('uses', '').startswith('actions/download-artifact@')]
        self.assertTrue(any(row['path'] == '${{ runner.temp }}/source-assets' for row in downloads))
        with tempfile.TemporaryDirectory(prefix='cpra consumer ') as directory:
            root = Path(directory)
            (root / 'cpra-dependencies').mkdir()
            (root / 'cpra-dependencies/dependencies.json').write_text(json.dumps({
                'private_modules': {'github.com/ziad-hsn/cpra/sdk/go': {'Version': 'v0.7.0-rc.2'}}}))
            tools = root / 'tools'; tools.mkdir()
            calls = root / 'called.json'
            python = tools / 'python3'
            python.write_text('#!/usr/bin/python3\nimport json, os, sys\n'
                              'if sys.argv[1] == "-c":\n'
                              '    os.execv(sys.executable, [sys.executable, *sys.argv[1:]])\n'
                              'with open(os.environ["CPRA_TEST_CALLS"], "w") as out:\n'
                              '    json.dump(sys.argv[1:], out)\n')
            python.chmod(0o755)
            go = tools / 'go'; go.write_text('#!/bin/sh\nexit 99\n'); go.chmod(0o755)
            env = {'PATH': str(tools) + ':' + os.defpath, 'RUNNER_TEMP': directory,
                   'WORKER_VERSION': 'v0.8.0-rc.3', 'CPRA_TEST_CALLS': str(calls)}
            command = consumer['run'].replace('${{ matrix.compiler }}', 'go1.26.0')
            result = subprocess.run(['bash', '-euo', 'pipefail', '-c', command], env=env,
                                    capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(calls.read_text()), [
                'scripts/sdk/verify.py', '--go', str(go), '--sdk-version', 'v0.7.0-rc.2',
                '--worker-version', 'v0.8.0-rc.3', '--out',
                str(root / 'sdk-consumers/consumer-go1.26.0.json')])
            calls.unlink()
            (root / 'cpra-dependencies/dependencies.json').write_text('{}')
            failed = subprocess.run(['bash', '-euo', 'pipefail', '-c', command], env=env,
                                    capture_output=True, text=True, timeout=5)
            self.assertNotEqual(failed.returncode, 0)
            self.assertFalse(calls.exists())

    def test_sdk_reader_requires_both_private_compilers_and_retained_cache_files(self):
        job = self.workflow['jobs']['sdk-consumer-evidence']
        self.assertEqual(set(job['needs']), {'private-staging', 'build', 'compatibility'})
        downloads = {step['with']['name'] for step in job['steps']
                     if step.get('uses', '').startswith('actions/download-artifact@')}
        self.assertEqual(downloads, {'release-metadata', 'dependency-preparation-build',
                                     'sdk-consumer-go1.26.0', 'sdk-consumer-go1.27.2'})
        reader = next(step['run'] for step in job['steps'] if 'sdk_evidence.py' in step.get('run', ''))
        self.assertEqual(reader.count('--consumer '), 2)
        self.assertIn('/consumer-go1.26.0.json', reader)
        self.assertIn('/consumer-go1.27.2.json', reader)
        self.assertIn('--release ', reader)
        self.assertIn('--dependencies ', reader)
        upload = next(step['with'] for step in self.workflow['jobs']['build']['steps']
                      if step.get('with', {}).get('name') == 'dependency-preparation-build')
        for module in ('sdk/go', 'sdk/go/worker'):
            for suffix in ('.info', '.mod', '.zip'):
                self.assertIn('/modules/cache/download/github.com/ziad-hsn/cpra/' + module + '/@v/*' + suffix,
                              upload['path'])
        ci = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_text())
        consumer_upload = next(step['with'] for step in ci['jobs']['sdk']['steps']
                               if step.get('with', {}).get('name') == 'sdk-consumer-${{ matrix.go }}')
        self.assertIn('evidence/local/sdk-consumer.json.modules/', consumer_upload['path'])

    def test_private_worker_job_preserves_original_evidence_and_never_enables_endurance(self):
        job = self.workflow['jobs']['worker-evidence']
        self.assertEqual(set(job['needs']), {'private-staging', 'build'})
        self.assertEqual(job['strategy']['matrix']['compiler'], ['go1.26.0', 'go1.27.2'])
        commands = '\n'.join(step.get('run', '') for step in job['steps'])
        self.assertIn('--source-dir "$RUNNER_TEMP/cpra-worker-dependencies/source"', commands)
        self.assertIn('--release "$RUNNER_TEMP/worker-source/RELEASE.json"', commands)
        self.assertNotIn('--release "$RUNNER_TEMP/cpra-worker-dependencies/source/RELEASE.json"', commands)
        self.assertNotIn('scripts/benchmark/', commands)
        self.assertNotIn('--mode soak', commands)
        self.assertNotIn('GOWORK=', commands)
        retained = next(step for step in job['steps'] if step.get('uses', '').startswith('actions/upload-artifact@'))
        self.assertEqual(retained['if'], 'always()')
        self.assertEqual(retained['with']['path'], '${{ runner.temp }}/worker-evidence/')
        self.assertEqual(retained['with']['if-no-files-found'], 'error')
        self.assertIn('worker-evidence', self.workflow['jobs']['private-assessment']['needs'])

    def test_container_evidence_keeps_transports_even_when_the_campaign_fails(self):
        job = self.workflow['jobs']['containers']
        self.assertEqual({row['arch'] for row in job['strategy']['matrix']['include']}, {'amd64', 'arm64'})
        step = next(row for row in job['steps']
                    if 'scripts/packaging/image_repro.py' in row.get('run', ''))
        self.assertIn('--out "evidence/image-repro-$TARGET_ARCH.json"', step['run'])
        upload = next(row for row in job['steps']
                      if row.get('with', {}).get('name') == 'container-evidence-${{ matrix.arch }}')
        self.assertEqual(upload['if'], 'always()')
        self.assertEqual(set(upload['with']['path'].splitlines()),
                         {'evidence/*.json', 'evidence/*.log', 'evidence/*.tar'})
        self.assertEqual(upload['with']['if-no-files-found'], 'error')


if __name__ == '__main__':
    unittest.main()
