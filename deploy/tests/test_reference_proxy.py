#!/usr/bin/env python3
"""End-to-end tests for ``deploy/reference-proxy.py`` (DF-HERMES-CANOPY-7).

These tests drive the REAL proxy as a subprocess over HTTP and assert the five
behaviours the deployment recipe depends on: static assets, the SPA fallback,
unauthenticated rejection (never the SPA shell), authenticated API JSON with
bearer injection/pass-through, upstream error preservation, and — the reason the
proxy exists at all — incremental SSE delivery (a relay that buffers the body
turns every event stream into one late blob).

Nothing here needs a live ``canopyd``, PostgreSQL, or any host service: the
upstream is a throwaway ``http.server`` on an ephemeral loopback port that
impersonates canopyd's relevant contract. Python 3 standard library only.

    python3 -m unittest discover -s deploy/tests -v
    make test-proxy
"""

from __future__ import annotations

import contextlib
import http.client
import json
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from types import SimpleNamespace
from urllib.parse import urlsplit

REPO_ROOT = Path(__file__).resolve().parents[2]
PROXY = REPO_ROOT / "deploy" / "reference-proxy.py"

#: Marks the dist shell, so "did the SPA fallback answer an /api request?" is a
#: string assertion instead of a content-type guess.
INDEX_MARKER = "CANOPY_INDEX_SHELL_MARKER_9f3a1c"
ASSET_NAME = "assets/app.js"
ASSET_BODY = (b"/* CANOPY_ASSET_MARKER_7c1d */\n"
              b"console.log('canopy asset payload');\n")

#: JWT-shaped (three base64url segments) — the proxy only ever concatenates it.
INJECTED_TOKEN = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYW5vcHktcHJveHktdGVzdCJ9.c2lnbmF0dXJl"
CLIENT_TOKEN = "client-supplied-bogus-jwt"

#: How long the stub upstream waits between flushing SSE event 1 and writing
#: event 2. Comfortably longer than the proxy can take to relay the first event,
#: so "first event arrived before the upstream wrote the second" is a real test
#: of non-buffering rather than a timing coin flip.
SSE_GAP_SECONDS = 1.5
#: Budget for event 1 to reach the client, measured from the instant the stub
#: starts writing the stream. A buffering relay cannot come close (it needs the
#: whole upstream body, i.e. >= SSE_GAP_SECONDS).
SSE_FIRST_EVENT_BUDGET = SSE_GAP_SECONDS * 0.6

READY_TIMEOUT_SECONDS = 20.0
HTTP_TIMEOUT_SECONDS = 15.0


# --------------------------------------------------------------------------- #
# Throwaway upstream (stands in for canopyd)
# --------------------------------------------------------------------------- #
class StubUpstream(ThreadingHTTPServer):
    """Ephemeral loopback HTTP server impersonating canopyd's API contract."""

    daemon_threads = True
    allow_reuse_address = True

    def __init__(self) -> None:
        super().__init__(("127.0.0.1", 0), StubUpstreamHandler)
        self.requests: list[dict] = []
        self.stream: dict[str, float] = {}
        self.lock = threading.Lock()

    @property
    def port(self) -> int:
        return int(self.server_address[1])

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def requests_for(self, path: str) -> list[dict]:
        """Every upstream request seen for ``path`` (query string ignored)."""
        with self.lock:
            return [r for r in self.requests if r["path"].split("?", 1)[0] == path]


class StubUpstreamHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "canopyd-stub/1.0"

    #: Paths that require `Authorization` — the real canopyd answers 401 without it.
    AUTH_REQUIRED = ("/api/v1/trees", "/api/v1/health", "/api/v1/stream", "/health")

    def log_message(self, fmt: str, *args: object) -> None:  # keep test output clean
        pass

    # -- helpers ------------------------------------------------------------ #
    def _record(self) -> str | None:
        auth = self.headers.get("Authorization")
        with self.server.lock:
            self.server.requests.append({
                "method": self.command,
                "path": self.path,
                "authorization": auth,
            })
        return auth

    def _json(self, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def _unauthorized(self, path: str, seen_auth: str | None) -> None:
        self._json(401, {"error": {
            "code": "TOKEN_MISSING",
            "message": f"missing bearer token for {path}",
            "received_authorization": seen_auth,
        }})

    def _stream(self) -> None:
        """SSE: flush event 1, wait, flush event 2 — the non-buffering probe."""
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        with self.server.lock:
            self.server.stream["started_at"] = time.monotonic()
        self.wfile.write(b"event: tick\ndata: first\n\n")
        self.wfile.flush()
        with self.server.lock:
            self.server.stream["first_event_at"] = time.monotonic()
        time.sleep(SSE_GAP_SECONDS)
        with self.server.lock:
            # Recorded BEFORE the second event is written: a client that already
            # holds event 1 by this instant proves the relay never buffered.
            self.server.stream["second_event_flush_at"] = time.monotonic()
        self.wfile.write(b"event: tick\ndata: second\n\n")
        self.wfile.flush()
        time.sleep(0.05)
        with self.server.lock:
            self.server.stream["finished_at"] = time.monotonic()

    # -- routes ------------------------------------------------------------- #
    def do_GET(self) -> None:  # noqa: N802 (http.server API)
        seen_auth = self._record()
        path = urlsplit(self.path).path
        if path in self.AUTH_REQUIRED and not seen_auth:
            return self._unauthorized(path, seen_auth)
        if path == "/api/v1/trees":
            return self._json(200, {
                "trees": [{"id": "tree-1", "title": "demo"}],
                "received_authorization": seen_auth,
            })
        if path in ("/api/v1/health", "/health"):
            return self._json(200, {"status": "ok", "received_authorization": seen_auth})
        if path == "/api/v1/broken":
            return self._json(502, {"error": {
                "code": "UPSTREAM_EXPLODED",
                "message": "canopyd failed to reach postgres",
            }})
        if path == "/api/v1/stream":
            return self._stream()
        return self._json(404, {"error": {"code": "NOT_FOUND", "message": path}})


# --------------------------------------------------------------------------- #
# Fixtures
# --------------------------------------------------------------------------- #
def make_dist(root: Path) -> Path:
    """A minimal but real build output: shell + one asset."""
    dist = root / "dist"
    (dist / "assets").mkdir(parents=True, exist_ok=True)
    (dist / "index.html").write_bytes(
        b"<!doctype html>\n<html><head><title>Canopy</title></head><body>"
        b'<div id="root"></div>' + INDEX_MARKER.encode() + b"</body></html>\n"
    )
    (dist / ASSET_NAME).write_bytes(ASSET_BODY)
    return dist


def free_port() -> int:
    """A port nothing is listening on right now (the proxy re-checks on bind)."""
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return int(probe.getsockname()[1])


def proxy_argv(*, dist: Path, api: str, port: int, host: str = "127.0.0.1",
               token: str | None = None, basic: tuple[str, str] | None = None) -> list[str]:
    argv = [sys.executable, str(PROXY), "--dist", str(dist), "--api", api,
            "--host", host, "--port", str(port)]
    if token is not None:
        argv += ["--token", token]
    if basic is not None:
        argv += ["--require-auth-user", basic[0], "--require-auth-password", basic[1]]
    return argv


def http_get(port: int, path: str, headers: dict | None = None,
             timeout: float = HTTP_TIMEOUT_SECONDS):
    """GET through the proxy; returns (response, body) with the body fully read."""
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)
    try:
        conn.request("GET", path, headers=headers or {})
        resp = conn.getresponse()
        body = resp.read()
        return resp, body
    finally:
        conn.close()


