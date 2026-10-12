import json,pathlib,numpy as np,random
r=pathlib.Path('/tmp/cineinsight-apple-validation');rows=list(map(json.loads,(r/'fresh-results.jsonl').read_text().splitlines()));assert len(rows)==400
threshold=json.loads((r/'open-calibration.json').read_text())['threshold']
def vec(row):
 fs=row.get('faces',[])
 if not fs:return None
 v=np.array(max(fs,key=lambda f:f['bbox'][2]*f['bbox'][3])['embedding']);return v/np.linalg.norm(v)
valid=[x for x in rows if vec(x) is not None];gallery=[x for x in valid if x['person']<120 and x['shot']<2];query=[x for x in valid if x['person']<120 and x['shot']>=2];unknown=[x for x in valid if x['person']>=120]
g=np.stack([vec(x) for x in gallery]);q=np.stack([vec(x) for x in query]);u=np.stack([vec(x) for x in unknown]);scores=np.einsum('ik,jk->ij',q,g,optimize=False);uscores=np.einsum('ik,jk->ij',u,g,optimize=False);pred=[gallery[i]['person'] for i in scores.argmax(1)];correct=np.array([p==x['person'] for p,x in zip(pred,query)]);accepted=scores.max(1)>=threshold
result=dict(threshold=threshold,images=len(rows),detected=len(valid),test_known_people=20,test_unknown_people=20,gallery=len(gallery),known_queries=len(query),known_rank1=int(sum(correct))/160,known_correct_accepted=int(sum(correct&accepted)),known_wrong_accepted=int(sum((~correct)&accepted)),known_rejected=int(sum(~accepted)),unknown_queries=len(unknown),unknown_wrong_accepted=int(sum(uscores.max(1)>=threshold)))
cluster_results=[]
for seed in range(5):
 ordered=valid.copy();random.Random(seed).shuffle(ordered);clusters=[];labels=[]
 for row in ordered:
  v=vec(row);s=[float(v@c['v']) for c in clusters];index=int(np.argmax(s)) if s and max(s)>=threshold else len(clusters)
  if index==len(clusters):clusters.append(dict(v=v,n=1))
  else:
   c=clusters[index];merged=(c['v']*c['n']+v)/(c['n']+1);c['v']=merged/np.linalg.norm(merged);c['n']+=1
  labels.append((row['person'],index))
 tp=fp=fn=0
 for i,(p,c) in enumerate(labels):
  for p2,c2 in labels[i+1:]:tp+=p==p2 and c==c2;fp+=p!=p2 and c==c2;fn+=p==p2 and c!=c2
 cluster_results.append(dict(clusters=len(clusters),pair_precision=tp/(tp+fp),pair_recall=tp/(tp+fn),wrong_pairs=fp))
result['clusters']=cluster_results
(r/'fresh-metrics.json').write_text(json.dumps(result,indent=2));print(json.dumps(result,indent=2))
