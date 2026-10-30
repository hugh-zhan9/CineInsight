"""Exercise the packaging entry point without compiling or touching a keychain."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class BuildSigningSetupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.scripts = self.root / 'scripts'
        self.scripts.mkdir()
        shutil.copy(Path(__file__).with_name('build_and_install_app.sh'), self.scripts)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.log = self.root / 'events'
        self.config = self.root / 'identity'
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ['PATH'],
                        TEST_EVENTS=str(self.log), TEST_IDENTITY=str(self.config),
                        TEST_ROOT=str(self.root), TEST_SETUP_FAIL='0', TEST_SIGN_FAIL='0')
        self.stub('uname', 'echo Darwin')
        self.stub('xcrun', 'echo 14.0')
        self.stub('wails', '''echo build >> "$TEST_EVENTS"
mkdir -p "$TEST_ROOT/build/bin/析微影策.app"''')
        self.stub('python3', '''case "$2" in
setup)
  echo setup >> "$TEST_EVENTS"
  [ "$TEST_SETUP_FAIL" = 0 ] || exit 42
  if [ ! -f "$TEST_IDENTITY" ]; then
    echo pinned > "$TEST_IDENTITY"
    echo created >> "$TEST_EVENTS"
  fi
  ;;
sign)
  echo sign >> "$TEST_EVENTS"
  [ -f "$TEST_IDENTITY" ] || exit 41
  [ "$TEST_SIGN_FAIL" = 0 ] || exit 43
  ;;
*) exit 99 ;;
esac''')
        (self.scripts / 'build_apple_ai.sh').write_text('echo apple >> "$TEST_EVENTS"\n')
        # A test must never install or close the user's real application.
        for name in ('ditto', 'pkill', 'sudo', 'open'):
            self.stub(name, 'echo forbidden-install >> "$TEST_EVENTS"; exit 98')

    def stub(self, name, script):
        path = self.bin / name
        path.write_text('#!/bin/sh\nset -eu\n' + script + '\n')
        path.chmod(0o755)

    def run_build(self, *args):
        return subprocess.run(['bash', str(self.scripts / 'build_and_install_app.sh'), *args],
                              env=self.env, capture_output=True, text=True, timeout=10)

    def events(self):
        return self.log.read_text().splitlines() if self.log.exists() else []

    def test_first_build_initializes_before_compilation_and_signs(self):
        result = self.run_build('--build-only')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.events(), ['setup', 'created', 'apple', 'build', 'sign'])

    def test_repeated_build_reuses_identity(self):
        self.config.write_text('original identity')
        for _ in range(2):
            result = self.run_build('--build-only')
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.config.read_text(), 'original identity')
        self.assertEqual(self.events(), ['setup', 'apple', 'build', 'sign'] * 2)

    def test_setup_failure_stops_before_build_and_install(self):
        self.env['TEST_SETUP_FAIL'] = '1'
        result = self.run_build()
        self.assertEqual(result.returncode, 42, result.stderr)
        self.assertEqual(self.events(), ['setup'])

    def test_skip_build_still_initializes_and_sign_failure_blocks_install(self):
        (self.root / 'build/bin/析微影策.app').mkdir(parents=True)
        self.env['TEST_SIGN_FAIL'] = '1'
        result = self.run_build('--skip-build')
        self.assertEqual(result.returncode, 43, result.stderr)
        self.assertEqual(self.events(), ['setup', 'created', 'sign'])

    def test_help_and_invalid_options_do_not_initialize(self):
        self.assertEqual(self.run_build('--help').returncode, 0)
        self.assertNotEqual(self.run_build('--skip-build', '--build-only').returncode, 0)
        self.assertEqual(self.events(), [])


if __name__ == '__main__':
    unittest.main()
