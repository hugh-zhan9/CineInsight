import tarfile,pathlib,random,json,collections
from PIL import Image,ImageOps
root=pathlib.Path('/tmp/cineinsight-apple-validation');out=root/'lfw-fresh';out.mkdir(exist_ok=True);rng=random.Random(20261012)
with tarfile.open(root/'lfw.tgz','r:gz') as archive:
 groups=collections.defaultdict(list)
 for member in archive.getmembers():
  if member.isfile() and member.name.endswith('.jpg'):groups[pathlib.PurePosixPath(member.name).parts[-2]].append(member)
 subjects=sorted(name for name,items in groups.items() if len(items)>=10);rng.shuffle(subjects);print('eligible_people',len(subjects));subjects=subjects[100:140];records=[]
 assert len(subjects)==40
 for person,name in enumerate(subjects):
  for shot,member in enumerate(rng.sample(sorted(groups[name],key=lambda m:m.name),10)):
   ident=f'fresh-{person:02d}-{shot:02d}';path=out/(ident+'.jpg')
   image=Image.open(archive.extractfile(member)).convert('RGB');ImageOps.expand(image,border=round(min(image.size)*.25),fill=(128,128,128)).save(path,quality=95)
   records.append(dict(id=ident,path=str(path),person=person+100,shot=shot,dataset='fresh',op='face'))
 (root/'fresh-manifest.json').write_text(json.dumps(records));print('fresh',len(records))
