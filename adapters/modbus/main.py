"""TrailSource Modbus TCP adapter.

Polls holding/input registers of one Modbus TCP unit and publishes every
reading as a sealed source.value.observed record. The register list in the
configuration is the address space of the source; its hash is the
`addressspace` policy of every value record. Modbus has no timestamps of its
own, so source_timestamp and server_timestamp are explicitly null.
"""
from __future__ import annotations

import logging
import os
import signal
import socket
import struct
import sys
import threading
import time
from decimal import Decimal
from typing import Any, Dict, List, Optional, Tuple

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "pylib"))

from trailsource_adapter import AdapterRuntime, decimal_string, load_config  # noqa: E402
from trailsource_adapter.runtime import EVENT_VALUE, sleep_until, token  # noqa: E402
from trailsource_evidence import format_time, utc_now  # noqa: E402

logging.basicConfig(level=os.getenv("LOG_LEVEL", "INFO").upper(),
                    format="%(asctime)s %(levelname)s [modbus-adapter] %(message)s")
log = logging.getLogger("trailsource")

EXCEPTIONS = {1: "IllegalFunction", 2: "IllegalDataAddress", 3: "IllegalDataValue", 4: "ServerDeviceFailure",
              5: "Acknowledge", 6: "ServerDeviceBusy", 10: "GatewayPathUnavailable", 11: "GatewayTargetFailed"}
TYPE_REGS = {"uint16": 1, "int16": 1, "bool": 1, "uint32": 2, "int32": 2, "float32": 2}


class ModbusError(Exception):
    def __init__(self, code: int) -> None:
        super().__init__("modbus exception %d (%s)" % (code, EXCEPTIONS.get(code, "Unknown")))
        self.code = code


