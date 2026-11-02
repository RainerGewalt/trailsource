# Architecture

## Purpose

TrailSource sits between a machine's protocol endpoint and an MQTT broker.
It reads, never writes, and makes three things reviewable later: which
address space a value belonged to, what changed on the server, and under
which identity and configuration the adapter observed it.

```
Raw value first.
Context second.
Evidence always.
Projection elsewhere.
```

Projections (dashboards, databases, rules) are not part of this project.

## Components

```
OPC UA server ──> trailsource (Go)            ──┐
Modbus TCP unit ─> adapters/modbus (Python)    ──┼──> MQTT broker ──> consumers
CIP controller ──> adapters/ethernetip (Python)──┘       (Mosquitto, TrailMQ, ...)
         │                 │
         └── state/        └── journal/*.jsonl ──> trailsource verify
```

| Component | Responsibility |
|---|---|
| `internal/opcua` | session loop: connect, browse, reconcile snapshot, subscribe, re-browse |
| `internal/inventory` | address-space items, fingerprint, snapshot file, diff |
| `internal/mapping` | semantic role, unit, scaling and context flag per browse path |
| `internal/chain` | per-stream `seq` and last `chain_hash`, persisted atomically |
| `internal/evidence` | record types, JCS, sealing, verification |
| `internal/journal` | JSON Lines append |
| `internal/mqttpub` | Paho client, retained health with Last Will |
| `adapters/pylib` | the same evidence rules in Python plus the polling runtime |
| `sim/` | simulators for the demo domain |

## OPC UA session state machine

```
connect ──> browse ──> reconcile ──> subscribe ──(rebrowse timer)──> browse ...
   │                                     │
   └──(error, backoff)<──────────────────┘
```

- `connect`: endpoint discovery, security policy and mode from config,
  anonymous or username token. The session identity is recorded in
  inventory and connection records.
- `browse`: depth-first along HasComponent, Organizes and HasProperty from
  each configured root, collecting Variable nodes with NodeClass,
  BrowseName, Description, AccessLevel, UserAccessLevel and DataType.
- `reconcile`: fingerprint the items, load the persisted snapshot, diff,
  emit `source.inventory.changed` when different, save the new snapshot with
  an incremented generation.
- `subscribe`: one subscription, monitored items in batches, both
  timestamps requested. Each data change becomes a sealed record. The
  initial value of every monitored item arrives through the same path, so
  there is no separate initial read.
- `rebrowse`: after the configured interval the subscription is cancelled,
  the address space is browsed again and reconciled, and a new subscription
  is created. A changed address space therefore shows up within one
  interval even without a reconnect.

## Emit order

For every record: seal (assign `seq`, `prev_hash`, hashes; persist chain
state), append to the journal, publish to MQTT. A record that could not be
sealed or journaled is not published. A record that was journaled but whose
publish failed is still in the journal and the chain; the broker side will
see a gap in topics but the journal is complete. This is the intended
priority: local evidence before transport.

## State files

```
state/<source_id>.chain.json       seq and chain_hash of the last sealed record
state/<source_id>.inventory.json   last browse snapshot with generation
journal/<source_id>.jsonl          every record, one per line
```

All files are written atomically (temp file and rename). Deleting the chain
file restarts the stream at `seq 0` with `chain_restart` anchored to
genesis; the verifier reports this as a restart, not as a break.

## Identity and configuration

- `producer.id` identifies the adapter instance; `stream_id` identifies the
  source. One adapter process serves one source in v0.1.
- `config_version` is `sha256` over the canonical configuration with secret
  keys masked. It appears in `context` of every record and as the `config`
  policy of inventory and connection records.
- The mapping file has its own hash, carried in `context.mapping`.
- Health is a retained message on `<prefix>/health` with a Last Will so an
  unexpected disconnect leaves a retained `down` state.

## Demo domain

An extrusion line (`Plant/Line01/Extruder01`): barrel zone temperatures,
melt pressure, screw speed and torque, haul-off speed, piece counter, alarm
words, recipe, order and batch; a line energy meter over Modbus TCP; a
robot cell over EtherNet/IP. The simulators are generic and carry no
company, site or customer references.

## Out of scope for v0.1

Writes of any kind, CIP explicit messaging, record signatures, multiple
sources per process, configuration hot reload, event subscriptions
(Euromap cycle events), alarm word decoding.
