"""Verify a dola.com cookie before it is handed to the router.

This is the same probe the router will run as `verify` (POST
/im/chain/recent_conv, cmd 3200) -- so a cookie that passes here is a cookie the
router can use, and running it now confirms the request shape before any Go is
written.

Stdlib only. Cookie file: one cookie per line, `#` comments allowed.

Usage:
    python spikes/verify_cookie.py --cookie-file cookies.txt
    python spikes/verify_cookie.py --cookie-file cookies.txt --egress relay
    python spikes/verify_cookie.py --cookie-file cookies.txt --json
"""

from __future__ import annotations

import argparse
import http.client
import json
import os
import ssl
import sys
import time
import uuid
from email.utils import parsedate_to_datetime
from urllib.parse import unquote_plus

API_HOST = "www.dola.com"
RECENT_CONV_PATH = "/im/chain/recent_conv"
RELAY_DEFAULT = "https://vcrelay.dmean.dpdns.org/"

# Required for the protocol path; the rest are optional but improve the odds.
REQUIRED_FIELDS = ("sessionid",)
RECOMMENDED_FIELDS = ("msToken", "s_v_web_id")
# Carried only so the operator can see when the session dies.
EXPIRY_FIELDS = ("sid_guard", "passport_auth_status", "sid_tt")

CTX = ssl.create_default_context()


def split_cookie(cookie: str) -> dict[str, str]:
    out: dict[str, str] = {}
    for part in cookie.split(";"):
        part = part.strip()
        if not part or "=" not in part:
            continue
        k, v = part.split("=", 1)
        out[k.strip()] = v.strip()
    return out


def build_query(fields: dict[str, str]) -> str:
    """Mirrors dola_client.build_query_params for the fields that matter here."""
    params = {
        "aid": "495671",
        "channel": "g",
        "device_platform": "web",
        "doubao_device_platform": "web",
        "language": "ja",
        "real_aid": "495671",
        "pkg_type": "release_version",
        "pc_version": "3.32.62",
        "doubao_pc_version": "3.32.62",
        "region": "JP",
        "sys_region": "JP",
        "samantha_web": "1",
        "use-olympus-account": "1",
        "version_code": "20800",
        "web_platform": "browser",
        "web_tab_id": str(uuid.uuid4()),
    }
    if fields.get("msToken"):
        params["msToken"] = fields["msToken"]
    if fields.get("s_v_web_id"):
        params["fp"] = fields["s_v_web_id"]
    from urllib.parse import urlencode

    return urlencode(params)


def build_body() -> bytes:
    """video_worker_ui.BALANCE_JS payload, with the limits cut right down.

    The upstream uses limit=20 / message_count_per_conv=10 to read balance text;
    this probe only needs proof that the session is alive, and a smaller body
    keeps the response small (which is also what the router will do).
    """
    payload = {
        "cmd": 3200,
        "uplink_body": {
            "pull_recent_conv_chain_uplink_body": {
                "limit": 5,
                "message_count_per_conv": 1,
                "api_version": 1,
                "conv_version": 0,
                "direction": 3,
                "option": {
                    "not_need_message": True,
                    "need_complete_conversation": True,
                    "need_coco_conversation": True,
                    "need_coco_bot": True,
                    "need_pc_pin_chain": True,
                    "pc_pin_query_type": 0,
                },
            }
        },
        "sequence_id": str(uuid.uuid4()),
        "channel": 2,
        "version": "1",
    }
    return json.dumps(payload, separators=(",", ":")).encode()


