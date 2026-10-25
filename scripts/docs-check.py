#!/usr/bin/env python3
"""Fail-closed README-vs-code checks for the repository's shipped surfaces.

The extraction scope is deliberately narrow: shell commands are collected only
from fenced ``bash``, ``sh``, or ``shell`` blocks and only when they invoke
``canopyd``.  Prose examples, API paths, arbitrary flags in prose, and commands
for other tools are not inferred.  The checker validates the command prefix and
flags on those mechanically extractable invocations against the built binary's
actual ``--help`` output.  It also validates the explicitly labelled Makefile
Targets and Environment Variables tables.
"""

from __future__ import annotations

import argparse
import os
import re
import shlex
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_README = ROOT / "README.md"
DEFAULT_BINARY = ROOT / "bin" / "canopyd"
DEFAULT_DOCS_DIR = ROOT / "docs"


class CheckFailure(RuntimeError):
    pass


def fail(message: str) -> None:
    raise CheckFailure(message)


def read_section(lines: list[str], heading: str) -> list[str]:
    start = next((i for i, line in enumerate(lines) if line.strip() == heading), None)
    if start is None:
        fail(f"README is missing required section {heading!r}")
    end = len(lines)
    for i in range(start + 1, len(lines)):
        if re.match(r"^#{1,6} ", lines[i]):
            end = i
            break
    return lines[start + 1 : end]


def table_values(lines: list[str], pattern: re.Pattern[str]) -> list[str]:
    values: list[str] = []
    for line in lines:
        match = pattern.match(line)
        if match:
            values.append(match.group(1))
    return values


def make_targets(makefile: str) -> set[str]:
    # A target is a non-indented make rule.  This intentionally ignores
    # variable assignments and recipe lines.
    return {
        match.group(1)
        for match in re.finditer(r"(?m)^([A-Za-z0-9_.-]+):(?:[^=]|$)", makefile)
    }


def shell_commands(readme: str) -> list[tuple[int, list[str]]]:
    commands: list[tuple[int, list[str]]] = []
    in_shell_fence = False
    fence_re = re.compile(r"^\s*(?:>\s*)?```\s*([A-Za-z0-9_-]*)\s*$")
    invoke_re = re.compile(
        r"(?:^|[;&|])\s*(?:(?:[A-Z][A-Z0-9_]*=(?:'[^']*'|\"[^\"]*\"|\S+))\s+)*"
        r"(?P<binary>(?:\./)?(?:bin/)?canopyd)(?P<rest>.*)$"
    )
    for lineno, line in enumerate(readme.splitlines(), 1):
        fence = fence_re.match(line)
        if fence:
            language = fence.group(1).lower()
            if not in_shell_fence:
                in_shell_fence = language in {"bash", "sh", "shell"}
            else:
                in_shell_fence = False
            continue
        if not in_shell_fence or line.lstrip().startswith("#"):
            continue
        match = invoke_re.search(line)
        if not match:
            continue
        raw = match.group("rest").strip()
        # Shell continuations and operators are not part of the CLI claim.
        raw = re.split(r"\s+(?:&&|\|\||[|;&])\s*", raw, maxsplit=1)[0]
        raw = raw.removesuffix("\\").rstrip()
        try:
            tokens = shlex.split(raw, comments=True, posix=True)
        except ValueError as exc:
            fail(f"README:{lineno}: cannot parse canopyd command: {exc}")
        if tokens:
            commands.append((lineno, tokens))
    return commands


