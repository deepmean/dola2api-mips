"""Read-only: the ability_param key set for every skill type, from the server.

POST /samantha/skill/pack {"skill_type": N} answers with that skill's input
configuration, whose option_list[] entries carry value = the ability_param key.
This is how the image param shape can be read without submitting anything:

    skill_type 3  ->  ratio, style
    skill_type 17 ->  ratio, duration, model   (matches the known video body)

    python spikes/skill_pack_all.py cookies.txt [--out spikes/skill_pack_all.json]
"""

from __future__ import annotations

import argparse
import asyncio
import json
import pathlib
import sys
import uuid

import aiohttp

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from skill_pack_probe import app_query, load_cookie  # noqa: E402

RELAY = "https://vcrelay.dmean.dpdns.org/"
BASE = "https://www.dola.com"
UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

TYPES = [0, 2, 3, 4, 5, 6, 7, 8, 9, 11, 13, 16, 17, 18, 25, 26, 27]


async def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("cookie_file")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()
    cookie = load_cookie(args.cookie_file)

    out: dict = {}
    async with aiohttp.ClientSession() as session:
        for st in TYPES:
            headers = {
                "Accept": "*/*", "Cookie": cookie, "Origin": BASE,
                "Referer": BASE + "/chat/create-image", "User-Agent": UA,
                "agw-js-conv": "str, str", "x-relay-target": BASE,
                "Content-Type": "application/json",
                "x-relay-path": "/samantha/skill/pack?" + app_query(cookie),
            }
            try:
                async with session.post(RELAY, data=json.dumps({"skill_type": st}).encode(),
                                        headers=headers,
                                        timeout=aiohttp.ClientTimeout(total=30)) as resp:
                    text = await resp.text()
                    status = resp.status
            except Exception as exc:  # noqa: BLE001
                print(f"skill_type {st:<3} ERROR {exc}")
                continue
            out[str(st)] = {"status": status, "body": text}
            try:
                b = json.loads(text)
            except Exception:
                print(f"skill_type {st:<3} status {status} not json: {text[:120]}")
                continue
            data = b.get("data") or {}
            summary = {}
            for name, section in data.items():
                meta = (section or {}).get("meta") if isinstance(section, dict) else None
                if not meta:
                    summary[name] = "no-meta"
                    continue
                summary[name] = {
                    "option_keys": [o.get("value") for o in (meta.get("option_list") or [])],
                    "model_capability": list((meta.get("model_capability") or {}).keys()),
                    "render_mode": meta.get("render_mode"),
                }
            print(f"skill_type {st:<3} status {status} code {b.get('code')} data={json.dumps(summary, ensure_ascii=True)}")
            await asyncio.sleep(0.2)
    if args.out:
        pathlib.Path(args.out).write_text(json.dumps(out, ensure_ascii=False, indent=2),
                                          encoding="utf-8")
        print("wrote", args.out)
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
