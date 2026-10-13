"""Determine what string is fed to a_bogus as the stub.

The algorithm is now fully identified:

    u[0] = 63 & (++bogusIndex)          u[1..2] = envcode (big endian)
    u[3] = 14 (ubcode)                  u[4],u[5] = hexdecode(md5(hexdecode(md5(""))))[14:16]
    u[6],u[7] = hexdecode(md5(hexdecode(stub)))[14:16]
    u[8] = random
    payload = u + xor(u)                -> 10 bytes
    rc4 with key = one random byte      -> first two output bytes are
                                            [kWebsocket<<6 | rand<<4, keyByte]
    custom-base64 (alphabet Dkdpgh...) of that

Because u[6],u[7] depend only on the stub, the captured signature reveals the
stub: decrypt it, read u[6],u[7], and compare against candidate strings.

Usage: python spikes/solve_stub.py
"""

from __future__ import annotations

import hashlib
from urllib.parse import unquote, urlsplit

ALPHABET = "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe="
_B64MAP = {c: i for i, c in enumerate(ALPHABET[:64])}
_HEXMAP = {c: i for i, c in enumerate("0123456789abcdef")}
_HEXMAP.update({c: i for i, c in enumerate("0123456789ABCDEF")})

# The signature captured from the browser's own /chat/completion request.
CAPTURED = (
    "x7s5hqy7DoQnCdFt8KaI99PRDhQlNBWyXPTdWNlRHquDLXtT1wBJtGrQjPiHs8D6VRsiNHI7AnS6bxxOZWRX5ZnkFmpfSBJW6U9nVzsL"
    "/qq3P0ksDrgheh8zFwBCWKTPuCp4xIf6lUiHhxZ-DqriUc57H/pr4O4BFHxRksyiCV4ZgPvG3VdYSWy="
)

# The same request's query string, exactly as it appeared in the URL, minus a_bogus.
CAPTURED_QUERY = (
    "aid=495671&channel=g&device_platform=web&language=zh-Hant&region=JP&sys_region=JP"
    "&samantha_web=1&use-olympus-account=1&version_code=20800&web_platform=browser"
    "&web_tab_id=00bf6be5-b224-4ecf-a268-ef2c9468cc44"
    "&msToken=PWf46-Ja3GLFvAQWPgKLYlu4jBJK8DJqpMTJm9-e1XnnkBrBOVA2qgcCCZp9dTHHKjS8jwkUXzV-5C9MgOvn6FV_9aqURqgrATprme2JL891WppMHoAb-c2YYOHQEqjrcpE%3D"
    "&fp=verify_mv5xmc35_Cd63W77v_3NZK_4aYG_Acq0_jD7WnT2ZxXVB"
)


def b64_decode(s: str) -> bytes:
    bits = 0
    nbits = 0
    out = bytearray()
    for ch in s:
        if ch == "=":
            break
        v = _B64MAP.get(ch)
        if v is None:
            continue
        bits = (bits << 6) | v
        nbits += 6
        if nbits >= 8:
            nbits -= 8
            out.append((bits >> nbits) & 0xFF)
    return bytes(out)


def rc4(key: bytes, data: bytes) -> bytes:
    S = list(range(256))
    j = 0
    for i in range(256):
        j = (j + S[i] + key[i % len(key)]) % 256
        S[i], S[j] = S[j], S[i]
    out = bytearray()
    i = j = 0
    for b in data:
        i = (i + 1) % 256
        j = (j + S[i]) % 256
        S[i], S[j] = S[j], S[i]
        out.append(b ^ S[(S[i] + S[j]) % 256])
    return bytes(out)


def dissect(bogus: str) -> dict:
    raw = b64_decode(bogus)
    byte0, key = raw[0], raw[1]
    payload = rc4(bytes([key]), raw[2:])
    u = payload[:9]
    chk = payload[9] if len(payload) > 9 else None
    x = 0
    for b in u:
        x ^= b
    return {"byte0": byte0, "key": key, "u": list(u), "checksum_ok": x == chk,
            "raw_len": len(raw)}


def js_hex_decode(s: str) -> bytes:
    n = len(s) >> 1
    out = bytearray()
    for i in range(n):
        out.append(((_HEXMAP.get(s[2 * i], 0) << 4) | _HEXMAP.get(s[2 * i + 1], 0)) & 0xFF)
    return bytes(out)


def stub_bytes(stub: str) -> tuple[int, int]:
    """u[6],u[7] for a stub, using the identified algorithm."""
    effective = stub if stub else "0" * 32
    inner = js_hex_decode(hashlib.md5(effective.encode("utf-8")).hexdigest())
    f = js_hex_decode(hashlib.md5(inner).hexdigest())
    return f[14], f[15]


def main() -> None:
    info = dissect(CAPTURED)
    print(f"captured a_bogus dissected: {info}")
    if not info["checksum_ok"]:
        print("checksum failed -> decoding is wrong; stop here")
        return
    want = (info["u"][6], info["u"][7])
    print(f"stub digest bytes to reproduce: u[6],u[7] = {want}\n")

    candidates = {
        "query as sent (with %3D)": CAPTURED_QUERY,
        "query url-decoded": unquote(CAPTURED_QUERY),
        "query without msToken/fp": CAPTURED_QUERY.split("&msToken=")[0],
        "path + query": "/chat/completion?" + CAPTURED_QUERY,
        "full url minus a_bogus": "https://www.dola.com/chat/completion?" + CAPTURED_QUERY,
        "sorted query": "&".join(sorted(CAPTURED_QUERY.split("&"))),
        "empty": "",
    }
    for label, cand in candidates.items():
        got = stub_bytes(cand)
        flag = "  <== MATCH" if got == want else ""
        print(f"  {label:<28} -> {got}{flag}")

    print("\nAlso worth checking: the same query with a_bogus stripped but other")
    print("params reordered, or the raw body. Add candidates above as needed.")


if __name__ == "__main__":
    main()
