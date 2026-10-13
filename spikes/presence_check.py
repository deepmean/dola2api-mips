"""Does the signature layer require a valid a_bogus, or merely a present one?

The generation endpoint cannot answer this: it sits behind a service throttle
that also hits the browser, so every variant there returns 710022002. A read
endpoint on the signer's path list is not throttled, so its response separates
the cases:

    no a_bogus        the unsigned baseline
    garbage a_bogus   well-formed in shape and length, but meaningless
    (for contrast)    the generation endpoint, where both reach the throttle

If the read endpoint answers a garbage signature with real data, the layer only
checks that something shaped like an a_bogus is present, and the port does not
need to reproduce the signature at all. If it rejects the garbage one while
accepting a page-signed URL, the signature must be valid.

Usage:
    python spikes/presence_check.py cookies.txt [relayURL]
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


def garbage_bogus(length: int = 184) -> str:
    """Same alphabet and length as a real a_bogus, with no meaning."""
    return "".join(random.choice(ALPHABET) for _ in range(length - 2)) + "=="


def read_query(with_ms_token: bool) -> str:
    # The short parameter set is deliberate: the longer one (device_id,
    # pc_version, web_id) makes this endpoint answer 404 through the relay,
    # while this form returns a JSON body either way.
    params = {
        "version_code": "20800",
        "aid": "495671",
        "device_platform": "web",
        "web_platform": "browser",
    }
    if with_ms_token:
        params["msToken"] = "".join(random.choice(
            "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") for _ in range(120)) + "=="
    return urlencode(params)


def call(relay: str, path: str, cookie: str) -> tuple[int, str]:
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
    conn = http.client.HTTPSConnection(r.hostname, r.port or 443, timeout=45, context=CTX)
    conn.request("GET", r.path or "/", headers=headers)
    resp = conn.getresponse()
    text = resp.read().decode("utf-8", "replace")
    conn.close()
    return resp.status, text


def summarize(text: str) -> str:
    try:
        parsed = json.loads(text)
    except json.JSONDecodeError:
        return text[:160].replace("\n", " ")
    if isinstance(parsed, dict):
        out = {k: parsed.get(k) for k in ("code", "msg", "message") if k in parsed}
        if not out:
            out = {"topKeys": list(parsed)[:8]}
        return json.dumps(out, ensure_ascii=False)[:200]
    return json.dumps(parsed, ensure_ascii=False)[:200]


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    cookie = [ln.strip() for ln in
              pathlib.Path(sys.argv[1]).read_text(encoding="utf-8-sig").splitlines()
              if ln.strip() and not ln.strip().startswith("#")][0]
    relay = sys.argv[2] if len(sys.argv) > 2 else "https://vcrelay.dmean.dpdns.org/"

    endpoint = "/alice/user/config/pull"
    variants = {
        "no a_bogus": read_query(False),
        "garbage a_bogus": read_query(False) + "&a_bogus=" + garbage_bogus(),
        "garbage msToken+a_bogus": read_query(True) + "&a_bogus=" + garbage_bogus(),
    }

    print(f"GET {endpoint}  (a read endpoint, not the throttled generation path)\n")
    for name, query in variants.items():
        path = f"{endpoint}?{query}"
        try:
            status, text = call(relay, path, cookie)
        except Exception as exc:  # noqa: BLE001 - a transport failure is a result too
            print(f"  {name:<24} transport error: {exc}")
            continue
        print(f"  {name:<24} http={status}  {summarize(text)}")
        time.sleep(0.4)

    print("\nIf 'garbage a_bogus' returns the same payload as a signed request, the")
    print("layer only requires a present, well-formed a_bogus.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