def wait_until_serving(fx: SimpleNamespace, timeout: float = READY_TIMEOUT_SECONDS) -> None:
    """Poll the static shell until the proxy answers — never a bare sleep."""
    deadline = time.monotonic() + timeout
    last: str | None = None
    while time.monotonic() < deadline:
        if fx.proc.poll() is not None:
            raise AssertionError(
                f"proxy exited with {fx.proc.returncode} before serving anything; "
                f"argv={fx.argv}\nstderr:\n{fx.stderr_text()}")
        try:
            resp, body = http_get(fx.port, "/", timeout=2.0)
            if resp.status == 200 and INDEX_MARKER.encode() in body:
                return
            last = f"status={resp.status} body={body[:120]!r}"
        except OSError as exc:  # not up yet
            last = repr(exc)
        time.sleep(0.05)
    raise AssertionError(
        f"proxy never served / within {timeout}s (last attempt: {last}); "
        f"argv={fx.argv}\nstderr:\n{fx.stderr_text()}")


def stop_process(proc: subprocess.Popen) -> None:
    if proc.poll() is not None:
        return
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)


@contextlib.contextmanager
def running_proxy(*, token: str | None = None, basic: tuple[str, str] | None = None,
                  host: str = "127.0.0.1"):
    """A started-and-ready proxy in front of a throwaway upstream.

    Always tears both down: no subprocess and no listener survives the ``with``
    block, even when an assertion inside it fails.
    """
    with tempfile.TemporaryDirectory(prefix="canopy-proxy-test-") as tmp:
        dist = make_dist(Path(tmp))
        stub = StubUpstream()
        thread = threading.Thread(target=stub.serve_forever, daemon=True)
        thread.start()
        port = free_port()
        argv = proxy_argv(dist=dist, api=stub.base_url, port=port, host=host,
                          token=token, basic=basic)
        log_path = Path(tmp) / "proxy.log"
        log_handle = open(log_path, "wb")
        proc = subprocess.Popen(argv, stdout=subprocess.DEVNULL, stderr=log_handle,
                                cwd=str(REPO_ROOT))

        def stderr_text() -> str:
            log_handle.flush()
            return log_path.read_text(errors="replace")

        fx = SimpleNamespace(proc=proc, port=port, dist=dist, stub=stub,
                             argv=argv, log_path=log_path, stderr_text=stderr_text)
        try:
            wait_until_serving(fx)
            yield fx
        finally:
            stop_process(proc)
            log_handle.close()
            stub.shutdown()
            stub.server_close()
            thread.join(timeout=5)


