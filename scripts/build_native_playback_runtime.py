#!/usr/bin/env python3
"""Assemble a pinned libmpv runtime; never inspect or modify an installed application."""
import argparse, hashlib, io, json, os, re, shutil, subprocess, tarfile, tempfile, urllib.request, urllib.parse, zipfile
from pathlib import Path
ROOT = Path(__file__).resolve().parents[1]
BASE = ROOT / 'build/native-playback'
ASSET_URL = 'https://github.com/iina/deps-buildscripts/releases/download/iina-1.5.0-release/iina-deps-arm64.zip'
ASSET_SHA = 'ec7266468a297b907eadbb473c7b3d2992cd274c40d379d8e0e8910caedfae50'
RECIPE_COMMIT = 'f86d54276547df6d14104583ea604af67433f1e1'
RECIPE_URL = f'https://github.com/iina/deps-buildscripts/archive/{RECIPE_COMMIT}.tar.gz'
RECIPE_SHA = '1a59a63623d00264c895997e17866cd6d4918e02f04f3462c573c0527f531808'

def digest(path):
    h=hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda:stream.read(1024*1024), b''): h.update(chunk)
    return h.hexdigest()

def download(url, target, expected=None):
    if target.exists() and (expected is None or digest(target)==expected): return target
    target.parent.mkdir(parents=True,exist_ok=True)
    with urllib.request.urlopen(url,timeout=45) as response, target.with_suffix(target.suffix+'.part').open('wb') as stream:
        shutil.copyfileobj(response,stream,1024*1024)
    temporary=target.with_suffix(target.suffix+'.part')
    if expected and digest(temporary)!=expected: temporary.unlink(); raise ValueError('Checksum mismatch: '+target.name)
    temporary.replace(target); return target

def parse_versions(text):
    values={}
    for line in text.splitlines():
        match=re.match(r'^([A-Z0-9_]+)="([^"]*)"',line)
        if not match: continue
        key,value=match.groups()
        value=re.sub(r'\$\{([A-Z0-9_]+)\}',lambda m:values[m[1]],value)
        if '$' in value: raise ValueError('Unexpected shell expression in version manifest')
        values[key]=value
    return values

def archive_licenses(archive, output):
    copied=[]
    with tarfile.open(archive,mode='r|*') as package:
        for entry in package:
            name=Path(entry.name)
            if not entry.isfile() or name.is_absolute() or '..' in name.parts: continue
            if not (re.match(r'^(COPYING|LICEN[CS]E|COPYRIGHT|NOTICE|AUTHORS)([._-].*)?$',name.name,re.I) or 'LICENSES' in name.parts): continue
            if entry.size>2*1024*1024: raise ValueError('Unexpectedly large license: '+entry.name)
            relative=Path(*name.parts[1:])
            target=output/relative; target.parent.mkdir(parents=True,exist_ok=True)
            target.write_bytes(package.extractfile(entry).read());copied.append(str(relative))
    return copied

def recipe_materials(output, collect=False):
    archive=download(RECIPE_URL,BASE/'upstream'/'recipe.tar.gz',RECIPE_SHA)
    with tarfile.open(archive) as package:
        for entry in package:
            relative=Path(*Path(entry.name).parts[1:])
            if not entry.isfile() or relative.is_absolute() or '..' in relative.parts: continue
            if str(relative)=='versions.env' or relative.parts and relative.parts[0] in ('buildscripts','patches','.github'):
                target=output/'recipe'/relative;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(package.extractfile(entry).read())
    values=parse_versions((output/'recipe/versions.env').read_text())
    sources=[];complete=True
    pinned=json.loads((ROOT/'native/playback/runtime-sources.json').read_text())['sources']
    git_sources={row['component']:row for row in pinned}
    for key,url in values.items():
        if not key.endswith('_URL'): continue
        component=key[:-4]; print('Source material:',component,flush=True); record={'component':component,'url':url,'sha256':values.get(component+'_SHA256'),'version':values.get(component+'_VERSION',values.get(component+'_COMMIT',''))}
        if collect:
            try:
                git=git_sources.pop(component,None)
                if git:
                    record['corresponding_git_source']=git
                    path=download(git['url'],BASE/'sources'/component/git['archive'],git['sha256'])
                else:
                    path=download(url,BASE/'sources'/component/Path(urllib.parse.urlparse(url).path).name,record['sha256'])
                record['licenses']=archive_licenses(path,output/component)
                if not record['licenses']: complete=False;record['error']='No license files found'
            except Exception as error: complete=False;record['error']=str(error)
        else: complete=False
        sources.append(record)
    for component,git in git_sources.items():
        record=dict(git)
        if collect:
            try:
                path=download(git['url'],BASE/'sources'/component/git['archive'],git['sha256'])
                record['licenses']=archive_licenses(path,output/component)
                if not record['licenses']: complete=False;record['error']='No license files found'
            except Exception as error: complete=False;record['error']=str(error)
        else: complete=False
        sources.append(record)
    document={'recipe_commit':RECIPE_COMMIT,'recipe_url':RECIPE_URL,'recipe_sha256':digest(archive),'sources':sources,'complete':complete}
    (output/'SOURCES.json').write_text(json.dumps(document,ensure_ascii=False,indent=2)+'\n')
    write_material_index(output)
    return complete

