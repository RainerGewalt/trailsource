# Status vocabulary

TrailSource describes the state of a source with two values:

- `last_event`: the reason code of the most recent lifecycle record the
  adapter sealed for that source (connection or inventory). Value
  observations do not overwrite it.
- `last_seen`: when the source last answered, as RFC 3339 UTC with
  milliseconds. A received value, a completed browse and an established
  session move it forward. A lost connection or a failed attempt does not.
  It is `null` until the source has answered once in this process.

Together they answer "what state is the source in, and how fresh is that":
`source.connection.lost` with `last_seen` three minutes ago means the session
broke and nothing has arrived since. `source.connection.established` with
`last_seen` one second ago is a source that is up and delivering.

The retained health topic (`<prefix>/health`) carries exactly these two
fields in addition to the existing ones. The MQTT Last Will carries
`last_event: "common.mqtt.will"` and `last_seen: null`, because the broker
publishes it after the adapter has vanished and the adapter cannot fill in
a time at connect.

## Reason codes

Records carry a reason code in `payload.reason_code`. Codes are dotted
lowercase names in two namespaces:

- `source.*`: something about the machine-side source (session, address
  space).
- `common.*`: something about the adapter or its hash chain that applies to
  every producer kind.

### Connection (`source.connection.changed`, `status` topic)

| Code | `payload.state` | Meaning |
|---|---|---|
| `source.connection.established` | `connected` | First session of this adapter process. |
| `source.connection.restored` | `connected` | Session after a lost or failed connection in the same process. The time since the preceding `lost` or `failed` record is the gap. |
| `source.connection.lost` | `disconnected` | An open session ended with an error. `payload.reason` carries the error text, for example `opc connection state Disconnected` or a StatusCode name. |
| `source.connection.failed` | `disconnected` | A connect attempt failed. Recorded once per outage: on start against an unreachable server, or never if a `lost` record already opened the outage. Retries stay silent until a session is restored. |
| `source.connection.closed` | `disconnected` | The adapter closed the session because it is stopping. Not an error. |

### Address space (`source.inventory.changed`, `inventory` topic)

| Code | Meaning |
|---|---|
| `source.inventory.initial` | First browse of the source, no persisted snapshot. |
| `source.inventory.added` | Nodes were added; nothing removed, no attribute changed. |
| `source.inventory.removed` | Nodes were removed; nothing added, no attribute changed. |
| `source.inventory.modified` | Only attributes of existing nodes changed (browse name, data type, browse path, access levels). |
| `source.inventory.mixed` | More than one of added, removed and modified. `payload.diff` has the details. |

A browse that matches the persisted fingerprint produces no record and does
not change `last_event`; it only moves `last_seen`.

An inventory record also carries `truncated` (true when `max_nodes` stopped
the browse, so the inventory is a deterministic prefix of the address space
rather than all of it) and `browse_limits`.

### Common

| Code | Where | Meaning |
|---|---|---|
| `common.chain.restart` | record with `chain_restart: true`; `last_event` of a freshly started adapter before its first lifecycle record | A new chain segment started. The record's `anchor_hash` names the last `chain_hash` of the previous segment, or genesis. Verification reports it as the notice class `restart`, never as an error. |
| `common.mqtt.will` | Last Will payload on the health topic | The broker lost the adapter's MQTT session without an orderly disconnect. |

`common.chain.restart` is not written into `payload.reason_code`: the
restart is a property of the record envelope (`chain_restart`,
`anchor_hash`), and the first record of a segment is usually a connection
record with its own code. Reports and dashboards derive it from the envelope.

## Event types and reason codes

Event types (`event_type`) stay coarse: `source.value.observed`,
`source.inventory.changed`, `source.connection.changed`. The reason code is
the fine-grained name inside the record. A consumer that only needs "is the
source up" reads `last_event` from health; a consumer that needs the
history reads `reason_code` from the journal or from `trailsource report`.

## Relation to trail-evidence

The trail-evidence repository is meant to keep the shared catalogue in
`docs/REASON_CODES.md`. When this list was written that file did not exist,
so the names above are TrailSource's proposal and will be reconciled with
the catalogue once it exists. Verification finding classes (`hash_mismatch`,
`chain_break`, `seq_gap`, `schema_invalid`, `restart`) are already the ones
of the trail-evidence specification.

## Preview adapters

The Modbus TCP and EtherNet/IP adapters (preview, not part of the 0.1 gate)
do not carry `reason_code`, `last_event` or `last_seen` yet. Their connection
records keep the free-text `reason` only.
