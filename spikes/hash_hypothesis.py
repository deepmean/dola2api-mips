"""Test the plain-MD5 hypothesis for the a_bogus digest bytes.

From the SDK source:

    let i = M(n);                                  // n forced to ""
    let a = r.decode(M(r.decode(i)));  u[4]=a[14]; u[5]=a[15];
    o || (o = "00000000000000000000000000000000");  // empty stub -> 32 zeros
    let f = r.decode(M(r.decode(o)));  u[6]=f[14]; u[7]=f[15];

r.decode is a hex-string decoder (recovered from the SDK). Observed bytes:
    u[4],u[5] = 69,63                              (constant)
    u[6],u[7] = 24,165   for stub ""
    u[6],u[7] = 66,126   for stub "a"
    u[6],u[7] = 239,247  for stub "abc"
    u[6],u[7] = 2,36     for the real query string
"""

from __future__ import annotations

import hashlib

OBSERVED_CONST = (69, 63)
OBSERVED_STUB = {"": (24, 165), "a": (66, 126), "abc": (239, 247),
                 "aid=495671&channel=g&device_platform=web&version_code=20800": (2, 36)}


def hex_decode(s: str) -> bytes:
    """Mirrors the SDK's decoder: pairs of hex chars, odd tail dropped."""
    if len(s) % 2:
        s = s[:-1]
    try:
        return bytes.fromhex(s)
    except ValueError:
        return b""


def md5_hex(data) -> str:
    if isinstance(data, str):
        data = data.encode("utf-8")
    return hashlib.md5(data).hexdigest()


def chain(x: str) -> tuple[int, int] | None:
    """decode(md5(decode(x)))[14:16]"""
    inner = hex_decode(md5_hex(x))
    outer = hex_decode(md5_hex(inner))
    if len(outer) < 16:
        return None
    return outer[14], outer[15]


def main() -> None:
    got = chain(md5_hex(""))
    print(f"constant path: decode(md5(decode(md5('')))) -> {got}   observed {OBSERVED_CONST}"
          f"   {'MATCH' if got == OBSERVED_CONST else 'no'}")

    print()
    for stub, want in OBSERVED_STUB.items():
        effective = stub if stub else "0" * 32
        got = chain(effective)
        label = repr(stub[:28]) if stub else '"" -> 32 zeros'
        print(f"stub {label:<34} -> {got}   observed {want}   {'MATCH' if got == want else 'no'}")

    print("\nIf the constant matches but the stubs do not, M is md5 plus a"
          "\npost/pre transform, or the stub is preprocessed before hashing.")


if __name__ == "__main__":
    main()
