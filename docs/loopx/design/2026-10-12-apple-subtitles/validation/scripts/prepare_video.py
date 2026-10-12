import pathlib,json,subprocess
from PIL import Image,ImageOps
r=pathlib.Path('/tmp/cineinsight-apple-validation')
for name,records in [('face-video',[x for x in json.loads((r/'fresh-manifest.json').read_text()) if x['person']<120 and x['shot']>=2]),('scene-video',json.loads((r/'coco-manifest.json').read_text()))]:
 frames=r/(name+'-input');frames.mkdir(exist_ok=True);out=r/(name+'-frames');out.mkdir(exist_ok=True)
 for index,row in enumerate(records):
  image=Image.open(row['path']).convert('RGB');image.thumbnail((640,480));canvas=Image.new('RGB',(640,480));canvas.paste(image,((640-image.width)//2,(480-image.height)//2));canvas.save(frames/f'{index:04}.png')
 video=r/(name+'.mp4')
 subprocess.run(['ffmpeg','-v','error','-y','-framerate','1','-i',str(frames/'%04d.png'),'-c:v','libx264','-crf','32','-pix_fmt','yuv420p',str(video)],check=True,timeout=120)
 subprocess.run(['ffmpeg','-v','error','-y','-i',str(video),'-fps_mode','passthrough','-start_number','0',str(out/'%04d.jpg')],check=True,timeout=60)
 for index,row in enumerate(records):row['path']=str(out/f'{index:04}.jpg');row['id']+='-video';row['op']='face' if name=='face-video' else 'image'
 (r/(name+'-manifest.json')).write_text(json.dumps(records));print(name,len(records),flush=True)
