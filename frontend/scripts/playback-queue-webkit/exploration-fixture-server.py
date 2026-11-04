"""Synthetic loopback assets only; includes Range for real WKWebView decoding."""
import http.server
import pathlib
import sys
import urllib.parse

root = pathlib.Path(sys.argv[1])

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        path = urllib.parse.urlsplit(self.path).path
        if path.startswith('/preview/'):
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
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass

server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
server.daemon_threads = True
pathlib.Path(sys.argv[2]).write_text(str(server.server_port))
server.serve_forever()
