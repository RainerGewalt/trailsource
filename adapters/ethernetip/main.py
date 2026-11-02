"""TrailSource EtherNet/IP adapter (v0.1: simulator shim driver).

Reads named tags from a robot-cell controller and publishes them as sealed
source.value.observed records. In v0.1 the only driver is `http-shim`: it
polls the JSON endpoint of the EtherNet/IP simulator in sim/ethernetip. CIP
explicit messaging (RegisterSession, Read Tag service 0x4C) is not
implemented yet; the driver boundary is `read_tags()` below so a real CIP
driver can replace the shim without touching the record mapping.
"""
from __future__ import annotations

import json
import logging
import os
import signal
import sys
import threading
import time
from typing import Any, Dict, List, Optional
from urllib.error import URLError
from urllib.request import urlopen

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "pylib"))

from trailsource_adapter import AdapterRuntime, decimal_string, load_config  # noqa: E402
from trailsource_adapter.runtime import EVENT_VALUE, sleep_until, token  # noqa: E402
from trailsource_evidence import format_time, utc_now  # noqa: E402

logging.basicConfig(level=os.getenv("LOG_LEVEL", "INFO").upper(),
                    format="%(asctime)s %(levelname)s [ethernetip-adapter] %(message)s")
log = logging.getLogger("trailsource")


def tag_list(cfg: Dict[str, Any]) -> List[Dict[str, Any]]:
    items = []
    for t in cfg["source"].get("tags", []):
        items.append({
            "node_id": t["name"],  # CIP symbolic tag path, e.g. Program:Robot.Mode
            "name": t["name"],
            "cip_type": t.get("type", "UNKNOWN"),
            "unit": t.get("unit", ""),
            "semantic_role": t.get("semantic_role") or token(t["name"]).replace("-", "_"),
            "writable": bool(t.get("writable", False)),
        })
    return items


def read_tags(url: str, timeout: float) -> Dict[str, Dict[str, Any]]:
    """Driver boundary. Returns {tag_name: {"value":..., "type":..., "status":...}}."""
    with urlopen(url, timeout=timeout) as response:
        body = json.loads(response.read().decode("utf-8"))
    tags = body.get("tags")
    if not isinstance(tags, list):
        raise ValueError("shim response has no tags list")
    return {str(t["name"]): t for t in tags}


def encode(value: Any, cip_type: str) -> Any:
    if value is None or isinstance(value, bool) or isinstance(value, int):
        return value, ("null" if value is None else "bool" if isinstance(value, bool) else "int")
    if isinstance(value, float):
        return (decimal_string(round(value, 6)), "float") if cip_type != "REAL" else ("%.7g" % value, "float32")
    return str(value), "string"


def main() -> int:
    path = os.getenv("TRAILSOURCE_CONFIG", "/config/ethernetip-robot-cell.yaml")
    if len(sys.argv) > 2 and sys.argv[1] == "--config":
        path = sys.argv[2]
    cfg, missing = load_config(path)
    for m in missing:
        log.warning("config: environment variable %s is not set", m)
    src = cfg["source"]
    driver = src.get("driver", "http-shim")
    if driver != "http-shim":
        log.error("driver %r is not implemented in v0.1 (only http-shim)", driver)
        return 2
    tags = tag_list(cfg)
    rt = AdapterRuntime(cfg, "ethernet_ip", tags)
    url = src["url"]
    src["endpoint"] = url

    stop = threading.Event()
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: stop.set())

    interval = float(src.get("poll_interval_s", 1.0))
    backoff = float(src.get("session_backoff_s", 5))
    timeout = float(src.get("timeout_s", 3))
    try:
        rt.emit_inventory({"endpoint": url, "driver": driver})
        while not stop.is_set():
            deadline = time.monotonic() + interval
            try:
                read_at = format_time(utc_now())
                readings = read_tags(url, timeout)
                rt.set_connected(True, endpoint=url)
                for tag in tags:
                    r: Optional[Dict[str, Any]] = readings.get(tag["name"])
                    value, value_type = encode(None if r is None else r.get("value"), tag["cip_type"])
                    status = "ok" if r is not None and r.get("status", "ok") == "ok" else (r or {}).get("status", "missing")
                    payload = {
                        "native_node_id": tag["node_id"],
                        "name": tag["name"],
                        "entity_key": "%s/%s" % (rt.source_id, token(tag["name"])),
                        "semantic_role": tag["semantic_role"],
                        "semantic_role_source": "mapping",
                        "cip_type": tag["cip_type"],
                        "value": value,
                        "value_type": value_type,
                        "unit": tag["unit"],
                        "status": status,
                        "quality": "good" if status == "ok" else "bad",
                        "source_timestamp": None,  # CIP explicit reads carry no timestamp
                        "server_timestamp": None,
                        "inventory_hash": rt.inventory_hash,
                        "publish_reason": "poll",
                        "driver": driver,
                        "access": {"writable": tag["writable"]},
                    }
                    rt.emit(EVENT_VALUE, payload, rt.addressspace_policy(), "values", read_at=read_at)
            except (URLError, OSError, ValueError) as exc:
                log.warning("poll failed: %s", exc)
                rt.set_connected(False, reason=str(exc), endpoint=url)
                stop.wait(backoff)
                continue
            except Exception as exc:
                log.error("unexpected error: %s", exc)
                rt.last_error = str(exc)
            sleep_until(deadline, stop)
    finally:
        rt.set_connected(False, reason="shutdown", endpoint=url)
        rt.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
