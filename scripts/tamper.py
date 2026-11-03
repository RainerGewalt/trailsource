#!/usr/bin/env python3
"""Change one value in a copy of a journal, for the tamper scenario.

    tamper.py <in.jsonl> <out.jsonl> [--line N] [--reseal]

Without --reseal the record keeps its old hashes, which is what a plain edit
in a text editor does. With --reseal the edited record gets new
content_hash and chain_hash computed by the stdlib evidence library in
adapters/pylib, which is what someone who knows the format would do. The
input file is never modified.

Without --line the first source.value.observed record with a numeric value
at line 10 or later is used. Stdlib only, Python 3.7+.
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "adapters", "pylib"))
from trailsource_evidence import seal  # noqa: E402


def pick_line(lines):
    for i, raw in enumerate(lines, start=1):
        if i < 10 or not raw.strip():
            continue
        rec = json.loads(raw)
        value = (rec.get("payload") or {}).get("value")
        if rec.get("event_type") == "source.value.observed" and isinstance(value, (int, float)) and not isinstance(value, bool):
            return i
    raise SystemExit("tamper: no numeric value record at line 10 or later")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("src")
    ap.add_argument("dst")
    ap.add_argument("--line", type=int, default=0)
    ap.add_argument("--reseal", action="store_true")
    args = ap.parse_args()

    with open(args.src, "r", encoding="utf-8") as fh:
        lines = fh.read().splitlines()
    n = args.line or pick_line(lines)
    rec = json.loads(lines[n - 1])
    old = rec["payload"]["value"]
    new = old + 1 if isinstance(old, int) else round(old + 1.5, 6)
    rec["payload"]["value"] = new
    if args.reseal:
        seal(rec)
    lines[n - 1] = json.dumps(rec, ensure_ascii=False, separators=(",", ":"))
    with open(args.dst, "w", encoding="utf-8") as fh:
        fh.write("\n".join(lines) + "\n")
    print("tamper: line %d seq %s %s value %r -> %r%s" % (
        n, rec.get("seq"), rec["payload"].get("browse_path", ""), old, new,
        ", record re-sealed" if args.reseal else ", hashes left as they were"))


if __name__ == "__main__":
    main()
