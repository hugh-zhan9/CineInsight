"""Synthetic loopback assets only; includes Range for real WKWebView decoding."""
import http.server
import pathlib
import sys
import threading
import urllib.parse

root = pathlib.Path(sys.argv[1])
gate_lock = threading.Lock()
gates = {}


def photo_gate(media_id):
    with gate_lock:
        if media_id not in gates:
            gates[media_id] = {key: threading.Event() for key in ('started', 'release', 'finished')}
        return gates[media_id]


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        url = urllib.parse.urlsplit(self.path)
        path = url.path
        finished = None
        if path.startswith('/photo-gate/'):
            _, _, action, media_id = path.split('/')
            event = photo_gate(int(media_id))[action]
            if action == 'release':
                event.set()
            elif not event.wait(12):
                self.send_error(504)
                return
            # An image request also works from the file:// fixture without fetch permission.
            body = b'<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>'
            self.send_response(200)
            self.send_header('Content-Type', 'image/svg+xml')
            self.send_header('Content-Length', str(len(body)))
        elif path.startswith('/preview/image/') and 'exploration_scope' in urllib.parse.parse_qs(url.query):
            media_id = int(path.rsplit('/', 1)[-1])
            gate = photo_gate(media_id)
            gate['started'].set()
            if not gate['release'].wait(20):
                self.send_error(504)
                return
            body = (root / ('photo-odd.jpg' if media_id % 2 else 'photo-even.jpg')).read_bytes()
            finished = gate['finished']
            self.send_response(200)
            self.send_header('Content-Type', 'image/jpeg')
            self.send_header('Content-Length', str(len(body)))
        elif path.startswith('/preview/'):
            media_id = int(path.rsplit('/', 1)[-1])
            hue = media_id * 137 % 360
            body = (f'<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256">'
                    f'<rect width="256" height="256" fill="hsl({hue},25%,55%)"/>'
                    f'<circle cx="180" cy="60" r="22" fill="hsl({hue},30%,80%)"/>'
                    f'<path d="M0 200L70 100L145 180L200 125L256 190V256H0Z" fill="hsl({hue},25%,25%)"/></svg>').encode()
            self.send_response(200)
            self.send_header('Content-Type', 'image/svg+xml')
            self.send_header('Content-Length', str(len(body)))
        elif path == '/clip.mp4':
            data = (root / 'clip.mp4').read_bytes()
            start, end = 0, len(data) - 1
            value = self.headers.get('Range')
            if value:
                bounds = value.removeprefix('bytes=').split(',')[0].split('-')
                start = int(bounds[0] or 0)
                end = min(int(bounds[1]) if bounds[1] else end, end)
            body = data[start:end + 1]
            self.send_response(206 if value else 200)
            self.send_header('Content-Type', 'video/mp4')
            self.send_header('Accept-Ranges', 'bytes')
            self.send_header('Content-Length', str(len(body)))
            if value:
                self.send_header('Content-Range', f'bytes {start}-{end}/{len(data)}')
        else:
            self.send_error(404)
            return
        self.send_header('Access-Control-Allow-Origin', '*')
        self.send_header('Cache-Control', 'no-store')
        try:
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            if finished:
                finished.set()

server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
server.daemon_threads = True
pathlib.Path(sys.argv[2]).write_text(str(server.server_port))
server.serve_forever()
