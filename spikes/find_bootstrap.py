"""Download every lazy chunk and look for the mssdk bootstrap.

The page loads two mssdk endpoints before it signs anything:

    mssdk.bytedance.com/web/r/token?ms_appid=495671&msToken=<current>
    mssdk.bytedance.com/web/common?ms_appid=495671&msToken=<fresh>

Neither the main bundles nor the two SDK files contain those strings, so the
code that issues them is in a lazily loaded chunk. The chunk names and their
hashes are in the page HTML, so every URL can be constructed without a browser.

Usage:
    python spikes/find_bootstrap.py <page.html> <out_dir>
"""

from __future__ import annotations

import json
import pathlib
import re
import subprocess
import sys
import urllib.parse

BASE = "https://sf-flow-web-cdn.ciciai.com/obj/ocean-flow-web-sg/dola_web/static/js/async/"
NEEDLES = ("web/r/token", "web/common", "ms_appid", "bytedance.com", "mssdk")


def maps(html: str) -> dict[str, str]:
    key = 'static/js/async/"+('
    i = html.find(key)
    if i < 0:
        raise SystemExit("the chunk name map is not where it was")
    start = i + len(key)
    depth, end = 0, None
    for j in range(start, start + 200_000):
        if html[j] == "{":
            depth += 1
        elif html[j] == "}":
            depth -= 1
            if depth == 0:
                end = j
                break
    names = dict(re.findall(r'"([0-9a-z]+)":"([^"]+)"', html[start:end + 1]))

    rest = html[end:end + 60_000]
    h = rest.find("{")
    depth, hend = 0, None
    for j in range(h, len(rest)):
        if rest[j] == "{":
            depth += 1
        elif rest[j] == "}":
            depth -= 1
            if depth == 0:
                hend = j
                break
    hashes = dict(re.findall(r'"?([0-9a-z]+)"?:\\?"([0-9a-f]{10})\\?"', rest[h:hend + 1]))
    return {cid: f"{name}.{hashes[cid]}.js" for cid, name in names.items() if cid in hashes}


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    html = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="ignore")
    out = pathlib.Path(sys.argv[2])
    out.mkdir(parents=True, exist_ok=True)

    urls = maps(html)
    print(f"{len(urls)} chunk URLs constructed")

    hits: list[tuple[str, str, int]] = []
    for n, (cid, file) in enumerate(sorted(urls.items()), 1):
        target = out / file
        if not target.exists():
            subprocess.run(
                ["curl", "-s", "-L", "--max-time", "40", "-o", str(target), BASE + file],
                check=False,
            )
        if not target.exists():
            continue
        text = target.read_text(encoding="utf-8", errors="ignore")
        for needle in NEEDLES:
            if needle in text:
                hits.append((file, needle, text.count(needle)))
        if n % 50 == 0:
            print(f"  {n}/{len(urls)} scanned, {len(hits)} hit(s)")

    print()
    if not hits:
        print("no chunk mentions the mssdk bootstrap")
        return 1
    print("chunks mentioning the bootstrap:")
    for file, needle, count in hits:
        print(f"  {file}: {needle} x{count}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
