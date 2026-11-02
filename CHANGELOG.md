# Changelog

All notable changes to this project are documented here. The format follows
Keep a Changelog; versions follow Semantic Versioning.

## [Unreleased]

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
