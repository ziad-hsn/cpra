"""Isolation-report accounting tests; no capacity or runtime claim."""
import unittest

from isolation import latency_summary


class LatencySummaryTests(unittest.TestCase):
    def test_unavailable_and_nonfinite_values_are_not_zero_measurements(self):
        for missing in (None, float('nan'), float('inf'), True, '1', -1):
            with self.subTest(value=missing):
                rows = [{'slo': {'window_seconds': 300, 'coverage_complete': False,
                                 'reports': [{'driver': 'http', 'samples': 0, 'expected': 12,
                                              'scheduling_queue': {'p99_ms': missing},
                                              'scheduled_result': {'p99_ms': missing}}]}}]
                summary = latency_summary(rows)['http']
                self.assertIsNone(summary['maximum_reported_queue_p99_ms'])
                self.assertIsNone(summary['maximum_reported_result_p99_ms'])
                self.assertEqual(summary['last_sample_count'], 0)
                self.assertEqual(summary['last_expected_count'], 12)
                self.assertFalse(summary['last_coverage_complete'])

    def test_sampled_maxima_keep_last_window_coverage_and_do_not_sum_rolling_counts(self):
        rows = []
        for queue, result, samples, timeouts in ((None, None, 0, 0), (45, 5300, 1200, 400), (20, 2000, 2000, 700)):
            rows.append({'slo': {'window_seconds': 300, 'coverage_complete': False,
                                'reports': [{'driver': 'http', 'samples': samples, 'expected': samples+3,
                                             'timeouts': timeouts, 'missed': 2,
                                             'condition': 'downstream_limited',
                                             'scheduling_queue': {'p99_ms': queue},
                                             'scheduled_result': {'p99_ms': result}}]}})
        summary = latency_summary(rows)['http']
        self.assertEqual(summary['maximum_reported_queue_p99_ms'], 45)
        self.assertEqual(summary['maximum_reported_result_p99_ms'], 5300)
        self.assertEqual(summary['maximum_timeouts'], 700)
        self.assertEqual(summary['last_sample_count'], 2000)
        self.assertEqual(summary['last_expected_count'], 2003)
        self.assertEqual(summary['last_window_seconds'], 300)
        self.assertEqual(summary['conditions'], ['downstream_limited'])
        self.assertFalse(summary['last_coverage_complete'])


if __name__ == '__main__':
    unittest.main()
