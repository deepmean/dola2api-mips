"""Dump the creation blocks of a conversation, field by field.

The port extracts video URLs from a creation of type 2. A live generation came
back with a creation of type 1 instead, so the question is what that block
actually is and where its URL lives.

Usage:
    python spikes/dump_creations.py <conversation_id> <cookie_file>
"""

from __future__ import annotations

import base64
import json
import sys

from poll_both import poll


def walk(node, path="", depth=0, out=None):
    """Collect every leaf, so nothing is missed by guessing field names."""
    if out is None:
        out = []
    if depth > 8:
        return out
    if isinstance(node, dict):
        for k, v in node.items():
            walk(v, f"{path}.{k}" if path else k, depth + 1, out)
    elif isinstance(node, list):
        for i, v in enumerate(node):
            walk(v, f"{path}[{i}]", depth + 1, out)
    else:
        out.append((path, node))
    return out


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    conversation, cookie_file = sys.argv[1], sys.argv[2]

    import pathlib
    cookie = [l.strip() for l in pathlib.Path(cookie_file).read_text(encoding="utf-8-sig").splitlines()
              if l.strip() and not l.startswith("#")][0]

    status, text = poll(cookie, conversation)
    print(f"http {status}, {len(text)} bytes")
    data = json.loads(text)
    chain = ((data.get("downlink_body") or {}).get("pull_singe_chain_downlink_body") or {})
    messages = chain.get("messages") or []
    print(f"messages: {len(messages)}\n")

    for mi, msg in enumerate(messages):
        content = msg.get("content")
        if isinstance(content, str):
            try:
                content = json.loads(content)
            except json.JSONDecodeError:
                continue
        if not isinstance(content, list):
            continue
        for block in content:
            btype = block.get("block_type")
            inner = block.get("content") or {}
            if btype == 10000:
                text_block = inner.get("text_block") or {}
                print(f"message {mi}: block 10000 (text): {str(text_block.get('text'))[:120]!r}")
                continue
            print(f"message {mi}: block {btype}")
            print(f"  content keys: {sorted(inner)}")
            creations = (inner.get("creation_block") or {}).get("creations") or []
            print(f"  creations: {len(creations)}")
            for ci, creation in enumerate(creations):
                print(f"    creation[{ci}] top-level keys: {sorted(creation)}")
                print(f"      type={creation.get('type')}  status={creation.get('status')}")
                for key in ("video", "image", "images", "creation", "result", "media"):
                    if key not in creation:
                        continue
                    value = creation[key]
                    print(f"      {key}: {type(value).__name__}")
                    leaves = walk(value)
                    for path, leaf in leaves:
                        shown = leaf
                        if isinstance(leaf, str) and len(leaf) > 90:
                            # video_model carries base64, so show whether it decodes
                            try:
                                decoded = base64.b64decode(leaf, validate=True).decode("utf-8", "ignore")
                                if decoded.startswith("http"):
                                    shown = f"<base64 -> {decoded[:80]}>"
                                else:
                                    shown = leaf[:80] + "..."
                            except Exception:  # noqa: BLE001
                                shown = leaf[:80] + "..."
                        if isinstance(shown, str) and ("url" in path.lower() or "http" in str(shown)[:5]
                                                       or "base64" in str(shown)[:10]):
                            print(f"        {path} = {shown}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
