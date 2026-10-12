import pathlib,json,subprocess,numpy as np
root=pathlib.Path('/tmp/cineinsight-apple-validation');manifest=json.loads((root/'coco-manifest.json').read_text());features={r['id']:r for r in map(json.loads,(root/'coco-results.jsonl').read_text().splitlines())}
assert len(manifest)==100 and len(features)==100
helper=next(p for p in pathlib.Path(__file__).resolve().parents if (p/'go.mod').exists()) / 'build/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai'
def translate(texts):
 result=[]
 for start in range(0,len(texts),50):
  req=dict(operation='translate',source='en',target='zh-Hans',texts=texts[start:start+50]);r=subprocess.run([str(helper)],input=json.dumps(req),text=True,capture_output=True,timeout=120,check=True);data=json.loads(r.stdout);assert 'error' not in data,data;result.extend(data['translations'])
 return result
def embed(texts,lang):
 r=subprocess.run([str(root/'embedding-worker')],input=json.dumps(dict(texts=texts,language=lang))+'\n',text=True,capture_output=True,timeout=60,check=True);data=json.loads(r.stdout);assert 'error' not in data,data
 vectors=np.array(data['vectors'],dtype=float);assert len(vectors)==len(texts);return vectors/np.linalg.norm(vectors,axis=1,keepdims=True)
labels=['An image of '+', '.join(x['label'].replace('_',' ') for x in features[row['id']]['labels'][:8])+'.' for row in manifest]
# Five human captions per image: use caption 0 for the reference-description control, caption 1 only as the query.
caption_index=[row['captions'][0] for row in manifest];query_en=[row['captions'][1] for row in manifest];query_zh=translate(query_en)
(root/'coco-queries-zh.json').write_text(json.dumps(query_zh,ensure_ascii=False,indent=2))
label_zh=translate(labels);caption_zh=translate(caption_index)
reports={};details=[]
for language,queries,index,reference in [('en',query_en,labels,caption_index),('zh-Hans',query_zh,label_zh,caption_zh)]:
 q=embed(queries,language);v=embed(index,language);ref=embed(reference,language)
 for kind,vectors in [('system_labels',v),('human_caption_control',ref)]:
  similarity=np.einsum('ik,jk->ij',q,vectors,optimize=False);order=np.argsort(-similarity,axis=1);ranks=np.argmax(order==np.arange(100)[:,None],axis=1)+1
  reports[language+'_'+kind]=dict(queries=100,recall_at_1=float(np.mean(ranks<=1)),recall_at_5=float(np.mean(ranks<=5)),recall_at_10=float(np.mean(ranks<=10)),median_rank=float(np.median(ranks)))
  if language=='zh-Hans' and kind=='system_labels':
   for i in range(100):details.append(dict(query=query_zh[i],target_id=manifest[i]['id'],rank=int(ranks[i]),top_id=manifest[int(order[i,0])]['id'],target_caption=query_en[i],top_caption=query_en[int(order[i,0])],target_labels=labels[i],top_labels=labels[int(order[i,0])]))
(root/'scene-text-metrics.json').write_text(json.dumps(reports,indent=2));(root/'scene-text-details.json').write_text(json.dumps(details,ensure_ascii=False,indent=2));print(json.dumps(reports,indent=2))
