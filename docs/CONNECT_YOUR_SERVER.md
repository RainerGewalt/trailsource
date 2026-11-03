# Connect your OPC UA server

This guide takes an OPC UA server you can reach over TCP to a running,
verified TrailSource journal. Every step before `trailsource run` is a
single command that reads from the server and changes nothing on it.

TrailSource is read-only in every configuration. It calls GetEndpoints,
CreateSession, ActivateSession, Read, Browse, BrowseNext,
CreateSubscription, CreateMonitoredItems, Publish, DeleteSubscriptions and
CloseSession. It never calls Write, Call, AddNodes, DeleteNodes or
HistoryUpdate, and there is no setting that would make it do so. Give it
an account that can only read anyway: a read-only account is the control
that holds even if this paragraph is wrong.

## 1. Ask the server what it offers

```sh
make build
./bin/trailsource endpoints opc.tcp://192.0.2.10:4840
```

```
None                   None            level=0   tokens=anonymous,username     opc.tcp://192.0.2.10:4840
Basic256Sha256         Sign            level=6   tokens=anonymous,username     opc.tcp://192.0.2.10:4840
Basic256Sha256         SignAndEncrypt  level=10  tokens=anonymous,username     opc.tcp://192.0.2.10:4840
server application_uri=urn:example:server product_uri=...
server certificate thumbprint (SHA-1) c75e5e8f51168ba9aa9fd4af07836de63850e71f
```

Each line is an endpoint: security policy, message security mode, the
server's own security level, and the user token types it accepts. No
session is opened for this.

## 2. Choose the security mode

| Setting | When |
|---|---|
| `security_policy: None`, `security_mode: None` | Lab and simulators only. Everything, including a password, crosses the network in clear text. |
| `Basic256Sha256` + `Sign` | Messages are signed, not encrypted. |
| `Basic256Sha256` + `SignAndEncrypt` | The default choice for a real server. |
| `Aes128_Sha256_RsaOaep`, `Aes256_Sha256_RsaPss` | Newer policies; use them when the server offers them. |

The policy names are the ones `endpoints` prints. TrailSource has been
run with `None` and with `Basic256Sha256` + `SignAndEncrypt` against
public servers (see `docs/INTEROP.md`); the other policies come from the
OPC UA stack (`gopcua`) and have not been exercised here.

`Sign` and `SignAndEncrypt` need a client certificate (step 3).

## 3. Create and trust a client certificate

```sh
./bin/trailsource gen-cert --uri urn:trailsource:host01:extruder-01 --host host01 --out pki
```

This writes `pki/client.der` (certificate) and `pki/client.key.pem` (RSA
2048 private key, mode 0600) and prints the SHA-1 thumbprint. The URI
becomes the certificate's subject alternative name and the session's
ApplicationURI; most servers reject a session whose ApplicationURI differs
from the certificate, so keep them identical (TrailSource takes it from the
certificate when one is configured). Use one URI per adapter instance.

```yaml
source:
  security_policy: Basic256Sha256
  security_mode: SignAndEncrypt
  certificate_file: /etc/trailsource/pki/client.der
  private_key_file: /etc/trailsource/pki/client.key.pem
```

The server has to trust the certificate. Most servers move an unknown
client certificate to a "rejected" list on the first attempt; an
administrator moves it to "trusted" (the thumbprint identifies it). The
first connect then fails with a certificate error, the second succeeds.
Some demo servers trust every certificate; production servers should not.

Keep the private key out of Git and out of images; mount it.

### Pin the server certificate

The OPC UA stack TrailSource uses does not check the server's certificate
against a trust list. It takes the certificate from the endpoint list and
verifies that the session was signed with it, which proves the session is
with whoever presented that certificate, not that this is your server. Pin
it:

```yaml
source:
  server_thumbprint: "c75e5e8f51168ba9aa9fd4af07836de63850e71f"   # from `trailsource endpoints`
```

With a pin, TrailSource refuses to connect to any other certificate, to an
unsecured endpoint, and when the endpoint list is unavailable. Check the
thumbprint out of band (the server's own certificate store or its
administrator), not only from `endpoints` over the same network. Every
inventory and connection record of a secured session carries the
thumbprint it saw in `payload.session.server_certificate_thumbprint`. When
the server certificate is renewed, update the pin.

## 4. User identity

```yaml
source:
  auth: username                       # or anonymous
  username: "${TRAILSOURCE_OPCUA_USERNAME}"
  password: "${TRAILSOURCE_OPCUA_PASSWORD}"
```

Values come from the environment. `username` and `password` are masked
before `config_version` is computed, so rotating them does not change the
hash and the hash never contains them. Use an account whose role grants
read and browse only. The access levels the session saw (`access_level`,
`user_access_level`, `writable`, `user_writable`) are recorded for every
node, so a reviewer can later see what this account could have written,
even though TrailSource did not.

## 5. Browse once

