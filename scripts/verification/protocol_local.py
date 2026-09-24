#!/usr/bin/env python3
"""Run real loopback HTTP/TCP/UDP/DNS/TLS/SMTP driver scenarios.

The gRPC scenario tests only CPRa's documented TCP reachability boundary, not a
gRPC health RPC. TLS uses a temporary trusted certificate (verification stays
enabled). The SMTP receiver validates the envelope and captures an actual DATA
message; it is an isolated local SMTP integration, not external email delivery.
"""
import argparse
import email
import email.utils
import hashlib
import http.server
import json
import os
import pathlib
import socket
import socketserver
import ssl
import struct
import subprocess
import sys
import tempfile
import threading

from contracts import RUN_PATTERN


class Audit:
    def __init__(self):
        self.lock = threading.Lock()
        self.records = {}

    def register(self, key, exact=True):
        self.records[key] = {'count': 0, 'invalid': 0, 'digest': '', 'run_id': '', 'exact': exact}

    def record(self, key, data=b'', run_id='', valid=True):
        with self.lock:
            record = self.records[key]
            record['count' if valid else 'invalid'] += 1
            record['digest'] = hashlib.sha256(data).hexdigest()
            record['run_id'] = run_id

    def get(self, key):
        with self.lock:
            return dict(self.records[key])


def dns_response(request, success):
    """Answer exactly the local test name for A/AAAA; reject malformed queries."""
    if len(request) < 17 or struct.unpack('!H', request[4:6])[0] != 1:
        raise ValueError('invalid DNS header')
    offset, labels = 12, []
    while request[offset]:
        length = request[offset]
        if length > 63 or offset + length + 1 >= len(request):
            raise ValueError('invalid DNS label')
        labels.append(request[offset + 1:offset + 1 + length].decode('ascii'))
        offset += length + 1
    offset += 1
    qtype, qclass = struct.unpack('!HH', request[offset:offset + 4])
    if '.'.join(labels) != 'cpra.fixture.test' or qtype not in (1, 28) or qclass != 1:
        raise ValueError('unexpected DNS question')
    question = request[12:offset + 4]
    flags = 0x8180 if success else 0x8183
    header = request[:2] + struct.pack('!HHHHH', flags, 1, int(success), 0, 0)
    address = socket.inet_pton(socket.AF_INET if qtype == 1 else socket.AF_INET6,
                               '127.0.0.1' if qtype == 1 else '::1')
    answer = b'\xc0\x0c' + struct.pack('!HHIH', qtype, 1, 0, len(address)) + address
    return header + question + (answer if success else b'')


def validate_mail(sender, recipient, raw, monitor):
    if sender.upper() != 'MAIL FROM:<SENDER@CPRA.TEST>' or recipient.upper() != 'RCPT TO:<RECEIVER@CPRA.TEST>':
        raise ValueError('SMTP envelope mismatch')
    message = email.message_from_bytes(raw)
    if (email.utils.parseaddr(message['From'])[1] != 'sender@cpra.test' or
            email.utils.parseaddr(message['To'])[1] != 'receiver@cpra.test' or
            message['Subject'] != 'CPRa local fixture'):
        raise ValueError('SMTP headers mismatch')
    body = message.get_payload(decode=True).decode('utf-8')
    match = RUN_PATTERN.search(body)
    if not match or 'Monitor: ' + monitor + ' [CPRa verification ' + match.group(1) + ']' not in body:
        raise ValueError('SMTP message correlation missing')
    return match.group(1)


def scenario_passed(driver, mode, record, state):
    # A failed precondition is not evidence that the production driver rejected
    # a closed TCP target: assert admission even when no server can accept it.
    expected_count = 0 if mode == 'negative' and driver in ('tcp', 'grpc') else 1
    counted = state['count'] >= expected_count if driver == 'dns' else state['count'] == expected_count
    return (record.get('operation_invoked') is True and counted and state['invalid'] == 0
            and record['status'] == ('pass' if mode == 'success' else 'fail')
            and record['accepted'] == (mode == 'success') and record['duration_ms'] < 4000)


class ThreadTCP(socketserver.ThreadingTCPServer):
    daemon_threads = True
    allow_reuse_address = True


class ThreadUDP(socketserver.ThreadingUDPServer):
    daemon_threads = True


