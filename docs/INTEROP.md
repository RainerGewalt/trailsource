# Interoperability record

What happened when TrailSource read from OPC UA servers it was not written
against. Each run is read-only, small (at most 50 monitored variables) and
a few minutes long, so it stays polite to shared public servers. Results
are what was observed on the date given; public demo servers change.

## 2026-11-03: Eclipse Milo and umati Sample-Server

Client: TrailSource with the fixes of commit `509b75e` (listed under
"Fixed because of these runs"), OPC UA stack `github.com/gopcua/opcua`
v0.9.1, over the public internet from a workstation. Broker: a local
Mosquitto. Configurations: `configs/interop/milo-demo.yaml` and
`configs/interop/umati-sample.yaml`. The journals of these runs carry the
provisional reason-code names TrailSource used before it adopted the
trail-evidence catalog (`source.connection.lost` and so on); this record
uses the catalog names, and `docs/STATUS.md` maps one to the other.

### Servers tried

| Server | Implementation | Result |
|---|---|---|
| `opc.tcp://milo.digitalpetri.com:62541/milo` | Eclipse Milo demo server (Java), product URI `https://github.com/digitalpetri/opc-ua-demo-server` | Used. Offers None and Sign/SignAndEncrypt with Basic256Sha256, Aes128_Sha256_RsaOaep and Aes256_Sha256_RsaPss; anonymous, username and certificate tokens. |
| `opc.tcp://opcua.umati.app:4840` | umati Sample-Server, built on open62541 (C), product URI `https://github.com/umati/Sample-Server/` | Used. Same policies; anonymous and certificate tokens. Address space follows the Machinery and machine tool companion specifications. |
| `opc.tcp://opcuaserver.com:48010` | | TCP port open, GetEndpoints answered with a connection reset, twice. Not used. |
| `opc.tcp://opcua.demo-this.com:51210/UA/SampleServer` | OPC Foundation sample server (.NET) | GetEndpoints answered once; ten minutes later GetEndpoints and connect timed out (`BadTimeout`), twice. Not used. |
| `opc.tcp://uademo.prosysopc.com:53530/OPCUA/SimulationServer` | | GetEndpoints ended with EOF. Not used. |
| `opc.tcp://opcua.rocks:4840` | | No TCP connection. Not used. |

### Runs

| Run | Server, security | Window (UTC) | Records | Verify |
|---|---|---|---|---|
| A | Milo, None/None, through a local TCP relay that was stopped for 15 s | 17:07:11 to 17:09:16 | 2,473 (2,468 values) | OK; 1 restart notice (start). Same result with the trail-evidence reference verifier. |
| B | Milo, Basic256Sha256 + SignAndEncrypt, client certificate from `trailsource gen-cert` | 16:55:35 to 16:56:35 | 1,367 (1,364 values) | OK; both verifiers. |
| C | umati, Basic256Sha256 + SignAndEncrypt, same certificate | 17:03:50 to 17:06:20 | 650 (644 values) | OK; both verifiers. |

Milo monitored 48 variables: `Demo.Dynamic` (24 scalars of every builtin
type, changing) and `CTT.Static.AllProfiles.Array` (24 arrays, static).
umati monitored the first 50 variables of the dynamic sample machine
(`Production` and `Monitoring` of `FullMachineToolDynamic`); the inventory
is marked `truncated`.

### What worked

- **Sessions and security.** Anonymous sessions with None and with
  Basic256Sha256 + SignAndEncrypt on both servers. Both demo servers
  accepted the self-signed client certificate without manual trust, which
  production servers should not do. `server_thumbprint` pinning (added
  in this change) accepted Milo's certificate and refused a wrong
  thumbprint.
- **Types.** All 24 builtin scalar types and their arrays from Milo were
  encoded and hashed: Boolean, SByte to UInt64 (Int64 and UInt64 as
  decimal strings), Float and Double, String, DateTime, Guid, ByteString,
  XmlElement, NodeId, ExpandedNodeId (with `nsu=`), StatusCode,
  QualifiedName (with namespace index), LocalizedText, DataValue (nested
  value, status and timestamps), Variant (unwrapped, `value_type`
  `variant<...>` for arrays), and ExtensionObject with a decoded body. umati
  added UInteger (delivered as UInt16) and companion-specification
  enumerations (delivered as Int32).
- **Timestamps and quality.** Run A and B: every value had a source and a
  server timestamp, every value was `good`. Run C: 632 `good`, 8
  `StatusBadNodeIDUnknown` (a job deleted by the server while monitored)
  and 4 `StatusBadWaitingForInitialData`; the 8 deleted-node values arrived
  without timestamps and were recorded with `source_timestamp: null` and
  `server_timestamp: null`. Bad values are recorded, not dropped.
- **Reconnect.** Run A: the relay was stopped at 17:08:11.0. The last value
  arrived at 17:08:10.998, `source.connection_lost` (reason
  `opc connection state Closed`) was sealed at 17:08:12.097, reconnect
  attempts every 5 s failed silently, `source.connection_restored` followed
  at 17:08:28.790 after the relay was back, then a fresh browse with the
  unchanged fingerprint and a new subscription. The report shows one gap of
  16.693 s. The chain continued without restart. The health topic went
  `source.inventory_initial` → `source.connection_lost` with `last_seen`
  frozen at 17:08:10.998 → `source.connection_restored`.
