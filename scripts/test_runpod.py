"""RunPod background process lifecycle without database, GPU or installer downloads."""
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import time
import unittest


@unittest.skipUnless(os.name == 'posix', 'Linux process groups required')
class BackgroundTests(unittest.TestCase):
    def test_background_duplicate_status_and_stop(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scripts = root / 'scripts'
            scripts.mkdir()
            launcher = scripts / 'worker-runpod.sh'
            shutil.copyfile(Path(__file__).with_name('worker-runpod.sh'), launcher)
            (root / 'install.sh').write_text('''#!/bin/bash
trap 'exit 0' TERM INT
echo 'fake worker ready'
while true; do sleep 1; done
''')
            app = root / 'runtime'
            env = dict(os.environ, SUBTITLE_DIR=str(app))
            def run(option):
                result = subprocess.run(['bash', str(launcher), option], env=env,
                                        capture_output=True, text=True, timeout=15)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                return result.stdout
            pid = None
            try:
                self.assertIn('Started in background', run('--background'))
                pid = int((app / 'worker.pid').read_text().split()[0])
                self.assertIn('running', run('--status'))
                self.assertIn('Already running', run('--background'))
                self.assertEqual(pid, int((app / 'worker.pid').read_text().split()[0]))
                for _ in range(30):
                    if 'fake worker ready' in (app / 'log/worker.log').read_text():
                        break
                    time.sleep(0.1)
                self.assertIn('fake worker ready', (app / 'log/worker.log').read_text())
                self.assertIn('stopped', run('--stop'))
                self.assertIn('stopped', run('--status'))
                self.assertIn('already stopped', run('--stop'))
                # A reused PID with a different Linux start time is never signalled.
                (app / 'worker.pid').write_text(f'{os.getpid()} 0\n')
                self.assertIn('already stopped', run('--stop'))
            finally:
                if pid:
                    try:
                        os.killpg(pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
