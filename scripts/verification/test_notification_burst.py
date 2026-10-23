"""Burst evidence keeps receiver calls separate from committed outcomes."""
import copy
import http.client
import json
import pathlib
import tempfile
import unittest
from unittest import mock

import notification_burst


def observations():
    calls, actions = [], []
    for color in ('red', 'green'):
        for monitor in ('svc000', 'svc001'):
            identity = color + '-' + monitor
            calls.append(dict(color=color, monitor_id=monitor, action_id=identity,
                              delivery_id='delivery-' + identity, endpoint_id=color,
                              episode_id='correlation-' + monitor, incident_id='incident-' + monitor,
                              received_at='2026-10-22T10:01:00Z' if color == 'red' else '2026-10-22T10:02:00Z',
                              response_written=True))
            actions.append(dict(id=identity, kind='code', color=color, monitorID=monitor,
                                deliveryID='delivery-' + identity, endpointID=color,
                                state='succeeded', queuedAt='2026-10-22T10:00:00Z',
                                startedAt='2026-10-22T10:00:50Z'))
    return calls, actions


class NotificationBurstSummaryTests(unittest.TestCase):
    def summary(self, calls, actions):
        return notification_burst.summarize(['svc000', 'svc001'], calls, actions)

    def test_independent_complete_observations_pass_and_report_long_queue_waits(self):
        calls, actions = observations()
        report = self.summary(calls, actions)
        self.assertEqual(report['status'], 'pass')
        self.assertEqual(report['colors']['red']['max_prestart_seconds'], 50)
        self.assertEqual(report['colors']['red']['waited_more_than_execution_budget'], 2)

    def test_receiver_acceptance_does_not_replace_unknown_committed_outcome(self):
        calls, actions = observations()
        actions[0]['state'] = 'unknown'
        report = self.summary(calls, actions)
        self.assertEqual(report['status'], 'fail')
        self.assertEqual(report['colors']['red']['unknown_action_ids'], ['red-svc000'])
        self.assertEqual(report['colors']['red']['distinct_monitors_received'], 2)
        self.assertEqual(report['colors']['red']['missing_confirmed_success'], ['svc000'])

    def test_committed_success_does_not_replace_missing_receiver_evidence(self):
        calls, actions = observations()
        calls.pop(0)
        report = self.summary(calls, actions)
        self.assertEqual(report['status'], 'fail')
        self.assertEqual(report['colors']['red']['missing_at_receiver'], ['svc000'])
        self.assertEqual(report['colors']['red']['missing_confirmed_success'], [])

    def test_repeated_call_fails_even_when_database_has_one_success(self):
        calls, actions = observations()
        calls.append(copy.deepcopy(calls[0]))
        report = self.summary(calls, actions)
        self.assertEqual(report['status'], 'fail')
        self.assertEqual(report['colors']['red']['repeated_monitor_calls'], {'svc000': 2})
        self.assertEqual(report['colors']['red']['repeated_action_calls'], {'red-svc000': 2})
        self.assertEqual(report['colors']['red']['repeated_delivery_calls'], {'delivery-red-svc000': 2})

    def test_new_episode_is_an_extra_outage_not_a_repeated_delivery(self):
        calls, actions = observations()
        calls.append(dict(calls[0], action_id='next-action', delivery_id='next-delivery',
                          episode_id='next-episode', incident_id='next-incident'))
        actions.append(dict(actions[0], id='next-action', deliveryID='next-delivery'))
        report = self.summary(calls, actions)
        self.assertEqual(report['status'], 'fail')
        self.assertEqual(report['colors']['red']['repeated_monitor_calls'], {'svc000': 2})
        self.assertEqual(report['colors']['red']['repeated_action_calls'], {})
        self.assertEqual(report['colors']['red']['repeated_delivery_calls'], {})

    def test_wrong_delivery_identity_fails_despite_matching_action_id(self):
        calls, actions = observations()
        calls[0]['delivery_id'] = 'another-delivery'
        report = self.summary(calls, actions)
        self.assertEqual(report['status'], 'fail')
        self.assertEqual(report['colors']['red']['receiver_action_identity_mismatch'], ['red-svc000'])

    def test_extra_unknown_action_is_not_hidden_by_matching_successes(self):
        calls, actions = observations()
        extra = dict(actions[0], id='additional-action', state='unknown')
        actions.append(extra)
        report = self.summary(calls, actions)
        self.assertEqual(report['status'], 'fail')
        self.assertEqual(report['colors']['red']['unknown_action_ids'], ['additional-action'])

    def test_failed_response_write_is_preserved(self):
        calls, actions = observations()
        calls[0]['response_written'] = False
        report = self.summary(calls, actions)
        self.assertEqual(report['status'], 'fail')
        self.assertIn('red_response_write_failure', report['problems'])

    def test_missing_start_is_unavailable_not_zero_wait(self):
        calls, actions = observations()
        for action in actions:
            action.pop('startedAt')
        report = self.summary(calls, actions)
        self.assertIsNone(report['colors']['red']['max_prestart_seconds'])
        self.assertEqual(report['colors']['red']['waited_more_than_execution_budget'], 0)

    def test_explicit_expected_unknown_is_a_hold_not_a_success(self):
        calls, actions = observations()
        actions[0]['state'] = 'unknown'
        report = notification_burst.summarize(['svc000', 'svc001'], calls, actions,
                                              expected_unknown=['red-svc000'])
        self.assertEqual(report['status'], 'pass')
        self.assertEqual(report['colors']['red']['committed_states'], {'unknown': 1, 'succeeded': 1})
        self.assertEqual(report['colors']['red']['expected_unknown_action_ids'], ['red-svc000'])

    def test_expected_unknown_must_remain_unknown(self):
        calls, actions = observations()
        report = notification_burst.summarize(['svc000', 'svc001'], calls, actions,
                                              expected_unknown=['red-svc000'])
        self.assertEqual(report['status'], 'fail')
        self.assertIn('red_non_success_outcomes', report['problems'])

    def test_planned_inflight_kill_allows_only_named_unknown_response_failure(self):
        calls, actions = observations()
        actions[0]['state'] = 'unknown'
        calls[0]['response_written'] = False
        report = notification_burst.summarize(['svc000', 'svc001'], calls, actions,
                                              expected_unknown=['red-svc000'], failed_responses=['red-svc000'])
        self.assertEqual(report['status'], 'pass')
        calls[1]['response_written'] = False
        report = notification_burst.summarize(['svc000', 'svc001'], calls, actions,
                                              expected_unknown=['red-svc000'], failed_responses=['red-svc000'])
        self.assertEqual(report['status'], 'fail')

    def test_duplicate_unknown_records_do_not_pass(self):
        calls, actions = observations()
        actions[0]['state'] = 'unknown'
        actions.append(copy.deepcopy(actions[0]))
        report = notification_burst.summarize(['svc000', 'svc001'], calls, actions,
                                              expected_unknown=['red-svc000'])
        self.assertEqual(report['status'], 'fail')
        self.assertIn('red_duplicate_action_records', report['problems'])


