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
                        TEST_ROOT=str(self.root), TEST_SETUP_FAIL='0', TEST_SIGN_FAIL='0', TEST_IMAGE_TOOLS_FAIL='0',
                        TEST_SPEECH_MISSING='', TEST_SPEECH_FAIL='0', TEST_SPEECH_STILL_MISSING='0', TEST_SPEECH_CHECK_FAIL='0')
        self.stub('uname', 'echo Darwin')
        self.stub('xcrun', 'echo 14.0')
        self.stub('wails', '''for arg in "$@"; do [ "$arg" != --skip-speech-assets ] || exit 92; done
echo build >> "$TEST_EVENTS"
mkdir -p "$TEST_ROOT/build/bin/析微影策.app"''')
        self.stub('python3', '''case "$1" in
*/speech_assets.py)
  echo speech-check >> "$TEST_EVENTS"
  [ "$TEST_SPEECH_CHECK_FAIL" = 0 ] || exit 47
  [ "$2" = missing ] && [ "$3" = --helper ] || exit 97
  [ "$4" = "$TEST_ROOT/build/bin/析微影策.app/Contents/Resources/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai" ] || exit 96
  [ "$5" = --languages ] && [ "$6" = ja,ko,en,zh ] || exit 95
  if [ ! -f "$TEST_ROOT/speech-ready" ]; then printf '%s\n' "$TEST_SPEECH_MISSING"; fi
  exit 0 ;;
*bundle_native_playback.py) echo native-playback >> "$TEST_EVENTS"; [ "${TEST_NATIVE_FAIL:-0}" = 0 ] || exit 45; exit 0 ;;
*/bundle_image_tools.py)
  echo image-tools >> "$TEST_EVENTS"
  [ "$TEST_IMAGE_TOOLS_FAIL" = 0 ] || exit 44
  exit 0
  ;;
esac
case "$2" in
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
        (self.scripts / 'install_speech_assets.sh').write_text("""set -eu
 echo speech-install >> "$TEST_EVENTS"
 [ "$1" = --helper ] && [ "$2" = "$TEST_ROOT/build/bin/析微影策.app/Contents/Resources/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai" ] || exit 94
 [ "$3" = --languages ] && [ "$4" = ja,ko,en,zh ] || exit 93
 [ "$TEST_SPEECH_FAIL" = 0 ] || exit 46
 if [ "$TEST_SPEECH_STILL_MISSING" = 0 ]; then touch "$TEST_ROOT/speech-ready"; fi
""")
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
        self.assertEqual(self.events(), ['setup', 'created', 'apple', 'build', 'native-playback', 'image-tools', 'sign'])

    def test_repeated_build_reuses_identity(self):
        self.config.write_text('original identity')
        for _ in range(2):
            result = self.run_build('--build-only')
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.config.read_text(), 'original identity')
        self.assertEqual(self.events(), ['setup', 'apple', 'build', 'native-playback', 'image-tools', 'sign'] * 2)

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
        self.assertEqual(self.events(), ['setup', 'created', 'native-playback', 'image-tools', 'sign'])

    def test_native_packaging_failure_blocks_signing_and_install(self):
        self.env['TEST_NATIVE_FAIL']='1'
        result=self.run_build('--build-only')
        self.assertEqual(result.returncode,45,result.stderr)
        self.assertNotIn('sign',self.events())
        self.assertNotIn('forbidden-install',self.events())

    def test_image_packaging_failure_blocks_signing_and_install(self):
        self.env['TEST_IMAGE_TOOLS_FAIL'] = '1'
        result = self.run_build('--build-only')
        self.assertEqual(result.returncode, 44, result.stderr)
        self.assertNotIn('sign', self.events())
        self.assertNotIn('forbidden-install', self.events())

    def add_speech_helper(self):
        helper = self.root / 'build/bin/析微影策.app/Contents/Resources/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai'
        helper.parent.mkdir(parents=True, exist_ok=True)
        helper.write_text('#!/bin/sh\nexit 0\n')
        helper.chmod(0o755)

    def test_install_skips_download_when_languages_already_installed(self):
        self.add_speech_helper()
        result = self.run_build()
        self.assertEqual(result.returncode, 98, result.stderr)  # Stop at mocked installation.
        self.assertIn('speech-check', self.events())
        self.assertNotIn('speech-install', self.events())
        self.assertLess(self.events().index('sign'), self.events().index('speech-check'))
        self.assertLess(self.events().index('speech-check'), self.events().index('forbidden-install'))

    def test_missing_languages_are_installed_and_rechecked_before_replacement(self):
        self.add_speech_helper()
        self.env['TEST_SPEECH_MISSING'] = 'ja ko'
        result = self.run_build('--skip-build')
        self.assertEqual(result.returncode, 98, result.stderr)
        self.assertEqual(self.events()[-4:], ['speech-check', 'speech-install', 'speech-check', 'forbidden-install'])

    def test_speech_failure_preserves_installed_application(self):
        self.add_speech_helper()
        self.env.update(TEST_SPEECH_MISSING='ko', TEST_SPEECH_FAIL='1')
        result = self.run_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('speech-install', self.events())
        self.assertNotIn('forbidden-install', self.events())
        self.assertIn('尚未替换', result.stderr)

    def test_status_error_does_not_install_resources_or_replace_application(self):
        self.add_speech_helper()
        self.env['TEST_SPEECH_CHECK_FAIL'] = '1'
        result = self.run_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('speech-install', self.events())
        self.assertNotIn('forbidden-install', self.events())
        self.assertIn('无法读取', result.stderr)

    def test_remaining_missing_language_is_not_reported_as_success(self):
        self.add_speech_helper()
        self.env.update(TEST_SPEECH_MISSING='en', TEST_SPEECH_STILL_MISSING='1')
        result = self.run_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.events().count('speech-check'), 2)
        self.assertNotIn('forbidden-install', self.events())
        self.assertIn('en', result.stderr)

    def test_build_only_does_not_check_or_install_speech_resources(self):
        self.add_speech_helper()
        self.env['TEST_SPEECH_MISSING'] = 'ja ko'
        result = self.run_build('--build-only')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('speech-check', self.events())
        self.assertNotIn('speech-install', self.events())
        self.assertNotIn('forbidden-install', self.events())

    def test_explicit_skip_does_not_check_speech_or_forward_flag_to_wails(self):
        self.add_speech_helper()
        self.env['TEST_SPEECH_MISSING'] = 'ja ko'
        result = self.run_build('--skip-speech-assets')
        self.assertEqual(result.returncode, 98, result.stderr)
        self.assertNotIn('speech-check', self.events())
        self.assertNotIn('speech-install', self.events())
        self.assertIn('跳过语音资源', result.stdout)

    def test_missing_helper_skips_system_resources(self):
        result = self.run_build()
        self.assertEqual(result.returncode, 98, result.stderr)
        self.assertNotIn('speech-check', self.events())
        self.assertIn('跳过语音资源', result.stdout)

    def test_help_and_invalid_options_do_not_initialize(self):
        self.assertEqual(self.run_build('--help').returncode, 0)
        self.assertNotEqual(self.run_build('--skip-build', '--build-only').returncode, 0)
        self.assertEqual(self.events(), [])


if __name__ == '__main__':
    unittest.main()
