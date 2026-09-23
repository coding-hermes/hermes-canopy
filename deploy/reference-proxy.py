#!/usr/bin/env python3
"""Reference same-origin reverse proxy for a production Hermes Canopy PWA.

WHY (DF-HERMES-CANOPY-7). `canopyd` is API-only in MVP — it serves the REST/SSE API
and never the PWA — so a built `frontend/dist` needs a server in front of it.
"Point any static server at frontend/dist" is dead by construction:

1. THE SPA FALLBACK MUST EXCLUDE /api. An SPA server (`npx serve -s`, CDN "SPA
   mode") answers EVERY unknown path with index.html, so `GET /api/v1/trees`
   returns HTML, the client's `res.json()` dies with `Unexpected token '<' ... is
   not valid JSON`, and the UI reports "Backend: unreachable". The fallback applies
   to app routes (`/tree/<id>`, `/trees`) ONLY; `/api/` and `/health` always go
   upstream to canopyd.
2. A STATIC BUILD IS UNAUTHENTICATED BY CONSTRUCTION. The Vite DEV server is what
   injects the dev JWT, and no `/api/v1/auth/*` endpoints exist (multi-user auth is
   deferred post-MVP), so a production bundle needs `VITE_API_TOKEN` baked at build
   time, a token pasted into `localStorage['canopy.token']`, or an authenticated
   reverse proxy injecting the header (below).

WHAT IT DOES. Serves `--dist` with an app-routes-only SPA fallback; streams `/api/`
and `/health` to `--api` chunk-by-chunk (no body buffering, so SSE arrives
incrementally); consumes a client `Authorization: Basic ...` header for the
proxy gate and never forwards it upstream; passes client-supplied non-Basic
`Authorization` headers (including Bearer) through untouched; and injects
`Authorization: Bearer <jwt>` from `--token`/`CANOPY_PROXY_TOKEN` ONLY when the
client sent no Authorization that can be forwarded. It REFUSES to inject a token
while bound to a non-loopback
address unless HTTP Basic (`--require-auth-user`/`--require-auth-password`) gates
the proxy, and refuses to start when `--port` is already bound.

LIMITATIONS. Dev-grade, single-user: no TLS, no compression/caching tuning, one
shared Basic credential over cleartext HTTP (terminate TLS in front). Beyond one
user, use a real server: `deploy/nginx.canopy.conf` (`proxy_buffering off`) or
`deploy/Caddyfile` (`flush_interval -1`) implement the same three behaviours.

USAGE
    python3 deploy/reference-proxy.py --dist frontend/dist --port 3000 \\
        --api http://127.0.0.1:8091 --token "$(cat token.jwt)" \\
        --require-auth-user canopy --require-auth-password secret

Env fallbacks: CANOPY_DIST, CANOPY_API, CANOPY_PROXY_PORT, CANOPY_PROXY_TOKEN.

VERIFICATION
    python3 -m unittest discover -s deploy/tests -v      # or: make test-proxy
"""

from __future__ import annotations

import argparse
import base64
import hmac
import http.client
import http.server
import ipaddress
import mimetypes
import os
import socket
import socketserver
import sys
import urllib.parse

DEFAULT_DIST, DEFAULT_API, DEFAULT_PORT = "frontend/dist", "http://127.0.0.1:8091", 3000
#: Never answered with the SPA fallback — always proxied upstream.
API_PREFIXES = ("/api/", "/health")
#: Hop-by-hop headers that must not be relayed (RFC 7230 §6.1).
HOP_BY_HOP = frozenset(
    {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te",
     "trailers", "transfer-encoding", "upgrade"}
)


def loopback(host: str) -> bool:
    """True when binding `host` cannot be reached from another machine."""
    if host in ("", "localhost"):
        return host == "localhost"
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


