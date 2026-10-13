"""Diagnose the /chat/completion submit: dump what actually came back.

protocol_probe.py only sees "no conversation_id returned", because
dola_client._read_sse_stream silently yields zero events for any body that is not
SSE. That is indistinguishable between a risk-control rejection, a redirect and a
malformed ACK, so this script prints the wire instead.

Reuses protocol_probe's loader and egress shim so both scripts test the same
request shape. The cookie is never printed.

It also retries with the msToken the server issues on each response
(X-Ms-Token): the cookie's msToken comes from the browser session, and if the
upstream binds it to the client, a server-issued one may behave differently.

Usage:
    python spikes/submit_diag.py --cookie-file cookies.txt --egress relay --attempts 3
    python spikes/submit_diag.py --cookie-file cookies.txt --egress proxy --attempts 3
    python spikes/submit_diag.py --cookie-file cookies.txt --egress relay --verbose
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from protocol_probe import install_egress, load_dola_client, read_cookies  # noqa: E402

REDACT = ("cookie", "set-cookie", "authorization")

# error_code values seen so far, with what they mean for this port.
ERROR_MEANING = {
    "710022004": "RISK CONTROL: slide captcha demanded (shark_admin)",
    "710022002": "RISK CONTROL: rate limited",
    "718013001": "server-side conversation creation failed (not a captcha)",
}


def redact_headers(headers) -> dict:
    out = {}
    for k, v in headers.items():
        out[k] = "<redacted>" if k.lower() in REDACT else v
    return out


def decode_body(raw: bytes) -> str:
    """Decode a response body, preferring whichever codec yields readable CJK.

    The upstream answers in UTF-8 for the ASCII JSON skeleton but has been seen
    returning GBK bytes for the Chinese error text. Decoding those as UTF-8
    mangles exactly the field that explains the failure, so try both and keep the
    one that actually decodes.
    """
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError:
        pass
    for enc in ("gbk", "gb18030", "big5"):
        try:
            candidate = raw.decode(enc)
        except UnicodeDecodeError:
            continue
        if re.search(r"[\u4e00-\u9fff]", candidate):
            return candidate
    return raw.decode("utf-8", "replace")


def unmojibake(text: str) -> str:
    """Recover text already mangled into a str by a wrong-codec decode."""
    if not text or re.search(r"[\u4e00-\u9fff]", text):
        return text
    best, best_score = text, 0
    for enc in ("cp1251", "latin-1", "cp1252"):
        try:
            candidate = text.encode(enc).decode("gbk", errors="replace")
        except (UnicodeEncodeError, UnicodeDecodeError):
            continue
        score = len(re.findall(r"[\u4e00-\u9fff]", candidate))
        if score > best_score:
            best, best_score = candidate, score
    return best


def parse_events(text: str) -> list[tuple[str, str]]:
    events = []
    for raw_event in text.split("\n\n"):
        if not raw_event.strip():
            continue
        name = ""
        data_lines = []
        for line in raw_event.split("\n"):
            if line.startswith("event:"):
                name = line[6:].strip()
            elif line.startswith("data:"):
                data_lines.append(line[5:].strip())
        events.append((name, "\n".join(data_lines)))
    return events


def summarize_events(events: list[tuple[str, str]]) -> dict:
    out: dict = {"names": [n for n, _ in events], "error_code": None, "error_msg": None, "conv_id": None}
    for name, data in events:
        if name == "SSE_ACK":
            try:
                out["conv_id"] = (json.loads(data).get("ack_client_meta") or {}).get("conversation_id")
            except json.JSONDecodeError:
                pass
        if name == "STREAM_ERROR":
            try:
                payload = json.loads(data)
            except json.JSONDecodeError:
                continue
            out["error_code"] = str(payload.get("error_code", ""))
            out["error_msg"] = unmojibake(str(payload.get("error_msg", "")))
            decision = (payload.get("extra") or {}).get("decision")
            if decision:
                try:
                    out["decision"] = json.loads(decision)
                except json.JSONDecodeError:
                    out["decision_raw"] = str(decision)[:200]
    return out


def build_submit_body(client, args) -> dict:
    """Build the submit body for the requested ability.

    Image generation is the control: it posts to the same endpoint with the same
    client and egress, so if images succeed and video does not, the problem is in
    the video ability (bot_id / ability_type / skill), not in the protocol path
    or the relay.
    """
    if args.mode == "image":
        return client._build_image_body(args.prompt, args.ratio, args.style)
    return client._build_video_body(args.prompt, args.ratio, args.duration)


def describe_body(body: dict) -> str:
    ability = body.get("chat_ability") or {}
    meta = body.get("client_meta") or {}
    return (f"ability_type={ability.get('ability_type')} "
            f"ability_param={ability.get('ability_param')} "
            f"bot_id={meta.get('bot_id')}")


async def diag(args) -> int:
    mod = load_dola_client(args.upstream)
    cookie = read_cookies(args.cookie_file)[0]
    notes = install_egress(args.egress, args.relay, args.proxy)
    print(f"egress      : {args.egress}" + (f" ({args.relay})" if args.egress == "relay" else ""))
    print(f"env         : {notes}")
    print(f"mode        : {args.mode}  attempts={args.attempts}  reuse_ms_token={args.reuse_ms_token}")
    print()

    import aiohttp

    original = aiohttp.ClientSession._request
    seen: list[dict] = []

    async def logging_request(self, method, url, **kwargs):
        seen.append({
            "method": str(method),
            "url": str(url),
            "params": dict(kwargs.get("params") or {}),
            "headers": redact_headers(kwargs.get("headers") or {}),
        })
        return await original(self, method, url, **kwargs)

    aiohttp.ClientSession._request = logging_request

    client = mod.DolaClient(cookie)
    fresh_token = ""
    results = []

    async with aiohttp.ClientSession(trust_env=True) as session:
        for attempt in range(1, args.attempts + 1):
            if fresh_token and args.reuse_ms_token:
                client.cookie = re.sub(r"msToken=[^;]*", "msToken=" + fresh_token, client.cookie)
                client.info = mod.parse_cookie(client.cookie)
                print(f"attempt {attempt}: reusing the server-issued msToken {fresh_token[:16]}...")

            body = build_submit_body(client, args)
            try:
                resp = await client._request_chat_completion(session, body, timeout=args.timeout)
            except Exception as exc:  # noqa: BLE001 - the exception is the finding
                print(f"attempt {attempt}: REQUEST FAILED {type(exc).__name__}: {exc}")
                results.append({"attempt": attempt, "transport_error": f"{type(exc).__name__}: {exc}"})
                continue

            async with resp:
                raw = await resp.read()
                new_token = resp.headers.get("X-Ms-Token") or ""
                status = resp.status
                ctype = resp.headers.get("Content-Type") or ""
                agw_login = resp.headers.get("X-Tt-Agw-Login") or ""

            text = decode_body(raw)
            events = parse_events(text)
            info = summarize_events(events)

            rec = {
                "attempt": attempt,
                "status": status,
                "content_type": ctype,
                "agw_login": agw_login,
                "bytes": len(raw),
                "event_names": info["names"],
                "error_code": info["error_code"],
                "error_msg": info["error_msg"],
                "conv_id": info["conv_id"],
                "decision": info.get("decision"),
                "used_ms_token_prefix": fresh_token[:16] if fresh_token and args.reuse_ms_token else "(cookie)",
                "raw_hex_head": raw[:240].hex() if len(raw) < 600 else "",
            }
            results.append(rec)

            print(f"attempt {attempt}: HTTP {status} {ctype} bytes={len(raw)} agw_login={agw_login}")
            print(f"            submit={describe_body(body)}")
            print(f"            events={info['names']}")
            if info["conv_id"]:
                print(f"            SSE_ACK conversation_id={info['conv_id']}  <-- SUCCESS")
            if info["error_code"]:
                meaning = ERROR_MEANING.get(info["error_code"], "unclassified")
                print(f"            error_code={info['error_code']}  {meaning}")
                print(f"            error_msg={info['error_msg']!r}")
            dec = info.get("decision")
            if isinstance(dec, dict):
                print(f"            decision: from={dec.get('from')} type={dec.get('type')} "
                      f"subtype={dec.get('subtype')} region={dec.get('region')}")
            if args.verbose:
                print("--- body ---")
                print(text[:2000])
                print("-----------")
            print()

            if new_token:
                fresh_token = new_token
            if info["conv_id"]:
                break
            if attempt < args.attempts:
                await asyncio.sleep(args.gap)

    print("--- outgoing request (last) ---")
    if seen:
        e = seen[-1]
        print(f"{e['method']} {e['url']}")
        print(f"  params: {sorted(e['params'].keys())}")
        print(f"  msToken in request: {str(e['params'].get('msToken'))[:16]}...")
        for k in ("x-relay-target", "x-relay-path", "Content-Type", "agw-js-conv"):
            if k in e["headers"]:
                print(f"  {k}: {e['headers'][k][:120]}")

    codes = [r.get("error_code") for r in results if r.get("error_code")]
    verdict = "SUCCESS" if any(r.get("conv_id") for r in results) else (
        "RISK_CONTROL" if any(c in ("710022004", "710022002") for c in codes) else
        "SERVER_ERROR" if codes else "UNKNOWN")
    print()
    print(json.dumps({"egress": args.egress, "attempts": len(results),
                      "error_codes": codes, "verdict": verdict}, ensure_ascii=False, indent=2))
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump({"egress": args.egress, "verdict": verdict, "results": results}, fh,
                  ensure_ascii=False, indent=2)
    print(f"wrote {args.out}")
    return 0 if verdict == "SUCCESS" else 1


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--upstream", default=os.getenv("DOLA_UPSTREAM", ".."))
    ap.add_argument("--cookie-file", required=True)
    ap.add_argument("--egress", default="relay", choices=["direct", "proxy", "relay"])
    ap.add_argument("--relay", default=os.getenv("DOLA_RELAY_URL", "https://vcrelay.dmean.dpdns.org/"))
    ap.add_argument("--proxy", default=os.getenv("DOLA_PROBE_PROXY", "http://127.0.0.1:10808"))
    ap.add_argument("--prompt", default="a cat sitting on a windowsill")
    ap.add_argument("--mode", default="video", choices=["video", "image"],
                    help="image is the control: same endpoint/client/egress, different ability")
    ap.add_argument("--ratio", default="9:16")
    ap.add_argument("--style", default="auto")
    ap.add_argument("--duration", type=int, default=10)
    ap.add_argument("--timeout", type=int, default=60)
    ap.add_argument("--attempts", type=int, default=1)
    ap.add_argument("--gap", type=float, default=4.0)
    ap.add_argument("--reuse-ms-token", action="store_true", default=True)
    ap.add_argument("--no-reuse-ms-token", dest="reuse_ms_token", action="store_false")
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--out", default="spikes/submit_diag_result.json")
    return asyncio.run(diag(ap.parse_args()))


if __name__ == "__main__":
    sys.exit(main())
