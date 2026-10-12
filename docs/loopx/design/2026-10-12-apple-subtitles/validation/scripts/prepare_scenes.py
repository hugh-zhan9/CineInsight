import json,pathlib,random,collections,urllib.request,concurrent.futures,time
root=pathlib.Path('/tmp/cineinsight-apple-validation');data=json.loads((root/'coco-captions.json').read_text());captions=collections.defaultdict(list)
for annotation in data['annotations']:captions[annotation['image_id']].append(annotation['caption'])
rng=random.Random(20261012);images=data['images'].copy();rng.shuffle(images);images=images[:100]
out=root/'coco';out.mkdir(exist_ok=True)
def fetch(item):
 path=out/item['file_name'];url='https://s3.amazonaws.com/images.cocodataset.org/val2014/'+item['file_name']
 try:
  if not path.exists():
   with urllib.request.urlopen(url,timeout=25) as response:path.write_bytes(response.read())
  return dict(id=str(item['id']),path=str(path),op='scene',captions=captions[item['id']],dataset='coco',url=url)
 except Exception as e:print('download_failed',item['id'],str(e),flush=True);return None
records=[]
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
 for result in pool.map(fetch,images):
  if result:records.append(result)
  if len(records)%20==0:print('scene_images',len(records),flush=True)
assert len(records)==100, ('incomplete dataset',len(records)); (root/'coco-manifest.json').write_text(json.dumps(records));print('download_complete',len(records),flush=True)
