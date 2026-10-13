"""Task 0 spike: does the protocol submit path survive risk control?

This is the decision gate for the whole port. Upstream ships two execution
paths: a browser path (browser_pool.py -> video_worker_ui.py, the only one
server.py actually uses) and a pure-HTTP protocol path (dola_client.DolaClient)
that has no caller in the repo. video_probe.py's own comment says the browser
exists to bypass captcha checks, so whether pure HTTP submission is accepted is
an empirical question, not a code-reading one.

Drives the *reference* implementation (dola_client.DolaClient) over a
configurable egress, so the measurement reflects the real request shape rather
than a reimplementation of it.

Why --runs defaults to 2 and not 10:
    browser_pool.DAILY_LIMIT is 2 videos per account per day. Runs past the
    second one fail with the daily-limit message, which says nothing about
    captcha risk and would pollute the verdict. A captcha *rate* needs either
    several accounts or several days; what this probe establishes is whether the
    protocol path works at all and what it looks like when it is blocked.

Requirements:
    pip install aiohttp
    a dola.com cookie in a file (see spikes/COOKIE.md)

Usage:
    python spikes/protocol_probe.py --upstream .. --cookie-file cookies.txt --runs 2
    python spikes/protocol_probe.py --upstream .. --cookie-file cookies.txt --runs 1 --duration 30
"""

from __future__ import annotations

import argparse
import asyncio
import importlib.util
import json
import os
import re
import sys
import time

RELAY_DEFAULT = "https://vcrelay.dmean.dpdns.org/"

# Risk-control and limit markers. The patterns come from the upstream code:
#   - 710022004 / 710022002 and the slide/shark wording: video_worker._check_submit
#   - credit wording: dola_client.CREDIT_FAIL_PATTERN
#   - daily-limit wording: video_worker_ui.DAILY_LIMIT_PATTERN (the protocol path
#     in dola_client._poll_video does NOT check for this, which is itself a port
#     requirement: the Go poll loop must classify it, or the pool will keep
#     retrying an account that is done for the day instead of rotating.)
RISK_SLIDE = ("710022004", "slide", "shark")
RISK_RATE = ("710022002",)
DAILY_LIMIT_PATTERN = re.compile(
    r"動画生成の\s*1日あたりの上限|每日(?:视频|影片)?生成.*(?:上限|限额|额度)|"
    r"daily.*(?:limit|quota)|(?:limit|quota).*per\s*day",
    re.IGNORECASE,
)


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
        return [ln.strip() for ln in fh if ln.strip() and not ln.strip().startswith("#")]


ENV_PROXY_VARS = ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy")


