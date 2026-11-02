# TrailSource

Source adapters that read machine data over OPC UA, Modbus TCP and
EtherNet/IP and publish every observation as a sealed Trail Evidence Record
over MQTT. Written for integrators who need to know, months later, which
address space a value belonged to, what changed on the server, and who was
allowed to write.

Status: v0.1, read-only, OPC UA first. Modbus TCP and EtherNet/IP are a
second stage and deliberately minimal.

## What it does

- Connects to an OPC UA server, browses the address space below configured
  roots and computes a fingerprint: `sha256` over the sorted list of
  NodeId, BrowseName and DataType of every Variable node.
- Persists that browse snapshot as a JSON file and diffs it on every connect
  and every re-browse. A different fingerprint produces one
  `source.inventory.changed` record with the added, removed and changed
  nodes, the access levels the session saw, and the session identity.
- Subscribes to all Variable nodes and emits one `source.value.observed`
  record per data change with three timestamps (source, server, read), the
  StatusCode, the native NodeId, a stable `entity_key`, a `semantic_role`
  from a versioned mapping file, and the inventory fingerprint as the
  record's `addressspace` policy. A value can therefore always be tied to
  the address space it came from.
- Seals every record into a per-source hash chain (`seq`, `prev_hash`,
  `content_hash`, `chain_hash`). Sequence numbers survive restarts; the
  first record after a start carries `chain_restart` and anchors to the last
  persisted hash.
- Hashes its own configuration into `config_version` with secret keys
  masked, publishes a retained health topic with an MQTT Last Will, and
  appends every record to a local JSON Lines journal that `trailsource
  verify` checks offline.
- Modbus TCP and EtherNet/IP adapters (Python) publish the same record
  format from a configured point or tag list. For those protocols the list
  itself is the address space, and the absence of source and server
  timestamps is recorded explicitly as `null`.

The record format is the Trail Evidence Record v1 (see
`docs/RECORD_MAPPING.md`). Canonicalisation uses `github.com/gowebpki/jcs`
(Go) and a stdlib port of the same rules (Python); sealing and verification
live in a small internal package until the shared `trail-evidence` library
replaces it. `testdata/vectors.json` is the contract, and journals written
by TrailSource verify with the `trail-evidence` reference verifier.

## What it does not do

- It does not write. No OPC UA write, no Modbus write, no CIP write, in any
  configuration. Writes are a policy question and come later.
- It does not speak CIP yet. The EtherNet/IP adapter reads tags through a
  simulator shim behind a one-function driver boundary.
- It does not sign records, notarise anything, or make any statement about
  regulatory compliance. A verified journal proves that the journal is
  internally consistent and unchanged since sealing, nothing more.
- It is not a broker, historian, dashboard or rules engine.

## How it relates to Node-RED, Telegraf and HiveMQ Edge

Node-RED, Telegraf and HiveMQ Edge move OPC UA and fieldbus data into MQTT
well and with far more protocol coverage than this project. TrailSource is
narrower: every published value is a hash-chained record that names the
address-space fingerprint, the session identity and the configuration
version it was produced under, and a change of the server's address space
is itself an event. If that provenance is not something you need, one of
those tools is the better choice.

## Quick start

```sh
make demo        # simulators, Mosquitto, three adapters (docker compose)
mosquitto_sub -h localhost -t 'trailsource/#' -v
make verify      # verifies ./.demo/journal/*.jsonl with trailsource verify
make demo-down
```

The OPC UA simulator adds a seventh barrel zone two minutes after start
(`SIM_ADD_NODE_AFTER_S`), so a `source.inventory.changed` record appears on
`trailsource/plant-a/ext-01/extruder-01/inventory` within the next
re-browse interval.

Without Docker:

```sh
make build
./bin/trailsource run --config configs/opcua-extruder.yaml   # adjust hosts first
./bin/trailsource verify journal/*.jsonl
./bin/trailsource config-version --config configs/opcua-extruder.yaml
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

One YAML file per source (`configs/*.yaml`), `${VAR}` placeholders for
secrets, and a mapping file for semantic roles, units and scaling
(`configs/mapping/extruder.yaml`). `trailsource config-version` prints the
hash that every record of that adapter carries. See the comments in the
example files for every setting.

## Repository layout

```
cmd/trailsource        CLI: run, verify, config-version, version
internal/evidence      Trail Evidence Record v1 (JCS, seal, verify)
internal/chain         persistent per-stream sequence and chain position
internal/inventory     address-space items, fingerprint, snapshot, diff
internal/config        YAML config and config_version
internal/mapping       semantic role mapping file
internal/opcua         OPC UA adapter
internal/mqttpub       MQTT publisher with retained health and Last Will
adapters/pylib         Python evidence library and adapter runtime
adapters/modbus        Modbus TCP adapter
adapters/ethernetip    EtherNet/IP adapter (shim driver)
sim/                   simulators: extrusion line, energy meter, robot cell
configs/               example configurations and mapping
docs/                  architecture, record mapping, provenance
testdata/vectors.json  canonicalisation and hash test vectors
```

## Documentation

- `docs/ARCHITECTURE.md`: components, data flow, state files.
- `docs/RECORD_MAPPING.md`: OPC UA, Modbus and CIP fields to record fields.
- `docs/CLEAN_ROOM.md`: where the code comes from.

## Licence

Apache License 2.0. Copyright 2026 Florian Przybylak.