def target_handler(driver, mode, audit, key, tls_context=None):
    class Handler(socketserver.StreamRequestHandler):
        def handle(self):
            self.connection.settimeout(2)
            if driver in ('tcp', 'grpc'):
                audit.record(key)
                return
            if driver == 'tls':
                try:
                    with tls_context.wrap_socket(self.connection, server_side=True):
                        audit.record(key, b'tls-handshake')
                except (OSError, ssl.SSLError):
                    audit.record(key, valid=False)
                return
            if driver == 'email':
                self.wfile.write(b'220 cpra.fixture.test ESMTP\r\n')
                sender = recipient = ''
                for _ in range(20):
                    raw = self.rfile.readline(65537)
                    if not raw or len(raw) > 65536:
                        return
                    line = raw.decode('ascii').strip()
                    command = line.split(' ', 1)[0].upper()
                    if command in ('EHLO', 'HELO'):
                        self.wfile.write(b'250 cpra.fixture.test\r\n')
                    elif command == 'MAIL':
                        sender = line
                        self.wfile.write(b'250 sender accepted\r\n')
                    elif command == 'RCPT':
                        recipient = line
                        self.wfile.write(b'250 recipient accepted\r\n')
                    elif command == 'DATA':
                        self.wfile.write(b'354 send data\r\n')
                        body = bytearray()
                        while len(body) <= 65536:
                            fragment = self.rfile.readline(65537)
                            if fragment == b'.\r\n':
                                break
                            if not fragment:
                                return
                            body.extend(fragment[1:] if fragment.startswith(b'..') else fragment)
                        try:
                            run_id = validate_mail(sender, recipient, bytes(body), key)
                            audit.record(key, bytes(body), run_id)
                        except (ValueError, TypeError, UnicodeError):
                            audit.record(key, valid=False)
                            self.wfile.write(b'550 invalid fixture envelope or message\r\n')
                            return
                        self.wfile.write(b'250 message captured\r\n' if mode == 'success' else b'550 fixture rejection\r\n')
                    elif command == 'QUIT':
                        self.wfile.write(b'221 bye\r\n')
                        return
                    else:
                        audit.record(key, valid=False)
                        self.wfile.write(b'500 unsupported command\r\n')
                        return
    return Handler


def datagram_handler(driver, mode, audit, key):
    class Handler(socketserver.BaseRequestHandler):
        def handle(self):
            data, sock = self.request
            try:
                if driver == 'dns':
                    response = dns_response(data, mode == 'success')
                else:
                    if data != ('cpra-probe-' + key).encode():
                        raise ValueError('UDP payload mismatch')
                    response = b'cpra-reply'
                audit.record(key, data)
                if driver == 'dns' or mode == 'success':
                    sock.sendto(response, self.client_address)
            except (ValueError, IndexError, struct.error, UnicodeError):
                audit.record(key, data, valid=False)
    return Handler