- **Address-space change on a real server.** Run C: the umati server
  creates and deletes production jobs while it runs. Four generations were
  recorded: initial; a mixed change (a job's 8 nodes removed, 8 other
  nodes moved into the 50-node window; recorded then as
  `source.inventory.mixed`, now `source.node_removed`); `mixed` again with the same
  fingerprint as generation 1; and a fourth seven seconds later, triggered
  by a monitored node reporting `BadNodeIdUnknown` instead of waiting for
  the 60 s re-browse interval.
- **Browse at scale.** Milo, Objects folder: 101 nodes in 5.4 s (65 s
  before batching), identical fingerprint. A one-off wider browse visited
  3,213 nodes and collected 600 variables in 38 s with 205 requests.

### What did not work, or only partly

- **Server certificate trust.** The OPC UA stack does not check the server
  certificate against a trust list. Without `server_thumbprint`, a secured
  session proves only that it is with whoever presented the certificate in
  the endpoint list. Pinning is now available and documented in
  `docs/CONNECT_YOUR_SERVER.md`; a trust-list check is not.
- **Enumerations.** Companion-specification enumerations (for example the
  stack light's `SignalColor`, data type `ns=4;i=3004`) are recorded as the
  integer with the enumeration's NodeId as `data_type`. Names are not
  resolved; that needs the type's EnumStrings or EnumValues and is open.
- **ExtensionObject.** `type_id` is the encoding NodeId as transmitted
  (Milo's XVType array shows `i=12090`, the binary encoding, not the data
  type `i=12080`). Milo's `Demo.Dynamic.ExtensionObject` arrived as an
  ExtensionObject with TypeId `i=0` and no body; whether the server sends
  it that way or the stack could not decode it was not determined.
  Structures the stack cannot decode are recorded as `body_base64`.
- **Fingerprint scope.** In run C, a new job with a different name reused
  the NodeIds and variable BrowseNames of the previous one, so its
  generation has the same fingerprint as an earlier one although the browse
  paths differ. The fingerprint covers NodeId, BrowseName and DataType of
  each variable, by design; paths are recorded in the inventory and in every
  value record. Whether the fingerprint should include browse paths is an
  open question, because changing it changes every existing fingerprint.
- **Truncated inventories shift.** With `max_nodes` reached, a node added
  or removed early in depth-first order moves the window: run C's
  generation 2 lists 8 nodes as added that existed all along. The record
  says `truncated: true`; the reader has to take that into account.
- **Re-browse resends values.** Every re-browse recreates the subscription,
  so the server sends every node's current value again (run C: four
  records per unchanging node in 150 s, one per subscription: at start and
  after each of three re-browses). Correct, but noisy.
- **Not tested.** Username tokens (both servers were used anonymously),
  policies other than Basic256Sha256, a server restart, a half-open
  connection (network silently dropping packets instead of closing the
  socket), servers with thousands of monitored items, and any server that
  actually refuses an untrusted client certificate.

### Fixed because of these runs

All in commit `509b75e`, with tests:

1. The browse took four round trips per node (65 s for 101 nodes); now it
   batches per group of siblings (5.4 s) with the same traversal order and
   fingerprint.
2. Sampling interval 0 let Milo sample ten times per second per node
   (23,788 records in two minutes for 48 nodes in a first run of A);
   `sampling_interval_ms` now defaults to the subscription interval (2,473
   records for the same run).
3. A monitored node deleted by the server now triggers a re-browse.
4. A node deleted between Browse and Read no longer enters the inventory
   with an empty data type.
5. Guid, XmlElement, ExpandedNodeId, DataValue, Variant, ExtensionObject,
   enumerations and structures were rendered with Go's `fmt`; they now
   encode as JSON under the JCS rules.

Server certificate pinning was added after the runs, as a consequence of
the trust finding.

### How to repeat

```sh
make build
./bin/trailsource gen-cert --uri urn:trailsource:interop:milo-demo --out .demo/interop/pki
export INTEROP_ENDPOINT=opc.tcp://milo.digitalpetri.com:62541/milo \
       INTEROP_SECURITY_POLICY=Basic256Sha256 INTEROP_SECURITY_MODE=SignAndEncrypt \
       INTEROP_CERT=$PWD/.demo/interop/pki/client.der INTEROP_KEY=$PWD/.demo/interop/pki/client.key.pem \
       INTEROP_MQTT_PORT=1883 INTEROP_DIR=$PWD/.demo/interop/milo
./bin/trailsource browse --config configs/interop/milo-demo.yaml
./bin/trailsource run --config configs/interop/milo-demo.yaml      # Ctrl-C after a few minutes
./bin/trailsource verify .demo/interop/milo/journal/*.jsonl
./bin/trailsource report .demo/interop/milo/journal/*.jsonl --html .demo/interop/milo/report.html
```

For the reconnect test, run `scripts/tcp_relay.py 48410
milo.digitalpetri.com 62541`, set `INTEROP_ENDPOINT` to
`opc.tcp://127.0.0.1:48410/milo` and security to `None`, and stop and
start the relay while the adapter runs. The umati configuration works the
same way; its NodeIds were valid on 2026-11-03 and may change when the
server is rebuilt. Keep the limits in the configurations: these are shared
public servers.
