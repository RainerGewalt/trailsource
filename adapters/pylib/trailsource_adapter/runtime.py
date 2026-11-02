"""Config loading, sealing, journaling, MQTT publishing and health for the
polling adapters. The record format is the same as the Go adapter's; see
docs/RECORD_MAPPING.md.
"""
from __future__ import annotations

import json
import logging
import os
import socket
import threading
import time
from decimal import Decimal
from typing import Any, Dict, List, Optional, Tuple
from urllib.parse import urlparse

import paho.mqtt.client as mqtt
import yaml

from trailsource_evidence import FileStore, Sealer, canonical_jcs, format_time, sha256_hash, utc_now
from trailsource_evidence.config import config_version, expand_env

log = logging.getLogger("trailsource")

VERSION = os.getenv("TRAILSOURCE_VERSION", "0.1.0-dev")

EVENT_VALUE = "source.value.observed"
EVENT_INVENTORY = "source.inventory.changed"
EVENT_CONNECTION = "source.connection.changed"


def token(s: str) -> str:
    out: List[str] = []
    dash = False
    for ch in str(s).strip().lower():
        if ch.isalnum() and ord(ch) < 128:
            out.append(ch)
            dash = False
        elif not dash:
            out.append("-")
            dash = True
    return "".join(out).strip("-")


def decimal_string(value: Any) -> Optional[str]:
    """Render a number as a plain decimal string (no exponent) for hashed payloads."""
    if value is None:
        return None
    d = value if isinstance(value, Decimal) else Decimal(str(value))
    s = format(d.normalize(), "f")
    if "." in s:
        s = s.rstrip("0").rstrip(".")
    return s or "0"


def load_config(path: str) -> Tuple[Dict[str, Any], List[str]]:
    with open(path, "r", encoding="utf-8") as fh:
        text, missing = expand_env(fh.read())
    cfg = yaml.safe_load(text) or {}
    cfg["_path"] = path
    cfg["_config_version"] = config_version(cfg if "_path" not in cfg else {k: v for k, v in cfg.items() if not k.startswith("_")})
    _apply_defaults(cfg)
    return cfg, missing


def _apply_defaults(cfg: Dict[str, Any]) -> None:
    for key in ("site", "line", "device"):
        if not cfg.get(key):
            raise ValueError("config: %s is required" % key)
    src = cfg.setdefault("source", {})
    protocol = src.get("protocol")
    if not protocol:
        raise ValueError("config: source.protocol is required")
    src.setdefault("id", "-".join(token(cfg[k]) for k in ("site", "line", "device")) + "-" + token(protocol))
    src.setdefault("poll_interval_s", 1.0)
    cfg.setdefault("revision", "0")
    producer = cfg.setdefault("producer", {})
    producer.setdefault("id", "trailsource-" + src["id"])
    m = cfg.setdefault("mqtt", {})
    m.setdefault("broker", "tcp://localhost:1883")
    m.setdefault("client_id", producer["id"])
    m.setdefault("qos", 1)
    m.setdefault("topic_prefix", "trailsource/%s/%s/%s" % tuple(token(cfg[k]) for k in ("site", "line", "device")))
    m["topic_prefix"] = m["topic_prefix"].strip("/")
    m.setdefault("health_interval_s", 15)
    cfg.setdefault("state_dir", "state")
    j = cfg.setdefault("journal", {})
    j.setdefault("enabled", True)
    if j["enabled"] and not j.get("path"):
        j["path"] = os.path.join("journal", src["id"] + ".jsonl")


def _broker(url: str) -> Tuple[str, int, bool]:
    p = urlparse(url if "://" in url else "tcp://" + url)
    tls = p.scheme in ("ssl", "tls", "mqtts")
    return p.hostname or "localhost", p.port or (8883 if tls else 1883), tls