class NotificationRestartSummaryTests(unittest.TestCase):
    def setUp(self):
        _, self.after = observations()
        self.before = copy.deepcopy(self.after[:2])
        self.before[0].update(state='queued', executionBudget='45s')
        self.before[0].pop('startedAt')
        self.after[0].update(executionBudget='45s', deadline='2026-10-22T10:01:35Z')
        self.before[1].update(state='started', startedAt='2026-10-22T10:00:10Z', deadline='2026-10-22T10:00:55Z')
        self.after[1].update(state='unknown', startedAt='2026-10-22T10:00:10Z', deadline='2026-10-22T10:00:55Z')

    def findings(self):
        return notification_burst.restart_findings(self.before, self.after, '2026-10-22T10:00:20Z')

    def test_original_queued_attempt_starts_once_and_inflight_unknown_keeps_budget(self):
        report = self.findings()
        self.assertEqual(report['problems'], [])
        self.assertEqual(report['original_queued_action_ids'], ['red-svc000'])
        self.assertEqual(report['original_unknown_or_inflight_action_ids'], ['red-svc001'])

    def test_replacement_identity_fails(self):
        self.after[0]['deliveryID'] = 'replacement'
        self.assertIn('restart_lost_original_identity', self.findings()['problems'])

    def test_renewed_unknown_budget_fails(self):
        self.after[1]['deadline'] = '2026-10-22T10:01:55Z'
        self.assertIn('restart_changed_unknown_outcome_or_budget', self.findings()['problems'])

    def test_fresh_queued_start_receives_exact_original_execution_budget(self):
        self.after[0]['deadline'] = '2026-10-22T10:01:36Z'
        self.assertIn('first_start_budget_not_preserved', self.findings()['problems'])

    def test_unknown_queued_boundary_is_not_replayed_or_claimed_as_success(self):
        self.after[0]['state'] = 'unknown'
        self.assertIn('queued_original_not_completed', self.findings()['problems'])


