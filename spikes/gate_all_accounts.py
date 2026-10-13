"""Try the submit gate once per account.

The gate check has only ever been run against the first account, but the refusal
is plausibly per-session rather than per-IP: a rate limit that one account has
hit says nothing about the others.

Sends the shape that is known to work when the gate is open: a real submit body
and NO a_bogus parameter.

Usage:
    python spikes/gate_all_accounts.py cookies.txt [more_cookies.txt ...]
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
from urllib.parse import urlencode

RELAY = "https://vcrelay.dmean.dpdns.org/"
CTX = ssl.create_default_context()


def fields(cookie: str) -> dict[str, str]:
    out: dict[str, str] = {}
    for part in cookie.split(";"):
        part = part.strip()
        if "=" in part:
            k, v = part.split("=", 1)
            out[k.strip()] = v.strip()
    return out


def submit_url(tab: str) -> str:
    params = {
        "aid": "495671", "channel": "g", "device_platform": "web",
        "language": "zh-Hant", "region": "JP", "sys_region": "JP",
        "samantha_web": "1", "use-olympus-account": "1", "version_code": "20800",
        "web_platform": "browser", "web_tab_id": tab,
    }
    return "/chat/completion?" + urlencode(params)


def submit_body(prompt: str) -> bytes:
    now = int(time.time() * 1000)
    body = {
        "client_meta": {"local_conversation_id": f"local_{now}", "conversation_id": "",
                        "bot_id": "7339470689562525703", "last_section_id": "",
                        "last_message_index": None},
        "messages": [{"local_message_id": str(uuid.uuid4()),
                      "content_block": [{"block_type": 10000,
                                         "content": {"text_block": {"text": prompt,
                                                                    "icon_url": "", "icon_url_dark": "",
                                                                    "summary": ""},
                                                     "pc_event_block": ""},
                                         "block_id": str(uuid.uuid4()), "parent_id": "",
                                         "meta_info": [], "append_fields": []}],
                      "message_status": 0}],
        "option": {"send_message_scene": "", "create_time_ms": now, "collect_id": "", "is_audio": False,
                   "answer_with_suggest": False, "tts_switch": False, "need_deep_think": 0,
                   "click_clear_context": False, "from_suggest": False, "is_regen": False,
                   "is_replace": False, "is_from_click_option": False, "disable_sse_cache": False,
                   "select_text_action": "", "is_select_text": False, "resend_for_regen": False,
                   "scene_type": 0, "unique_key": str(uuid.uuid4()), "start_seq": 0,
                   "need_create_conversation": True,
                   "conversation_init_option": {"need_ack_conversation": True},
                   "regen_query_id": [], "edit_query_id": [], "regen_instruction": "",
                   "no_replace_for_regen": False, "message_from": 0, "shared_app_name": "",
                   "shared_app_id": "", "sse_recv_event_options": {"support_chunk_delta": True},
                   "is_ai_playground": False, "is_old_user": False,
                   "recovery_option": {"is_recovery": False, "req_create_time_sec": now // 1000,
                                       "append_sse_event_scene": 0},
                   "message_storage_type": 0},
        "chat_ability": {"ability_type": 17,
                         "ability_param": json.dumps({"ratio": "9:16", "model": "seedance_v2.0",
                                                      "duration": 10})},
        "user_context": [],
        "ext": {"fp": "", "use_deep_think": "0", "sub_conv_firstmet_type": "1", "collection_id": "",
                "conversation_init_option": '{"need_ack_conversation":true}',
                "commerce_credit_config_enable": "0"},
    }
    return json.dumps(body, separators=(",", ":")).encode()


def attempt(cookie: str, with_bogus: bool) -> dict:
    tab = str(uuid.uuid4())
    path = submit_url(tab)
    if with_bogus:
        alphabet = "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe"
        path += "&a_bogus=" + "".join(random.choice(alphabet) for _ in range(182)) + "=="

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
    conn = http.client.HTTPSConnection("vcrelay.dmean.dpdns.org", 443, timeout=90, context=CTX)
    conn.request("POST", "/", body=submit_body("Generate video: a cat on a windowsill, 9:16"),
                 headers=headers)
    resp = conn.getresponse()
    text = resp.read().decode("utf-8", "replace")
    conn.close()

    code = None
    if m := __import__("re").search(r'"error_code":(\d+)', text):
        code = m.group(1)
    conv = ""
    if m := __import__("re").search(r'"conversation_id":"(\d+)"', text):
        conv = m.group(1)
    events = text.count("event:")
    return {"http": resp.status, "code": code, "conv": conv,
            "acked": "SSE_ACK" in text, "events": events}


def main() -> int:
    files = sys.argv[1:] or ["cookies.txt"]
    print("one real submit per account, NO a_bogus (the shape that worked before)\n")
    any_ok = False
    for path in files:
        try:
            cookie = [l.strip() for l in pathlib.Path(path).read_text(encoding="utf-8-sig").splitlines()
                      if l.strip() and not l.startswith("#")][0]
        except (OSError, IndexError) as exc:
            print(f"  {path}: cannot read ({exc})")
            continue
        session = fields(cookie).get("sessionid", "")[:8]
        try:
            result = attempt(cookie, with_bogus=False)
        except Exception as exc:  # noqa: BLE001
            print(f"  sessionid={session}...  transport error: {exc}")
            continue
        verdict = "ACCEPTED" if result["acked"] else "refused"
        print(f"  sessionid={session}...  {verdict}  http={result['http']} "
              f"code={result['code']} conv={result['conv'] or '-'} events={result['events']}")
        if result["acked"]:
            any_ok = True
        time.sleep(1.0)

    print()
    if any_ok:
        print("=> at least one account was accepted: the gate is open for that session")
    else:
        print("=> every account refused: the gate is closed for all of them right now")
    return 0 if any_ok else 1


if __name__ == "__main__":
    sys.exit(main())
