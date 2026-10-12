"""Task 0 spike: does the protocol submit path survive risk control?

This is the decision gate for the whole port. Upstream ships two execution
paths: a browser path (browser_pool.py -> video_worker_ui.py, the only one
server.py actually uses) and a pure-HTTP protocol path (dola_client.DolaClient)
that has no caller in the repo. video_probe.py's own comment says the browser
exists to bypass captcha checks, so whether pure HTTP submission is accepted is
an empirical question, not a code-reading one.

This script drives the *reference* implementation (dola_client.DolaClient) over
a configurable egress, so the measurement reflects the real request shape rather
than a reimplementation of it.

Requirements:
    pip install aiohttp
    a dola.com cookie (sessionid + msToken + s_v_web_id) in a file, one per line

Usage:
    python spikes/protocol_probe.py --cookie-file cookies.txt --runs 10
    python spikes/protocol_probe.py --cookie-file cookies.txt --runs 10 --egress relay
    python spikes/protocol_probe.py --upstream ../dola-render-gateway --cookie-file ../dola-render-gateway/cookies.txt

Output: one line per run plus a summary, and spikes/protocol_result.json.
"""

from __future__ import annotations

import argparse
import asyncio
import importlib.util
import json
import os
import sys
import time

DEFAULT_UPSTREAM = os.path.join("..", "dola-render-gateway")
RELAY_DEFAULT = "https://vcrelay.dmean.dpdns.org/"

# Risk-control markers seen in the upstream code and its error handling.
RISK_SLIDE = ("710022004", "slide", "shark")
RISK_RATE = ("710022002",)


def load_dola_client(upstream: str):
    """Import dola_client.py from the upstream checkout without installing it."""
    path = os.path.join(upstream, "dola_client.py")
    if not os.path.exists(path):
        sys.exit(f"dola_client.py not found at {path}; pass --upstream <checkout>")
    spec = importlib.util.spec_from_file_location("dola_client", path)
    if spec is None or spec.loader is None:
        sys.exit(f"cannot load {path}")
    mod = importlib.util.module_from_spec(spec)
    sys.modules["dola_client"] = mod
    spec.loader.exec_module(mod)
    return mod


def read_cookies(path: str) -> list[str]:
    with open(path, "r", encoding="utf-8-sig") as fh:
        lines = [ln.strip() for ln in fh if ln.strip() and not ln.strip().startswith("#")]
    if not lines:
        sys.exit(f"{path} has no cookies")
    return lines


def install_egress(egress: str, relay: str, proxy: str) -> None:
    """Point aiohttp at the requested egress.

    - direct: aiohttp's default (trust_env=True is set by dola_client, so any
      HTTP_PROXY in the environment would be picked up; clear them).
    - proxy: standard HTTP CONNECT via the environment aiohttp already honours.
    - relay: not expressible as an aiohttp proxy -- the relay rewrites the base
      URL and forwards x-relay-target/x-relay-path, so it needs a shim around
      aiohttp.ClientSession.request. See relay_shim.py for the full version; the
      short form below is enough to measure submit/poll behaviour.
    """
    if egress == "direct":
        for var in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
            os.environ.pop(var, None)
        return
    if egress == "proxy":
        for var in ("HTTP_PROXY", "http_proxy"):
            os.environ[var] = proxy
        for var in ("HTTPS_PROXY", "https_proxy"):
            os.environ[var] = proxy
        return
    if egress == "relay":
        install_relay_shim(relay)
        return
    sys.exit(f"unknown egress {egress!r}")


def install_relay_shim(relay: str) -> None:
    """Rewrite outbound aiohttp requests onto the relay's header contract."""
    import aiohttp

    original = aiohttp.ClientSession._request

    async def patched(self, method, url, **kwargs):
        from urllib.parse import urlsplit

        u = urlsplit(str(url))
        if u.scheme in ("http", "https") and not str(url).startswith(relay):
            headers = dict(kwargs.get("headers") or {})
            headers["x-relay-target"] = f"{u.scheme}://{u.netloc}"
            headers["x-relay-path"] = u.path + (("?" + u.query) if u.query else "")
            kwargs["headers"] = headers
            url = relay
        return await original(self, method, url, **kwargs)

    aiohttp.ClientSession._request = patched


