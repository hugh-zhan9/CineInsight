#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('bundle_image_tools', Path(__file__).with_name('bundle_image_tools.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ImageToolPackagingTests(unittest.TestCase):
    def test_rejects_a_tool_requiring_a_newer_macos_without_touching_the_bundle(self):
        with tempfile.TemporaryDirectory(prefix='cineinsight-image-tools-') as directory:
            app = Path(directory) / 'Fixture.app'
            target = app / 'Contents/Resources/image-tools'
            target.mkdir(parents=True)
            sentinel = target / 'existing'
            sentinel.write_text('keep')
            original_run = module.run
            for too_new_name in ('jpegtran', 'library'):
                with self.subTest(component=too_new_name):
                    def too_new(*args):
                        if args[:3] == ('/usr/bin/xcrun', 'vtool', '-show-build') and (Path(args[-1]).name == 'jpegtran' if too_new_name == 'jpegtran' else Path(args[-1]).suffix == '.dylib'):
                            return 'platform MACOS\nminos 27.0\n'
                        return original_run(*args)
                    with patch.object(module, 'run', side_effect=too_new):
                        with self.assertRaises(ValueError):
                            module.bundle(app)
                    self.assertEqual(sentinel.read_text(), 'keep')

    def test_real_relocated_tool_and_notices(self):
        with tempfile.TemporaryDirectory(prefix='cineinsight-image-tools-') as directory:
            app = Path(directory) / 'Fixture.app'
            app.mkdir()
            target = module.bundle(app)
            self.assertTrue((target / 'jpegtran').is_file())
            self.assertFalse((target / 'libjpeg.8.dylib').is_symlink())
            self.assertTrue((target / 'licenses' / 'README.ijg').is_file())
            dependencies = module.run('/usr/bin/otool', '-L', str(target / 'jpegtran'))
            self.assertIn('@loader_path/libjpeg.8.dylib', dependencies)
            self.assertNotIn('/opt/homebrew', dependencies)
            subprocess.run([str(target / 'jpegtran'), '-version'], check=True, capture_output=True)

    def test_missing_tool_keeps_existing_bundle(self):
        with tempfile.TemporaryDirectory(prefix='cineinsight-image-tools-') as directory:
            app = Path(directory) / 'Fixture.app'
            target = app / 'Contents/Resources/image-tools'
            target.mkdir(parents=True)
            sentinel = target / 'existing'
            sentinel.write_text('keep')
            with self.assertRaises(ValueError):
                module.bundle(app, Path(directory) / 'missing')
            self.assertEqual(sentinel.read_text(), 'keep')

    def test_rejects_resource_symlink_and_non_bundle(self):
        with tempfile.TemporaryDirectory(prefix='cineinsight-image-tools-') as directory:
            app = Path(directory) / 'Fixture.app'
            (app / 'Contents').mkdir(parents=True)
            outside = Path(directory) / 'outside'
            outside.mkdir()
            (app / 'Contents/Resources').symlink_to(outside, target_is_directory=True)
            with self.assertRaises(ValueError):
                module.bundle(app)
            self.assertEqual(list(outside.iterdir()), [])
            with self.assertRaises(ValueError):
                module.bundle(outside)


if __name__ == '__main__':
    unittest.main()
