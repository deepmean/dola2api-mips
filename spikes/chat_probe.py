"""Probe: what does the text-chat path look like on the wire?

Read-only research instrument. It submits ONE chat request (ability_type 0, no
a_bogus) through the relay, records every SSE event, then polls the conversation
with /im/chain/single and dumps the block shape.

    python spikes/chat_probe.py cookies.txt [--ability N] [--param JSON]

Exit code 0 always; the point is the dump.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import pathlib
import sys
import time
import uuid
from urllib.parse import urlencode

import aiohttp

RELAY = "https://vcrelay.dmean.dpdns.org/"
BASE = "https://www.dola.com"
BOT_ID = "7339470689562525703"


def load_cookie(path: str) -> str:
    for line in pathlib.Path(path).read_text(encoding="utf-8-sig").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            return line
    raise SystemExit("no cookie line found")


def query(tab: str, cookie: str = "", extras: str = "") -> str:
    import re
    params = {
        "aid": "495671", "channel": "g", "device_platform": "web",
        "language": "zh-Hant", "region": "JP", "sys_region": "JP",
        "samantha_web": "1", "use-olympus-account": "1", "version_code": "20800",
        "web_platform": "browser", "web_tab_id": tab,
    }
    if "ms" in extras:
        m = re.search(r"msToken=([^;]+)", cookie)
        if m:
            params["msToken"] = m.group(1)
        f = re.search(r"s_v_web_id=([^;]+)", cookie)
        if f:
            params["fp"] = f.group(1)
    if "bogus" in extras:
        import random
        alphabet = "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe"
        params["a_bogus"] = "".join(random.choice(alphabet) for _ in range(186)) + "=="
    return urlencode(params)


def submit_body(prompt: str, ability: int, param: str | None) -> bytes:
    now = int(time.time() * 1000)
    chat_ability: dict = {"ability_type": ability}
    if param is not None:
        chat_ability["ability_param"] = param
    body = {
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
            "message_storage_type": 0,
        },
        "chat_ability": chat_ability,
        "user_context": [],
        "ext": {"fp": "", "use_deep_think": "0", "sub_conv_firstmet_type": "1",
                "collection_id": "", "conversation_init_option": '{"need_ack_conversation":true}',
                "commerce_credit_config_enable": "0"},
    }
    return json.dumps(body, separators=(",", ":")).encode()


async def submit(session: aiohttp.ClientSession, cookie: str, tab: str,
                 prompt: str, ability: int, param: str | None, seconds: float,
                 extras: str = "", full: bool = False) -> dict:
    headers = {
        "Content-Type": "application/json", "agw-js-conv": "str, str", "Accept": "*/*",
        "Cookie": cookie, "Origin": BASE, "Referer": BASE + "/chat/",
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
        "x-relay-target": BASE, "x-relay-path": "/chat/completion?" + query(tab, cookie, extras),
    }
    if full:
        headers.update({
            "Accept-Encoding": "identity",
            "Accept-Language": "en,ja;q=0.9,en-US;q=0.8,en;q=0.7",
            "Cache-Control": "no-cache",
            "Pragma": "no-cache",
            "Sec-Ch-Ua": '"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"',
            "Sec-Ch-Ua-Mobile": "?0",
            "Sec-Ch-Ua-Platform": '"Windows"',
            "Sec-Fetch-Dest": "empty",
            "Sec-Fetch-Mode": "cors",
            "Sec-Fetch-Site": "same-origin",
        })
    body = submit_body(prompt, ability, param)
    events: list[tuple[str, str]] = []
    conv = ""
    deadline = time.time() + seconds
    out = {"status": None, "events": events, "conversation_id": conv, "raw_tail": ""}
    try:
        async with session.post(RELAY, data=body, headers=headers,
                                timeout=aiohttp.ClientTimeout(total=seconds + 10)) as resp:
            out["status"] = resp.status
            buf = ""
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
                    if name in ("STREAM_ERROR",):
                        out["raw_tail"] = d[:600]
                if time.time() > deadline:
                    break
                if conv and len(events) > 200:
                    break
    except Exception as exc:  # noqa: BLE001
        out["error"] = repr(exc)
    out["conversation_id"] = conv
    return out


async def poll(session: aiohttp.ClientSession, cookie: str, tab: str, conv: str) -> dict:
    payload = {
        "cmd": 3100,
        "uplink_body": {"pull_singe_chain_uplink_body": {
            "conversation_id": conv, "anchor_index": 9007199254740991,
            "conversation_type": 3, "direction": 1, "limit": 20,
            "ext": {}, "filter": {"index_list": []},
            "evaluate_ab_params": "", "evaluate_common_params": ""}},
        "sequence_id": str(uuid.uuid4()), "channel": 2, "version": "1",
    }
    headers = {
        "Content-Type": "application/json; encoding=utf-8", "agw-js-conv": "str",
        "Accept": "*/*", "Cookie": cookie, "Origin": BASE,
        "Referer": f"{BASE}/chat/{conv}",
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
        "x-relay-target": BASE, "x-relay-path": "/im/chain/single?" + query(tab),
    }
    async with session.post(RELAY, data=json.dumps(payload).encode(), headers=headers,
                            timeout=aiohttp.ClientTimeout(total=30)) as resp:
        text = await resp.text()
    return {"status": resp.status, "body": text}


def summarize_poll(raw: str) -> dict:
    try:
        data = json.loads(raw)
    except Exception:
        return {"parse": "failed", "head": raw[:300]}
    dl = (data.get("downlink_body") or {}).get("pull_singe_chain_downlink_body") or {}
    msgs = dl.get("messages") or []
    out = {"status_code": data.get("status_code"), "code": data.get("code"),
           "msg": data.get("msg"), "messages": len(msgs), "blocks": []}
    for msg in msgs:
        content = msg.get("content")
        if isinstance(content, str):
            try:
                content = json.loads(content)
            except Exception:
                out["blocks"].append({"unparsed": content[:200]})
                continue
        if not isinstance(content, list):
            out["blocks"].append({"nonlist": str(content)[:120], "user_type": msg.get("user_type")})
            continue
        for block in content:
            entry = {"block_type": block.get("block_type"),
                     "keys": sorted((block.get("content") or {}).keys()),
                     "user_type": msg.get("user_type")}
            c = block.get("content") or {}
            tb = c.get("text_block") or {}
            if tb.get("text"):
                entry["text"] = tb["text"][:400]
            cb = (c.get("creation_block") or {}).get("creations")
            if cb:
                entry["creations"] = [{"type": cr.get("type"),
                                       "keys": sorted(cr.keys()),
                                       "image_keys": sorted((cr.get("image") or {}).keys()),
                                       "video_keys": sorted((cr.get("video") or {}).keys()),
                                       "raw": json.dumps(cr)[:800]} for cr in cb]
            out["blocks"].append(entry)
    return out


async def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("cookie_file")
    ap.add_argument("--ability", type=int, default=0)
    ap.add_argument("--param", default=None)
    ap.add_argument("--prompt", default="用一句话介绍你自己")
    ap.add_argument("--seconds", type=float, default=45.0)
    ap.add_argument("--extras", default="", help="comma list: ms,bogus")
    ap.add_argument("--full-headers", action="store_true")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()

    cookie = load_cookie(args.cookie_file)
    tab = str(uuid.uuid4())
    async with aiohttp.ClientSession() as session:
        sub = await submit(session, cookie, tab, args.prompt, args.ability,
                           args.param, args.seconds, args.extras, args.full_headers)
        print("=== SUBMIT")
        print("status:", sub.get("status"), "conv:", sub.get("conversation_id"))
        for name, data in sub["events"][:40]:
            print(f"  event={name!r} data={data[:500]}")
        if sub.get("error"):
            print("  error:", sub["error"])
        if len(sub["events"]) > 40:
            print(f"  ... {len(sub['events'])} events total")

        result = {"submit": sub}
        conv = sub.get("conversation_id")
        if conv:
            for wait in (3, 6, 10):
                await asyncio.sleep(wait)
                p = await poll(session, cookie, tab, conv)
                summ = summarize_poll(p["body"])
                result[f"poll_{wait}"] = summ
                print(f"=== POLL after {wait}s: status={p['status']} messages={summ.get('messages')}")
                print(json.dumps(summ, ensure_ascii=False, indent=2)[:4000])
                if summ.get("blocks"):
                    break
        if args.out:
            pathlib.Path(args.out).write_text(
                json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
            print("wrote", args.out)
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
