"""Is the gate the client's TLS/HTTP2 fingerprint?

Established facts that motivate this probe:
  * In a real Chrome, the submit succeeds with EXACTLY the headers our
    out-of-browser client sends ({"Accept":"*/*","Content-Type":"application/json",
    "agw-js-conv":"str, str"}) and the same body/params -- captured in-page.
  * From the same machine and the same IP, the plain Python client gets a 302
    (direct) or 718013001 (through the relay).
  * Same IP, same cookie, same headers, different outcome => the server is
    distinguishing clients by something below HTTP.

This script isolates that one variable: libcurl's own TLS stack versus
libcurl-impersonate configured for Chrome. If impersonation flips the outcome,
the router needs a Chrome-like ClientHello (uTLS), and the port's "stdlib only,
zero dependencies" constraint has to be revisited.

Usage:
    python spikes/tls_impersonate_probe.py --cookie-file cookies.txt
    python spikes/tls_impersonate_probe.py --cookie-file cookies.txt --variants plain chrome
"""

from __future__ import annotations

import argparse
import json
import pathlib
import sys
import time
import uuid

SUBMIT_PATH = "/chat/completion"
BASE = "https://www.dola.com"


def build_body(prompt: str, ratio: str, duration: int) -> dict:
    """Mirrors video_probe.SUBMIT_JS, which is the shape the browser sends."""
    now_ms = int(time.time() * 1000)
    now_sec = now_ms // 1000
    return {
        "client_meta": {
            "local_conversation_id": f"local_{now_ms}",
            "conversation_id": "",
            "bot_id": "7339470689562525703",
            "last_section_id": "",
            "last_message_index": None,
        },
        "messages": [{
            "local_message_id": str(uuid.uuid4()),
            "content_block": [{
                "block_type": 10000,
                "content": {
                    "text_block": {"text": f"Generate video: {prompt}, {ratio}",
                                   "icon_url": "", "icon_url_dark": "", "summary": ""},
                    "pc_event_block": "",
                },
                "block_id": str(uuid.uuid4()),
                "parent_id": "",
                "meta_info": [],
                "append_fields": [],
            }],
            "message_status": 0,
        }],
        "option": {
            "send_message_scene": "", "create_time_ms": now_ms, "collect_id": "",
            "is_audio": False, "answer_with_suggest": False, "tts_switch": False,
            "need_deep_think": 0, "click_clear_context": False, "from_suggest": False,
            "is_regen": False, "is_replace": False, "is_from_click_option": False,
            "disable_sse_cache": False, "select_text_action": "", "is_select_text": False,
            "resend_for_regen": False, "scene_type": 0, "unique_key": str(uuid.uuid4()),
            "start_seq": 0, "need_create_conversation": True,
            "conversation_init_option": {"need_ack_conversation": True},
            "regen_query_id": [], "edit_query_id": [], "regen_instruction": "",
            "no_replace_for_regen": False, "message_from": 0, "shared_app_name": "",
            "shared_app_id": "", "sse_recv_event_options": {"support_chunk_delta": True},
            "is_ai_playground": False, "is_old_user": False,
            "recovery_option": {"is_recovery": False, "req_create_time_sec": now_sec,
                                "append_sse_event_scene": 0},
            "message_storage_type": 0,
        },
        "chat_ability": {
            "ability_type": 17,
            "ability_param": json.dumps({"ratio": ratio, "model": "seedance_v2.0", "duration": duration}),
        },
        "user_context": [],
        "ext": {
            "fp": "",  # filled from the cookie below
            "use_deep_think": "0", "sub_conv_firstmet_type": "1", "collection_id": "",
            "conversation_init_option": '{"need_ack_conversation":true}',
            "commerce_credit_config_enable": "0",
        },
    }


def parse_cookie(raw: str) -> dict[str, str]:
    out = {}
    for part in raw.split(";"):
        part = part.strip()
        if part and "=" in part:
            k, v = part.split("=", 1)
            out[k.strip()] = v.strip()
    return out


def build_params(fields: dict[str, str]) -> dict[str, str]:
    params = {
        "aid": "495671", "channel": "g", "device_platform": "web", "language": "zh-Hant",
        "region": "JP", "sys_region": "JP", "samantha_web": "1", "use-olympus-account": "1",
        "version_code": "20800", "web_platform": "browser", "web_tab_id": str(uuid.uuid4()),
    }
    if fields.get("msToken"):
        params["msToken"] = fields["msToken"]
    if fields.get("s_v_web_id"):
        params["fp"] = fields["s_v_web_id"]
    return params


