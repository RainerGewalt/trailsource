#!/usr/bin/env python3
"""Check the bindings in a TrailSource journal (docs/SCENARIOS.md).

    check_bindings.py <journal.jsonl> [--expect-diff added=N,removed=N,changed=N]

- Every entity_key of the value records belongs to exactly one node, and
  every node to exactly one entity_key. A node is identified by its NodeId
  with the namespace URI instead of the index, so a server that renumbers
  its namespaces keeps the same node; a key that would follow a different
  node, or a node that got a second key, is a double identity and fails.
- With --expect-diff, the last inventory record with change_kind "changed"
  must have exactly that many added, removed and changed entries.

Prints a short summary; exit code 1 on any violation. Stdlib only.
"""
import argparse
import collections
import json
import re
import sys

NS = re.compile(r"^ns=(\d+);")


def node_key(payload):
    nid, uri = payload.get("native_node_id", ""), payload.get("namespace_uri") or ""
    return NS.sub("nsu=%s;" % uri, nid) if uri and NS.match(nid) else nid


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("journal")
    ap.add_argument("--expect-diff", default="")
    args = ap.parse_args()

    by_key = collections.defaultdict(set)
    by_node = collections.defaultdict(set)
    native = collections.defaultdict(set)
    last_changed = None
    with open(args.journal, "r", encoding="utf-8") as fh:
        for line in fh:
            if not line.strip():
                continue
            rec = json.loads(line)
            p = rec.get("payload") or {}
            if rec.get("event_type") == "source.value.observed":
                k, n = p.get("entity_key"), node_key(p)
                by_key[k].add(n)
                by_node[n].add(k)
                native[k].add(p.get("native_node_id"))
            elif rec.get("event_type") == "source.inventory.changed" and p.get("change_kind") == "changed":
                last_changed = p

    problems = []
    for k, nodes in by_key.items():
        if len(nodes) > 1:
            problems.append("entity_key %s is used by %d nodes: %s" % (k, len(nodes), sorted(nodes)))
    for n, keys in by_node.items():
        if len(keys) > 1:
            problems.append("node %s has %d entity_keys: %s" % (n, len(keys), sorted(keys)))
    rebound = sum(1 for ids in native.values() if len(ids) > 1)
    print("bindings: %d entity_keys, %d nodes, %d read through more than one native NodeId (renumbered namespace)"
          % (len(by_key), len(by_node), rebound))

    if args.expect_diff:
        want = dict(kv.split("=") for kv in args.expect_diff.split(","))
        if last_changed is None:
            problems.append("no inventory record with change_kind changed")
        else:
            diff = last_changed.get("diff") or {}
            got = {k: len(diff.get(k) or []) for k in ("added", "removed", "changed")}
            print("last inventory change: %s, reason_code %s" % (got, last_changed.get("reason_code")))
            for k, v in want.items():
                if got.get(k) != int(v):
                    problems.append("inventory diff %s: want %s, got %d (%s)" % (k, v, got.get(k), json.dumps(diff.get(k))[:300]))

    for msg in problems:
        print("FAIL: " + msg, file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
