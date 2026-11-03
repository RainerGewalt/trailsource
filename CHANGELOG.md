# Changelog

All notable changes to this project are documented here. The format follows
Keep a Changelog; versions follow Semantic Versioning.

## Unreleased

Nothing yet.

## 0.1.0 - 2026-11-03

First release: a read-only OPC UA source adapter that seals every
observation as a Trail Evidence Record (`trail.evidence/1`), publishes it
over MQTT and appends it to a local journal that verifies with the
trail-evidence reference verifier v0.2.0.

### Added

- OPC UA adapter (`trailsource run`): sessions with None, Sign or
  SignAndEncrypt (Basic256Sha256, Aes128_Sha256_RsaOaep,
  Aes256_Sha256_RsaPss), anonymous or username login (password from the
  environment or a 0600 file); server certificates only from an OPC UA
  trust list (`pki_dir`: `trusted/`, `issuers/`, `rejected/`), optional pin.
- Address space: batched depth-first browse with order-independent
  bindings (`entity_key` survives reordered references, renumbered
  namespaces and restarts), fingerprint over normalized path, NodeId,
  NodeClass and DataType, and `source.inventory.changed` records with a
  diff; a deleted monitored node triggers a re-browse.
- Values: `source.value.observed` with source, server and read timestamps,
  StatusCode and quality, the address-space fingerprint, a semantic role
  from a mapping file; every builtin type, arrays and structures encoded
  for RFC 8785.
- Lifecycle as records with reason codes from the trail-evidence catalog:
  start (`source.bridge.restarted`), connection established, lost, failed,
  restored and closed, certificate rejected, subscription stalled with the
  missed interval declared as `common.gap`; liveness read for half-open
  connections; keep-alive supervision of the subscription. Retained health
  with `last_event`, `last_seen` and an MQTT Last Will (`docs/STATUS.md`).
- Hash chain per source with `seq` as a decimal string, `chain_restart`
  and anchor on every start; `config_version` with secrets masked.
- `trailsource verify` (finding classes of the trail-evidence
  specification), `trailsource report` (text, self-contained HTML, JSON),
  `endpoints`, `browse --mapping-out`, `gen-cert --pki`, `config-version`.
- Seven local scenarios (`make scenarios`, `docs/SCENARIOS.md`): inventory
  change, gap, restart, tamper, server restart, half-open connection,
  silent subscription. `docs/CONNECT_YOUR_SERVER.md` for a server of your
  own.

### Interoperability

Tested against the Eclipse Milo demo server and the umati Sample-Server
(open62541): every offered combination of the three policies, Sign and
SignAndEncrypt, anonymous and (Milo) username login; all journals `valid`
with trail-evidence v0.2.0 (`docs/INTEROP.md`).

### Limits

- OPC UA only, and read-only: no Write or Call in any configuration.
- Not tested: certificate and issued-token logins, a server that refuses
  an untrusted client certificate, CA-signed server certificates with CRLs
  outside unit tests. One Milo session period refused username logins for
  about ten minutes for reasons not determined (`docs/INTEROP.md`).
- Keep-alive supervision relies on a monitored `ServerStatus.CurrentTime`;
  it is off for a server that rejects that item.
- Enumeration names are not resolved; no host-name check in server
  certificates; no automatic CRL download; no record signatures.

### Preview

- Modbus TCP and EtherNet/IP adapters (Python, `adapters/`) publish the
  same record format but are not part of this release: no reason codes,
  no `last_event`/`last_seen`, not in the container image.
