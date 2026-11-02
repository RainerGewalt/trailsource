"""OPC UA simulator: one extrusion line (asyncua).

Address space (namespace urn:trailsource:sim:opcua, string NodeIds equal to
the browse path):

    Objects
    └── Plant
        └── Line01
            └── Extruder01
                ├── MachineState, RecipeId, OrderId, BatchId
                ├── ScrewSpeedRpm, ScrewTorquePct, MeltPressureBar, MeltTempC
                ├── BarrelZone01..06 / TempActualC, TempSetpointC
                ├── BarrelZone01 / TempActualRaw      (Int16, 1 decimal, as a PLC exports it)
                ├── HaulOff / SpeedMPerMin, TorquePct
                ├── Saw / PieceCounter, CutLengthMm
                ├── Alarms / AlarmWord1, AlarmWord2   (UInt32 bit words)
                └── SourceTimestamp

After SIM_ADD_NODE_AFTER_S seconds a seventh barrel zone is added so the
adapter's address-space diff has something to report.
"""
import asyncio
import logging
import math
import os
import random
import signal
from datetime import datetime, timezone
from typing import Any, Dict

from asyncua import Server, ua

LOG_LEVEL = os.getenv("LOG_LEVEL", "INFO").upper()
OPC_ENDPOINT = os.getenv("OPC_ENDPOINT", "opc.tcp://0.0.0.0:4840/trailsource")
OPC_SERVER_NAME = os.getenv("OPC_SERVER_NAME", "TrailSource Extrusion Line Simulator")
OPC_NAMESPACE_URI = os.getenv("OPC_NAMESPACE_URI", "urn:trailsource:sim:opcua")
SIM_INTERVAL_SECONDS = float(os.getenv("SIM_INTERVAL_SECONDS", "1.0"))
SIM_ADD_NODE_AFTER_S = float(os.getenv("SIM_ADD_NODE_AFTER_S", "120"))
LINE_ID = os.getenv("LINE_ID", "Line01")
MACHINE_ID = os.getenv("MACHINE_ID", "Extruder01")
ZONES = int(os.getenv("SIM_BARREL_ZONES", "6"))

logging.basicConfig(level=LOG_LEVEL, format="%(asctime)s %(levelname)s [opcua-sim] %(message)s")
logger = logging.getLogger(__name__)

ZONE_SETPOINTS = [170.0, 185.0, 195.0, 205.0, 210.0, 210.0, 212.0, 212.0]
RECIPES = ["PE-HD-110x6.6", "PP-R-63x10.5", "PE-HD-160x9.5"]

# Alarm word 1 bits (see docs/RECORD_MAPPING.md): bit 0 zone overtemp,
# bit 1 thermocouple break, bit 5 melt pressure pre-warning, bit 6 melt pressure
# shutdown, bit 9 drive overload pre-warning, bit 10 drive overload shutdown.
ALARM_OVERTEMP, ALARM_TC_BREAK, ALARM_MP_WARN, ALARM_MP_TRIP, ALARM_DRIVE_WARN, ALARM_DRIVE_TRIP = 1, 2, 32, 64, 512, 1024


def clamp(v: float, lo: float, hi: float) -> float:
    return max(lo, min(v, hi))


