#!/usr/bin/env bash
# Local scenario runner: OPC UA simulator, Mosquitto and the adapter as plain
# processes, no Docker. See docs/SCENARIOS.md.
#
#   scripts/scenario.sh inventory-change | gap | restart | tamper
#                       server-restart | half-open
#
# Needs: ./bin/trailsource (make build), mosquitto on PATH, a Python with
# asyncua for the simulator (pip install -r sim/opcua/requirements.txt).
# Everything is written below .demo/scenarios/<name>/, which is git-ignored.
#
# Environment:
#   SCENARIO_MQTT_PORT   local Mosquitto port            (default 18830)
#   SCENARIO_OPC_PORT    port the adapter connects to    (default 48400; the
#                        simulator's, or the relay's in half-open)
#   SCENARIO_GAP_S       seconds the simulator stays off (default 8)
#   SIM_PYTHON           Python interpreter with asyncua (auto-detected)
#   SCENARIO_BROKER      "external" to use a broker already on SCENARIO_MQTT_PORT
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT=$PWD
NAME=${1:-}
case "$NAME" in
  inventory-change|gap|restart|tamper|server-restart|half-open) ;;
  *) echo "usage: $0 inventory-change|gap|restart|tamper|server-restart|half-open" >&2; exit 2 ;;
esac

export SCENARIO_MQTT_PORT=${SCENARIO_MQTT_PORT:-18830}
export SCENARIO_OPC_PORT=${SCENARIO_OPC_PORT:-48400}
export SCENARIO_DIR=$ROOT/.demo/scenarios/$NAME
export SCENARIO_REBROWSE_S=${SCENARIO_REBROWSE_S:-30}
export SCENARIO_BACKOFF_S=${SCENARIO_BACKOFF_S:-2}
export SCENARIO_LIVENESS_S=${SCENARIO_LIVENESS_S:-10}
SIM_PORT=$SCENARIO_OPC_PORT
[ "$NAME" = half-open ] && SIM_PORT=$((SCENARIO_OPC_PORT + 1))
EXPECT_DIFF=
GAP_S=${SCENARIO_GAP_S:-8}
BIN=$ROOT/bin/trailsource
CONFIG=configs/scenario/opcua-local.yaml
JOURNAL=$SCENARIO_DIR/journal/plant-a-ext-01-extruder-01-opcua.jsonl

say() { printf '\n== %s\n' "$*"; }
die() { printf 'scenario %s: %s\n' "$NAME" "$*" >&2; exit 1; }

# -- prerequisites -------------------------------------------------------------
[ -x "$BIN" ] || die "bin/trailsource missing (run make build)"
if [ "${SCENARIO_BROKER:-}" != "external" ]; then
  command -v mosquitto >/dev/null || die "mosquitto not found; install it or run a broker on port $SCENARIO_MQTT_PORT and set SCENARIO_BROKER=external (docs/SCENARIOS.md)"
fi
if [ -z "${SIM_PYTHON:-}" ]; then
  for candidate in python3 /usr/bin/python3 python; do
    if command -v "$candidate" >/dev/null && "$candidate" -c 'import asyncua' >/dev/null 2>&1; then
      SIM_PYTHON=$candidate; break
    fi
  done
fi
[ -n "${SIM_PYTHON:-}" ] || die "no Python with asyncua found; pip install -r sim/opcua/requirements.txt or set SIM_PYTHON"
port_busy() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }
if [ "${SCENARIO_BROKER:-}" != "external" ] && port_busy "$SCENARIO_MQTT_PORT"; then die "port $SCENARIO_MQTT_PORT is in use (set SCENARIO_MQTT_PORT)"; fi
for p in "$SCENARIO_OPC_PORT" "$SIM_PORT"; do
  if port_busy "$p"; then die "port $p is in use (set SCENARIO_OPC_PORT)"; fi
done

rm -rf "$SCENARIO_DIR"
mkdir -p "$SCENARIO_DIR/state" "$SCENARIO_DIR/journal" "$SCENARIO_DIR/logs"

