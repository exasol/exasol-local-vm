#!/usr/bin/env python3
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

"""Check or add copyright/SPDX headers for selected repository files.

This script intentionally has no third-party dependencies. The target scope is
configured near the top of the file so it is easy to see and change.
"""

from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path


# ---------------------------------------------------------------------------
# Visible target configuration
# ---------------------------------------------------------------------------

COPYRIGHT = "Copyright 2026 Exasol AG"
SPDX = "SPDX-License-Identifier: MIT"

# The tool only checks/adds headers for these file groups:
#   1. GitHub Actions workflow YAML files and composite/action metadata YAML
#   2. POSIX-style shell scripts (.sh/.bash/.zsh/.ksh or shell shebang)
#   3. PowerShell scripts (*.ps1)
#   4. Go source files (*.go), deliberately NOT go.mod/go.sum
#   5. Container files named Containerfile or Dockerfile
#
# It deliberately does NOT target XML/plist files, Linux config files such as
# fstab/inittab/*.conf/*.event, Markdown, JSON, or go.mod.
GITHUB_ACTIONS_YAML_GLOBS = (
    ".github/workflows/*.yml",
    ".github/workflows/*.yaml",
    ".github/actions/**/action.yml",
    ".github/actions/**/action.yaml",
)

SHELL_SCRIPT_EXTENSIONS = (
    ".sh",
    ".bash",
    ".zsh",
    ".ksh",
)

SHELL_SHEBANG_COMMANDS = {
    "ash",
    "bash",
    "dash",
    "ksh",
    "sh",
    "zsh",
}

POWERSHELL_SCRIPT_EXTENSIONS = (
    ".ps1",
)

GO_SOURCE_EXTENSIONS = (
    ".go",
)

CONTAINER_FILE_NAMES = {
    "Containerfile",
    "Dockerfile",
}

# ---------------------------------------------------------------------------
# End of target configuration
# ---------------------------------------------------------------------------


ROOT = Path(__file__).resolve().parents[1]


def repository_files() -> list[Path]:
    """Return tracked and untracked-but-not-ignored repository files."""
    result = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        cwd=ROOT,
        check=True,
        capture_output=True,
    )
    return [ROOT / name.decode("utf-8") for name in result.stdout.split(b"\0") if name]


def is_binary(data: bytes) -> bool:
    return b"\0" in data


def matches_any_glob(path: Path, patterns: tuple[str, ...]) -> bool:
    path_text = path.as_posix()
    return any(path.match(pattern) or path_text == pattern for pattern in patterns)


def shell_command_from_shebang(first_line: str) -> str | None:
    if not first_line.startswith("#!"):
        return None

    parts = first_line[2:].strip().split()
    if not parts:
        return None

    command = Path(parts[0]).name
    if command != "env":
        return command

    for part in parts[1:]:
        if part.startswith("-"):
            continue
        return Path(part).name
    return None


def is_shell_script(path: Path, text: str) -> bool:
    if path.suffix in SHELL_SCRIPT_EXTENSIONS:
        return True
    first_line = text.splitlines()[0] if text.splitlines() else ""
    return shell_command_from_shebang(first_line) in SHELL_SHEBANG_COMMANDS


def detect_comment_style(path: Path, text: str) -> str | None:
    """Return the comment style for configured targets, or None to skip."""
    relative_path = path.relative_to(ROOT)

    if matches_any_glob(relative_path, GITHUB_ACTIONS_YAML_GLOBS):
        return "#"
    if is_shell_script(relative_path, text):
        return "#"
    if relative_path.suffix in POWERSHELL_SCRIPT_EXTENSIONS:
        return "#"
    if relative_path.suffix in GO_SOURCE_EXTENSIONS:
        return "//"
    if relative_path.name in CONTAINER_FILE_NAMES:
        return "#"
    return None


def header_for(style: str) -> list[str]:
    return [f"{style} {COPYRIGHT}\n", f"{style} {SPDX}\n", "\n"]


def insertion_index(lines: list[str]) -> int:
    if lines and lines[0].startswith("#!"):
        return 1
    return 0


def has_header(lines: list[str]) -> bool:
    insert_at = insertion_index(lines)
    window = "".join(lines[insert_at : insert_at + 8])
    return COPYRIGHT in window and SPDX in window


def add_header(text: str, style: str) -> str:
    lines = text.splitlines(keepends=True)
    insert_at = insertion_index(lines)

    while insert_at < len(lines) and lines[insert_at].strip() == "":
        del lines[insert_at]

    lines[insert_at:insert_at] = header_for(style)
    return "".join(lines)


def candidate_files() -> list[tuple[Path, str, str]]:
    candidates: list[tuple[Path, str, str]] = []
    for path in repository_files():
        if not path.exists():
            continue
        data = path.read_bytes()
        if is_binary(data):
            continue
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            continue
        style = detect_comment_style(path, text)
        if style is None:
            continue
        candidates.append((path, text, style))
    return sorted(candidates, key=lambda item: item[0].relative_to(ROOT).as_posix())


def check() -> int:
    missing: list[Path] = []
    for path, text, _style in candidate_files():
        if not has_header(text.splitlines(keepends=True)):
            missing.append(path.relative_to(ROOT))

    if missing:
        print("Missing copyright/SPDX headers:")
        for path in missing:
            print(f"  {path}")
        print("\nRun `task fix-copyright` to add missing headers.")
        return 1

    print("All configured files have copyright/SPDX headers.")
    return 0


def fix() -> int:
    updated: list[Path] = []
    for path, text, style in candidate_files():
        if has_header(text.splitlines(keepends=True)):
            continue
        path.write_text(add_header(text, style), encoding="utf-8")
        updated.append(path.relative_to(ROOT))

    if updated:
        print("Added copyright/SPDX headers:")
        for path in updated:
            print(f"  {path}")
    else:
        print("No missing copyright/SPDX headers found in configured files.")
    return 0


def list_files() -> int:
    print("Configured files checked by this tool:")
    for path, _text, _style in candidate_files():
        print(f"  {path.relative_to(ROOT)}")
    return 0


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="check headers and fail if any are missing")
    mode.add_argument("--fix", action="store_true", help="add missing headers in place")
    mode.add_argument("--list", action="store_true", help="list configured files that would be checked")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.fix:
        return fix()
    if args.list:
        return list_files()
    return check()


if __name__ == "__main__":
    sys.exit(main())
