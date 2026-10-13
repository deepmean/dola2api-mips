"""Implement X-Bogus and test whether a_bogus is actually required.

X-Bogus is fully identified and verified against the live SDK oracle:

    u[0] = 63 & bogusIndex           u[1..2] = envcode (big endian)
    u[3] = 14 (ubcode)               u[4],u[5] = hexdecode(md5(hexdecode(md5(""))))[14:16]
    u[6],u[7] = hexdecode(md5(hexdecode(stub)))[14:16]      (stub "" -> 32 zeros)
    u[8] = random
    payload = u + xor(u)             -> 10 bytes
    rc4 key = one random byte; the output starts with
        [ kWebsocket<<6 | rand<<4 , keyByte ]  then the rc4 of the payload
    custom-base64 with alphabet Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe=

The open question this script answers: every request the page makes also carries
an `a_bogus` query param (~180 chars) that frontierSign does NOT produce. If a
valid X-Bogus header alone satisfies the risk check, the port does not need
a_bogus at all.

Usage:
    python spikes/xbogus_test.py --cookie-file cookies.txt --check      # self-check
    python spikes/xbogus_test.py --cookie-file cookies.txt --submit     # live test
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import random
import ssl
import sys
import time
import uuid
import http.client
from urllib.parse import urlencode, urlsplit

ALPHABET = "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe="
_B64MAP = {c: i for i, c in enumerate(ALPHABET[:64])}
_HEXMAP = {c: i for i, c in enumerate("0123456789abcdef")}
_HEXMAP.update({c: i for i, c in enumerate("0123456789ABCDEF")})

K_WEBSOCKET = 1
UBCODE = 14
ENVCODE = 1  # a clean client: none of the automation flags set

CTX = ssl.create_default_context()


def hex_decode(s: str) -> bytes:
    out = bytearray()
    for i in range(len(s) >> 1):
        out.append(((_HEXMAP.get(s[2 * i], 0) << 4) | _HEXMAP.get(s[2 * i + 1], 0)) & 0xFF)
    return bytes(out)


def md5_hex(b: bytes) -> str:
    return hashlib.md5(b).hexdigest()


def rc4(key: bytes, data: bytes) -> bytes:
    S = list(range(256))
    j = 0
    for i in range(256):
        j = (j + S[i] + key[i % len(key)]) % 256
        S[i], S[j] = S[j], S[i]
    out = bytearray()
    i = j = 0
    for b in data:
        i = (i + 1) % 256
        j = (j + S[i]) % 256
        S[i], S[j] = S[j], S[i]
        out.append(b ^ S[(S[i] + S[j]) % 256])
    return bytes(out)


def b64_encode(data: bytes) -> str:
    out = []
    i = 0
    while i + 3 <= len(data):
        v = (data[i] << 16) | (data[i + 1] << 8) | data[i + 2]
        i += 3
        out += [ALPHABET[(v >> 18) & 0x3F], ALPHABET[(v >> 12) & 0x3F],
                ALPHABET[(v >> 6) & 0x3F], ALPHABET[v & 0x3F]]
    rem = len(data) - i
    if rem:
        v = (data[i] << 16) | ((data[i + 1] << 8) if rem > 1 else 0)
        out.append(ALPHABET[(v >> 18) & 0x3F])
        out.append(ALPHABET[(v >> 12) & 0x3F])
        out.append(ALPHABET[(v >> 6) & 0x3F] if rem > 1 else "=")
        out.append("=")
    return "".join(out)


def x_bogus(stub: str, bogus_index: int = 1) -> str:
    u = bytearray(9)
    u[0] = 63 & bogus_index
    u[1] = (ENVCODE >> 8) & 0xFF
    u[2] = ENVCODE & 0xFF
    u[3] = UBCODE
    const = hex_decode(md5_hex(hex_decode(md5_hex(b""))))
    u[4], u[5] = const[14], const[15]
    effective = stub if stub else "0" * 32
    f = hex_decode(md5_hex(hex_decode(effective)))
    u[6], u[7] = f[14], f[15]
    u[8] = random.randrange(256)

    chk = 0
    for b in u:
        chk ^= b
    payload = bytes(u) + bytes([chk])

    key = random.randrange(256)
    enc = rc4(bytes([key]), payload)
    s = (K_WEBSOCKET << 6) | (random.randrange(2) << 4)
    return b64_encode(bytes([s, key]) + enc)


def dissect(bogus: str) -> dict:
    raw = bytes()
    bits = nbits = 0
    for ch in bogus:
        if ch == "=":
            break
        v = _B64MAP.get(ch)
        if v is None:
            continue
        bits = (bits << 6) | v
        nbits += 6
        if nbits >= 8:
            nbits -= 8
            raw += bytes([(bits >> nbits) & 0xFF])
    payload = rc4(bytes([raw[1]]), raw[2:])
    return {"u": list(payload[:9]), "checksum_ok": (lambda: __import__("functools").reduce(
        lambda a, b: a ^ b, payload[:9], 0) == payload[9])()}


def build_submit(ratio: str, duration: int) -> dict:
    now_ms = int(time.time() * 1000)
    return {
        "client_meta": {"local_conversation_id": f"local_{now_ms}", "conversation_id": "",
                        "bot_id": "7339470689562525703", "last_section_id": "", "last_message_index": None},
        "messages": [{"local_message_id": str(uuid.uuid4()),
                      "content_block": [{"block_type": 10000,
                                         "content": {"text_block": {"text": f"Generate video: a cat, {ratio}",
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
                         "ability_param": json.dumps({"ratio": ratio, "model": "seedance_v2.0", "duration": duration})},
        "user_context": [],
        "ext": {"fp": "", "use_deep_think": "0", "sub_conv_firstmet_type": "1", "collection_id": "",
                "conversation_init_option": '{"need_ack_conversation":true}',
                "commerce_credit_config_enable": "0"},
    }


def parse_sse(text: str) -> dict:
    conv, events, err = "", [], None
    for raw in text.split("\n\n"):
        if not raw.strip():
            continue
        name, data = "", []
        for line in raw.split("\n"):
            if line.startswith("event:"):
                name = line[6:].strip()
            elif line.startswith("data:"):
                data.append(line[5:].strip())
        payload = "\n".join(data)
        events.append(name)
        if name == "SSE_ACK":
            try:
                conv = (json.loads(payload).get("ack_client_meta") or {}).get("conversation_id") or ""
            except json.JSONDecodeError:
                pass
        if name == "STREAM_ERROR":
            try:
                p = json.loads(payload)
                err = {"code": p.get("error_code"), "msg": p.get("error_msg")}
            except json.JSONDecodeError:
                err = {"raw": payload[:160]}
    return {"conv": conv, "events": events, "error": err}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cookie-file", default="cookies.txt")
    ap.add_argument("--check", action="store_true", help="self-check the implementation")
    ap.add_argument("--submit", action="store_true", help="send a real submit with the X-Bogus header")
    ap.add_argument("--no-a-bogus", action="store_true", default=True)
    ap.add_argument("--ratio", default="9:16")
    ap.add_argument("--duration", type=int, default=10)
    ap.add_argument("--timeout", type=float, default=30.0)
    ap.add_argument("--relay", default="https://vcrelay.dmean.dpdns.org/",
                    help="forward through this relay (required: direct is country-restricted)")
    ap.add_argument("--direct", action="store_true", help="skip the relay (expect 710022003)")
    args = ap.parse_args()

    if args.check:
        for stub in ["", "a", "abc", "aid=495671&channel=g"]:
            b = x_bogus(stub, bogus_index=random.randrange(1, 60))
            d = dissect(b)
            print(f"stub {stub!r:<28} -> {b}  u={d['u']} checksum_ok={d['checksum_ok']}")
        print("\nExpected u[3]==14, u[1..2]==0,1, and a valid checksum for every line.")
        return 0

    if not args.submit:
        print("nothing to do: pass --check or --submit")
        return 2

    cookie = [ln.strip() for ln in pathlib.Path(args.cookie_file).read_text(encoding="utf-8-sig").splitlines()
              if ln.strip() and not ln.strip().startswith("#")][0]

    # Params mirror the current web client (pc_version 3.39.1, device_id present).
    params = {
        "aid": "495671", "channel": "g", "device_platform": "web", "doubao_device_platform": "web",
        "real_aid": "495671", "pkg_type": "release_version", "device_id": "7686671608228726325",
        "pc_version": "3.39.1", "doubao_pc_version": "3.39.1", "language": "zh-Hant",
        "region": "JP", "sys_region": "JP", "samantha_web": "1", "web_platform": "browser",
        "use-olympus-account": "1", "version_code": "20800", "web_tab_id": str(uuid.uuid4()),
    }
    cookie_map = dict(p.split("=", 1) for p in cookie.split(";") if "=" in p)
    cookie_map = {k.strip(): v.strip() for k, v in cookie_map.items()}
    if cookie_map.get("msToken"):
        params["msToken"] = cookie_map["msToken"]
    if cookie_map.get("s_v_web_id"):
        params["fp"] = cookie_map["s_v_web_id"]

    query = urlencode(params)
    xb = x_bogus(query, bogus_index=random.randrange(1, 60))
    print(f"query       : {query[:90]}...")
    print(f"X-Bogus     : {xb}")
    print(f"a_bogus     : (omitted on purpose)")

    body = build_submit(args.ratio, args.duration)
    body["ext"]["fp"] = cookie_map.get("s_v_web_id", "")
    headers = {
        "Content-Type": "application/json",
        "agw-js-conv": "str, str",
        "Accept": "*/*",
        "Cookie": cookie,
        "X-Bogus": xb,
        "Referer": "https://www.dola.com/chat/",
        "Origin": "https://www.dola.com",
    }
    payload = json.dumps(body, separators=(",", ":")).encode()

    if args.direct:
        endpoint = "https://www.dola.com"
        path = "/chat/completion?" + query
    else:
        endpoint = args.relay
        headers["x-relay-target"] = "https://www.dola.com"
        headers["x-relay-path"] = "/chat/completion?" + query
        path = urlsplit(endpoint).path or "/"

    u = urlsplit(endpoint)
    conn = http.client.HTTPSConnection(u.hostname, u.port or 443, timeout=args.timeout, context=CTX)
    conn.request("POST", path, body=payload, headers=headers)
    resp = conn.getresponse()
    text = resp.read().decode("utf-8", "replace")
    conn.close()

    info = parse_sse(text)
    result = {"status": resp.status, "agw_login": resp.getheader("X-Tt-Agw-Login"),
              "conv": info["conv"], "events": info["events"][:6], "error": info["error"],
              "body_head": text[:200]}
    print()
    print(json.dumps(result, ensure_ascii=False, indent=2))

    if info["conv"]:
        print("\n=> a_bogus is NOT required: X-Bogus alone passed")
    elif info["error"] and str(info["error"].get("code")) == "710022004":
        print("\n=> still risk-controlled: a_bogus (or more context) is required")
    else:
        print("\n=> different failure; inspect the body")
    return 0


if __name__ == "__main__":
    sys.exit(main())