def run(binary, out):
    out = pathlib.Path(out).resolve()
    out.parent.mkdir(parents=True, exist_ok=True)
    audit, servers, threads, reservations = Audit(), [], [], []
    observer = pathlib.Path(__file__).with_name('observe_fixture.py').resolve()
    suite = {'evidence_type': 'local_integration', 'provider_certification': False, 'scenarios': [],
             'binary_sha256': hashlib.sha256(pathlib.Path(binary).read_bytes()).hexdigest(),
             'limitations': ['gRPC checks TCP reachability only; no gRPC health RPC is implemented.',
                             'SMTP receipt is verified at the local relay; no external inbox is involved.']}

    class HTTPHandler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            if self.path.startswith('/audit/'):
                try:
                    body = json.dumps(audit.get(self.path.removeprefix('/audit/'))).encode()
                except KeyError:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            elif self.path in ('/http-success', '/http-negative'):
                audit.record(self.path[1:], b'GET ' + self.path.encode())
                self.send_response(204 if self.path == '/http-success' else 503)
                self.end_headers()
            else:
                self.send_error(404)

    def start(server):
        servers.append(server)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        threads.append(thread)
        thread.start()
        return server.server_address[1]

    http_port = start(http.server.ThreadingHTTPServer(('127.0.0.1', 0), HTTPHandler))
    try:
        with tempfile.TemporaryDirectory(prefix='cpra-protocol-') as directory:
            directory = pathlib.Path(directory)
            cert, private_key = directory / 'ca.pem', directory / 'server.key'
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '365',
                            '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1',
                            '-keyout', str(private_key), '-out', str(cert)], check=True, capture_output=True)
            tls_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            tls_context.minimum_version = tls_context.maximum_version = ssl.TLSVersion.TLSv1_2
            tls_context.load_cert_chain(cert, private_key)
            for mode in ('success', 'negative'):
                monitors, cases = [], []
                for driver in ('http', 'tcp', 'udp', 'dns', 'tls', 'grpc', 'email'):
                    key = driver + '-' + mode
                    audit.register(key, exact=driver != 'dns')
                    if driver == 'http':
                        config = {'url': f'http://127.0.0.1:{http_port}/{key}', 'expected_status': [204]}
                    else:
                        if driver in ('tcp', 'grpc') and mode == 'negative':
                            # A bound, non-listening socket reserves a kernel-rejected
                            # endpoint so another process cannot take this test port.
                            reservation = socket.socket()
                            reservation.bind(('127.0.0.1', 0))
                            reservations.append(reservation)
                            port = reservation.getsockname()[1]
                        elif driver in ('udp', 'dns'):
                            port = start(ThreadUDP(('127.0.0.1', 0), datagram_handler(driver, mode, audit, key)))
                        else:
                            port = start(ThreadTCP(('127.0.0.1', 0), target_handler(driver, mode, audit, key, tls_context)))
                        config = {'host': '127.0.0.1', 'port': port}
                        if driver == 'udp':
                            config['payload'] = 'cpra-probe-' + key
                        elif driver == 'dns':
                            # Absolute name prevents host search-domain expansion
                            # from sending unrelated questions after NXDOMAIN.
                            config = {'host': 'cpra.fixture.test.', 'server': f'127.0.0.1:{port}'}
                        elif driver == 'tls':
                            config.update(server_name='localhost', critical_days=1 if mode == 'success' else 730)
                        elif driver == 'email':
                            config = {'server': f'127.0.0.1:{port}', 'from': 'sender@cpra.test',
                                      'to': 'receiver@cpra.test', 'subject': 'CPRa local fixture', 'allow_insecure': True}
                    monitor = {'id': key, 'name': key,
                               'pulse_check': {'type': driver if driver != 'email' else 'http', 'interval': '60s',
                                               'timeout': '500ms', 'config': config if driver != 'email' else {'url': f'http://127.0.0.1:{http_port}/http-success'}}}
                    if driver == 'email':
                        monitor['codes'] = {'red': {'dispatch': True, 'notify': 'email', 'config': config}}
                    monitors.append(monitor)
                    cases.append({'kind': 'code' if driver == 'email' else 'pulse', 'driver': driver,
                                  'configured': True, 'monitor_id': key, 'color': 'red', 'evidence_type': 'local_integration',
                                  'timeout': '3s', 'observer': [sys.executable, str(observer), f'http://127.0.0.1:{http_port}/audit/{key}']})
                config_file = directory / (mode + '.yaml')
                config_file.write_text(json.dumps({'manifest': {'monitors': monitors}, 'cases': cases}))
                report_path = out if mode == 'success' else out.with_name(out.stem + '-negative.json')
                result = subprocess.run([str(pathlib.Path(binary).resolve()), '-live', '-config', str(config_file), '-out', str(report_path)],
                                        capture_output=True, text=True, timeout=30,
                                        env={**os.environ, 'SSL_CERT_FILE': str(cert)})
                if result.returncode not in (0, 2):
                    raise RuntimeError(f'cpra-verify exited {result.returncode}: {result.stderr[:1000]}')
                report = json.loads(report_path.read_text())
                for case in cases:
                    driver = case['driver']
                    record = next(r for r in report['records'] if r['driver'] == driver and r['kind'] == case['kind'])
                    state = audit.get(case['monitor_id'])
                    expected_count = 0 if mode == 'negative' and driver in ('tcp', 'grpc') else 1
                    passed = scenario_passed(driver, mode, record, state)
                    suite['scenarios'].append({'kind': case['kind'], 'driver': driver, 'mode': mode,
                                               'passed': passed, 'operations_observed': state['count'],
                                               'invalid_operations': state['invalid'], 'operation_sha256': state['digest'],
                                               'driver_accepted': record['accepted'], 'duration_ms': record['duration_ms'],
                                               'driver_status': record['status'], 'reason': record.get('reason', ''),
                                               'operation_invoked': record.get('operation_invoked', False),
                                               'bound_non_listening_target': expected_count == 0, 'report': report_path.name})
                print(f'{mode}: {sum(s["passed"] for s in suite["scenarios"] if s["mode"] == mode)}/{len(cases)} protocol cases passed')
    finally:
        for server in servers:
            server.shutdown()
            server.server_close()
        for thread in threads:
            thread.join()
        for reservation in reservations:
            reservation.close()
        suite['passed'] = len(suite['scenarios']) == 14 and all(s['passed'] for s in suite['scenarios'])
        out.with_suffix('.suite.json').write_text(json.dumps(suite, indent=2) + '\n')
    return 0 if suite['passed'] else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    return run(args.binary, args.out)


if __name__ == '__main__':
    raise SystemExit(main())
