"""Exercise the actual preparation flow with an isolated fake system helper."""
import contextlib
from io import StringIO
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

from translation_assets import prepare_pairs


class TranslationAssetTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        self.log = root / "log"
        self.config = root / "config.json"
        self.helper = root / "helper"
        self.helper.write_text(f"#!{sys.executable}\n" + """
import json, pathlib, sys
request = json.load(sys.stdin)
assert set(request) == {'operation', 'source', 'target'}
operation, source, target = (request[k] for k in ('operation', 'source', 'target'))
assert target == 'zh'
root = pathlib.Path(__file__).parent
log = root / 'log'
event = f'{operation}:{source}'
history = log.read_text().splitlines() if log.exists() else []
with log.open('a') as f: print(event, file=f)
mode = json.loads((root / 'config.json').read_text()).get(source, 'installed')
if operation == 'translation_status':
    prepared = f'prepare_translation:{source}' in history
    if mode == 'legacy': print(json.dumps({'error':'apple_invalid_request'}))
    else:
        status = 'installed' if mode == 'installed' or mode == 'delayed' and prepared and history.count(event) >= 2 else (
            'unsupported' if mode == 'unsupported' else 'supported')
        print(json.dumps({'translationStatus':status}))
elif operation == 'prepare_translation':
    print(json.dumps({'error':'cancelled'} if mode == 'cancelled' else {'supported':True}))
else: raise AssertionError(operation)
""")
        self.helper.chmod(0o755)

    def run_prepare(self, **modes):
        self.config.write_text(json.dumps(modes))
        with contextlib.redirect_stdout(StringIO()):
            prepare_pairs(str(self.helper), timeout=3, poll=0.01)
        return self.log.read_text().splitlines()

    def test_installed_does_not_request_download(self):
        self.assertEqual(self.run_prepare(), [
            "translation_status:ja", "translation_status:ko", "translation_status:en",
        ])

    def test_missing_requests_once_and_rechecks_until_installed(self):
        self.assertEqual(self.run_prepare(ja="delayed"), [
            "translation_status:ja", "prepare_translation:ja", "translation_status:ja",
            "translation_status:ja", "translation_status:ko", "translation_status:en",
        ])

    def test_unsupported_does_not_open_download(self):
        with self.assertRaisesRegex(RuntimeError, "不支持"):
            self.run_prepare(ja="unsupported")
        self.assertEqual(self.log.read_text().splitlines(), ["translation_status:ja"])

    def test_cancelled_does_not_try_other_pairs(self):
        with self.assertRaisesRegex(RuntimeError, "已取消"):
            self.run_prepare(ja="cancelled")
        self.assertEqual(self.log.read_text().splitlines(), ["translation_status:ja", "prepare_translation:ja"])

    def test_legacy_helper_requests_rebuild(self):
        with self.assertRaisesRegex(RuntimeError, "--skip-build"):
            self.run_prepare(ja="legacy")

    def test_second_probe_required_before_success(self):
        with patch("translation_assets.helper_request", side_effect=[
            {"translationStatus": "supported"}, {"supported": True}, {"translationStatus": "supported"},
        ]), patch("translation_assets.time.monotonic", side_effect=[0, 1, 2, 3]), \
             patch("translation_assets.time.sleep"), contextlib.redirect_stdout(StringIO()):
            with self.assertRaisesRegex(RuntimeError, "超时"):
                prepare_pairs("unused", timeout=3)


if __name__ == "__main__":
    unittest.main()
