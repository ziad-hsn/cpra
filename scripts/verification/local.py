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


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
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
            log = str(pathlib.Path(directory) / 'alerts.jsonl')
            monitors, cases = [], []
            for kind, driver, observation in [('pulse', 'http', 'http'), ('code', 'webhook', 'notify'), ('code', 'log', 'log'), ('intervention', 'webhook', 'recover')]:
                ident = kind + '-' + driver
                monitor = {'id': ident, 'name': ident, 'pulse_check': {'type': 'http', 'interval': '60s', 'timeout': '5s', 'config': {'url': url + '/health', 'expected_status': [204]}}}
                if kind == 'code':
                    monitor['codes'] = {'red': {'dispatch': True, 'notify': driver, 'config': {'file': log} if driver == 'log' else {'url': url + '/notify'}}}
                if kind == 'intervention':
                    monitor['intervention'] = {'action': 'webhook', 'target': {'type': 'webhook', 'url': url + '/recover', 'timeout': '5s'}}
                monitors.append(monitor)
                cases.append({'kind': kind, 'driver': driver, 'configured': True, 'monitor_id': ident, 'color': 'red', 'observer': [sys.executable, observer, observation, log if observation == 'log' else url]})
            config = pathlib.Path(directory) / 'live.yaml'
            config.write_text(json.dumps({'manifest': {'monitors': monitors}, 'cases': cases}))
            result = subprocess.run([str(pathlib.Path(args.binary).resolve()), '-live', '-config', str(config), '-out', str(pathlib.Path(args.out).resolve())])
            if result.returncode not in (0, 2):
                return result.returncode
            report = json.loads(pathlib.Path(args.out).read_text())
            configured = [r for r in report['records'] if r['status'] != 'not_configured']
            if len(configured) != 4 or any(r['status'] != 'pass' for r in configured):
                return 1
            print('Four local scenarios passed. Full-provider verification remains incomplete.')
            return 0
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


if __name__ == '__main__':
    raise SystemExit(main())