class ExtrusionSimulation:
    """STOPPED -> HEATING -> STARTUP -> PRODUCING -> (ALARM -> PRODUCING | STOPPED)."""

    def __init__(self) -> None:
        self.tick = 0
        self.phase = "STOPPED"
        self.phase_tick = 0
        self.zone_temp = [25.0] * ZONES
        self.zone_set = ZONE_SETPOINTS[:ZONES]
        self.screw_rpm = 0.0
        self.torque = 0.0
        self.melt_pressure = 0.0
        self.melt_temp = 25.0
        self.hauloff = 0.0
        self.hauloff_torque = 0.0
        self.pieces = 0
        self.cut_length_mm = 6000.0
        self.alarm1 = 0
        self.alarm2 = 0
        self.recipe = RECIPES[0]
        self.order = "ORD-2026-000112"
        self.batch = "B-2026-11-02-01"
        self._length_acc = 0.0
        self._alarm_until = 0

    def _transition(self, phase: str) -> None:
        logger.info("phase %s -> %s", self.phase, phase)
        self.phase, self.phase_tick = phase, 0

    def step(self) -> None:
        self.tick += 1
        self.phase_tick += 1
        wave = math.sin(self.tick / 17.0)

        if self.phase == "STOPPED":
            self.screw_rpm, self.torque, self.melt_pressure, self.hauloff = 0.0, 0.0, 0.0, 0.0
            for i in range(ZONES):
                self.zone_temp[i] = clamp(self.zone_temp[i] - 0.4 + random.uniform(-0.1, 0.1), 22.0, 300.0)
            if self.phase_tick >= 10:
                self._transition("HEATING")
        elif self.phase == "HEATING":
            done = True
            for i in range(ZONES):
                self.zone_temp[i] = clamp(self.zone_temp[i] + 2.5 + random.uniform(-0.3, 0.3), 20.0, self.zone_set[i] + 1.0)
                if self.zone_temp[i] < self.zone_set[i] - 2.0:
                    done = False
            self.melt_temp = clamp(self.melt_temp + 2.0, 20.0, 215.0)
            if done:
                self._transition("STARTUP")
        elif self.phase == "STARTUP":
            f = min(self.phase_tick / 30.0, 1.0)
            self.screw_rpm = round(85.0 * f + random.uniform(-0.5, 0.5), 1)
            self.torque = round(clamp(62.0 * f + random.uniform(-1.0, 1.0), 0.0, 100.0), 1)
            self.melt_pressure = round(clamp(190.0 * f + random.uniform(-2.0, 2.0), 0.0, 400.0), 1)
            self.hauloff = round(2.4 * f, 3)
            self._hold_zones()
            if f >= 1.0:
                self._transition("PRODUCING")
        elif self.phase == "PRODUCING":
            self.screw_rpm = round(clamp(85.0 + wave * 1.5 + random.uniform(-0.3, 0.3), 0.0, 200.0), 1)
            self.torque = round(clamp(62.0 + wave * 4.0 + random.uniform(-0.8, 0.8), 0.0, 100.0), 1)
            self.melt_pressure = round(clamp(190.0 + wave * 6.0 + random.uniform(-1.5, 1.5), 0.0, 400.0), 1)
            self.hauloff = round(clamp(2.4 + wave * 0.02 + random.uniform(-0.005, 0.005), 0.0, 10.0), 3)
            self.hauloff_torque = round(clamp(38.0 + wave * 2.0 + random.uniform(-0.5, 0.5), 0.0, 100.0), 1)
            self._hold_zones()
            self._length_acc += self.hauloff * 1000.0 / 60.0 * SIM_INTERVAL_SECONDS
            while self._length_acc >= self.cut_length_mm:
                self._length_acc -= self.cut_length_mm
                self.pieces += 1
            if self.tick % 400 == 0:
                self.recipe = RECIPES[(RECIPES.index(self.recipe) + 1) % len(RECIPES)]
                self.batch = "B-2026-11-02-%02d" % (int(self.batch[-2:]) + 1)
            if self.tick > 60 and self.tick % 180 == 0:
                self.alarm1 |= random.choice([ALARM_MP_WARN, ALARM_DRIVE_WARN, ALARM_OVERTEMP])
                self._alarm_until = self.tick + 15
                self._transition("ALARM")
        elif self.phase == "ALARM":
            self.screw_rpm = round(clamp(self.screw_rpm * 0.97, 0.0, 200.0), 1)
            self.torque = round(clamp(self.torque * 0.97, 0.0, 100.0), 1)
            self.melt_pressure = round(clamp(self.melt_pressure + random.uniform(-3.0, 1.0), 0.0, 400.0), 1)
            self._hold_zones()
            if self.tick >= self._alarm_until:
                self.alarm1 = 0
                self._transition("STARTUP")

        self.melt_temp = round(clamp(self.zone_temp[-1] + 3.0 + wave * 0.8 + random.uniform(-0.3, 0.3), 20.0, 300.0), 1)

    def _hold_zones(self) -> None:
        for i in range(ZONES):
            self.zone_temp[i] = round(clamp(self.zone_set[i] + random.uniform(-0.8, 0.8), 20.0, 300.0), 1)


async def add_var(parent: Any, idx: int, path: str, value: Any, vtype: ua.VariantType, writable: bool = False):
    node = await parent.add_variable(ua.NodeId(path, idx), path.rsplit(".", 1)[-1], ua.Variant(value, vtype))
    if writable:
        await node.set_writable()
    return node


