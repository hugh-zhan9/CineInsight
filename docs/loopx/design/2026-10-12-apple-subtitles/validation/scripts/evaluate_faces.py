import pathlib,json,numpy as np,collections,random
root=pathlib.Path('/tmp/cineinsight-apple-validation')
def evaluate(name):
 rows=[json.loads(line) for line in (root/(name+'-results.jsonl')).read_text().splitlines()];selected=[];multiface=0
 for r in rows:
  faces=r.get('faces',[])
  if len(faces)>1:multiface+=1
  if not faces:continue
  # LFW's labelled subject is central. Select the largest detected face, as a generic photo pipeline would.
  f=max(faces,key=lambda x:x['bbox'][2]*x['bbox'][3]);v=np.asarray(f['embedding'],dtype=float);v/=np.linalg.norm(v)
  selected.append((r,v))
 npeople=max(r['person'] for r in rows)+1;split=npeople//2
 def pairs(subset):
  matrix=np.stack([v for r,v in subset]);labels=np.array([r['person'] for r,v in subset]);i,j=np.triu_indices(len(labels),1)
  score=np.einsum('ik,jk->ij',matrix,matrix,optimize=False)[i,j];assert np.isfinite(score).all() and np.abs(score).max()<=1.000001; same=labels[i]==labels[j];return score[same],score[~same]
 cal=[item for item in selected if item[0]['person']<split];test=[item for item in selected if item[0]['person']>=split]
 pos,neg=pairs(cal);threshold=float(np.quantile(neg,0.999,method='higher'))+1e-9
 p,n=pairs(test)
 gallery=[item for item in test if item[0]['shot']<2];queries=[item for item in test if item[0]['shot']>=2]
 g=np.stack([v for r,v in gallery]);gl=np.array([r['person'] for r,v in gallery]);q=np.stack([v for r,v in queries]);ql=np.array([r['person'] for r,v in queries]);scores=np.einsum('ik,jk->ij',q,g,optimize=False);best=scores.argmax(1);correct=gl[best]==ql
 expected_test=sum(r['person']>=split for r in rows);expected_queries=sum(r['person']>=split and r['shot']>=2 for r in rows)
 def cluster(seed):
  ordered=test.copy();random.Random(seed).shuffle(ordered);clusters=[];assignments=[]
  for r,v in ordered:
   matches=[float(v@c['v']) for c in clusters];index=int(np.argmax(matches)) if matches and max(matches)>=threshold else len(clusters)
   if index==len(clusters):clusters.append(dict(v=v.copy(),n=1,labels=[r['person']]))
   else:
    c=clusters[index];merged=(c['v']*c['n']+v)/(c['n']+1);c['v']=merged/np.linalg.norm(merged);c['n']+=1;c['labels'].append(r['person'])
   assignments.append((r['person'],index))
  tp=fp=fn=0
  for i,(a,ac) in enumerate(assignments):
   for b,bc in assignments[i+1:]:
    tp+=a==b and ac==bc;fp+=a!=b and ac==bc;fn+=a==b and ac!=bc
  return dict(clusters=len(clusters),mixed_clusters=sum(len(set(c['labels']))>1 for c in clusters),pair_precision=tp/(tp+fp) if tp+fp else 0,pair_recall=tp/(tp+fn) if tp+fn else 0)
 report=dict(dataset=name,images=len(rows),identities=npeople,detected=len(selected),detection_rate=len(selected)/len(rows),multi_face_images=multiface,calibration_people=split,test_people=npeople-split,threshold=threshold,calibration_true_accept=float(np.mean(pos>=threshold)),test_positive_pairs=len(p),test_negative_pairs=len(n),test_true_accept=float(np.mean(p>=threshold)),test_false_accept=float(np.mean(n>=threshold)),gallery_images=len(gallery),query_images=len(queries),rank1_detected=float(np.mean(correct)),rank1_all=correct.sum()/expected_queries,rejected_queries=int(np.sum(scores.max(1)<threshold)),wrong_accepted_queries=int(np.sum((~correct)&(scores.max(1)>=threshold))),test_detection=len(test)/expected_test,clusters=[cluster(seed) for seed in range(5)],existing_055_true_accept=float(np.mean(p>=.55)),existing_055_false_accept=float(np.mean(n>=.55)))
 (root/(name+'-metrics.json')).write_text(json.dumps(report,indent=2));print(json.dumps(report,indent=2))
import sys
for name in sys.argv[1:]:evaluate(name)
