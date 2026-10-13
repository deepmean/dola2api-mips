"""Split accounts.json into one cookie file per account, for probing.

Prints only labels and session-id prefixes, never a cookie value.

Usage:
    python spikes/split_accounts.py accounts.json out_dir
"""

from __future__ import annotations

import json
import pathlib
import sys


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    source, out_dir = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
    out_dir.mkdir(parents=True, exist_ok=True)

    entries = json.loads(source.read_text(encoding="utf-8"))
    if isinstance(entries, dict):
        entries = entries.get("accounts", [])

    written = []
    for i, entry in enumerate(entries):
        cookie = str(entry.get("cookie", "")).strip()
        if not cookie:
            continue
        label = str(entry.get("label") or f"account-{i + 1}")
        session = ""
        for part in cookie.split(";"):
            if "sessionid=" in part:
                session = part.split("=", 1)[1][:8]
        path = out_dir / f"{label}.txt"
        path.write_text(cookie + "\n", encoding="utf-8")
        written.append((label, session, path))

    for label, session, path in written:
        print(f"  {label}: sessionid={session}... -> {path.name}")
    print(f"wrote {len(written)} cookie file(s) to {out_dir}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
