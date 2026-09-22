#!/usr/bin/env python3
"""Reference-client canary for the stateless Canopy MCP endpoint."""

import asyncio
import base64
import hashlib
import hmac
import json
import os
import subprocess
import time
import urllib.error
import urllib.request
from urllib.parse import urlsplit, urlunsplit

from mcp import ClientSession
from mcp.client.streamable_http import create_mcp_http_client, streamable_http_client

URL = os.environ.get("CANOPY_MCP_URL", "http://127.0.0.1:8106/api/v1/mcp")


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
    token = os.environ.get("CANOPY_TOKEN") or dev_token()
    http_client = create_mcp_http_client(headers={"Authorization": f"Bearer {token}"})
    async with http_client:
        async with streamable_http_client(URL, http_client=http_client) as (read_stream, write_stream):
            async with ClientSession(read_stream, write_stream) as session:
                await session.initialize()
                tools = await session.list_tools()
                names = [tool.name for tool in tools.tools]
                expected = ["list_trees", "get_tree", "create_node", "list_topics", "get_graph_stats", "list_approvals", "list_cards"]
                assert names == expected, f"tools/list names = {names!r}, want {expected!r}"
                result = await session.call_tool("list_trees", arguments={})
                assert result.is_error is False, f"list_trees returned isError={result.is_error!r}"
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


if __name__ == "__main__":
    main()
