#!/usr/bin/env python3
"""rackanvil test infrastructure: Debian mirror reverse proxy.

Why this exists: in sandboxes where the install target (QEMU guest) cannot
reach the public internet, the guest fetches Debian packages through this
proxy running on the host (which can). The guest uses it as a plain Debian
mirror (http://<host>:<port>/debian) -- exactly how a real deployment would
use a local mirror ("弹药库"). Content is forwarded byte-identical; the
installer still verifies Debian archive signatures itself.

Usage: mirror-proxy.py <listen-port> <upstream-base>
  e.g. mirror-proxy.py 18998 http://mirrors.aliyun.com
  Forwards the full request path (e.g. /debian/dists/...) to <upstream-base>.
"""
import hashlib
import os
import shutil
import sys
import urllib.request
import urllib.error
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(sys.argv[1])
UPSTREAM = sys.argv[2].rstrip("/")
# Persistent on-disk cache (survives sandbox restarts; retries get faster).
CACHE_DIR = sys.argv[3] if len(sys.argv) > 3 else "/tmp/mirror-cache"
os.makedirs(CACHE_DIR, exist_ok=True)


def cache_path(path):
    h = hashlib.sha256(path.encode()).hexdigest()[:32]
    # keep a readable suffix for debugging
    suffix = os.path.basename(path).replace("/", "_")[:60]
    return os.path.join(CACHE_DIR, f"{h}_{suffix}")


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        # Only serve the /debian tree; refuse everything else.
        if not self.path.startswith("/debian/"):
            self.send_error(404)
            return
        cpath = cache_path(self.path)
        if os.path.exists(cpath):
            self.serve_file(cpath)
            return
        url = UPSTREAM + self.path  # UPSTREAM is the mirror root, path starts with /debian/
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "rackanvil-mirror-proxy"})
            upstream = urllib.request.urlopen(req, timeout=60)
        except urllib.error.HTTPError as e:
            self.send_error(e.code, f"upstream: {e.reason}")
            return
        except Exception as e:  # noqa: BLE001 - report upstream failure honestly
            self.send_error(502, f"upstream fetch failed: {e}")
            return
        # Stream to client while saving to cache.
        self.send_response(upstream.status)
        self.send_header("Content-Type", upstream.headers.get("Content-Type", "application/octet-stream"))
        length = upstream.headers.get("Content-Length")
        if length:
            self.send_header("Content-Length", length)
        self.end_headers()
        try:
            with open(cpath + ".tmp", "wb") as f:
                while True:
                    chunk = upstream.read(65536)
                    if not chunk:
                        break
                    f.write(chunk)
                    self.wfile.write(chunk)
            os.rename(cpath + ".tmp", cpath)
        finally:
            upstream.close()

    def serve_file(self, cpath):
        size = os.path.getsize(cpath)
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(size))
        self.end_headers()
        with open(cpath, "rb") as f:
            shutil.copyfileobj(f, self.wfile)

    def log_message(self, *args):
        pass  # keep test logs clean


if __name__ == "__main__":
    print(f"[mirror-proxy] listening on 127.0.0.1:{PORT}, upstream {UPSTREAM}", flush=True)
    HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
