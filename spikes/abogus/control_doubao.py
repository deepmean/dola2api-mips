#!/usr/bin/env python3
"""Control experiment: does the recovered algorithm work on doubao at all?

This is the test that says whether the remaining work is about doubao's bytecode
or about the algorithm being wrong. doubao and dola run the same anti-abuse
system, and a signature that doubao accepts but dola does not points at dola
specifically; a signature doubao also rejects points at the algorithm itself.

    doubao accepts  -> the algorithm is right, dola's payload differs
    doubao rejects  -> the algorithm is wrong, and nothing else matters yet

The parameters come from a working doubao deployment rather than from dola, so
the aid and version are doubao's.

Usage:
    python control_doubao.py <cookies.txt>
"""

from __future__ import annotations

import base64
import json
import os
import pathlib
import subprocess
import sys
import urllib.parse
import urllib.request
import uuid

RELAY = "https://vcrelay.dmean.dpdns.org/"
UA = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
)


def cookie_of(path: str) -> str:
    with open(path, encoding="utf-8") as f:
        return "; ".join(ln.strip() for ln in f if ln.strip() and not ln.startswith("#"))


def main() -> int:
    cookie = cookie_of(sys.argv[1])
    out = subprocess.run(
        ["node", "sign-doubao.js", str(uuid.uuid4())],
        capture_output=True,
        text=True,
        check=True,
    )
    signed = json.loads(out.stdout.strip().splitlines()[-1])
    print(f"a_bogus: {signed['len']} chars in {signed['elapsed']} ms")
    print(f"signed  : {signed['sorted'][:140]}...")

    query = signed["sorted"] + "&a_bogus=" + urllib.parse.quote(signed["sig"])

    body = json.dumps({
        "messages": [{
            "local_message_id": str(uuid.uuid4()),
            "content_block": [{
                "block_type": 10000,
                "content": {
                    "text_block": {"text": "ping", "icon_url": "", "icon_url_dark": "", "summary": ""},
                    "pc_event_block": "",
                },
                "block_id": str(uuid.uuid4()),
                "parent_id": "",
                "meta_info": [],
                "append_fields": [],
            }],
            "message_status": 0,
        }],
        "option": {
            "create_time_ms": 1700000000000,
            "unique_key": str(uuid.uuid4()),
            "need_create_conversation": True,
            "conversation_init_option": {"need_ack_conversation": True},
            "need_deep_think": 0,
            "sse_recv_event_options": {"support_chunk_delta": True},
            "scene_type": 0,
            "start_seq": 0,
        },
        "user_context": [],
        "ext": {"fp": "", "use_deep_think": "0", "sub_conv_firstmet_type": "1",
                "collection_id": "", "conversation_init_option": '{"need_ack_conversation":true}',
                "commerce_credit_config_enable": "0"},
    })

    req = urllib.request.Request(RELAY, method="POST")
    req.add_header("x-relay-target", "https://www.doubao.com")
    req.add_header("x-relay-path", f"/samantha/chat/completion?{query}")
    req.add_header("Content-Type", "application/json")
    req.add_header("agw-js-conv", "str")
    req.add_header("Cookie", cookie)
    req.add_header("User-Agent", UA)
    req.add_header("Referer", "https://www.doubao.com/chat/")
    req.data = body.encode()

    try:
        with urllib.request.urlopen(req, timeout=90) as resp:
            text = resp.read().decode("utf-8", "replace")
            status = resp.status
    except urllib.error.HTTPError as e:
        text = e.read().decode("utf-8", "replace")
        status = e.code

    print(f"HTTP {status}, {len(text)} bytes\n")
    print(text[:500])
    for marker, verdict in (
        ("710022004", "unsigned"),
        ("710022002", "signed, rejected"),
        ("SSE_ACK", "ACCEPTED"),
    ):
        if marker in text:
            print(f"\nRESULT: doubao says {verdict}")
            return 0 if verdict == "ACCEPTED" else 1
    print("\nRESULT: no verdict marker")
    return 1


if __name__ == "__main__":
    sys.exit(main())