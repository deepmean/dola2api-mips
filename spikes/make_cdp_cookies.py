"""Convert a cookies.txt line into the CDP cookie array that browser_auth wants.

sessionid is HttpOnly, so it cannot be set from page JS via document.cookie; the
CDP path (Network.setCookie) can, which is what this array feeds.

Usage:
    python spikes/make_cdp_cookies.py [cookies.txt] [--domain .dola.com] [--out spikes/cookies.cdp.json]
"""

from __future__ import annotations

import argparse
import json
import pathlib
import sys


def parse(line: str) -> dict[str, str]:
    out = {}
    for part in line.split(";"):
        part = part.strip()
        if not part or "=" not in part:
            continue
        k, v = part.split("=", 1)
        out[k.strip()] = v.strip()
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("file", nargs="?", default="cookies.txt")
    ap.add_argument("--domain", default=".dola.com")
    ap.add_argument("--out", default="spikes/cookies.cdp.json")
    args = ap.parse_args()

    lines = [ln.strip() for ln in pathlib.Path(args.file).read_text(encoding="utf-8-sig").splitlines()
             if ln.strip() and not ln.strip().startswith("#")]
    if not lines:
        print(f"{args.file} has no cookie lines", file=sys.stderr)
        return 2

    cookies = []
    for name, value in parse(lines[0]).items():
        cookie = {
            "name": name,
            "value": value,
            "domain": args.domain,
            "path": "/",
            "secure": True,
            "sameSite": "Lax",
        }
        # The session cookie is HttpOnly in the browser; mark the ones that are,
        # so the restored session behaves like the original.
        if name in ("sessionid", "sessionid_ss", "sid_tt", "sid_guard", "sid_ucp_v1", "ssid_ucp_v1",
                    "uid_tt", "uid_tt_ss", "passport_auth_status", "passport_auth_status_ss", "odin_tt"):
            cookie["httpOnly"] = True
        cookies.append(cookie)

    pathlib.Path(args.out).write_text(json.dumps(cookies, ensure_ascii=False, indent=1), encoding="utf-8")
    names = ", ".join(c["name"] for c in cookies)
    print(f"{len(cookies)} cookies -> {args.out}")
    print(f"names: {names}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
