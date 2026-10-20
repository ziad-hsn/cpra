#!/usr/bin/env python3
"""Falsify local fixture oracles independently of the CPRa production binary."""
import base64
import json
import pathlib
import struct
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest import mock
import urllib.error
import urllib.parse
import urllib.request

import contracts
import local
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
        'pagerduty': {'routing_key': 'local-routing-key', 'event_action': 'trigger', 'dedup_key': 'cpra:' + RUN_ID,
                     'payload': {'summary': message, 'source': monitor, 'severity': 'critical'}},
        'webhook': {'message': message, 'monitor': monitor, 'color': 'red'},
        'telegram': {'text': message, 'chat_id': '424242'},
        'discord': {'content': message},
        'opsgenie': {'message': 'Urgent', 'description': message, 'alias': 'cpra:' + RUN_ID, 'priority': 'P1'},
        'mattermost': {'text': message, 'channel': 'cpra-fixture', 'username': 'cpra-verifier'},
        'victorops': {'state_message': message, 'message_type': 'CRITICAL', 'entity_id': 'local-entity'},
        'pushover': {'message': message, 'token': 'local-app-token', 'user': 'local-user', 'title': 'CPRa fixture',
                     'priority': '2', 'retry': '60', 'expire': '1800', 'sound': 'pushover'},
        'datadog': {'text': message, 'title': 'CPRa CRITICAL ALERT: ' + monitor, 'alert_type': 'error', 'tags': ['fixture:cpra']},
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

    def test_datadog_rejects_stale_title_wrong_identity_severity_and_tags(self):
        key, path, headers, raw = request_data('datadog')
        body = json.loads(raw)
        for field, value in (
                ('title', body['title'].replace('CPRa CRITICAL ALERT: ', 'CPRA alert: ')),
                ('title', body['title'].replace(key, 'another-monitor')),
                ('title', body['title'].replace(RUN_ID, '11111111-1111-4111-8111-111111111111')),
                ('alert_type', 'info'), ('tags', ['another:fixture'])):
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                contracts.validate_request('datadog', 'POST', path, headers,
                                           json.dumps({**body, field: value}).encode(), path, key)

    def test_datadog_requires_both_credential_headers(self):
        key, path, headers, raw = request_data('datadog')
        for name in ('DD-API-KEY', 'DD-APPLICATION-KEY'):
            with self.subTest(header=name), self.assertRaises(ValueError):
                contracts.validate_request('datadog', 'POST', path,
                                           {key: value for key, value in headers.items() if key != name},
                                           raw, path, key)


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


class SuiteEvidence(unittest.TestCase):
    def test_existing_output_is_preserved_before_any_operation(self):
        for module, negative in ((contracts, 'timeout'), (protocol_local, 'negative')):
            for kind in ('primary', 'negative', 'suite', 'evidence', 'negative-evidence'):
                for entry in ('file', 'symlink', 'dangling-symlink'):
                    with self.subTest(module=module.__name__, kind=kind, entry=entry), tempfile.TemporaryDirectory() as directory:
                        root = pathlib.Path(directory)
                        out = root / 'report.json'
                        paths = {'primary': out, 'negative': root / ('report-' + negative + '.json'),
                                 'suite': root / 'report.suite.json', 'evidence': root / 'report.json.evidence',
                                 'negative-evidence': root / ('report-' + negative + '.json.evidence')}
                        path = paths[kind]
                        original = b'original retained evidence\n'
                        if entry == 'file':
                            if kind.endswith('evidence'):
                                path.mkdir(); (path / 'record').write_bytes(original)
                            else:
                                path.write_bytes(original)
                        else:
                            target = root / 'target'
                            if entry == 'symlink': target.write_bytes(original)
                            path.symlink_to(target)
                        before = {p.name: (p.is_symlink(), p.readlink() if p.is_symlink() else p.is_dir()) for p in root.iterdir()}
                        with mock.patch.object(module.subprocess, 'run') as child, \
                                mock.patch.object(contracts, 'ContractServer') as http, \
                                mock.patch.object(protocol_local.http.server, 'ThreadingHTTPServer') as protocol:
                            with self.assertRaises(FileExistsError): module.run('/unused/binary', out)
                            child.assert_not_called(); http.assert_not_called(); protocol.assert_not_called()
                        self.assertEqual(before, {p.name: (p.is_symlink(), p.readlink() if p.is_symlink() else p.is_dir()) for p in root.iterdir()})
                        if entry != 'dangling-symlink':
                            self.assertEqual((path / 'record').read_bytes() if path.is_dir() else path.read_bytes(), original)

    def test_suite_reservation_is_exclusive_and_private(self):
        with tempfile.TemporaryDirectory() as directory:
            out = pathlib.Path(directory) / 'report.json'
            _, stream = contracts.reserve_suite(out, ('success', 'negative'))
            with stream:
                with self.assertRaises(FileExistsError): contracts.reserve_suite(out, ('success', 'negative'))
                contracts.write_suite(stream, {'passed': False, 'scenarios': []})
            path = out.with_suffix('.suite.json')
            self.assertEqual(json.loads(path.read_text()), {'passed': False, 'scenarios': []})
            self.assertEqual(path.stat().st_mode & 0o077, 0)

    def test_ambiguous_receiver_effect_cannot_be_reported_as_rejected(self):
        state = {'count': 1, 'invalid': 0, 'run_id': RUN_ID}
        for driver in contracts.DRIVERS:
            for mode in ('500', 'malformed_http', 'disconnect', 'timeout'):
                record = {'operation_invoked': True, 'accepted': False, 'status': 'fail', 'outcome': 'unknown', 'duration_ms': 10}
                with self.subTest(driver=driver, mode=mode):
                    self.assertTrue(contracts.scenario_passed(driver, mode, record, state, RUN_ID))
                    for wrong in ('rejected', 'failed', 'accepted', None):
                        self.assertFalse(contracts.scenario_passed(driver, mode, {**record, 'outcome': wrong}, state, RUN_ID))
                    self.assertFalse(contracts.scenario_passed(driver, mode, record, {**state, 'count': 2}, RUN_ID))

    def test_generic_400_and_429_require_provider_specific_rejection_evidence(self):
        state = {'count': 1, 'invalid': 0, 'run_id': RUN_ID}
        rejections = {'400': {'pagerduty', 'opsgenie'}, '429': {'slack', 'pagerduty', 'opsgenie', 'teams', 'twilio'}}
        for mode, rejected in rejections.items():
            for driver in contracts.DRIVERS:
                outcome = 'rejected' if driver in rejected else 'unknown'
                record = {'operation_invoked': True, 'accepted': False, 'status': 'fail', 'outcome': outcome, 'duration_ms': 10}
                with self.subTest(driver=driver, mode=mode):
                    self.assertTrue(contracts.scenario_passed(driver, mode, record, state, RUN_ID))
                    record['outcome'] = 'unknown' if outcome == 'rejected' else 'rejected'
                    self.assertFalse(contracts.scenario_passed(driver, mode, record, state, RUN_ID))


