# Run from the repository root after building the Apple helper. Uses only public validation data.
import json,subprocess,struct,base64,math,pathlib,time
root=pathlib.Path('/tmp/cineinsight-apple-validation');exe=pathlib.Path.cwd()/'build/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai'
p=subprocess.Popen([str(exe),'--face-worker'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=open('/tmp/apple-prod-native-stderr.log','w'),text=True,bufsize=1)
assert json.loads(p.stdout.readline())['ready']
rows=[];started=time.time()
for manifest in ['lfw-manifest.json','fresh-original-manifest.json','unknown-manifest.json']:
 for item in json.loads((root/manifest).read_text()):
  p.stdin.write(json.dumps({'id':item['id'],'path':item['path']})+'\n');p.stdin.flush();r=json.loads(p.stdout.readline());faces=r.get('faces',[]);v=None
  if faces:
   face=max(faces,key=lambda f:f['bbox'][2]*f['bbox'][3])
   v=struct.unpack('<128f',base64.b64decode(face['embedding']));norm=math.sqrt(sum(x*x for x in v));v=[x/norm for x in v]
  rows.append({**item,'vector':v,'error':r.get('error'),'face_count':len(faces)})
  if len(rows)%200==0:print('processed',len(rows),'elapsed',round(time.time()-started,1),flush=True)
p.stdin.close();assert p.wait()==0
(root/'production-face-results.json').write_text(json.dumps(rows))
gallery=[r for r in rows if r['person']<120 and r['shot']<2 and r['vector'] is not None]
threshold=.8307785119309815;metrics={'known_queries':0,'correct':0,'wrong':0,'rejected':0,'unknown_queries':0,'false_accept':0,'missing':sum(r['vector'] is None for r in rows),'gallery_people':len({r['person'] for r in gallery}),'gallery_images':len(gallery),'extracted':len(rows),'threshold':threshold}
for row in rows:
 unknown=row['dataset']=='fresh' and row['person']>=120;known=row['dataset']=='fresh' and row['person']<120 and row['shot']>=2
 if not (known or unknown):continue
 score,person=max((sum(a*b for a,b in zip(row['vector'],r['vector'])),r['person']) for r in gallery) if row['vector'] else (-1,-1)
 if unknown:metrics['unknown_queries']+=1;metrics['false_accept']+=score>=threshold
 else:
  metrics['known_queries']+=1
  if score<threshold:metrics['rejected']+=1
  elif person==row['person']:metrics['correct']+=1
  else:metrics['wrong']+=1
metrics['elapsed_seconds']=round(time.time()-started,2)
(root/'production-face-metrics.json').write_text(json.dumps(metrics,indent=2));print(json.dumps(metrics),flush=True)
