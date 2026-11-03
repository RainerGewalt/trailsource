# Status vocabulary

A TrailSource record says two different things, in two different places:

- `event_type`: **what happened**. Coarse, one per kind of record.
- `payload.reason_code`: **why** the outcome is what it is. A name from the
  shared reason-code catalog of the trail-evidence repository
  (`schema/reason-codes.json`, `docs/REASON_CODES.md`).

The state of a source is built from the reason codes: the name of its last
lifecycle event plus the time it was last seen.

## Event types

| `event_type` | Topic | Written when |
|---|---|---|
| `source.bridge.restarted` | `status` | The adapter process starts. Always the first record of a process, always with `chain_restart: true` and `anchor_hash`. |
| `source.connection.changed` | `status` | A session to the server opens, ends, or cannot be opened. |
| `source.inventory.changed` | `inventory` | The first browse, and every browse whose fingerprint differs from the persisted snapshot. |
| `source.value.observed` | `values` | Every data change of a monitored node. |

Event types are not reason codes and never appear in `payload.reason_code`.

## Reason codes

Every code below is in the trail-evidence catalog (catalog pattern
`<namespace>.<name>`: one dot after the namespace, then lowercase words
joined by underscores). The severity is the catalog's.

### On `source.bridge.restarted`

| Code | Severity | When |
|---|---|---|
| `common.restart` | info | The process started and continues the stream with a new segment, anchored at the last `chain_hash` it knows, or at genesis for a stream that has no records yet. `payload.chain_state` is `resumed` or `new`. |
| `common.state_lost` | warning | The process started without chain state although the journal or the inventory snapshot already holds records: the state file was deleted or replaced. The new segment is anchored at genesis; `payload.chain_state` is `lost`. |

### On `source.connection.changed`

| Code | `payload.state` | Severity | When |
|---|---|---|---|
| `source.connection_established` | `connected` | info | First session of this process. |
| `source.connection_restored` | `connected` | info | Session after a lost or failed connection. `payload.down_ms` is the time since the `lost` or `failed` record. |
| `source.connection_lost` | `disconnected` | error | An open session ended with an error. `payload.reason` has the error text. |
| `source.connection_failed` | `disconnected` | error | A connect attempt failed. Recorded once per outage: on a start against an unreachable server, never while a `lost` record already opened the outage. Retries stay silent until a session is restored. |
| `source.connection_closed` | `disconnected` | info | The adapter closed the session because it is stopping. |

### On `source.inventory.changed`

| Code | Severity | When |
|---|---|---|
| `source.inventory_initial` | info | First browse of the source, no persisted snapshot; also the one record after an upgrade that changed the fingerprint definition (`change_kind: rebaseline`). |
| `source.node_removed` | warning | Nodes disappeared from the address space. |
| `source.node_changed` | warning | Attributes of nodes that stay changed: browse name, data type, browse path or access levels. |
| `source.node_added` | info | Nodes appeared in the address space. |

The catalog allows one code per record. When a browse finds more than one
kind of change, the record carries the first that applies in the order of
the table, removed before changed before added; `payload.diff` lists all of
them. A browse that matches the persisted fingerprint produces no record.

An inventory record also carries `truncated` (true when `max_nodes` stopped
the browse, so the inventory is a deterministic prefix of the address space
rather than all of it) and `browse_limits`.

### On `source.value.observed`

| Code | Severity | When |
|---|---|---|
| `source.quality_uncertain` | warning | The server delivered the value with an uncertain StatusCode. |
| `source.quality_bad` | error | The server delivered the value with a bad StatusCode, for example `BadNodeIdUnknown` for a deleted node or `BadWaitingForInitialData`. |

Values with good quality carry no `reason_code`.

### On the health topic only

| Code | Severity | When |
|---|---|---|
| `common.will_published` | warning | `last_event` of the MQTT Last Will: the broker lost the adapter's session without an orderly disconnect. The adapter cannot seal a record at that moment, so this code never appears in the journal. |

## State of a source

TrailSource describes the state of a source with two values:

- `last_event`: the reason code of the most recent lifecycle record the
  adapter sealed for that source (`source.bridge.restarted`,
  `source.connection.changed`, `source.inventory.changed`). Value records,
  including bad ones, do not overwrite it.
- `last_seen`: when the source last answered, as RFC 3339 UTC with
  milliseconds. A received value, a completed browse and an established
  session move it forward. A lost connection or a failed attempt does not.
  It is `null` until the source has answered once in this process.

Together they answer "what state is the source in, and how fresh is that":
`source.connection_lost` with `last_seen` three minutes ago means the
session broke and nothing has arrived since. `source.connection_established`
or `source.inventory_initial` with `last_seen` one second ago is a source
that is up and delivering. Right after a start, before the first session,
`last_event` is `common.restart` (or `common.state_lost`) and `last_seen`
is `null`.

The retained health topic (`<prefix>/health`) carries exactly these two
fields in addition to the existing ones. The MQTT Last Will carries
`last_event: "common.will_published"` and `last_seen: null`, because the
broker publishes it after the adapter has vanished and the adapter cannot
fill in a time at connect.

`trailsource report` prints the same pair for the end of a journal
("At end").

## Provisional names

Journals written before TrailSource adopted the catalog use provisional
names. `trailsource report` reads them under the catalog names; the
verifier does not look at reason codes at all.

| Provisional | Catalog |
|---|---|
| `source.connection.established` | `source.connection_established` |
| `source.connection.restored` | `source.connection_restored` |
| `source.connection.lost` | `source.connection_lost` |
| `source.connection.failed` | `source.connection_failed` |
| `source.connection.closed` | `source.connection_closed` |
| `source.inventory.initial` | `source.inventory_initial` |
| `source.inventory.added` | `source.node_added` |
| `source.inventory.removed` | `source.node_removed` |
| `source.inventory.modified` | `source.node_changed` |
| `source.inventory.mixed` | one of `source.node_removed`, `source.node_changed`, `source.node_added`, classified from the diff |
| `common.chain.restart` | `common.restart` (now on the new `source.bridge.restarted` record) |
| `common.mqtt.will` | `common.will_published` |

`source.connection_established`, `source.connection_failed`,
`source.connection_closed`, `source.node_changed` and
`common.will_published` were added to the catalog for TrailSource; the
other codes were already there.

## Preview adapters

The Modbus TCP and EtherNet/IP adapters (preview, not part of the 0.1 gate)
do not carry `reason_code`, `last_event` or `last_seen` yet and write no
`source.bridge.restarted` record. Their connection records keep the
free-text `reason` only.
