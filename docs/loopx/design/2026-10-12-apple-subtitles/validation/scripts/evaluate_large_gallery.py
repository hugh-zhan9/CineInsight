import json,pathlib,numpy as np
root=pathlib.Path('/tmp/cineinsight-apple-validation')
def rows(name):return list(map(json.loads,(root/(name+'-results.jsonl')).read_text().splitlines()))
def vector(row):
    if not row.get('faces'):return None
    result=np.array(max(row['faces'],key=lambda face:face['bbox'][2]*face['bbox'][3])['embedding'])
    return result/np.linalg.norm(result)
gallery=[x for x in rows('lfwpad')+rows('unknownpad')+rows('fresh') if x['person']<120 and x['shot']<2 and vector(x) is not None]
known=[x for x in rows('fresh') if 100<=x['person']<120 and x['shot']>=2 and vector(x) is not None]
unknown=[x for x in rows('fresh') if x['person']>=120 and vector(x) is not None]
g=np.stack([vector(x) for x in gallery]);q=np.stack([vector(x) for x in known]);u=np.stack([vector(x) for x in unknown])
scores=np.einsum('ik,jk->ij',q,g,optimize=False);uscores=np.einsum('ik,jk->ij',u,g,optimize=False)
threshold=json.loads((root/'open-calibration.json').read_text())['threshold']
correct=np.array([gallery[i]['person']==x['person'] for i,x in zip(scores.argmax(1),known)]);accepted=scores.max(1)>=threshold
result=dict(gallery_people=len({x['person'] for x in gallery}),gallery_images=len(gallery),known_queries=len(known),known_rank1=int(sum(correct))/len(known),known_correct_accepted=int(sum(correct&accepted)),known_wrong_accepted=int(sum((~correct)&accepted)),known_rejected=int(sum(~accepted)),unknown_queries=len(unknown),unknown_wrong_accepted=int(sum(uscores.max(1)>=threshold)),threshold=threshold)
(root/'large-gallery-metrics.json').write_text(json.dumps(result,indent=2));print(json.dumps(result,indent=2))
