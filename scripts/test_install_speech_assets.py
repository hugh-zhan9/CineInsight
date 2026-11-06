"""Run the real speech installer with isolated preferences and a fake Apple helper."""
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import unittest


class SpeechAssetInstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.prefs = self.root / 'preferences.plist'
        self.original = plistlib.dumps({'AppleEnabledInputSources': [], 'unrelated': 'keep'})
        self.prefs.write_bytes(self.original)
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ['PATH'],
                        TMPDIR=str(self.root), SPEECH_TEST_ROOT=str(self.root),
                        SPEECH_TEST_INSTALLED='en,zh', SPEECH_TEST_DOWNLOAD='1')
        self.stub('uname', '#!/bin/sh\necho Darwin\n')
        self.stub('killall', '#!/bin/sh\nexit 0\n')
        self.helper = self.stub('helper', f'#!{sys.executable}\n' + '''
import json, os, pathlib, sys
assert json.loads(sys.stdin.read()) == {'operation': 'status'}
root = pathlib.Path(os.environ['SPEECH_TEST_ROOT'])
installed = set(os.environ['SPEECH_TEST_INSTALLED'].split(','))
if (root / 'downloaded').exists(): installed.update(['ja', 'ko'])
print(json.dumps({'languages': [{'code': code + '-XX', 'installed': code in installed} for code in ['ja', 'ko', 'en', 'zh']]}))
''')
        self.stub('defaults', f'#!{sys.executable}\n' + '''
import os, pathlib, plistlib, sys
root = pathlib.Path(os.environ['SPEECH_TEST_ROOT'])
prefs = root / 'preferences.plist'
operation, domain, path = sys.argv[1:]
assert domain == 'com.apple.HIToolbox'
with (root / 'commands').open('a') as log: log.write(operation + '\\n')
if operation == 'export': pathlib.Path(path).write_bytes(prefs.read_bytes())
elif operation == 'import':
    prefs.write_bytes(pathlib.Path(path).read_bytes())
    entries = plistlib.loads(prefs.read_bytes())['AppleEnabledInputSources']
    if entries and os.environ['SPEECH_TEST_DOWNLOAD'] == '1': (root / 'downloaded').touch()
else: raise SystemExit(90)
''')

    def stub(self, name, source):
        path = self.bin / name
        path.write_text(source)
        path.chmod(0o755)
        return path

    def run_installer(self, *args):
        return subprocess.run(['bash', str(Path(__file__).with_name('install_speech_assets.sh').resolve()),
                               '--helper', str(self.helper), '--languages', 'ja,ko,en,zh', *args],
                              env=self.env, capture_output=True, text=True, timeout=10)

    def test_installed_resources_do_not_touch_preferences(self):
        self.env['SPEECH_TEST_INSTALLED'] = 'ja,ko,en,zh'
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('都已是本机可用', result.stdout)
        self.assertFalse((self.root / 'commands').exists())
        self.assertEqual(self.prefs.read_bytes(), self.original)

    def test_missing_resources_are_installed_and_preferences_restored(self):
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.root / 'downloaded').exists())
        self.assertEqual(self.prefs.read_bytes(), self.original)
        self.assertEqual((self.root / 'commands').read_text().splitlines(), ['export', 'import', 'import'])

    def test_timeout_returns_failure_and_restores_preferences(self):
        self.env['SPEECH_TEST_DOWNLOAD'] = '0'
        result = self.run_installer('--timeout', '0')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('超时', result.stderr)
        self.assertEqual(self.prefs.read_bytes(), self.original)


if __name__ == '__main__':
    unittest.main()
