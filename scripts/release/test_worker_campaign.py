"""Real process-group cleanup for local verification fixtures."""
import json
import os
from pathlib import Path
import signal
import sys
import tempfile
import time
import unittest

import worker_campaign


@unittest.skipUnless(sys.platform == 'linux', 'process identity fixture uses Linux procfs')
class CaptureProcessTests(unittest.TestCase):
    def test_capture_stops_child_after_success_or_failure(self):
        for status in (0, 1):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                identity = root / 'child.json'
                program = root / 'parent.py'
                # A normal test exit and a test panic can both leave children
                # alive. Record exact identity so the test cannot kill a reused PID.
                program.write_text(
                    'import json,os,subprocess,sys\n'
                    'from pathlib import Path\n'
                    "child=subprocess.Popen([sys.executable,'-c','import time;time.sleep(60)'])\n"
                    'stat=Path("/proc",str(child.pid),"stat").read_text().split()\n'
                    f'Path({str(identity)!r}).write_text(json.dumps({{"pid":child.pid,"start_ticks":stat[21]}}))\n'
                    f'sys.exit({status})\n')
                try:
                    result = worker_campaign.capture([sys.executable, '-B', str(program)], root,
                                                     dict(os.environ), root / 'stdout', root / 'stderr', 5)
                    self.assertEqual(result, status, 'cleanup changed the original exit status')
                    recorded = json.loads(identity.read_text())
                    stat = Path('/proc', str(recorded['pid']), 'stat')
                    deadline = time.monotonic() + 1
                    while True:
                        try:
                            state = stat.read_text().split()
                        except FileNotFoundError:
                            break
                        if state[21] != recorded['start_ticks'] or state[2] == 'Z':
                            break
                        if time.monotonic() >= deadline:
                            self.fail('capture left its fixture child running after signal delivery')
                        time.sleep(.01)
                finally:
                    if identity.exists():
                        recorded = json.loads(identity.read_text())
                        stat = Path('/proc', str(recorded['pid']), 'stat')
                        try:
                            state = stat.read_text().split()
                        except FileNotFoundError:
                            state = None
                        if state is not None:
                            if state[21] == recorded['start_ticks'] and state[2] != 'Z':
                                os.kill(recorded['pid'], signal.SIGKILL)
