"""Debug: why does the relay answer 404 to http.client but 200 to curl?"""

from __future__ import annotations

import http.client
import ssl
from urllib.parse import urlsplit

RELAY = "https://vcrelay.dmean.dpdns.org/"
PATHS = [
    "/robots.txt",
    "/alice/user/config/pull?version_code=20800&aid=495671&device_platform=web&web_platform=browser",
]

r = urlsplit(RELAY)

# The cookie is what presence_check adds and this debug script did not; a very
# long Cookie header is the suspected cause of the 404.
cookie = ""
try:
    with open("cookies.txt", encoding="utf-8-sig") as fh:
        for line in fh:
            line = line.strip()
            if line and not line.startswith("#"):
                cookie = line
                break
except OSError as exc:
    print(f"  (no cookie file: {exc})")
print(f"  cookie length: {len(cookie)}")


def _first(jar: str, *names: str) -> str:
    """Keep only the named cookies, to tell length from content."""
    kept = []
    for part in jar.split(";"):
        part = part.strip()
        if part.split("=", 1)[0].strip() in names:
            kept.append(part)
    return "; ".join(kept)

for path in PATHS:
    for label, jar in (("none", ""), ("full", cookie), ("sessionid-only", _first(cookie, "sessionid")),
                       ("sessionid+sid_tt", _first(cookie, "sessionid", "sid_tt")),
                       ("dummy", "sessionid=deadbeef")):
        if label != "none" and not jar:
            continue
        headers = {
            "x-relay-target": "https://www.dola.com",
            "x-relay-path": path,
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                          "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
            "Accept": "*/*",
        }
        if jar:
            headers["Cookie"] = jar
        conn = http.client.HTTPSConnection(r.hostname, r.port or 443, timeout=30,
                                           context=ssl.create_default_context())
        try:
            conn.request("GET", r.path or "/", headers=headers)
            resp = conn.getresponse()
            body = resp.read().decode("utf-8", "replace")
            print(f"  {resp.status}  cookie={label:<16} len={len(jar):<5} {path[:46]}")
        except Exception as exc:  # noqa: BLE001
            print(f"  ERROR cookie={label} {path[:46]}: {exc}")
        conn.close()
