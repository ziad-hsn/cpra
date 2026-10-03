"""Synthetic report-reader contracts, never measured CPRa performance evidence."""
import copy
from contextlib import contextmanager
import gzip
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import prepare
import verify
from test_evidence import manifest_fixture


COMMIT = 'a' * 40
SEED = 20260910


def write_json(path, value):
    path.write_text(json.dumps(value, sort_keys=True) + '\n')


def source_archive(root, name, commit):
    files = {'go.mod': b'module example.invalid/report-fixture\n',
             'main.go': b'// Not a CPRa build or a benchmark execution.\n',
             'sdk/go/go.mod': b'module github.com/ziad-hsn/cpra/sdk/go\n',
             'sdk/go/worker/go.mod': b'module github.com/ziad-hsn/cpra/sdk/go/worker\n'}
    inventory = {}
    path = root / (name + '-source.tar')
    with tarfile.open(path, 'w', format=tarfile.PAX_FORMAT, pax_headers={'comment': commit}) as archive:
        for filename, body in files.items():
            member = tarfile.TarInfo(filename)
            member.size = len(body)
            member.mode = 0o644
            archive.addfile(member, io.BytesIO(body))
            inventory[filename] = {'kind': 'file', 'mode': 0o644, 'sha256': hashlib.sha256(body).hexdigest()}
    write_json(root / (name + '-source.json'), inventory)
    return prepare.sha256(path), prepare.digest_json(inventory)


def prepared_fixture(root):
    root.mkdir()
    sources = {name: source_archive(root, name, prepare.BASELINE if name == 'baseline' else COMMIT)
               for name in ('baseline', 'candidate')}
    manifest = manifest_fixture()
    manifest.update(source_date_epoch=1, source_date='1970-01-01T00:00:01Z')
    inventory = json.loads((root / 'candidate-source.json').read_text())
    manifest['source_files'] = {name: {'mode': '100755' if record['mode'] & 0o111 else '100644',
                                     'sha256': record['sha256']} for name, record in inventory.items()}
    write_json(root / 'RELEASE.json', manifest)
    rows = []
    for name in ('baseline', 'candidate', 'target'):
        (root / name).write_bytes(('Not executable; ' + name + ' report identity fixture').encode())
        archive, tree = sources['baseline' if name == 'baseline' else 'candidate']
        rows.append({'name': name, 'commit': prepare.BASELINE if name == 'baseline' else COMMIT,
                     'dirty': False, 'go_version': prepare.RECIPE['go_version'],
                     'flags': prepare.build_flags(prepare.RECIPE['build_tags'], manifest if name != 'baseline' else None),
                     'environment': prepare.recorded_environment('linux_amd64'), 'source_date_epoch': 1,
                     'source_sha256': archive, 'source_tree_sha256': tree, 'sha256': prepare.sha256(root / name)})
    metadata = {'schema_version': 3, 'baseline_commit': prepare.BASELINE, 'recipe': prepare.RECIPE,
                'release': manifest, 'release_identity': prepare.release_identity(manifest),
                'tags': prepare.RECIPE['build_tags'], 'builds': rows,
                'toolchain': {'version': prepare.RECIPE['go_version'], 'target': 'linux_amd64',
                              'archive_sha256': prepare.TOOLCHAINS[prepare.RECIPE['go_version']]['linux_amd64'],
                              'binary_sha256': 'd' * 64}}
    write_json(root / 'builds.json', metadata)
    return metadata


def fault_fixture(mode, scenario):
    if mode == 'soak':
        return [{'type': 'cohort_outage', 'start': 360 + n * 3600, 'planned_end': 485 + n * 3600,
                 'actual_end': 485 + n * 3600, 'cohort': 100, 'episode': n + 1} for n in range(24)]
    if scenario == 'healthy':
        return []
    if scenario == 'crash':
        return [{'type': 'process_kill', 'start': 330, 'actual_end': 335}]
    duration = 2 if scenario == 'burst' else 125
    return [{'type': scenario, 'start': 330, 'planned_end': 330 + duration,
             'actual_end': 335 if scenario == 'burst' else 455,
             'cohort': 100 if scenario == 'recovery' else 1000000, 'episode': 1}]