def post(cookie: str, *, relay: str | None, timeout: float) -> dict:
    """POST recent_conv, optionally through the relay's header contract."""
    path = f"{RECENT_CONV_PATH}?{build_query(split_cookie(cookie))}"
    headers = {
        "Content-Type": "application/json; encoding=utf-8",
        "agw-js-conv": "str",
        "Accept": "*/*",
        "Cookie": cookie,
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    }
    body = build_body()

    if relay:
        endpoint = relay
        headers["x-relay-target"] = f"https://{API_HOST}"
        headers["x-relay-path"] = path
    else:
        endpoint = f"https://{API_HOST}"

    from urllib.parse import urlsplit

    u = urlsplit(endpoint)
    if not u.hostname:
        raise ValueError(f"egress endpoint has no host: {endpoint!r}")
    conn = http.client.HTTPSConnection(u.hostname, u.port or 443, timeout=timeout, context=CTX)
    t0 = time.perf_counter()
    try:
        conn.request("POST", u.path + (("?" + u.query) if u.query else ""), body=body, headers=headers)
        resp = conn.getresponse()
        raw = resp.read()
        text = raw.decode("utf-8", "replace")
        return {
            "status": resp.status,
            "ms": round((time.perf_counter() - t0) * 1000, 1),
            # Keep the full body for parsing and a short copy for display:
            # truncating before json.loads makes every valid response look
            # malformed, which is exactly the wrong conclusion to draw here.
            "body": text,
            "body_head": text[:200],
            "raw_len": len(raw),
            "content_type": resp.getheader("Content-Type") or "",
            "location": resp.getheader("Location") or "",
        }
    finally:
        conn.close()


def classify(result: dict) -> tuple[str, str]:
    """Return (verdict, human explanation)."""
    status = result.get("status", 0)
    body = result.get("body", "")
    where = (f"len={result.get('raw_len')} ctype={result.get('content_type') or '?'} "
             f"body={result.get('body_head', '')[:160]!r}")

    if status in (301, 302, 303, 307, 308):
        # Seen on the direct path from a non-JP vantage: the upstream bounces the
        # request instead of serving it. That is an egress problem, not a dead
        # cookie, and it is why strict proxy mode must never fall back to direct.
        return "REDIRECT", (f"HTTP {status} to {result.get('location') or '(no Location)'}: "
                            f"the upstream is bouncing this egress, not rejecting the session")
    if status in (401, 403):
        return "EXPIRED", f"HTTP {status}: the session is not accepted ({where})"
    if status == 429:
        return "RATE_LIMITED", f"HTTP 429: the upstream is throttling this egress IP ({where})"

    try:
        data = json.loads(body)
    except json.JSONDecodeError:
        if "<html" in body.lower():
            return "EXPIRED", f"HTML instead of JSON: almost always a login/verify page ({where})"
        return "UNKNOWN", f"HTTP {status} but the body is not JSON ({where})"

    if isinstance(data, dict):
        if data.get("code") not in (None, 0):
            return "REJECTED", f"HTTP {status}, upstream code={data.get('code')} message={str(data.get('message'))[:120]}"
        down = (data.get("downlink_body") or {}).get("pull_recent_conv_chain_downlink_body")
        if down is not None:
            cells = down.get("cells") or []
            msgs = down.get("messages") or []
            return "OK", (f"HTTP {status}, downlink_body present "
                          f"({len(cells)} conversation cell(s), {len(msgs)} message(s))")
        if data.get("downlink_body"):
            return "OK", f"HTTP {status}, downlink_body present"
        return "UNKNOWN", f"HTTP {status}, JSON parsed but no downlink_body ({where})"

    return "UNKNOWN", f"HTTP {status}, unexpected JSON shape ({where})"


