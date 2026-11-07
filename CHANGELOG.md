# Changelog

All notable changes to this project are documented here. The format follows
Keep a Changelog; versions follow Semantic Versioning.

## Unreleased

### Fixed

- `make demo` on Linux: the adapters (uid 10001) could not write
  `.demo/state` and `.demo/journal`, created by the host user or by Docker,
  and restarted in a loop with `permission denied`. A demo-only one-shot
  service `demo-init` makes both directories writable before the adapters
  start, independent of the host uid.
- The version no longer carries the tag's leading `v`: `go install` builds,
  the container image and `make build` now report and record `0.1.0` like
  the release archives, instead of `v0.1.0`.
- `make sim-venv` uses only Python 3.9 or newer (asyncua 1.1.8 needs it)
  and stops with a clear message if none is found, instead of failing in
  pip with `No matching distribution found for asyncua==1.1.8`.

### Added

- `trailsource --version` as an alias of `trailsource version`.
- `make first-use` (`scripts/first-use.sh`): walks through the README and
  `docs/CONNECT_YOUR_SERVER.md` from a fresh clone, or with `RELEASE=x.y.z`
  against the published tag, `go install`, release archive and image, and
  stops at the first step that does not behave as documented. Testing
  levels in `docs/TESTING.md`.
- Conformance with the trail-evidence reference vectors
  (`TestReferenceVectors`): every valid vector within the producer role is
  reproduced byte for byte, a legacy integer `seq` verifies as written, and
  every invalid vector fails the verifier. CI checks out trail-evidence for
  it, so the reason-code catalog test now runs there too instead of being
  skipped. That showed that `source.subscription_stalled`, which TrailSource
  0.1 emits, is in the trail-evidence catalog only after v0.2.0;
  trail-evidence v0.2.1 lists it, and CI checks against that tag. Journals
  with the code verify with v0.2.0 unchanged.
- `scripts/check_demo_init.py`, run by `make ci` and in CI: the demo-only
  `chmod 0777` of `demo-init` may reach `.demo/state` and `.demo/journal`
  only (no other mount, no recursive chmod, no other path, no network).

### Documentation

- README: what a verified journal shows is stated exactly (no edit, removal
  or reordering without re-sealing everything after it; records cut off at
  the end are not detectable without an outside anchor), and a section
  "Using it with TrailMQ": topics need a topic rule and publish rights
  before the first run, TrailMQ does not read the journal. The comments in
  `docker-compose.yaml` and `configs/opcua-extruder.yaml` no longer suggest
  more than that.
- `docs/RECORD_MAPPING.md`: `context.host` is the operating-system host name
  of the adapter's machine and travels in every record and MQTT message.
- `docs/CONNECT_YOUR_SERVER.md`, step 2: names the security policies and
  logins that the interoperability runs of 3 November covered, instead of
  only the first two.
- README "Install": create `pki`, `state` and `journal` before the first
  `docker run` (Docker creates a missing bind-mount directory as root),
  mount the mapping directory, and state the Go 1.24 requirement for
  building from a clone.
- `docs/CONNECT_YOUR_SERVER.md`, step 5: the fields a local run has to
  change in the copied example (`state_dir`, `journal.path`,
  `mapping_file`, `mqtt.broker`); the README short form matches it.
- Links to trail-evidence v0.2.0 with both install lines (Go binary
  `trail-evidence`, `pip install trail-evidence` for `trail-evidence-py`);
  statements that it is still unpublished removed.
- Current address-space fingerprints in the README and
  `docs/SCENARIOS.md` samples; `make help` lists seven scenarios;
  `version` in the command list and `internal/trust` in the repository
  layout.

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
