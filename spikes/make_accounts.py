"""Build the router's accounts.json from the local cookie files.

Keeps the secrets out of the terminal: the cookies are read from files and only
a summary is printed.

Usage:
    python spikes/make_accounts.py out.json old_cookies.txt [new_cookie.txt ...]
"""

from __future__ import annotations

import json
import pathlib
import sys
import urllib.parse


def read_cookie(path: str) -> str:
    for line in pathlib.Path(path).read_text(encoding="utf-8-sig").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            return line
    raise SystemExit(f"no cookie line in {path}")


def fields(cookie: str) -> dict[str, str]:
    out: dict[str, str] = {}
    for part in cookie.split(";"):
        part = part.strip()
        if "=" in part:
            k, v = part.split("=", 1)
            out[k.strip()] = v.strip()
    return out


def expiry(cookie: str) -> str:
    raw = fields(cookie).get("sid_guard", "")
    if not raw:
        return "unknown"
    bits = urllib.parse.unquote(raw).split("|")
    return bits[3] if len(bits) >= 4 else "unparsed"


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    out_path, sources = sys.argv[1], sys.argv[2:]

    accounts = []
    seen: set[str] = set()
    for i, path in enumerate(sources):
        cookie = read_cookie(path)
        session = fields(cookie).get("sessionid", "")
        if not session:
            print(f"  skipping {path}: no sessionid")
            continue
        if session in seen:
            print(f"  skipping {path}: duplicate sessionid")
            continue
        seen.add(session)

        accounts.append({
            "label": "primary" if i == 0 else f"spare-{i}",
            "cookie": cookie,
            "enabled": True,
        })
        print(f"  {path}: sessionid={session[:8]}...  expires {expiry(cookie)}"
              f"  msToken={'yes' if fields(cookie).get('msToken') else 'NO'}")

    if not accounts:
        raise SystemExit("no usable cookies")

    pathlib.Path(out_path).write_text(
        json.dumps(accounts, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {out_path} with {len(accounts)} account(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
