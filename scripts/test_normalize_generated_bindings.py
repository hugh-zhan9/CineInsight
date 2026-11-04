import importlib.util
import pathlib
import subprocess
import sys
import unittest

script = pathlib.Path(__file__).with_name('normalize_generated_bindings.py')
spec = importlib.util.spec_from_file_location('bindings_normalizer', script)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class NormalizerTest(unittest.TestCase):
    def test_only_added_blank_lines_change(self):
        before = 'old\n\t\nnew\n\t\nend\n'
        patch = '@@ -2,0 +3,2 @@\n+new\n+\t\n'
        self.assertEqual(module.normalize_added_blank_lines(before, patch), 'old\n\t\nnew\n\nend\n')

    def test_nonblank_and_empty_patch_preserved(self):
        value = '\tname = "spaces  ";  \n'
        self.assertEqual(module.normalize_added_blank_lines(value, '@@ -0,0 +1 @@\n+' + value), value)
        self.assertEqual(module.normalize_added_blank_lines(value, ''), value)

    def test_stale_or_invalid_patch_rejected(self):
        for patch in ['@@ -0,0 +1 @@\n+different\n', '@@ -0,0 +99 @@\n+\t\n']:
            with self.assertRaises(ValueError):
                module.normalize_added_blank_lines('same\n', patch)

    def test_arguments_fail_before_any_write(self):
        result = subprocess.run([sys.executable, str(script), '--unexpected'], capture_output=True)
        self.assertEqual(result.returncode, 2)


if __name__ == '__main__':
    unittest.main()