def install_egress(egress: str, relay: str, proxy: str) -> list[str]:
    """Point aiohttp at the requested egress. Returns the env vars that were set.

    dola_client builds its sessions with trust_env=True, so an HTTP_PROXY in the
    environment would silently add a hop. For both direct and relay the env is
    cleared; for relay the hop is added by the header shim instead, which is what
    the router will do.
    """
    cleared = []
    for var in ENV_PROXY_VARS:
        if os.environ.pop(var, None) is not None:
            cleared.append(var)

    if egress == "proxy":
        os.environ["HTTP_PROXY"] = proxy
        os.environ["HTTPS_PROXY"] = proxy
        os.environ["http_proxy"] = proxy
        os.environ["https_proxy"] = proxy
        return ["HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy]

    if egress == "relay":
        install_relay_shim(relay)
    return cleared


def install_relay_shim(relay: str) -> None:
    """Rewrite outbound aiohttp requests onto the relay's header contract."""
    import aiohttp
    from urllib.parse import urlsplit

    original = aiohttp.ClientSession._request

    async def patched(self, method, url, **kwargs):
        u = urlsplit(str(url))
        if u.scheme in ("http", "https") and not str(url).startswith(relay):
            headers = dict(kwargs.get("headers") or {})
            headers["x-relay-target"] = f"{u.scheme}://{u.netloc}"
            headers["x-relay-path"] = u.path + (("?" + u.query) if u.query else "")
            kwargs["headers"] = headers
            url = relay
        return await original(self, method, url, **kwargs)

    aiohttp.ClientSession._request = patched


def classify_error(exc: Exception) -> str:
    text = f"{type(exc).__name__}: {exc}"
    if any(m in text for m in RISK_SLIDE):
        return "RISK_SLIDE"
    if any(m in text for m in RISK_RATE):
        return "RATE_LIMITED"
    if DAILY_LIMIT_PATTERN.search(text):
        return "DAILY_LIMIT"
    if re.search(r"insufficient|unable to (generate|create)|無法生成|无法生成|余额不足|生成できません", text, re.I):
        return "NO_CREDIT"
    if type(exc).__name__ == "TimeoutError" or "timeout" in text.lower():
        return "TIMEOUT"
    return "ERROR"


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
        rec["unwatermarked_hint"] = "ibyteimg" in str(url) or "byteimg" in str(url)
    except Exception as exc:  # noqa: BLE001 - every failure mode is data here
        rec["ok"] = False
        rec["error_type"] = type(exc).__name__
        rec["error"] = str(exc)[:500]
        rec["verdict"] = classify_error(exc)
    rec["elapsed_s"] = round(time.perf_counter() - t0, 2)
    return rec


async def main_async(args) -> int:
    mod = load_dola_client(args.upstream)
    cookies = read_cookies(args.cookie_file)
    egress_note = install_egress(args.egress, args.relay, args.proxy)

    print(f"upstream : {os.path.abspath(args.upstream)}")
    print(f"egress   : {args.egress}" + (f" ({args.relay})" if args.egress == "relay" else ""))
    print(f"env      : {egress_note}")
    print(f"cookies  : {len(cookies)}")
    print(f"runs     : {args.runs}  duration={args.duration}s  timeout={args.timeout}s")
    print("note     : DAILY_LIMIT is 2 videos/account/day, so runs beyond the")
    print("           second one are expected to report DAILY_LIMIT, not captcha.")
    print()

    results = []
    for i in range(args.runs):
        cookie = cookies[i % len(cookies)]
        rec = await probe_one(mod, cookie, args.prompt, args.duration, args.timeout)
        rec["run"] = i + 1
        results.append(rec)
        if rec.get("ok"):
            print(f"[OK  ] run {rec['run']:>2}  {rec['elapsed_s']:>6.2f}s  video={rec['video_url_head'][:70]}")
        else:
            print(f"[ERR ] run {rec['run']:>2}  {rec['elapsed_s']:>6.2f}s  "
                  f"{rec['verdict']:<12} {rec['error'][:140]}")
        if i + 1 < args.runs:
            await asyncio.sleep(args.gap)

    ok = sum(1 for r in results if r.get("ok"))
    verdicts = {}
    for r in results:
        verdicts[r.get("verdict", "OK")] = verdicts.get(r.get("verdict", "OK"), 0) + 1

    # ROUTE=A requires the protocol path to work; a daily-limit result is a
    # working path that hit a known ceiling, not a captcha block.
    blocking = verdicts.get("RISK_SLIDE", 0) + verdicts.get("RATE_LIMITED", 0)
    summary = {
        "runs": len(results),
        "ok": ok,
        "verdicts": verdicts,
        "blocking_verdicts": blocking,
        "egress": args.egress,
        "duration": args.duration,
        "route": "A" if ok >= 1 and blocking == 0 else ("B" if blocking else "UNRESOLVED"),
        "note": "route A = router standalone; route B = submit delegated to a LAN sidecar",
    }

    print()
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump({"summary": summary, "runs": results}, fh, ensure_ascii=False, indent=2)
    print(f"\nwrote {args.out}")
    return 0 if ok else 1


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--upstream", default=os.getenv("DOLA_UPSTREAM", ".."))
    ap.add_argument("--cookie-file", required=True)
    # Bounded by DAILY_LIMIT (2 videos/account/day) on purpose; see module docstring.
    ap.add_argument("--runs", type=int, default=2)
    ap.add_argument("--egress", default="relay", choices=["direct", "proxy", "relay"])
    ap.add_argument("--relay", default=os.getenv("DOLA_RELAY_URL", RELAY_DEFAULT))
    ap.add_argument("--proxy", default=os.getenv("DOLA_PROBE_PROXY", "http://127.0.0.1:10808"))
    ap.add_argument("--prompt", default="a cat sitting on a windowsill")
    ap.add_argument("--duration", type=int, default=10, help="10/15/30; 30 probes the extended path")
    ap.add_argument("--timeout", type=int, default=300)
    ap.add_argument("--gap", type=float, default=5.0, help="seconds between runs")
    ap.add_argument("--out", default="spikes/protocol_result.json")
    args = ap.parse_args()
    return asyncio.run(main_async(args))


if __name__ == "__main__":
    sys.exit(main())
