import sys,json,subprocess,pathlib,time,selectors
root=pathlib.Path('/tmp/cineinsight-apple-validation');name=sys.argv[1];records=json.loads((root/(name+'-manifest.json')).read_text());output=root/(name+'-results.jsonl')
worker=subprocess.Popen([str(root/'vision-worker')],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=open(root/(name+'-stderr.log'),'w'),text=True,bufsize=1)
selector=selectors.DefaultSelector();selector.register(worker.stdout,selectors.EVENT_READ)
start=time.monotonic();count=0
try:
 with output.open('w') as target:
  for row in records:
   worker.stdin.write(json.dumps(row)+'\n');worker.stdin.flush()
   if not selector.select(45):raise TimeoutError(row['id'])
   response=worker.stdout.readline();value=json.loads(response);value.update({k:row[k] for k in ('person','shot','dataset') if k in row});target.write(json.dumps(value)+'\n');target.flush();count+=1
   if count%25==0:print(name,count,'/',len(records),'seconds',round(time.monotonic()-start,1),flush=True)
 worker.stdin.close();worker.wait(timeout=10)
finally:
 if worker.poll() is None:worker.kill();worker.wait()
print('completed',name,count,'seconds',time.monotonic()-start,flush=True)
