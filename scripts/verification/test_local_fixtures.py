#!/usr/bin/env python3
"""Falsify local fixture oracles independently of the CPRa production binary."""
import base64
import json
import struct
import threading
import unittest
import urllib.error
import urllib.parse
import urllib.request

import contracts
import observe_fixture
import protocol_local

RUN_ID = '01234567-89ab-4cde-8123-0123456789ab'


def request_data(driver, mode='success'):
    key = driver + '-' + mode
    monitor = key + ' [CPRa verification ' + RUN_ID + ']'
    message = 'URGENT\nMonitor: ' + monitor + '\nStatus: unhealthy'
    headers = {'Content-Type': 'application/json'}
    body = {
        'slack': {'text': message},
        'pagerduty': {'routing_key': 'local-routing-key', 'event_action': 'trigger', 'dedup_key': 'cpra:' + monitor,
                     'payload': {'summary': message, 'source': monitor, 'severity': 'error'}},
        'webhook': {'message': message, 'monitor': monitor, 'color': 'red'},
        'telegram': {'text': message, 'chat_id': '424242'},
        'discord': {'content': message},
        'opsgenie': {'message': 'Urgent', 'description': message, 'alias': RUN_ID},
        'mattermost': {'text': message, 'channel': 'cpra-fixture', 'username': 'cpra-verifier'},
        'victorops': {'state_message': message, 'message_type': 'CRITICAL', 'entity_id': 'local-entity'},
        'pushover': {'message': message, 'token': 'local-app-token', 'user': 'local-user', 'title': 'CPRa fixture',
                     'priority': '2', 'retry': '60', 'expire': '1800', 'sound': 'pushover'},
        'datadog': {'text': message, 'title': 'CPRA alert: ' + monitor, 'alert_type': 'error', 'tags': ['fixture:cpra']},
        'teams': {'type': 'message', 'attachments': [{'contentType': 'application/vnd.microsoft.card.adaptive',
                    'content': {'type': 'AdaptiveCard', 'version': '1.2', 'body': [{'type': 'TextBlock', 'text': message, 'wrap': True}]}}]},
        'twilio': {'From': '+15005550006', 'To': '+15005550009', 'Body': message},
    }[driver]
    if driver == 'webhook':
        headers['X-CPRa-Fixture'] = 'local-header'
    if driver == 'opsgenie':
        headers['Authorization'] = 'GenieKey local-api-key'
    if driver == 'twilio':
        headers['Authorization'] = 'Basic ' + base64.b64encode(b'ACLOCAL:local-auth-token').decode()
    if driver == 'datadog':
        headers.update({'DD-API-KEY': 'local-api-key', 'DD-APPLICATION-KEY': 'local-app-key'})
    if driver in ('twilio', 'pushover'):
        headers['Content-Type'] = 'application/x-www-form-urlencoded'
        raw = urllib.parse.urlencode(body).encode()
    else:
        raw = json.dumps(body).encode()
    return key, '/' + key + contracts.PATHS[driver], headers, raw


class RequestOracles(unittest.TestCase):
    def test_all_twelve_valid_contracts(self):
        for driver in contracts.DRIVERS:
            with self.subTest(driver=driver):
                key, path, headers, raw = request_data(driver)
                self.assertEqual(contracts.validate_request(driver, 'POST', path, headers, raw, path, key), RUN_ID)

    def test_every_contract_rejects_wrong_path_method_empty_body_or_uncorrelated_monitor(self):
        for driver in contracts.DRIVERS:
            key, path, headers, raw = request_data(driver)
            for method, actual_path, actual_raw, monitor in (
                    ('GET', path, raw, key), ('POST', path + '/wrong', raw, key),
                    ('POST', path, b'{}', key), ('POST', path, raw, 'different-monitor')):
                with self.subTest(driver=driver, method=method, path=actual_path, monitor=monitor):
                    with self.assertRaises((ValueError, KeyError, TypeError)):
                        contracts.validate_request(driver, method, actual_path, headers, actual_raw, path, monitor)

    def test_credentials_and_custom_header_required(self):
        for driver, header in [('opsgenie', 'Authorization'), ('twilio', 'Authorization'),
                               ('datadog', 'DD-API-KEY'), ('webhook', 'X-CPRa-Fixture')]:
            key, path, headers, raw = request_data(driver)
            del headers[header]
            with self.subTest(driver=driver), self.assertRaises(ValueError):
                contracts.validate_request(driver, 'POST', path, headers, raw, path, key)

    def test_body_fields_and_recipient_checked(self):
        for driver, original in [('telegram', b'424242'), ('pagerduty', b'local-routing-key'),
                                 ('pushover', b'local-user'), ('twilio', b'15005550009'),
                                 ('mattermost', b'cpra-fixture'), ('teams', b'AdaptiveCard')]:
            key, path, headers, raw = request_data(driver)
            with self.subTest(driver=driver), self.assertRaises(ValueError):
                contracts.validate_request(driver, 'POST', path, headers, raw.replace(original, b'WRONG'), path, key)

    def test_receiver_does_not_count_invalid_request_as_delivery(self):
        server = contracts.ContractServer()
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        key, url = server.register('slack', 'success')
        try:
            with self.assertRaises(urllib.error.HTTPError) as error:
                urllib.request.urlopen(urllib.request.Request(url, b'{}', {'Content-Type': 'application/json'}), timeout=2)
            self.assertEqual(error.exception.code, 422)
            self.assertEqual(server.audit(key)['count'], 0)
            self.assertEqual(server.audit(key)['invalid'], 1)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


