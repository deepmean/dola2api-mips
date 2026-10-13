"""Pin down M and r.decode with JS-exact semantics.

Two mistakes in the first attempt:
  1. Python's bytes.fromhex() fails on non-hex input, while the SDK's decoder
     maps a non-hex char to 0 (`undefined << 4` is 0 in JS). Every stub that is
     not pure hex therefore decoded differently.
  2. The empty stub is replaced by 32 zeros before the second digest:
         o || (o = "00000000000000000000000000000000")

Chain under test:
    u[4],u[5] = decode(M(decode(M(""))))[14:16]      observed (69, 63)
    u[6],u[7] = decode(M(decode(stub)))[14:16]       observed per stub below

Search space: hash (md5/sha1/sha256) x decoder (hex / custom-base64) x salt
placement (none / prefix / suffix), with salts taken from constants that
actually appear in the SDK.

Usage: python spikes/identify_hash2.py
"""

from __future__ import annotations

import hashlib
import itertools

ALPHABET = "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe="
_B64MAP = {c: i for i, c in enumerate(ALPHABET[:64])}
_HEXMAP = {c: i for i, c in enumerate("0123456789abcdef")}
_HEXMAP.update({c: i for i, c in enumerate("0123456789ABCDEF")})

OBSERVED_CONST = (69, 63)
OBSERVED_STUB = {
    "": (24, 165),
    "a": (66, 126),
    "abc": (239, 247),
    "aid=495671&channel=g&device_platform=web&version_code=20800": (2, 36),
}

SALTS = [
    "",
    "X-MS-STUB", "X-MS-PAYLOAD", "X-Bogus", "a_bogus",
    "00000000000000000000000000000000",
    ALPHABET,
    "bdms", "webmssdk", "mssdk", "secsdk",
    "495671", "samantha", "dola", "doubao",
]


def js_hex_decode(s: str) -> bytes:
    """The SDK's decoder: pairs of chars, non-hex counts as 0, odd tail dropped."""
    n = len(s) >> 1
    out = bytearray()
    for i in range(n):
        hi = _HEXMAP.get(s[2 * i], 0)
        lo = _HEXMAP.get(s[2 * i + 1], 0)
        out.append(((hi << 4) | lo) & 0xFF)
    return bytes(out)


def custom_b64_decode(s: str) -> bytes:
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


DECODERS = {"hex": js_hex_decode, "b64": custom_b64_decode}
HASHES = {
    "md5": lambda b: hashlib.md5(b).hexdigest(),
    "sha1": lambda b: hashlib.sha1(b).hexdigest(),
    "sha256": lambda b: hashlib.sha256(b).hexdigest(),
}


def make_hash(name: str, salt: str, placement: str):
    base = HASHES[name]

    def fn(data) -> str:
        if isinstance(data, str):
            data = data.encode("utf-8")
        if placement == "prefix":
            data = salt.encode("utf-8") + data
        elif placement == "suffix":
            data = data + salt.encode("utf-8")
        return base(data)

    return fn


def chain_digest(hfn, dec, stub: str) -> tuple[int, int] | None:
    """decode(M(decode(stub)))[14:16]"""
    inner = dec(hfn(dec(stub)))
    if len(inner) < 16:
        return None
    return inner[14], inner[15]


def main() -> None:
    placements = ["none", "prefix", "suffix"]
    hits = []

    for hname, dname, placement, salt in itertools.product(
            HASHES, DECODERS, placements, SALTS):
        hfn = make_hash(hname, salt, placement)
        dec = DECODERS[dname]

        # The constant path hashes the empty string with the SAME M, then decodes
        # and hashes again: decode(M(decode(M(""))))
        first = dec(hfn(""))
        if len(first) < 16:
            continue
        const = dec(hfn(first))
        if len(const) < 16 or (const[14], const[15]) != OBSERVED_CONST:
            continue

        # Constant matched; now the stub path.
        ok = 0
        for stub, want in OBSERVED_STUB.items():
            effective = stub if stub else "0" * 32
            if chain_digest(hfn, dec, effective) == want:
                ok += 1
        hits.append((ok, hname, dname, placement, salt))

    if not hits:
        print("constant path (69,63) not reproduced by any candidate.")
        print("=> M is not a plain hash of the input; look for a custom mixer in the SDK.")
        return

    for ok, hname, dname, placement, salt in sorted(hits, reverse=True):
        print(f"const MATCH  stubs {ok}/{len(OBSERVED_STUB)}  M={hname} "
              f"decode={dname} salt={placement}:{salt[:24]!r}")


if __name__ == "__main__":
    main()
