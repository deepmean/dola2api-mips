#!/usr/bin/env python3
"""Does the recovered a_bogus pass dola's check?

The three answers are unambiguous, which is what makes this testable:

    710022004   no signature at all
    710022002   signature present but rejected
    SSE_ACK     signature accepted

So: sign the port's own parameters, send them with a full request body, and see
which of the three comes back. Anything other than 710022002 means the signature
is doing its job.

Usage:
    python verify_abogus.py <cookies.txt>
"""

from __future__ import annotations

import json
import subprocess
import sys
import time
import urllib.parse
import urllib.request

RELAY = "https://vcrelay.dmean.dpdns.org/"
TARGET_HOST = "www.dola.com"
UA = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
)

BODY = {
    "client_meta": {
        "local_conversation_id": f"local_{int(time.time() * 1000)}",
        "conversation_id": "",
        "bot_id": "7339470689562525703",
        "last_section_id": "",
        "last_message_index": None,
    },
    "messages": [
        {
            "local_message_id": "11111111-2222-3333-4444-555555555555",
            "content_block": [
                {
                    "block_type": 10000,
                    "content": {
                        "text_block": {
                            "text": "ping",
                            "icon_url": "",
                            "icon_url_dark": "",
                            "summary": "",
                        },
                        "pc_event_block": "",
                    },
                    "block_id": "66666666-7777-8888-9999-000000000000",
                    "parent_id": "",
                    "meta_info": [],
                    "append_fields": [],
                }
            ],
            "message_status": 0,
        }
    ],
    "option": {
        "send_message_scene": "",
        "create_time_ms": int(time.time() * 1000),
        "collect_id": "",
        "is_audio": False,
        "answer_with_suggest": False,
        "tts_switch": False,
        "need_deep_think": 0,
        "click_clear_context": False,
        "from_suggest": False,
        "is_regen": False,
        "is_replace": False,
        "is_from_click_option": False,
        "disable_sse_cache": False,
        "select_text_action": "",
        "is_select_text": False,
        "resend_for_regen": False,
        "scene_type": 0,
        "unique_key": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
        "start_seq": 0,
        "need_create_conversation": True,
        "conversation_init_option": {"need_ack_conversation": True},
        "regen_query_id": [],
        "edit_query_id": [],
        "regen_instruction": "",
        "no_replace_for_regen": False,
        "message_from": 0,
        "shared_app_name": "",
        "shared_app_id": "",
        "sse_recv_event_options": {"support_chunk_delta": True},
        "is_ai_playground": False,
        "is_old_user": False,
        "recovery_option": {
            "is_recovery": False,
            "req_create_time_sec": int(time.time()),
            "append_sse_event_scene": 0,
        },
        "message_storage_type": 0,
    },
    "user_context": [],
    "ext": {
        "fp": "",
        "use_deep_think": "0",
        "sub_conv_firstmet_type": "1",
        "collection_id": "",
        "conversation_init_option": '{"need_ack_conversation":true}',
        "commerce_credit_config_enable": "0",
    },
}


def read_cookie(path: str) -> str:
    with open(path, encoding="utf-8") as f:
        parts = [ln.strip() for ln in f if ln.strip() and not ln.startswith("#")]
    return "; ".join(parts)


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    cookie = read_cookie(sys.argv[1])
    print(f"cookie: {len(cookie)} chars")

    proc = subprocess.run(
        ["node", "sign-port.js"], capture_output=True, text=True, check=True
    )
    signed = json.loads(proc.stdout.strip().splitlines()[-1])
    print(f"a_bogus: {signed['len']} chars in {signed['elapsed']} ms")

    query = signed["sorted"] + "&msToken=" + urllib.parse.quote("TESTTOKEN123") + \
        "&a_bogus=" + urllib.parse.quote(signed["sig"])

    req = urllib.request.Request(RELAY, method="POST")
    req.add_header("x-relay-target", f"https://{TARGET_HOST}")
    req.add_header("x-relay-path", f"/chat/completion?{query}")
    req.add_header("Content-Type", "application/json")
    req.add_header("agw-js-conv", "str")
    req.add_header("Cookie", cookie)
    req.add_header("User-Agent", UA)
    req.data = json.dumps(BODY).encode()

    with urllib.request.urlopen(req, timeout=90) as resp:
        text = resp.read().decode("utf-8", "replace")

    print(f"HTTP {resp.status}, {len(text)} bytes")
    code = ""
    for marker in ("710022004", "710022002", "710020202", "SSE_ACK", "conversation_id"):
        if marker in text:
            code = marker
            break
    print(f"marker: {code or '(none)'}")
    print()
    print(text[:600])
    if code == "SSE_ACK":
        print("\nRESULT: the signature was accepted")
        return 0
    if code == "710022002":
        print("\nRESULT: the signature was rejected")
        return 1
    print("\nRESULT: inconclusive, see the response above")
    return 1


if __name__ == "__main__":
    sys.exit(main())