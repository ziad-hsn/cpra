"""Validation acceptance is bounded-memory evidence, not parsed fleet coverage."""
import unittest

import validation_memory


class ValidationMemoryClassificationTests(unittest.TestCase):
    valid = 'supplied configuration valid; not applied to an existing catalog. Provider access was not tested'

    def test_accepted_validation_can_complete_the_memory_scenario(self):
        result = validation_memory.classify(0, self.valid, 56000, False, None)
        self.assertEqual(result, {'status': 'pass', 'disposition': 'accepted_validation_within_limits'})

    def test_controlled_expansion_rejection_can_complete_the_memory_scenario(self):
        result = validation_memory.classify(1, 'expanded monitor spool exceeds limit', 56000, False, None)
        self.assertEqual(result, {'status': 'pass', 'disposition': 'controlled_expansion_quota_rejection'})

    def test_zero_exit_cannot_hide_an_unsafe_diagnostic(self):
        for message in ('panic: failure', 'fatal error: failure', 'out of memory'):
            with self.subTest(message=message):
                self.assertEqual(validation_memory.classify(0, self.valid + '\n' + message, 56000, False, None)['status'], 'fail')

    def test_created_store_cannot_pass(self):
        self.assertEqual(validation_memory.classify(0, self.valid, 56000, True, None)['status'], 'fail')

    def test_missing_memory_measurement_cannot_pass(self):
        for rss in (None, 0):
            self.assertEqual(validation_memory.classify(0, self.valid, rss, False, None)['status'], 'fail')

    def test_caps_and_interruption_override_success_output(self):
        for stop in ('time_limit', 'diagnostic_byte_limit', 'diagnostic_line_limit', 'interrupted'):
            with self.subTest(stop=stop):
                self.assertEqual(validation_memory.classify(0, self.valid, 56000, False, stop)['status'], 'incomplete')

    def test_arbitrary_exit_zero_output_cannot_pass(self):
        self.assertEqual(validation_memory.classify(0, '', 56000, False, None)['status'], 'fail')

    def test_crash_with_quota_words_cannot_pass(self):
        self.assertEqual(validation_memory.classify(-9, 'expanded monitor spool exceeds limit', 56000, False, None)['status'], 'fail')

    def test_contradictory_success_and_quota_messages_cannot_pass(self):
        self.assertEqual(validation_memory.classify(0, self.valid + '\nexpanded monitor spool exceeds limit', 56000, False, None)['status'], 'fail')


if __name__ == '__main__':
    unittest.main()
