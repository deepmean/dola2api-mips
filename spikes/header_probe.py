"""Send the port's request with browser-like headers, to see if headers are the
difference.

Isolated by experiment so far:
  browser env + browser params -> accepted
  browser env + port params    -> accepted
  python  env + port params    -> refused
  python  env + client params  -> refused

So the parameter set is not the variable and the environment is. Headers are the
cheapest part of the environment to reproduce, so they are tried first.

Usage:
    python spikes/header_probe.py <cookie_file> [--prompt TEXT]
"""

from __future__ import annotations

import argparse
import http.client
import json
import pathlib
import ssl
import sys
import time
import uuid

from parity_probe import chat_body, client_query, port_query

RELAY_HOST = "vcrelay.dmean.dpdns.org"
CTX = ssl.create_default_context()

CHROME_UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
             "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")


def headers_for(style: str) -> dict[str, str]:
    base = {"Content-Type": "application/json", "Cookie": ""}
    if style == "minimal":
        base["agw-js-conv"] = "str, str"
        base["Accept"] = "*/*"
        base["User-Agent"] = CHROME_UA
        return base

    # What the browser sends: captured from its own fetch, plus the headers a
    # same-origin fetch adds by itself.
    trace = "04-" + uuid.uuid4().hex + "-" + uuid.uuid4().hex[:16] + "-01"
    base.update({
        "agw-js-conv": "str",
        "Agw-Js-Conv": "str",
        "Accept": "*/*",
        "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
        "User-Agent": CHROME_UA,
        "Origin": "https://www.dola.com",
        "Referer": "https://www.dola.com/chat",
        "sec-ch-ua": '"Chromium";v="131", "Not_A Brand";v="24", "Google Chrome";v="131"',
        "sec-ch-ua-mobile": "?0",
        "sec-ch-ua-platform": '"Windows"',
        "Sec-Fetch-Dest": "empty",
        "Sec-Fetch-Mode": "cors",
        "Sec-Fetch-Site": "same-origin",
        "x-flow-trace": trace,
        "Priority": "u=1, i",
    })
    return base


def send(cookie: str, query: str, prompt: str, style: str, direct: bool = False) -> dict:
    headers = headers_for(style)
    headers["Cookie"] = cookie
    path = "/chat/completion?" + query
    if direct:
        conn = http.client.HTTPSConnection("www.dola.com", 443, timeout=90, context=CTX)
    else:
        headers["x-relay-target"] = "https://www.dola.com"
        headers["x-relay-path"] = path
        conn = http.client.HTTPSConnection(RELAY_HOST, 443, timeout=90, context=CTX)
        path = "/"
    conn.request("POST", path, body=chat_body(prompt), headers=headers)
    resp = conn.getresponse()
    text = resp.read().decode("utf-8", "replace")
    conn.close()

    import re
    code = (re.search(r'"error_code":(\d+)', text) or [None, None])[1]
    conv = (re.search(r'"conversation_id":"(\d+)"', text) or [None, ""])[1]
    return {"http": resp.status, "code": code, "conv": conv,
            "acked": "SSE_ACK" in text, "events": text.count("event:")}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("cookie_file")
    ap.add_argument("--prompt", default="用一句话介绍你自己")
    ap.add_argument("--direct", action="store_true")
    args = ap.parse_args()

    cookie = [l.strip() for l in pathlib.Path(args.cookie_file).read_text(encoding="utf-8-sig").splitlines()
              if l.strip() and not l.startswith("#")][0]

    for style in ("minimal", "browser"):
        result = send(cookie, port_query(str(uuid.uuid4()), cookie), args.prompt, style, args.direct)
        print(f"  headers={style:8s} -> {result}")
        time.sleep(1.0)
    return 0


if __name__ == "__main__":
    sys.exit(main())
