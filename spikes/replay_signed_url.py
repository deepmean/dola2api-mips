"""Replay a captured signed URL, to see whether the signature is the gate.

The browser's real request was recovered from the page's own performance
entries: it carries 22 parameters, including a_bogus (188-196 characters) and
msToken, neither of which the JavaScript hook could see because bdms wraps fetch
after the hook is installed.

If replaying that URL from a different environment succeeds, the signature is
what separates a working write from a refused one, and the environment does not
matter.

Usage:
    python spikes/replay_signed_url.py <cookie_file> <signed_url>
"""

from __future__ import annotations

import http.client
import pathlib
import re
import ssl
import sys

from parity_probe import chat_body

RELAY_HOST = "vcrelay.dmean.dpdns.org"
CTX = ssl.create_default_context()


def replay(cookie: str, signed_url: str, prompt: str) -> dict:
    # Keep the query exactly as captured; the signature is over that string.
    path = signed_url.split("www.dola.com", 1)[-1]
    headers = {
        "Content-Type": "application/json",
        "agw-js-conv": "str, str",
        "Accept": "*/*",
        "Cookie": cookie,
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
        "x-relay-target": "https://www.dola.com",
        "x-relay-path": path,
    }
    conn = http.client.HTTPSConnection(RELAY_HOST, 443, timeout=90, context=CTX)
    conn.request("POST", "/", body=chat_body(prompt), headers=headers)
    resp = conn.getresponse()
    text = resp.read().decode("utf-8", "replace")
    conn.close()

    code = (re.search(r'"error_code":(\d+)', text) or [None, None])[1]
    conv = (re.search(r'"conversation_id":"(\d+)"', text) or [None, ""])[1]
    return {"http": resp.status, "code": code, "conv": conv,
            "acked": "SSE_ACK" in text, "events": text.count("event:")}


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    cookie = [l.strip() for l in pathlib.Path(sys.argv[1]).read_text(encoding="utf-8-sig").splitlines()
              if l.strip() and not l.startswith("#")][0]
    signed = sys.argv[2].strip()
    prompt = sys.argv[3] if len(sys.argv) > 3 else "你好"

    has_bogus = "a_bogus=" in signed
    print(f"  replaying a URL with a_bogus={has_bogus} from python (different TLS, no browser)")
    print("  result:", replay(cookie, signed, prompt))
    return 0


if __name__ == "__main__":
    sys.exit(main())
