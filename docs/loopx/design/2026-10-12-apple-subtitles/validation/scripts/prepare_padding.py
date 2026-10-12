import pathlib,json
from PIL import Image,ImageOps
root=pathlib.Path('/tmp/cineinsight-apple-validation')
for dataset in ['orl','lfw','unknown']:
    records=json.loads((root/(dataset+'-manifest.json')).read_text())
    output=root/(dataset+'-padded');output.mkdir(exist_ok=True)
    for row in records:
        image=Image.open(row['path'])
        if dataset!='orl':image=image.convert('RGB')
        fill=128 if image.mode=='L' else (128,128,128)
        path=output/pathlib.Path(row['path']).name
        ImageOps.expand(image,border=round(min(image.size)*.25),fill=fill).save(path,quality=95)
        row['path']=str(path)
    (root/(dataset+'pad-manifest.json')).write_text(json.dumps(records))
