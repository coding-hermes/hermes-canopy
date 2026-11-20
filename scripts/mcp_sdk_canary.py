#!/usr/bin/env python3
"""Reference-client canary for the stateless Canopy MCP endpoint.

Supported MCP SDK versions: >=1.30.0, <1.31.0 (stable releases only).
"""

import asyncio
import base64
import hashlib
import hmac
from importlib import metadata
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
from urllib.parse import urlsplit, urlunsplit

URL = os.environ.get("CANOPY_MCP_URL", "http://127.0.0.1:8106/api/v1/mcp")
MIN_MCP_VERSION = (1, 30, 0)
MAX_MCP_VERSION = (1, 31, 0)  # exclusive


def check_sdk_version() -> bool:
    try:
        installed = metadata.version("mcp")
    except metadata.PackageNotFoundError:
        installed = "not installed"
    match = re.fullmatch(r"(\d+)\.(\d+)\.(\d+)", installed)
    if match and MIN_MCP_VERSION <= tuple(map(int, match.groups())) < MAX_MCP_VERSION:
        return True
    minimum = ".".join(map(str, MIN_MCP_VERSION))
    maximum = ".".join(map(str, MAX_MCP_VERSION))
    print(f"Unsupported MCP SDK version {installed}; supported range: >= {minimum}, < {maximum} (stable releases only)", file=sys.stderr)
    return False


def dev_token() -> str:
    secret = os.environ.get("JWT_SECRET", "dev-secret-change-me").encode()
    sub = os.environ.get("CANOPY_JWT_SUB", "00000000-0000-0000-0000-000000000001")
    now = int(time.time())

    def encoded(value: object) -> bytes:
        return base64.urlsafe_b64encode(
            json.dumps(value, separators=(",", ":")).encode()
        ).rstrip(b"=")

    header = encoded({"alg": "HS256", "typ": "JWT"})
    payload = encoded({"sub": sub, "iat": now, "exp": now + 3600})
    signature = hmac.new(secret, header + b"." + payload, hashlib.sha256).digest()
    signature = base64.urlsafe_b64encode(signature).rstrip(b"=")
    return (header + b"." + payload + b"." + signature).decode()


def wait_for_server() -> None:
    health = urlsplit(URL)._replace(path="/health", query="", fragment="")
    health_url = urlunsplit(health)
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(health_url, timeout=1) as response:
                if response.status == 200:
                    return
        except (OSError, urllib.error.URLError):
            time.sleep(0.25)
    raise RuntimeError(f"canopyd did not become healthy at {health_url}")


async def check() -> None:
    from mcp import ClientSession
    from mcp.client.streamable_http import create_mcp_http_client, streamable_http_client

    token = os.environ.get("CANOPY_TOKEN") or dev_token()
    http_client = create_mcp_http_client(headers={"Authorization": f"Bearer {token}"})
    async with http_client:
        async with streamable_http_client(URL, http_client=http_client) as (read_stream, write_stream, _get_session_id):
            async with ClientSession(read_stream, write_stream) as session:
                await session.initialize()
                tools = await session.list_tools()
                names = [tool.name for tool in tools.tools]
                expected = ["list_trees", "get_tree", "create_node", "list_topics", "get_graph_stats", "list_approvals", "list_cards"]
                assert names == expected, f"tools/list names = {names!r}, want {expected!r}"
                result = await session.call_tool("list_trees", arguments={})
                assert result.isError is False, f"list_trees returned isError={result.isError!r}"
                assert result.content, "list_trees returned no content"
                item = result.content[0]
                assert item.type == "text", f"content[0].type = {item.type!r}"
                json.loads(item.text)
                print("MCP_SDK_CANARY_OK")
                print(json.dumps({"tools": names, "text": item.text}, separators=(",", ":")))


def main() -> None:
    process = None
    binary = os.environ.get("CANOPY_SDK_CANARY_BINARY")
    try:
        if binary:
            process = subprocess.Popen([binary], env=os.environ.copy())
            wait_for_server()
        asyncio.run(check())
    finally:
        if process is not None:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def error_detail(exc: BaseException) -> str:
    if isinstance(exc, BaseExceptionGroup):
        return "; ".join(error_detail(child) for child in exc.exceptions)
    return str(exc) or type(exc).__name__


if __name__ == "__main__":
    if not check_sdk_version():
        sys.exit(2)
    try:
        main()
    except Exception as exc:
        print(f"MCP SDK canary failed for {URL}: {error_detail(exc)}", file=sys.stderr)
        sys.exit(1)
