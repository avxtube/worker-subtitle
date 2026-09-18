"""Installer integration checks with fake releases; no GPU, network or root needed."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


@unittest.skipUnless(os.name == 'posix', 'Linux installer integration tests')
class InstallerTests(unittest.TestCase):
    def test_verified_install_upgrade_and_corrupt_asset(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tools = root / 'tools'
            tools.mkdir()
            app = root / 'app'
            fixture = root / 'fixture'
            fixture.mkdir()
            binary = b'#!/bin/sh\nexit 0\n'
            (fixture / 'linux').write_bytes(binary)
            (fixture / 'SHA256SUMS').write_text(hashlib.sha256(binary).hexdigest() + '  linux\n')
            (fixture / 'release.json').write_text(json.dumps({'tag_name': 'v0.1.0', 'assets': [
                {'name': name, 'url': 'https://example.invalid/' + name}
                for name in ('linux', 'SHA256SUMS')]}))
            mocks = {
                'uname': '#!/bin/sh\nif [ "$1" = -s ]; then echo Linux; else echo x86_64; fi\n',
                'nvidia-smi': '#!/bin/sh\nexit 0\n',
                'ffmpeg': '#!/bin/sh\nexit 0\n',
                'ffprobe': '#!/bin/sh\nexit 0\n',
                'curl': '''#!/bin/bash
url= out=
while (($#)); do
 case "$1" in
 -o|--output) out="$2"; shift 2;;
 https://*) url="$1"; shift;;
 *) shift;;
 esac
done
case "$url" in
 */releases/*) cp "$FIXTURE/release.json" "$out";;
 *) cp "$FIXTURE/${url##*/}" "$out";;
esac
'''}
            for name, script in mocks.items():
                path = tools / name
                path.write_text(script)
                path.chmod(0o755)
            env = dict(os.environ, PATH=str(tools) + ':' + os.environ['PATH'],
                       FIXTURE=str(fixture), DATABASE_URL='mongodb://localhost/test',
                       STORAGE_ENCRYPTION_KEY='test-key')
            command = ['bash', str(Path(__file__).resolve().parents[1] / 'install.sh'),
                       '--mode', 'runpod', '--skip-deps', '--no-start', '--dir', str(app)]
            def run():
                return subprocess.run(command, env=env, capture_output=True, text=True)
            result = run()
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual((app / 'linux').read_bytes(), binary)
            self.assertEqual((app / '.env').stat().st_mode & 0o777, 0o600)
            config = (app / '.env').read_bytes()
            model = app / 'models' / 'keep'
            model.write_text('retain')
            env['DATABASE_URL'] = 'mongodb://changed/test'
            result = run()
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual((app / '.env').read_bytes(), config)
            self.assertEqual(model.read_text(), 'retain')
            (fixture / 'linux').write_text('corrupt')
            self.assertNotEqual(run().returncode, 0)
            self.assertEqual((app / 'linux').read_bytes(), binary)
