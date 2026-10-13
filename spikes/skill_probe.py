"""Read-only: ask the account which skills exist, and with which ids.

The web client's own numeric enum says SkillImageGen=3, but that enum lives in a
lazily-loaded chunk and is a client-side constant. The server's answer would be
better evidence, and the skill/config routes are NOT on the mssdk signed path
list (measured earlier: they answer code 0 with no a_bogus at all), so this costs
no generation quota.

    python spikes/skill_probe.py cookies.txt [--out spikes/skill_probe.json]
"""

from __future__ import annotations

import argparse
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

ROUTES = [
    ("/samantha/skill/recommend", {}),
    ("/alice/slot/action_bar_v3/get_item_conf", {}),
    ("/alice/user/config/pull", {}),
    ("/alice/call/downgrade_config_pc", {}),
]


def load_cookie(path: str) -> str:
    for line in pathlib.Path(path).read_text(encoding="utf-8-sig").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            return line
    raise SystemExit("no cookie line found")


def app_query() -> str:
    return urlencode({
        "version_code": "20800", "language": "zh-Hant", "device_platform": "web",
        "doubao_device_platform": "web", "aid": "495671", "real_aid": "495671",
        "pkg_type": "release_version", "device_id": "7686671608228726325",
        "pc_version": "3.39.1", "doubao_pc_version": "3.39.1",
        "region": "JP", "sys_region": "JP", "samantha_web": "1",
        "web_platform": "browser", "use-olympus-account": "1",
        "web_tab_id": str(uuid.uuid4()),
    })


async def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("cookie_file")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()

    cookie = load_cookie(args.cookie_file)
    out: dict = {}
    async with aiohttp.ClientSession() as session:
        for path, body in ROUTES:
            headers = {
                "Content-Type": "application/json", "Accept": "*/*", "Cookie": cookie,
                "Origin": BASE, "Referer": BASE + "/chat/", "User-Agent": UA,
                "agw-js-conv": "str, str", "x-relay-target": BASE,
                "x-relay-path": path + "?" + app_query(),
            }
            try:
                async with session.post(RELAY, data=json.dumps(body).encode(),
                                        headers=headers,
                                        timeout=aiohttp.ClientTimeout(total=30)) as resp:
                    text = await resp.text()
                    status = resp.status
            except Exception as exc:  # noqa: BLE001
                out[path] = {"error": repr(exc)}
                print(path, "ERROR", exc)
                continue
            out[path] = {"status": status, "body": text}
            print("=" * 20, path, "status", status, "bytes", len(text))
            print(text[:3000])
            print()
    if args.out:
        pathlib.Path(args.out).write_text(json.dumps(out, ensure_ascii=False, indent=2),
                                          encoding="utf-8")
        print("wrote", args.out)
    return 0


if __name__ == "__main__":
    sys.exit(__import__("asyncio").run(main()))
