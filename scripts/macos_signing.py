#!/usr/bin/env python3
"""Persistent local code-signing identity; no TCC or trust-store mutations."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import secrets
import subprocess
import sys
import tempfile

CONFIG = Path.home() / 'Library/Application Support/CineInsight/build-signing.json'
NAME = 'CineInsight Local Code Signing'

def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)

def output(*args):
    return run(*args, stdout=subprocess.PIPE).stdout.strip()

def read_plist(path):
    # Wails emits valid XML plists without an XML declaration. Use the platform
    # parser, emitting canonical XML to stdout without rewriting the bundle.
    return plistlib.loads(output('/usr/bin/plutil','-convert','xml1','-o','-',str(path)).encode())

def identity():
    data = json.loads(CONFIG.read_text())
    if not re.fullmatch('[0-9A-F]{40}', data['sha1']):
        raise ValueError('Invalid pinned certificate fingerprint')
    return data

def setup():
    if CONFIG.exists():
        data = identity()
        print('Pinned identity configured (signing verifies key availability): ' + data['sha1'])
        return
    keychain = output('/usr/bin/security', 'default-keychain', '-d', 'user').strip('"')
    if not Path(keychain).is_file():
        raise ValueError('User default keychain is unavailable')
    # Do not silently generate another key if configuration was lost.
    existing = subprocess.run(['/usr/bin/security','find-certificate','-c',NAME,keychain], capture_output=True)
    if existing.returncode == 0:
        raise ValueError('Existing local certificate found; restore the pinned configuration instead of regenerating it')
    with tempfile.TemporaryDirectory(prefix='cineinsight-signing-') as tmp:
        root = Path(tmp)
        conf = root/'openssl.cnf'
        conf.write_text('[req]\ndistinguished_name=dn\nx509_extensions=ext\nprompt=no\n[dn]\nCN='+NAME+'\n[ext]\nbasicConstraints=critical,CA:false\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=critical,codeSigning\n')
        key, cert, p12 = (root/name for name in ('key.pem','cert.pem','identity.p12'))
        run('/usr/bin/openssl','req','-new','-newkey','rsa:3072','-nodes','-x509','-days','3650','-config',str(conf),'-keyout',str(key),'-out',str(cert),stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        key.chmod(0o600)
        # Random transport password and private material live only for this import.
        password = secrets.token_hex(24)
        run('/usr/bin/openssl','pkcs12','-export','-inkey',str(key),'-in',str(cert),'-out',str(p12),'-keypbe','PBE-SHA1-3DES','-certpbe','PBE-SHA1-3DES','-macalg','sha1','-passout','stdin',input=password+'\n')
        p12.chmod(0o600)
        fingerprint = output('/usr/bin/openssl','x509','-in',str(cert),'-noout','-fingerprint','-sha1').split('=',1)[1].replace(':','').upper()
        result = subprocess.run(['/usr/bin/security','import',str(p12),'-k',keychain,'-P',password,'-T','/usr/bin/codesign'], capture_output=True, text=True)
        if result.returncode: raise ValueError('Keychain import failed: '+result.stderr.strip())
        CONFIG.parent.mkdir(parents=True,exist_ok=True)
        with CONFIG.open('x') as f:
            os.chmod(CONFIG,0o600)
            json.dump({'sha1':fingerprint,'keychain':keychain},f,indent=2)
        print('Created pinned local identity: '+fingerprint)

def sha256(path):
    with path.open('rb') as stream:
        digest = hashlib.sha256()
        for chunk in iter(lambda: stream.read(1024 * 1024), b''): digest.update(chunk)
        return digest.hexdigest()

def checked_manifest(app):
    path = app/'Contents/Resources/enhance-runtime/manifest.json'
    if not path.exists(): return None
    manifest = json.loads(path.read_text())
    if not manifest.get('files'): raise ValueError('Empty enhancement manifest')
    for item in manifest['files']:
        target = (path.parent/item['path']).resolve()
        if Path(item['path']).is_absolute() or not target.is_relative_to(path.parent.resolve()):
            raise ValueError('Unsafe enhancement manifest path')
        if sha256(target) != item['sha256']:
            raise ValueError('Enhancement input checksum mismatch: '+item['path'])
    return path, manifest

def macho(path):
    if not path.is_file() or path.is_symlink(): return False
    with path.open('rb') as f: magic=f.read(4)
    return magic in (b'\xfe\xed\xfa\xce',b'\xce\xfa\xed\xfe',b'\xfe\xed\xfa\xcf',b'\xcf\xfa\xed\xfe',b'\xca\xfe\xba\xbe',b'\xbe\xba\xfe\xca',b'\xca\xfe\xba\xbf',b'\xbf\xba\xfe\xca')

def sign(app):
    if app.is_symlink(): raise ValueError('Symlink app bundles are not supported')
    app=app.resolve()
    # This package has no framework symlink layout. Reject links before any write,
    # including a manifest link or a helper pointing outside the build artifact.
    if any(path.is_symlink() for path in app.rglob('*')):
        raise ValueError('Symlinks in app bundles are not supported')
    if app.suffix!='.app' or not (app/'Contents/Info.plist').is_file(): raise ValueError('Expected an app bundle')
    data=identity(); manifest=checked_manifest(app)
    bundles=sorted([app,*app.rglob('*.app')],key=lambda p:len(p.parts),reverse=True)
    executables=set()
    for bundle in bundles:
        info=read_plist(bundle/'Contents/Info.plist')
        name = info['CFBundleExecutable']
        if not isinstance(name,str) or Path(name).name != name or name in ('','.', '..'):
            raise ValueError('Invalid bundle executable')
        executable = bundle/'Contents/MacOS'/name
        if not macho(executable): raise ValueError('Bundle executable must be a regular Mach-O file')
        executables.add(executable)
    files=[p for p in app.rglob('*') if macho(p) and p not in executables]
    def sign_one(path, identifier):
        if not re.fullmatch(r'[A-Za-z0-9_.\-\u0080-\uffff]+',identifier): raise ValueError('Invalid signing identifier')
        requirement=f'designated => identifier "{identifier}" and certificate leaf = H"{data["sha1"]}"'
        run('/usr/bin/codesign','--force','--sign',data['sha1'],'--keychain',data['keychain'],'--timestamp=none','--identifier',identifier,'--requirements','='+requirement,str(path))
        run('/usr/bin/codesign','--verify','--strict',str(path))
    signed=set()
    for path in files:
        identifier='com.cineinsight.local.'+hashlib.sha256(str(path.relative_to(app)).encode()).hexdigest()[:24]
        sign_one(path,identifier); signed.add(path.resolve())
    for bundle in bundles:
        if bundle==app and manifest:
            path,doc=manifest
            for item in doc['files']:
                target=(path.parent/item['path']).resolve()
                if target in signed: item['sha256']=sha256(target)
            path.write_text(json.dumps(doc,indent=2)+'\n')
        info=read_plist(bundle/'Contents/Info.plist')
        sign_one(bundle,info['CFBundleIdentifier'])
    run('/usr/bin/codesign','--verify','--deep','--strict',str(app))
    print('Verified stable local signature: '+str(app))

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    sub=parser.add_subparsers(dest='command',required=True)
    sub.add_parser('setup'); sub.add_parser('sign').add_argument('app',type=Path)
    args=parser.parse_args()
    if sys.platform!='darwin': parser.error('macOS only')
    if args.command=='setup': setup()
    else: sign(args.app)

if __name__=='__main__':
    try: main()
    except (ValueError,OSError,KeyError,subprocess.CalledProcessError) as exc:
        sys.exit('Signing failed: '+str(exc))