def write_material_index(output):
    records=[]
    for path in sorted(output.rglob('*')):
        if path.is_symlink():raise ValueError('Symlink native source material')
        if path.is_file() and path.name!='MATERIALS.json':records.append({'path':str(path.relative_to(output)),'sha256':digest(path)})
    (output/'MATERIALS.json').write_text(json.dumps({'files':records},indent=2)+'\n')
    return digest(output/'MATERIALS.json')

def binary_info(path, names):
    architectures=subprocess.check_output(['lipo','-archs',str(path)],text=True).split()
    if architectures!=['arm64']: raise ValueError('Expected arm64 library: '+path.name)
    commands=subprocess.check_output(['otool','-arch','arm64','-l',str(path)],text=True)
    version=re.search(r'\bminos\s+([0-9.]+)',commands)
    if not version or tuple(map(int,version[1].split('.')))>(15,0): raise ValueError('Unsupported minimum OS: '+path.name)
    deps=[]
    for line in subprocess.check_output(['otool','-arch','arm64','-L',str(path)],text=True).splitlines()[2:]:
        dep=line.strip().split(' (')[0]
        if dep.startswith(('/usr/lib/','/System/Library/')): continue
        if not dep.startswith(('@rpath/','@loader_path/')) or Path(dep).name not in names: raise ValueError('External dependency: '+dep)
        deps.append(dep)
    return version[1],deps

def build(collect=False):
    if os.uname().sysname!='Darwin' or os.uname().machine!='arm64': raise ValueError('Requires Apple Silicon macOS')
    archive=download(ASSET_URL,BASE/'upstream/iina-deps-arm64.zip',ASSET_SHA)
    BASE.mkdir(parents=True,exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='.runtime-',dir=BASE) as temporary:
        staged=Path(temporary)/'runtime';libraries=staged/'lib';libraries.mkdir(parents=True)
        with zipfile.ZipFile(archive) as package:
            for entry in package.infolist():
                name=Path(entry.filename)
                if name.is_absolute() or '..' in name.parts: raise ValueError('Unsafe ZIP path')
                if name.suffix=='.dylib':
                    if len(name.parts)!=1: raise ValueError('Unexpected library layout')
                    (libraries/name.name).write_bytes(package.read(entry))
        names={p.name for p in libraries.iterdir()};files=[]
        if 'libmpv.2.dylib' not in names: raise ValueError('libmpv missing')
        for path in sorted(libraries.iterdir()):
            minimum,deps=binary_info(path,names);original=digest(path)
            args=['install_name_tool','-id','@loader_path/'+path.name]
            for dep in deps:
                replacement='@loader_path/'+Path(dep).name
                if dep!=replacement: args.extend(['-change',dep,replacement])
            subprocess.run([*args,str(path)],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            subprocess.run(['codesign','--force','--sign','-',str(path)],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            binary_info(path,names)
            files.append({'path':'lib/'+path.name,'upstream_sha256':original,'sha256':digest(path),'minimum_macos':minimum})
        licenses=staged/'licenses';licenses.mkdir()
        complete=recipe_materials(licenses,collect)
        manifest={'runtime_id':'libmpv-0.41.0-iina-1.5.0-arm64','version':'0.41.0','architecture':'arm64','source_url':ASSET_URL,'source_sha256':ASSET_SHA,'recipe_commit':RECIPE_COMMIT,'source_materials_complete':complete,'materials_sha256':digest(licenses/'MATERIALS.json'),'entry':'lib/libmpv.2.dylib','files':files}
        (staged/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
        target=BASE/'runtime'
        if target.is_symlink(): raise ValueError('Output must not be a symlink')
        if target.exists(): shutil.rmtree(target)
        staged.rename(target)
    print('Verified runtime libraries:',len(files),'source materials complete:',complete)

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--collect-sources',action='store_true');args=parser.parse_args();build(args.collect_sources)
