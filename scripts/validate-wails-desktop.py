#!/usr/bin/env python3
"""Validate the repository-owned Wails desktop packaging contract offline.

This is a source-level check in the style of
validate-release-manifest-publication.py: GitHub Actions is not contacted,
Wails is not downloaded, and no native build is attempted. It catches drift
between the desktop entry module, the wails.json project config, the
Makefile build-wails surface, the updater wiring, and the docs boundary.

Traceability: ch:trace row=FTR-06 spec=specs/SPEC-FTR-06-webui-native-packaging-distribution.md#phase-5
wave=none:serial-tick-hermes-canopy-2026-10-26-16-13-57
test=python3 scripts/validate-wails-desktop.py doc=docs/DESKTOP-WAILS.md
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import NoReturn


def fail(message: str) -> NoReturn:
    print(f"FAIL: {message}", file=sys.stderr)
    raise SystemExit(1)


def require(text: str, fragment: str, description: str) -> None:
    if fragment not in text:
        fail(f"missing {description}: {fragment!r}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--root",
        type=Path,
        default=Path(__file__).resolve().parents[1],
        help="repository root (default: inferred from this script)",
    )
    args = parser.parse_args()
    root = args.root.resolve()

    main_go = root / "cmd/canopy-desktop/main.go"
    desktop_mod = root / "cmd/canopy-desktop/go.mod"
    wails_json = root / "wails.json"
    makefile = root / "Makefile"
    feed_go = root / "internal/wailsbridge/feed.go"
    backend_go = root / "internal/wailsbridge/backend.go"
    updater_go = root / "internal/updater/updater.go"
    docs = root / "docs/DESKTOP-WAILS.md"
    release_yml = root / ".github/workflows/release.yml"
    for path in (
        main_go, desktop_mod, wails_json, makefile, feed_go, backend_go,
        updater_go, docs,
    ):
        if not path.is_file():
            fail(f"required file does not exist: {path}")

    main = main_go.read_text(encoding="utf-8")
    desktop_module = desktop_mod.read_text(encoding="utf-8")
    makefile_text = makefile.read_text(encoding="utf-8")
    feed = feed_go.read_text(encoding="utf-8")
    backend = backend_go.read_text(encoding="utf-8")
    updater = updater_go.read_text(encoding="utf-8")
    workflow = release_yml.read_text(encoding="utf-8") if release_yml.is_file() else ""
    docs_text = docs.read_text(encoding="utf-8")

    # --- nested module boundary: the canopy module must stay Wails-free ---
    root_go_mod = (root / "go.mod").read_text(encoding="utf-8")
    if "wailsapp/wails" in root_go_mod:
        fail("root go.mod must NOT depend on Wails (nested-module boundary)")
    require(
        desktop_module,
        "module github.com/coding-hermes/hermes-canopy/cmd/canopy-desktop",
        "nested module declaration in cmd/canopy-desktop/go.mod",
    )
    require(desktop_module, "github.com/wailsapp/wails/v3", "Wails dependency")
    require(
        desktop_module,
        "replace github.com/coding-hermes/hermes-canopy => ../..",
        "replace directive to the canopy module",
    )
    wails_version = re.search(
        r"github\.com/wailsapp/wails/v3 (v\S+)", desktop_module
    )
    if not wails_version:
        fail("cmd/canopy-desktop/go.mod has no pinned wailsapp/wails/v3 version")

    # --- entry point contract ---
    require(main, "package main", "package declaration")
    require(
        main,
        "github.com/coding-hermes/hermes-canopy/internal/wailsbridge",
        "desktop shell import of the repository-owned bridge package",
    )
    require(main, "app.Updater.Init(", "Wails updater Init call")
    require(
        main,
        "updater.ResolvePublicKey",
        "shared release-key resolution",
    )
    require(
        main,
        "-tags production",
        "desktop build tag documentation",
    )
    require(
        main,
        "CGO_ENABLED=0 go build -tags server,production",
        "host-independent server-mode build documentation",
    )

    # --- wails.json: parseable and consistent with the entry module ---
    try:
        config = json.loads(wails_json.read_text(encoding="utf-8"))
    except json.JSONDecodeError as err:
        fail(f"wails.json is not valid JSON: {err}")
    if config.get("name") != "canopy-desktop":
        fail(f"wails.json name = {config.get('name')!r}, want 'canopy-desktop'")
    if config.get("outputfilename") != "canopy-desktop":
        fail("wails.json outputfilename must be canopy-desktop")
    info = config.get("info") or {}
    if not info.get("productIdentifier"):
        fail("wails.json info.productIdentifier is required for packaging hooks")
    if not info.get("version"):
        fail("wails.json info.version is required for packaging hooks")

    # --- updater wiring: one shared latest.json contract ---
    require(
        feed,
        "updater.DefaultManifestURL",
        "feed default-URL reuse of the updater constant",
    )
    require(
        feed,
        "manifest_signature",
        "manifest signature field on the feed wire shape",
    )
    require(
        feed,
        "updater.VerifyManifestSignature",
        "feed Ed25519 manifest verification",
    )
    require(
        feed,
        'releaseModeWails = "wails"',
        "wails-mode artifact preference",
    )
    require(
        updater,
        "func DefaultManifestURL",
        "exported DefaultManifestURL in internal/updater",
    )
    require(
        updater,
        "func CompareVersions",
        "exported CompareVersions in internal/updater",
    )
    require(
        backend,
        "func backendServeArgs",
        "desktop child argument builder",
    )
    backend_args_body = backend.split("func backendServeArgs", 1)[1].split("}", 1)[0]
    if "-update-interval" in backend_args_body:
        fail("desktop backend child must never carry update-check flags")

    # --- Makefile: explicit, prerequisite-checking build-wails target ---
    require(makefile_text, "build-wails:", "Makefile build-wails target")
    require(
        makefile_text,
        "validate-wails-desktop:",
        "Makefile validate-wails-desktop target",
    )
    build_wails = makefile_text.split("build-wails:", 1)[1].split("\n\n", 1)[0]
    require(
        build_wails,
        "validate-wails-desktop",
        "build-wails prerequisite check",
    )
    # Error redirection is only allowed INTO the check recipes that PRINT a
    # failure message and exit non-zero; a blanket `2>/dev/null` hiding a
    # real failure would be a silent fallback.
    if ">/dev/null 2>&1 ||" not in build_wails and "2>/dev/null" in build_wails:
        fail("build-wails must not swallow prerequisite errors")

    # --- release workflow stays embed-only for this phase ---
    if "build-wails" in workflow:
        fail(
            "release.yml mentions build-wails: the Wails CI matrix must only be "
            "added when it can be proven (WebView SDKs, signing); see "
            "docs/DESKTOP-WAILS.md"
        )

    # --- honest boundary documentation ---
    require(
        docs_text,
        "signed `.dmg`",
        "docs statement that signed macOS artifacts are NOT produced here",
    )
    require(
        docs_text,
        "GTK4",
        "docs Linux SDK prerequisite (GTK4/WebKitGTK 6.0)",
    )
    require(
        docs_text,
        "not verified on this host",
        "docs host-limitation statement",
    )

    print("OK: wails desktop packaging contract consistent")
    print(f"  wails version: {wails_version.group(1)}")
    print(f"  entry point: {main_go.relative_to(root)}")
    print(f"  updater feed: {feed_go.relative_to(root)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
