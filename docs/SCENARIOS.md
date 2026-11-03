# Scenarios

Seven short demonstrations, one command each, that show what TrailSource
records when something happens at the source: the address space changes,
the server goes away or restarts changed, the connection goes silent, the
subscription goes silent while the session lives, the adapter restarts, or
someone edits the journal. They run as local processes
in under a minute each. No Grafana, no hardware, no Docker.

```sh
make scenario-inventory-change   # which address space was valid, and what changed
make scenario-gap                # where the gap was, and why
make scenario-restart            # a restart is a notice, not a break
make scenario-tamper             # an edit is found, even a re-sealed one
make scenario-server-restart     # the server comes back reordered and renumbered: same bindings
make scenario-half-open          # the connection stays open but goes silent
make scenario-silent-subscription  # reads answered, publish responses gone
make scenarios                   # all seven
```

Every scenario ends with `trailsource verify`, `scripts/check_bindings.py`
(every `entity_key` belongs to exactly one node and every node to exactly
one key; a node is its NodeId with the namespace URI, so a renumbered
namespace is the same node) and `trailsource report`. A failed check fails
the run.

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

## server-restart

The server goes away and comes back changed, the way a controller does
after a firmware update or a project download:

1. Simulator and adapter run; initial inventory.
2. The simulator stops. `source.connection_lost`, reason `opc connection
   state Closed`.
3. A new simulator starts with three differences: its objects and
   variables are created in reverse order, so it returns every reference
   list reversed (`SIM_REVERSE_ORDER=1`); it registers another namespace
   first, so the simulator's namespace moves from index 2 to index 3 with
   the same URI (`SIM_EXTRA_NAMESPACE=1`); and it has a seventh barrel zone
   (`SIM_BARREL_ZONES=7`).
4. `source.connection_restored` with `down_ms`, a fresh browse, and one
   inventory record whose diff the run checks to be exactly two added
   variables, nothing removed, nothing changed.

```
  Address space generations (2)
    gen 1   seq 2      ...  source.inventory_initial   28 nodes  sha256:f276db9dd6d9...
    gen 2   seq 68     ...  source.node_added          30 nodes  sha256:f7e48ebc0b07...
            added 2, removed 0, changed 0
            + Plant.Line01.Extruder01.BarrelZone07.TempActualC (float64)
            + Plant.Line01.Extruder01.BarrelZone07.TempSetpointC (float64)
```

```
bindings: 30 entity_keys, 30 nodes, 28 read through more than one native NodeId (renumbered namespace)
```

The 28 variables that existed before are now read as `ns=3;s=...` instead
of `ns=2;s=...` and keep their `entity_key`. The reversed reference order
changes nothing. This works because the configured browse root is given in
URI form (`nsu=urn:trailsource:sim:opcua;s=Plant`); with `ns=2;s=Plant`
the root would point into the other namespace after the restart and the
browse would fail with `BadNodeIdUnknown`, which is recorded but not what
you want.

## half-open

The adapter reaches the simulator through `scripts/tcp_relay.py`. After the
initial inventory the relay goes half-open (SIGUSR1): every socket stays
open, the adapter's bytes are accepted and dropped, nothing comes back, no
FIN, no RST. The OPC UA client state does not change in that situation, so
the adapter asks the server for `ServerStatus.CurrentTime` every
`liveness_interval_s` (2 s in this scenario, 10 s by default); the first
read without an answer within `request_timeout_s` (5 s) ends the session:

```
  Gaps (1)
    connection 2026-11-03T18:06:28.917Z .. 2026-11-03T18:06:37.392Z  8.475s
               source.connection_lost server not answering: liveness read of ServerStatus.CurrentTime failed after 5s: context deadline exceeded
               last value before the gap: 2026-11-03T18:06:21.910Z
```

The loss is recorded seven seconds after the last value (liveness interval
plus request timeout). Reconnect attempts during the silence fail
silently. When the relay forwards again (SIGUSR2, which also closes the
silenced connections), the next attempt succeeds:
`source.connection_restored` with `down_ms: 8475`. The chain continues
without restart, the bindings are unchanged.

## silent-subscription

The session stays healthy, but the publish cycle goes silent: the server
still answers reads and browses, yet no publish response (neither data
nor keep-alive) reaches the adapter. `scripts/tcp_relay.py --mode
drop-publish` produces exactly that: after SIGUSR1 it drops every OPC UA
PublishResponse from the server and forwards everything else, Read
responses included (on an unsecured channel, where the message type is
readable).

The scenario asks for a keep-alive period of 2 s (`keepalive_interval_s`);
the server grants MaxKeepAliveCount 4 at 500 ms, so the window is 3 s:

```
subscription 78: publishing interval 500ms, max keep-alive count 4, stall after 3s without a publish response
subscription 78 stalled (no publish response for 3.004s), replaced by 79
subscription 79 stalled (no publish response for 7.004s), replaced by 80
...
```

1. `source.subscription.changed` with `reason_code:
   source.subscription_stalled`, `last_publish_at` and the window, three
   seconds after the last publish response. One record per stall.
2. While the silence lasts the adapter keeps creating a new subscription
   (before deleting the old one) and re-adding the monitored items, every
   window, without further records. The liveness read keeps working, so
   no `source.connection_lost` is recorded; the run fails if one is.
3. When the relay forwards again (SIGUSR2), the first publish response
   ends the stall: `source.data.gap` with `reason_code: common.gap`,
   `from`, `to` and `cause: source.subscription_stalled`.

```
  Gaps (1)
    subscription 2026-11-03T18:58:36.080Z .. 2026-11-03T18:58:51.833Z  15.753s
               source.subscription_stalled declared by the producer (common.gap): values in this interval were not observed
```

The chain is intact and the journal verifies as `valid`: the gap is in
what the adapter observed, not in what it recorded, and the record says
so.

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
| server-restart | `docker compose stop opcua-sim`, then start it with `SIM_REVERSE_ORDER=1 SIM_EXTRA_NAMESPACE=1 SIM_BARREL_ZONES=7` in its environment (`docker compose run` or an override file). |
| half-open, silent-subscription | Not with the stack as it is; they need the relay between adapter and simulator. |

Then `make verify` and
`./bin/trailsource report .demo/journal/plant-a-ext-01-extruder-01-opcua.jsonl --html report.html`.

Two differences: the container stack keeps its state in `.demo/state/`
across `make demo` runs, so the chain and the inventory generations
continue from earlier runs (delete `.demo/` for a clean start); and the
same directory also holds the journals of the Modbus and EtherNet/IP
preview adapters, which are not part of these scenarios.
