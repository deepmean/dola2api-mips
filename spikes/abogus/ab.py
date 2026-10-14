#!/usr/bin/env python3
"""A/B the recovered signature against the live endpoint.

Sends the same request twice, once signed and once unsigned, so the difference
in the response is attributable to the signature alone rather than to the
parameters, the body or the moment.

    unsigned            -> 710022004, no signature at all
    signed + rejected   -> 710022002, signature present and refused
    signed + accepted   -> the stream proceeds (SSE_ACK, then content)
"""

from __future__ import annotations

import json
import subprocess
import sys
import urllib.parse
import urllib.request
import uuid

RELAY = "https://vcrelay.dmean.dpdns.org/"
HOST = "www.dola.com"
UA = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
)


def cookie_of(path: str) -> str:
    with open(path, encoding="utf-8") as f:
        return "; ".join(ln.strip() for ln in f if ln.strip() and not ln.startswith("#"))


def body() -> str:
    now = uuid.uuid4()
    return json.dumps({
        "client_meta": {
            "local_conversation_id": f"local_{now}",
            "conversation_id": "",
            "bot_id": "7339470689562525703",
            "last_section_id": "",
            "last_message_index": None,
        },
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


def send(cookie: str, query: str) -> tuple[int, str]:
    req = urllib.request.Request(RELAY, method="POST")
    req.add_header("x-relay-target", f"https://{HOST}")
    req.add_header("x-relay-path", f"/chat/completion?{query}")
    req.add_header("Content-Type", "application/json")
    req.add_header("agw-js-conv", "str")
    req.add_header("Cookie", cookie)
    req.add_header("User-Agent", UA)
    req.data = body().encode()
    try:
        with urllib.request.urlopen(req, timeout=90) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")


def classify(text: str) -> str:
    for marker, verdict in (
        ("710022004", "unsigned"),
        ("710022002", "signed, rejected"),
        ("710020702", "accepted, replay refused"),
        ("SSE_ACK", "ACCEPTED"),
    ):
        if marker in text:
            return verdict
    return "no verdict marker"


def main() -> int:
    cookie = cookie_of(sys.argv[1])
    out = subprocess.run(["node", "sign-port.js", str(uuid.uuid4())],
                         capture_output=True, text=True, check=True)
    signed = json.loads(out.stdout.strip().splitlines()[-1])
    q = signed["sorted"]
    sig_q = q + "&a_bogus=" + urllib.parse.quote(signed["sig"])

    print(f"a_bogus: {signed['len']} chars\n")
    for label, query in (("unsigned ", q), ("signed   ", sig_q)):
        status, text = send(cookie, query)
        print(f"{label} HTTP {status}  -> {classify(text)}")
        for line in text.splitlines():
            if "error_code" in line:
                print(f"           {line.strip()[:150]}")
    return 0


if __name__ == "__main__":
    sys.exit(main())