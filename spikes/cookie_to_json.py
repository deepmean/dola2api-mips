"""Convert one account's cookie header into the list browser_auth expects.

The header form is "k=v; k=v; ..."; the browser API wants one object per cookie
with a domain and path. Values are never printed.

Usage:
    python spikes/cookie_to_json.py <cookie_file> <out.json> [domain]
"""

from __future__ import annotations

import json
import pathlib
import sys


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    source, out = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
    domain = sys.argv[3] if len(sys.argv) > 3 else ".dola.com"

    raw = [l.strip() for l in source.read_text(encoding="utf-8-sig").splitlines()
           if l.strip() and not l.startswith("#")][0]

    cookies = []
    for part in raw.split(";"):
        part = part.strip()
        if "=" not in part:
            continue
        name, value = part.split("=", 1)
        name = name.strip()
        if not name:
            continue
        cookies.append({
            "name": name,
            "value": value.strip(),
            "domain": domain,
            "path": "/",
            "secure": True,
            "httpOnly": False,
            "sameSite": "Lax",
        })

    out.write_text(json.dumps(cookies, ensure_ascii=False), encoding="utf-8")
    names = [c["name"] for c in cookies]
    print(f"wrote {len(cookies)} cookie(s) to {out}")
    print("names:", ", ".join(names))
    for key in ("sessionid", "msToken", "s_v_web_id", "sid_guard"):
        present = key in names
        print(f"  {key}: {'present' if present else 'MISSING'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