async def probe_one(mod, cookie: str, prompt: str, duration: int, timeout: int) -> dict:
    rec: dict = {"started": time.time()}
    client = mod.DolaClient(cookie)
    rec["sessionid_present"] = bool(client.info.get("sessionid"))
    rec["mstoken_present"] = bool(client.info.get("msToken"))
    rec["fp_present"] = bool(client.info.get("fp"))
    t0 = time.perf_counter()
    try:
        url = await client.generate_video(prompt, "9:16", duration, timeout)
        rec["ok"] = True
        rec["video_url_head"] = str(url)[:120]
    except Exception as exc:  # noqa: BLE001 - every failure mode is data here
        rec["ok"] = False
        rec["error_type"] = type(exc).__name__
        rec["error"] = str(exc)[:400]
        text = f"{type(exc).__name__}: {exc}"
        rec["risk_slide"] = any(m in text for m in RISK_SLIDE)
        rec["risk_rate_limited"] = any(m in text for m in RISK_RATE)
    rec["elapsed_s"] = round(time.perf_counter() - t0, 2)
    return rec


async def main_async(args) -> int:
    mod = load_dola_client(args.upstream)
    cookies = read_cookies(args.cookie_file)
    install_egress(args.egress, args.relay, args.proxy)

    print(f"upstream : {os.path.abspath(args.upstream)}")
    print(f"egress   : {args.egress}" + (f" ({args.relay})" if args.egress == "relay" else ""))
    print(f"cookies  : {len(cookies)} (first: {cookies[0][:24]}...)")
    print(f"runs     : {args.runs}  duration={args.duration}s")
    print()

    results = []
    for i in range(args.runs):
        cookie = cookies[i % len(cookies)]
        rec = await probe_one(mod, cookie, args.prompt, args.duration, args.timeout)
        rec["run"] = i + 1
        results.append(rec)
        flag = "OK " if rec.get("ok") else "ERR"
        extra = ""
        if rec.get("risk_slide"):
            extra = "  <-- SLIDER/CAPTCHA"
        elif rec.get("risk_rate_limited"):
            extra = "  <-- RATE LIMITED"
        print(f"[{flag}] run {rec['run']:>2}  {rec['elapsed_s']:>6.2f}s  "
              f"{rec.get('error_type', 'completed')}{extra}")
        if i + 1 < args.runs:
            await asyncio.sleep(args.gap)

    ok = sum(1 for r in results if r.get("ok"))
    slides = sum(1 for r in results if r.get("risk_slide"))
    rate = sum(1 for r in results if r.get("risk_rate_limited"))
    summary = {
        "runs": len(results),
        "ok": ok,
        "success_rate": round(ok / len(results), 3),
        "risk_slide": slides,
        "risk_rate_limited": rate,
        "egress": args.egress,
        "duration": args.duration,
        "verdict": "ROUTE=A" if ok / len(results) >= 0.8 and slides < 2 else "ROUTE=B",
    }

    print()
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump({"summary": summary, "runs": results}, fh, ensure_ascii=False, indent=2)
    print(f"\nwrote {args.out}")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--upstream", default=os.getenv("DOLA_UPSTREAM", DEFAULT_UPSTREAM))
    ap.add_argument("--cookie-file", required=True)
    ap.add_argument("--runs", type=int, default=10)
    ap.add_argument("--egress", default="relay", choices=["direct", "proxy", "relay"])
    ap.add_argument("--relay", default=os.getenv("DOLA_RELAY_URL", RELAY_DEFAULT))
    ap.add_argument("--proxy", default=os.getenv("DOLA_PROBE_PROXY", "http://127.0.0.1:10808"))
    ap.add_argument("--prompt", default="a cat sitting on a windowsill")
    ap.add_argument("--duration", type=int, default=10, help="10/15/30; 30 probes the extended path")
    ap.add_argument("--timeout", type=int, default=300)
    ap.add_argument("--gap", type=float, default=3.0, help="seconds between runs")
    ap.add_argument("--out", default="spikes/protocol_result.json")
    args = ap.parse_args()
    return asyncio.run(main_async(args))


if __name__ == "__main__":
    sys.exit(main())
