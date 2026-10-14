"""Extract and decode a VM payload of the shape the recovered algorithm uses.

The decode chain, read off the deobfuscated implementation:

    base64 -> atob -> the first 8 bytes are a header
    key = (sum of bytes 4..8) % 256
    bytes 8.. are XORed with key, then inflated as raw deflate

What comes out is the VM in plain form: a string pool, then instruction metadata,
then the instruction stream. Comparing that pool between two sites is what says
whether one algorithm can be pointed at another.

Usage:
    python vm_payload.py <sdk.js> [more.js ...]
"""

from __future__ import annotations

import base64
import pathlib
import re
import sys
import zlib

B64 = re.compile(r"[\"'`]([A-Za-z0-9+/]{2000,}={0,2})[\"'`]")


def candidates(text: str) -> list[str]:
    return sorted(B64.findall(text), key=len, reverse=True)


def decode(b64: str) -> bytes | None:
    try:
        raw = base64.b64decode(b64)
    except Exception:
        return None
    if len(raw) <= 8:
        return None
    key = sum(raw[4:8]) % 256
    body = bytes(b ^ key for b in raw[8:])
    for wbits in (-zlib.MAX_WBITS, zlib.MAX_WBITS):
        try:
            return zlib.decompress(body, wbits)
        except Exception:
            continue
    return None


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    out_dir = pathlib.Path("payloads")
    out_dir.mkdir(exist_ok=True)

    for arg in sys.argv[1:]:
        path = pathlib.Path(arg)
        text = path.read_text(encoding="utf-8", errors="ignore")
        found = 0
        for i, b64 in enumerate(candidates(text)[:6]):
            plain = decode(b64)
            if not plain or len(plain) < 512:
                continue
            found += 1
            stem = f"{path.stem}_{i}"
            (out_dir / f"{stem}.vm").write_bytes(plain)
            # The pool comes first and is what a cross-site diff needs.
            print(f"{path.name} [{i}]: b64={len(b64)} plain={len(plain)} -> payloads/{stem}.vm")
            print(f"   head: {plain[:120]!r}")
        if not found:
            print(f"{path.name}: no payload of this shape decoded")
    return 0


if __name__ == "__main__":
    sys.exit(main())