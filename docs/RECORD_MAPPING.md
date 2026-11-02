# Record mapping

TrailSource emits Trail Evidence Records v1. This document maps protocol
fields to record fields. The record format itself is specified outside this
repository; the parts that matter for a reader of a TrailSource journal are
repeated here.

## Record envelope

Every record is one JSON object (one line in a journal file):

| Field | Content in TrailSource |
|---|---|
| `schema` | `trail.evidence/1` |
| `producer` | `{kind: "source-bridge", id: <producer.id>, version: <build version>}` |
| `stream_id` | the source id; one hash chain per source |
| `seq` | persistent, strictly increasing by 1; survives restarts |
| `prev_hash` | `chain_hash` of the previous record; genesis is `sha256:` + 64 zeros |
| `chain_restart`, `anchor_hash` | present on the first record after a process start; `anchor_hash` is the last persisted `chain_hash` (or genesis) and equals `prev_hash` |
| `event_type` | `source.value.observed`, `source.inventory.changed`, `source.connection.changed` |
| `time.recorded_at` | moment of sealing, RFC 3339 UTC, milliseconds |
| `time.occurred_at` | OPC UA SourceTimestamp (microseconds) when present; absent for Modbus and CIP |
| `time.read_at` | when the adapter received the value |
| `time.source` | `system` (the adapter's clock; NTP status is not asserted) |
| `actor` | `{auth: "self-declared", id: <producer.id>, roles: ["collector"]}`. The adapter speaks for itself; a broker that authenticates the client can replace this downstream |
| `policy` | value records: `{kind: "addressspace", id: "<source_id>/addressspace", version: <snapshot generation>, hash: <inventory fingerprint>}`. Inventory and connection records: `{kind: "config", id: "config:<file>", version: <revision>, hash: <config_version>}` |
| `payload` | protocol-specific, see below; hashed into `content_hash` |
| `content_hash` | `sha256:` + hex over JCS(payload) |
| `chain_hash` | `sha256:` + hex over JCS of the core members |
| `context` | informational, not hashed: site, line, device, protocol, endpoint, `config_version`, mapping file name/version/hash, host |

Hashes use SHA-256 over RFC 8785 (JCS) bytes. Floating point values are
transported as decimal strings inside `payload` so that Go and Python
canonicalise to identical bytes; integers, booleans, strings and null are
native JSON.

## OPC UA: `source.value.observed`

| OPC UA | Payload field | Notes |
|---|---|---|
| NodeId (string form) | `native_node_id` | e.g. `ns=2;s=Plant.Line01.Extruder01.MeltPressureBar` |
| NamespaceArray[ns] | `namespace_uri` | resolved at browse time |
| BrowseName chain below root | `browse_path` | dot-joined, namespace prefixes stripped |
| BrowseName | `browse_name` | |
| derived from source id + browse path | `entity_key` | `<source_id>/<lowercase path with non-alphanumerics as "-">`; stable across NodeId changes as long as the path is stable |
| mapping file rule | `semantic_role`, `semantic_role_source` | `mapping` when a rule matched, `derived` (snake_case of the BrowseName) otherwise |
| DataType attribute | `data_type` | builtin types as short names (`float64`, `int16`, ...), others as NodeId |
| Value | `value`, `value_type` | floats as strings (`"187.4"`), DateTime as RFC 3339, ByteString as base64, arrays as arrays |
| mapping `scale_decimals` | `scale_decimals`, `engineering_value` | for PLC-style scaled integers: raw `2135` with 1 decimal gives `"213.5"` |
| mapping `unit` | `unit` | UN/CEFACT Rec. 20 code where available |
| StatusCode | `status_code` (hex), `status_name`, `quality` | `quality` is `good`, `uncertain` or `bad` from the top bits |
| SourceTimestamp | `source_timestamp` and `time.occurred_at` | `null` when the server sent none |
| ServerTimestamp | `server_timestamp` | `null` when the server sent none |
| receive time | `time.read_at` | |
| inventory fingerprint | `inventory_hash`, `inventory_generation` | same values as `policy.hash` / `policy.version` |
| AccessLevel, UserAccessLevel | `access.access_level`, `access.user_access_level`, `access.writable`, `access.user_writable` | what the server allows in general and what it allowed this session |
| mapping rules with `context: true` | `context_values` | latest values of recipe, order, batch, machine state repeated in every record |
| | `publish_reason` | `subscription` |

## OPC UA: `source.inventory.changed`

Emitted on the first browse of a source (no persisted snapshot,
`change_kind: "initial"`) and whenever the fingerprint differs from the
persisted snapshot (`change_kind: "changed"`).

| Payload field | Content |
|---|---|
| `fingerprint`, `previous_fingerprint`, `generation` | `sha256` over sorted `NodeId|BrowseName|DataType` lines; generation increments per change |
| `node_count`, `nodes` | the full list of Variable nodes with NodeId, browse path, data type, access levels |
| `diff.added`, `diff.removed`, `diff.changed` | keyed by NodeId; `changed` lists field, old and new value for browse name, data type, browse path and access levels |
| `namespaces` | the server's NamespaceArray at browse time |
| `session` | `auth`, `security_policy`, `security_mode`, `server_uri` of the session that saw this address space |
| `endpoint`, `browse_roots` | |

The fingerprint deliberately ignores descriptions and access levels so that
cosmetic edits do not create a new generation; access level changes are
still visible in `diff.changed`.

## OPC UA: `source.connection.changed`

`{state: "connected" | "disconnected", endpoint, session, reason?}` on the
`status` topic. Together with `chain_restart` this makes gaps in the value
stream explainable.

## Modbus TCP: `source.value.observed`

| Modbus | Payload field | Notes |
|---|---|---|
| table + 0-based address | `native_node_id` | `hr:4` (holding) or `ir:0` (input) |
| | `table`, `address`, `register_count`, `raw_registers` | raw 16-bit words as read |
| configured type | `data_type`, `word_order` | `uint16`, `int16`, `bool`, `uint32`, `int32`, `float32`; word order per device |
| decoded value | `value`, `value_type` | float32 as string with 7 significant digits |
| configured `scale` | `scale`, `engineering_value` | exact decimal arithmetic |
| exception response | `status`, `quality` | `exception:<code>` and `bad`; the record is still emitted |
| | `source_timestamp`, `server_timestamp` | always `null`; Modbus has none, and that absence is information |
| point list hash | `inventory_hash` | `policy{kind: "addressspace"}` is the hash of the configured point list |

## EtherNet/IP (shim driver): `source.value.observed`

| CIP | Payload field | Notes |
|---|---|---|
| symbolic tag path | `native_node_id`, `name` | e.g. `Program:Robot.Mode` |
| configured CIP type | `cip_type` | `BOOL`, `DINT`, `REAL`, `STRING` |
| value | `value`, `value_type` | REAL as string with 7 significant digits |
| | `source_timestamp`, `server_timestamp` | always `null` |
| | `driver` | `http-shim` in v0.1 |

## Alarm words

The extrusion simulator exposes alarms as 32-bit words
(`Alarms.AlarmWord1`). TrailSource transports the word as an integer; it
does not decode bits in v0.1. Decoding needs a versioned code table per
controller generation and belongs in a transform stage, not in the adapter.

## Verification

`trailsource verify <journal>` checks, per stream and in `seq` order:
schema, recomputed `content_hash`, recomputed `chain_hash`, linkage of
`prev_hash` (or `anchor_hash` on restart) and `seq` continuity. Restarts are
reported as information; gaps and hash mismatches are reported as separate
failure kinds and set a non-zero exit code.