async def build_zone(extruder: Any, idx: int, base: str, zone: int, setpoint: float, nodes: Dict[str, Any]) -> None:
    zpath = "%s.BarrelZone%02d" % (base, zone)
    zobj = await extruder.add_object(ua.NodeId(zpath, idx), "BarrelZone%02d" % zone)
    nodes[zpath + ".TempActualC"] = await add_var(zobj, idx, zpath + ".TempActualC", 25.0, ua.VariantType.Double)
    nodes[zpath + ".TempSetpointC"] = await add_var(zobj, idx, zpath + ".TempSetpointC", setpoint, ua.VariantType.Double, writable=True)
    if zone == 1:
        nodes[zpath + ".TempActualRaw"] = await add_var(zobj, idx, zpath + ".TempActualRaw", 250, ua.VariantType.Int16)


async def build_address_space(server: Server, idx: int) -> Dict[str, Any]:
    plant = await server.nodes.objects.add_object(ua.NodeId("Plant", idx), "Plant")
    line = await plant.add_object(ua.NodeId("Plant.%s" % LINE_ID, idx), LINE_ID)
    base = "Plant.%s.%s" % (LINE_ID, MACHINE_ID)
    extruder = await line.add_object(ua.NodeId(base, idx), MACHINE_ID)
    nodes: Dict[str, Any] = {}

    for name, value, vtype, writable in [
        ("MachineState", "STOPPED", ua.VariantType.String, False),
        ("RecipeId", RECIPES[0], ua.VariantType.String, True),
        ("OrderId", "ORD-2026-000112", ua.VariantType.String, True),
        ("BatchId", "B-2026-11-02-01", ua.VariantType.String, True),
        ("ScrewSpeedRpm", 0.0, ua.VariantType.Double, False),
        ("ScrewTorquePct", 0.0, ua.VariantType.Double, False),
        ("MeltPressureBar", 0.0, ua.VariantType.Double, False),
        ("MeltTempC", 25.0, ua.VariantType.Double, False),
        ("SourceTimestamp", datetime.now(timezone.utc), ua.VariantType.DateTime, False),
    ]:
        nodes[base + "." + name] = await add_var(extruder, idx, base + "." + name, value, vtype, writable)

    for zone in range(1, ZONES + 1):
        await build_zone(extruder, idx, base, zone, ZONE_SETPOINTS[zone - 1], nodes)

    hauloff = await extruder.add_object(ua.NodeId(base + ".HaulOff", idx), "HaulOff")
    nodes[base + ".HaulOff.SpeedMPerMin"] = await add_var(hauloff, idx, base + ".HaulOff.SpeedMPerMin", 0.0, ua.VariantType.Double)
    nodes[base + ".HaulOff.TorquePct"] = await add_var(hauloff, idx, base + ".HaulOff.TorquePct", 0.0, ua.VariantType.Double)

    saw = await extruder.add_object(ua.NodeId(base + ".Saw", idx), "Saw")
    nodes[base + ".Saw.PieceCounter"] = await add_var(saw, idx, base + ".Saw.PieceCounter", 0, ua.VariantType.UInt32)
    nodes[base + ".Saw.CutLengthMm"] = await add_var(saw, idx, base + ".Saw.CutLengthMm", 6000.0, ua.VariantType.Double, writable=True)

    alarms = await extruder.add_object(ua.NodeId(base + ".Alarms", idx), "Alarms")
    nodes[base + ".Alarms.AlarmWord1"] = await add_var(alarms, idx, base + ".Alarms.AlarmWord1", 0, ua.VariantType.UInt32)
    nodes[base + ".Alarms.AlarmWord2"] = await add_var(alarms, idx, base + ".Alarms.AlarmWord2", 0, ua.VariantType.UInt32)

    nodes["_extruder"] = extruder
    nodes["_base"] = base
    return nodes


async def write(node: Any, value: Any, vtype: ua.VariantType, now: datetime) -> None:
    await node.write_value(ua.DataValue(ua.Variant(value, vtype), SourceTimestamp=now))


