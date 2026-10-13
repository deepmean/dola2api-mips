"""Identify M (the hash) and r.decode from the dissected a_bogus bytes.

The dissector in the browser recovers the 9 header bytes of a_bogus, so the two
digest bytes are observable for any stub. From the SDK source:

    let i = M(n);                        // n = "" for the STUB form
    let a = r.decode(M(r.decode(i)));
    u[4] = a[14]; u[5] = a[15];
    let f = r.decode(M(r.decode(o)));    // o = the stub string
    u[6] = f[14]; u[7] = f[15];

Observed:
    stub ""     -> u[4],u[5] = 69,63
    stub "a"    -> u[6],u[7] = 66,126
    stub "abc"  -> u[6],u[7] = 239,247
    stub "aid=495671&channel=g&device_platform=web&version_code=20800" -> 2,36

So M and decode are pinned by four known input/output pairs.

Usage: python spikes/identify_hash.py
"""

from __future__ import annotations

import hashlib

ALPHABET = "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe="

OBSERVED = {
    "": (69, 63),
    "a": (66, 126),
    "abc": (239, 247),
    "aid=495671&channel=g&device_platform=web&version_code=20800": (2, 36),
}


def b64_custom_decode(s: str) -> bytes:
    """The SDK's custom-alphabet base64."""
    table = {c: i for i, c in enumerate(ALPHABET[:64])}
    bits = 0
    nbits = 0
    out = bytearray()
    for ch in s:
        if ch == "=":
            break
        v = table.get(ch)
        if v is None:
            continue
        bits = (bits << 6) | v
        nbits += 6
        if nbits >= 8:
            nbits -= 8
            out.append((bits >> nbits) & 0xFF)
    return bytes(out)


def hex_decode(s: str) -> bytes:
    try:
        return bytes.fromhex(s)
    except ValueError:
        return b""


def latin1(s: str) -> bytes:
    return s.encode("latin-1")


DECODERS = {
    "hex": hex_decode,
    "custom_b64": lambda s: b64_custom_decode(s),
    "latin1": latin1,
    "utf8": lambda s: s.encode("utf-8"),
}

HASHES = {
    "md5_hex": lambda b: hashlib.md5(b).hexdigest(),
    "md5_bytes": lambda b: hashlib.md5(b).digest(),
    "md5_upper_hex": lambda b: hashlib.md5(b).hexdigest().upper(),
    "sha1_hex": lambda b: hashlib.sha1(b).hexdigest(),
    "sha256_hex": lambda b: hashlib.sha256(b).hexdigest(),
    "sha256_bytes": lambda b: hashlib.sha256(b).digest(),
}


def last_two(x) -> tuple[int, int] | None:
    if isinstance(x, str):
        try:
            raw = bytes.fromhex(x)
        except ValueError:
            raw = x.encode("latin-1")
    else:
        raw = x
    if len(raw) < 16:
        return None
    return raw[14], raw[15]


def main() -> int:
    print(f"observed pairs: {len(OBSERVED)}\n")
    winners = []
    for hname, hfn in HASHES.items():
        for d1name, d1 in DECODERS.items():
            for d2name, d2 in DECODERS.items():
                ok = 0
                details = []
                for stub, want in OBSERVED.items():
                    try:
                        step1 = d1(hfn(latin1(stub)))
                        step2 = d2(hfn(step1))
                        got = last_two(step2)
                    except Exception:  # noqa: BLE001 - a candidate that throws is just wrong
                        got = None
                    if got == want:
                        ok += 1
                    details.append((stub[:14], got, want))
                if ok == len(OBSERVED):
                    winners.append((hname, d1name, d2name))
                    print(f"*** MATCH: M={hname}  decode1={d1name}  decode2={d2name}")

    if not winners:
        print("no full match; best partial candidates:")
        best = []
        for hname, hfn in HASHES.items():
            for d1name, d1 in DECODERS.items():
                for d2name, d2 in DECODERS.items():
                    ok = 0
                    for stub, want in OBSERVED.items():
                        try:
                            got = last_two(d2(hfn(d1(hfn(latin1(stub))))))
                        except Exception:  # noqa: BLE001
                            got = None
                        if got == want:
                            ok += 1
                    if ok:
                        best.append((ok, hname, d1name, d2name))
        for ok, hname, d1name, d2name in sorted(best, reverse=True)[:10]:
            print(f"  {ok}/{len(OBSERVED)}  M={hname} d1={d1name} d2={d2name}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
