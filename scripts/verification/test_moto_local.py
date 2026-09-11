"""Independent EC2 emulator response-oracle tests; no Moto dependency required."""
import unittest

import moto_local


class MotoResponseTests(unittest.TestCase):
    def test_documented_moto_success_without_return_element(self):
        body = b'<RebootInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>request-123</requestId></RebootInstancesResponse>'
        self.assertEqual(moto_local.response_kind(200, body), 'count')

    def test_success_can_include_true_return(self):
        body = b'<RebootInstancesResponse><requestId>request-123</requestId><return>true</return></RebootInstancesResponse>'
        self.assertEqual(moto_local.response_kind(200, body), 'count')

    def test_explicit_false_return_is_not_success(self):
        for value in ('false', 'unexpected', ''):
            body = ('<RebootInstancesResponse><requestId>request-123</requestId><return>' + value + '</return></RebootInstancesResponse>').encode()
            with self.subTest(value=value):
                self.assertEqual(moto_local.response_kind(200, body), 'invalid')

    def test_missing_empty_or_whitespace_request_id_is_invalid(self):
        for inner in ('', '<requestId/>', '<requestId> </requestId>'):
            with self.subTest(inner=inner):
                self.assertEqual(moto_local.response_kind(200, ('<RebootInstancesResponse>' + inner + '</RebootInstancesResponse>').encode()), 'invalid')

    def test_invalid_xml_or_unrelated_success_cannot_increment_count(self):
        for body in (b'', b'{"ok":true}', b'<RebootInstancesResponse>',
                     b'<DescribeInstancesResponse><requestId>123</requestId></DescribeInstancesResponse>',
                     b'<Response><Errors><Error><Code>InvalidInstanceID.NotFound</Code></Error></Errors></Response>'):
            with self.subTest(body=body):
                self.assertEqual(moto_local.response_kind(200, body), 'invalid')

    def test_http_failure_cannot_be_counted_as_accepted_reboot(self):
        body = b'<RebootInstancesResponse><requestId>request-123</requestId></RebootInstancesResponse>'
        for status in (201, 400, 401, 429, 500, 503):
            with self.subTest(status=status):
                self.assertEqual(moto_local.response_kind(status, body), 'invalid')

    def test_exact_invalid_instance_error_is_observed_rejection(self):
        body = b'<Response><Errors><Error><Code>InvalidInstanceID.NotFound</Code><Message>The instance does not exist</Message></Error></Errors><RequestID>request-123</RequestID></Response>'
        self.assertEqual(moto_local.response_kind(400, body), 'rejected')
        for status in (200, 403, 500):
            self.assertEqual(moto_local.response_kind(status, body), 'invalid')

    def test_unrelated_auth_error_cannot_satisfy_missing_instance_scenario(self):
        for code in ('UnauthorizedOperation', 'AuthFailure', 'InvalidAction', ''):
            body = ('<Response><Errors><Error><Code>' + code + '</Code></Error></Errors></Response>').encode()
            with self.subTest(code=code):
                self.assertEqual(moto_local.response_kind(400, body), 'invalid')


if __name__ == '__main__':
    unittest.main()
