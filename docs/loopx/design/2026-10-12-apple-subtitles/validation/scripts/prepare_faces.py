import pathlib,struct,numpy as np,json
from PIL import Image
root=pathlib.Path('/tmp/cineinsight-apple-validation');b=(root/'olivetti.mat').read_bytes();endian='>' if b[126:128]==b'MI' else '<'
def elements(data):
 pos=0
 while pos+8<=len(data):
  tag=struct.unpack(endian+'I',data[pos:pos+4])[0]
  if tag>>16:
   kind,n=tag&65535,tag>>16;value=data[pos+4:pos+4+n];pos+=8
  else:
   kind,n=struct.unpack(endian+'II',data[pos:pos+8]);value=data[pos+8:pos+8+n];pos+=8+((n+7)//8)*8
  yield kind,value
for kind,outer in elements(b[128:]):
 if kind!=14:continue
 parts=list(elements(outer));name=parts[2][1].decode()
 if name!='faces':continue
 dims=struct.unpack(endian+'i'*(len(parts[1][1])//4),parts[1][1]);dtype={2:'u1',7:'f4',9:'f8'}[parts[3][0]]
 faces=np.frombuffer(parts[3][1],dtype=endian+dtype).reshape(dims,order='F').T.reshape(400,64,64).transpose(0,2,1)
 print('faces',faces.shape,faces.min(),faces.max())
 records=[];out=root/'orl';out.mkdir(exist_ok=True)
 for index,array in enumerate(faces):
  ident=f'orl-{index//10:02d}-{index%10:02d}';path=out/(ident+'.png')
  Image.fromarray(array.astype('uint8')).resize((256,256),Image.Resampling.BICUBIC).save(path)
  records.append(dict(id=ident,path=str(path),person=index//10,shot=index%10,dataset='orl',op='face'))
 (root/'orl-manifest.json').write_text(json.dumps(records))
