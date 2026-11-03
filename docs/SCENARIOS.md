# Scenarios

Four short demonstrations, one command each, that show what TrailSource
records when something happens at the source: the address space changes,
the server goes away, the adapter restarts, or someone edits the journal.
They run as local processes in about half a minute each. No Grafana, no
hardware, no Docker.

```sh
make scenario-inventory-change   # which address space was valid, and what changed
make scenario-gap                # where the gap was, and why
make scenario-restart            # a restart is a notice, not a break
make scenario-tamper             # an edit is found, even a re-sealed one
make scenarios                   # all four
```

Each run writes to `.demo/scenarios/<name>/` (git-ignored): the journal,
the chain and inventory state, the logs of every process, and
`report.html`, a self-contained report you can open in a browser or send
along. The runner is `scripts/scenario.sh`.

## Requirements

- Go, to build `bin/trailsource` (`make` does that).
- `mosquitto` on `PATH`. The runner starts its own broker on
  `127.0.0.1:18830`. Without Mosquitto, start any MQTT broker on that port
  and run with `SCENARIO_BROKER=external`.
- A Python 3 with `asyncua` for the OPC UA simulator
  (`pip install -r sim/opcua/requirements.txt`). The runner looks for one
  among `python3`, `/usr/bin/python3` and `python`; set `SIM_PYTHON` to
  choose explicitly.

Ports are configurable with `SCENARIO_MQTT_PORT` and `SCENARIO_OPC_PORT`
(default 48400). The adapter configuration for all scenarios is
`configs/scenario/opcua-local.yaml`; the runner fills its placeholders.

## inventory-change

The simulator adds a seventh barrel zone (two variables) 12 seconds after it
starts. The adapter re-browses every 5 seconds in this scenario, sees a new
fingerprint and seals one `source.inventory.changed` record with
`reason_code: source.node_added`, generation 2, and a diff that lists
the two new nodes with NodeId, browse path, data type and access levels.
Every value record after it carries the new fingerprint as its
`addressspace` policy, every value before it the old one.

What the report shows:

```
  Address space generations (2)
    gen 1   seq 2      ...  source.inventory_initial   28 nodes  sha256:73e25d33f55e...
    gen 2   seq 149    ...  source.node_added          30 nodes  sha256:d249a2e98ace...
            added 2, removed 0, changed 0
            + Plant.Line01.Extruder01.BarrelZone07.TempActualC (float64)
            + Plant.Line01.Extruder01.BarrelZone07.TempSetpointC (float64)
```

The question it answers: which address space was a given value read
against, and what exactly differed from the one before.

## gap

The adapter runs against the simulator; after a few seconds the simulator
is stopped for 8 seconds (`SCENARIO_GAP_S`) and started again. The adapter
polls the session state once per second, so the loss is recorded about a
second after the last value:

1. `source.connection.changed`, `state: disconnected`,
   `reason_code: source.connection_lost`, `reason` with the error text.
2. Silent reconnect attempts every 2 seconds (`session_backoff_s`). They are
   not recorded one by one; the outage is already open.
3. `source.connection.changed`, `state: connected`,
   `reason_code: source.connection_restored` with `down_ms`, then a fresh
   browse. The
   fingerprint is unchanged, so no inventory record follows.

The chain continues without a restart: the adapter process never stopped.

```
  Gaps (1)
    connection 2026-11-03T17:23:45.752Z .. 2026-11-03T17:23:55.763Z  10.011s
               source.connection_lost opc connection state Closed
               last value before the gap: 2026-11-03T17:23:44.749Z
```

The gap is longer than the 8 seconds the simulator was off: the simulator
needs about two seconds to start, and the next connect attempt follows the
backoff. The report gives both the recorded boundaries and the last value
before the gap, so the reader can tell detection delay from outage.

## restart

The adapter is stopped with SIGTERM and started again three seconds later.
On the way down it records `source.connection_closed`. On the way up the
first record is `source.bridge.restarted` with `reason_code: common.restart`
and `chain_state: resumed`; it carries `chain_restart: true` and an
`anchor_hash` equal to the `chain_hash` of the last record before the stop,
and `seq` continues where it stopped. `trailsource verify` reports the restart as a notice:

```
OK, 122 records, 1 streams
  notice restart        line=69 seq=68 restart at seq 68, anchor sha256:a9a334ff8694...
```

The report lists the restart with its anchor and the time since the
previous record, and the stop as an adapter gap of the same length. A
restart whose anchor did not match the previous segment would be a
`chain_break` error instead; `internal/evidence` has the test for that.

## tamper

The scenario records a short journal, verifies it (OK), and then makes two
edited copies with `scripts/tamper.py`. The original stays untouched.

1. **Edit one value and leave the hashes alone**, as a text editor would.
   Verify reports two `hash_mismatch` errors on that line (content hash and
   chain hash). The next line stays clean, because it links to the hash
   that is written in the edited line.
2. **Edit the same value and re-seal that record**, as someone who knows
   the format would: new `content_hash` and `chain_hash`, computed with the
   stdlib evidence library in `adapters/pylib`. The edited line now
   verifies on its own, and the next line reports `chain_break`: its
   `prev_hash` names the old `chain_hash`.

```
tamper: line 10 seq 9 ... value 24.678826128545847 -> 26.178826, hashes left as they were
  error  hash_mismatch  line=10 seq=9 content_hash recorded sha256:ad53..., recomputed sha256:7320...
  error  hash_mismatch  line=10 seq=9 chain_hash recorded sha256:8ea6..., recomputed sha256:0901...
tamper: line 10 seq 9 ... value 24.678826128545847 -> 26.178826, record re-sealed
  error  chain_break    line=11 seq=10 prev_hash sha256:8ea6... does not match previous chain_hash sha256:0901...
```

A single edit therefore never yields both findings at once: the
specification judges every record against the hash its successor declares,
so that one damaged line does not turn the rest of the journal red. To
hide the edit completely, the editor has to re-seal every following record
up to the end of the journal. An unsigned journal permits exactly that;
this is why the record format has a signature block and why anchoring the
latest `chain_hash` somewhere else matters. TrailSource v0.1 does neither,
and says so.

Both tampered journals produce the same findings with the `trail-evidence`
reference verifier (`trail-evidence verify <file>`).

## The same scenarios against the container stack

`make demo` runs the simulators, Mosquitto and the adapters in Docker and
writes journals to `.demo/journal/`. The scenarios work there too, by hand.
These variants are documented, not scripted, and were not run as part of
the change that added the local scenarios.

| Scenario | Container stack |
|---|---|
| inventory-change | Built in: the simulator adds the zone after `SIM_ADD_NODE_AFTER_S` (120 s by default; `SIM_ADD_NODE_AFTER_S=20 make demo` for a quicker run). The adapter re-browses every 30 s (`configs/opcua-extruder.yaml`), so the record appears up to 30 s later. |
| gap | `docker compose stop opcua-sim`, wait, `docker compose start opcua-sim`. |
| restart | `docker compose restart opcua-adapter`. |
| tamper | Copy `.demo/journal/plant-a-ext-01-extruder-01-opcua.jsonl` and run `scripts/tamper.py` on the copy, as above. |

Then `make verify` and
`./bin/trailsource report .demo/journal/plant-a-ext-01-extruder-01-opcua.jsonl --html report.html`.

Two differences: the container stack keeps its state in `.demo/state/`
across `make demo` runs, so the chain and the inventory generations
continue from earlier runs (delete `.demo/` for a clean start); and the
same directory also holds the journals of the Modbus and EtherNet/IP
preview adapters, which are not part of these scenarios.
