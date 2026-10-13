"""Poll one conversation with each stored account, to see which one owns it.

A conversation belongs to the session that created it, so polling with the wrong
account is indistinguishable from the conversation not existing. That is the
first thing to rule out when a task stays "running" while a direct poll returns
almost nothing.

Usage:
    python spikes/poll_both.py <conversation_id> [cookie_file ...]
"""

from __future__ import annotations

import http.client
import json
import pathlib
import ssl
import sys
import uuid
from urllib.parse import urlencode

RELAY = "https://vcrelay.dmean.dpdns.org/"


def query(cookie: str) -> str:
    fields: dict[str, str] = {}
    for part in cookie.split(";"):
        part = part.strip()
        if "=" in part:
            k, v = part.split("=", 1)
            fields[k.strip()] = v.strip()
    params = {
        "version_code": "20800", "language": "ja", "device_platform": "web",
        "doubao_device_platform": "web", "aid": "495671", "real_aid": "495671",
        "pkg_type": "release_version", "pc_version": "3.32.61",
        "doubao_pc_version": "3.32.61", "region": "JP", "sys_region": "JP",
        "samantha_web": "1", "web_platform": "browser", "use-olympus-account": "1",
        "web_tab_id": str(uuid.uuid4()),
    }
    if fields.get("msToken"):
        params["msToken"] = fields["msToken"]
    if fields.get("s_v_web_id"):
        params["fp"] = fields["s_v_web_id"]
    return urlencode(params)


def poll(cookie: str, conversation: str) -> tuple[int, str]:
    body = {
        "cmd": 3100,
        "uplink_body": {"pull_singe_chain_uplink_body": {
            "conversation_id": conversation,
            "anchor_index": 9007199254740991,
            "conversation_type": 3, "direction": 1, "limit": 20,
            "ext": {}, "filter": {"index_list": []},
            "evaluate_ab_params": "", "evaluate_common_params": "",
        }},
        "sequence_id": str(uuid.uuid4()), "channel": 2, "version": "1",
    }
    headers = {
        "Cookie": cookie,
        "Content-Type": "application/json; encoding=utf-8",
        "agw-js-conv": "str",
        "User-Agent": "Mozilla/5.0",
        "x-relay-target": "https://www.dola.com",
        "x-relay-path": "/im/chain/single?" + query(cookie),
    }
    conn = http.client.HTTPSConnection("vcrelay.dmean.dpdns.org", 443, timeout=45,
                                       context=ssl.create_default_context())
    conn.request("POST", "/", body=json.dumps(body, separators=(",", ":")).encode(), headers=headers)
    resp = conn.getresponse()
    text = resp.read().decode("utf-8", "replace")
    conn.close()
    return resp.status, text


def summarise(text: str) -> str:
    try:
        data = json.loads(text)
    except json.JSONDecodeError:
        return text[:160]
    bits = {k: data[k] for k in ("status_code", "status_desc", "code", "msg") if k in data}
    chain = ((data.get("downlink_body") or {}).get("pull_singe_chain_downlink_body") or {})
    if chain:
        messages = chain.get("messages") or []
        bits["messages"] = len(messages)
        block_types: set[int] = set()
        creation_types: set[int] = set()
        for msg in messages:
            content = msg.get("content")
            if isinstance(content, str):
                try:
                    content = json.loads(content)
                except json.JSONDecodeError:
                    continue
            if not isinstance(content, list):
                continue
            for block in content:
                block_types.add(block.get("block_type"))
                for creation in (((block.get("content") or {}).get("creation_block") or {})
                                 .get("creations") or []):
                    creation_types.add(creation.get("type"))
        if block_types:
            bits["block_types"] = sorted(x for x in block_types if x is not None)
        if creation_types:
            bits["creation_types"] = sorted(x for x in creation_types if x is not None)
    return json.dumps(bits, ensure_ascii=False)


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    conversation = sys.argv[1]
    files = sys.argv[2:] or ["cookies.txt"]

    for path in files:
        try:
            cookie = [l.strip() for l in pathlib.Path(path).read_text(encoding="utf-8-sig").splitlines()
                      if l.strip() and not l.startswith("#")][0]
        except (OSError, IndexError) as exc:
            print(f"  {path}: cannot read ({exc})")
            continue
        session = ""
        for part in cookie.split(";"):
            if "sessionid=" in part:
                session = part.split("=", 1)[1][:8]
        try:
            status, text = poll(cookie, conversation)
        except Exception as exc:  # noqa: BLE001 - every failure mode is data
            print(f"  {path} (sessionid={session}...): transport error {exc}")
            continue
        print(f"  {path} (sessionid={session}...): http {status}, {len(text)} bytes")
        print(f"      {summarise(text)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
