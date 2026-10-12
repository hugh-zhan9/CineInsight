# Run from the repository root after building the Apple helper. Uses only public validation data.
import json,base64,struct,subprocess,pathlib
root=pathlib.Path('/tmp/cineinsight-apple-validation');exe=pathlib.Path.cwd()/'build/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai'
references=json.loads((root/'coco-manifest.json').read_text());queries=json.loads((root/'coco-query-manifest.json').read_text())+json.loads((root/'scene-video-manifest.json').read_text())
def vectors(rows):
 out=[]
 for start in range(0,len(rows),32):
  p=subprocess.run([str(exe)],input=json.dumps({'operation':'image_features','paths':[r['path'] for r in rows[start:start+32]]}),text=True,capture_output=True,check=True);r=json.loads(p.stdout);assert not r.get('error'),r
  out.extend(struct.unpack('<768f',base64.b64decode(v)) for v in r['vectors'])
 return out
g=vectors(references);q=vectors(queries);metrics={}
for row,v in zip(queries,q):
 order=sorted(range(len(g)),key=lambda i:sum((a-b)**2 for a,b in zip(v,g[i])));ident=row['id'].split('-')[0];kind=row['id'].split('-')[-1];r=metrics.setdefault(kind,{'queries':0,'top1':0,'top5':0});r['queries']+=1;r['top1']+=references[order[0]]['id']==ident;r['top5']+=any(references[i]['id']==ident for i in order[:5])
(root/'production-scene-metrics.json').write_text(json.dumps(metrics,indent=2));print(json.dumps(metrics))
