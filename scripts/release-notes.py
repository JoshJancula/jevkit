#!/usr/bin/env python3
"""Extract one reviewed changelog entry for GoReleaser; never fall back to commits."""

import re
import sys
from pathlib import Path


VERSION = r"[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?"
HEADING = re.compile(rf"^#{{2,3}} \[?v?({VERSION})(?:\]|(?=\s|$))")


def release_notes(changelog: str, tag: str) -> str:
    if not re.fullmatch(rf"v{VERSION}", tag):
        raise ValueError(f"Expected a version tag such as v0.1.0, got {tag!r}")
    entries = []
    current_version = None
    body = []
    for line in changelog.splitlines():
        match = HEADING.match(line)
        if match:
            if current_version == tag[1:]:
                entries.append("\n".join(body).strip())
            current_version = match.group(1)
            body = []
        elif current_version is not None:
            body.append(line)
    if current_version == tag[1:]:
        entries.append("\n".join(body).strip())
    if len(entries) != 1 or not entries[0]:
        raise ValueError(f"Expected exactly one nonempty changelog entry for {tag}")
    return entries[0] + "\n"


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            raise ValueError("Usage: release-notes.py vX.Y.Z")
        print(release_notes(Path("CHANGELOG.md").read_text(), sys.argv[1]), end="")
    except (OSError, ValueError) as error:
        sys.exit(str(error))