class AdapterRuntime:
    """One source: sealer, journal, MQTT connection, health."""

    def __init__(self, cfg: Dict[str, Any], protocol: str, point_list: List[Dict[str, Any]]) -> None:
        self.cfg = cfg
        self.protocol = protocol
        self.source_id: str = cfg["source"]["id"]
        self.producer_id: str = cfg["producer"]["id"]
        self.version = cfg["producer"].get("version") or VERSION
        self.topic_prefix: str = cfg["mqtt"]["topic_prefix"]
        self.config_version: str = cfg["_config_version"]
        # The configured point list is this source's address space.
        self.point_list = point_list
        self.inventory_hash = sha256_hash(point_list)
        self.connected = False
        self.last_error = ""
        self.records_emitted = 0
        self._lock = threading.Lock()

        producer = {"kind": "source-bridge", "id": self.producer_id, "version": self.version}
        store = FileStore(os.path.join(cfg["state_dir"], self.source_id + ".chain.json"))
        self.sealer = Sealer(store, producer, self.source_id)

        self._journal = None
        if cfg["journal"]["enabled"]:
            os.makedirs(os.path.dirname(cfg["journal"]["path"]) or ".", exist_ok=True)
            self._journal = open(cfg["journal"]["path"], "a", encoding="utf-8")

        self._client = self._mqtt_client()
        self._health_stop = threading.Event()
        self._health_thread = threading.Thread(target=self._health_loop, daemon=True)
        self._health_thread.start()

        seq, chain_hash, sealed = self.sealer.position
        log.info("trailsource %s adapter %s source_id=%s topic_prefix=%s config_version=%s inventory_hash=%s chain_seq=%d resumed=%s",
                 protocol, self.version, self.source_id, self.topic_prefix, self.config_version, self.inventory_hash, seq, sealed)

    # -- MQTT -----------------------------------------------------------------
    def _mqtt_client(self) -> mqtt.Client:
        m = self.cfg["mqtt"]
        host, port, tls = _broker(m["broker"])
        client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2, client_id=m["client_id"], protocol=mqtt.MQTTv311)
        if m.get("username"):
            client.username_pw_set(m["username"], m.get("password") or None)
        tls_cfg = m.get("tls") or {}
        if tls or tls_cfg.get("enabled"):
            import ssl
            insecure = bool(tls_cfg.get("insecure_skip_verify"))
            client.tls_set(ca_certs=tls_cfg.get("ca_cert") or None, certfile=tls_cfg.get("client_cert") or None,
                           keyfile=tls_cfg.get("client_key") or None,
                           cert_reqs=ssl.CERT_NONE if insecure else ssl.CERT_REQUIRED)
            client.tls_insecure_set(insecure)
        will = canonical_jcs({"status": "down", "reason": "will", "producer_id": self.producer_id, "source_id": self.source_id})
        client.will_set(self.topic_prefix + "/health", will, qos=int(m["qos"]), retain=True)
        client.reconnect_delay_set(min_delay=2, max_delay=20)

        def on_connect(c, _u, _f, reason_code, _p=None):
            log.info("mqtt connected broker=%s:%s rc=%s", host, port, reason_code)
            self.publish_health("up")

        def on_disconnect(c, _u, _f, reason_code, _p=None):
            log.warning("mqtt disconnected rc=%s", reason_code)

        client.on_connect = on_connect
        client.on_disconnect = on_disconnect
        client.connect(host, port, keepalive=30)
        client.loop_start()
        return client

    def _publish(self, topic: str, body: bytes, retain: bool = False, wait: bool = True) -> None:
        info = self._client.publish(topic, body, qos=int(self.cfg["mqtt"]["qos"]), retain=retain)
        if info.rc != mqtt.MQTT_ERR_SUCCESS:
            raise RuntimeError("mqtt publish failed topic=%s rc=%s" % (topic, info.rc))
        if wait:
            # Never wait inside a paho callback: callbacks run on the network
            # thread and waiting there blocks the thread that must complete
            # the publish. publish_health() therefore passes wait=False.
            info.wait_for_publish(timeout=10)

    # -- records ----------------------------------------------------------------
    def actor(self) -> Dict[str, Any]:
        return {"auth": "self-declared", "id": self.producer_id, "roles": ["collector"]}

    def config_policy(self) -> Dict[str, Any]:
        return {"id": "config:" + os.path.basename(self.cfg["_path"]), "version": str(self.cfg["revision"]),
                "hash": self.config_version, "kind": "config"}

    def addressspace_policy(self) -> Dict[str, Any]:
        return {"id": self.source_id + "/pointlist", "version": str(self.cfg["revision"]),
                "hash": self.inventory_hash, "kind": "addressspace"}

    def context(self) -> Dict[str, Any]:
        src = self.cfg["source"]
        return {"site": self.cfg["site"], "line": self.cfg["line"], "device": self.cfg["device"],
                "protocol": self.protocol, "endpoint": src.get("endpoint") or src.get("url") or "",
                "config_version": self.config_version, "host": socket.gethostname()}

    def emit(self, event_type: str, payload: Dict[str, Any], policy: Dict[str, Any], topic_suffix: str,
             occurred_at: Optional[str] = None, read_at: Optional[str] = None) -> Dict[str, Any]:
        """Seal, journal, publish (in that order). Not published if not journaled."""
        time_block: Dict[str, Any] = {"source": "system", "read_at": read_at or format_time(utc_now())}
        if occurred_at:
            time_block["occurred_at"] = occurred_at
        record = {"event_type": event_type, "time": time_block, "actor": self.actor(), "policy": policy,
                  "payload": payload, "context": self.context()}
        self.sealer.seal(record)
        line = json.dumps(record, ensure_ascii=False, separators=(",", ":"))
        if self._journal is not None:
            self._journal.write(line + "\n")
            self._journal.flush()
        with self._lock:
            self.records_emitted += 1
        self._publish("%s/%s" % (self.topic_prefix, topic_suffix), line.encode("utf-8"))
        return record

    def emit_inventory(self, extra: Dict[str, Any]) -> None:
        """Publish the configured point list as the address space of this source.

        Emitted once per start when the list hash differs from the persisted one.
        """
        snap_path = os.path.join(self.cfg["state_dir"], self.source_id + ".inventory.json")
        previous = None
        if os.path.exists(snap_path):
            with open(snap_path, "r", encoding="utf-8") as fh:
                previous = json.load(fh)
        if previous and previous.get("fingerprint") == self.inventory_hash:
            return
        generation = (previous or {}).get("generation", 0) + 1
        payload = {"change_kind": "changed" if previous else "initial", "fingerprint": self.inventory_hash,
                   "previous_fingerprint": (previous or {}).get("fingerprint"), "generation": generation,
                   "node_count": len(self.point_list), "nodes": self.point_list}
        payload.update(extra)
        self.emit(EVENT_INVENTORY, payload, self.config_policy(), "inventory")
        os.makedirs(os.path.dirname(snap_path) or ".", exist_ok=True)
        tmp = snap_path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump({"source_id": self.source_id, "generation": generation, "fingerprint": self.inventory_hash,
                       "taken_at": format_time(utc_now()), "items": self.point_list}, fh, indent=2)
        os.replace(tmp, snap_path)

    def set_connected(self, state: bool, reason: str = "", endpoint: str = "") -> None:
        if state == self.connected:
            return
        self.connected = state
        payload: Dict[str, Any] = {"state": "connected" if state else "disconnected", "endpoint": endpoint}
        if reason:
            payload["reason"] = reason
            self.last_error = reason
        try:
            self.emit(EVENT_CONNECTION, payload, self.config_policy(), "status")
        except Exception as exc:  # the broker may be the problem
            log.warning("emit connection record failed: %s", exc)

    # -- health -----------------------------------------------------------------
    def publish_health(self, status: str) -> None:
        seq, chain_hash, _ = self.sealer.position
        body = {"status": status, "producer_id": self.producer_id, "producer_version": self.version,
                "source_id": self.source_id, "stream_id": self.source_id, "protocol": self.protocol,
                "source_connected": self.connected, "inventory_hash": self.inventory_hash, "seq": seq,
                "chain_hash": chain_hash, "config_version": self.config_version,
                "records_emitted": self.records_emitted, "last_error": self.last_error,
                "ts": format_time(utc_now())}
        try:
            self._publish(self.topic_prefix + "/health", json.dumps(body, separators=(",", ":")).encode(),
                          retain=True, wait=False)
        except Exception as exc:
            log.warning("publish health failed: %s", exc)

    def _health_loop(self) -> None:
        interval = float(self.cfg["mqtt"]["health_interval_s"])
        while not self._health_stop.wait(interval):
            self.publish_health("up")

    def close(self) -> None:
        self._health_stop.set()
        self.publish_health("down")
        self._client.loop_stop()
        self._client.disconnect()
        if self._journal is not None:
            self._journal.close()


def sleep_until(deadline: float, stop: threading.Event) -> None:
    remaining = deadline - time.monotonic()
    if remaining > 0:
        stop.wait(remaining)
