"""Check serial order, fail-fast, arguments and unsupported platforms without compiling."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

class RunnerTest(unittest.TestCase):
    def run_fixture(self, platform='Darwin', fail='', args=()):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            shutil.copy(Path(__file__).with_name('test_macos_webkit.sh'), root)
            uname = root / 'uname'
            uname.write_text('#!/bin/sh\necho '+platform+'\n'); uname.chmod(0o700)
            for suite in ['virtual_window','viewing_notes','playback_queue','video_crop']:
                (root/f'test_{suite}_webkit.sh').write_text(f'echo {suite} >> "{root}/order"\nexit '+('7' if suite==fail else '0')+'\n')
            result = subprocess.run(['bash',str(root/'test_macos_webkit.sh'),*args], env={**os.environ,'PATH':str(root)+':'+os.environ['PATH']},capture_output=True,text=True)
            return result.returncode, (root/'order').read_text().splitlines() if (root/'order').exists() else []
    def test_success(self):
        self.assertEqual(self.run_fixture(),(0,['virtual_window','viewing_notes','playback_queue','video_crop']))
    def test_failure(self):
        self.assertEqual(self.run_fixture(fail='viewing_notes'),(7,['virtual_window','viewing_notes']))
    def test_platform(self):
        self.assertEqual(self.run_fixture(platform='Linux'),(2,[]))
    def test_args(self):
        self.assertEqual(self.run_fixture(args=['unknown']),(2,[]))
if __name__=='__main__': unittest.main()
