import json,pathlib,numpy as np
r=pathlib.Path('/tmp/cineinsight-apple-validation');rows=list(map(json.loads,(r/'lfwpad-results.jsonl').read_text().splitlines()))
def vec(row):
 fs=row.get('faces',[])
 if not fs:return None
 v=np.array(max(fs,key=lambda f:f['bbox'][2]*f['bbox'][3])['embedding']);return v/np.linalg.norm(v)
rows=[x for x in rows if x['person']<40 and vec(x) is not None]
gal=[x for x in rows if x['person']<20 and x['shot']<2];unknown=[x for x in rows if 20<=x['person']<40];g=np.stack([vec(x) for x in gal]);u=np.stack([vec(x) for x in unknown]);scores=np.einsum('ik,jk->ij',u,g,optimize=False).max(1)
threshold=float(np.quantile(scores,.995,method='higher'))+1e-9
result=dict(threshold=threshold,target_calibration_false_identification=.005,calibration_gallery_people=20,calibration_unknown_people=20,calibration_unknown_queries=len(unknown),calibration_false_identification=int(sum(scores>=threshold)))
(r/'open-calibration.json').write_text(json.dumps(result,indent=2));print(result)
