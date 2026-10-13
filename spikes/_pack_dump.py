import json
import pathlib
import sys

p = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "spikes/skill_pack.json")
d = json.loads(p.read_text(encoding="utf-8"))
key = [k for k in d if k.startswith("POST") and "skill_type" in k]
for k in key:
    b = json.loads(d[k]["body"])
    for name, section in (b.get("data") or {}).items():
        print("#" * 20, k, "->", name)
        meta = section.get("meta") if isinstance(section, dict) else None
        if meta:
            print("  meta keys:", list(meta.keys()))
            print("  title:", json.dumps(meta.get("title"), ensure_ascii=True))
            for opt in meta.get("option_list") or []:
                vals = [o.get("value") for o in (opt.get("options") or [])]
                print("   option type=%s value=%s show_name=%s -> %s"
                      % (opt.get("type"), json.dumps(opt.get("value"), ensure_ascii=True),
                         json.dumps(opt.get("show_name"), ensure_ascii=True),
                         json.dumps(vals, ensure_ascii=True)))
            print("   model_capability:", json.dumps(meta.get("model_capability"), ensure_ascii=True))
            extra = {kk: vv for kk, vv in meta.items()
                     if kk not in ("title", "desc", "option_list", "model_capability")}
            if extra:
                print("   other meta:", json.dumps(extra, ensure_ascii=True)[:2500])
        else:
            print("  ", json.dumps(section, ensure_ascii=True)[:3000])
