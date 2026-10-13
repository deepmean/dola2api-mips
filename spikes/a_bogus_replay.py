"""Confirm that a_bogus is the gate.

The browser's own /chat/completion URL was captured from the page's resource
timing and it carries an extra query parameter the upstream client never sends:

    ...&msToken=<cookie token>&fp=<s_v_web_id>&a_bogus=<signature>

a_bogus is computed by the bdms-sdk (ByteDance risk SDK) the page loads. The
upstream repo only has dola_client._fake_a_bogus(), which returns a random
string -- so its protocol path was never able to pass risk control, which is why
server.py never calls it.

This replays that exact captured URL (signature included) from outside the
browser. If it returns SSE_ACK, a_bogus is proven to be the discriminator.

Usage:
    python spikes/a_bogus_replay.py --cookie-file cookies.txt --url-file spikes/captured_submit_url.txt
"""

from __future__ import annotations

import argparse
import json
import pathlib
import sys
import time
import uuid

from urllib.parse import urlsplit

from tls_impersonate_probe import build_body, parse_cookie, parse_sse


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cookie-file", default="cookies.txt")
    ap.add_argument("--url-file", required=True, help="file containing the captured /chat/completion URL")
    ap.add_argument("--impersonate", default="chrome")
    ap.add_argument("--prompt", default="a cat sitting on a windowsill")
    ap.add_argument("--ratio", default="9:16")
    ap.add_argument("--duration", type=int, default=10)
    ap.add_argument("--timeout", type=float, default=60.0)
    args = ap.parse_args()

    cookie = [ln.strip() for ln in pathlib.Path(args.cookie_file).read_text(encoding="utf-8-sig").splitlines()
              if ln.strip() and not ln.strip().startswith("#")][0]
    fields = parse_cookie(cookie)
    url = pathlib.Path(args.url_file).read_text(encoding="utf-8").strip()

    parts = urlsplit(url)
    has_bogus = "a_bogus=" in parts.query
    print(f"replaying   : {parts.scheme}://{parts.netloc}{parts.path}")
    print(f"a_bogus     : {'present' if has_bogus else 'MISSING'}")
    print(f"impersonate : {args.impersonate or '(libcurl default)'}")
    print()

    from curl_cffi import requests as creq

    body = build_body(args.prompt, args.ratio, args.duration)
    body["ext"]["fp"] = fields.get("s_v_web_id", "")
    headers = {
        "Content-Type": "application/json",
        "agw-js-conv": "str, str",
        "Accept": "*/*",
        "Cookie": cookie,
    }
    kwargs = {"headers": headers, "json": body, "timeout": args.timeout, "stream": True}
    if args.impersonate:
        kwargs["impersonate"] = args.impersonate

    t0 = time.perf_counter()
    r = creq.post(url, **kwargs)
    chunks = []
    for chunk in r.iter_content():
        chunks.append(chunk)
        joined = b"".join(chunks).decode("utf-8", "replace")
        if "SSE_ACK" in joined or "STREAM_ERROR" in joined:
            break
    text = b"".join(chunks).decode("utf-8", "replace")
    parsed = parse_sse(text)

    result = {
        "status": r.status_code,
        "content_type": r.headers.get("content-type"),
        "agw_login": r.headers.get("x-tt-agw-login"),
        "elapsed_s": round(time.perf_counter() - t0, 2),
        "a_bogus_present": has_bogus,
        "conversation_id": parsed["conv_id"],
        "event_names": parsed["event_names"][:6],
        "error": parsed["error"],
        "body_head": text[:200],
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))

    if parsed["conv_id"]:
        print("\nVERDICT: a_bogus is the discriminator -- the same request without it is risk-controlled")
        return 0
    print("\nVERDICT: the captured signature did not help (it is probably bound to time/body/nonce)")
    return 1


if __name__ == "__main__":
    sys.path.insert(0, str(pathlib.Path(__file__).parent))
    sys.exit(main())
