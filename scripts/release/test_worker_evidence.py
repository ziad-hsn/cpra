"""Worker reader/recorder regressions; synthetic cases are not release evidence."""
import argparse
import base64
import copy
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
import zipfile
from unittest.mock import Mock, patch

import release
import worker_campaign
import worker_evidence as worker
import test_sdk_evidence as sdk_fixtures


def measurement():
    return {'duration_seconds': 300.001, 'restart_seconds': 4.25,
            'monitors': 64, 'limits': dict(worker.LIMITS),
            'target_invocations': {'check': 1440, 'recovery': 360, 'notification': 400},
            'target_peak_handlers': 16, 'status_samples': 300, 'saturated_samples': 280,
            'duplicate_invocations': 0, 'unreconciled_outcomes': 0, 'expired_check_receipts': 100,
            'expired_check_finalizations': 1,
            'public_configuration_qualified': False, 'release_qualified': False,
            'windows': [{'elapsed_seconds': 30 * index, 'active_handlers': 16,
                         'invocations': {'check': 144 * index, 'recovery': 36 * index, 'notification': 40 * index}}
                        for index in range(1, 10)],
            'drained': {'Records': 0, 'ActiveHandlers': 0, 'PendingOutcomes': 0, 'UnknownActions': 0,
                        'LiveBytes': 0, 'ReservedBytes': 0, 'AllocatedBytes': 131072,
                        'AdmissionAvailable': True, 'AdmissionCapacity': 16}}


def event(action, name=None, **fields):
    return dict(Action=action, Package=worker.PACKAGE, **({'Test': name} if name is not None else {}), **fields)


def raw_events(compiler, race):
    events = [event('output', name, Output=f'SDK worker helper uses {compiler}; race instrumentation: {str(race).lower()}\n')
              for name in worker.TESTS]
    events.append(event('output', 'TestMainWorkerLoad', Output='WORKER_LOAD_EVIDENCE ' + json.dumps(measurement()) + '\n'))
    events.extend(event('pass', name, Elapsed=1) for name in sorted(worker.process_cases()))
    events.extend(event('pass', name, Elapsed=310 if name == 'TestMainWorkerLoad' else 60) for name in worker.TESTS)
    events.append(event('pass', Elapsed=370))
    return events


class WorkerEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.fixture = sdk_fixtures.SDKEvidenceTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.root, self.manifest = self.fixture.root, self.fixture.manifest
        required = worker.EXACT | {'main_worker_process_test.go', 'main_worker_capacity_test.go', 'main_worker_load_test.go',
                                  'sdk/go/worker/server_process_test.go', 'scripts/release/worker_campaign.py',
                                  'scripts/release/worker_evidence.py'}
        for name in required:
            self.manifest['source_files'].setdefault(name, {'mode': '100644', 'sha256': '1' * 64})
        self.paths, self.reports, self.events = [], [], []
        self.sdk = self.fixture.dependency['private_modules'][worker.sdk_evidence.CORE]
        for compiler in (worker.MINIMUM_COMPILER, release.RECIPE['go_version']):
            race = compiler != worker.MINIMUM_COMPILER
            path = self.root / ('worker-' + compiler + '.json')
            directory = path.with_name(path.stem + '.artifacts')
            directory.mkdir()
            source = directory / 'source.json'
            source.write_bytes(release.encoded(worker.source_inputs(self.manifest)))
            for name, suffix in (('sdk.zip', '.zip'), ('sdk.mod', '.mod'), ('sdk.info', '.info')):
                original = self.fixture.cache / 'modules/cache/download' / self.sdk['Path'] / '@v' / (self.sdk['Version'] + suffix)
                (directory / name).write_bytes(original.read_bytes())
            graphs = {}
            for role, module in (('application', worker.PACKAGE), ('worker', worker.sdk_evidence.WORKER)):
                graph = [{'Path': module, 'Main': True}, {name: self.sdk[name] for name in ('Path', 'Version', 'Sum', 'GoModSum')}]
                graphs[role] = graph
                (directory / (role + '-modules.jsonl')).write_text('\n'.join(json.dumps(row) for row in graph))
                (directory / (role + '-verify.log')).write_text('all modules verified\n')
            self.paths.append(path)
            self.events.append(raw_events(compiler, race))
            self.reports.append({'schema_version': 1, 'result': 'passed', 'release_qualified': False,
                'release_identity': release.evidence_identity(self.manifest), 'exit_code': 0,
                'go': compiler, 'compiler_sha256': '2' * 64, 'platform': 'linux/amd64', 'race': race,
                'host': {'system': 'Linux', 'machine': 'x86_64', 'os_version': 'controlled Linux fixture'},
                'workspace': 'off', 'required_tests': list(worker.TESTS), 'source_changed': [],
                'recorded_at': '2026-10-01T10:00:00+00:00', 'seconds': 400,
                'command': worker.command('/fixture/go', race),
                'environment': dict(worker_campaign.POLICY, CPRA_WORKER_PROCESS_RACE='1' if race else '0'),
                'source_inventory': source.relative_to(self.root).as_posix(),
                'source_inventory_sha256': release.sha(source), 'load': measurement(),
                'sdk_module': {name: self.sdk[name] for name in ('Path', 'Version', 'Sum', 'GoModSum', 'ArchiveSHA256')},
                'module_inputs': {name: worker_campaign.record(directory / name) for name in (
                    'sdk.zip', 'sdk.mod', 'sdk.info', 'application-modules.jsonl', 'worker-modules.jsonl',
                    'application-verify.log', 'worker-verify.log')}})
        self.save()

    def save(self):
        for path, report, events in zip(self.paths, self.reports, self.events):
            directory = path.with_name(path.stem + '.artifacts')
            log, errors = directory / 'go.jsonl', directory / 'stderr.log'
            log.write_bytes(b''.join((json.dumps(row) + '\n').encode() for row in events))
            errors.write_bytes(b'')
            report['logs'] = {name.relative_to(self.root).as_posix(): worker_campaign.record(name) for name in (log, errors)}
            path.write_bytes(release.encoded(report))

    def verify(self):
        return worker.verify_worker_campaigns(self.manifest, self.paths)

    def test_complete_raw_coverage_is_consistent_but_never_publication_authority(self):
        with patch('subprocess.Popen', side_effect=AssertionError('reader must not execute work')):
            result = self.verify()
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])
        self.assertEqual(len(worker.process_cases()), 37)
        for report in result['reports']:
            self.assertEqual(len(report['process_cases']), 37)
            self.assertEqual(report['load']['duplicate_invocations'], 0)
            self.assertIn(report['report'], result['input_sha256'])
        self.assertEqual(len(result['input_sha256']), 22)

    def test_split_and_interleaved_output_preserves_each_test_stream(self):
        for index, original in enumerate(self.events):
            streams = {}
            for row in original:
                if row['Action'] == 'output':
                    streams.setdefault(row['Test'], []).append(row['Output'])
            chunks = {}
            for name, values in streams.items():
                text = ''.join(values)
                chunks[name] = [text[position:position + 67] for position in range(0, len(text), 67)]
            fragmented = []
            for position in range(max(map(len, chunks.values()))):
                for name, values in chunks.items():
                    if position < len(values):
                        fragmented.append(event('output', name, Output=values[position]))
            self.events[index] = fragmented + [row for row in original if row['Action'] != 'output']
        self.save()
        result = self.verify()
        self.assertEqual(result['status'], 'consistent_pending_review')
        for report in result['reports']:
            self.assertEqual(report['load'], measurement())
        self.assertFalse(result['release_gate_passed'])

    def test_complete_test_events_cannot_hide_truncated_output_payload(self):
        observation = self.events[0][2]
        text = observation['Output'].rstrip()[:-1]
        self.events[0][2:3] = [event('output', observation['Test'], Output=text[:57]),
                              event('output', observation['Test'], Output=text[57:])]
        self.save()
        with self.assertRaises(ValueError):
            self.verify()

    def test_fragmented_output_cannot_exceed_the_assembled_line_limit(self):
        text = 'x' * (worker.MAX_LINE_BYTES + 1)
        self.events[0][:0] = [event('output', worker.TESTS[0], Output=text[position:position + 1024])
                             for position in range(0, len(text), 1024)]
        self.save()
        with self.assertRaisesRegex(ValueError, 'assembled Go output line exceeds'):
            self.verify()

    def test_frozen_source_requires_all_worker_inputs_and_regular_files(self):
        for change in (lambda m: m.update(dirty=True), lambda m: m.update(candidate=True),
                       lambda m: m['source_files'].pop('main_worker_process_test.go'),
                       lambda m: m['source_files']['main_worker_process_test.go'].update(mode='120000')):
            with self.subTest(change=change):
                original = copy.deepcopy(self.manifest)
                try:
                    change(self.manifest)
                    with self.assertRaises(ValueError): self.verify()
                finally: self.manifest = original

    def test_duplicate_compiler_missing_compiler_and_unsupported_platform_are_rejected(self):
        for paths in (self.paths[:1], [self.paths[0], self.paths[0]]):
            with self.subTest(paths=paths), self.assertRaises(ValueError):
                worker.verify_worker_campaigns(self.manifest, paths)
        self.reports[1]['go'] = self.reports[0]['go']; self.save()
        with self.assertRaises(ValueError): self.verify()

    def test_workspace_ambient_overrides_unstamped_source_or_exit_mismatch_cannot_pass(self):
        changes = [{'workspace': '/unreviewed/go.work'}, {'source_changed': ['go.mod']}, {'exit_code': True},
                   {'exit_code': 1}, {'release_qualified': True}, {'schema_version': True}, {'race': False},
                   {'platform': 'linux/arm64'}, {'recorded_at': '2026-10-01T10:00:00'}, {'seconds': 299},
                   {'host': {'system': 'Darwin', 'machine': 'x86_64', 'os_version': 'Darwin fixture'}},
                   {'compiler_sha256': ''}, {'source_inventory_sha256': None}, {'required_tests': []},
                   {'environment': dict(self.reports[1]['environment'], GOSUMDB='off')},
                   {'environment': dict(self.reports[1]['environment'], GOFLAGS='-mod=readonly')},
                   {'command': worker.command('/fixture/go', True) + ['-short']},
                   {'load': dict(measurement(), duplicate_invocations=5)}]
        original = copy.deepcopy(self.reports[1])
        for change in changes:
            with self.subTest(change=change):
                self.reports[1] = dict(copy.deepcopy(original), **change); self.save()
                with self.assertRaises(ValueError): self.verify()
        self.reports[1] = original

    def test_missing_case_parent_package_and_skipped_or_failed_test_are_rejected(self):
        original = copy.deepcopy(self.events[0])
        names = [next(iter(worker.process_cases())), *worker.TESTS, None]
        for name in names:
            with self.subTest(missing=name):
                self.events[0] = [row for row in original if not (row['Action'] == 'pass' and row.get('Test') == name)]
                self.save()
                with self.assertRaises(ValueError): self.verify()
        for action in ('skip', 'fail', 'build-fail'):
            with self.subTest(action=action):
                self.events[0] = copy.deepcopy(original)
                self.events[0][-1]['Action'] = action; self.save()
                with self.assertRaises(ValueError): self.verify()

    def test_helper_instrumentation_and_compiler_must_match_in_both_processes(self):
        original = copy.deepcopy(self.events[1])
        for change in ('missing', 'duplicate', 'compiler', 'race'):
            with self.subTest(change=change):
                rows = copy.deepcopy(original)
                if change == 'missing': rows.pop(0)
                elif change == 'duplicate': rows.insert(0, copy.deepcopy(rows[0]))
                elif change == 'compiler': rows[0]['Output'] = rows[0]['Output'].replace(release.RECIPE['go_version'], worker.MINIMUM_COMPILER)
                else: rows[0]['Output'] = rows[0]['Output'].replace('true', 'false')
                self.events[1] = rows; self.save()
                with self.assertRaises(ValueError): self.verify()

    def test_raw_duplicate_events_unexpected_coverage_and_measurement_are_rejected(self):
        original = copy.deepcopy(self.events[0])
        for extra in (original[-1], original[2], event('pass', 'TestMainWorkerLoadSmoke', Elapsed=300),
                      event('pass', 'TestUnrelated', Elapsed=1),
                      dict(original[-1], Package='another/module')):
            with self.subTest(extra=extra):
                self.events[0] = copy.deepcopy(original) + [extra]; self.save()
                with self.assertRaises(ValueError): self.verify()

    def test_duration_and_measurement_must_agree_with_raw_completion(self):
        self.events[0][-2]['Elapsed'] = 299; self.save()
        with self.assertRaises(ValueError): self.verify()

    def test_restart_measurement_must_fit_within_raw_completion(self):
        self.events[0][-2]['Elapsed'] = 302; self.save()
        with self.assertRaisesRegex(ValueError, 'measured duration'):
            self.verify()

    def test_source_inventory_added_removed_modified_or_missing_inputs_are_rejected(self):
        path = self.root / self.reports[0]['source_inventory']
        original = json.loads(path.read_text())
        for change in (lambda value: value.pop('main.go'),
                       lambda value: value.update({'unexpected.go': {'mode': '100644', 'sha256': '3' * 64}}),
                       lambda value: value['main.go'].update(sha256='3' * 64),
                       lambda value: value['main.go'].update(mode='100755')):
            with self.subTest(change=change):
                current = copy.deepcopy(original); change(current)
                path.write_bytes(release.encoded(current))
                self.reports[0]['source_inventory_sha256'] = release.sha(path); self.save()
                with self.assertRaises(ValueError): self.verify()

    def test_changed_raw_bytes_and_symlinked_inputs_cannot_pass(self):
        directory = self.paths[0].with_name(self.paths[0].stem + '.artifacts')
        log = directory / 'go.jsonl'
        log.write_bytes(log.read_bytes() + b'\n')
        with self.assertRaises(ValueError): self.verify()
        self.save()
        actual = directory / 'actual.log'; log.rename(actual); log.symlink_to(actual.name)
        with self.assertRaises(ValueError): self.verify()

    def test_truncated_duplicate_json_and_nonfinite_events_are_rejected(self):
        directory = self.paths[0].with_name(self.paths[0].stem + '.artifacts')
        log = directory / 'go.jsonl'
        for data in (b'{"Action":', b'{"Action":"pass","Action":"fail"}\n',
                     b'{"Action":"pass","Package":"github.com/ziad-hsn/cpra","Elapsed":NaN}\n'):
            with self.subTest(data=data):
                log.write_bytes(data)
                self.reports[0]['logs'][log.relative_to(self.root).as_posix()] = worker_campaign.record(log)
                self.paths[0].write_bytes(release.encoded(self.reports[0]))
                with self.assertRaises(ValueError): self.verify()

    def test_unexpected_oversized_or_unavailable_logs_are_rejected(self):
        original = copy.deepcopy(self.reports[0])
        for change in ({'logs': {}}, {'source_inventory': '../outside.json'},
                       {'source_inventory': '/absolute/source.json'},
                       {'source_inventory': 'absent/source.json'}):
            with self.subTest(change=change):
                self.reports[0] = dict(copy.deepcopy(original), **change)
                self.paths[0].write_bytes(release.encoded(self.reports[0]))
                with self.assertRaises((ValueError, OSError)): self.verify()
        self.reports[0] = original; self.save()
        with patch.object(worker, 'MAX_LINE_BYTES', 16), self.assertRaises(ValueError): self.verify()

    def test_unverified_replaced_and_different_sdk_dependencies_are_rejected(self):
        directory = self.paths[0].with_name(self.paths[0].stem + '.artifacts')
        graph_path = directory / 'worker-modules.jsonl'
        original = graph_path.read_text()
        rows = worker.module_records(original)
        mutations = [lambda value: value[1].update(Version='v0.9.0'),
                     lambda value: value[1].update(Sum='h1:wrong'),
                     lambda value: value[1].update(Replace=None),
                     lambda value: value.append(copy.deepcopy(value[1])),
                     lambda value: value[0].update(Main=False),
                     lambda value: value.pop()]
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                current = copy.deepcopy(rows); mutate(current)
                graph_path.write_text('\n'.join(json.dumps(row) for row in current))
                self.reports[0]['module_inputs'][graph_path.name] = worker_campaign.record(graph_path)
                self.save()
                with self.assertRaises(ValueError): self.verify()
        graph_path.write_text(original)
        self.reports[0]['module_inputs'][graph_path.name] = worker_campaign.record(graph_path)
        verify = directory / 'application-verify.log'
        verify.write_text('not checked')
        self.reports[0]['module_inputs'][verify.name] = worker_campaign.record(verify)
        self.save()
        with self.assertRaises(ValueError): self.verify()

    def test_sdk_archive_bytes_match_frozen_source_and_not_just_the_recorded_hash(self):
        directory = self.paths[0].with_name(self.paths[0].stem + '.artifacts')
        archive = directory / 'sdk.zip'
        archive.write_bytes(b'not a valid SDK module')
        self.reports[0]['module_inputs']['sdk.zip'] = worker_campaign.record(archive)
        self.reports[0]['sdk_module']['ArchiveSHA256'] = release.sha(archive); self.save()
        with self.assertRaises((ValueError, zipfile.BadZipFile)): self.verify()

    def test_compilers_cannot_claim_different_sdk_content_identities(self):
        directory = self.paths[1].with_name(self.paths[1].stem + '.artifacts')
        changed = 'h1:' + base64.b64encode(b'x' * 32).decode()
        self.reports[1]['sdk_module']['Sum'] = changed
        for role in ('application', 'worker'):
            path = directory / (role + '-modules.jsonl')
            rows = worker.module_records(path.read_text()); rows[1]['Sum'] = changed
            path.write_text('\n'.join(json.dumps(row) for row in rows))
            self.reports[1]['module_inputs'][path.name] = worker_campaign.record(path)
        self.save()
        with self.assertRaisesRegex(ValueError, 'between compiler campaigns'): self.verify()

    def test_changed_during_inspection_is_rejected(self):
        original = worker.browser_evidence.hash_file
        count = {}
        def change(path, limit):
            count[str(path)] = count.get(str(path), 0) + 1
            if path == self.paths[0] and count[str(path)] == 2:
                path.write_bytes(path.read_bytes() + b' ')
            return original(path, limit)
        with patch.object(worker.browser_evidence, 'hash_file', side_effect=change), self.assertRaises(ValueError):
            self.verify()

    def test_recorder_produces_reader_compatible_reports_from_original_captured_inputs(self):
        self.fixture.dependency['release_identity'] = release.evidence_identity(self.manifest)
        self.fixture.save()
        manifest = self.root / 'RELEASE.json'; manifest.write_bytes(release.encoded(self.manifest))
        go = self.root / 'fixture-go'; go.write_bytes(b'controlled compiler identity fixture'); go.chmod(0o755)
        outputs = []
        for index, compiler in enumerate((worker.MINIMUM_COMPILER, release.RECIPE['go_version'])):
            output = self.root / 'recorded' / ('worker-' + compiler + '.json')
            args = argparse.Namespace(out=output, source_dir=self.root / 'source', dependencies=self.fixture.cache,
                                      release=manifest, go=str(go))
            def capture(command, directory, environment, out, errors, timeout):
                self.assertEqual(environment['GOWORK'], 'off')
                self.assertEqual(environment['PATH'].split(os.pathsep)[0], str(go.parent))
                self.assertEqual(environment['GOPROXY'], 'off')
                self.assertEqual(environment['GOSUMDB'], 'sum.golang.org')
                self.assertEqual(environment['GOFLAGS'], '-mod=readonly -pgo=off -trimpath -buildvcs=false -p=2')
                if command[1:3] == ['list', '-m']:
                    role = 'worker' if directory.name == 'worker' else 'application'
                    original = self.paths[index].with_name(self.paths[index].stem + '.artifacts') / (role + '-modules.jsonl')
                    out.write_bytes(original.read_bytes())
                elif command[1:3] == ['mod', 'verify']:
                    out.write_bytes(b'all modules verified\n')
                else:
                    original = self.paths[index].with_name(self.paths[index].stem + '.artifacts') / 'go.jsonl'
                    out.write_bytes(original.read_bytes())
                errors.write_bytes(b'')
                return 0
            with patch.object(worker, 'source_snapshot', return_value=worker.source_inputs(self.manifest)), \
                 patch.object(worker_campaign.subprocess, 'check_output', return_value=json.dumps(
                     {'GOVERSION': compiler, 'GOOS': 'linux', 'GOARCH': 'amd64'})), \
                 patch.object(worker_campaign, 'capture', side_effect=capture), \
                 patch.object(worker_campaign.time, 'monotonic', side_effect=[0, 1, 400]):
                result = worker_campaign.campaign(args)
            self.assertEqual(result['result'], 'passed')
            self.assertFalse(result['release_qualified'])
            outputs.append(output)
        result = worker.verify_worker_campaigns(self.manifest, outputs)
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])