# --------------------------------------------------------------------------- #
# Static assets + SPA fallback + /api routing (no token configured)
# --------------------------------------------------------------------------- #
class ProxyStaticAndApiTests(unittest.TestCase):
    """The served-from-disk half and the unauthenticated/API half."""

    def setUp(self) -> None:
        self.fx = self.enterContext(running_proxy())

    def test_static_asset_served_with_its_bytes_and_content_type(self) -> None:
        resp, body = http_get(self.fx.port, "/" + ASSET_NAME)
        ctype = (resp.getheader("Content-Type") or "").lower()
        self.assertEqual(200, resp.status)
        self.assertEqual(ASSET_BODY, body, "asset bytes must survive the proxy unchanged")
        self.assertIn("javascript", ctype, f"unexpected content type {ctype!r}")
        self.assertEqual(str(len(ASSET_BODY)), resp.getheader("Content-Length"))
        self.assertIn("CANOPY_ASSET_MARKER_7c1d", body.decode())

    def test_spa_deep_link_falls_back_to_index_shell(self) -> None:
        for route in ("/tree/abc", "/trees", "/some/deep/route?tab=graph"):
            with self.subTest(route=route):
                resp, body = http_get(self.fx.port, route)
                text = body.decode()
                self.assertEqual(200, resp.status)
                self.assertIn("text/html", (resp.getheader("Content-Type") or "").lower())
                self.assertIn(INDEX_MARKER, text)

    def test_unauthenticated_api_is_rejected_and_never_the_spa_shell(self) -> None:
        """The trap of DF-HERMES-CANOPY-7: an SPA fallback answering /api with HTML."""
        for path in ("/api/v1/trees", "/api/v1/health", "/health"):
            with self.subTest(path=path):
                resp, body = http_get(self.fx.port, path)
                text = body.decode("utf-8", "replace")
                ctype = (resp.getheader("Content-Type") or "").lower()
                self.assertEqual(401, resp.status, f"{path} must not be answered locally: {text!r}")
                self.assertIn("application/json", ctype, ctype)
                self.assertNotIn(INDEX_MARKER, text, "SPA shell leaked into an /api response")
                self.assertNotIn("<!doctype html", text.lower())
                self.assertEqual("TOKEN_MISSING", json.loads(text)["error"]["code"])

    def test_upstream_error_status_and_body_are_preserved(self) -> None:
        resp, body = http_get(self.fx.port, "/api/v1/broken",
                              headers={"Authorization": f"Bearer {CLIENT_TOKEN}"})
        text = body.decode()
        self.assertEqual(502, resp.status)
        self.assertIn("application/json", (resp.getheader("Content-Type") or "").lower())
        self.assertNotIn(INDEX_MARKER, text)
        self.assertNotIn("<!doctype html", text.lower())
        payload = json.loads(text)
        self.assertEqual("UPSTREAM_EXPLODED", payload["error"]["code"])
        self.assertEqual("canopyd failed to reach postgres", payload["error"]["message"])

    def test_sse_events_are_delivered_incrementally_not_buffered(self) -> None:
        conn = http.client.HTTPConnection("127.0.0.1", self.fx.port, timeout=HTTP_TIMEOUT_SECONDS)
        self.addCleanup(conn.close)
        conn.request("GET", "/api/v1/stream", headers={
            "Authorization": f"Bearer {CLIENT_TOKEN}",
            "Accept": "text/event-stream",
        })
        resp = conn.getresponse()
        self.assertEqual(200, resp.status)
        self.assertIn("text/event-stream", (resp.getheader("Content-Type") or "").lower())

        # One buffer shared across both reads: a buffering relay hands the whole
        # stream over in a single chunk, so the second marker is already present
        # when the first is seen — and the timing assertions below reject that.
        buf = bytearray()

        def read_until(marker: bytes, deadline: float) -> float:
            while True:
                if marker in buf:
                    return time.monotonic()
                if time.monotonic() >= deadline:
                    break
                chunk = resp.read1(4096)
                if not chunk:
                    break
                buf.extend(chunk)
            self.fail(f"never received {marker!r} within {HTTP_TIMEOUT_SECONDS}s; "
                      f"got {bytes(buf[:200])!r}")

        deadline = time.monotonic() + HTTP_TIMEOUT_SECONDS
        first_at = read_until(b"data: first", deadline)
        second_at = read_until(b"data: second", deadline)

        timeline = self.fx.stub.stream
        for key in ("started_at", "second_event_flush_at"):
            self.assertIn(key, timeline, "the stub's SSE endpoint never ran")
        self.assertLess(
            first_at - timeline["started_at"], SSE_FIRST_EVENT_BUDGET,
            "event 1 arrived %.3fs after the upstream wrote it — the relay is buffering"
            % (first_at - timeline["started_at"]))
        self.assertLess(
            first_at, timeline["second_event_flush_at"],
            "event 1 reached the client only after the upstream had already written "
            "event 2 — the relay buffered the stream")
        self.assertGreaterEqual(
            second_at - first_at, SSE_GAP_SECONDS * 0.5,
            "both SSE events arrived together — they were coalesced by buffering")