class LocalScenarios(unittest.TestCase):
    def report(self):
        return {'all_providers_verified': False, 'records': [
            {'kind': kind, 'driver': driver, 'configured': True, 'status': 'pass', 'operation_invoked': True,
             'accepted': True, 'observed': True, 'evidence_type': 'local_integration'} for kind, driver in local.ROLES]}

    def test_requires_each_intended_role_and_independent_local_evidence(self):
        self.assertTrue(local.passing_report(self.report()))
        for field, value in (('configured', False), ('operation_invoked', False), ('accepted', False), ('observed', False),
                             ('status', 'fail'), ('evidence_type', 'live_account'), ('driver', 'unrelated')):
            report = self.report(); report['records'][0][field] = value
            with self.subTest(field=field): self.assertFalse(local.passing_report(report))
        report = self.report(); report['all_providers_verified'] = True
        self.assertFalse(local.passing_report(report))

    def test_harness_uses_a_private_root_and_relative_log_filename(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / 'result.json'
            def verify(arguments):
                config = pathlib.Path(arguments[arguments.index('-config') + 1])
                data = json.loads(config.read_text())
                root = pathlib.Path(data['log_directory'])
                self.assertTrue(root.is_absolute() and root.is_dir())
                self.assertEqual(root.stat().st_mode & 0o077, 0)
                self.assertNotEqual(root, config.parent)
                monitor = next(row for row in data['manifest']['monitors'] if row['id'] == 'code-log')
                name = monitor['codes']['red']['config']['file']
                self.assertEqual(name, 'alerts.jsonl')
                case = next(row for row in data['cases'] if row['driver'] == 'log')
                self.assertEqual(pathlib.Path(case['observer'][-1]), root / name)
                output.write_text(json.dumps(self.report()))
                return SimpleNamespace(returncode=2)
            with mock.patch('sys.argv', ['local.py', '--binary', '/unused/verifier', '--out', str(output)]), \
                    mock.patch.object(local.subprocess, 'run', side_effect=verify) as child:
                self.assertEqual(local.main(), 0)
                self.assertEqual(child.call_count, 1)

    def test_existing_report_or_evidence_cannot_redirect_or_repeat_operations(self):
        for suffix in ('', '.evidence'):
            for link in (False, True):
                with self.subTest(suffix=suffix, link=link), tempfile.TemporaryDirectory() as directory:
                    output = pathlib.Path(directory) / 'result.json'
                    existing = pathlib.Path(str(output) + suffix)
                    target = pathlib.Path(directory) / 'must-not-be-created'
                    if link: existing.symlink_to(target)
                    else: existing.write_bytes(b'original evidence')
                    with mock.patch('sys.argv', ['local.py', '--binary', '/unused/verifier', '--out', str(output)]), \
                            mock.patch.object(local.subprocess, 'run') as child, \
                            mock.patch.object(local.http.server, 'ThreadingHTTPServer') as listener:
                        with self.assertRaises(FileExistsError): local.main()
                        child.assert_not_called(); listener.assert_not_called()
                    self.assertFalse(target.exists())
                    if link: self.assertEqual(existing.readlink(), target)
                    else: self.assertEqual(existing.read_bytes(), b'original evidence')


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