class NotificationBurstBoundaryTests(unittest.TestCase):
    def setUp(self):
        _, self.actions = observations()
        for row in self.actions:
            row.update(executionBudget='45s', deadline='2026-10-22T10:01:35Z')

    def test_succeeded_red_demonstrates_long_wait_with_original_budget(self):
        report = notification_burst.burst_boundaries(self.actions)
        self.assertTrue(report['long_prestart_boundary_exercised'])
        self.assertEqual(report['execution_budget_violations'], [])
        self.assertEqual(report['successful_red_count'], 2)

    def test_quick_delivery_is_not_evidence_for_long_wait_boundary(self):
        for row in self.actions:
            row['queuedAt'] = '2026-10-22T10:00:49Z'
        report = notification_burst.burst_boundaries(self.actions)
        self.assertFalse(report['long_prestart_boundary_exercised'])
        self.assertEqual(report['execution_budget_violations'], [])

    def test_unknown_long_wait_does_not_satisfy_successful_boundary(self):
        for row in self.actions:
            if row['color'] == 'red':
                row['state'] = 'unknown'
        self.assertFalse(notification_burst.burst_boundaries(self.actions)['long_prestart_boundary_exercised'])

    def test_wrong_deadline_or_missing_original_budget_is_detected(self):
        self.actions[0]['deadline'] = '2026-10-22T10:01:36Z'
        self.actions[1].pop('executionBudget')
        self.assertEqual(notification_burst.burst_boundaries(self.actions)['execution_budget_violations'],
                         ['red-svc000', 'red-svc001'])


class NotificationSharedColorTests(unittest.TestCase):
    def setUp(self):
        self.calls, self.actions = observations()
        for row in list(self.actions[:2]):
            row = dict(row, id='yellow-' + row['monitorID'], color='yellow', state='cancelled',
                       deliveryID='yellow-delivery-' + row['monitorID'], outcome='notification_superseded')
            row.pop('startedAt')
            self.actions.append(row)

    def summary(self):
        return notification_burst.summarize(['svc000', 'svc001'], self.calls, self.actions,
                                              configured_colors=('yellow', 'red', 'green'))

    def test_never_started_yellow_supersession_is_not_a_lost_red(self):
        report = self.summary()
        self.assertEqual(report['status'], 'pass')
        self.assertEqual(report['colors']['yellow']['calls'], 0)
        self.assertEqual(report['colors']['yellow']['never_started_superseded_action_ids'],
                         ['yellow-svc000', 'yellow-svc001'])
        self.assertEqual(report['colors']['red']['calls'], 2)

    def test_superseded_yellow_cannot_have_received_effect(self):
        event = dict(self.calls[0], color='yellow', action_id='yellow-svc000', delivery_id='yellow-delivery-svc000')
        self.calls.append(event)
        self.assertEqual(self.summary()['status'], 'fail')

    def test_expired_or_unknown_yellow_is_not_accepted_as_superseded(self):
        for state, outcome in [('unknown', 'unknown'), ('cancelled', 'notification_deadline')]:
            with self.subTest(state=state):
                self.actions[-1].update(state=state, outcome=outcome)
                self.assertEqual(self.summary()['status'], 'fail')

    def test_each_configured_monitor_needs_its_original_yellow_intent(self):
        self.actions.pop()
        report = self.summary()
        self.assertIn('yellow_original_intent_coverage_or_duplicates', report['problems'])

    def test_incidentless_yellow_on_shared_route_preserves_other_identities(self):
        data = dict(self.calls[0], color='yellow')
        data.pop('incident_id')
        actual = notification_burst.delivery_fields('/alert/hook', data, ('yellow', 'red', 'green'), True)
        self.assertEqual(actual['incident_id'], '')
        self.assertEqual(actual['action_id'], data['action_id'])
        data['color'] = 'red'
        with self.assertRaises(ValueError):
            notification_burst.delivery_fields('/alert/hook', data, ('yellow', 'red', 'green'), True)

    def test_shared_route_cannot_invent_a_different_color_or_destination(self):
        data = self.calls[0]
        with self.assertRaises(ValueError):
            notification_burst.delivery_fields('/alert/red', data, ('yellow', 'red', 'green'), True)
        with self.assertRaises(ValueError):
            notification_burst.delivery_fields('/alert/hook', dict(data, color='blue'), ('yellow', 'red', 'green'), True)

    def test_green_uses_started_provider_correlation_not_action_episode_equality(self):
        for row in self.actions:
            row['notificationEpisode'] = row['id'] + '-different-episode'
        self.assertEqual(notification_burst.green_exposure_findings(self.calls, self.actions), [])
        self.calls[-1]['episode_id'] = 'another-outage'
        self.assertEqual(notification_burst.green_exposure_findings(self.calls, self.actions), ['green-svc001'])

    def test_partial_fleet_page_does_not_establish_all_monitor_health(self):
        page = {'total': 2, 'monitors': [{'monitor_id': 'svc000', 'health': 'healthy'}]}
        self.assertEqual(notification_burst.fleet_findings(page, ['svc000', 'svc001'], 'healthy'),
                         ['incomplete_fleet_observation'])


