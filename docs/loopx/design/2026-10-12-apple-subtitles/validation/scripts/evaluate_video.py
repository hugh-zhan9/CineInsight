import json,pathlib,numpy as np
r=pathlib.Path('/tmp/cineinsight-apple-validation')
def rows(name):return list(map(json.loads,(r/(name+'-results.jsonl')).read_text().splitlines()))
def vector(row):
 fs=row.get('faces',[])
 if not fs:return None
 v=np.array(max(fs,key=lambda f:f['bbox'][2]*f['bbox'][3])['embedding']);return v/np.linalg.norm(v)
base=rows('fresh');gallery=[x for x in base if x['person']<120 and x['shot']<2 and vector(x) is not None];queries=[x for x in rows('face-video') if vector(x) is not None]
g=np.stack([vector(x) for x in gallery]);q=np.stack([vector(x) for x in queries]);scores=np.einsum('ik,jk->ij',q,g,optimize=False);correct=np.array([gallery[b]['person']==row['person'] for b,row in zip(scores.argmax(1),queries)]);threshold=json.loads((r/'open-calibration.json').read_text())['threshold'];accepted=scores.max(1)>=threshold
face=dict(frames=160,extracted=len(queries),rank1_all=int(sum(correct))/160,correct_accepted=int(sum(correct&accepted)),wrong_accepted=int(sum((~correct)&accepted)),rejected=int(sum(~accepted)))
images=rows('coco');queries=rows('scene-video');g=np.array([x['embedding'] for x in images]);q=np.array([x['embedding'] for x in queries]);distance=np.sum((q[:,None,:]-g[None,:,:])**2,axis=2);order=np.argsort(distance,axis=1);keys=[x['id'] for x in images];ranks=[int(np.where(order[i]==keys.index(row['id'].split('-')[0]))[0][0])+1 for i,row in enumerate(queries)]
scene=dict(frames=len(queries),recall_at_1=float(np.mean(np.array(ranks)<=1)),recall_at_5=float(np.mean(np.array(ranks)<=5)))
result=dict(face_video=face,scene_video=scene);(r/'video-metrics.json').write_text(json.dumps(result,indent=2));print(json.dumps(result,indent=2))
