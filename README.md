<p align="center">
  <img src="docs/brand/trailsource-mark.svg" width="96"
       alt="TrailSource logo: a source point with signals read from it." />
</p>

<h1 align="center">TrailSource</h1>

A read-only OPC UA source adapter that publishes every observation as a
sealed Trail Evidence Record over MQTT and keeps a local, verifiable
journal. Written for integrators who need to know, months later, which
address space a value belonged to, what changed on the server, and where
data is missing and why.

Status: v0.1, OPC UA only, read-only. The Modbus TCP and EtherNet/IP
adapters in this repository are a preview and not part of the 0.1 gate
(see "Preview adapters" below).

![TrailSource HTML report. Verify OK: 140 records in 1 stream, 0 errors, 1 notice. Stream plant-a-ext-01-extruder-01-opcua with 133 value observations from 30 nodes, all of good quality. Address space generation 2 adds Plant.Line01.Extruder01.BarrelZone07.TempActualC and TempSetpointC. One connection gap of 6.007 seconds with the reason source.connection_lost and the last value before the gap.](docs/screenshots/report-server-restart.png)

*`trailsource report --html` for the journal of `make
scenario-server-restart`, a local run against the bundled simulator: the
server comes back after a six-second gap with two new variables, and the
report names both the gap and the new address-space generation.*

## Three questions this answers

Each answer is one command, runs locally in about half a minute against the
bundled simulator, and needs neither Grafana nor hardware nor Docker. Go
1.24 or newer, Mosquitto and Python 3.9 or newer for the simulator are
enough: `make sim-venv` installs `asyncua` into `.demo/sim-venv` (see
`docs/SCENARIOS.md`).

**Which address space was valid?**

```sh
make scenario-inventory-change
```

The simulator adds two nodes at runtime. Every value record names the
fingerprint and generation of the address space it was read against; the
change itself is one `source.inventory.changed` record with
`reason_code: source.node_added` and the diff: which nodes, with which
NodeId, data type and access level.

**What has changed since it was recorded?**

```sh
make scenario-tamper
```

One value in a copy of the journal is edited. Verification reports
`hash_mismatch` on that line. If the edited record is also re-sealed, the
line passes and the next one reports `chain_break`.

**Where was the gap?**

```sh
make scenario-gap
```

The simulator stops for a few seconds. The adapter records
`source.connection_lost` with the reason about a second after the last
value, reconnects, records `source.connection_restored`, and the chain
continues. The report shows the gap with begin, end and duration.

`make scenario-restart` adds the fourth case: an adapter restart is a
`source.bridge.restarted` record with `common.restart`, anchored to the
last hash before the stop, and verification reports it as a notice, not as
an error. `make scenario-server-restart` brings the server back with
reversed references, a renumbered namespace and two new variables: the
diff is the two variables, and every other `entity_key` stays. `make
scenario-half-open` keeps the connection open but silent: the loss is a
record seven seconds later, with its reason. `make
scenario-silent-subscription` keeps the session answering but drops every
publish response: the stall is a record after the keep-alive window, the
subscription is rebuilt, and the missed interval is declared as a gap.

## Report

`trailsource report` turns a journal into something a person reads: per
stream the period, records per event type, value quality, address-space
generations with their diff, gaps with begin, end, duration and reason,
restarts, the state at the end, and the verification result.

```sh
./bin/trailsource report journal/plant-a-ext-01-extruder-01-opcua.jsonl
./bin/trailsource report journal/plant-a-ext-01-extruder-01-opcua.jsonl --html report.html
```

