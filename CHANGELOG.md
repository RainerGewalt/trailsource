# Changelog

All notable changes to this project are documented here. The format follows
Keep a Changelog; versions follow Semantic Versioning.

## [Unreleased]

The 0.1 gate is OPC UA only. The Modbus TCP and EtherNet/IP adapters are
marked as preview.

### Added

- Four scenario demos as make targets (`scenario-inventory-change`,
  `scenario-gap`, `scenario-restart`, `scenario-tamper`, `scenarios`) that
  run the simulator, Mosquitto and the adapter as local processes, then
  verify and report (`scripts/scenario.sh`, `docs/SCENARIOS.md`).
- `trailsource report <journal> [--html out.html] [--json]`: period,
  records per event type, value quality, address-space generations with
  diff, gaps with begin, end, duration and reason, restarts, state at the
  end, verification result. The HTML file is self-contained.
- `trailsource endpoints`, `trailsource browse [--mapping-out]` and
  `trailsource gen-cert` for connecting a new server
  (`docs/CONNECT_YOUR_SERVER.md`).
- Reason codes from the trail-evidence catalog in `payload.reason_code`:
  `source.connection_established`, `_restored`, `_lost`, `_failed`,
  `_closed` on connection records; `source.inventory_initial`,
  `source.node_removed`, `source.node_changed`, `source.node_added` on
  inventory records; `source.quality_bad` and `source.quality_uncertain` on
  values of that quality; `common.restart` or `common.state_lost` on the new
  start record. `last_event`/`last_seen` in the health payload, and
  `common.will_published` in the Last Will (`docs/STATUS.md`).
- `source.bridge.restarted`, the first record of every process start, with
  `chain_restart` and the reason code the catalog assigns to it.
- `down_ms` on `source.connection_restored` records.
- Inventory records carry `truncated` and `browse_limits`.
- Settings `sampling_interval_ms`, `browse_timeout_s`, `application_uri`
  and `server_thumbprint` (pin the server certificate; the session's
  thumbprint is recorded).
- Interoperability record against the Eclipse Milo demo server and the
  umati Sample-Server (`docs/INTEROP.md`, `configs/interop/`,
  `scripts/tcp_relay.py`).

### Changed

- `trailsource verify` reports the finding classes of the Trail Evidence
  Record specification (`hash_mismatch`, `chain_break`, `seq_gap`,
  `schema_invalid`, notice `restart`) and recomputes `chain_hash` with the
  recomputed `content_hash`, like the reference verifier.
- The browse batches its requests per group of siblings: 101 nodes over the
  internet in 5 s instead of 65 s, with unchanged traversal and
  fingerprints.
- The OPC UA stack no longer reconnects on its own. A lost session is
  recorded, detected within a second, and followed by a new session with a
  fresh browse.
- `sampling_interval_ms` defaults to the subscription interval instead of
  the server's fastest rate.
- A monitored node that the server deletes triggers a re-browse.
- Guid, XmlElement, ExpandedNodeId, DataValue, Variant, ExtensionObject,
  enumerations and decoded structures are encoded as JSON under the JCS
  rules; all builtin data types have short names; QualifiedName keeps its
  namespace index.

### Fixed

- A re-browse on a dying session sealed an empty inventory as a new
  generation with every node removed. A browse with an unreadable root or a
  connection error is now discarded.
- An unknown browse root produced an empty inventory instead of an error.
- `source.connection.changed` records for a lost session carried no reason.
- A node deleted between Browse and Read entered the inventory without a
  data type.

## [0.1.0] - 2026-11-02

### Added

- OPC UA adapter (Go): connect, browse, address-space fingerprint
  (`sha256` over sorted NodeId, BrowseName and DataType), persisted snapshot,
  diff and `source.inventory.changed` records, subscription-based
  `source.value.observed` records with source, server and read timestamps,
  StatusCode, native NodeId, stable `entity_key`, semantic role from a
  mapping file and the inventory fingerprint as `addressspace` policy.
- Trail Evidence Record v1 sealing and verification (`internal/evidence`,
  `adapters/pylib/trailsource_evidence`) with shared test vectors in
  `testdata/vectors.json`; canonicalisation via `github.com/gowebpki/jcs`
  and a matching stdlib port in Python, cross-checked against the
  `trail-evidence` reference verifier.
- Persistent per-stream sequence numbers with `chain_restart` on start.
- `config_version` as SHA-256 over the canonical configuration with masked
  secret keys.
- Retained health topic with MQTT Last Will.
- Modbus TCP adapter (Python) reading holding and input registers from a
  configured point list; EtherNet/IP adapter (Python) with a simulator shim
  driver.
- Simulators for an extrusion line (OPC UA), a line energy meter (Modbus TCP)
  and a robot cell (EtherNet/IP shim) under `sim/`.
- `trailsource verify` for JSON Lines journals; `docker-compose.yaml`,
  `make demo` and `make verify`.

### Deliberately not included

- Writes to OPC UA, Modbus or CIP. Writes are a policy question and come
  later.
- CIP explicit messaging. The EtherNet/IP adapter talks to the simulator shim.
- Signatures (`signature` block of the record format).
