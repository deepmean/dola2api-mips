"""Replay an exactly-signed URL captured from the live page.

The point of this probe: a_bogus is computed over the *final* query string, and
the mssdk interceptor injects a fresh msToken into that string before signing.
Every earlier attempt built its own query with no msToken (or a stale one), so
the signature never matched what was sent.

If this replay reaches SSE_ACK, the signature is the only gate and the port can
satisfy it. If it still returns 710022002, something beyond the signature is
required and a router-only submit is not reachable this way.

Usage:
    python spikes/replay_signed.py "<full signed URL>" cookies.txt [relayURL]
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
from urllib.parse import urlsplit

CTX = ssl.create_default_context()


def submit_body() -> dict:
    now_ms = int(time.time() * 1000)
    return {
        "client_meta": {"local_conversation_id": f"local_{now_ms}", "conversation_id": "",
                        "bot_id": "7339470689562525703", "last_section_id": "", "last_message_index": None},
        "messages": [{"local_message_id": str(uuid.uuid4()),
                      "content_block": [{"block_type": 10000,
                                         "content": {"text_block": {
                                             "text": "Generate video: a cat sitting on a windowsill, 9:16",
                                             "icon_url": "", "icon_url_dark": "", "summary": ""},
                                             "pc_event_block": ""},
                                         "block_id": str(uuid.uuid4()), "parent_id": "",
                                         "meta_info": [], "append_fields": []}],
                      "message_status": 0}],
        "option": {"send_message_scene": "", "create_time_ms": now_ms, "collect_id": "", "is_audio": False,
                   "answer_with_suggest": False, "tts_switch": False, "need_deep_think": 0,
                   "click_clear_context": False, "from_suggest": False, "is_regen": False, "is_replace": False,
                   "is_from_click_option": False, "disable_sse_cache": False, "select_text_action": "",
                   "is_select_text": False, "resend_for_regen": False, "scene_type": 0,
                   "unique_key": str(uuid.uuid4()), "start_seq": 0, "need_create_conversation": True,
                   "conversation_init_option": {"need_ack_conversation": True}, "regen_query_id": [],
                   "edit_query_id": [], "regen_instruction": "", "no_replace_for_regen": False,
                   "message_from": 0, "shared_app_name": "", "shared_app_id": "",
                   "sse_recv_event_options": {"support_chunk_delta": True}, "is_ai_playground": False,
                   "is_old_user": False,
                   "recovery_option": {"is_recovery": False, "req_create_time_sec": now_ms // 1000,
                                       "append_sse_event_scene": 0},
                   "message_storage_type": 0},
        "chat_ability": {"ability_type": 17,
                         "ability_param": '{"ratio": "9:16", "model": "seedance_v2.0", "duration": 10}'},
        "user_context": [],
        "ext": {"fp": "", "use_deep_think": "0", "sub_conv_firstmet_type": "1", "collection_id": "",
                "conversation_init_option": '{"need_ack_conversation":true}',
                "commerce_credit_config_enable": "0"},
    }


def parse_sse(text: str) -> dict:
    conv, events, err = "", [], None
    for block in text.split("\n\n"):
        if not block.strip():
            continue
        name, data = "", ""
        for line in block.split("\n"):
            if line.startswith("event:"):
                name = line[6:].strip()
            elif line.startswith("data:"):
                data = line[5:].strip()
        events.append(name)
        if name == "SSE_ACK":
            try:
                conv = (json.loads(data).get("ack_client_meta") or {}).get("conversation_id") or ""
            except json.JSONDecodeError:
                pass
        if name == "STREAM_ERROR":
            try:
                p = json.loads(data)
                code = p.get("error_code")
                err = {"code": f"{code:.0f}" if isinstance(code, float) else str(code),
                       "msg": p.get("error_msg")}
            except json.JSONDecodeError:
                err = {"raw": data[:160]}
    return {"conv": conv, "events": events, "error": err}


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    signed_url = sys.argv[1]
    cookie = [ln.strip() for ln in
              pathlib.Path(sys.argv[2]).read_text(encoding="utf-8-sig").splitlines()
              if ln.strip() and not ln.strip().startswith("#")][0]
    relay = sys.argv[3] if len(sys.argv) > 3 else "https://vcrelay.dmean.dpdns.org/"
    # Optional: send an X-Bogus header too. The mssdk is known to pair the query
    # parameter with a header, and a replay carrying only a_bogus is measurably
    # not enough. The stub is the part of the URL the signature is computed over.
    xbogus_stub = sys.argv[4] if len(sys.argv) > 4 else ""

    u = urlsplit(signed_url)
    path = u.path + ("?" + u.query if u.query else "")
    has_bogus = "a_bogus=" in path
    print(f"path      : {path[:120]}...")
    print(f"a_bogus   : {'present' if has_bogus else 'MISSING'}")
    print(f"msToken   : {'present' if 'msToken=' in path else 'missing'}")

    body = submit_body()
    payload = json.dumps(body, separators=(",", ":")).encode()
    headers = {
        "Content-Type": "application/json",
        "agw-js-conv": "str, str",
        "Accept": "*/*",
        "Cookie": cookie,
        "Origin": "https://www.dola.com",
        "Referer": "https://www.dola.com/chat/",
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
        "x-relay-target": f"{u.scheme}://{u.netloc}",
        "x-relay-path": path,
    }

    if xbogus_stub:
        from xbogus_test import x_bogus  # verified against the live SDK oracle

        query = u.query
        stub = {
            "query": query,
            "path": u.path + "?" + query,
            "query-no-bogus": "&".join(p for p in query.split("&") if not p.startswith("a_bogus=")),
            "url": signed_url,
        }[xbogus_stub]
        value = x_bogus(stub, bogus_index=random.randrange(1, 60))
        headers["X-Bogus"] = value
        print(f"X-Bogus   : {value}  (stub = {xbogus_stub})")
    r = urlsplit(relay)
    conn = http.client.HTTPSConnection(r.hostname, r.port or 443, timeout=60, context=CTX)
    conn.request("POST", r.path or "/", body=payload, headers=headers)
    resp = conn.getresponse()
    text = resp.read().decode("utf-8", "replace")
    conn.close()

    info = parse_sse(text)
    print(f"\nstatus    : {resp.status}")
    print(f"agw-login : {resp.getheader('X-Tt-Agw-Login')}")
    print(f"events    : {info['events'][:6]}")
    if info["conv"]:
        print(f"conv      : {info['conv']}")
    if info["error"]:
        print(f"error     : {info['error']}")
    if not info["conv"] and not info["error"]:
        print(f"body      : {text[:240]}")

    print()
    if info["conv"]:
        print("=> ACCEPTED: a fresh signed URL passes out-of-browser. The signature is the gate.")
    elif (info["error"] or {}).get("code") == "710022002":
        print("=> still 710022002: a valid signature is not sufficient out-of-browser.")
    else:
        print("=> inconclusive; read the body above")
    return 0


if __name__ == "__main__":
    sys.exit(main())