async def write_values(nodes: Dict[str, Any], sim: ExtrusionSimulation) -> None:
    base = nodes["_base"]
    now = datetime.now(timezone.utc)
    await write(nodes[base + ".MachineState"], sim.phase, ua.VariantType.String, now)
    await write(nodes[base + ".RecipeId"], sim.recipe, ua.VariantType.String, now)
    await write(nodes[base + ".OrderId"], sim.order, ua.VariantType.String, now)
    await write(nodes[base + ".BatchId"], sim.batch, ua.VariantType.String, now)
    await write(nodes[base + ".ScrewSpeedRpm"], sim.screw_rpm, ua.VariantType.Double, now)
    await write(nodes[base + ".ScrewTorquePct"], sim.torque, ua.VariantType.Double, now)
    await write(nodes[base + ".MeltPressureBar"], sim.melt_pressure, ua.VariantType.Double, now)
    await write(nodes[base + ".MeltTempC"], sim.melt_temp, ua.VariantType.Double, now)
    await write(nodes[base + ".SourceTimestamp"], now, ua.VariantType.DateTime, now)
    for zone in range(1, len(sim.zone_temp) + 1):
        zpath = "%s.BarrelZone%02d" % (base, zone)
        if zpath + ".TempActualC" in nodes:
            await write(nodes[zpath + ".TempActualC"], sim.zone_temp[zone - 1], ua.VariantType.Double, now)
            await write(nodes[zpath + ".TempSetpointC"], sim.zone_set[zone - 1], ua.VariantType.Double, now)
        if zpath + ".TempActualRaw" in nodes:
            await write(nodes[zpath + ".TempActualRaw"], int(round(sim.zone_temp[zone - 1] * 10)), ua.VariantType.Int16, now)
    await write(nodes[base + ".HaulOff.SpeedMPerMin"], sim.hauloff, ua.VariantType.Double, now)
    await write(nodes[base + ".HaulOff.TorquePct"], sim.hauloff_torque, ua.VariantType.Double, now)
    await write(nodes[base + ".Saw.PieceCounter"], sim.pieces, ua.VariantType.UInt32, now)
    await write(nodes[base + ".Saw.CutLengthMm"], sim.cut_length_mm, ua.VariantType.Double, now)
    await write(nodes[base + ".Alarms.AlarmWord1"], sim.alarm1, ua.VariantType.UInt32, now)
    await write(nodes[base + ".Alarms.AlarmWord2"], sim.alarm2, ua.VariantType.UInt32, now)


async def main() -> None:
    stop_event = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        try:
            loop.add_signal_handler(sig, stop_event.set)
        except NotImplementedError:
            pass

    server = Server()
    await server.init()
    server.set_endpoint(OPC_ENDPOINT)
    server.set_server_name(OPC_SERVER_NAME)
    server.set_application_uri(OPC_NAMESPACE_URI)
    server.set_security_policy([ua.SecurityPolicyType.NoSecurity])  # lab only
    idx = await server.register_namespace(OPC_NAMESPACE_URI)
    nodes = await build_address_space(server, idx)
    sim = ExtrusionSimulation()

    logger.info("endpoint=%s namespace_index=%s example=ns=%s;s=%s.MeltPressureBar", OPC_ENDPOINT, idx, idx, nodes["_base"])
    started = loop.time()
    added_zone = False
    async with server:
        while not stop_event.is_set():
            sim.step()
            await write_values(nodes, sim)
            if not added_zone and SIM_ADD_NODE_AFTER_S > 0 and loop.time() - started >= SIM_ADD_NODE_AFTER_S:
                zone = ZONES + 1
                sim.zone_temp.append(25.0)
                sim.zone_set.append(ZONE_SETPOINTS[min(zone - 1, len(ZONE_SETPOINTS) - 1)])
                await build_zone(nodes["_extruder"], idx, nodes["_base"], zone, sim.zone_set[-1], nodes)
                added_zone = True
                logger.info("address space changed: added BarrelZone%02d", zone)
            if sim.tick % 10 == 0:
                logger.info("phase=%s rpm=%.1f melt_p=%.1f bar zone1=%.1f C pieces=%d alarm1=%d",
                            sim.phase, sim.screw_rpm, sim.melt_pressure, sim.zone_temp[0], sim.pieces, sim.alarm1)
            try:
                await asyncio.wait_for(stop_event.wait(), timeout=SIM_INTERVAL_SECONDS)
            except asyncio.TimeoutError:
                pass


if __name__ == "__main__":
    asyncio.run(main())
