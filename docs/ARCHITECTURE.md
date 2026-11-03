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
Modbus TCP unit ─> adapters/modbus (preview)   ──┼──> MQTT broker ──> consumers
CIP controller ──> adapters/ethernetip (preview)─┘       (Mosquitto, TrailMQ, ...)
         │                 │
         └── state/        └── journal/*.jsonl ──> trailsource verify, trailsource report
```

The 0.1 gate covers the OPC UA adapter only. The Python adapters are a
preview with the same record format and fewer guarantees (no reason codes,
no `last_event`/`last_seen`, not part of the scenarios or interop runs).

| Component | Responsibility |
|---|---|
| `internal/opcua` | session loop: connect, batched browse, reconcile snapshot, subscribe, re-browse; one-shot inspection and endpoint listing for the CLI |
| `internal/inventory` | address-space items, fingerprint, snapshot file, diff |
| `internal/mapping` | semantic role, unit, scaling and context flag per browse path |
| `internal/chain` | per-stream `seq` and last `chain_hash`, persisted atomically |
| `internal/evidence` | record types, JCS, sealing, verification with the specification's finding classes |
| `internal/status` | reason codes and the `last_event`/`last_seen` state model (`docs/STATUS.md`) |
| `internal/report` | journal report: generations, gaps, restarts, verification; text, HTML, JSON |
| `internal/certgen` | self-signed client certificate with the ApplicationURI as SAN |
| `internal/journal` | JSON Lines append |
| `internal/mqttpub` | Paho client, retained health with Last Will |
| `adapters/pylib` | the same evidence rules in Python plus the polling runtime of the preview adapters |
| `sim/` | simulators for the demo domain |

## OPC UA session state machine

```
connect ──> browse ──> reconcile ──> subscribe ──(rebrowse timer)──> browse ...
   │                                     │
   └──(error, backoff)<──────────────────┘
```

- `connect`: endpoint discovery, security policy and mode from config,
  anonymous or username token, optional pin of the server certificate
  thumbprint (the stack does not check server certificates against a
  trust list). The session identity, including the server certificate
  thumbprint of secured sessions, is recorded in inventory and connection
  records. Every session opens with `source.connection.changed`
  (`established` or `restored`).
- `browse`: depth-first along HasComponent, Organizes and HasProperty from
  each configured root, collecting Variable nodes with NodeClass,
  BrowseName, Description, AccessLevel, UserAccessLevel and DataType. The
  children of all siblings are fetched in one Browse call and the
  attributes of all sibling variables in one Read call. An unreadable root,
  any transport or session error, or a client that is no longer connected
  afterwards discards the browse; it never yields a partial inventory.
  Nodes deleted between Browse and Read are skipped.
- `reconcile`: fingerprint the items, load the persisted snapshot, diff,
  emit `source.inventory.changed` when different, save the new snapshot with
  an incremented generation.
- `subscribe`: one subscription, monitored items in batches, both
  timestamps requested, sampling interval from `sampling_interval_ms`
  (default: the publishing interval). Each data change becomes a sealed
  record. The initial value of every monitored item arrives through the
  same path, so there is no separate initial read.
- `rebrowse`: after the configured interval, or at most 5 s after a
  monitored node reports `BadNodeIdUnknown`, the subscription is
  cancelled, the address space is browsed again and reconciled, and a new
  subscription is created. A changed address space therefore shows up
  within one interval even without a reconnect.
- `loss`: the client state is polled once per second. The stack's own
  reconnect is switched off: a lost session ends with
  `source.connection.lost` and its reason, and the next session starts
  from `connect` after `session_backoff_s`. Failed attempts are recorded
  once per outage (`source.connection.failed`), not per retry. On shutdown
  the session ends with `source.connection.closed`.

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
(Euromap cycle events, model change events), alarm word decoding,
validation of server certificates against a trust list, resolution of
enumeration names.
