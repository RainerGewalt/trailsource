"""EtherNet/IP simulator shim: a robot cell controller (stdlib only).

v0.1 does not speak CIP. The controller's tags are served as JSON on
GET /tags so the adapter's record mapping can be exercised end to end; the
adapter's driver boundary is where a real CIP explicit-messaging client
(RegisterSession, SendRRData, Read Tag 0x4C) will plug in.

Tags:
    Program:Robot.Mode          STRING   RUNNING | WAITING | FAULT | MANUAL
    Program:Robot.ProgramName   STRING
    Program:Robot.CycleCount    DINT
    Program:Robot.CycleTimeS    REAL
    Program:Robot.ServoLoadPct  REAL
    Program:Robot.ToolTempC     REAL
    Program:Safety.GateClosed   BOOL
    Program:Cell.PartsGood      DINT
    Program:Cell.PartsReject    DINT
"""
import json
import logging
import math
import os
import random
import signal
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG_LEVEL = os.getenv("LOG_LEVEL", "INFO").upper()
HOST = os.getenv("HTTP_HOST", "0.0.0.0")
PORT = int(os.getenv("HTTP_PORT", "8080"))
SIM_INTERVAL_SECONDS = float(os.getenv("SIM_INTERVAL_SECONDS", "1.0"))

logging.basicConfig(level=LOG_LEVEL, format="%(asctime)s %(levelname)s [ethernetip-sim] %(message)s")
logger = logging.getLogger(__name__)

LOCK = threading.Lock()
TAGS = {}
RUNNING = True


class Simulation:
    def __init__(self) -> None:
        self.tick = 0
        self.cycles = random.randint(12000, 18000)
        self.good = self.cycles - 37
        self.reject = 37
        self.mode = "RUNNING"

    def step(self) -> None:
        self.tick += 1
        wave = math.sin(self.tick / 18.0)
        if self.tick % 240 < 8:
            self.mode = "WAITING"
        elif self.tick % 900 in range(400, 412):
            self.mode = "FAULT"
        else:
            self.mode = "RUNNING"
        if self.mode == "RUNNING" and self.tick % 9 == 0:
            self.cycles += 1
            if random.random() < 0.02:
                self.reject += 1
            else:
                self.good += 1
        snapshot = {
            "Program:Robot.Mode": ("STRING", self.mode),
            "Program:Robot.ProgramName": ("STRING", "PALLETIZE_110"),
            "Program:Robot.CycleCount": ("DINT", self.cycles),
            "Program:Robot.CycleTimeS": ("REAL", round(8.6 + wave * 0.3 + random.uniform(-0.05, 0.05), 2)),
            "Program:Robot.ServoLoadPct": ("REAL", round(48.0 + wave * 19.0 + random.uniform(-3.0, 3.0), 2)),
            "Program:Robot.ToolTempC": ("REAL", round(34.0 + abs(wave) * 8.0 + random.uniform(-0.5, 0.8), 2)),
            "Program:Safety.GateClosed": ("BOOL", self.tick % 240 > 8),
            "Program:Cell.PartsGood": ("DINT", self.good),
            "Program:Cell.PartsReject": ("DINT", self.reject),
        }
        with LOCK:
            TAGS.clear()
            TAGS.update(snapshot)
        if self.tick % 10 == 0:
            logger.info("mode=%s cycles=%d good=%d reject=%d", self.mode, self.cycles, self.good, self.reject)


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if self.path == "/health":
            body = json.dumps({"status": "ok"}).encode()
        elif self.path in ("/", "/tags"):
            with LOCK:
                tags = [{"name": n, "type": t, "value": v, "status": "ok"} for n, (t, v) in TAGS.items()]
            body = json.dumps({"controller": "robot-cell-plc-sim", "tags": tags}, separators=(",", ":")).encode()
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args) -> None:
        logger.debug(fmt, *args)


def main() -> None:
    global RUNNING
    sim = Simulation()
    sim.step()
    server = ThreadingHTTPServer((HOST, PORT), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    logger.info("shim listening on http://%s:%d/tags", HOST, PORT)

    def stop(*_):
        global RUNNING
        RUNNING = False

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    while RUNNING:
        time.sleep(SIM_INTERVAL_SECONDS)
        sim.step()
    server.shutdown()


if __name__ == "__main__":
    main()
