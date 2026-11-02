# Code provenance

TrailSource is assembled from code the owner wrote for earlier, non-public
showcase and prototype repositories, re-targeted to a generic manufacturing
domain. No third-party source code beyond the declared open-source
dependencies was read or copied for this repository.

## Origins

| Area | Origin | What changed |
|---|---|---|
| OPC UA browse, subscription loop, MQTT publisher | the owner's IT/OT showcase (Go, gopcua + Paho) | domain-specific normalisation removed; events replaced by sealed records; snapshot persistence, diff, access levels, session identity and health/Will added |
| Modbus TCP and EtherNet/IP adapters and simulators | the owner's IT/OT showcase (Python) | HTTP-mocked Modbus replaced by a real Modbus TCP server and client; record format applied; generic energy meter and robot cell domain |
| OPC UA simulator | the owner's IT/OT showcase (Python asyncua) | rewritten for an extrusion line; address-space change trigger added |
| Time semantics and `config_version` with secret masking | one stdlib-only event envelope module from the owner's bridge prototype | ported to Go and reused in Python |
| Architecture document | the owner's showcase architecture notes | rewritten for the adapter scope |
| RFC 8785 canonicalisation | `github.com/gowebpki/jcs` (Go, Apache-2.0); Python number and sort logic follows the owner's `trail-evidence` reference package | library dependency; the Python module is a port to be replaced by the published package |

## Dependencies

Go: `github.com/gopcua/opcua` (MIT), `github.com/eclipse/paho.mqtt.golang`
(EPL-2.0 / EDL-1.0), `github.com/gowebpki/jcs` (Apache-2.0),
`gopkg.in/yaml.v3` (MIT, Apache-2.0 parts).
Python: `paho-mqtt` (EPL-2.0 / EDL-1.0), `PyYAML` (MIT), `asyncua` (LGPL-3.0,
simulator only).

## Specification

The record format follows the Trail Evidence Record v1 draft maintained
alongside the `trail-evidence` library. The implementation in
`internal/evidence` and `adapters/pylib/trailsource_evidence` is temporary
and is marked with `TODO(trail-evidence)` comments; `testdata/vectors.json`
is the compatibility contract.