class CanopyProxyHandler(http.server.BaseHTTPRequestHandler):
    """Static files (SPA fallback for app routes) + streamed /api relay."""

    protocol_version = "HTTP/1.1"
    server_version = "canopy-reference-proxy/1.0"
    dist_dir, api_host, api_port = DEFAULT_DIST, "127.0.0.1", 8091
    api_tls, inject_token, basic_creds = False, None, None

    def log_message(self, fmt: str, *args: object) -> None:
        sys.stderr.write("[proxy] %s - %s\n" % (self.address_string(), fmt % args))

    def _reply(self, status: int, body: bytes, ctype: str = "text/plain; charset=utf-8",
               extra: list[tuple[str, str]] | None = None) -> None:
        """Single small reply path (also used by the 401/404/500/502 errors)."""
        self.send_response(status)
        for key, value in extra or []:
            self.send_header(key, value)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def _authorized(self) -> bool:
        """HTTP Basic gate — closed only when credentials were configured."""
        if self.basic_creds is None:
            return True
        header = self.headers.get("Authorization", "")
        if not header.lower().startswith("basic "):
            return False
        try:
            raw = base64.b64decode(header.split(" ", 1)[1], validate=True).decode()
        except (ValueError, UnicodeDecodeError):
            return False
        user, _, password = raw.partition(":")
        exp_user, exp_password = self.basic_creds
        return hmac.compare_digest(user, exp_user) and hmac.compare_digest(password, exp_password)

    def _handle(self) -> None:
        if not self._authorized():
            self._reply(401, b"proxy authentication required\n",
                        extra=[("WWW-Authenticate", 'Basic realm="canopy"')])
            return
        # /api and /health are NEVER answered with the SPA fallback.
        if self.path == "/health" or self.path.startswith(API_PREFIXES):
            self._proxy()
        else:
            self._serve_static()

    do_GET = do_HEAD = do_POST = do_PUT = do_PATCH = do_DELETE = do_OPTIONS = _handle

    def _proxy(self) -> None:
        """Relay to canopyd, flushing every chunk so SSE stays incremental."""
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else None
        headers = {k: v for k, v in self.headers.items()
                   if k.lower() not in HOP_BY_HOP and k.lower() not in ("host", "content-length")
                   and not (k.lower() == "authorization" and v.lower().startswith("basic "))}
        headers["Host"] = f"{self.api_host}:{self.api_port}"
        # Basic authenticates only to this proxy; forward non-Basic credentials,
        # and inject only when no upstream Authorization remains.
        if self.inject_token and not any(
            key.lower() == "authorization" and value for key, value in headers.items()
        ):
            headers["Authorization"] = f"Bearer {self.inject_token}"
        cls = http.client.HTTPSConnection if self.api_tls else http.client.HTTPConnection
        conn = cls(self.api_host, self.api_port, timeout=600)
        try:
            conn.request(self.command, self.path, body=body, headers=headers)
            upstream = conn.getresponse()
            self.send_response(upstream.status)
            for key, value in upstream.getheaders():
                # send_response() already emitted Server/Date — relaying the
                # upstream's copies would send them twice.
                if key.lower() in HOP_BY_HOP or key.lower() in ("content-length", "date", "server"):
                    continue
                self.send_header(key, value)
            # Chunked relay: needs no Content-Length, keeps the stream live.
            self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()
            while chunk := upstream.read1(65536):
                self.wfile.write(b"%x\r\n%s\r\n" % (len(chunk), chunk))
                self.wfile.flush()
            self.wfile.write(b"0\r\n\r\n")
            self.wfile.flush()
        except (OSError, http.client.HTTPException) as exc:
            self.log_message("upstream error for %s: %s", self.path, exc)
            try:
                self._reply(502, b"upstream unreachable\n")
            except OSError:
                pass
        finally:
            conn.close()

    def _serve_static(self) -> None:
        rel = urllib.parse.unquote(self.path.split("?", 1)[0]).lstrip("/")
        root = os.path.normpath(self.dist_dir)
        candidate = os.path.normpath(os.path.join(root, rel))
        target = candidate if (
            (candidate == root or candidate.startswith(root + os.sep))
            and os.path.isfile(candidate)
        ) else None
        if target is None:  # unknown app route (or traversal) → SPA fallback
            target = os.path.join(root, "index.html")
            self.log_message("SPA fallback %s -> index.html", self.path)
            if not os.path.isfile(target):
                self._reply(500, b"index.html missing from --dist\n")
                return
        try:
            with open(target, "rb") as handle:
                payload = handle.read()
        except OSError as exc:
            self.log_message("cannot read %s: %s", target, exc)
            self._reply(404, b"Not Found\n")
            return
        self.send_response(200)
        self.send_header("Content-Type", mimetypes.guess_type(target)[0] or "application/octet-stream")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(payload)


class CanopyProxyServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    # SO_REUSEADDR so an operator can restart immediately: without it, sockets a
    # previous instance accepted sit in TIME_WAIT for ~60s and the next start is
    # refused as "already bound" even though nothing is listening. It does NOT
    # allow two proxies on one port (that needs SO_REUSEPORT, which we never set)
    # — a genuinely busy port is still refused, by validate_args and by bind().
    allow_reuse_address = True


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Reference same-origin reverse proxy for a production Hermes Canopy PWA."
    )
    parser.add_argument("--dist", default=os.environ.get("CANOPY_DIST", DEFAULT_DIST),
                        help=f"directory holding the built PWA (default: {DEFAULT_DIST})")
    parser.add_argument("--api", default=os.environ.get("CANOPY_API", DEFAULT_API),
                        help=f"canopyd base URL (default: {DEFAULT_API})")
    parser.add_argument("--host", default="127.0.0.1", help="bind address (default: 127.0.0.1)")
    parser.add_argument("--port", type=int,
                        default=int(os.environ.get("CANOPY_PROXY_PORT", DEFAULT_PORT)),
                        help=f"listen port (default: {DEFAULT_PORT}; must be free)")
    parser.add_argument("--token", default=os.environ.get("CANOPY_PROXY_TOKEN"),
                        help="JWT injected as 'Authorization: Bearer <token>' when the client sent none")
    parser.add_argument("--require-auth-user", help="HTTP Basic user (gates the whole proxy)")
    parser.add_argument("--require-auth-password", help="HTTP Basic password")
    return parser


def validate_args(args: argparse.Namespace) -> tuple[bool, str, tuple[str, str] | None]:
    """Return (ok, refusal message, basic credentials). Refusals exit nonzero, loudly."""
    if not os.path.isfile(os.path.join(args.dist, "index.html")):
        return False, (f"--dist {args.dist!r} has no index.html — build the PWA first "
                       f"(`cd frontend && npm run build`, output: frontend/dist)."), None
    if bool(args.require_auth_user) != bool(args.require_auth_password):
        return False, "--require-auth-user and --require-auth-password must be given together.", None
    creds = (args.require_auth_user, args.require_auth_password)
    if args.token and not args.token.strip():
        return False, "--token was given but is blank/whitespace-only.", None
    # Trap: injecting a JWT on a reachable interface hands a live canopyd credential
    # to an anonymous caller.
    if args.token and not loopback(args.host) and not all(creds):
        return False, (
            f"refusing to inject a bearer token while bound to non-loopback address "
            f"{args.host!r} without authentication: add --require-auth-user/ "
            f"--require-auth-password (HTTP Basic), bind --host 127.0.0.1, or drop --token "
            f"and let the browser send its own token (VITE_API_TOKEN at build time, or "
            f"localStorage['canopy.token'])."), None
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            probe.bind((args.host or "0.0.0.0", args.port))
        except OSError as exc:
            return False, (f"port {args.port} is already bound on {args.host} ({exc}). Pick "
                           f"another with --port (the docs' :3000 example is not guaranteed "
                           f"free)."), None
    return True, "", (creds if all(creds) else None)


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    ok, message, basic_creds = validate_args(args)
    if not ok:
        sys.stderr.write(f"[proxy] REFUSING TO START: {message}\n")
        return 2
    parsed = urllib.parse.urlsplit(args.api if "://" in args.api else f"http://{args.api}")
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        sys.stderr.write(f"[proxy] REFUSING TO START: --api must be an http(s) URL with a "
                         f"host, got {args.api!r}\n")
        return 2
    handler = type("BoundCanopyProxyHandler", (CanopyProxyHandler,), {
        "dist_dir": os.path.abspath(args.dist),
        "api_host": parsed.hostname,
        "api_port": parsed.port or (443 if parsed.scheme == "https" else 80),
        "api_tls": parsed.scheme == "https",
        "inject_token": args.token.strip() if args.token else None,
        "basic_creds": basic_creds,
    })
    try:
        server = CanopyProxyServer((args.host, args.port), handler)
    except OSError as exc:
        sys.stderr.write(f"[proxy] REFUSING TO START: cannot bind {args.host}:{args.port} ({exc})\n")
        return 2
    sys.stderr.write(
        f"[proxy] serving {os.path.abspath(args.dist)} on http://{args.host}:{args.port}\n"
        f"[proxy] proxying /api/ and /health -> {args.api} (streamed, unbuffered)\n"
        f"[proxy] token injection: {'on when client sends none' if args.token else 'off'}; "
        f"basic auth: {'on' if basic_creds else 'off'}\n")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        sys.stderr.write("[proxy] shutting down\n")
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
