#!/usr/bin/env python3
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('bundle_enhancement_driver', Path(__file__).with_name('bundle_enhancement_driver.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
SOURCE = Path(__file__).resolve().parent.parent / 'build/enhance-runtime'


class EnhancementDriverPackagingTests(unittest.TestCase):
    def test_real_relocation_updates_manifest_without_changing_models(self):
        with tempfile.TemporaryDirectory(prefix='cineinsight-driver-test-') as directory:
            runtime = Path(directory) / 'runtime'
            shutil.copytree(SOURCE, runtime)
            before = json.loads((runtime / 'manifest.json').read_text())
            module.bundle(runtime)
            manifest = json.loads((runtime / 'manifest.json').read_text())
            self.assertEqual(manifest['runtime_version'], module.RUNTIME_VERSION)
            self.assertEqual(manifest['models'], before['models'])
            for item in manifest['files']:
                self.assertEqual(hashlib.sha256((runtime / item['path']).read_bytes()).hexdigest(), item['sha256'])
            dependencies = module.run('/usr/bin/otool', '-L', str(runtime / manifest['binary']))
            self.assertIn('@loader_path/../lib/libMoltenVK.dylib', dependencies)
            self.assertNotIn('/opt/homebrew', dependencies)
            self.assertNotIn('libvulkan', dependencies)
            self.assertTrue((runtime / 'licenses/MoltenVK-LICENSE.txt').is_file())

    def test_missing_driver_preserves_existing_runtime(self):
        with tempfile.TemporaryDirectory(prefix='cineinsight-driver-test-') as directory:
            runtime = Path(directory) / 'runtime'
            shutil.copytree(SOURCE, runtime)
            manifest = (runtime / 'manifest.json').read_bytes()
            with self.assertRaises(ValueError):
                module.bundle(runtime, runtime / 'missing-library')
            self.assertEqual((runtime / 'manifest.json').read_bytes(), manifest)


if __name__ == '__main__':
    unittest.main()