def run_help(binary: Path, args: list[str]) -> tuple[str, str, int]:
    last: tuple[str, str, int] | None = None
    for suffix in ("--help", "-h"):
        proc = subprocess.run(
            [str(binary), *args, suffix],
            cwd=ROOT,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        result = (proc.stdout + proc.stderr, proc.stderr, proc.returncode)
        last = result
        if proc.returncode == 0:
            return result
        # A few existing subcommands use flag.ContinueOnError and print a
        # complete FlagSet help page before returning 1 for ErrHelp.  That is
        # still the built binary's authoritative help surface.
        if re.search(r"(?m)^Usage(?::| of )", result[0]):
            return result
    assert last is not None
    return last


def usage_tokens(output: str) -> list[str]:
    for line in output.splitlines():
        if line.startswith("Usage:"):
            try:
                return shlex.split(line[len("Usage:") :])
            except ValueError:
                return []
        match = re.match(r"Usage of (.+):$", line)
        if match:
            return ["canopyd", *match.group(1).split()]
    return []


def command_help(binary: Path, tokens: list[str], lineno: int) -> tuple[list[str], str]:
    # Try progressively longer prefixes.  The binary always appends a canonical
    # Usage line, so positional arguments (tree names, ids, paths) cannot become
    # command names merely because ``--help`` was appended to the probe.
    prefix_candidates: list[list[str]] = [[]]
    positional: list[str] = []
    for token in tokens:
        if token == "--" or token.startswith("-"):
            break
        positional.append(token)
    for size in range(1, min(4, len(positional)) + 1):
        prefix_candidates.append(positional[:size])

    best: tuple[list[str], str] | None = None
    for prefix in prefix_candidates:
        output, _, _ = run_help(binary, prefix)
        actual_usage = usage_tokens(output)
        if not actual_usage:
            continue
        expected = ["canopyd", *prefix]
        if actual_usage[: len(expected)] == expected:
            best = (prefix, output)
    if best is None:
        fail(
            f"README:{lineno}: documented canopyd command has no successful help "
            f"surface: {' '.join(['canopyd', *positional])}"
        )
    return best


def help_flags(help_output: str) -> set[str]:
    # Help text is the authority.  Matching the displayed option tokens also
    # covers the custom usage functions used by the CLI subcommands.
    return {
        match.group(1)
        for match in re.finditer(r"(?<![A-Za-z0-9])(-{1,2}[A-Za-z][A-Za-z0-9-]*)\b", help_output)
    }


def command_flags(tokens: list[str]) -> list[str]:
    result: list[str] = []
    for token in tokens:
        if token in {"--", "\\"}:
            continue
        if token.startswith("-") and token != "-":
            result.append(token.split("=", 1)[0])
    return result


def normalized_flags(flags: set[str]) -> set[str]:
    return {flag.lstrip("-") for flag in flags}


def check_docs_index(readme: str, docs_dir: Path) -> int:
    """Require one canonical, one-line entry for every top-level docs page."""
    if not docs_dir.is_dir():
        fail(f"documentation directory not found: {docs_dir}")

    index = docs_dir / "README.md"
    compatibility = docs_dir / "_index.md"
    if not index.is_file():
        fail(f"canonical documentation index not found: {index}")
    if not compatibility.is_file():
        fail(f"documentation compatibility index not found: {compatibility}")

    expected = sorted(
        path.name
        for path in docs_dir.glob("*.md")
        if path.name not in {"README.md", "_index.md"}
    )
    if not expected:
        fail(f"no top-level documentation pages found in {docs_dir}")

    rows: list[tuple[str, str, int]] = []
    row_re = re.compile(r"^\s*-\s+\[[^]]+\]\(([^)]+)\)\s+—\s+(.+?)\s*$")
    for lineno, line in enumerate(index.read_text(encoding="utf-8").splitlines(), 1):
        match = row_re.match(line)
        if match:
            rows.append((match.group(1), match.group(2), lineno))

    targets = [target for target, _, _ in rows]
    if len(rows) != len(expected) or sorted(targets) != expected:
        missing = sorted(set(expected) - set(targets))
        extra = sorted(set(targets) - set(expected))
        duplicates = sorted({target for target in targets if targets.count(target) > 1})
        details = []
        if missing:
            details.append(f"missing={missing}")
        if extra:
            details.append(f"extra={extra}")
        if duplicates:
            details.append(f"duplicates={duplicates}")
        fail(
            f"{index} must list each top-level docs page exactly once with a one-line purpose"
            + (f" ({'; '.join(details)})" if details else "")
        )

    for target, purpose, lineno in rows:
        if not purpose.strip():
            fail(f"{index}:{lineno}: documentation purpose is empty for {target!r}")
        target_path = docs_dir / target
        if target_path.parent != docs_dir or not target_path.is_file():
            fail(f"{index}:{lineno}: indexed documentation page is missing: {target}")

    readme_index_links = re.findall(r"\]\(docs/README\.md(?:#[^)]+)?\)", readme)
    if len(readme_index_links) != 1:
        fail(
            "README.md must link the canonical documentation index exactly once "
            f"(found {len(readme_index_links)})"
        )

    compatibility_text = compatibility.read_text(encoding="utf-8")
    compatibility_links = re.findall(r"\]\(README\.md\)", compatibility_text)
    if len(compatibility_links) != 1:
        fail(
            f"{compatibility} must contain exactly one link to the canonical README.md "
            f"(found {len(compatibility_links)})"
        )
    if row_re.search(compatibility_text):
        fail(f"{compatibility} must remain a short compatibility link, not a duplicate index")

    return len(expected)


def check_cli(readme: str, binary: Path) -> int:
    commands = shell_commands(readme)
    checked = 0
    for lineno, tokens in commands:
        prefix, output = command_help(binary, tokens, lineno)
        available = normalized_flags(help_flags(output))
        for flag in command_flags(tokens):
            if flag.lstrip("-") not in available:
                fail(
                    f"README:{lineno}: documented flag {flag!r} is absent from "
                    f"{' '.join(['canopyd', *prefix])} --help"
                )
        checked += 1

    # The explicit Flags table is a second mechanical surface even when a flag
    # is not used in a copy/paste shell command.  It belongs to server mode.
    flag_rows = table_values(
        read_section(readme.splitlines(), "## Configuration"),
        re.compile(r"^\|\s*`(-{1,2}[A-Za-z][A-Za-z0-9-]*)`\s*\|"),
    )
    if flag_rows:
        output, _, code = run_help(binary, [])
        if code != 0:
            fail("canopyd --help failed while checking the README Flags table")
        available = normalized_flags(help_flags(output))
        for flag in flag_rows:
            if flag.lstrip("-") not in available:
                fail(f"README Flags table documents {flag!r}, absent from canopyd --help")
    return checked


def production_go_sources() -> list[Path]:
    paths: list[Path] = []
    for path in ROOT.rglob("*.go"):
        relative = path.relative_to(ROOT)
        if any(part in {".git", ".vfs", ".coding-hermes", ".gitreins"} for part in relative.parts):
            continue
        if path.name.endswith("_test.go"):
            continue
        paths.append(path)
    return paths


def has_real_env_read(variable: str, sources: list[tuple[Path, str]]) -> bool:
    # Accept direct os.Getenv/os.LookupEnv reads and the config helper that
    # receives the variable name (strictEnvInt).  A comment-only mention is not
    # enough: at least one production source file must also contain a runtime
    # environment-read call.
    for _, text in sources:
        for line in text.splitlines():
            if variable not in line:
                continue
            if re.search(r"\bos\.(?:Getenv|LookupEnv)\s*\(", line):
                return True
            if re.search(r"\b(?:Getenv|LookupEnv|getenv|strictEnvInt)\s*\(", line):
                return True
    return False


def check_tables(readme: str) -> tuple[int, int]:
    lines = readme.splitlines()
    target_rows = table_values(
        read_section(lines, "### Makefile Targets"),
        re.compile(r"^\|\s*`([A-Za-z0-9_.-]+)`\s*\|"),
    )
    if not target_rows:
        fail("README Makefile Targets table is empty or not mechanically parseable")
    targets = make_targets((ROOT / "Makefile").read_text(encoding="utf-8"))
    for target in target_rows:
        if target not in targets:
            fail(f"README Makefile Targets table lists missing target {target!r}")

    env_rows = table_values(
        read_section(lines, "### Environment Variables"),
        re.compile(r"^\|\s*`([A-Z][A-Z0-9_]*)`\s*\|"),
    )
    if not env_rows:
        fail("README Environment Variables table is empty or not mechanically parseable")
    sources = [(path, path.read_text(encoding="utf-8")) for path in production_go_sources()]
    for variable in env_rows:
        if not has_real_env_read(variable, sources):
            fail(f"README Environment Variables table lists {variable!r} with no production read site")
    return len(target_rows), len(env_rows)


def main() -> int:
    parser = argparse.ArgumentParser(description="check README claims against canopyd and source")
    parser.add_argument("--readme", type=Path, default=Path(os.environ.get("DOCS_CHECK_README", DEFAULT_README)))
    parser.add_argument("--binary", type=Path, default=Path(os.environ.get("CANOPYD_BIN", DEFAULT_BINARY)))
    parser.add_argument("--docs-dir", type=Path, default=Path(os.environ.get("DOCS_CHECK_DOCS_DIR", DEFAULT_DOCS_DIR)))
    args = parser.parse_args()
    readme = args.readme if args.readme.is_absolute() else ROOT / args.readme
    binary = args.binary if args.binary.is_absolute() else ROOT / args.binary
    docs_dir = args.docs_dir if args.docs_dir.is_absolute() else ROOT / args.docs_dir
    if not readme.is_file():
        fail(f"README file not found: {readme}")
    if not binary.is_file() or not os.access(binary, os.X_OK):
        fail(f"built/current canopyd binary is missing or not executable: {binary} (run make build)")

    readme_text = readme.read_text(encoding="utf-8")
    docs_count = check_docs_index(readme_text, docs_dir)
    command_count = check_cli(readme_text, binary)
    target_count, env_count = check_tables(readme_text)
    print(
        f"docs-check: PASS ({docs_count} docs pages, {command_count} canopyd commands, "
        f"{target_count} Makefile targets, {env_count} environment variables)"
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except CheckFailure as exc:
        print(f"docs-check: FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1)