def target_audit(count, elapsed, actions, rate_scale=1):
    return {'configured_monitors': count, 'received': int(elapsed * count / 60 * rate_scale),
            'completed': int(elapsed * count / 60 * rate_scale), 'actions': actions,
            'duplicate_actions': 0, 'early_checks': 0, 'fault_epoch': actions // 100}


def sample_fixture(count, elapsed, start, candidate, declared, mode, rate_scale):
    completed = sum(f['actual_end'] <= elapsed and f['type'] != 'process_kill' for f in declared)
    sample = {'at': start + elapsed, 'elapsed': elapsed,
              'target_audit': target_audit(count, elapsed, completed * 100, rate_scale),
              'target_audit_latency': .01,
              'controller': {'rss_bytes': 1000000, 'descriptors': 40, 'threads': 10, 'cpu_seconds': elapsed * .1},
              'target': {'rss_bytes': 200000, 'descriptors': 20, 'threads': 5, 'cpu_seconds': elapsed * .02},
              'queues': {p: {'queue_depth': 0, 'capacity': 1000, 'dropped': 0} for p in ('pulse', 'intervention', 'code')},
              'pools': {p: {'current_capacity': 10, 'running_workers': 2, 'waiting_tasks': 0}
                        for p in ('pulse', 'intervention', 'code')},
              'dashboard': {'total': count, 'returned': 100, 'page_size': 100}}
    if elapsed:
        sample['target_cpu_cores'] = .02
    if candidate:
        sample.update(storage={'mode': 'raft', 'ready': True, 'commit_latency_ms': .01, 'snapshot_duration_ms': 1},
                      storage_usage={'available': True, 'bytes': 10000, 'history_bytes': completed * 100,
                                     'snapshot_files': int(elapsed >= 300), 'snapshot_bytes': 1000},
                      runtime={'heap_bytes': 100000, 'goroutines': 15},
                      declared_fault_window=any(f['start'] <= elapsed < f['actual_end'] + 300 for f in declared),
                      slo={'window_seconds': 300, 'queue_target_ms': 250, 'result_target_ms': 5000,
                           'coverage_complete': elapsed >= 300,
                           'reports': [{'driver': 'http', 'samples': 10000, 'expected': 10000, 'missed': 0,
                                        'overdue': 0, 'pending': 0, 'timeouts': 0, 'queue_met': 10000,
                                        'result_met': 10000, 'queue_attainment': 1, 'result_attainment': 1,
                                        **{key: {'p50_ms': .1, 'p95_ms': .2, 'p99_ms': 1}
                                           for key in ('scheduling_queue', 'execution', 'scheduled_result')}}]})
    return sample


def campaign_fixture(root, mode, metadata, start):
    root.mkdir()
    write_json(root / 'builds.json', metadata)
    write_json(root / 'preflight.json', {'ready': True, 'baseline_commit': prepare.BASELINE,
               'required_host_free_bytes': 30 * 1024 ** 3, 'required_fixture_headroom_bytes': 5 * 1024 ** 3,
               'host_free_bytes': 40 * 1024 ** 3, 'cpu_count': 4, 'hostname': 'report-fixture',
               'platform': 'Linux-microsoft-standard-WSL2', 'ubuntu_vhd_base': 'C:\\fixture'})
    if mode == 'compare':
        plan = [(f'{build}-{count}-{repeat}', build, count, 900, 'healthy')
                for count in (10000, 100000, 1000000) for repeat in range(3) for build in ('baseline', 'candidate')]
    elif mode == 'faults':
        plan = [(kind, 'candidate', 1000000, 900, kind) for kind in ('burst', 'slow', 'outage', 'recovery', 'crash')]
    else:
        plan = [('soak', 'candidate', 1000000, 86400, 'healthy')]
    rows = {r['name']: r for r in metadata['builds']}
    runs = []
    for name, build, count, duration, scenario in plan:
        directory = root / name
        directory.mkdir()
        declared = fault_fixture(mode, scenario)
        # A slower baseline remains valid comparative evidence, not a candidate failure.
        rate_scale = .5 if build == 'baseline' else 1
        healthy = 0
        with gzip.open(directory / 'samples.jsonl.gz', 'wt') as stream:
            for elapsed in range(0, duration + 300, 5):
                sample = sample_fixture(count, elapsed, start, build == 'candidate', declared, mode, rate_scale)
                healthy += int(build == 'candidate' and elapsed >= 300 and not sample['declared_fault_window'])
                stream.write(json.dumps(sample, separators=(',', ':')) + '\n')
        actions = 100 * sum(f['type'] != 'process_kill' for f in declared)
        final = target_audit(count, duration + 300, actions, rate_scale)
        final.update(distinct=count, minimum_checks=1, maximum_checks=(duration + 300) // 60,
                     count_digest='e' * 64, cohort_actions=[actions // 100] * 100)
        result = {'status': 'measured_pending_review', 'monitors': count, 'loaded_monitors': count,
                  'interval_seconds': 60, 'warmup_seconds': 300, 'measurement_seconds': duration,
                  'scenario': scenario, 'declared_faults': declared, 'binary_sha256': rows[build]['sha256'],
                  'internal_percentiles': 'candidate' if build == 'candidate' else 'unavailable in baseline',
                  'started_at': start, 'seed': SEED, 'samples': (duration + 300) // 5,
                  'measurement_started_at': start + 300, 'actual_measurement_seconds': duration,
                  'elapsed_seconds': duration + 295, 'finished_at': start + duration + 302,
                  'peak_rss_bytes': 1000000, 'peak_descriptors': 40, 'healthy_slo_windows': healthy,
                  'target_rate': count / 60, 'achieved_rate': count / 60 * rate_scale,
                  'target_saturation_detected': False, 'final_target_audit': final, 'failures': [],
                  'cleanup': {'controller_stopped': True, 'target_stopped': True, 'token_removed': True}}
        write_json(directory / 'result.json', result)
        if build == 'candidate':
            write_json(directory / 'reconciliation.json', {
                'target_accepted': actions, 'committed_successes': actions, 'unknown_outcomes': 0,
                'confirmed_failures': 0, 'history_events': actions, 'duplicate_interventions': 0, 'reconciled': True,
                'monitors': [{'monitor_id': f'benchmark-{SEED}-{n}', 'target_accepted': actions // 100,
                              'committed_successes': actions // 100, 'unknown_outcomes': 0, 'confirmed_failures': 0,
                              'terminal_digest': 'f' * 64} for n in range(100)]})
        runs.append(result)
        start += duration + 400
    write_json(root / 'campaign.json', {'mode': mode, 'status': 'measured_pending_review',
               'baseline_commit': prepare.BASELINE, 'planned_runs': len(plan), 'runs': runs,
               'release_gate_passed': False, 'review_required': True})
    return start


class EvidenceContracts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        cls.root = Path(cls.temporary.name)
        cls.metadata = prepared_fixture(cls.root / 'builds')
        start = 1900000000
        for mode in ('compare', 'faults', 'soak'):
            start = campaign_fixture(cls.root / mode, mode, cls.metadata, start)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def verify_all(self):
        return verify.verify(*(self.root / name for name in ('builds', 'compare', 'faults', 'soak')), COMMIT)

    @contextmanager
    def change(self, relative, update, binary=False):
        path = self.root / relative
        original = path.read_bytes()
        try:
            if binary:
                path.write_bytes(update(original))
            else:
                data = json.loads(original)
                update(data)
                write_json(path, data)
            yield
        finally:
            path.write_bytes(original)

    @contextmanager
    def change_sample(self, update, index=60):
        def edit(data):
            lines = gzip.decompress(data).splitlines()
            row = json.loads(lines[index]); update(row)
            lines[index] = json.dumps(row).encode()
            return gzip.compress(b'\n'.join(lines) + b'\n')
        with self.change('compare/candidate-10000-0/samples.jsonl.gz', edit, binary=True):
            yield

    def test_complete_retained_matrix_stays_pending_review(self):
        result = self.verify_all()
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertFalse(result['release_gate_passed'])
        self.assertEqual([len(result['campaigns'][m]) for m in ('compare', 'faults', 'soak')], [18, 5, 1])
        self.assertLess(result['campaigns']['compare'][0]['achieved_rate'], 10000 / 60)
        self.assertGreater(len(result['inputs']['soak']), 5)
        self.assertTrue(result['review_required'])

    def test_comparisons_and_faults_do_not_require_or_claim_endurance(self):
        result = verify.verify(self.root / 'builds', self.root / 'compare', self.root / 'faults', commit=COMMIT)
        self.assertEqual(result['status'], 'consistent_pending_review')
        self.assertEqual(result['scope'], 'comparisons_and_faults')
        self.assertEqual(set(result['campaigns']), {'compare', 'faults'})
        self.assertEqual(set(result['inputs']), {'builds', 'compare', 'faults'})
        self.assertEqual(result['endurance'], {'included': False, 'required': False, 'claim_qualified': False})
        self.assertFalse(result['release_gate_passed'])

    def test_missing_trial_smoke_or_unreviewed_promotion_is_rejected(self):
        changes = [lambda d: d['runs'].pop(), lambda d: d.update(mode='smoke'),
                   lambda d: d.update(status='pass'), lambda d: d.update(release_gate_passed=True),
                   lambda d: d.update(review_required=False), lambda d: d.update(planned_runs=1)]
        for update in changes:
            with self.subTest(update=update), self.change('compare/campaign.json', update), self.assertRaises(ValueError):
                self.verify_all()

    def test_source_binary_and_candidate_identities_are_verified(self):
        with self.change('builds/target', lambda _: b'different target', binary=True), self.assertRaisesRegex(ValueError, 'digest'):
            self.verify_all()
        with self.change('builds/candidate-source.json', lambda d: d.pop('main.go')), self.assertRaisesRegex(ValueError, 'inventory digest'):
            self.verify_all()
        with self.assertRaisesRegex(ValueError, 'candidate commit'):
            verify.verify(*(self.root / name for name in ('builds', 'compare', 'faults', 'soak')), 'b' * 40)

    def test_failed_disk_gate_or_another_host_cannot_qualify(self):
        for field, value in (('host_free_bytes', 29 * 1024 ** 3), ('required_fixture_headroom_bytes', -1),
                             ('platform', 'Linux-cloud-instance'), ('ubuntu_vhd_base', 'D:\\fixture')):
            with self.subTest(field=field), self.change('compare/preflight.json', lambda d: d.update({field: value})), self.assertRaises(ValueError):
                self.verify_all()

    def test_sample_mutations_cannot_hide_missed_checks_or_target_saturation(self):
        mutations = [lambda s: s['slo']['reports'][0].update(missed=500),
                     lambda s: s['slo']['reports'][0].update(result_met=9000),
                     lambda s: s['slo'].update(coverage_complete=False),
                     lambda s: s['slo'].update(queue_target_ms=1000),
                     lambda s: s.update(target_audit_latency=.3),
                     lambda s: s['target_audit'].update(duplicate_actions=1),
                     lambda s: s['storage'].update(mode='memory'),
                     lambda s: s['storage_usage'].update(available=False),
                     lambda s: s['dashboard'].update(total=9999),
                     lambda s: s['dashboard'].update(returned=1000),
                     lambda s: s['pools'].pop('pulse'),
                     lambda s: s.update(declared_fault_window=True),
                     lambda s: s.update(at=s['at'] + 30)]
        for update in mutations:
            with self.subTest(update=update), self.change_sample(update), self.assertRaises(ValueError):
                self.verify_all()

    def test_missing_sample_and_corrupt_compression_are_rejected(self):
        def remove(data):
            rows = gzip.decompress(data).splitlines()
            return gzip.compress(b'\n'.join(rows[:70] + rows[75:]) + b'\n')
        with self.change('compare/candidate-10000-0/samples.jsonl.gz', remove, binary=True), self.assertRaisesRegex(ValueError, 'missing samples'):
            self.verify_all()
        with self.change('compare/candidate-10000-0/samples.jsonl.gz', lambda d: d[:-12], binary=True), self.assertRaises((OSError, EOFError)):
            self.verify_all()

    def test_run_summary_cannot_replace_the_retained_record(self):
        with self.change('compare/campaign.json', lambda d: d['runs'][0].update(actual_measurement_seconds=1)), self.assertRaisesRegex(ValueError, 'summary differs'):
            self.verify_all()

    def test_shortened_soak_is_rejected_even_when_both_summaries_agree(self):
        def shorten(d):
            d['actual_measurement_seconds'] = 86400 - 1
        with self.change('soak/soak/result.json', shorten), \
                self.change('soak/campaign.json', lambda d: shorten(d['runs'][0])), \
                self.assertRaisesRegex(ValueError, 'full-duration'):
            self.verify_all()

    def test_correctly_computed_attainment_still_fails_below_the_required_threshold(self):
        def breach(sample):
            row = sample['slo']['reports'][0]
            row.update(result_met=9800, result_attainment=.98)
        with self.change_sample(breach), self.assertRaisesRegex(ValueError, 'breaches'):
            self.verify_all()

    def test_overlapping_runs_on_the_same_host_are_rejected(self):
        campaign = json.loads((self.root / 'compare/campaign.json').read_text())
        def overlap(d):
            d['finished_at'] = campaign['runs'][1]['started_at'] + 1
        with self.change('compare/baseline-10000-0/result.json', overlap), \
                self.change('compare/campaign.json', lambda d: overlap(d['runs'][0])), \
                self.assertRaisesRegex(ValueError, 'overlap'):
            self.verify_all()

    def test_reconciliation_requires_per_monitor_effects_even_when_totals_match(self):
        with self.change('compare/candidate-10000-0/reconciliation.json', lambda d: d['monitors'][0].update(target_accepted=1)), self.assertRaisesRegex(ValueError, 'per-monitor outcomes disagree'):
            self.verify_all()

    def test_full_duration_and_all_faults_are_required(self):
        result = json.loads((self.root / 'soak/soak/result.json').read_text())
        with self.assertRaisesRegex(ValueError, 'incomplete'):
            verify.fault_intervals({**result, 'declared_faults': result['declared_faults'][:-1]}, 'soak', 'healthy', 300)
        with self.assertRaisesRegex(ValueError, 'recovery'):
            verify.fault_intervals({**result, 'actual_measurement_seconds': 83000}, 'soak', 'healthy', 300)
        altered = copy.deepcopy(result); altered['declared_faults'][0]['actual_end'] = 800
        with self.assertRaisesRegex(ValueError, 'duration'):
            verify.fault_intervals(altered, 'soak', 'healthy', 300)

    def test_bounded_json_and_compressed_sample_decoding(self):
        for text in ('{"x":1,"x":2}', '{"x":NaN}', '{"x":1e999}', '[]'):
            with self.subTest(text=text), self.assertRaises(ValueError):
                verify.decode(text, 'fixture')
        with patch.object(verify, 'REPORT_LIMIT', 64), self.assertRaisesRegex(ValueError, 'exceeds'):
            self.verify_all()
        with patch.object(verify, 'LINE_LIMIT', 64), self.assertRaisesRegex(ValueError, 'byte limit'):
            self.verify_all()
        with patch.object(verify, 'SAMPLE_LIMIT', 4000), self.assertRaisesRegex(ValueError, 'byte limit'):
            self.verify_all()

    def test_aliases_and_evidence_changes_are_detected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); (root / 'value').write_text('{}')
            (root / 'alias').symlink_to(root / 'value')
            evidence = verify.Evidence(root)
            with self.assertRaisesRegex(ValueError, 'unaliased'):
                evidence.read('alias')
            evidence.read('value'); (root / 'value').write_text('{"changed":true}')
            with self.assertRaisesRegex(ValueError, 'changed during'):
                evidence.unchanged()

    def test_cli_preserves_invalid_evidence_and_refuses_to_overwrite_it(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); output = root / 'report.json'
            command = [sys.executable, str(Path(verify.__file__)), '--candidate-commit', COMMIT, '--out', str(output)]
            for flag in ('builds', 'comparison', 'faults', 'soak'):
                command += ['--' + flag, str(root / 'missing')]
            first = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(first.returncode, 1, first.stderr)
            original = output.read_bytes()
            self.assertEqual(json.loads(original)['status'], 'invalid_evidence')
            self.assertFalse(json.loads(original)['release_gate_passed'])
            second = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(second.returncode, 1)
            self.assertEqual(output.read_bytes(), original)


if __name__ == '__main__':
    unittest.main()
