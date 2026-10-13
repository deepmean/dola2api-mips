"""Does the poll path work outside the browser?

This is what decides how much of the work the router can keep in a
browser-in-the-loop design. The mssdk the page loads lists its signed paths as
/alice, /samantha, /passport and /chat/completion -- /im/chain/single is NOT
among them, so polling may need no signature at all.

Polls a conversation the browser created and reports what comes back, including
whether a video URL appears (block_type 2074, creations[].type == 2,
video.download_url) and the unwatermarked URL when present.

Usage:
    python spikes/poll_check.py --cookie-file cookies.txt --conversation-id 38418263229243665
    python spikes/poll_check.py --cookie-file cookies.txt --conversation-id X --egress relay --once
"""

from __future__ import annotations

import argparse
import base64
import json
import pathlib
import ssl
import sys
import time
import uuid

import http.client
from urllib.parse import urlencode, urlsplit

API_HOST = "www.dola.com"
RELAY_DEFAULT = "https://vcrelay.dmean.dpdns.org/"
CTX = ssl.create_default_context()


def parse_cookie(raw: str) -> dict[str, str]:
    out = {}
    for part in raw.split(";"):
        part = part.strip()
        if part and "=" in part:
            k, v = part.split("=", 1)
            out[k.strip()] = v.strip()
    return out


def build_query(fields: dict[str, str]) -> str:
    """The richer param set the page uses for polling (video_worker.POLL_JS)."""
    params = {
        "version_code": "20800", "language": "ja", "device_platform": "web",
        "doubao_device_platform": "web", "aid": "495671", "real_aid": "495671",
        "pkg_type": "release_version", "pc_version": "3.32.61", "doubao_pc_version": "3.32.61",
        "region": "JP", "sys_region": "JP", "samantha_web": "1", "web_platform": "browser",
        "use-olympus-account": "1", "web_tab_id": str(uuid.uuid4()),
    }
    if fields.get("msToken"):
        params["msToken"] = fields["msToken"]
    if fields.get("s_v_web_id"):
        params["fp"] = fields["s_v_web_id"]
    return urlencode(params)


def build_body(conv_id: str) -> bytes:
    payload = {
        "cmd": 3100,
        "uplink_body": {"pull_singe_chain_uplink_body": {
            "conversation_id": conv_id,
            "anchor_index": 9007199254740991,  # Number.MAX_SAFE_INTEGER, as the page sends
            "conversation_type": 3,
            "direction": 1,
            "limit": 20,
            "ext": {},
            "filter": {"index_list": []},
            "evaluate_ab_params": "",
            "evaluate_common_params": "",
        }},
        "sequence_id": str(uuid.uuid4()),
        "channel": 2,
        "version": "1",
    }
    return json.dumps(payload, separators=(",", ":")).encode()


def poll_once(cookie: str, conv_id: str, relay: str | None, timeout: float) -> dict:
    path = f"/im/chain/single?{build_query(parse_cookie(cookie))}"
    headers = {
        "Content-Type": "application/json; encoding=utf-8",
        "agw-js-conv": "str",
        "Accept": "*/*",
        "Cookie": cookie,
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    }
    if relay:
        endpoint = relay
        headers["x-relay-target"] = f"https://{API_HOST}"
        headers["x-relay-path"] = path
    else:
        endpoint = f"https://{API_HOST}"

    u = urlsplit(endpoint)
    conn = http.client.HTTPSConnection(u.hostname, u.port or 443, timeout=timeout, context=CTX)
    try:
        conn.request("POST", u.path + (("?" + u.query) if u.query else ""),
                     body=build_body(conv_id), headers=headers)
        resp = conn.getresponse()
        raw = resp.read()
        return {"status": resp.status, "ctype": resp.headers.get("Content-Type"),
                "bytes": len(raw), "text": raw.decode("utf-8", "replace")}
    finally:
        conn.close()


