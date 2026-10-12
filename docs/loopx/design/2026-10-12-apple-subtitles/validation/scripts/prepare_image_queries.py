import pathlib,json
from PIL import Image
root=pathlib.Path('/tmp/cineinsight-apple-validation');records=json.loads((root/'coco-manifest.json').read_text());output=root/'coco-queries';output.mkdir(exist_ok=True);queries=[]
for row in records:
    image=Image.open(row['path']).convert('RGB')
    for variant in ['compressed','crop']:
        result=image.copy()
        if variant=='crop':
            width,height=result.size
            result=result.crop((int(.1*width),int(.1*height),int(.9*width),int(.9*height)))
        result.thumbnail((320,320));path=output/(row['id']+'-'+variant+'.jpg');result.save(path,quality=40)
        queries.append(dict(id=row['id']+'-'+variant,path=str(path),op='image',dataset='coco'))
(root/'coco-query-manifest.json').write_text(json.dumps(queries))
