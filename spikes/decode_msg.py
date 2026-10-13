"""Decode the raw error_msg bytes from a submit_diag result.

The upstream mixes GBK-encoded Chinese into an otherwise-ASCII JSON skeleton, so
a UTF-8 decode turns the one field that explains the failure into U+FFFD. This
pulls the bytes back out and shows which codec actually reads them.

Usage: python spikes/decode_msg.py [spikes/submit_diag_result.json]
"""

from __future__ import annotations

import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "spikes/submit_diag_result.json")
data = json.loads(path.read_text(encoding="utf-8"))

for rec in data.get("results", []):
    hex_head = rec.get("raw_hex_head") or ""
    if not hex_head:
        print(f"attempt {rec.get('attempt')}: no raw hex captured")
        continue
    raw = bytes.fromhex(hex_head)
    i = raw.find(b'"error_msg":"')
    if i < 0:
        print(f"attempt {rec.get('attempt')}: no error_msg field")
        continue
    j = raw.find(b'"', i + 13)
    seg = raw[i + 13:j]

    print(f"attempt {rec.get('attempt')}: error_code={rec.get('error_code')} msg={len(seg)} bytes")
    print(f"  hex    : {seg.hex()}")
    for enc in ("gbk", "gb18030", "big5", "utf-8", "latin-1"):
        try:
            print(f"  {enc:<9}: {ascii(seg.decode(enc))}")
        except Exception as exc:  # noqa: BLE001 - reporting which codec fails is the point
            print(f"  {enc:<9}: FAIL {exc}")
    print()