def extract(text: str) -> dict:
    """Pull the interesting bits out of a poll response."""
    out: dict = {"messages": 0, "texts": [], "videos": [], "unwatermarked": []}
    try:
        data = json.loads(text)
    except json.JSONDecodeError:
        out["not_json"] = text[:200]
        return out
    msgs = (((data.get("downlink_body") or {}).get("pull_singe_chain_downlink_body") or {})
            .get("messages") or [])
    out["messages"] = len(msgs)
    for msg in msgs:
        content = msg.get("content")
        if isinstance(content, str):
            try:
                content = json.loads(content)
            except json.JSONDecodeError:
                continue
        if not isinstance(content, list):
            continue
        for block in content:
            btext = (((block.get("content") or {}).get("text_block")) or {}).get("text") or ""
            if btext:
                out["texts"].append(btext[:160])
            if block.get("block_type") != 2074:
                continue
            for cre in (((block.get("content") or {}).get("creation_block")) or {}).get("creations") or []:
                if cre.get("type") != 2:
                    continue
                vid = cre.get("video") or {}
                url = vid.get("download_url") or ""
                if url:
                    out["videos"].append(url[:140])
                model_str = vid.get("video_model") or ""
                if model_str:
                    try:
                        vm = json.loads(model_str)
                        for v in (vm.get("video_list") or {}).values():
                            main = (v or {}).get("main_url") or ""
                            if main:
                                dec = base64.b64decode(main).decode("utf-8", "ignore")
                                if dec.startswith("http"):
                                    out["unwatermarked"].append(dec[:140])
                    except Exception:  # noqa: BLE001
                        pass
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cookie-file", default="cookies.txt")
    ap.add_argument("--conversation-id", required=True)
    ap.add_argument("--egress", default="relay", choices=["direct", "relay"])
    ap.add_argument("--relay", default=RELAY_DEFAULT)
    ap.add_argument("--interval", type=float, default=15.0)
    ap.add_argument("--timeout", type=float, default=600.0)
    ap.add_argument("--once", action="store_true")
    ap.add_argument("--out", default="spikes/poll_check_result.json")
    args = ap.parse_args()

    cookie = [ln.strip() for ln in pathlib.Path(args.cookie_file).read_text(encoding="utf-8-sig").splitlines()
              if ln.strip() and not ln.strip().startswith("#")][0]
    relay = args.relay if args.egress == "relay" else None

    print(f"polling conversation {args.conversation_id} via {args.egress}")
    started = time.time()
    rounds = []
    while time.time() - started < args.timeout:
        try:
            r = poll_once(cookie, args.conversation_id, relay, timeout=20)
        except Exception as exc:  # noqa: BLE001 - every failure mode is data
            print(f"  transport error: {type(exc).__name__}: {exc}")
            rounds.append({"error": str(exc)})
            time.sleep(args.interval)
            continue

        info = extract(r["text"])
        rec = {"t": round(time.time() - started, 1), "status": r["status"], "bytes": r["bytes"], **info}
        rounds.append(rec)
        print(f"  +{rec['t']:>6}s  http={r['status']}  bytes={r['bytes']}  messages={info['messages']}  "
              f"videos={len(info['videos'])}  unwatermarked={len(info['unwatermarked'])}")
        if info["videos"] or info["unwatermarked"]:
            print("  video URL:", (info["videos"] or info["unwatermarked"])[0])
        if info.get("not_json"):
            print("  not JSON:", info["not_json"][:160])
            break
        if args.once:
            break
        time.sleep(args.interval)

    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump({"conversation_id": args.conversation_id, "egress": args.egress, "rounds": rounds},
                  fh, ensure_ascii=False, indent=2)
    print(f"\nwrote {args.out}  ({len(rounds)} rounds)")

    ok = [r for r in rounds if r.get("status") == 200 and r.get("messages") is not None]
    print(f"verdict: poll path {'WORKS' if ok else 'FAILED'} outside the browser via {args.egress}")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
