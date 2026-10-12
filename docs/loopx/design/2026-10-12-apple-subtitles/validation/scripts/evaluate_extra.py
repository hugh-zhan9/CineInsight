import pathlib,json,numpy as np
r=pathlib.Path('/tmp/cineinsight-apple-validation')
def rows(name):return list(map(json.loads,(r/(name+'-results.jsonl')).read_text().splitlines()))
def face(x):
 fs=x.get('faces',[])
 if not fs:return None
 v=np.array(max(fs,key=lambda f:f['bbox'][2]*f['bbox'][3])['embedding']);return v/np.linalg.norm(v)
known=rows('lfw');unknown=rows('unknown');gallery=[x for x in known if x['person']>=40 and x['shot']<2];g=np.stack([face(x) for x in gallery]);uq=[x for x in unknown if face(x) is not None];u=np.stack([face(x) for x in uq]);scores=np.einsum('ik,jk->ij',u,g,optimize=False);threshold=json.loads((r/'lfw-metrics.json').read_text())['threshold']
unknown_result=dict(unknown_people=20,queries=len(unknown),extracted=len(uq),wrongly_accepted=int(np.sum(scores.max(1)>=threshold)),threshold=threshold)
images=rows('coco');queries=rows('coco-query');gallery=np.array([x['embedding'] for x in images]);keys=[x['id'] for x in images];report={}
for variant in ['compressed','crop']:
 subset=[x for x in queries if x['id'].endswith('-'+variant)];q=np.array([x['embedding'] for x in subset]);distances=np.sum((q[:,None,:]-gallery[None,:,:])**2,axis=2);order=np.argsort(distances,axis=1);ranks=[]
 for i,x in enumerate(subset):ranks.append(int(np.where(order[i]==keys.index(x['id'].split('-')[0]))[0][0])+1)
 report[variant]=dict(queries=len(subset),recall_at_1=float(np.mean(np.array(ranks)<=1)),recall_at_5=float(np.mean(np.array(ranks)<=5)),ranks=ranks)
(r/'unknown-metrics.json').write_text(json.dumps(unknown_result,indent=2));(r/'scene-image-metrics.json').write_text(json.dumps(report,indent=2));print('unknown',unknown_result);print('image_retrieval',{k:{a:b for a,b in v.items() if a!='ranks'} for k,v in report.items()})
