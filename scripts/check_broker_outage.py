#!/usr/bin/env python3
"""Check a journal of the broker-outage scenario (docs/SCENARIOS.md).

    check_broker_outage.py <journal.jsonl> [--max-read-lag-s 5]
    check_broker_outage.py <journal.jsonl> --read-after <RFC 3339 time>

The OPC UA server runs the whole time; only the MQTT broker is frozen and
then killed. Nothing in the journal may attribute that outage to the source:

- no source.subscription_stalled and no common.gap (source.data.gap);
- no source.connection.changed other than connection_established at the
  start and connection_closed at the orderly stop;
- read_at is when a value reached the adapter, not when the adapter got
  round to sealing it: for every value record with a source timestamp,
  read_at is at most --max-read-lag-s seconds after it (the simulator
  writes every value once a second with the current time as source
  timestamp, the subscription publishes every 500 ms).

With --read-after the script only answers whether the journal already holds
a value record read at or after that time (exit 0) or not (exit 1); the
scenario uses it to wait until the adapter has caught up after the outage.

Prints a short summary; exit code 1 on any violation. Stdlib only.
"""
import argparse
import json
import sys
import time
from datetime import datetime


def parse_time(s):
    if not isinstance(s, str) or not s:
        return None
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        return None


def records(path, live=False):
    with open(path, "r", encoding="utf-8") as fh:
        for line in fh:
            if live and not line.endswith("\n"):
                break  # The writer has not completed this append yet.
            if line.strip():
                yield json.loads(line)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("journal")
    ap.add_argument("--max-read-lag-s", type=float, default=5.0)
    ap.add_argument("--max-record-lag-s", type=float, default=5.0)
    ap.add_argument("--read-after", default="")
    ap.add_argument("--sample", default="")
    ap.add_argument("--samples", default="")
    ap.add_argument("--health", default="")
    args = ap.parse_args()

    if args.sample:
        count = sum(rec.get("event_type") == "source.value.observed" for rec in records(args.journal, live=True))
        print(json.dumps({"phase": args.sample, "at": time.monotonic(), "values": count}))
        return 0

    if args.read_after:
        after = parse_time(args.read_after)
        for rec in records(args.journal, live=True):
            if rec.get("event_type") != "source.value.observed":
                continue
            t = parse_time((rec.get("time") or {}).get("read_at"))
            if t is not None and after is not None and t >= after:
                return 0
        return 1

    problems = []
    values = 0
    worst = None  # (lag seconds, seq)
    worst_record_lag = 0.0
    total_records = 0
    for rec in records(args.journal):
        total_records += 1
        p = rec.get("payload") or {}
        et, code, seq = rec.get("event_type"), p.get("reason_code"), rec.get("seq")
        if et == "source.subscription.changed" or code == "source.subscription_stalled":
            problems.append("seq %s: %s %s, but the OPC UA server never stopped publishing" % (seq, et, code))
        elif et == "source.data.gap" or code == "common.gap":
            problems.append("seq %s: %s from %s to %s (cause %s), but no value was missing at the source"
                            % (seq, et, p.get("from"), p.get("to"), p.get("cause")))
        elif et == "source.connection.changed" and code not in ("source.connection_established", "source.connection_closed"):
            problems.append("seq %s: %s %s (%s), but the OPC UA session never ended" % (seq, et, code, p.get("reason")))
        elif et == "source.value.observed":
            values += 1
            read_at = parse_time((rec.get("time") or {}).get("read_at"))
            recorded_at = parse_time((rec.get("time") or {}).get("recorded_at"))
            if read_at is not None and recorded_at is not None:
                journal_lag = (recorded_at - read_at).total_seconds()
                worst_record_lag = max(worst_record_lag, journal_lag)
                if journal_lag > args.max_record_lag_s:
                    problems.append("seq %s: journal is %.3f s behind read_at; MQTT must not block local recording" % (seq, journal_lag))
            source = parse_time(p.get("source_timestamp"))
            if read_at is None or source is None:
                continue
            lag = (read_at - source).total_seconds()
            if worst is None or lag > worst[0]:
                worst = (lag, seq)
            if lag > args.max_read_lag_s:
                problems.append("seq %s: read_at %s is %.3f s after the source timestamp %s; read_at must be when the value reached the adapter"
                                % (seq, (rec.get("time") or {}).get("read_at"), lag, p.get("source_timestamp")))

    if values == 0:
        problems.append("no value records")
    print("broker outage: %d value records, largest read_at - source_timestamp %s"
          % (values, "%.3f s (seq %s)" % worst if worst else "n/a"))
    print("largest recorded_at - read_at: %.3f s" % worst_record_lag)
    if args.samples:
        samples = {s["phase"]: s for s in records(args.samples)}
        rates = {}
        for name, begin, end in (("normal", "normal_start", "normal_end"),
                                  ("frozen", "normal_end", "frozen_end"),
                                  ("absent", "frozen_end", "absent_end")):
            a, b = samples[begin], samples[end]
            rates[name] = (b["values"] - a["values"]) / (b["at"] - a["at"])
        print("journal values/s: " + ", ".join("%s=%.2f" % pair for pair in rates.items()))
        if rates["normal"] <= 0:
            problems.append("no normal-rate baseline")
        for phase in ("frozen", "absent"):
            if rates[phase] < 0.65 * rates["normal"]:
                problems.append("%s journal rate below 65%% of the measured normal rate" % phase)
    if args.health:
        with open(args.health, encoding="utf-8") as fh:
            health = json.load(fh)
        fields = ("mqtt_records_published", "mqtt_records_dropped", "mqtt_records_unconfirmed")
        if health.get("status") != "down" or health.get("mqtt_queue_depth") != 0:
            problems.append("final health is not down with an empty queue")
        if any(not isinstance(health.get(k), int) for k in fields):
            problems.append("transport counters missing from health")
        else:
            if sum(health[k] for k in fields) != total_records:
                problems.append("transport counters do not account for every journal record")
            if health["mqtt_records_dropped"] + health["mqtt_records_unconfirmed"] == 0:
                problems.append("outage has no recorded transport loss/uncertainty")
            print("transport: " + ", ".join("%s=%d" % (k, health[k]) for k in fields))
    shown = problems[:20]
    for msg in shown:
        print("FAIL: " + msg, file=sys.stderr)
    if len(problems) > len(shown):
        print("FAIL: ... %d more" % (len(problems) - len(shown)), file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
