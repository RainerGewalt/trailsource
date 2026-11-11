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
  anonymous or username token. For a secured endpoint the server
  certificate must pass the trust list in `pki_dir` (`internal/trust`:
  validity, ApplicationURI, direct trust or a chain to a trusted CA with
  current CRLs) and an optional pin; a refused certificate goes to
  `rejected/` and is recorded as `source.certificate_rejected`. The
  session identity, including the accepted server certificate thumbprint,
  is recorded in inventory and connection records. Every session opens
  with `source.connection.changed` (`established` or `restored`).
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
- `subscribe`: one subscription per session, monitored items in batches,
  both timestamps requested, sampling interval from `sampling_interval_ms`
  (default: the publishing interval). Each data change becomes a sealed
  record. The initial value of every monitored item arrives through the
  same path, so there is no separate initial read.
- `rebrowse`: after the configured interval, or at most 5 s after a
  monitored node reports `BadNodeIdUnknown`, the address space is browsed
  again in the background and reconciled while notifications keep being
  processed. Then the monitored items are adjusted: items of nodes that
  are gone are deleted, new nodes are added, the others keep their item and
  get the current path, `entity_key` and mapping. A changed address space
  therefore shows up within one interval even without a reconnect, and
  unchanged nodes are not sent again. (Up to this change each re-browse
  replaced the subscription; against the simulator a replacement
  subscription sometimes stayed silent for seconds, and every node's value
  was sent again.)
- `stall`: every subscription also monitors `ServerStatus.CurrentTime`
  (never recorded), so each publishing interval carries data; gopcua does
  not pass keep-alive responses on. No publish response within the
  revised MaxKeepAliveCount times the revised publishing interval, times
  1.5, means the publish cycle stalled although the session answers. The
  window is measured from the arrival of the last publish response, which
  is stamped when it reaches the adapter, and a response that still waits
  to be handled always counts as alive: time the adapter spends waiting
  for the MQTT broker is never a stall of the source. Then: a
  `source.subscription.changed` record (`source.subscription_stalled`), a
  new subscription created before the old one is deleted (so gopcua never
  pauses its publish loop), all monitored items re-added, and a
  `source.data.gap` record (`common.gap`) with the interval once data flows
  again.
- `loss`: the client state is polled once per second. The stack's own
  reconnect is switched off: a lost session ends with
  `source.connection_lost` and its reason, and the next session starts
  from `connect` after `session_backoff_s`. Failed attempts are recorded
  once per outage (`source.connection_failed`), not per retry. On shutdown
  the session ends with `source.connection_closed`.

## Emit order

For every record: seal (assign `seq`, `prev_hash`, hashes; persist chain
state), append to the journal, publish to MQTT. A record that could not be
sealed or journaled is not published. A record that was journaled but whose
publish failed (no acknowledgement within 15 s) stays in the journal and the
chain; it is not sent again, and MQTT subscribers see the gap in `seq`.

## Broker outage

The source and the transport are kept apart: a broker that stops
answering or goes away is never recorded as a gap, a stall or a lost
connection of the OPC UA source (`make scenario-broker-outage`, docs/SCENARIOS.md).

- Publish responses from the server keep arriving while the adapter waits
  for the broker. Each is stamped with its arrival time, which becomes the
  value's `read_at`, and waits in a queue (1024 responses in TrailSource,
  1024 more in the OPC UA stack).
- The publish is synchronous and runs in the same loop as sealing: while
  the broker does not acknowledge, the adapter waits up to 15 s per record
  before it seals the next one. The journal therefore falls behind the
  source during an outage (by up to 75 s in the scenario: 30 s frozen, 20 s
  gone) and catches up once the broker is back; `recorded_at` minus
  `read_at` shows the delay.
- Limit of 0.1: when both queues are full, at one publish response per
  500 ms after roughly 17 minutes of outage (computed, not measured), the
  OPC UA stack stops asking for publish responses, the server keeps only
  its monitored-item queues (TrailSource asks for 10 values per item) and
  drops older values, and no record says so. A journal that is written
  independently of MQTT is not part of 0.1.
- The adapter starts only with a reachable broker: `run` waits up to 25 s
  for the first MQTT connection and then ends with an error that names the
  broker. No record is sealed before; the journal file may already exist,
  empty.

## Journal size and rotation

One journal file per source; TrailSource appends to it and never rotates
it. A value record is about 2.3 KB (2,336 bytes on average over 715 value
records of the broker-outage scenario on 11 November; an inventory record
with 28 nodes about 21 KB). A source with one value change per second
writes about 200 MB a day, the simulator with about ten changes per
second about 2 GB (computed, not measured over a day). `verify` and
`report` read the whole file into memory.

To rotate by hand:

1. Stop the adapter (SIGTERM). Its last record is
   `source.connection_closed`.
2. Move the journal file, for example to `journal/<source-id>.2026-11.jsonl`.
   Leave `state_dir` as it is: the chain position lives there.
3. Start the adapter. The new file begins with `source.bridge.restarted`
   (`common.restart`), anchored at the last `chain_hash` of the moved file.

Verified on its own, the new file reports that restart as a notice whose
previous segment is not in this journal; the files concatenated in order
(`cat old.jsonl new.jsonl | trailsource verify -`) verify as one chain
(run of 11 November against the simulator). Deleting the chain state
instead starts a new chain at genesis, recorded as `common.state_lost`
while the inventory snapshot or a journal file is still there.

## State files

```
state/<source_id>.chain.json       seq and chain_hash of the last sealed record
state/<source_id>.inventory.json   last browse snapshot with generation
journal/<source_id>.jsonl          every record, one per line
```

All files are written atomically (temp file and rename). Every process
start writes `source.bridge.restarted` as its first record, with
`chain_restart` and `reason_code: common.restart`. Deleting the chain file
restarts the stream at `seq 0` anchored to genesis; if the journal or the
inventory snapshot already holds records, that start record says
`common.state_lost`. The verifier reports both as a restart notice, not as
a break, as long as the journal does not contain the previous segment.

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
resolution of enumeration names, certificate checks beyond the trust list
(host names in the certificate, key usage), automatic CRL download.
