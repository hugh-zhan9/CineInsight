import tarfile,pathlib,random,json,collections
root=pathlib.Path('/tmp/cineinsight-apple-validation');out=root/'lfw';out.mkdir(exist_ok=True);rng=random.Random(20261012)
with tarfile.open(root/'lfw.tgz','r:gz') as archive:
 groups=collections.defaultdict(list)
 for member in archive.getmembers():
  parts=pathlib.PurePosixPath(member.name).parts
  if member.isfile() and member.name.endswith('.jpg'):groups[parts[-2]].append(member)
 subjects=sorted(name for name,items in groups.items() if len(items)>=10);rng.shuffle(subjects);subjects=subjects[:80];records=[]
 for person,name in enumerate(subjects):
  chosen=rng.sample(sorted(groups[name],key=lambda m:m.name),10)
  for shot,member in enumerate(chosen):
   ident=f'lfw-{person:02d}-{shot:02d}';path=out/(ident+'.jpg');path.write_bytes(archive.extractfile(member).read())
   records.append(dict(id=ident,path=str(path),person=person,shot=shot,dataset='lfw',op='face'))
 (root/'lfw-manifest.json').write_text(json.dumps(records))
 print('lfw subjects',len(subjects),'images',len(records))
