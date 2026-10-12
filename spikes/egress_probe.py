"""Task 0 spike: measure each egress path (direct / CONNECT proxy / Vercel relay).

Stdlib only -- no aiohttp/requests needed, so the probe runs on a bare Python.

What it measures, per egress:
  1. reachability + time-to-first-body-byte (TTFB) + total, against a stable target
     (www.dola.com/robots.txt -- httpbin.org is NOT used as a health target; it
     returned 503 on 5/5 attempts during recon)
  2. the egress IP the upstream actually sees (api.ipify.org)
  3. stability over N sequential requests (success rate, p50/p95)

Relay-specific extra checks:
  4. missing x-relay-target -> expect 400
  5. request body limit -- Vercel Edge documents 4.5MB; probe 4MiB and 5MiB
  6. buffering -- time-to-first-byte on a slow drip upstream, to confirm whether
     the edge buffers chunks (this decides whether SSE can be consumed incrementally)

Usage:
    python spikes/egress_probe.py [--runs 20] [--relay URL] [--proxy URL]
"""

from __future__ import annotations

import argparse
import http.client
import json
import os
import ssl
import statistics
import sys
import time
from urllib.parse import urlsplit

DOLA_HOST = "https://www.dola.com"
ROBOTS_PATH = "/robots.txt"
IPIFY = "https://api.ipify.org?format=json"
DEFAULT_RELAY = "https://vcrelay.dmean.dpdns.org/"
DEFAULT_PROXY = "http://127.0.0.1:7890"

CTX = ssl.create_default_context()


class ProbeError(Exception):
    pass


def _split(url: str):
    u = urlsplit(url)
    return u.scheme, u.hostname, u.port or (443 if u.scheme == "https" else 80), u.path or "/", u.query


def request(
    url: str,
    *,
    method: str = "GET",
    headers: dict | None = None,
    body: bytes | None = None,
    proxy: str | None = None,
    timeout: float = 30.0,
) -> dict:
    """One HTTP(S) request with first-body-byte timing.

    Returns {status, first_byte_ms, total_ms, length, head, headers}.
    Raises ProbeError on transport failure.
    """
    scheme, host, port, path, query = _split(url)
    if scheme != "https":
        raise ProbeError(f"only https supported, got {scheme!r}")

    target = path + (("?" + query) if query else "")
    hdrs = dict(headers or {})
    if body is not None:
        hdrs["Content-Length"] = str(len(body))

    conn = None
    t0 = time.perf_counter()
    try:
        if proxy:
            _, phost, pport, _, _ = _split(proxy)
            conn = http.client.HTTPSConnection(phost, pport, timeout=timeout, context=CTX)
            conn.set_tunnel(host, port)
        else:
            conn = http.client.HTTPSConnection(host, port, timeout=timeout, context=CTX)
        conn.request(method, target, body=body, headers=hdrs)
        resp = conn.getresponse()
        head_ms = (time.perf_counter() - t0) * 1000.0
        first = resp.read(1)
        first_ms = (time.perf_counter() - t0) * 1000.0
        rest = resp.read()
        total_ms = (time.perf_counter() - t0) * 1000.0
        payload = first + rest
        return {
            "status": resp.status,
            "header_ms": round(head_ms, 1),
            "first_byte_ms": round(first_ms, 1),
            "total_ms": round(total_ms, 1),
            "length": len(payload),
            "head": payload[:120].decode("utf-8", "replace"),
            "resp_headers": {k.lower(): v for k, v in resp.getheaders()},
        }
    except Exception as exc:  # noqa: BLE001 - spike wants every failure recorded
        raise ProbeError(f"{type(exc).__name__}: {exc}") from exc
    finally:
        if conn is not None:
            try:
                conn.close()
            except Exception:  # noqa: BLE001
                pass


def relay_request(relay: str, target_url: str, *, method: str = "GET", headers: dict | None = None,
                  body: bytes | None = None, timeout: float = 30.0) -> dict:
    """Forward one request through the Vercel relay using 9router's header contract."""
    scheme, host, port, path, query = _split(target_url)
    hdrs = dict(headers or {})
    hdrs["x-relay-target"] = f"{scheme}://{host}" + (f":{port}" if port not in (443,) else "")
    hdrs["x-relay-path"] = path + (("?" + query) if query else "")
    return request(relay, method=method, headers=hdrs, body=body, timeout=timeout)


def build_egresses(relay: str, proxy: str | None) -> list[dict]:
    out = [{"name": "direct", "kind": "direct", "proxy": None, "relay": None}]
    if proxy:
        out.append({"name": "proxy", "kind": "connect", "proxy": proxy, "relay": None})
    out.append({"name": "relay", "kind": "vercel", "proxy": None, "relay": relay})
    return out


def do(eg: dict, target_url: str, **kw) -> dict:
    if eg["kind"] == "vercel":
        return relay_request(eg["relay"], target_url, **kw)
    return request(target_url, proxy=eg["proxy"], **kw)


def pct(values: list[float], p: float) -> float:
    if not values:
        return float("nan")
    ordered = sorted(values)
    idx = min(len(ordered) - 1, max(0, round((p / 100.0) * (len(ordered) - 1))))
    return ordered[idx]