def decode_expiry(cookie: str) -> str:
    """Decode the session expiry from sid_guard.

    sid_guard is URL-encoded and pipe-delimited:
        <sessionid>|<issued_epoch>|<ttl_seconds>|<human readable GMT expiry>

    Parse it positionally. Scanning the value for any long digit run instead
    picks up digits inside the sessionid hash and reports a wildly wrong TTL, so
    the human-readable field is also used to cross-check the arithmetic.
    """
    fields = split_cookie(cookie)
    notes: list[str] = []
    for name in EXPIRY_FIELDS:
        raw = fields.get(name)
        if not raw:
            continue
        decoded = unquote_plus(raw)
        parts = decoded.split("|")
        if len(parts) >= 3 and parts[1].isdigit() and parts[2].isdigit():
            issued, ttl = int(parts[1]), int(parts[2])
            expires = issued + ttl
            days = (expires - time.time()) / 86400
            line = (f"{name}: issued={time.strftime('%Y-%m-%d %H:%M', time.gmtime(issued))}Z "
                    f"ttl={ttl}s ({ttl / 86400:.1f}d) "
                    f"expires={time.strftime('%Y-%m-%d %H:%M', time.gmtime(expires))}Z ({days:+.1f} days)")
            if len(parts) >= 4 and parts[3].strip():
                stated = parts[3].strip()
                try:
                    parsed = parsedate_to_datetime(stated)
                    drift = abs(parsed.timestamp() - expires)
                    line += f" [cookie states: {stated}]"
                    if drift > 86400:
                        line += f" WARNING: computed expiry differs from the stated one by {drift / 86400:.1f}d"
                except Exception:  # noqa: BLE001 - the stated field is only a cross-check
                    line += f" [cookie states: {stated} (unparseable)]"
            notes.append(line)
            continue
        notes.append(f"{name}: present but no structured expiry ({decoded[:40]!r})")
    return "; ".join(notes) if notes else "no sid_guard/passport_auth_status in the cookie; expiry unknown"


def read_cookies(path: str) -> list[str]:
    with open(path, "r", encoding="utf-8-sig") as fh:
        return [ln.strip() for ln in fh if ln.strip() and not ln.strip().startswith("#")]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cookie-file", required=True)
    ap.add_argument("--egress", default="direct", choices=["direct", "relay"])
    ap.add_argument("--relay", default=os.getenv("DOLA_RELAY_URL", RELAY_DEFAULT))
    ap.add_argument("--timeout", type=float, default=25.0)
    ap.add_argument("--json", action="store_true", help="machine-readable output")
    args = ap.parse_args()

    cookies = read_cookies(args.cookie_file)
    if not cookies:
        print(f"{args.cookie_file} contains no cookies", file=sys.stderr)
        return 2

    relay = args.relay if args.egress == "relay" else None
    print(f"cookie file : {args.cookie_file}")
    print(f"egress      : {args.egress}" + (f" ({args.relay})" if relay else ""))
    print(f"cookies     : {len(cookies)}")
    print()

    reports = []
    ok_count = 0
    for i, cookie in enumerate(cookies, 1):
        fields = split_cookie(cookie)
        missing = [f for f in REQUIRED_FIELDS if not fields.get(f)]
        lacking = [f for f in RECOMMENDED_FIELDS if not fields.get(f)]
        label = fields.get("sessionid", "")[:8] or "(no sessionid)"

        if missing:
            verdict, detail = "INVALID", f"missing required field(s): {', '.join(missing)}"
            result = {}
        else:
            try:
                result = post(cookie, relay=relay, timeout=args.timeout)
                verdict, detail = classify(result)
            except Exception as exc:  # noqa: BLE001 - every failure mode is reported
                result = {}
                verdict, detail = "NETWORK", f"{type(exc).__name__}: {exc}"

        if verdict == "OK":
            ok_count += 1

        report = {
            "index": i,
            "sessionid_prefix": label,
            "verdict": verdict,
            "detail": detail,
            "missing_recommended": lacking,
            "expiry": decode_expiry(cookie),
            "http_status": result.get("status"),
            "elapsed_ms": result.get("ms"),
        }
        reports.append(report)

        print(f"[{verdict:<12}] cookie #{i}  sessionid={label}...")
        print(f"               {detail}")
        if lacking:
            print(f"               missing recommended field(s): {', '.join(lacking)} (higher captcha risk)")
        print(f"               {report['expiry']}")
        if result.get("ms"):
            print(f"               http={result['status']} in {result['ms']}ms")
        print()

    if args.json:
        with open("spikes/verify_result.json", "w", encoding="utf-8") as fh:
            json.dump(reports, fh, ensure_ascii=False, indent=2)
        print("wrote spikes/verify_result.json")

    print(f"summary: {ok_count}/{len(cookies)} usable")
    if ok_count == 0:
        print("No usable cookie: re-copy it from a logged-in browser session (see spikes/COOKIE.md).")
        return 1
    print("Next: put the usable line(s) into accounts.json on the router, or run the protocol probe.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
