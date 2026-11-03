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
| `event_type` | `source.value.observed`, `source.inventory.changed`, `source.connection.changed`, `source.bridge.restarted` |
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

Start, inventory and connection records, and values of bad or uncertain
quality, carry `payload.reason_code` from the trail-evidence catalog; the
codes TrailSource uses are listed in `docs/STATUS.md`. Event types are not
reason codes.

Hashes use SHA-256 over RFC 8785 (JCS) bytes. The Go side uses
`github.com/gowebpki/jcs`, the Python side a stdlib port of the same rules;
both reproduce `testdata/vectors.json` byte for byte.

### Numbers

RFC 8785 inherits ES6 number semantics: every JSON number is an IEEE-754
double and is rendered like `Number.prototype.toString` (`1.0` becomes `1`,
`1e21` becomes `1e+21`). Two consequences for producers:

- Integers outside +/-2^53 would be rounded by canonicalisation
  (`9007199254740993` hashes as `9007199254740992`). TrailSource therefore
  encodes OPC UA `Int64` and `UInt64` values as decimal strings. `SByte` to
  `UInt32` stay JSON integers.
- Floating point values whose exact decimal rendering matters travel as
  strings. This applies to `engineering_value` (scaled integers with a
  configured number of decimals) and to anything derived with exact decimal
  arithmetic. Raw `Float` and `Double` readings are JSON numbers: `Double` as
  read; `Float` widened through its shortest 32-bit round-trip decimal, so a
  `Float` 0.1 hashes as `0.1`, not as `0.10000000149011612`. The Modbus
  `float32` and CIP `REAL` adapters widen through 7 significant digits in
  the same spirit. NaN and infinities become `null`.

`value_type` always names the native type so the original representation can
be restored. Consumers comparing values must not assume more precision than
the native type offers.

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
| Value | `value`, `value_type` | see "Value encoding" below |
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
| `quality` bad or uncertain | `reason_code` | `source.quality_bad` or `source.quality_uncertain`; absent for good values |

### Value encoding

