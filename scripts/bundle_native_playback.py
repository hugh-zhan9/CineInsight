#!/usr/bin/env python3
"""Validate and copy the fixed native playback runtime into a build artifact."""
import hashlib,importlib.util,json,shutil,sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('native_playback_builder',ROOT/'scripts/build_native_playback_runtime.py')
builder=importlib.util.module_from_spec(spec);spec.loader.exec_module(builder)
def checked_runtime(root):
    if root.is_symlink() or any(path.is_symlink() for path in root.rglob('*')):raise ValueError('Native runtime must not contain symlinks')
    path=root/'manifest.json';document=json.loads(path.read_text())
    if document.get('runtime_id')!='libmpv-0.41.0-iina-1.5.0-arm64' or document.get('source_sha256')!=builder.ASSET_SHA or document.get('entry')!='lib/libmpv.2.dylib' or not document.get('source_materials_complete'):raise ValueError('Native runtime identity/source evidence mismatch')
    entries=document.get('files',[])
    if not entries or len(entries)>128:raise ValueError('Native runtime file budget invalid')
    names=set()
    for entry in entries:
        relative=Path(entry['path'])
        if len(relative.parts)!=2 or relative.parts[0]!='lib' or relative.suffix!='.dylib' or relative.name in names:raise ValueError('Unsafe native runtime path')
        names.add(relative.name)
        if builder.digest(root/relative)!=entry['sha256']:raise ValueError('Native input checksum mismatch: '+str(relative))
    if {p.name for p in (root/'lib').iterdir()}!=names:raise ValueError('Unlisted native runtime files')
    for name in names:builder.binary_info(root/'lib'/name,names)
    index_path=root/'licenses/MATERIALS.json'
    if builder.digest(index_path)!=document.get('materials_sha256'):raise ValueError('Native material index mismatch')
    materials=json.loads(index_path.read_text()).get('files',[])
    if not materials or len(materials)>10000:raise ValueError('Native material index invalid')
    seen=set()
    for item in materials:
        relative=Path(item['path'])
        if relative.is_absolute() or '..' in relative.parts or item['path'] in seen:raise ValueError('Unsafe native material path')
        seen.add(item['path'])
        if builder.digest(root/'licenses'/relative)!=item['sha256']:raise ValueError('Native source material mismatch: '+str(relative))
    sources=json.loads((root/'licenses/SOURCES.json').read_text())
    if not sources.get('complete'):raise ValueError('Native license/source index incomplete')
    return path,document

def bundle(app,source):
    if app.is_symlink() or any(path.is_symlink() for path in [app/'Contents',app/'Contents/Resources',app/'Contents/Resources/native-playback']) or not (app/'Contents/Info.plist').is_file():raise ValueError('Expected a regular build app bundle')
    target=app/'Contents/Resources/native-playback'
    if not (source/'manifest.json').is_file():raise ValueError('Required native playback runtime is missing; assemble it before building the app')
    checked_runtime(source)
    if target.is_symlink():raise ValueError('Native bundle target must not be a symlink')
    if target.exists():shutil.rmtree(target)
    shutil.copytree(source,target);checked_runtime(target)
    print('Bundled verified native playback runtime')
if __name__=='__main__':
    try:
        if len(sys.argv)!=2:raise ValueError('Usage: bundle_native_playback.py <build.app>')
        bundle(Path(sys.argv[1]),ROOT/'build/native-playback/runtime')
    except (ValueError,OSError,KeyError) as error:raise SystemExit(str(error))
