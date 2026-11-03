import argparse,http.server,json,pathlib,threading,urllib.parse
parser=argparse.ArgumentParser();parser.add_argument('directory');parser.add_argument('port_file');args=parser.parse_args();root=pathlib.Path(args.directory);calls=[];lock=threading.Lock()
class Handler(http.server.SimpleHTTPRequestHandler):
 def __init__(self,*args,**kwargs):super().__init__(*args,directory=str(root),**kwargs)
 def log_message(self,*args):pass
 def do_GET(self):
  parsed=urllib.parse.urlsplit(self.path)
  if parsed.path=='/calls':
   with lock:body=json.dumps(calls).encode()
   self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Access-Control-Allow-Origin','*');self.end_headers();self.wfile.write(body);return
  if parsed.path.startswith('/preview/map-tile/') or parsed.path.startswith('/preview/image'):
   with lock:calls.append({'kind':'tile' if '/map-tile/' in parsed.path else 'image','path':parsed.path,'generation':urllib.parse.parse_qs(parsed.query).get('generation',[''])[0]})
   body=(root/'tile.png').read_bytes();self.send_response(200);self.send_header('Content-Type','image/png');self.send_header('Cache-Control','no-store');self.end_headers();self.wfile.write(body);return
  super().do_GET()
server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler);server.daemon_threads=True;pathlib.Path(args.port_file).write_text(str(server.server_port));server.serve_forever()
