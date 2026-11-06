import json,os,plistlib,tempfile,unittest
from pathlib import Path
import bundle_native_playback as native
class NativeBundleTests(unittest.TestCase):
    def app(self,root):
        app=root/'Fixture.app';(app/'Contents').mkdir(parents=True);(app/'Contents/Info.plist').write_bytes(plistlib.dumps({'CFBundleIdentifier':'com.cineinsight.test'}));return app
    def test_missing_runtime_is_explicit_and_preserves_existing_copy(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);app=self.app(root);stale=app/'Contents/Resources/native-playback';stale.mkdir(parents=True);(stale/'old').write_text('stale')
            with self.assertRaises(ValueError):native.bundle(app,root/'missing')
            self.assertTrue((stale/'old').exists())
    def test_forged_manifest_and_symlinks_rejected_before_copy(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);app=self.app(root);source=root/'runtime';source.mkdir();(source/'manifest.json').write_text(json.dumps({'runtime_id':'wrong'}))
            with self.assertRaises(ValueError):native.bundle(app,source)
            self.assertFalse((app/'Contents/Resources/native-playback').exists())
            link=root/'link';link.symlink_to(source,target_is_directory=True)
            with self.assertRaises(ValueError):native.checked_runtime(link)
    def test_resources_symlink_never_modifies_external_files(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);app=self.app(root);outside=root/'outside';stale=outside/'native-playback';stale.mkdir(parents=True);(stale/'keep').write_text('unchanged');(app/'Contents/Resources').symlink_to(outside,target_is_directory=True)
            with self.assertRaises(ValueError):native.bundle(app,root/'missing')
            self.assertEqual((stale/'keep').read_text(),'unchanged')

    def test_pinned_runtime_and_tamper(self):
        fixture=os.environ.get('CINEINSIGHT_TEST_NATIVE_RUNTIME')
        if not fixture:self.skipTest('explicit pinned native runtime required')
        with tempfile.TemporaryDirectory() as folder:
            app=self.app(Path(folder));native.bundle(app,Path(fixture));runtime=app/'Contents/Resources/native-playback';native.checked_runtime(runtime)
            index=json.loads((runtime/'licenses/MATERIALS.json').read_text())
            material=runtime/'licenses'/index['files'][0]['path'];original=material.read_bytes();material.unlink()
            with self.assertRaises((ValueError,OSError)):native.checked_runtime(runtime)
            material.write_bytes(original+b'tampered')
            with self.assertRaises(ValueError):native.checked_runtime(runtime)
            material.write_bytes(original)
            with (runtime/'lib/libmpv.2.dylib').open('ab') as stream:stream.write(b'tampered')
            with self.assertRaises(ValueError):native.checked_runtime(runtime)
if __name__=='__main__':unittest.main()