class WorkerAccountingTests(unittest.TestCase):
    def test_restart_measurement_is_required_and_finite_positive(self):
        value = measurement()
        value.pop('restart_seconds')
        with self.assertRaisesRegex(ValueError, 'restart'):
            worker.load_measurement(value)
        for invalid in (None, False, True, '4.25', 0, -1, float('nan'), float('inf'), -float('inf')):
            with self.subTest(restart_seconds=invalid):
                value = measurement()
                value['restart_seconds'] = invalid
                with self.assertRaisesRegex(ValueError, 'restart'):
                    worker.load_measurement(value)
        for valid in (1, 4.25):
            with self.subTest(restart_seconds=valid):
                value = measurement()
                value['restart_seconds'] = valid
                self.assertEqual(worker.load_measurement(value), value)

    def test_real_duration_capacity_saturation_progress_and_drain_are_required(self):
        changes = [lambda x: x.update(duration_seconds=299), lambda x: x.update(duration_seconds=float('nan')),
                   lambda x: x.update(monitors=True), lambda x: x.update(duplicate_invocations=1),
                   lambda x: x.update(unreconciled_outcomes=1), lambda x: x.update(status_samples=0),
                   lambda x: x.update(saturated_samples=0), lambda x: x.update(target_peak_handlers=15),
                   lambda x: x['limits'].update(Concurrency=15), lambda x: x['drained'].update(Records=1),
                   lambda x: x['drained'].update(UnknownActions=1), lambda x: x['drained'].update(LiveBytes=1),
                   lambda x: x['drained'].update(ReservedBytes=1), lambda x: x.update(windows=[]),
                   lambda x: x['windows'][1]['invocations'].update(recovery=0),
                   lambda x: x['windows'][1].update(elapsed_seconds=90),
                   lambda x: x.update(expired_check_receipts=100000),
                   lambda x: x.pop('expired_check_finalizations'),
                   lambda x: x.update(expired_check_finalizations=-1),
                   lambda x: x.update(expired_check_finalizations=100000),
                   lambda x: x.update(release_qualified=True)]
        for mutate in changes:
            with self.subTest(mutate=mutate):
                value = measurement(); mutate(value)
                with self.assertRaises(ValueError): worker.load_measurement(value)

    def test_source_snapshot_detects_added_executable_changed_and_linked_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for prefix in worker.PREFIXES: (root / prefix).mkdir(parents=True, exist_ok=True)
            for name in worker.EXACT:
                path = root / name; path.parent.mkdir(parents=True, exist_ok=True); path.write_bytes(b'fixture')
            (root / 'main.go').write_bytes(b'package fixture')
            original = worker.source_snapshot(root)
            (root / 'extra.go').write_bytes(b'package fixture')
            self.assertIn('extra.go', worker.source_snapshot(root))
            (root / 'extra.go').unlink()
            (root / 'main.go').chmod(0o755)
            self.assertNotEqual(worker.source_snapshot(root), original)
            (root / 'main.go').unlink(); (root / 'main.go').symlink_to('go.mod')
            with self.assertRaises(ValueError): worker.source_snapshot(root)

    def test_module_parser_rejects_duplicate_keys_errors_replacements_and_truncated_data(self):
        for data in ('{"Path":"one","Path":"two"}', '{"Replace":null}', '{"Error":null}',
                     '{"Value":NaN}', '{"Value":1e999}', '[{}]', '{"Path":'):
            with self.subTest(data=data), self.assertRaises(ValueError): worker.module_records(data)

    def test_recorder_bounds_output_and_kills_its_process_group_on_deadline(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with self.assertRaises(TimeoutError):
                worker_campaign.capture([sys.executable, '-c', 'import time; time.sleep(30)'], root, {},
                                        root / 'go.log', root / 'errors.log', 0.1)
            with patch.object(worker, 'MAX_LOG_BYTES', 100), self.assertRaises(ValueError):
                worker_campaign.capture([sys.executable, '-c', 'print("x" * 1000)'], root, {},
                                        root / 'large.log', root / 'large-errors.log', 5)

    def test_recorder_deadline_also_stops_descendant_fixture_processes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            stopped = root / 'child-stopped'
            child = ('import signal,sys,time; from pathlib import Path; '
                     'signal.signal(signal.SIGTERM, lambda *_: (Path(sys.argv[1]).write_text("stopped"), sys.exit(0))); '
                     'time.sleep(30)')
            parent = ('import subprocess,sys,time; '
                      'subprocess.Popen([sys.executable,"-c",sys.argv[1],sys.argv[2]]); time.sleep(30)')
            with self.assertRaises(TimeoutError):
                worker_campaign.capture([sys.executable, '-c', parent, child, str(stopped)], root, {},
                                        root / 'group.log', root / 'group-errors.log', 1)
            self.assertEqual(stopped.read_text(), 'stopped')

    def test_recorder_refuses_stale_source_before_any_compiler_execution(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            output = root / 'output/worker.json'
            args = argparse.Namespace(out=output, source_dir=root / 'source', dependencies=root / 'dependencies',
                                      release=root / 'RELEASE.json', go='/unneeded/go')
            args.release.write_bytes(release.encoded({'fixture': True}))
            with patch.object(worker_campaign, 'evidence_identity', return_value={}), \
                 patch.object(worker, 'source_inputs', return_value={'main.go': {}}), \
                 patch.object(worker, 'source_snapshot', return_value={}), \
                 patch.object(worker_campaign.subprocess, 'check_output', side_effect=AssertionError('compiler invoked')):
                with self.assertRaisesRegex(ValueError, 'source differs'): worker_campaign.campaign(args)
            self.assertFalse(output.exists())

    def test_recorder_preserves_existing_or_aliased_outputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / 'source'; source.mkdir()
            output = root / 'worker.json'; output.write_bytes(b'existing evidence')
            args = argparse.Namespace(out=output, source_dir=source, dependencies=root / 'dependencies',
                                      release=root / 'RELEASE.json', go='/unneeded/go')
            with patch.object(worker_campaign, 'read_report', side_effect=AssertionError('input consumed')):
                with self.assertRaises(ValueError): worker_campaign.campaign(args)
            self.assertEqual(output.read_bytes(), b'existing evidence')
            args.out = source / 'new/output.json'
            with self.assertRaises(ValueError): worker_campaign.campaign(args)
            self.assertFalse(args.out.parent.exists())