# -- process control -----------------------------------------------------------
BROKER_PID= SIM_PID= ADAPTER_PID=
stop_pid() {
  local pid=$1
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null || return 0
  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 50); do kill -0 "$pid" 2>/dev/null || return 0; sleep 0.2; done
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
RELAY_PID=
cleanup() { stop_pid "$ADAPTER_PID"; stop_pid "$RELAY_PID"; stop_pid "$SIM_PID"; stop_pid "$BROKER_PID"; }
trap cleanup EXIT

start_broker() {
  [ "${SCENARIO_BROKER:-}" = "external" ] && return 0
  printf 'listener %s 127.0.0.1\nallow_anonymous true\npersistence false\nlog_dest stdout\nlog_type error\nlog_type warning\n' \
    "$SCENARIO_MQTT_PORT" > "$SCENARIO_DIR/mosquitto.conf"
  mosquitto -c "$SCENARIO_DIR/mosquitto.conf" >"$SCENARIO_DIR/logs/mosquitto.log" 2>&1 &
  BROKER_PID=$!
  wait_port "$SCENARIO_MQTT_PORT" mosquitto
}
start_sim() {
  # $1: seconds until BarrelZone07 is added (0 = never); further arguments
  # are VAR=value settings for the simulator (see sim/opcua/main.py)
  local after=$1; shift
  env "$@" OPC_ENDPOINT="opc.tcp://127.0.0.1:$SIM_PORT/trailsource" SIM_ADD_NODE_AFTER_S="$after" \
    "$SIM_PYTHON" sim/opcua/main.py >>"$SCENARIO_DIR/logs/sim.log" 2>&1 &
  SIM_PID=$!
  wait_port "$SIM_PORT" simulator
}
start_relay() {
  "$SIM_PYTHON" scripts/tcp_relay.py "$SCENARIO_OPC_PORT" 127.0.0.1 "$SIM_PORT" >>"$SCENARIO_DIR/logs/relay.log" 2>&1 &
  RELAY_PID=$!
  wait_port "$SCENARIO_OPC_PORT" relay
}
start_adapter() {
  "$BIN" run --config "$CONFIG" >>"$SCENARIO_DIR/logs/adapter.log" 2>&1 &
  ADAPTER_PID=$!
}
wait_port() {
  for _ in $(seq 1 100); do port_busy "$1" && return 0; sleep 0.2; done
  die "$2 did not open port $1 (see $SCENARIO_DIR/logs)"
}
count() { local n; n=$(grep -c -- "$1" "$JOURNAL" 2>/dev/null) || true; echo "${n:-0}"; }
wait_for() {
  # wait_for <fixed string> <min count> <timeout s> <description>
  local deadline=$((SECONDS + $3))
  while [ "$(count "$1")" -lt "$2" ]; do
    [ $SECONDS -lt $deadline ] || die "timed out after $3 s waiting for $4 (see $SCENARIO_DIR/logs/adapter.log)"
    sleep 0.5
  done
  echo "   seen: $4"
}
finish() {
  stop_pid "$ADAPTER_PID"; ADAPTER_PID=
  stop_pid "$SIM_PID"; SIM_PID=
  stop_pid "$RELAY_PID"; RELAY_PID=
  say "trailsource verify"
  set +e; "$BIN" verify "$JOURNAL"; local rc=$?; set -e
  say "bindings (scripts/check_bindings.py)"
  local diffarg=()
  [ -n "$EXPECT_DIFF" ] && diffarg=(--expect-diff "$EXPECT_DIFF")
  "$SIM_PYTHON" scripts/check_bindings.py "$JOURNAL" "${diffarg[@]}" || rc=1
  say "trailsource report"
  "$BIN" report "$JOURNAL" --html "$SCENARIO_DIR/report.html"
  echo
  echo "journal  $JOURNAL"
  echo "report   $SCENARIO_DIR/report.html"
  return $rc
}

start_broker
say "scenario $NAME (simulator python: $SIM_PYTHON)"

case "$NAME" in
inventory-change)
  export SCENARIO_REBROWSE_S=5
  start_sim 12
  start_adapter
  wait_for '"reason_code":"source.inventory_initial"' 1 30 "initial inventory (generation 1)"
  echo "   the simulator adds BarrelZone07 12 s after start; the adapter re-browses every 5 s"
  wait_for '"reason_code":"source.node_added"' 1 45 "source.node_added (generation 2)"
  sleep 3
  finish
  ;;