def parse_sse(text: str) -> dict:
    conv_id, events, error = "", [], None
    for raw in text.split("\n\n"):
        if not raw.strip():
            continue
        name, data = "", []
        for line in raw.split("\n"):
            if line.startswith("event:"):
                name = line[6:].strip()
            elif line.startswith("data:"):
                data.append(line[5:].strip())
        payload = "\n".join(data)
        events.append(name)
        if name == "SSE_ACK":
            try:
                conv_id = (json.loads(payload).get("ack_client_meta") or {}).get("conversation_id") or ""
            except json.JSONDecodeError:
                pass
        if name == "STREAM_ERROR":
            try:
                p = json.loads(payload)
                error = {"code": p.get("error_code"), "msg": p.get("error_msg")}
            except json.JSONDecodeError:
                error = {"raw": payload[:200]}
    return {"conv_id": conv_id, "event_names": events, "error": error}


def run_variant(label: str, impersonate: str | None, cookie: str, fields: dict, body: dict,
                params: dict, timeout: float) -> dict:
    from curl_cffi import requests as creq

    body["ext"]["fp"] = fields.get("s_v_web_id", "")
    headers = {
        "Content-Type": "application/json",
        "agw-js-conv": "str, str",
        "Accept": "*/*",
        "Cookie": cookie,
    }
    kwargs = {"headers": headers, "params": params, "json": body, "timeout": timeout, "stream": True}
    if impersonate:
        kwargs["impersonate"] = impersonate

    t0 = time.perf_counter()
    try:
        r = creq.post(BASE + SUBMIT_PATH, **kwargs)
    except Exception as exc:  # noqa: BLE001 - the transport failure is the finding
        return {"variant": label, "transport_error": f"{type(exc).__name__}: {exc}"}

    chunks = []
    try:
        for chunk in r.iter_content():
            chunks.append(chunk)
            # The verdict arrives in the first frames; stop early so a success does
            # not sit through the whole generation.
            joined = b"".join(chunks).decode("utf-8", "replace")
            if "SSE_ACK" in joined or "STREAM_ERROR" in joined:
                break
    except Exception as exc:  # noqa: BLE001
        return {"variant": label, "status": r.status_code, "read_error": f"{type(exc).__name__}: {exc}"}

    text = b"".join(chunks).decode("utf-8", "replace")
    parsed = parse_sse(text)
    return {
        "variant": label,
        "impersonate": impersonate or "(libcurl default)",
        "status": r.status_code,
        "content_type": r.headers.get("content-type"),
        "agw_login": r.headers.get("x-tt-agw-login"),
        "elapsed_s": round(time.perf_counter() - t0, 2),
        "bytes": len(text),
        "conversation_id": parsed["conv_id"],
        "event_names": parsed["event_names"][:8],
        "error": parsed["error"],
        "body_head": text[:200],
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cookie-file", default="cookies.txt")
    ap.add_argument("--variants", nargs="+", default=["plain", "chrome"],
                    help="plain = libcurl TLS, chrome = impersonate Chrome")
    ap.add_argument("--impersonate-target", default="chrome")
    ap.add_argument("--prompt", default="a cat sitting on a windowsill")
    ap.add_argument("--ratio", default="9:16")
    ap.add_argument("--duration", type=int, default=10)
    ap.add_argument("--timeout", type=float, default=60.0)
    ap.add_argument("--out", default="spikes/tls_impersonate_result.json")
    args = ap.parse_args()

    lines = [ln.strip() for ln in pathlib.Path(args.cookie_file).read_text(encoding="utf-8-sig").splitlines()
             if ln.strip() and not ln.strip().startswith("#")]
    if not lines:
        print(f"{args.cookie_file} has no cookie lines", file=sys.stderr)
        return 2
    cookie = lines[0]
    fields = parse_cookie(cookie)

    print(f"target      : {BASE}{SUBMIT_PATH}")
    print(f"egress      : direct (this machine, same as the browser that succeeded)")
    print(f"variants    : {args.variants}")
    print()

    results = []
    for v in args.variants:
        body = build_body(args.prompt, args.ratio, args.duration)
        params = build_params(fields)
        imp = None if v == "plain" else args.impersonate_target
        rec = run_variant(v, imp, cookie, fields, body, params, args.timeout)
        results.append(rec)
        print(json.dumps(rec, ensure_ascii=False, indent=2))
        print()

    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(results, fh, ensure_ascii=False, indent=2)
    print(f"wrote {args.out}")

    verdict = "UNRESOLVED"
    plain = next((r for r in results if r["variant"] == "plain"), None)
    chrome = next((r for r in results if r["variant"] == "chrome"), None)
    if chrome and chrome.get("conversation_id"):
        verdict = "TLS_FINGERPRINT" if not (plain and plain.get("conversation_id")) else "NOT_TLS"
    print(f"\nverdict: {verdict}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
