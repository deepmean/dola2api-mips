"""Send a chat turn with the parameter set captured from the working browser.

The browser answered normally while every port request was refused. Its request
carried twenty query parameters; the port sent eleven of them, plus a channel the
client does not send, plus a language the client leaves empty. This probe sends
exactly what the browser sent, to see whether that is the difference.

Usage:
    python spikes/parity_probe.py <cookie_file> [--prompt TEXT] [--seconds N]
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
from urllib.parse import urlencode

RELAY_HOST = "vcrelay.dmean.dpdns.org"
CTX = ssl.create_default_context()

# Captured from the browser's own request.
DEVICE = {
    "device_id": "7686671608228726325",
    "web_id": "7695956629460485685",
    "tea_uuid": "7695956629460485685",
    "fp": "verify_mv6q3fus_Oi5TeE8s_72in_4CGa_9k8g_RYDKyKyHA2UK",
    "tz_name": "Asia/Shanghai",
}


def client_query(tab: str) -> str:
    """Exactly the twenty parameters the browser sent, in its own key set."""
    return urlencode({
        "aid": "495671",
        "device_id": DEVICE["device_id"],
        "device_platform": "web",
        "doubao_device_platform": "web",
        "doubao_pc_version": "3.41.0",
        "fp": DEVICE["fp"],
        "language": "",
        "pc_version": "3.41.0",
        "pkg_type": "release_version",
        "real_aid": "495671",
        "region": "JP",
        "samantha_web": "1",
        "sys_region": "JP",
        "tea_uuid": DEVICE["tea_uuid"],
        "tz_name": DEVICE["tz_name"],
        "use-olympus-account": "1",
        "version_code": "20800",
        "web_id": DEVICE["web_id"],
        "web_platform": "browser",
        "web_tab_id": tab,
    })


def port_query(tab: str, cookie: str) -> str:
    """What the port sent before this change, for comparison."""
    params = {
        "aid": "495671", "channel": "g", "device_platform": "web",
        "language": "zh-Hant", "region": "JP", "sys_region": "JP",
        "samantha_web": "1", "use-olympus-account": "1", "version_code": "20800",
        "web_platform": "browser", "web_tab_id": tab,
    }
    fields = {}
    for part in cookie.split(";"):
        if "=" in part:
            k, v = part.split("=", 1)
            fields[k.strip()] = v.strip()
    if fields.get("msToken"):
        params["msToken"] = fields["msToken"]
    if fields.get("s_v_web_id"):
        params["fp"] = fields["s_v_web_id"]
    return urlencode(params)


def chat_body(prompt: str) -> bytes:
    now = int(time.time() * 1000)
    return json.dumps({
        "client_meta": {"local_conversation_id": f"local_{now}", "conversation_id": "",
                        "bot_id": "7339470689562525703", "last_section_id": "",
                        "last_message_index": None},
        "messages": [{"local_message_id": str(uuid.uuid4()),
                      "content_block": [{"block_type": 10000,
                                         "content": {"text_block": {"text": prompt, "icon_url": "",
                                                                    "icon_url_dark": "", "summary": ""},
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
        "user_context": [],
        "ext": {"fp": DEVICE["fp"], "use_deep_think": "0", "sub_conv_firstmet_type": "1",
                "collection_id": "", "conversation_init_option": '{"need_ack_conversation":true}',
                "commerce_credit_config_enable": "0"},
    }, separators=(",", ":")).encode()


def send(cookie: str, query: str, prompt: str, direct: bool = False) -> dict:
    headers = {
        "Content-Type": "application/json", "agw-js-conv": "str, str", "Accept": "*/*",
        "Cookie": cookie,
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    }
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

    print("the client's parameter set:")
    print(" ", send(cookie, client_query(str(uuid.uuid4())), args.prompt, args.direct))
    print("the port's previous parameter set:")
    print(" ", send(cookie, port_query(str(uuid.uuid4()), cookie), args.prompt, args.direct))
    return 0


if __name__ == "__main__":
    sys.exit(main())
