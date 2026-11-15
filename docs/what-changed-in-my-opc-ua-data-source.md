# What changed in my OPC UA data source?

An OPC UA server that restarts after a firmware or project update can come
back with a different address space, and a client that simply reconnects
keeps no record of the difference. To answer the question later you need
three things written down at the time: a snapshot of the variables the
client saw, a comparison on every reconnect that ignores browse order and
namespace index, and the begin and end of every connection gap. Without
them, "nothing changed" and "two variables were added during a short
outage" look the same in the stored data.

Below is a run you can repeat in about half a minute. It needs no
hardware, no Docker and no account.

Every output on this page comes from one run on 15 November 2026 with
TrailSource 0.1.2 (tag `v0.1.2`) against the bundled simulator, on Linux
with Go 1.26.11, Mosquitto 1.6.9 and Python 3.11. The results hold for that
version and that setup.

## Why a plain reconnect hides the change

- **A namespace index can change.** A server may give a namespace a
  different index after a restart; only the namespace URI is stable. The
  same variable can be `ns=2;s=Plant.Line01…` before the restart and
  `ns=3;s=Plant.Line01…` after it. A comparison of raw NodeIds reports that every node changed.
- **Browse order is not stable.** A server may return the same references
  in a different order. A comparison of browse results in sequence reports
  a change where there is none.
- **A new variable is silent.** A client that re-creates its old
  subscriptions gets its old values back. Nothing tells it that the server
  now offers two more variables, or that one changed its data type.

A useful comparison therefore works on the namespace URI and the path of
each variable, with its node class and data type, and treats a reorder or
a renumbered namespace as no change.

## The run

The scenario starts a simulated extruder with 28 variables, stops the
server after a few seconds and starts a changed one: references reversed,
the namespace moved from index 2 to 3, and a seventh barrel zone with two
new variables.

```sh
git clone --branch v0.1.2 https://github.com/RainerGewalt/trailsource && cd trailsource
make sim-venv                  # once: installs the simulator's asyncua into .demo/sim-venv
make scenario-server-restart
```

Requirements: Go 1.26.11 or newer, `mosquitto` on `PATH`, Python 3.9 or
newer. Ports 18830 and 48400 on 127.0.0.1; move them with
`SCENARIO_MQTT_PORT` and `SCENARIO_OPC_PORT` if they are taken.

```
== stopping the server
   seen: source.connection_lost

== starting a changed server: references reversed, namespace moved from index 2 to 3, a seventh barrel zone
   seen: source.connection_restored
   seen: source.node_added: only the new zone, nothing for the reorder or the new index

== bindings (scripts/check_bindings.py)
bindings: 30 entity_keys, 30 nodes, 28 read through more than one native NodeId (renumbered namespace)
last inventory change: {'added': 2, 'removed': 0, 'changed': 0}, reason_code source.node_added
```

## What the report says

The run ends with `trailsource report`, as text and as one self-contained
HTML file (`.demo/scenarios/server-restart/report.html`):

```
Verify   OK: 141 records in 1 stream(s), 0 error(s), 1 notice(s)
         All records in this journal pass hash verification, and every chain
         link holds from each stream's first record in this file onwards.

Stream plant-a-ext-01-extruder-01-opcua
  Values    30 nodes; quality good 134, uncertain 0, bad 0; without source timestamp 0

  Address space generations (2)
    gen 1   seq 2      2026-11-15T20:52:30.044Z  source.inventory_initial   28 nodes  sha256:f276db9dd6d9...
    gen 2   seq 69     2026-11-15T20:52:41.089Z  source.node_added          30 nodes  sha256:f7e48ebc0b07...
            added 2, removed 0, changed 0
            + Plant.Line01.Extruder01.BarrelZone07.TempActualC (float64)
            + Plant.Line01.Extruder01.BarrelZone07.TempSetpointC (float64)

  Gaps (1)
    connection 2026-11-15T20:52:35.051Z .. 2026-11-15T20:52:41.071Z  6.02s
               source.connection_lost opc connection state Closed
               last value before the gap: 2026-11-15T20:52:34.048Z
```

Three answers are in there:

- **What changed:** two variables were added. The reversed references and
  the new namespace index produced no entry, and the 28 existing variables
  kept their identity although each was now read through a different
  native NodeId.
- **Where data is missing:** one gap of 6.02 seconds, with its reason and
  the time of the last value before it.
- **Which address space a value belongs to:** every value record carries
  the generation and fingerprint it was read against. A value from before
  the restart names generation 1, one after it generation 2.

## Checking the journal with a second tool

The journal is a JSON Lines file in which every record carries the hash of
the one before it. `trailsource verify` checks it offline. The same file
verifies with the
[trail-evidence](https://github.com/RainerGewalt/trail-evidence) reference
verifier, a separate program that TrailSource does not import:

```sh
go install github.com/RainerGewalt/trail-evidence/go/cmd/trail-evidence@v0.2.2
trail-evidence verify .demo/scenarios/server-restart/journal/plant-a-ext-01-extruder-01-opcua.jsonl
```

```
VALID: 141 record(s) in 1 stream(s), hash chain intact, 1 restart(s)
  notice  restart           record #0 (line 1, stream "plant-a-ext-01-extruder-01-opcua", seq 0): chain restart at seq 0, anchor sha256:0000000000000000000000000000000000000000000000000000000000000000
```

The run of 15 November gave this result with trail-evidence v0.2.1 and
v0.2.2.

## What this does not show

- A verified journal is internally consistent: no record was edited,
  removed, inserted or reordered without re-sealing everything after it.
  The records are not signed. Whoever controls the file can rewrite and
  re-seal all of it, and records cut off at the end are not detectable
  without an anchor outside the file.
- Nothing in a journal shows that the values the server delivered were
  true.
- The run uses a simulator with 30 variables. Servers with thousands of
  monitored items have not been tested.
- TrailSource reads OPC UA and nothing else. It never writes to the
  server, and it makes no statement about regulatory compliance.

## With your own server

[Connect your server](CONNECT_YOUR_SERVER.md) walks through endpoint,
security policy, trust list and browse roots. If the run above or your own
server gives a result you did not expect, an
[evaluation report](https://github.com/RainerGewalt/trailsource/issues/new/choose)
is the most useful thing you can send; it is public and voluntary.
