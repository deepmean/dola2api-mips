"""Compare how the signature layer treats a signed request, a tampered one, and none.

The generation endpoint cannot answer this: it is behind a service throttle that
affects the browser too, so every attempt there returns the same 710022002. A
read endpoint that is also on the signer's path list is not throttled, and the
response there distinguishes the cases:

    as-is        the URL exactly as the page signed it   -> should succeed
    tampered     a_bogus present but corrupted           -> valid vs merely present
    stripped     a_bogus removed                         -> the unsigned baseline

Usage:
    python spikes/sig_check.py "<signed URL from the page>" cookies.txt [relayURL]
"""

from __future__ import annotations

import http.client
import json
import pathlib
import ssl
import sys
from urllib.parse import quote, urlsplit

CTX = ssl.create_default_context()


def build_headers(cookie: str, target: str, path: str) -> dict:
    return {
        "Accept": "*/*",
        "Cookie": cookie,
        "Origin": "https://www.dola.com",
        "Referer": "https://www.dola.com/chat/",
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
        "x-relay-target": target,
        "x-relay-path": path,
    }


def request(relay: str, method: str, headers: dict, body: bytes | None):
    r = urlsplit(relay)
    conn = http.client.HTTPSConnection(r.hostname, r.port or 443, timeout=45, context=CTX)
    conn.request(method, r.path or "/", body=body, headers=headers)
    resp = conn.getresponse()
    text = resp.read().decode("utf-8", "replace")
    conn.close()
    return resp.status, resp.getheader("X-Tt-Agw-Login"), text


def tamper(bogus: str) -> str:
    """Flip characters while keeping the alphabet and length."""
    flipped = []
    for i, ch in enumerate(bogus):
        if i % 7 == 3:
            flipped.append("A" if ch != "A" else "B")
        else:
            flipped.append(ch)
    return "".join(flipped)


def strip_param(query: str, name: str) -> str:
    return "&".join(p for p in query.split("&") if not p.startswith(name + "="))


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    signed_url = sys.argv[1]
    cookie = [ln.strip() for ln in
              pathlib.Path(sys.argv[2]).read_text(encoding="utf-8-sig").splitlines()
              if ln.strip() and not ln.strip().startswith("#")][0]
    relay = sys.argv[3] if len(sys.argv) > 3 else "https://vcrelay.dmean.dpdns.org/"

    u = urlsplit(signed_url)
    target = f"{u.scheme}://{u.netloc}"
    query = u.query
    bogus = ""
    for part in query.split("&"):
        if part.startswith("a_bogus="):
            bogus = part[len("a_bogus="):]
    if not bogus:
        print("the URL carries no a_bogus; nothing to compare")
        return 2

    variants = {
        "as-is": query,
        "tampered": query.replace("a_bogus=" + bogus, "a_bogus=" + quote(tamper(bogus), safe="")),
        "stripped": strip_param(query, "a_bogus"),
    }

    method = "POST" if u.path.startswith("/chat/completion") else "GET"
    body = b"{}" if method == "POST" else None
    print(f"{method} {u.path}\na_bogus length {len(bogus)}\n")

    for name, q in variants.items():
        path = u.path + ("?" + q if q else "")
        headers = build_headers(cookie, target, path)
        if method == "POST":
            headers["Content-Type"] = "application/json"
        try:
            status, login, text = request(relay, method, headers, body)
        except Exception as exc:  # noqa: BLE001 - a transport failure is a result too
            print(f"  {name:<9} transport error: {exc}")
            continue
        try:
            parsed = json.loads(text)
            summary = {"code": parsed.get("code"), "msg": parsed.get("msg")}
            if summary["code"] is None:
                summary = {"keys": list(parsed)[:8]}
        except json.JSONDecodeError:
            summary = {"sseEvents": (text.count("event:") and
                                     [e.strip() for e in text.split("event:")[1:4]]) or text[:120]}
        print(f"  {name:<9} http={status} login={login}  {json.dumps(summary, ensure_ascii=False)[:200]}")

    print("\nInterpretation: if 'as-is' succeeds while 'tampered' does not, the signature")
    print("must be valid. If 'tampered' also succeeds, the layer only checks presence.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
