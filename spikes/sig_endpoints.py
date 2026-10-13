"""Test whether signature-enforcing endpoints accept a well-formed but meaningless a_bogus.

The presence-only hypothesis does not need a valid signature to test. It needs
the opposite direction: if a garbage a_bogus is accepted wherever a signature is
enforced, then validity is not checked. A valid signature is only needed to
prove the converse.

Method matters and cost earlier conclusions: these routes answer 404 to GET and
200 to POST, which is why a real cookie appeared to break them.

Usage:
    python spikes/sig_endpoints.py cookies.txt [relayURL]
"""

from __future__ import annotations

import http.client
import json
import pathlib
import random
import ssl
import sys
import time
import uuid
from urllib.parse import urlencode, urlsplit

CTX = ssl.create_default_context()
ALPHABET = "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe"

# Routes the app signs, taken from its own requests. None of these generate
# content, so the generation throttle does not mask their answers.
ENDPOINTS = [
    "/alice/slot/action_bar_v3/get_item_conf",
    "/samantha/skill/recommend",
    "/alice/call/downgrade_config_pc",
    "/alice/commerce/sale/subscription/entry/config/",
    "/alice/user/config/pull",
    "/biz/activity/get_pop_window",
]


def garbage(length: int = 188) -> str:
    return "".join(random.choice(ALPHABET) for _ in range(length - 2)) + "=="


def app_query() -> str:
    """The parameter set the current web client sends for these routes."""
    return urlencode({
        "version_code": "20800", "language": "zh", "device_platform": "web",
        "doubao_device_platform": "web", "aid": "495671", "real_aid": "495671",
        "pkg_type": "release_version", "device_id": "7686671608228726325",
        "pc_version": "3.39.1", "doubao_pc_version": "3.39.1",
        "region": "JP", "sys_region": "JP", "samantha_web": "1",
        "web_platform": "browser", "use-olympus-account": "1",
        "web_tab_id": str(uuid.uuid4()),
    })


def call(relay: str, path: str, cookie: str, method: str) -> tuple[int, str]:
    r = urlsplit(relay)
    headers = {
        "Accept": "*/*",
        "Cookie": cookie,
        "Origin": "https://www.dola.com",
        "Referer": "https://www.dola.com/chat/",
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
        "x-relay-target": "https://www.dola.com",
        "x-relay-path": path,
    }
    body = None
    if method == "POST":
        headers["Content-Type"] = "application/json"
        body = b"{}"
    conn = http.client.HTTPSConnection(r.hostname, r.port or 443, timeout=45, context=CTX)
    conn.request(method, r.path or "/", body=body, headers=headers)
    resp = conn.getresponse()
    text = resp.read().decode("utf-8", "replace")
    conn.close()
    return resp.status, text


def summarize(text: str) -> str:
    try:
        parsed = json.loads(text)
    except json.JSONDecodeError:
        return text[:90].replace("\n", " ")
    if isinstance(parsed, dict):
        keys = [k for k in ("code", "msg", "message") if k in parsed]
        out = {k: parsed[k] for k in keys}
        if not out:
            out = {"topKeys": list(parsed)[:5]}
        return json.dumps(out, ensure_ascii=False)[:120]
    return str(parsed)[:100]


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    cookie = [ln.strip() for ln in
              pathlib.Path(sys.argv[1]).read_text(encoding="utf-8-sig").splitlines()
              if ln.strip() and not ln.strip().startswith("#")][0]
    relay = sys.argv[2] if len(sys.argv) > 2 else "https://vcrelay.dmean.dpdns.org/"

    for endpoint in ENDPOINTS:
        query = app_query()
        print(f"{endpoint}")
        for label, extra in (("no a_bogus", ""), ("garbage a_bogus", "&a_bogus=" + garbage())):
            path = f"{endpoint}?{query}{extra}"
            try:
                status, text = call(relay, path, cookie, "POST")
            except Exception as exc:  # noqa: BLE001
                print(f"    {label:<16} transport error: {exc}")
                continue
            print(f"    {label:<16} http={status}  {summarize(text)}")
            time.sleep(0.3)
        print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
