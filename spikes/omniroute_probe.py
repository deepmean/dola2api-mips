"""Reproduce the public Dola-web client's request shape, and see whether it acked.

OmniRoute (github.com/diegosouzapw/OmniRoute) ships open-sse/executors/doubao-web.ts,
a maintained executor that posts to https://www.dola.com/chat/completion with no
a_bogus. Its shape differs from this port's submit in ways that could matter to the
risk-control layer:

  * query carries device_id / web_id / tea_uuid (a device identity) plus fp
  * pc_version 3.25.3, and real_aid
  * Accept: text/event-stream, Agw-Js-Conv: str  (not "str, str")
  * body has NO chat_ability field at all

If this acks where the port's shape does not, the difference is the finding.

    python spikes/omniroute_probe.py cookies.txt [--deep-think] [--out FILE]
"""

from __future__ import annotations

import argparse
import asyncio
import json
import pathlib
import re
import sys
import time
import uuid
from urllib.parse import urlencode

import aiohttp

RELAY = "https://vcrelay.dmean.dpdns.org/"
BASE = "https://www.dola.com"
BOT_ID = "7339470689562525703"
UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36")


def load_cookie(path: str) -> str:
    for line in pathlib.Path(path).read_text(encoding="utf-8-sig").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            return line
    raise SystemExit("no cookie line found")


def cookie_value(cookie: str, name: str) -> str:
    m = re.search(r"(?:^|;\s*)" + re.escape(name) + r"=([^;]*)", cookie)
    return m.group(1) if m else ""


def query(cookie: str) -> str:
    device_id = "7" + "".join(str(int(x)) for x in str(uuid.uuid4().int)[:18])
    return urlencode({
        "aid": "495671", "real_aid": "495671", "device_platform": "web",
        "device_id": device_id, "web_id": device_id, "tea_uuid": device_id,
        "web_tab_id": str(uuid.uuid4()), "pc_version": "3.25.3",
        "pkg_type": "release_version", "version_code": "20800",
        "samantha_web": "1", "web_platform": "browser", "use-olympus-account": "1",
        "language": "en", "region": "US", "sys_region": "US",
        "fp": cookie_value(cookie, "s_v_web_id"),
    })


def body(prompt: str, deep: bool) -> bytes:
    now = int(time.time() * 1000)
    return json.dumps({
        "client_meta": {"local_conversation_id": f"local_{now}", "conversation_id": "",
                        "bot_id": BOT_ID, "last_section_id": "", "last_message_index": None},
        "messages": [{
            "local_message_id": str(uuid.uuid4()),
            "content_block": [{
                "block_type": 10000,
                "content": {"text_block": {"text": prompt, "icon_url": "",
                                           "icon_url_dark": "", "summary": ""},
                            "pc_event_block": ""},
                "block_id": str(uuid.uuid4()), "parent_id": "",
                "meta_info": [], "append_fields": [],
            }],
            "message_status": 0,
        }],
        "option": {
            "send_message_scene": "", "create_time_ms": now, "collect_id": "", "is_audio": False,
            "answer_with_suggest": False, "tts_switch": False, "need_deep_think": 3 if deep else 0,
            "click_clear_context": False, "from_suggest": False, "is_regen": False,
            "is_replace": False, "is_from_click_option": False, "is_from_click_softlink": False,
            "disable_sse_cache": False, "select_text_action": "", "is_select_text": False,
            "resend_for_regen": False, "scene_type": 0, "unique_key": str(uuid.uuid4()),
            "start_seq": 0, "need_create_conversation": True,
            "conversation_init_option": {"need_ack_conversation": True},
            "regen_query_id": [], "edit_query_id": [], "regen_instruction": "",
            "no_replace_for_regen": False, "message_from": 0, "shared_app_name": "",
            "shared_app_id": "", "sse_recv_event_options": {"support_chunk_delta": True},
            "is_ai_playground": False, "is_old_user": False,
            "recovery_option": {"is_recovery": False, "req_create_time_sec": now // 1000,
                                "append_sse_event_scene": 0},
            "message_storage_type": 0,
        },
        "user_context": [],
        "ext": {"use_deep_think": "3" if deep else "0",
                "fp": cookie_value("", "x") or "",
                "sub_conv_firstmet_type": "1", "collection_id": "",
                "conversation_init_option": json.dumps({"need_ack_conversation": True}),
                "commerce_credit_config_enable": "0"},
    }, separators=(",", ":")).encode()


async def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("cookie_file")
    ap.add_argument("--deep-think", action="store_true")
    ap.add_argument("--prompt", default="Say hello in one short sentence.")
    ap.add_argument("--seconds", type=float, default=30.0)
    ap.add_argument("--out", default=None)
    args = ap.parse_args()

    cookie = load_cookie(args.cookie_file)
    fp = cookie_value(cookie, "s_v_web_id")
    payload = body(args.prompt, args.deep_think)
    # ext.fp is set from the cookie in the reference implementation.
    obj = json.loads(payload)
    obj["ext"]["fp"] = fp
    payload = json.dumps(obj, separators=(",", ":")).encode()

    headers = {
        "Content-Type": "application/json", "User-Agent": UA,
        "Accept": "text/event-stream", "Referer": BASE + "/chat/",
        "Origin": BASE, "Agw-Js-Conv": "str", "Cookie": cookie,
        "x-relay-target": BASE,
        "x-relay-path": "/chat/completion?" + query(cookie),
    }
    events: list[tuple[str, str]] = []
    conv = ""
    async with aiohttp.ClientSession() as session:
        async with session.post(RELAY, data=payload, headers=headers,
                                timeout=aiohttp.ClientTimeout(total=args.seconds + 15)) as resp:
            print("status", resp.status, "content-type", resp.headers.get("Content-Type"))
            buf = ""
            deadline = time.time() + args.seconds
            async for chunk in resp.content.iter_any():
                buf += chunk.decode("utf-8", "replace")
                while "\n\n" in buf:
                    raw, buf = buf.split("\n\n", 1)
                    name, data = "", []
                    for line in raw.split("\n"):
                        if line.startswith("event:"):
                            name = line[6:].strip()
                        elif line.startswith("data:"):
                            data.append(line[5:].strip())
                    d = "\n".join(data)
                    events.append((name, d))
                    if name == "SSE_ACK" and not conv:
                        try:
                            conv = json.loads(d)["ack_client_meta"]["conversation_id"]
                        except Exception:
                            pass
                if time.time() > deadline:
                    break
    print("conversation_id:", conv or "(none)")
    for name, data in events[:60]:
        print(f"  event={name!r} data={data[:600]}")
    print(f"total events: {len(events)}")
    if args.out:
        pathlib.Path(args.out).write_text(
            json.dumps({"conversation_id": conv,
                        "events": [{"event": n, "data": d} for n, d in events]},
                       ensure_ascii=False, indent=2), encoding="utf-8")
        print("wrote", args.out)
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
