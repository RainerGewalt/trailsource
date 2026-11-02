"""Sealing and verification of Trail Evidence Records v1."""
from __future__ import annotations

import hashlib
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional

from .jcs import canonical_jcs

SCHEMA = "trail.evidence/1"
HASH_PREFIX = "sha256:"
GENESIS_HASH = HASH_PREFIX + "0" * 64

CORE_MEMBERS = ("schema", "producer", "stream_id", "seq", "prev_hash", "event_type",
                "time", "actor", "policy", "content_hash")


def sha256_hash(value: Any) -> str:
    return HASH_PREFIX + hashlib.sha256(canonical_jcs(value)).hexdigest()


def is_hash(s: Any) -> bool:
    return (isinstance(s, str) and s.startswith(HASH_PREFIX) and len(s) == len(HASH_PREFIX) + 64
            and all(c in "0123456789abcdef" for c in s[len(HASH_PREFIX):]))


def no_policy() -> Dict[str, str]:
    return {"id": "none", "version": "0", "hash": GENESIS_HASH}


def utc_now() -> datetime:
    return datetime.now(timezone.utc)


def format_time(t: datetime) -> str:
    """RFC 3339 UTC with millisecond precision."""
    t = t.astimezone(timezone.utc)
    return t.strftime("%Y-%m-%dT%H:%M:%S.") + "%03dZ" % (t.microsecond // 1000)


def format_time_micro(t: datetime) -> str:
    t = t.astimezone(timezone.utc)
    return t.strftime("%Y-%m-%dT%H:%M:%S.%f") + "Z"


def core_of(record: Dict[str, Any]) -> Dict[str, Any]:
    core = {k: record[k] for k in CORE_MEMBERS if k in record}
    for k in ("chain_restart", "anchor_hash"):
        if k in record:
            core[k] = record[k]
    return core


def _validate(record: Dict[str, Any]) -> None:
    errors = []
    if record.get("schema") != SCHEMA:
        errors.append("schema must be %r" % SCHEMA)
    producer = record.get("producer") or {}
    if not all(producer.get(k) for k in ("kind", "id", "version")):
        errors.append("producer.kind, producer.id and producer.version are required")
    if not record.get("stream_id"):
        errors.append("stream_id is required")
    if not is_hash(record.get("prev_hash")):
        errors.append("prev_hash is not a sha256 hash")
    if not record.get("event_type"):
        errors.append("event_type is required")
    time_block = record.get("time") or {}
    if not time_block.get("recorded_at") or not time_block.get("source"):
        errors.append("time.recorded_at and time.source are required")
    if not (record.get("actor") or {}).get("auth"):
        errors.append("actor.auth is required")
    policy = record.get("policy") or {}
    if not policy.get("id") or not policy.get("version") or not is_hash(policy.get("hash")):
        errors.append("policy.id, policy.version and a sha256 policy.hash are required")
    if record.get("chain_restart"):
        if not is_hash(record.get("anchor_hash")):
            errors.append("chain_restart requires anchor_hash")
        elif record.get("anchor_hash") != record.get("prev_hash"):
            errors.append("chain_restart requires prev_hash == anchor_hash")
    elif record.get("anchor_hash"):
        errors.append("anchor_hash is only allowed with chain_restart")
    if "payload" not in record:
        errors.append("payload is required")
    if errors:
        raise ValueError("; ".join(errors))


def seal(record: Dict[str, Any]) -> Dict[str, Any]:
    """Compute content_hash and chain_hash in place and return the record."""
    _validate(record)
    record["content_hash"] = sha256_hash(record["payload"])
    record.pop("chain_hash", None)
    record["chain_hash"] = sha256_hash(core_of(record))
    return record


@dataclass
class Issue:
    kind: str
    stream_id: str
    index: int
    seq: int
    detail: str

    @property
    def is_break(self) -> bool:
        return self.kind != "restart"


@dataclass
class Report:
    records: int = 0
    issues: List[Issue] = field(default_factory=list)
    streams: Dict[str, Dict[str, Any]] = field(default_factory=dict)

    @property
    def ok(self) -> bool:
        return not any(i.is_break for i in self.issues)


def verify_records(records: List[Dict[str, Any]]) -> Report:
    """Verify a list of parsed records (one or more streams)."""
    report = Report(records=len(records))
    by_stream: Dict[str, List[tuple]] = {}
    for idx, rec in enumerate(records):
        by_stream.setdefault(str(rec.get("stream_id")), []).append((idx, rec))
    for sid, recs in by_stream.items():
        recs.sort(key=lambda p: _seq(p[1]))
        _verify_stream(sid, recs, report)
    return report


def _seq(rec: Dict[str, Any]) -> int:
    s = rec.get("seq")
    return s if isinstance(s, int) and not isinstance(s, bool) else -1


def _verify_stream(sid: str, recs: List[tuple], report: Report) -> None:
    summary = {"records": 0, "restarts": 0, "gaps": 0, "breaks": 0, "last_chain_hash": ""}
    report.streams[sid] = summary
    prev_hash: Optional[str] = None
    prev_seq = -1
    first = True

    def add(kind: str, idx: int, seq: int, detail: str) -> None:
        report.issues.append(Issue(kind, sid, idx, seq, detail))
        if kind == "restart":
            summary["restarts"] += 1
        elif kind == "gap":
            summary["gaps"] += 1
        else:
            summary["breaks"] += 1

    for idx, rec in recs:
        seq = _seq(rec)
        summary["records"] += 1
        if rec.get("schema") != SCHEMA:
            add("schema", idx, seq, "unsupported schema %r" % rec.get("schema"))
        if seq < 0:
            add("schema", idx, seq, "seq missing or not a non-negative integer")
        if "payload" in rec:
            want = sha256_hash(rec["payload"])
            if want != rec.get("content_hash"):
                add("content_hash", idx, seq, "content_hash %s, recomputed %s" % (rec.get("content_hash"), want))
        else:
            add("schema", idx, seq, "payload missing")
        want_chain = sha256_hash(core_of(rec))
        got_chain = rec.get("chain_hash")
        if want_chain != got_chain:
            add("chain_hash", idx, seq, "chain_hash %s, recomputed %s" % (got_chain, want_chain))

        prev = rec.get("prev_hash")
        if rec.get("chain_restart"):
            anchor = rec.get("anchor_hash")
            if not is_hash(anchor):
                add("anchor_hash", idx, seq, "chain_restart without anchor_hash")
            elif anchor != prev:
                add("anchor_hash", idx, seq, "chain_restart with prev_hash != anchor_hash")
            elif not first and prev_hash and anchor != prev_hash:
                add("anchor_hash", idx, seq, "anchor_hash %s does not match the last chain_hash %s of the previous segment" % (anchor, prev_hash))
            else:
                add("restart", idx, seq, "restart at seq %d, anchor %s" % (seq, anchor))
        elif first:
            if prev != GENESIS_HASH:
                add("prev_hash", idx, seq, "first record prev_hash %s is not genesis and not a restart" % prev)
        else:
            if prev != prev_hash:
                add("prev_hash", idx, seq, "prev_hash %s does not match previous chain_hash %s" % (prev, prev_hash))
            if seq != prev_seq + 1:
                add("gap", idx, seq, "seq %d follows %d" % (seq, prev_seq))
        prev_hash = got_chain if isinstance(got_chain, str) else ""
        prev_seq = seq
        first = False
    summary["last_chain_hash"] = prev_hash or ""