class ModbusClient:
    """Minimal Modbus TCP client: FC3 (holding) and FC4 (input) register reads."""

    def __init__(self, host: str, port: int, unit_id: int, timeout: float) -> None:
        self.host, self.port, self.unit_id, self.timeout = host, port, unit_id, timeout
        self._sock: Optional[socket.socket] = None
        self._tid = 0

    def connect(self) -> None:
        self.close()
        self._sock = socket.create_connection((self.host, self.port), timeout=self.timeout)

    def close(self) -> None:
        if self._sock is not None:
            try:
                self._sock.close()
            finally:
                self._sock = None

    def read(self, table: str, address: int, count: int) -> List[int]:
        if self._sock is None:
            raise ConnectionError("not connected")
        fc = 3 if table == "holding" else 4
        self._tid = (self._tid + 1) & 0xFFFF
        pdu = struct.pack(">BHH", fc, address, count)
        mbap = struct.pack(">HHHB", self._tid, 0, len(pdu) + 1, self.unit_id)
        self._sock.sendall(mbap + pdu)
        header = self._recv(7)
        tid, _pid, length, _uid = struct.unpack(">HHHB", header)
        body = self._recv(length - 1)
        if tid != self._tid:
            raise ConnectionError("transaction id mismatch")
        if body[0] & 0x80:
            raise ModbusError(body[1])
        byte_count = body[1]
        regs = struct.unpack(">%dH" % (byte_count // 2), body[2:2 + byte_count])
        return list(regs)

    def _recv(self, n: int) -> bytes:
        assert self._sock is not None
        buf = b""
        while len(buf) < n:
            chunk = self._sock.recv(n - len(buf))
            if not chunk:
                raise ConnectionError("connection closed by peer")
            buf += chunk
        return buf


def decode(regs: List[int], data_type: str, word_order: str) -> Any:
    if data_type in ("uint16", "int16", "bool"):
        raw = regs[0]
        if data_type == "bool":
            return raw != 0
        return raw - 0x10000 if data_type == "int16" and raw >= 0x8000 else raw
    words = regs[:2] if word_order == "big" else [regs[1], regs[0]]
    packed = struct.pack(">HH", *words)
    if data_type == "uint32":
        return struct.unpack(">I", packed)[0]
    if data_type == "int32":
        return struct.unpack(">i", packed)[0]
    if data_type == "float32":
        return struct.unpack(">f", packed)[0]
    raise ValueError("unsupported type %s" % data_type)


def point_list(cfg: Dict[str, Any]) -> List[Dict[str, Any]]:
    src = cfg["source"]
    items = []
    for p in src.get("points", []):
        data_type = p.get("type", "uint16")
        if data_type not in TYPE_REGS:
            raise ValueError("point %s: unsupported type %s" % (p.get("name"), data_type))
        table = p.get("table", "holding")
        prefix = "hr" if table == "holding" else "ir"
        items.append({
            "node_id": "%s:%d" % (prefix, int(p["address"])),
            "name": p["name"],
            "table": table,
            "address": int(p["address"]),
            "register_count": TYPE_REGS[data_type],
            "data_type": data_type,
            "word_order": p.get("word_order", src.get("word_order", "big")),
            "scale": str(p["scale"]) if p.get("scale") is not None else None,
            "unit": p.get("unit", ""),
            "semantic_role": p.get("semantic_role") or token(p["name"]).replace("-", "_"),
            "writable": bool(p.get("writable", table == "holding")),
        })
    return items


def value_payload(rt: AdapterRuntime, point: Dict[str, Any], regs: Optional[List[int]], error: Optional[Exception]) -> Dict[str, Any]:
    value: Any = None
    value_type = "null"
    engineering: Optional[str] = None
    status, quality = "ok", "good"
    if error is not None:
        quality = "bad"
        status = "exception:%d" % error.code if isinstance(error, ModbusError) else "error:" + type(error).__name__
    elif regs is not None:
        decoded = decode(regs, point["data_type"], point["word_order"])
        if isinstance(decoded, bool):
            value, value_type = decoded, "bool"
        elif isinstance(decoded, int):
            value, value_type = decoded, point["data_type"]
        else:
            value, value_type = "%.7g" % decoded, "float32"
        if point["scale"] is not None and isinstance(decoded, int) and not isinstance(decoded, bool):
            engineering = decimal_string(Decimal(decoded) * Decimal(point["scale"]))
    payload: Dict[str, Any] = {
        "native_node_id": point["node_id"],
        "table": point["table"],
        "address": point["address"],
        "register_count": point["register_count"],
        "raw_registers": regs,
        "name": point["name"],
        "entity_key": "%s/%s" % (rt.source_id, token(point["name"])),
        "semantic_role": point["semantic_role"],
        "semantic_role_source": "mapping",
        "data_type": point["data_type"],
        "word_order": point["word_order"],
        "value": value,
        "value_type": value_type,
        "unit": point["unit"],
        "status": status,
        "quality": quality,
        "source_timestamp": None,  # Modbus carries no timestamps; that absence is information
        "server_timestamp": None,
        "inventory_hash": rt.inventory_hash,
        "publish_reason": "poll",
        "access": {"writable": point["writable"]},
    }
    if point["scale"] is not None:
        payload["scale"] = point["scale"]
        payload["engineering_value"] = engineering
    return payload


def main() -> int:
    path = os.getenv("TRAILSOURCE_CONFIG", "/config/modbus-energy-meter.yaml")
    if len(sys.argv) > 2 and sys.argv[1] == "--config":
        path = sys.argv[2]
    cfg, missing = load_config(path)
    for m in missing:
        log.warning("config: environment variable %s is not set", m)
    src = cfg["source"]
    points = point_list(cfg)
    rt = AdapterRuntime(cfg, "modbus_tcp", points)
    endpoint = "modbus-tcp://%s:%d/unit/%d" % (src["host"], int(src.get("port", 502)), int(src.get("unit_id", 1)))
    src["endpoint"] = endpoint

    stop = threading.Event()
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: stop.set())

    client = ModbusClient(src["host"], int(src.get("port", 502)), int(src.get("unit_id", 1)), float(src.get("timeout_s", 3)))
    interval = float(src.get("poll_interval_s", 1.0))
    backoff = float(src.get("session_backoff_s", 5))
    try:
        rt.emit_inventory({"endpoint": endpoint, "unit_id": int(src.get("unit_id", 1))})
        while not stop.is_set():
            deadline = time.monotonic() + interval
            try:
                if not rt.connected:
                    client.connect()
                    rt.set_connected(True, endpoint=endpoint)
                read_at = format_time(utc_now())
                for point in points:
                    regs: Optional[List[int]] = None
                    error: Optional[Exception] = None
                    try:
                        regs = client.read(point["table"], point["address"], point["register_count"])
                    except ModbusError as exc:  # isolated per point
                        error = exc
                    rt.emit(EVENT_VALUE, value_payload(rt, point, regs, error), rt.addressspace_policy(), "values", read_at=read_at)
            except (OSError, ConnectionError) as exc:
                log.warning("poll failed: %s", exc)
                client.close()
                rt.set_connected(False, reason=str(exc), endpoint=endpoint)
                stop.wait(backoff)
                continue
            except Exception as exc:
                log.error("unexpected error: %s", exc)
                rt.last_error = str(exc)
            sleep_until(deadline, stop)
    finally:
        client.close()
        rt.set_connected(False, reason="shutdown", endpoint=endpoint)
        rt.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
