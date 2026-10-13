"""One command that settles whether the generation gate needs a VALID a_bogus.

Everything else is already known: on the generation path, no a_bogus answers
710022004 (slide captcha) while any well-formed a_bogus answers 710022002, and
a garbage signature behaves exactly like a valid one. The missing piece is
whether a garbage signature also survives a *successful* generation, which could
not be tested while the service was throttling (the throttle hits the browser
too, so the browser is not a usable control right now).

This runs three variants with a real submit body and prints the comparison:

    none      no a_bogus            expect 710022004 while the gate is closed
    garbage   well-formed nonsense  the question
    computed  our own signature     for contrast

Read it as:
  * garbage generates like computed -> the port only needs a well-formed string
  * garbage is rejected while computed generates -> the algorithm must be real

Usage:
    python spikes/confirm_gate.py cookies.txt [relayURL]
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


def garbage(length: int = 188) -> str:
    return "".join(random.choice(ALPHABET) for _ in range(length - 2)) + "=="


def computed(stub: str) -> str:
    """Our own implementation, imported from the probe that verified it."""
    sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
    from xbogus_test import x_bogus  # noqa: PLC0415 - optional, only for contrast

    return x_bogus(stub, bogus_index=random.randrange(1, 60))


def submit_body() -> bytes:
    now = int(time.time() * 1000)
    body = {
        "client_meta": {"local_conversation_id": f"local_{now}", "conversation_id": "",
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
        "option": {"send_message_scene": "", "create_time_ms": now, "collect_id": "", "is_audio": False,
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
                   "recovery_option": {"is_recovery": False, "req_create_time_sec": now // 1000,
                                       "append_sse_event_scene": 0},
                   "message_storage_type": 0},
        "chat_ability": {"ability_type": 17,
                         "ability_param": '{"ratio": "9:16", "model": "seedance_v2.0", "duration": 10}'},
        "user_context": [],
        "ext": {"fp": "", "use_deep_think": "0", "sub_conv_firstmet_type": "1", "collection_id": "",
                "conversation_init_option": '{"need_ack_conversation":true}',
                "commerce_credit_config_enable": "0"},
    }
    return json.dumps(body, separators=(",", ":")).encode()


def run(relay: str, query: str, cookie: str) -> dict:
    r = urlsplit(relay)
    headers = {
        "Content-Type": "application/json",
        "agw-js-conv": "str, str",
        "Accept": "*/*",
        "Cookie": cookie,
        "Origin": "https://www.dola.com",
        "Referer": "https://www.dola.com/chat/",
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
        "x-relay-target": "https://www.dola.com",
        "x-relay-path": "/chat/completion?" + query,
    }
    conn = http.client.HTTPSConnection(r.hostname, r.port or 443, timeout=60, context=CTX)
    conn.request("POST", r.path or "/", body=submit_body(), headers=headers)
    resp = conn.getresponse()
    text = resp.read().decode("utf-8", "replace")
    conn.close()
    code = None
    if m := __import__("re").search(r'"error_code":(\d+)', text):
        code = m.group(1)
    conv = ""
    if m := __import__("re").search(r'"conversation_id":"(\d+)"', text):
        conv = m.group(1)
    return {"status": resp.status, "code": code, "conv": conv,
            "acked": "SSE_ACK" in text, "events": (text.count("event:"))}


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    cookie = [ln.strip() for ln in
              pathlib.Path(sys.argv[1]).read_text(encoding="utf-8-sig").splitlines()
              if ln.strip() and not ln.strip().startswith("#")][0]
    relay = sys.argv[2] if len(sys.argv) > 2 else "https://vcrelay.dmean.dpdns.org/"

    base = urlencode({
        "aid": "495671", "channel": "g", "device_platform": "web",
        "language": "zh-Hant", "region": "JP", "sys_region": "JP",
        "samantha_web": "1", "use-olympus-account": "1", "version_code": "20800",
        "web_platform": "browser", "web_tab_id": str(uuid.uuid4()),
    })

    variants = {
        "none": base,
        "garbage": base + "&a_bogus=" + garbage(),
        "computed": base + "&a_bogus=" + computed(base),
    }

    print("POST /chat/completion with a real body, three signature variants\n")
    results = {}
    for name, query in variants.items():
        try:
            results[name] = run(relay, query, cookie)
        except Exception as exc:  # noqa: BLE001
            results[name] = {"error": str(exc)}
        r = results[name]
        print(f"  {name:<9} {json.dumps(r, ensure_ascii=False)}")
        time.sleep(1.0)

    print()
    ok = [n for n, r in results.items() if r.get("acked") or r.get("conv")]
    if ok:
        print(f"generation succeeded for: {', '.join(ok)}")
        print("if 'garbage' is in that list, the gate needs presence only")
    else:
        print("no variant generated; the gate is still closed (throttle or signature)")
        codes = {n: r.get("code") for n, r in results.items()}
        if len(set(codes.values())) == 1:
            print(f"all variants returned {codes} -> the gate is not discriminating right now")
        else:
            print(f"codes differ: {codes} -> compare 'none' against the others")
    return 0


if __name__ == "__main__":
    sys.exit(main())
