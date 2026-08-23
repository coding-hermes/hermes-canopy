#!/usr/bin/env python3
"""
Sweep throwaway E2E battery trees from the live Canopy database (BUG-044).

The Playwright-vitest integration battery creates uniquely-named scratch
trees through the Vite dev proxy and historically left them behind in the
real app's PostgreSQL. This script walks GET /api/v1/trees with keyset
cursor pagination, matches battery junk by title pattern + ownership +
absence of session metadata, and (with --apply) DELETEs each match.

Defaults to DRY-RUN: prints the match count and the first 10 titles.

Safety contract:
  - only trees owned by the dev JWT user are matched;
  - trees carrying session metadata (session_id) are NEVER touched —
    that protects every session-imported tree;
  - the shared demo tree ("UI-02 Rail Demo") is protected by title.

Usage:
  python3 scripts/sweep-e2e-test-trees.py            # dry-run
  python3 scripts/sweep-e2e-test-trees.py --apply    # actually delete

Environment:
  CANOPY_SWEEP_BASE_URL  Vite dev proxy URL (default http://localhost:5173).
                         The proxy injects the dev JWT; do NOT authenticate
                         manually.
Stdlib only (urllib.request + json + argparse).
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

BASE_URL = os.environ.get("CANOPY_SWEEP_BASE_URL", "http://localhost:5173").rstrip("/")

DEV_USER_ID = "00000000-0000-0000-0000-000000000001"

# Battery-created scratch trees. Every family ends in "<13-digit ms>_<6
# lowercase alphanumerics>" (Date.now() + Math.random().toString(36)).
JUNK_TITLE_RE = re.compile(
    r"^(T[0-9]+ (BUG|WIRE|GAP)|GAP0[0-9]+ E2E|T265 Sync|BUG-040)"
    r" [0-9]{13}_[a-z0-9]{6}$"
)

# Real content that must never be swept, even if it somehow matched.
PROTECTED_TITLES = {"UI-02 Rail Demo"}


def http_json(path: str, timeout: int = 30) -> dict:
    """GET a JSON document through the dev proxy."""
    url = BASE_URL + path
    req = urllib.request.Request(url, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        raise SystemExit(f"GET {url} -> HTTP {exc.code}: {exc.read().decode('utf-8', 'replace')}") from exc
    except urllib.error.URLError as exc:
        raise SystemExit(f"GET {url} failed: {exc.reason}") from exc


def delete_tree(tree_id: str, timeout: int = 30) -> int:
    """DELETE one tree; returns the HTTP status code."""
    url = f"{BASE_URL}/api/v1/trees/{tree_id}"
    req = urllib.request.Request(url, method="DELETE")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status
    except urllib.error.HTTPError as exc:
        return exc.code


def is_junk(tree: dict) -> bool:
    """Battery-junk predicate: title pattern AND dev-owned AND no session."""
    if tree.get("session_id"):
        return False  # never sweep session-imported trees
    if tree.get("owner_id") != DEV_USER_ID:
        return False
    if tree.get("title") in PROTECTED_TITLES:
        return False
    return bool(JUNK_TITLE_RE.match(tree.get("title", "")))


def walk_all_trees() -> list[dict]:
    """Cursor-paginated walk of GET /api/v1/trees?limit=100."""
    trees: list[dict] = []
    cursor: str | None = None
    while True:
        query = urllib.parse.urlencode({"limit": 100, **({"cursor": cursor} if cursor else {})})
        page = http_json(f"/api/v1/trees?{query}")

        # Tolerate both wire shapes: flat next_cursor/nextCursor or nested
        # under pagination{} (the handler nests; the service type is flat).
        pagination = page.get("pagination", {})
        raw_cursor = pagination.get("next_cursor", pagination.get("nextCursor"))
        if raw_cursor is None:
            raw_cursor = page.get("next_cursor", page.get("nextCursor"))

        batch = page.get("trees", [])
        trees.extend(batch)
        print(f"fetched {len(batch):3d} trees (total so far: {len(trees)}, total in db: {pagination.get('total', '?')})", file=sys.stderr)

        if not raw_cursor or not page.get("has_more", True):
            break
        cursor = raw_cursor
    return trees


def main() -> int:
    parser = argparse.ArgumentParser(description="Sweep throwaway E2E battery trees (BUG-044).")
    parser.add_argument("--apply", action="store_true", help="delete matches (default is dry-run)")
    args = parser.parse_args()

    all_trees = walk_all_trees()
    junk = [t for t in all_trees if is_junk(t)]

    mode = "APPLY" if args.apply else "DRY-RUN"
    print(f"\n[{mode}] matched {len(junk)} junk tree(s) out of {len(all_trees)} visible tree(s)")
    for tree in junk[:10]:
        print(f"  - {tree['title']}  (id={tree['id']}, created={tree.get('created_at', '?')})")
    if len(junk) > 10:
        print(f"  ... and {len(junk) - 10} more")

    if not args.apply:
        print("dry-run only — re-run with --apply to delete these trees")
        return 0

    deleted = 0
    for i, tree in enumerate(junk, start=1):
        status = delete_tree(tree["id"])
        if status == 204:
            deleted += 1
        elif status == 404:
            pass  # already gone — still counts as swept
        else:
            print(f"unexpected status deleting {tree['id']}: HTTP {status}", file=sys.stderr)
        if i % 25 == 0 or i == len(junk):
            print(f"progress: {i}/{len(junk)} processed, {deleted} deleted", file=sys.stderr)

    print(f"swept {deleted} trees")
    return 0


if __name__ == "__main__":
    sys.exit(main())