```
Verify   OK: 141 records in 1 stream(s), 0 error(s), 1 notice(s)

Stream plant-a-ext-01-extruder-01-opcua
  Period    2026-11-03T17:23:39.721Z .. 2026-11-03T17:23:59.958Z (20.237s)
  At end    source.connection_closed, last seen 2026-11-03T17:23:59.784Z
  Records   141
    source.value.observed           135
    source.connection.changed         4
    source.bridge.restarted           1
    source.inventory.changed          1
  Values    28 nodes; quality good 135, uncertain 0, bad 0; without source timestamp 0

  Address space generations (1)
    gen 1   seq 2      2026-11-03T17:23:39.746Z  source.inventory_initial   28 nodes  sha256:f276db9dd6d9...

  Gaps (1)
    connection 2026-11-03T17:23:45.752Z .. 2026-11-03T17:23:55.763Z  10.011s
               source.connection_lost opc connection state Closed
               last value before the gap: 2026-11-03T17:23:44.749Z
```

`--html` writes one self-contained file without scripts or external
resources, readable on a phone, in light and dark. `--json` prints the same
content for other tools. The report reads the journal and never changes it;
`trailsource verify` is the command whose exit code says whether the chain
holds.

## Where this fits

TrailSource runs next to Node-RED, Telegraf or HiveMQ Edge, not instead of
them: those tools move far more protocols into MQTT and into databases.
TrailSource adds what they do not record: which address space, session and
configuration a value was observed under, address-space changes and
connection gaps as events of their own, and a hash chain that shows later
edits. If that provenance is not needed, one of those tools alone is the
better choice.

## What it does

- Connects to an OPC UA server (None, Sign or SignAndEncrypt; anonymous or
  username). A secured session uses only a server certificate from the
  OPC UA trust list in `pki_dir` (plus an optional pin); a refused one is
  kept in `rejected/` and recorded. It browses the address space below
  configured roots and computes a fingerprint over the sorted normalized
  paths, NodeIds, node classes and data types of every Variable node, so a
  reorder or a renumbered namespace is no change and a rename or move is.
  Each variable's `entity_key` survives reorders and restarts. The browse
  is batched per group of siblings, so a few thousand nodes take seconds,
  not minutes.
- Persists that snapshot and diffs it on every connect and re-browse, and
  when the server deletes a monitored node. A different fingerprint
  produces one `source.inventory.changed` record with the added, removed
  and changed nodes, the access levels the session saw, and the session
  identity.
- Subscribes to all Variable nodes and emits one `source.value.observed`
  record per data change with three timestamps (source, server, read), the
  StatusCode, the native NodeId, a stable `entity_key`, a `semantic_role`
  from a versioned mapping file, and the inventory fingerprint as the
  record's `addressspace` policy. Every OPC UA builtin type, arrays,
  ExtensionObjects and decoded structures are encoded so that they hash
  identically in every RFC 8785 implementation.
