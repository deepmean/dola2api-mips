"""Read-only: dump the block/creation shapes of the account's own conversations.

The poll path (/im/chain/single, /im/chain/recent_conv) is NOT on the mssdk's
signed path list, so this needs no signature and no generation quota. It lists
recent conversations, then dumps every content block of each one: block_type,
the keys inside creation_block.creations[], and the URL-bearing fields.

That is how the image-creation shape can be read off a real response without
submitting anything.

    python spikes/poll_dump.py cookies.txt [--limit 20] [--out spikes/poll_dump.json]
"""

from __future__ import annotations

import argparse
import asyncio
import json
import pathlib
import sys
import uuid
from urllib.parse import urlencode

import aiohttp

RELAY = "https://vcrelay.dmean.dpdns.org/"
BASE = "https://www.dola.com"
SAMPLE = 1500
UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")


def load_cookie(path: str) -> str:
    for line in pathlib.Path(path).read_text(encoding="utf-8-sig").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            return line
    raise SystemExit("no cookie line found")


def query(tab: str, cookie: str) -> str:
    import re
    params = {
        "version_code": "20800", "language": "ja", "device_platform": "web",
        "doubao_device_platform": "web", "aid": "495671", "real_aid": "495671",
        "pkg_type": "release_version", "pc_version": "3.32.61",
        "doubao_pc_version": "3.32.61", "region": "JP", "sys_region": "JP",
        "samantha_web": "1", "web_platform": "browser", "use-olympus-account": "1",
        "web_tab_id": tab,
    }
    m = re.search(r"msToken=([^;]+)", cookie)
    if m:
        params["msToken"] = m.group(1)
    f = re.search(r"s_v_web_id=([^;]+)", cookie)
    if f:
        params["fp"] = f.group(1)
    return urlencode(params)


async def post(session, cookie, tab, path, payload):
    headers = {
        "Content-Type": "application/json; encoding=utf-8", "agw-js-conv": "str",
        "Accept": "*/*", "Cookie": cookie, "Origin": BASE, "Referer": BASE + "/chat/",
        "User-Agent": UA, "x-relay-target": BASE,
        "x-relay-path": path + "?" + query(tab, cookie),
    }
    async with session.post(RELAY, data=json.dumps(payload).encode(), headers=headers,
                            timeout=aiohttp.ClientTimeout(total=30)) as resp:
        return resp.status, await resp.text()


async def recent(session, cookie, tab, limit):
    payload = {
        "cmd": 3200,
        "uplink_body": {"pull_recent_conv_chain_uplink_body": {
            "limit": limit, "message_count_per_conv": 1, "api_version": 1,
            "conv_version": 0, "direction": 3,
            "option": {"not_need_message": True, "need_complete_conversation": True,
                       "need_coco_conversation": True, "need_coco_bot": True,
                       "need_pc_pin_chain": True, "pc_pin_query_type": 0}}},
        "sequence_id": str(uuid.uuid4()), "channel": 2, "version": "1",
    }
    return await post(session, cookie, tab, "/im/chain/recent_conv", payload)


async def chain(session, cookie, tab, conv, limit=20):
    payload = {
        "cmd": 3100,
        "uplink_body": {"pull_singe_chain_uplink_body": {
            "conversation_id": conv, "anchor_index": 9007199254740991,
            "conversation_type": 3, "direction": 1, "limit": limit,
            "ext": {}, "filter": {"index_list": []},
            "evaluate_ab_params": "", "evaluate_common_params": ""}},
        "sequence_id": str(uuid.uuid4()), "channel": 2, "version": "1",
    }
    return await post(session, cookie, tab, "/im/chain/single", payload)