def probe_egress(eg: dict, runs: int) -> dict:
    rec: dict = {"name": eg["name"], "kind": eg["kind"], "reachable": False}

    # 1) stable target
    try:
        r = do(eg, DOLA_HOST + ROBOTS_PATH, timeout=25)
        rec["robots"] = {k: r[k] for k in ("status", "first_byte_ms", "total_ms", "length", "head")}
        rec["reachable"] = r["status"] == 200 and "User-agent" in r["head"]
    except ProbeError as exc:
        rec["robots_error"] = str(exc)

    # 2) egress IP as the upstream sees it
    try:
        r = do(eg, IPIFY, timeout=25)
        try:
            rec["egress_ip"] = json.loads(r["head"] if r["length"] <= 120 else "")  # may be truncated
        except Exception:  # noqa: BLE001
            rec["egress_ip_raw"] = r["head"]
        if "egress_ip" not in rec:
            rec["egress_ip_raw"] = r["head"]
    except ProbeError as exc:
        rec["ip_error"] = str(exc)

    # 3) stability
    ok, times, errs = 0, [], []
    for _ in range(runs):
        try:
            r = do(eg, DOLA_HOST + ROBOTS_PATH, timeout=25)
            times.append(r["total_ms"])
            if r["status"] == 200:
                ok += 1
        except ProbeError as exc:
            errs.append(str(exc))
        time.sleep(0.4)
    rec["stability"] = {
        "runs": runs,
        "ok": ok,
        "success_rate": round(ok / runs, 3) if runs else 0.0,
        "p50_ms": round(statistics.median(times), 1) if times else None,
        "p95_ms": round(pct(times, 95), 1) if times else None,
        "errors": errs[:3],
    }
    return rec


def probe_relay_extras(relay: str) -> dict:
    out: dict = {}

    # 4) missing header must be a clean 400 from the relay itself
    try:
        r = request(relay, timeout=20)
        out["missing_header"] = {"status": r["status"], "head": r["head"][:120]}
    except ProbeError as exc:
        out["missing_header"] = {"error": str(exc)}

    # 5) request body limit (Vercel documents 4.5MB)
    for label, size in (("4MiB", 4 * 1024 * 1024), ("5MiB", 5 * 1024 * 1024)):
        try:
            r = relay_request(
                relay, DOLA_HOST + ROBOTS_PATH, method="POST",
                headers={"Content-Type": "application/octet-stream"},
                body=b"\0" * size, timeout=60,
            )
            out[f"body_{label}"] = {"status": r["status"], "head": r["head"][:160], "total_ms": r["total_ms"]}
        except ProbeError as exc:
            out[f"body_{label}"] = {"error": str(exc)}

    # 6) buffering: drip 5 bytes over 8s, delay 2s -- compare first-byte vs total
    drip = "https://httpbin.org/drip?numbytes=5&duration=8&delay=2"
    for label, fn in (
        ("direct", lambda: request(drip, timeout=40)),
        ("relay", lambda: relay_request(relay, drip, timeout=40)),
    ):
        try:
            r = fn()
            out[f"drip_{label}"] = {
                "status": r["status"], "first_byte_ms": r["first_byte_ms"], "total_ms": r["total_ms"],
                "head": r["head"][:40],
            }
        except ProbeError as exc:
            out[f"drip_{label}"] = {"error": str(exc)}
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=20)
    ap.add_argument("--relay", default=os.getenv("DOLA_RELAY_URL", DEFAULT_RELAY))
    ap.add_argument("--proxy", default=os.getenv("DOLA_PROBE_PROXY", DEFAULT_PROXY))
    ap.add_argument("--out", default="spikes/egress_result.json")
    args = ap.parse_args()

    print(f"relay      : {args.relay}")
    print(f"proxy      : {args.proxy}")
    print(f"env proxies: HTTP_PROXY={os.getenv('HTTP_PROXY')} HTTPS_PROXY={os.getenv('HTTPS_PROXY')} "
          f"ALL_PROXY={os.getenv('ALL_PROXY')}")
    print(f"runs       : {args.runs}")
    print()

    # Skip the proxy egress when nothing is listening on it.
    proxy = args.proxy
    try:
        request(DOLA_HOST + ROBOTS_PATH, proxy=proxy, timeout=10)
    except ProbeError as exc:
        print(f"[warn] proxy {proxy} unusable ({exc}) -- skipping that egress\n")
        proxy = None

    result: dict = {"relay": args.relay, "proxy": proxy, "runs": args.runs, "egresses": []}
    for eg in build_egresses(args.relay, proxy):
        print(f"== {eg['name']} ({eg['kind']}) ==")
        rec = probe_egress(eg, args.runs)
        result["egresses"].append(rec)
        print(json.dumps(rec, ensure_ascii=False, indent=2))
        print()

    print("== relay extras ==")
    extras = probe_relay_extras(args.relay)
    result["relay_extras"] = extras
    print(json.dumps(extras, ensure_ascii=False, indent=2))

    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(result, fh, ensure_ascii=False, indent=2)
    print(f"\nwrote {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
