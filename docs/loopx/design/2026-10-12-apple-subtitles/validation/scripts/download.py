import pathlib,urllib.request,hashlib,time,concurrent.futures
root=pathlib.Path('/tmp/cineinsight-apple-validation')
items=[('lfw.tgz','https://ndownloader.figshare.com/files/5976018','055f7d9c632d7370e6fb4afc7468d40f970c34a80d4c6f50ffec63f5a8d536c0'),('olivetti.mat','https://ndownloader.figshare.com/files/5976027','b612fb967f2dc77c9c62d3e1266e0c73d5fca46a4b8906c18e454d41af987794')]
def download(item):
 name,url,expected=item;path=root/name
 if not path.exists():
  with urllib.request.urlopen(url,timeout=30) as response,open(path,'wb') as out:
   count=0;last=time.monotonic()
   while data:=response.read(1024*1024):
    out.write(data);count+=len(data)
    if time.monotonic()-last>15: print(name,count,flush=True);last=time.monotonic()
 actual=hashlib.sha256(path.read_bytes()).hexdigest();assert actual==expected,(name,actual)
 print(name,'verified',path.stat().st_size,flush=True)
with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:list(pool.map(download,items))