gap)
  start_sim 0
  start_adapter
  wait_for '"reason_code":"source.connection_established"' 1 30 "session established"
  wait_for '"reason_code":"source.inventory_initial"' 1 30 "initial inventory"
  sleep 5
  say "stopping the simulator for $GAP_S s"
  stop_pid "$SIM_PID"; SIM_PID=
  wait_for '"reason_code":"source.connection_lost"' 1 20 "source.connection_lost"
  sleep "$GAP_S"
  say "starting the simulator again"
  start_sim 0
  wait_for '"reason_code":"source.connection_restored"' 1 30 "source.connection_restored"
  sleep 4
  finish
  ;;
restart)
  start_sim 0
  start_adapter
  wait_for '"reason_code":"source.inventory_initial"' 1 30 "initial inventory"
  sleep 4
  say "stopping the adapter (SIGTERM) and starting it again"
  stop_pid "$ADAPTER_PID"; ADAPTER_PID=
  wait_for '"reason_code":"source.connection_closed"' 1 5 "source.connection_closed"
  sleep 3
  start_adapter
  wait_for '"chain_restart":true' 2 30 "second chain_restart record (anchor = last chain_hash before the stop)"
  sleep 4
  finish
  ;;
server-restart)
  start_sim 0
  start_adapter
  wait_for '"reason_code":"source.inventory_initial"' 1 30 "initial inventory"
  sleep 4
  say "stopping the server"
  stop_pid "$SIM_PID"; SIM_PID=
  wait_for '"reason_code":"source.connection_lost"' 1 20 "source.connection_lost"
  sleep 3
  say "starting a changed server: references reversed, namespace moved from index 2 to 3, a seventh barrel zone"
  start_sim 0 SIM_REVERSE_ORDER=1 SIM_EXTRA_NAMESPACE=1 SIM_BARREL_ZONES=7
  wait_for '"reason_code":"source.connection_restored"' 1 30 "source.connection_restored"
  wait_for '"reason_code":"source.node_added"' 1 30 "source.node_added: only the new zone, nothing for the reorder or the new index"
  sleep 4
  EXPECT_DIFF=added=2,removed=0,changed=0
  finish
  ;;
half-open)
  export SCENARIO_LIVENESS_S=2
  start_sim 0
  start_relay
  start_adapter
  wait_for '"reason_code":"source.inventory_initial"' 1 30 "initial inventory"
  sleep 4
  say "relay goes half-open: sockets stay open, nothing is forwarded"
  kill -USR1 "$RELAY_PID"
  wait_for '"reason_code":"source.connection_lost"' 1 30 "source.connection_lost (liveness read unanswered)"
  grep -q 'server not answering' "$JOURNAL" || die "the lost record does not name the unanswered liveness read"
  sleep "$GAP_S"
  say "relay forwards again"
  kill -USR2 "$RELAY_PID"
  wait_for '"reason_code":"source.connection_restored"' 1 40 "source.connection_restored"
  sleep 4
  finish
  ;;
tamper)
  start_sim 0
  start_adapter
  wait_for '"reason_code":"source.inventory_initial"' 1 30 "initial inventory"
  sleep 5
  finish || die "the untouched journal must verify"
  EDITED=$SCENARIO_DIR/journal/tampered-edit.jsonl
  RESEALED=$SCENARIO_DIR/journal/tampered-resealed.jsonl
  say "1/2 edit one value, keep its hashes (what a text editor does)"
  "$SIM_PYTHON" scripts/tamper.py "$JOURNAL" "$EDITED"
  set +e; "$BIN" verify "$EDITED"; rc1=$?; set -e
  say "2/2 edit the same value and re-seal that record (what someone who knows the format does)"
  "$SIM_PYTHON" scripts/tamper.py "$JOURNAL" "$RESEALED" --reseal
  set +e; "$BIN" verify "$RESEALED"; rc2=$?; set -e
  "$BIN" report "$RESEALED" --html "$SCENARIO_DIR/report-tampered.html" >/dev/null
  echo
  echo "edited    $EDITED (verify exit $rc1)"
  echo "resealed  $RESEALED (verify exit $rc2)"
  echo "report    $SCENARIO_DIR/report-tampered.html"
  [ $rc1 -ne 0 ] && [ $rc2 -ne 0 ] || die "a tampered journal verified"
  ;;
esac