class NotificationClockTests(unittest.TestCase):
    def reading(self, wall, monotonic, boottime=None):
        return {'wall_seconds': wall, 'monotonic_seconds': monotonic, 'boottime_seconds': boottime}

    def test_small_steps_cannot_hide_cumulative_clock_discontinuity(self):
        initial = self.reading(100, 0)
        previous = self.reading(112, 10)
        current = self.reading(124, 20)
        observed = notification_burst.clock_observation(initial, previous, current)
        self.assertEqual(observed['step_wall_drift_seconds'], 2)
        self.assertEqual(observed['cumulative_wall_drift_seconds'], 4)
        self.assertTrue(observed['discontinuity'])

    def test_backwards_wall_correction_invalidates_durations(self):
        initial = self.reading(100, 0)
        observed = notification_burst.clock_observation(initial, self.reading(108, 10), self.reading(116, 20))
        self.assertEqual(observed['cumulative_wall_drift_seconds'], -4)
        self.assertTrue(observed['discontinuity'])

    def test_boottime_exposes_pause_without_wall_change(self):
        initial = self.reading(100, 0, 0)
        current = self.reading(101, 1, 5)
        observed = notification_burst.clock_observation(initial, initial, current)
        self.assertTrue(observed['discontinuity'])
        self.assertEqual(observed['boottime_minus_monotonic_seconds'], 4)

    def test_stable_clocks_and_short_observation_gap_remain_valid(self):
        initial = self.reading(100, 0, 0)
        previous = self.reading(110, 10, 10)
        current = self.reading(110.25, 10.25, 10.25)
        observed = notification_burst.clock_observation(initial, previous, current)
        self.assertFalse(observed['discontinuity'])

    def test_long_unobserved_gap_is_not_a_measured_pass(self):
        initial = self.reading(100, 0)
        observed = notification_burst.clock_observation(initial, initial, self.reading(111, 11))
        self.assertTrue(observed['discontinuity'])


class NotificationReceiverTests(unittest.TestCase):
    def request(self, receiver):
        connection = http.client.HTTPConnection('127.0.0.1', receiver.server.server_port, timeout=2)
        try:
            connection.request('GET', '/health')
            response = connection.getresponse()
            self.assertEqual(response.read(), b'ok')
            return response.status, response.version
        finally:
            connection.close()

    def test_request_evidence_records_status_timing_and_bounded_protocol(self):
        with tempfile.TemporaryDirectory(prefix='cpra-receiver-test-') as directory:
            receiver = notification_burst.Receiver(pathlib.Path(directory), ['svc000'], 0)
            try:
                self.assertEqual(self.request(receiver), (200, 10))
                receiver.set_health(False)
                self.assertEqual(self.request(receiver), (503, 10))
            finally:
                receiver.close()
            timing = receiver.timing()
            calls, checks, errors = receiver.snapshot()
            records = [json.loads(line) for line in (pathlib.Path(directory) / 'health-requests.jsonl').read_text().splitlines()]
            self.assertEqual([row['status'] for row in records], [200, 503])
            self.assertTrue(all(row['response_written'] and row['response_seconds'] >= 0 for row in records))
            self.assertEqual(checks['shared_target'], {'healthy': 1, 'unhealthy': 1})
            self.assertEqual(errors, [])
            self.assertEqual(calls, [])
            self.assertEqual(timing['health_records'], 2)
            self.assertEqual(timing['accepted_connections'], 2)
            self.assertEqual(timing['active_handlers'], 0)
            self.assertGreaterEqual(timing['peak_handlers'], 1)
            self.assertGreaterEqual(timing['listen_backlog'], 128)

    def test_evidence_quota_failure_is_explicit_and_does_not_drop_counts(self):
        with tempfile.TemporaryDirectory(prefix='cpra-receiver-quota-test-') as directory:
            with mock.patch.object(notification_burst, 'MAX_HEALTH_RECORDS', 1):
                receiver = notification_burst.Receiver(pathlib.Path(directory), ['svc000'], 0)
                try:
                    self.request(receiver)
                    self.request(receiver)
                finally:
                    receiver.close()
                _, checks, errors = receiver.snapshot()
                self.assertEqual(checks['shared_target']['healthy'], 2)
                self.assertEqual(receiver.timing()['health_records'], 1)
                self.assertIn('health_evidence_limit', errors)


if __name__ == '__main__':
    unittest.main()