- Records every session start, loss, failed attempt and orderly stop as
  `source.connection.changed` with a reason code from the shared
  [trail-evidence](https://github.com/RainerGewalt/trail-evidence) catalog
  (`docs/STATUS.md`). The
  retained health topic carries the state of the source as `last_event`
  and `last_seen`, and an MQTT Last Will.
- Seals every record into a per-source hash chain (`seq`, `prev_hash`,
  `content_hash`, `chain_hash`). Sequence numbers survive restarts; the
  first record after a start, `source.bridge.restarted` with
  `reason_code: common.restart`, carries `chain_restart` and anchors to the
  last persisted hash.
- Hashes its own configuration into `config_version` with secret keys
  masked, and appends every record to a local JSON Lines journal that
  `trailsource verify` checks offline with the finding classes of the
  Trail Evidence Record specification.

The record format is the Trail Evidence Record v1 (see
`docs/RECORD_MAPPING.md`). Canonicalisation uses `github.com/gowebpki/jcs`;
journals written by TrailSource verify with the
[trail-evidence](https://github.com/RainerGewalt/trail-evidence) v0.2.0
reference verifier with the same findings. It comes as a Go binary,
`trail-evidence`, and as a Python package whose command is
`trail-evidence-py`:

```sh
go install github.com/RainerGewalt/trail-evidence/go/cmd/trail-evidence@v0.2.0
trail-evidence verify journal/<source-id>.jsonl

pip install trail-evidence==0.2.0
trail-evidence-py verify journal/<source-id>.jsonl
```

## What it does not do

- It does not write. No OPC UA Write or Call, in any configuration.
- It does not sign records, notarise anything, or make any statement about
  regulatory compliance. A verified journal proves that the journal is
  internally consistent and unchanged since sealing, nothing more. Whoever
  controls the file can rewrite and re-seal all of it.
- It is not a broker, historian, dashboard or rules engine.

## Install

Binaries for Linux, macOS and Windows (amd64, arm64) are attached to every
[release](https://github.com/RainerGewalt/trailsource/releases), with
`checksums.txt`. Each archive holds `trailsource`, `LICENSE`, `NOTICE`,
this README and `docs/CONNECT_YOUR_SERVER.md`.

With Go 1.24 or newer:

```sh
go install github.com/RainerGewalt/trailsource/cmd/trailsource@latest
```

Building from a clone (`make build`, which the scenarios run first) needs
the same Go 1.24 or newer. An older `go` with the default
`GOTOOLCHAIN=auto` downloads 1.24 on first use; with `GOTOOLCHAIN=local`
it stops with an error.

Container image (linux/amd64, linux/arm64, runs as uid 10001, contains the
OPC UA adapter only):

```sh
docker pull ghcr.io/rainergewalt/trailsource:0.1.0
docker run --rm ghcr.io/rainergewalt/trailsource:0.1.0 endpoints opc.tcp://192.0.2.10:4840
```

Against a server of your own: write the configuration as described in
`docs/CONNECT_YOUR_SERVER.md` with container paths (`pki_dir: /pki`,
`state_dir: /state`, `journal.path: /journal/<source-id>.jsonl`,
`mapping_file: /config/mapping/<file>.yaml` or no `mapping_file` at all)
and an MQTT broker the container can reach. Create the directories before
the first run: Docker creates a missing bind-mount directory as root, and
the container's user then cannot write to it (`permission denied`).

```sh
mkdir -p pki state journal configs/mapping
docker run --rm --read-only --user "$(id -u):$(id -g)" \
  -v "$PWD/my-server.yaml:/config/trailsource.yaml:ro" \
  -v "$PWD/configs/mapping:/config/mapping:ro" \
  -v "$PWD/pki:/pki" -v "$PWD/state:/state" -v "$PWD/journal:/journal" \
  -e TRAILSOURCE_OPCUA_USERNAME -e TRAILSOURCE_OPCUA_PASSWORD \
  ghcr.io/rainergewalt/trailsource:0.1.0 run --config /config/trailsource.yaml
```

TrailSource only reads from the server; `--read-only` also keeps the
container's own filesystem read-only, so the three writable mounts are
the only places it writes (trust list decisions in `pki/rejected`, chain
and inventory state, journal). The mapping directory is mounted read-only
and is only read when the configuration sets `mapping_file`; without one,
every node gets a role derived from its name. Run `browse` instead of `run`
with the same mounts for a first look without MQTT.

## Quick start

```sh
make scenarios                # all seven scenarios (docs/SCENARIOS.md), local processes
```

Your own server, step by step (endpoints, security, certificate, first
browse, mapping file, run): `docs/CONNECT_YOUR_SERVER.md`. The short form,
with a configuration that uses local paths (`state_dir: state`,
`journal.path: journal/<source-id>.jsonl`, a reachable `mqtt.broker`; step
5 of that guide lists every field to change):

```sh
make build
./bin/trailsource endpoints opc.tcp://192.0.2.10:4840
./bin/trailsource browse --config my-server.yaml --mapping-out configs/mapping/my-server.yaml
./bin/trailsource run --config my-server.yaml            # with mapping_file: configs/mapping/my-server.yaml
./bin/trailsource verify journal/*.jsonl
./bin/trailsource report journal/<source-id>.jsonl --html report.html
```

With Docker, the demo stack (simulators, Mosquitto, adapters):

```sh
make demo
mosquitto_sub -h localhost -t 'trailsource/#' -v
make verify
make demo-down
```

## Commands

```
trailsource run --config <file>                  run the OPC UA adapter
trailsource endpoints <opc.tcp://...>            security policies, modes, token types, server certificate
trailsource browse --config <file>               one read-only browse, optional --mapping-out
trailsource gen-cert --uri <urn>                 client certificate for Sign and SignAndEncrypt
trailsource verify [--json] <journal>...         verify journals
trailsource report <journal> [--html <file>]     readable report, text, HTML or --json
trailsource config-version --config <file>       the config_version every record carries
trailsource version                              the version every record carries (also --version)
```

## Topics

```
trailsource/<site>/<line>/<device>/values      source.value.observed
trailsource/<site>/<line>/<device>/inventory   source.inventory.changed
trailsource/<site>/<line>/<device>/status      source.connection.changed
trailsource/<site>/<line>/<device>/health      retained state, with Last Will
trailsource/<site>/<line>/<device>/nodes/<p>   optional per-node copy of values
```

Retain is used for health only. Records are events and are never retained.

## Configuration

One YAML file per source (`configs/opcua-extruder.yaml` is the commented
example), `${VAR}` placeholders for secrets, and a mapping file for
semantic roles, units and scaling (`configs/mapping/extruder.yaml`).
`trailsource config-version` prints the hash that every record of that
adapter carries.

## Preview adapters

`adapters/modbus` (Modbus TCP) and `adapters/ethernetip` (EtherNet/IP
through a simulator shim, no CIP stack) are Python adapters that publish
the same record format from a configured point or tag list. They are a
preview: not part of the 0.1 gate, not covered by the scenarios or the
interoperability runs, and without reason codes, `last_event` and
`last_seen`. They stay in the repository and in the container demo stack.

## Repository layout

```
cmd/trailsource        CLI
internal/opcua         OPC UA adapter: session, batched browse, values, endpoints
internal/inventory     address-space items, fingerprint, snapshot, diff
internal/evidence      Trail Evidence Record v1 (JCS, seal, verify)
internal/chain         persistent per-stream sequence and chain position
internal/status        reason codes and the last_event/last_seen state model
internal/report        journal report (text, HTML, JSON)
internal/trust         OPC UA trust list: server certificate checks, rejected copies
internal/mapping       semantic role mapping file and skeleton generator
internal/certgen       client certificate generation
internal/config        YAML config and config_version
internal/journal       JSON Lines append
internal/mqttpub       MQTT publisher with retained health and Last Will
sim/                   simulators: extrusion line (OPC UA); energy meter, robot cell (preview)
adapters/              Python evidence library and the preview adapters
configs/               example, scenario and interop configurations, mapping
scripts/               scenario runner, tamper helper, TCP relay for reconnect tests
docs/                  see below
testdata/              canonicalisation vectors, report fixture
```

## Documentation

- `docs/SCENARIOS.md`: the seven scenarios, locally and in the container stack.
- `docs/CONNECT_YOUR_SERVER.md`: from endpoint to verified journal on your server.
- `docs/STATUS.md`: reason codes and the state of a source.
- `docs/INTEROP.md`: runs against servers of other implementations, what worked and what did not.
- `docs/ARCHITECTURE.md`: components, data flow, state files.
- `docs/RECORD_MAPPING.md`: OPC UA fields to record fields.
- `docs/CLEAN_ROOM.md`: where the code comes from.

## Feedback and support

Tried it? An
[evaluation report](https://github.com/RainerGewalt/trailsource/issues/new?template=evaluation-report.yml)
helps more than anything else: what you ran, whether it worked, and what got
in the way. A first run that failed is as useful as one that worked. Bugs can
also go into a plain issue. Vulnerabilities go to [SECURITY.md](SECURITY.md),
not to a public issue.

Support is best effort: issues are read and answered, but there is no
guaranteed response time, no support contract, no SLA and no paid support.

## Licence

Apache License 2.0. Copyright 2026 Florian Przybylak.