Copy `configs/opcua-extruder.yaml`, set `endpoint`, the security settings
and `browse_roots`, then:

```sh
./bin/trailsource browse --config my-server.yaml
```

```
endpoint     opc.tcp://192.0.2.10:4840
session      auth=anonymous policy=Basic256Sha256 mode=SignAndEncrypt server_uri=urn:example:server
roots        ns=3;s=Line1
variables    412 (visited 530 nodes with 61 requests in 3.2s)
fingerprint  sha256:...

  r  float64      Line1.Extruder.MeltPressure             ns=3;s=Line1.Extruder.MeltPressure
  rw float64      Line1.Extruder.Zone1.Setpoint           ns=3;s=Line1.Extruder.Zone1.Setpoint
  ...
```

This is exactly the browse the adapter does at start, without MQTT,
journal or state files. Adjust until the list is the data you want:

- `browse_roots`: NodeIds where the walk starts (`ns=3;s=Line1`,
  `ns=3;i=1001`). Browsing `ns=0;i=85` (the Objects folder) also walks the
  server's own diagnostics, often thousands of nodes; start lower.
- `max_depth` (default 16) and `max_nodes` (default 2500): when
  `max_nodes` stops the walk, `browse` says `truncated`, and the inventory
  records carry `truncated: true`. The inventory is then the first
  `max_nodes` variables in depth-first order, the same on every browse
  while the server does not change, but not the whole address space.
- `browse_timeout_s` (default 120): for the whole walk. The walk batches
  its requests (one Browse and one Read per group of siblings); against a
  public server over the internet, 3213 nodes took 38 seconds.
- `rebrowse_interval_s` (default 30): how often the adapter browses again
  to detect address-space changes. Every re-browse also recreates the
  subscription, so the server sends the current value of every node once
  more. On large servers 300 seconds or more is reasonable. A monitored
  node that the server deletes triggers a re-browse on its own.
- `sampling_interval_ms` (default: `subscription_interval_ms`): how often
  the server samples each node. `0` asks for the server's fastest rate,
  which on some servers means ten values per node and second.

## 6. Generate the mapping file from the first browse

```sh
./bin/trailsource browse --config my-server.yaml --mapping-out configs/mapping/my-server.yaml
```

The skeleton has one rule per node, with the exact browse path and a
derived role (`MeltPressure` becomes `melt_pressure`), and the data type,
access and NodeId as a comment:

```yaml
rules:
  - path: Line1.Extruder.MeltPressure # float64, r, ns=3;s=Line1.Extruder.MeltPressure
    semantic_role: melt_pressure
```

Edit it before use: rename roles to your vocabulary, add `unit` (UN/CEFACT
Rec. 20 codes such as `BAR`, `CEL`, `RPM`), `scale_decimals` for PLC-style
scaled integers, `context: true` for recipe, order and batch variables
that every record should repeat, and replace groups of exact paths with
one `pattern` rule (`Line1.Extruder.Zone*.Setpoint`). Delete rules you do
not need: unmatched nodes still get a derived role. Bump `version` on
every change; the file's hash travels in every record as
`context.mapping.hash`. Then set `mapping_file` in the configuration.

## 7. Run, check, verify

```sh
./bin/trailsource config-version --config my-server.yaml   # the hash every record will carry
./bin/trailsource run --config my-server.yaml
mosquitto_sub -h <broker> -t 'trailsource/+/+/+/health' -v
```

The retained health message shows the state of the source as
`last_event` and `last_seen` (see `docs/STATUS.md`):
`source.connection.established` or `source.inventory.initial` with a
`last_seen` a second old is a source that delivers.

After a few minutes:

```sh
./bin/trailsource verify journal/<source-id>.jsonl
./bin/trailsource report journal/<source-id>.jsonl --html report.html
```

## When it does not work

| Symptom | Likely cause |
|---|---|
| `BadCertificateUntrusted`, `BadSecurityChecksFailed` on first connect | The server has not trusted the client certificate yet (step 3). |
| `BadCertificateUriInvalid` | The certificate's URI SAN and the ApplicationURI differ; regenerate with `gen-cert --uri`. |
| `BadIdentityTokenRejected`, `BadUserAccessDenied` at session level | Wrong user or token type not offered on that endpoint (`endpoints` lists the token types). |
| `browse root ... unreadable: BadNodeIdUnknown` | The root NodeId does not exist; namespace indexes can differ between servers and firmware versions. |
| `browse: ... context deadline exceeded` | Raise `browse_timeout_s` or narrow `browse_roots`. |
| `monitor rejected ... BadTooManyMonitoredItems` in the log | The server limits monitored items per session; lower `max_nodes`. |
| `server certificate thumbprint ... does not match server_thumbprint` | The server presents a different certificate: renewed, or not your server. Check before updating the pin. |
| Values with `quality: bad` and `status_name` `StatusBadNodeIDUnknown` | The server deleted the node; a re-browse follows and records the change. |
