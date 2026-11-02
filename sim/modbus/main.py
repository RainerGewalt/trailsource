"""Modbus TCP simulator: a line energy meter (stdlib only).

Holding registers (FC3), 0-based addresses, 32-bit values big-endian word order:
    0-1  LineVoltageV      float32   V
    2-3  PhaseCurrentA     float32   A
    4-5  ActivePowerKw     float32   kW
    6-7  EnergyKwh         float32   kWh (accumulating)
    8    AlarmWord         uint16    bit 0 undervoltage, bit 1 overcurrent, bit 4 comms
    9    MeterStatus       uint16    0 off, 1 running, 2 alarm
Input registers (FC4):
    0    CabinetTempC      int16     x0.1 degC
    1    FrequencyHz       uint16    x0.01 Hz
Addresses outside these ranges return exception 02 (IllegalDataAddress).
"""
import logging
import math
import os
import random
import signal
import socketserver
import struct
import threading
import time

LOG_LEVEL = os.getenv("LOG_LEVEL", "INFO").upper()
HOST = os.getenv("MODBUS_HOST", "0.0.0.0")
PORT = int(os.getenv("MODBUS_PORT", "5020"))
UNIT_ID = int(os.getenv("MODBUS_UNIT_ID", "1"))
SIM_INTERVAL_SECONDS = float(os.getenv("SIM_INTERVAL_SECONDS", "1.0"))

logging.basicConfig(level=LOG_LEVEL, format="%(asctime)s %(levelname)s [modbus-sim] %(message)s")
logger = logging.getLogger(__name__)

HOLDING = [0] * 10
INPUT = [0] * 2
LOCK = threading.Lock()
RUNNING = True


def f32(value: float):
    hi, lo = struct.unpack(">HH", struct.pack(">f", value))
    return hi, lo


class Simulation:
    def __init__(self) -> None:
        self.tick = 0
        self.energy_kwh = 12500.0 + random.random() * 400.0

    def step(self) -> None:
        self.tick += 1
        wave = math.sin(self.tick / 18.0)
        voltage = 400.0 + math.sin(self.tick / 11.0) * 5.0 + random.uniform(-1.0, 1.0)
        power = 82.0 + wave * 14.0 + random.uniform(-2.0, 2.0)
        current = power * 1000.0 / (math.sqrt(3.0) * voltage)
        self.energy_kwh += power * (SIM_INTERVAL_SECONDS / 3600.0)
        cabinet = 31.0 + wave * 2.0 + random.uniform(-0.3, 0.3)
        freq = 50.0 + random.uniform(-0.02, 0.02)
        alarm = 0
        if voltage < 392.0:
            alarm |= 1
        if current > 140.0:
            alarm |= 2
        if self.tick % 300 < 5:
            alarm |= 16
        with LOCK:
            HOLDING[0:2] = f32(voltage)
            HOLDING[2:4] = f32(current)
            HOLDING[4:6] = f32(power)
            HOLDING[6:8] = f32(self.energy_kwh)
            HOLDING[8] = alarm
            HOLDING[9] = 2 if alarm else 1
            INPUT[0] = int(round(cabinet * 10)) & 0xFFFF
            INPUT[1] = int(round(freq * 100)) & 0xFFFF
        if self.tick % 10 == 0:
            logger.info("U=%.1f V I=%.1f A P=%.1f kW E=%.3f kWh alarm=%d", voltage, current, power, self.energy_kwh, alarm)


class Handler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        self.request.settimeout(30)
        try:
            while RUNNING:
                header = self._recv(7)
                if not header:
                    return
                tid, pid, length, uid = struct.unpack(">HHHB", header)
                pdu = self._recv(length - 1)
                if not pdu:
                    return
                response = self._respond(uid, pdu)
                self.request.sendall(struct.pack(">HHHB", tid, pid, len(response) + 1, uid) + response)
        except (ConnectionError, OSError):
            return

    def _recv(self, n: int) -> bytes:
        buf = b""
        while len(buf) < n:
            try:
                chunk = self.request.recv(n - len(buf))
            except OSError:
                return b""
            if not chunk:
                return b""
            buf += chunk
        return buf

    def _respond(self, uid: int, pdu: bytes) -> bytes:
        fc = pdu[0]
        if fc not in (3, 4):
            return bytes([fc | 0x80, 0x01])
        if len(pdu) < 5:
            return bytes([fc | 0x80, 0x03])
        address, count = struct.unpack(">HH", pdu[1:5])
        table = HOLDING if fc == 3 else INPUT
        if count < 1 or count > 125 or address + count > len(table):
            return bytes([fc | 0x80, 0x02])
        with LOCK:
            regs = table[address:address + count]
        return bytes([fc, count * 2]) + struct.pack(">%dH" % count, *regs)


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def main() -> None:
    global RUNNING
    sim = Simulation()
    sim.step()
    server = Server((HOST, PORT), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    logger.info("listening on %s:%d unit_id=%d", HOST, PORT, UNIT_ID)

    def stop(*_):
        global RUNNING
        RUNNING = False

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    while RUNNING:
        time.sleep(SIM_INTERVAL_SECONDS)
        sim.step()
    server.shutdown()
    server.server_close()


if __name__ == "__main__":
    main()
