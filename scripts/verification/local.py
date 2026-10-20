#!/usr/bin/env python3
"""Run real HTTP and file drivers against a separate local target process boundary.

The HTTP target owns independent counters. This is evidence for these local
drivers only; the other inventory entries remain not configured.
"""
import argparse
import http.server
import json
import pathlib
import subprocess
import sys
import tempfile
import threading


ROLES = {('pulse', 'http'), ('code', 'webhook'), ('code', 'log'), ('intervention', 'webhook')}


def passing_report(report):
    configured = [record for record in report['records'] if record.get('configured')]
    return (len(configured) == len(ROLES)
            and {(record['kind'], record['driver']) for record in configured} == ROLES
            and all(record['status'] == 'pass' and record.get('operation_invoked') is True
                    and record.get('accepted') is True and record.get('observed') is True
                    and record.get('evidence_type') == 'local_integration' for record in configured)
            and report.get('all_providers_verified') is False)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    output = pathlib.Path(args.out).absolute()
    if any(path.exists() or path.is_symlink() for path in (output, pathlib.Path(str(output) + '.evidence'))):
        raise FileExistsError('select fresh report and evidence paths before running the fixture')
    counts = {'http': 0, 'notify': 0, 'recover': 0}
    lock = threading.Lock()

    class Target(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == '/audit':
                with lock:
                    data = json.dumps(counts).encode()
                self.send_response(200)
                self.end_headers()
                self.wfile.write(data)
            else:
                with lock:
                    counts['http'] += 1
                self.send_response(204)
                self.end_headers()

        def do_POST(self):
            length = int(self.headers.get('Content-Length', '0'))
            if length > 65536:
                self.send_error(413)
                return
            self.rfile.read(length)
            kind = 'recover' if self.path == '/recover' else 'notify'
            with lock:
                counts[kind] += 1
            self.send_response(204)
            self.end_headers()

        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Target)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    url = f'http://127.0.0.1:{server.server_port}'
    observer = str(pathlib.Path(__file__).with_name('observe_local.py').resolve())
    try:
        with tempfile.TemporaryDirectory(prefix='cpra-live-') as directory:
            log_directory = pathlib.Path(directory) / 'notifications'
            log_directory.mkdir(mode=0o700)
            log_name = 'alerts.jsonl'
            log = str(log_directory / log_name)
            monitors, cases = [], []
            for kind, driver, observation in [('pulse', 'http', 'http'), ('code', 'webhook', 'notify'), ('code', 'log', 'log'), ('intervention', 'webhook', 'recover')]:
                ident = kind + '-' + driver
                monitor = {'id': ident, 'name': ident, 'pulse_check': {'type': 'http', 'interval': '60s', 'timeout': '5s', 'config': {'url': url + '/health', 'expected_status': [204]}}}
                if kind == 'code':
                    monitor['codes'] = {'red': {'dispatch': True, 'notify': driver, 'config': {'file': log_name} if driver == 'log' else {'url': url + '/notify'}}}
                if kind == 'intervention':
                    monitor['intervention'] = {'action': 'webhook', 'target': {'type': 'webhook', 'url': url + '/recover', 'timeout': '5s'}}
                monitors.append(monitor)
                cases.append({'kind': kind, 'driver': driver, 'configured': True, 'evidence_type': 'local_integration', 'monitor_id': ident, 'color': 'red', 'observer': [sys.executable, observer, observation, log if observation == 'log' else url]})
            config = pathlib.Path(directory) / 'live.yaml'
            config.write_text(json.dumps({'log_directory': str(log_directory), 'manifest': {'monitors': monitors}, 'cases': cases}))
            result = subprocess.run([str(pathlib.Path(args.binary).resolve()), '-live', '-config', str(config), '-out', str(output)])
            if result.returncode not in (0, 2):
                return result.returncode
            report = json.loads(output.read_text())
            if not passing_report(report):
                return 1
            print('Four local scenarios passed. Full-provider verification remains incomplete.')
            return 0
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


if __name__ == '__main__':
    raise SystemExit(main())