# --------------------------------------------------------------------------- #
# Bearer injection / pass-through
# --------------------------------------------------------------------------- #
class ProxyTokenInjectionTests(unittest.TestCase):
    """`--token` injects only when the client sent no Authorization of its own."""

    def setUp(self) -> None:
        self.fx = self.enterContext(running_proxy(token=INJECTED_TOKEN))

    def test_authenticated_api_json_with_injected_bearer(self) -> None:
        resp, body = http_get(self.fx.port, "/api/v1/trees")
        text = body.decode()
        self.assertEqual(200, resp.status, text)
        self.assertIn("application/json", (resp.getheader("Content-Type") or "").lower())
        self.assertNotIn(INDEX_MARKER, text)
        payload = json.loads(text)
        self.assertEqual([{"id": "tree-1", "title": "demo"}], payload["trees"])

        seen = self.fx.stub.requests_for("/api/v1/trees")
        self.assertEqual(1, len(seen), seen)
        self.assertEqual(f"Bearer {INJECTED_TOKEN}", seen[-1]["authorization"],
                         "the proxy did not inject --token upstream")
        self.assertEqual(f"Bearer {INJECTED_TOKEN}", payload["received_authorization"])

    def test_client_authorization_wins_over_injection(self) -> None:
        resp, body = http_get(self.fx.port, "/api/v1/trees",
                              headers={"Authorization": f"Bearer {CLIENT_TOKEN}"})
        text = body.decode()
        self.assertEqual(200, resp.status, text)
        self.assertEqual(f"Bearer {CLIENT_TOKEN}", json.loads(text)["received_authorization"])

        seen = self.fx.stub.requests_for("/api/v1/trees")
        self.assertEqual(1, len(seen), seen)
        self.assertEqual(f"Bearer {CLIENT_TOKEN}", seen[-1]["authorization"],
                         "the client's Authorization must pass through untouched")
        self.assertNotIn(INJECTED_TOKEN, seen[-1]["authorization"])


# --------------------------------------------------------------------------- #
# Startup refusals (as subprocesses)
# --------------------------------------------------------------------------- #
class ProxyRefusalTests(unittest.TestCase):
    """The guard rails: a busy port, and token injection on a reachable interface."""

    def test_refuses_when_port_is_already_bound(self) -> None:
        listener = socket.socket()
        self.addCleanup(listener.close)
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind(("127.0.0.1", 0))
        listener.listen(1)
        busy_port = int(listener.getsockname()[1])
        with tempfile.TemporaryDirectory(prefix="canopy-proxy-test-") as tmp:
            dist = make_dist(Path(tmp))
            proc = subprocess.run(
                proxy_argv(dist=dist, api="http://127.0.0.1:1", port=busy_port),
                capture_output=True, text=True, timeout=60, cwd=str(REPO_ROOT))
            dist_text = str(dist)
        self.assertNotEqual(0, proc.returncode, f"a busy port must refuse to start: {proc.stderr!r}")
        self.assertIn("REFUSING TO START", proc.stderr)
        self.assertIn("already bound", proc.stderr)
        self.assertIn(str(busy_port), proc.stderr)
        self.assertTrue(dist_text)  # the refusal came after the dist check, not before

    def test_refuses_token_injection_on_non_loopback_without_basic_auth(self) -> None:
        with tempfile.TemporaryDirectory(prefix="canopy-proxy-test-") as tmp:
            dist = make_dist(Path(tmp))
            proc = subprocess.run(
                proxy_argv(dist=dist, api="http://127.0.0.1:1", port=free_port(),
                           host="0.0.0.0", token=INJECTED_TOKEN),
                capture_output=True, text=True, timeout=60, cwd=str(REPO_ROOT))
            dist_text = str(dist)
        self.assertNotEqual(0, proc.returncode,
                            f"token injection on a reachable interface must refuse: {proc.stderr!r}")
        self.assertIn("REFUSING TO START", proc.stderr)
        self.assertIn("non-loopback", proc.stderr)
        self.assertIn("0.0.0.0", proc.stderr)
        self.assertTrue(dist_text)

        # Positive control: the SAME token on loopback is accepted, so the guard
        # cannot pass by refusing every --token.
        with running_proxy(token=INJECTED_TOKEN) as fx:
            resp, _ = http_get(fx.port, "/api/v1/trees")
            self.assertEqual(200, resp.status)


if __name__ == "__main__":
    unittest.main(verbosity=2)
