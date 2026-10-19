#!/usr/bin/env python3
"""Validate the release manifest's stable publication contract offline.

This is intentionally a source-level check: GitHub Actions is not contacted and
no release or tag is created. It catches drift between the updater default,
the workflow's gh-pages path, the override plumbing, and the release asset.

Traceability: ch:trace row=DF-HERMES-CANOPY-72 spec=specs/SPEC-FTR-06.md#48
wave=none:serial test=internal/updater/updater_test.go::TestDefaultManifestURL
 doc=README.md prompt=/tmp/hermes-canopy-2026-10-19-22-09-14-DF-HERMES-CANOPY-72-rework.txt
 evidence=scripts/validate-release-manifest-publication.py
 witness=none:worker-local-unpublished verdict=verdict:a44b1cc7:prior-incomplete
 commit=see-final-worker-report memory=/project/hermes-canopy/foreman-tick-2026-10-19
"""

from __future__ import annotations

import argparse
import re
import shutil
import sys
import tempfile
from pathlib import Path
from typing import NoReturn


DEFAULT_URL = "https://releases.canopy.app/%s/latest.json"


def fail(message: str) -> "NoReturn":
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

    workflow_path = root / ".github/workflows/release.yml"
    updater_path = root / "internal/updater/updater.go"
    main_path = root / "cmd/canopyd/main.go"
    readme_path = root / "README.md"
    makefile_path = root / "Makefile"
    for path in (workflow_path, updater_path, main_path, readme_path, makefile_path):
        if not path.is_file():
            fail(f"required file does not exist: {path}")

    workflow = workflow_path.read_text(encoding="utf-8")
    updater = updater_path.read_text(encoding="utf-8")
    canopyd = main_path.read_text(encoding="utf-8")
    readme = readme_path.read_text(encoding="utf-8")
    makefile = makefile_path.read_text(encoding="utf-8")

    default_match = re.search(
        r'defaultManifestURLBase = "(https://[^" ]+)"',
        updater,
    )
    if not default_match:
        fail("could not find the updater's default manifest URL base")
    if 'return fmt.Sprintf("%s/%s/latest.json", defaultManifestURLBase, channel)' not in updater:
        fail("updater default URL helper does not preserve channel/latest.json path")
    default_url = f"{default_match.group(1)}/%s/latest.json"
    if default_url != DEFAULT_URL:
        fail(f"updater default URL is {default_url!r}, want {DEFAULT_URL!r}")

    require(updater, "url := u.ManifestURL", "updater override assignment")
    require(updater, 'if url == "" {', "updater empty-override branch")
    require(canopyd, 'manifestURL := strings.TrimSpace(*updateManifestURL)', "flag override plumbing")
    require(canopyd, 'os.Getenv("CANOPY_UPDATE_MANIFEST_URL")', "environment override plumbing")
    require(canopyd, "ManifestURL:    manifestURL", "resolved override passed to updater")

    require(
        makefile,
        "validate-release-manifest-publication:\n\tpython3 scripts/validate-release-manifest-publication.py",
        "Makefile release publication validator target",
    )
    require(workflow, "run: make validate-release-manifest-publication", "workflow validator invocation")
    validate_step = workflow.index("- name: Validate release manifest publication contract")
    publish_step = workflow.index("- name: Publish signed manifest to GitHub Pages")
    if validate_step > publish_step:
        fail("release manifest validator must run before stable publication")

    require(workflow, "channel=stable", "stable publication channel")
    require(
        workflow,
        'cp -- dist/latest.json "$pages_dir/$channel/latest.json"',
        "byte-preserving publication copy",
    )
    require(
        workflow,
        'cmp --silent dist/latest.json "$pages_dir/$channel/latest.json"',
        "byte-for-byte publication check",
    )
    require(workflow, 'git -C "$pages_dir" push origin HEAD:gh-pages', "gh-pages publication")
    require(workflow, "permissions:\n  contents: write", "workflow contents permission")
    require(workflow, "releases.canopy.app", "GitHub Pages custom domain")

    upload = workflow.index("- name: Upload release artifacts")
    publish = workflow.index("- name: Publish signed manifest to GitHub Pages")
    if publish > upload:
        fail("stable publication must happen before the versioned release upload")
    release_asset = workflow.find("            dist/latest.json", upload)
    if release_asset < 0:
        fail("versioned GitHub Release asset dist/latest.json was removed")

    require(readme, "https://releases.canopy.app/<channel>/latest.json", "documented stable URL")
    require(readme, "`v0.1.0` release remains historical", "historical-release note")
    require(readme, "ch:trace row=DF-HERMES-CANOPY-72", "release publication traceability marker")

    # Exercise the exact byte-copy invariant with bytes that include whitespace
    # and a NUL. This stays network-free and proves the validator is checking
    # bytes rather than parsed/re-encoded JSON.
    with tempfile.TemporaryDirectory(prefix="canopy-release-contract-") as temporary:
        temporary_root = Path(temporary)
        source = temporary_root / "dist/latest.json"
        published = temporary_root / "stable/latest.json"
        source.parent.mkdir()
        published.parent.mkdir()
        manifest_bytes = b'{\n  "signed": true,\n  "padding": "keep-me"\n}\x00\n'
        source.write_bytes(manifest_bytes)
        shutil.copyfile(source, published)
        if source.read_bytes() != published.read_bytes():
            fail("publication copy changed generated manifest bytes")

    print(
        "PASS: updater default, flag/env overrides, gh-pages stable path, "
        "versioned asset, and byte-copy invariant agree"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
