"""Read-only: ask for the skill pack, which is where ability_param's shape lives.

The extension in the upstream repo tampers with /samantha/skill/pack to inject a
15s duration, and the tampered copy it ships shows the response structure:
data.video_generation.meta.option_list[] entries carry value = "ratio" /
"duration" / "model". Those are exactly the ability_param keys.

If the live response also has an image_generation entry, its option_list is the
image ability_param shape — and unlike the generation endpoints, this route costs
no quota.

    python spikes/skill_pack_probe.py cookies.txt [--out spikes/skill_pack.json]
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
UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")


def load_cookie(path: str) -> str:
    for line in pathlib.Path(path).read_text(encoding="utf-8-sig").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            return line
    raise SystemExit("no cookie line found")


def app_query(cookie: str) -> str:
    import re
    params = {
        "version_code": "20800", "language": "zh-Hant", "device_platform": "web",
        "doubao_device_platform": "web", "aid": "495671", "real_aid": "495671",
        "pkg_type": "release_version", "device_id": "7686671608228726325",
        "pc_version": "3.39.1", "doubao_pc_version": "3.39.1",
        "region": "JP", "sys_region": "JP", "samantha_web": "1",
        "web_platform": "browser", "use-olympus-account": "1",
        "web_tab_id": str(uuid.uuid4()),
    }
    m = re.search(r"msToken=([^;]+)", cookie)
    if m:
        params["msToken"] = m.group(1)
    f = re.search(r"s_v_web_id=([^;]+)", cookie)
    if f:
        params["fp"] = f.group(1)
    return urlencode(params)


async def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("cookie_file")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()
    cookie = load_cookie(args.cookie_file)

    attempts = [("GET", {}), ("POST", {}), ("POST", {"skill_type": 3})]
    out: dict = {}
    async with aiohttp.ClientSession() as session:
        for method, body in attempts:
            label = f"{method} {json.dumps(body)}"
            headers = {
                "Accept": "*/*", "Cookie": cookie, "Origin": BASE,
                "Referer": BASE + "/chat/create-image", "User-Agent": UA,
                "agw-js-conv": "str, str", "x-relay-target": BASE,
                "x-relay-path": "/samantha/skill/pack?" + app_query(cookie),
            }
            data = None
            if method == "POST":
                headers["Content-Type"] = "application/json"
                data = json.dumps(body).encode()
            try:
                async with session.request(method, RELAY, data=data, headers=headers,
                                           timeout=aiohttp.ClientTimeout(total=30)) as resp:
                    text = await resp.text()
                    status = resp.status
            except Exception as exc:  # noqa: BLE001
                print(label, "ERROR", exc)
                continue
            out[label] = {"status": status, "body": text}
            print("=" * 20, label, "status", status, "bytes", len(text))
            print(text[:1500])
            print()
    if args.out:
        pathlib.Path(args.out).write_text(json.dumps(out, ensure_ascii=False, indent=2),
                                          encoding="utf-8")
        print("wrote", args.out)
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
