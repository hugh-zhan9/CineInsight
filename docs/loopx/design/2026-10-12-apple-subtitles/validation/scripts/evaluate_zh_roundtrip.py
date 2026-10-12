import pathlib,json,subprocess,numpy as np
r=pathlib.Path('/tmp/cineinsight-apple-validation');zh=json.loads((r/'coco-queries-zh.json').read_text());helper=str(next(p for p in pathlib.Path(__file__).resolve().parents if (p/'go.mod').exists()) / 'build/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai');en=[]
for i in range(0,100,25):
 result=subprocess.run([helper],input=json.dumps(dict(operation='translate',source='zh-Hans',target='en',texts=zh[i:i+25])),text=True,capture_output=True,timeout=120,check=True);data=json.loads(result.stdout);assert 'error' not in data,data;en+=data['translations'];print('translated',len(en),flush=True)
(r/'coco-queries-roundtrip.json').write_text(json.dumps(en,ensure_ascii=False))
manifest=json.loads((r/'coco-manifest.json').read_text());features={x['id']:x for x in map(json.loads,(r/'coco-results.jsonl').read_text().splitlines())};labels=['An image of '+', '.join(x['label'].replace('_',' ') for x in features[row['id']]['labels'][:8])+'.' for row in manifest]
def embed(texts):
 result=subprocess.run([str(r/'embedding-worker')],input=json.dumps(dict(language='en',texts=texts))+'\n',text=True,capture_output=True,timeout=60,check=True);v=np.array(json.loads(result.stdout)['vectors'],dtype=float);return v/np.linalg.norm(v,axis=1,keepdims=True)
q,v=embed(en),embed(labels);scores=np.einsum('ik,jk->ij',q,v,optimize=False);order=np.argsort(-scores,axis=1);rank=np.argmax(order==np.arange(100)[:,None],axis=1)+1
metrics=dict(queries=100,recall_at_1=float(np.mean(rank<=1)),recall_at_5=float(np.mean(rank<=5)),recall_at_10=float(np.mean(rank<=10)),median_rank=float(np.median(rank)));(r/'scene-roundtrip-metrics.json').write_text(json.dumps(metrics,indent=2));print(metrics)
