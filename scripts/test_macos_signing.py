import json
from pathlib import Path
import plistlib
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import macos_signing as signing

class SigningTests(unittest.TestCase):
    def fixture(self, root, executable='/bin/echo'):
        app=root/'Fixture.app'
        for bundle, ident in [(app,'com.cineinsight.signing-test'),(app/'Contents/Resources/apple-ai/Helper.app','com.cineinsight.signing-test.helper')]:
            (bundle/'Contents/MacOS').mkdir(parents=True)
            (bundle/'Contents/Info.plist').write_bytes(plistlib.dumps({'CFBundleIdentifier':ident,'CFBundleExecutable':'runner','CFBundlePackageType':'APPL','CFBundleVersion':'1'}))
            shutil.copy(executable,bundle/'Contents/MacOS/runner')
        runtime=app/'Contents/Resources/enhance-runtime'
        (runtime/'bin').mkdir(parents=True)
        shutil.copy('/bin/echo',runtime/'bin/tool')
        (runtime/'manifest.json').write_text(json.dumps({'files':[{'path':'bin/tool','sha256':signing.sha256(runtime/'bin/tool')}],'models':[{'path':'external-model','sha256':'unchanged'}]}))
        return app
    def dr(self,app):
        output=subprocess.run(['/usr/bin/codesign','-d','-r-',str(app)],capture_output=True,text=True,check=True)
        return next(line.removeprefix('# ') for line in (output.stdout+output.stderr).splitlines() if 'designated =>' in line)
    def test_stable_identity_and_integrity(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            first=self.fixture(root/'a'); second=self.fixture(root/'b','/bin/pwd')
            signing.sign(first); signing.sign(second)
            for relative in ['', 'Contents/Resources/apple-ai/Helper.app']:
                self.assertEqual(self.dr(first/relative),self.dr(second/relative))
                self.assertNotIn('cdhash',self.dr(first/relative))
            signing.checked_manifest(first); signing.checked_manifest(second)
            doc=json.loads((first/'Contents/Resources/enhance-runtime/manifest.json').read_text())
            self.assertEqual(doc['models'],[{'path':'external-model','sha256':'unchanged'}])
            (first/'Contents/Resources/tampered').write_text('tampered')
            check=subprocess.run(['/usr/bin/codesign','--verify','--deep','--strict',str(first)],capture_output=True)
            self.assertNotEqual(check.returncode,0)
    def test_symlinks_rejected_before_any_signing(self):
        for location in ['Contents/Resources/External.app', 'Contents/Resources/enhance-runtime/manifest.json', 'Contents/MacOS/runner']:
            with self.subTest(location=location), tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp); app=self.fixture(root/'inner'); outside=self.fixture(root/'outside')
                original=signing.sha256(outside/'Contents/MacOS/runner')
                link=app/location
                if link.exists(): link.unlink()
                link.symlink_to(outside)
                with patch.object(signing, 'run') as command:
                    with self.assertRaisesRegex(ValueError,'Symlink'): signing.sign(app)
                    command.assert_not_called()
                self.assertEqual(signing.sha256(outside/'Contents/MacOS/runner'),original)
    def test_executable_path_escape_rejected_before_signing(self):
        with tempfile.TemporaryDirectory() as tmp:
            app=self.fixture(Path(tmp));path=app/'Contents/Info.plist'
            info=plistlib.loads(path.read_bytes());info['CFBundleExecutable']='../../external';path.write_bytes(plistlib.dumps(info))
            with patch.object(signing,'run', wraps=signing.run) as command:
                with self.assertRaisesRegex(ValueError,'Invalid bundle executable'): signing.sign(app)
                self.assertFalse(any(call.args[0]=='/usr/bin/codesign' for call in command.call_args_list))
    def test_wails_plist_without_xml_declaration(self):
        with tempfile.TemporaryDirectory() as tmp:
            app=self.fixture(Path(tmp));path=app/'Contents/Info.plist'
            path.write_bytes(path.read_bytes().split(b'\n',1)[1])
            self.assertEqual(signing.read_plist(path)['CFBundleIdentifier'],'com.cineinsight.signing-test')
    def test_damaged_input_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            app=self.fixture(Path(tmp))
            (app/'Contents/Resources/enhance-runtime/bin/tool').write_bytes(b'broken')
            with self.assertRaisesRegex(ValueError,'checksum mismatch'): signing.sign(app)
    def test_manifest_escape_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            app=self.fixture(Path(tmp));path=app/'Contents/Resources/enhance-runtime/manifest.json'
            data=json.loads(path.read_text());data['files'][0]['path']='../../../../outside';path.write_text(json.dumps(data))
            with self.assertRaisesRegex(ValueError,'Unsafe'): signing.checked_manifest(app)
if __name__=='__main__': unittest.main()