class ObservationOracles(unittest.TestCase):
    def setUp(self):
        self.before = {'count': 0, 'invalid': 0, 'run_id': RUN_ID}
        self.after = {'count': 1, 'invalid': 0, 'run_id': RUN_ID}

    def test_accepts_one_correlated_operation(self):
        self.assertTrue(observe_fixture.observed(self.before, self.after, RUN_ID))

    def test_missing_duplicate_invalid_or_stale_operation_does_not_pass(self):
        for patch in ({'count': 0}, {'count': 2}, {'invalid': 1}, {'run_id': 'another-run'}):
            with self.subTest(patch=patch):
                self.assertFalse(observe_fixture.observed(self.before, {**self.after, **patch}, RUN_ID))

    def test_dns_multiple_queries_require_at_least_one(self):
        self.assertTrue(observe_fixture.observed(self.before, {**self.after, 'count': 2}, RUN_ID, exact=False))
        self.assertFalse(observe_fixture.observed(self.before, {**self.after, 'count': 0}, RUN_ID, exact=False))


class ProtocolOracles(unittest.TestCase):
    def test_closed_port_setup_failure_cannot_pass_negative_case(self):
        state = {'count': 0, 'invalid': 0}
        record = {'status': 'fail', 'accepted': False, 'duration_ms': 1}
        for driver in ('tcp', 'grpc'):
            self.assertFalse(protocol_local.scenario_passed(driver, 'negative', record, state))
            self.assertFalse(protocol_local.scenario_passed(driver, 'negative', {**record, 'operation_invoked': False}, state))
            self.assertTrue(protocol_local.scenario_passed(driver, 'negative', {**record, 'operation_invoked': True}, state))

    def test_dns_real_answer_and_nxdomain(self):
        for qtype in (1, 28):
            question = b'\x04cpra\x07fixture\x04test\x00' + struct.pack('!HH', qtype, 1)
            request = struct.pack('!HHHHHH', 421, 0x0100, 1, 0, 0, 0) + question
            for success in (True, False):
                answer = protocol_local.dns_response(request, success)
                ident, flags, questions, answers, _, _ = struct.unpack('!HHHHHH', answer[:12])
                self.assertEqual((ident, questions, answers), (421, 1, int(success)))
                self.assertEqual(flags & 15, 0 if success else 3)
                self.assertEqual(answer[12:12 + len(question)], question)
        with self.assertRaises(ValueError):
            protocol_local.dns_response(b'not DNS', True)

    def test_dns_rejects_unexpected_name(self):
        request = struct.pack('!HHHHHH', 421, 0x0100, 1, 0, 0, 0) + b'\x03bad\x04test\x00' + struct.pack('!HH', 1, 1)
        with self.assertRaises(ValueError):
            protocol_local.dns_response(request, True)

    def test_smtp_validates_envelope_and_correlated_data(self):
        raw = ('From: sender@cpra.test\r\nTo: receiver@cpra.test\r\nSubject: CPRa local fixture\r\n\r\n'
               'Monitor: email-success [CPRa verification ' + RUN_ID + ']').encode()
        self.assertEqual(protocol_local.validate_mail('MAIL FROM:<sender@cpra.test>', 'RCPT TO:<receiver@cpra.test>', raw, 'email-success'), RUN_ID)
        for sender, recipient, data in (
                ('MAIL FROM:<other@cpra.test>', 'RCPT TO:<receiver@cpra.test>', raw),
                ('MAIL FROM:<sender@cpra.test>', 'RCPT TO:<other@cpra.test>', raw),
                ('MAIL FROM:<sender@cpra.test>', 'RCPT TO:<receiver@cpra.test>', raw.replace(RUN_ID.encode(), b'stale'))):
            with self.assertRaises(ValueError):
                protocol_local.validate_mail(sender, recipient, data, 'email-success')


if __name__ == '__main__':
    unittest.main()