| OPC UA type | `value` | `value_type` |
|---|---|---|
| Boolean, SByte to UInt32 | JSON boolean or integer | `bool`, `int8` ... `uint32` |
| Int64, UInt64 | decimal string (see Numbers) | `int64`, `uint64` |
| Float, Double | JSON number, NaN and infinities as `null` | `float32`, `float64` |
| String, XmlElement | string | `string`, `xmlelement` |
| DateTime | RFC 3339 UTC string | `datetime` |
| ByteString | base64 | `bytestring` |
| Guid | `72962B91-FA75-...` | `guid` |
| NodeId, ExpandedNodeId | string form; ExpandedNodeId with `nsu=` and `svr=` when present | `nodeid`, `expandednodeid` |
| StatusCode | `0x` + 8 hex digits | `statuscode` |
| QualifiedName | `name`, or `ns:name` when the namespace index is not 0 | `qualifiedname` |
| LocalizedText | the text (locale dropped) | `localizedtext` |
| Variant (BaseDataType) | the inner value | the inner type, `variant<...>` inside arrays |
| DataValue | `{value, value_type, status_code, source_timestamp, server_timestamp}` | `datavalue` |
| ExtensionObject | `{type_id, body}` with the decoded structure walked field by field under these same rules, or `{type_id, body_base64}` when the stack cannot decode it. `type_id` is the encoding NodeId as transmitted. | `extensionobject` |
| Enumeration | the integer; names are not resolved | `int32` (`data_type` names the enumeration's NodeId) |
| Arrays, matrices | JSON arrays, nested for matrices | `array<...>` |

`data_type` uses short names for all builtin types (`guid`,
`structure`, `variant`, `integer`, ...) and the NodeId for every other
type.

## OPC UA: `source.inventory.changed`

Emitted on the first browse of a source (no persisted snapshot,
`change_kind: "initial"`) and whenever the fingerprint differs from the
persisted snapshot (`change_kind: "changed"`).

| Payload field | Content |
|---|---|
| `reason_code` | `source.inventory_initial`, `source.node_removed`, `source.node_changed` or `source.node_added` (the first that applies; see `docs/STATUS.md`) |
| `fingerprint`, `previous_fingerprint`, `generation` | `sha256` over sorted `NodeId|BrowseName|DataType` lines; generation increments per change |
| `truncated`, `browse_limits` | `truncated` is true when `max_nodes` stopped the browse: the inventory is the first `max_nodes` variables in depth-first order, not the whole address space |
| `node_count`, `nodes` | the full list of Variable nodes with NodeId, browse path, data type, access levels |
| `diff.added`, `diff.removed`, `diff.changed` | keyed by NodeId; `changed` lists field, old and new value for browse name, data type, browse path and access levels |
| `namespaces` | the server's NamespaceArray at browse time |
| `session` | `auth`, `security_policy`, `security_mode`, `server_uri` and, for secured sessions, `server_certificate_thumbprint` (SHA-1) of the session that saw this address space |
| `endpoint`, `browse_roots` | |

The fingerprint deliberately ignores descriptions and access levels so that
cosmetic edits do not create a new generation; access level changes are
still visible in `diff.changed`.

## OPC UA: `source.bridge.restarted`

The first record of every process start, with `chain_restart` and
`anchor_hash`. Payload: `reason_code` (`common.restart`, or
`common.state_lost` when the chain state is missing although the journal
or the inventory snapshot already holds records), `chain_state`
(`resumed`, `new` or `lost`), `producer_version`, `config_version`,
`mapping_hash`, `endpoint`. Policy is the configuration, as on connection
records.

## OPC UA: `source.connection.changed`

`{state: "connected" | "disconnected", reason_code, endpoint, session, reason?}`
on the `status` topic, plus `down_ms` on a restored session. `reason_code`
is `source.connection_established`, `source.connection_restored`,
`source.connection_lost`, `source.connection_failed` or
`source.connection_closed`; `reason` carries the error text for `lost` and
`failed`. Together with `chain_restart` this makes gaps in
the value stream explainable; `trailsource report` pairs them into gaps
with begin, end and duration.

## Modbus TCP: `source.value.observed` (preview)

| Modbus | Payload field | Notes |
|---|---|---|
| table + 0-based address | `native_node_id` | `hr:4` (holding) or `ir:0` (input) |
| | `table`, `address`, `register_count`, `raw_registers` | raw 16-bit words as read |
| configured type | `data_type`, `word_order` | `uint16`, `int16`, `bool`, `uint32`, `int32`, `float32`; word order per device |
| decoded value | `value`, `value_type` | `float32` as JSON number widened through 7 significant digits; integers native |
| configured `scale` | `scale`, `engineering_value` | exact decimal arithmetic |
| exception response | `status`, `quality` | `exception:<code>` and `bad`; the record is still emitted |
| | `source_timestamp`, `server_timestamp` | always `null`; Modbus has none, and that absence is information |
| point list hash | `inventory_hash` | `policy{kind: "addressspace"}` is the hash of the configured point list |

## EtherNet/IP (shim driver): `source.value.observed` (preview)

| CIP | Payload field | Notes |
|---|---|---|
| symbolic tag path | `native_node_id`, `name` | e.g. `Program:Robot.Mode` |
| configured CIP type | `cip_type` | `BOOL`, `DINT`, `REAL`, `STRING` |
| value | `value`, `value_type` | `REAL` as JSON number widened through 7 significant digits; `DINT`/`BOOL`/`STRING` native |
| | `source_timestamp`, `server_timestamp` | always `null` |
| | `driver` | `http-shim` in v0.1 |

## Alarm words

The extrusion simulator exposes alarms as 32-bit words
(`Alarms.AlarmWord1`). TrailSource transports the word as an integer; it
does not decode bits in v0.1. Decoding needs a versioned code table per
controller generation and belongs in a transform stage, not in the adapter.

## Verification

`trailsource verify <journal>` checks, per stream and in `seq` order:
schema, recomputed `content_hash`, recomputed `chain_hash` (over the core
with the recomputed `content_hash`), linkage of `prev_hash` (or
`anchor_hash` on restart) and `seq` continuity. Findings use the classes of
the specification: `hash_mismatch`, `chain_break`, `seq_gap` and
`schema_invalid` are errors, `restart` is a notice. A restart is a notice
when the previous segment is absent or the anchor matches its last
`chain_hash`; a restart whose anchor contradicts the previous segment in the
same journal is a `chain_break`. Any error sets a non-zero exit code. An
edited payload fails both hashes of its own line; a re-sealed edit breaks
the link of the following line (`docs/SCENARIOS.md`, tamper).
Journals written by TrailSource also verify with the `trail-evidence`
reference verifier, which applies the same rules.