def block_shape(block: dict) -> dict:
    c = block.get("content") or {}
    entry = {"block_type": block.get("block_type"), "content_keys": sorted(c.keys())}
    tb = c.get("text_block") or {}
    if tb.get("text"):
        entry["text"] = tb["text"][:300]
    cb = (c.get("creation_block") or {})
    if cb.get("creations"):
        entry["creations"] = []
        for cr in cb["creations"]:
            img = cr.get("image") or {}
            vid = cr.get("video") or {}
            entry["creations"].append({
                "type": cr.get("type"),
                "keys": sorted(cr.keys()),
                "image_keys": sorted(img.keys()),
                "image_raw_keys": sorted((img.get("image_raw") or {}).keys()),
                "image_ori_keys": sorted((img.get("image_ori") or {}).keys()),
                "video_keys": sorted(vid.keys()),
                "status": img.get("status"),
                "tips": img.get("tips"),
                "sample": json.dumps(cr, ensure_ascii=False)[:SAMPLE],
            })
    return entry


def summarize(raw: str) -> dict:
    try:
        data = json.loads(raw)
    except Exception:
        return {"parse": "failed", "head": raw[:300]}
    dl = (data.get("downlink_body") or {})
    chain = dl.get("pull_singe_chain_downlink_body") or {}
    msgs = chain.get("messages") or []
    out = {"status_code": data.get("status_code"), "code": data.get("code"),
           "msg": data.get("msg"), "message_count": len(msgs), "blocks": [],
           "raw_keys": sorted(dl.keys())}
    for msg in msgs:
        content = msg.get("content")
        if isinstance(content, str):
            try:
                content = json.loads(content)
            except Exception:
                out["blocks"].append({"unparsed": content[:200]})
                continue
        if not isinstance(content, list):
            out["blocks"].append({"nonlist": str(content)[:150], "user_type": msg.get("user_type")})
            continue
        for block in content:
            e = block_shape(block)
            e["user_type"] = msg.get("user_type")
            e["msg_ext"] = {k: v for k, v in (msg.get("ext") or {}).items()
                            if k in ("chat_ability", "skill", "input_skill", "action_bar_skill_id")}
            out["blocks"].append(e)
    return out


async def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("cookie_file")
    ap.add_argument("--limit", type=int, default=20)
    ap.add_argument("--conv", default=None, help="poll this conversation only")
    ap.add_argument("--sample", type=int, default=1500, help="chars of raw JSON per creation")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()
    globals()["SAMPLE"] = args.sample

    cookie = load_cookie(args.cookie_file)
    tab = str(uuid.uuid4())
    result: dict = {"conversations": [], "chains": {}}
    async with aiohttp.ClientSession() as session:
        if args.conv:
            convs = [args.conv]
        else:
            status, text = await recent(session, cookie, tab, args.limit)
            print("recent_conv status", status)
            try:
                data = json.loads(text)
            except Exception:
                print(text[:500])
                return 1
            dl = (data.get("downlink_body") or {}).get("pull_recent_conv_chain_downlink_body") or {}
            cells = dl.get("cells") or dl.get("conversations") or []
            result["recent_keys"] = sorted(dl.keys())
            for cell in cells:
                conv = (cell.get("conversation") or {}).get("conversation_id") or cell.get("conversation_id")
                name = (cell.get("conversation") or {}).get("name") or cell.get("name") or ""
                bot = (cell.get("conversation") or {}).get("bot_id")
                result["conversations"].append({"id": conv, "name": name, "bot_id": bot})
                print(f"  conv={conv} name={name!r} bot={bot}")
            convs = [c["id"] for c in result["conversations"] if c["id"]]
        print(f"\ndumping {len(convs)} conversation(s)")
        for conv in convs:
            status, text = await chain(session, cookie, tab, conv)
            summ = summarize(text)
            result["chains"][conv] = summ
            print(f"\n=== conv {conv} status={status} messages={summ.get('message_count')} "
                  f"downlink_keys={summ.get('raw_keys')}")
            print(json.dumps(summ, ensure_ascii=False, indent=2)[:6000])
            await asyncio.sleep(0.4)
    if args.out:
        pathlib.Path(args.out).write_text(json.dumps(result, ensure_ascii=False, indent=2),
                                          encoding="utf-8")
        print("\nwrote", args.out)
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
