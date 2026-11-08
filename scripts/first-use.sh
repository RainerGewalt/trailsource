#!/usr/bin/env bash
# First-use check, level L5 in docs/TESTING.md.
#
# Walks through README.md and docs/CONNECT_YOUR_SERVER.md the way a new user
# would and stops at the first step that does not behave as documented:
#
#   make first-use                  the committed HEAD, from a fresh clone
#   make first-use RELEASE=0.1.1    the published release: tag v0.1.1 from
#                                   GitHub, go install …@v0.1.1, the release
#                                   archive with checksums.txt, GHCR image 0.1.1
#
# It needs what the README asks of a user: git, Go 1.24 or newer, Mosquitto
# (broker and mosquitto_sub), Python 3.9 or newer, Docker with Compose v2, and
# free ports: 18830, 48400 and 48401 (scenarios), 18831 and 48410 (your own
# server, played by the bundled simulator), 1883, 4840, 5020 and 8080 (demo
# stack). If the independent verifier trail-evidence is on PATH, the journals
# are also checked with it (L3); otherwise that step says it was skipped.
#
# FIRST_USE_KEEP=1 keeps the working directory for inspection.
set -euo pipefail

RELEASE="${RELEASE:-}"
REPO_URL="https://github.com/RainerGewalt/trailsource.git"
IMAGE="ghcr.io/rainergewalt/trailsource"
MODULE="github.com/RainerGewalt/trailsource/cmd/trailsource"
# Safety net for each wait, in seconds. A wait ends on what it waits for; the
# bound only stops a hang and is never the assertion.
WAIT="${FIRST_USE_WAIT:-120}"
OWN_MQTT=18831
OWN_OPC=48410

here="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/trailsource-first-use.XXXXXX")"
clone="$work/trailsource"
pids=()
demo_up=0
steps=0

# The demo stack uses fixed container names; the project name only keeps its
# network and volumes apart from a demo the user already runs.
export COMPOSE_PROJECT_NAME="trailsource-first-use"

step() { steps=$((steps + 1)); printf '\n==> %s\n' "$*"; }
ok() { printf '    ok: %s\n' "$*"; }

diagnostics() {
	for f in "$work"/logs/*.log; do
		[[ -s $f ]] || continue
		echo "--- $(basename "$f"):" >&2
		tail -n 15 "$f" >&2
	done
	if ((demo_up)); then
		echo "--- docker compose logs opcua-adapter:" >&2
		(cd "$clone" && docker compose logs --tail 20 opcua-adapter 2>/dev/null) >&2 || true
	fi
}

fail() {
	printf '\nFIRST-USE FAILED at step %d: %s\n' "$steps" "$*" >&2
	diagnostics
	exit 1
}

stop_all() {
	local pid
	for pid in "${pids[@]}"; do
		kill "$pid" 2>/dev/null || true
	done
	for pid in "${pids[@]}"; do
		wait "$pid" 2>/dev/null || true
	done
	pids=()
}

cleanup() {
	stop_all
	if ((demo_up)); then
		(cd "$clone" && docker compose down -v --remove-orphans >/dev/null 2>&1) || true
	fi
	if [[ ${FIRST_USE_KEEP:-0} == 1 ]]; then
		echo "kept: $work"
	else
		# The demo stack makes .demo/state and .demo/journal writable for its
		# uid 10001, which then owns the files in them; they are removable
		# because the directories are 0777.
		rm -rf "$work" 2>/dev/null || echo "note: could not remove all of $work"
	fi
}
trap cleanup EXIT

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is not installed: $2"; }
port_in_use() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

# wait_for FILE PATTERN WHAT: wait until FILE has a line matching PATTERN.
wait_for() {
	local file=$1 pattern=$2 what=$3 i
	for ((i = 0; i < WAIT * 5; i++)); do
		grep -Eq -- "$pattern" "$file" 2>/dev/null && return 0
		sleep 0.2
	done
	fail "no $what within ${WAIT}s"
}

wait_port() {
	local i
	for ((i = 0; i < WAIT * 5; i++)); do
		port_in_use "$1" && return 0
		sleep 0.2
	done
	fail "$2 did not open port $1 within ${WAIT}s"
}

# run_cli EXPECTED_EXIT GREP_PATTERN ARGS...: run a command, check its exit
# code and that its output matches the pattern.
run_cli() {
	local want=$1 pattern=$2 out code
	shift 2
	if out="$("$@" 2>&1)"; then code=0; else code=$?; fi
	[[ $code == "$want" ]] || fail "$* exited $code, want $want; output: $(tail -n 5 <<<"$out")"
	grep -Eq -- "$pattern" <<<"$out" || fail "$* did not print /$pattern/; output: $(tail -n 5 <<<"$out")"
	ok "$(grep -Em1 -- "$pattern" <<<"$out")  [exit $code: ${*//$work/<work>}]"
}

count() { local n; n=$(grep -c -- "$1" "$2" 2>/dev/null) || true; echo "${n:-0}"; }

# ---------------------------------------------------------------------------
step "Prerequisites from the README"
need git "it is used to get the code"
need go "README, Install: Go 1.24 or newer"
need mosquitto "the scenarios run a local Mosquitto (docs/SCENARIOS.md)"
need mosquitto_sub "the Quick start watches the topics with it"
need docker "the demo stack runs on Docker Compose"
docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 (docker compose) is missing"
gov="$(go env GOVERSION)"
gomin="${gov#go1.}"
gomin="${gomin%%.*}"
[[ $gomin =~ ^[0-9]+$ && $gomin -ge 24 ]] || fail "Go $gov is older than the documented 1.24"
for port in 18830 48400 48401 $OWN_MQTT $OWN_OPC 1883 4840 5020 8080; do
	port_in_use "$port" && fail "port $port is in use (scenarios: 18830, 48400, 48401; own server: $OWN_MQTT, $OWN_OPC; demo: 1883, 4840, 5020, 8080)"
done
for name in trailsource_mosquitto trailsource_opcua_sim trailsource_opcua_adapter; do
	[[ -z "$(docker ps -aq --filter "name=^${name}$")" ]] || fail "a container named $name exists; stop your own demo first (make demo-down)"
done
mkdir -p "$work/logs"
ok "git, $gov, mosquitto, docker compose; ports free"

# ---------------------------------------------------------------------------
step "Get the code"
if [[ -z $RELEASE ]]; then
	rev="$(git -C "$here" rev-parse HEAD)"
	if [[ -n "$(git -C "$here" status --porcelain)" ]]; then
		echo "    note: uncommitted changes are not part of this check; it runs the committed HEAD ${rev:0:12}"
	fi
	git clone --quiet "$here" "$clone"
	git -C "$clone" checkout --quiet "$rev"
	ok "fresh clone of $(git -C "$clone" describe --tags --always)"
else
	git clone --quiet --depth 1 --branch "v$RELEASE" "$REPO_URL" "$clone" 2>"$work/logs/clone.log" ||
		fail "tag v$RELEASE not found on $REPO_URL"
	ok "tag v$RELEASE from GitHub"
fi
cd "$clone"

# ---------------------------------------------------------------------------
step "Install (README, Install)"
make build >"$work/logs/build.log" 2>&1 || fail "make build"
ts="$clone/bin/trailsource"
ok "make build: $("$ts" version)"
if [[ -n $RELEASE ]]; then
	GOBIN="$work/bin" go install "$MODULE@v$RELEASE" 2>"$work/logs/install.log" || fail "go install $MODULE@v$RELEASE"
	[[ "$("$work/bin/trailsource" version)" == "$RELEASE" ]] ||
		fail "go install binary reports '$("$work/bin/trailsource" version)', want $RELEASE"
	ok "go install …@v$RELEASE reports $RELEASE"
	arch="$(go env GOARCH)"
	archive="trailsource_${RELEASE}_linux_${arch}.tar.gz"
	base="https://github.com/RainerGewalt/trailsource/releases/download/v$RELEASE"
	mkdir -p "$work/archive"
	(cd "$work/archive" &&
		curl -fsSLO "$base/$archive" && curl -fsSLO "$base/checksums.txt" &&
		sha256sum --ignore-missing -c checksums.txt >/dev/null &&
		tar -xzf "$archive" trailsource) || fail "release archive $archive with checksums.txt"
	[[ "$("$work/archive/trailsource" version)" == "$RELEASE" ]] || fail "release archive binary reports the wrong version"
	ok "$archive: checksum matches, reports $RELEASE"
	docker pull --quiet "$IMAGE:$RELEASE" >/dev/null || fail "docker pull $IMAGE:$RELEASE"
	iv="$(docker run --rm "$IMAGE:$RELEASE" version)"
	[[ $iv == "$RELEASE" ]] || fail "image $IMAGE:$RELEASE reports '$iv', want $RELEASE"
	ok "image $IMAGE:$RELEASE reports $RELEASE"
	ts="$work/bin/trailsource"
fi

# ---------------------------------------------------------------------------
step "Simulator environment (make sim-venv)"
make sim-venv >"$work/logs/sim-venv.log" 2>&1 || fail "make sim-venv: $(tail -n 3 "$work/logs/sim-venv.log")"
simpy="$clone/.demo/sim-venv/bin/python"
ok "asyncua in .demo/sim-venv ($("$simpy" --version 2>&1))"

step "Quick start: all seven scenarios (make scenarios)"
make scenarios >"$work/logs/scenarios.log" 2>&1 || fail "make scenarios: $(grep -E '^scenario ' "$work/logs/scenarios.log" | tail -n 3)"
sdir=".demo/scenarios"
j() { echo "$sdir/$1/journal/plant-a-ext-01-extruder-01-opcua.jsonl"; }
[[ "$(count '"reason_code":"source.node_added"' "$(j inventory-change)")" -ge 1 ]] ||
	fail "inventory-change: no source.node_added record"
[[ "$(count '"reason_code":"source.connection_lost"' "$(j gap)")" -ge 1 && "$(count '"reason_code":"source.connection_restored"' "$(j gap)")" -ge 1 ]] ||
	fail "gap: connection_lost and connection_restored records missing"
[[ "$(count '"reason_code":"common.restart"' "$(j restart)")" -ge 2 ]] ||
	fail "restart: fewer than two common.restart records"
[[ "$(count '"reason_code":"source.subscription_stalled"' "$(j silent-subscription)")" -ge 1 ]] ||
	fail "silent-subscription: no source.subscription_stalled record"
ok "seven scenarios passed; node_added, lost/restored, restart, subscription_stalled recorded"

step "Report (README, Report)"
html="$sdir/server-restart/report.html"
[[ -s $html ]] || fail "no report for server-restart"
grep -Eqi '<script|src="https?:|href="https?:|@import' "$html" &&
	fail "the HTML report loads scripts or external resources"
grep -q 'generation' "$html" || fail "the HTML report names no address-space generation"
"$ts" report "$(j server-restart)" --json | "$simpy" -c 'import json,sys; json.load(sys.stdin)' ||
	fail "report --json is not JSON"
ok "self-contained HTML without scripts or external resources; --json parses"

# ---------------------------------------------------------------------------
step "Your own server (docs/CONNECT_YOUR_SERVER.md), played by the bundled simulator"
own="$work/own"
mkdir -p "$own"
printf 'listener %s 127.0.0.1\nallow_anonymous true\npersistence false\n' "$OWN_MQTT" >"$own/mosquitto.conf"
mosquitto -c "$own/mosquitto.conf" >"$work/logs/own-mosquitto.log" 2>&1 &
pids+=($!)
OPC_ENDPOINT="opc.tcp://127.0.0.1:$OWN_OPC/trailsource" SIM_ADD_NODE_AFTER_S=0 \
	"$simpy" sim/opcua/main.py >"$work/logs/own-sim.log" 2>&1 &
pids+=($!)
wait_port "$OWN_MQTT" mosquitto
wait_port "$OWN_OPC" simulator
endpoint="opc.tcp://127.0.0.1:$OWN_OPC/trailsource"

# Step 1: ask the server what it offers.
run_cli 0 '^None +None .*anonymous' "$ts" endpoints "$endpoint"

# Step 5: copy the example. Left unchanged apart from the endpoint, run stops
# at the mapping file, as the guide says.
cfg="$own/my-server.yaml"
sed "s#endpoint: opc.tcp://opcua-sim:4840/trailsource#endpoint: $endpoint#" configs/opcua-extruder.yaml >"$cfg"
grep -q "endpoint: $endpoint" "$cfg" || fail "could not set the endpoint in a copy of configs/opcua-extruder.yaml"
run_cli 1 'load mapping: open /config/mapping/extruder.yaml: no such file or directory' "$ts" run --config "$cfg"

# The four fields of the table in step 5.
sed -i \
	-e 's#^state_dir: /state#state_dir: state#' \
	-e 's#^  path: /journal/plant-a-ext-01-extruder-01-opcua.jsonl#  path: journal/plant-a-ext-01-extruder-01-opcua.jsonl#' \
	-e '/^mapping_file: /d' \
	-e "s#broker: tcp://mosquitto:1883#broker: tcp://localhost:$OWN_MQTT#" "$cfg"
for want in '^state_dir: state$' '^  path: journal/' "broker: tcp://localhost:$OWN_MQTT"; do
	grep -Eq "$want" "$cfg" || fail "step 5 field not found in the example: $want"
done
grep -q '^mapping_file:' "$cfg" && fail "mapping_file still set"
cd "$own"
run_cli 0 '^fingerprint +sha256:' "$ts" browse --config "$cfg"
mkdir -p configs/mapping
run_cli 0 '' "$ts" browse --config "$cfg" --mapping-out configs/mapping/my-server.yaml
grep -q '^rules:' configs/mapping/my-server.yaml || fail "the mapping skeleton has no rules"
printf 'mapping_file: configs/mapping/my-server.yaml\n' >>"$cfg"
run_cli 0 'sha256:' "$ts" config-version --config "$cfg"

# Step 7: run, check the retained health, verify, report.
mosquitto_sub -h localhost -p "$OWN_MQTT" -v -t 'trailsource/+/+/+/health' >"$work/logs/own-health.log" 2>&1 &
pids+=($!)
"$ts" run --config "$cfg" >"$work/logs/own-adapter.log" 2>&1 &
adapter=$!
wait_for "$work/logs/own-health.log" '"last_event":"source\.(connection_established|inventory_initial)"' "health message with last_event connection_established or inventory_initial"
journal="journal/plant-a-ext-01-extruder-01-opcua.jsonl"
for ((i = 0; i < WAIT * 5; i++)); do
	[[ "$(count '"source.value.observed"' "$journal")" -ge 20 ]] && break
	sleep 0.2
done
[[ "$(count '"source.value.observed"' "$journal")" -ge 20 ]] || fail "fewer than 20 value records within ${WAIT}s"
kill "$adapter"
wait "$adapter" 2>/dev/null || true
ok "retained health with last_event, $(count '"source.value.observed"' "$journal") value records, orderly stop"
run_cli 0 ': OK, [0-9]+ records' "$ts" verify "$journal"
"$ts" report "$journal" --html "$own/report.html" >/dev/null || fail "report --html"
ok "report $own/report.html"
# README, "What has changed since it was recorded?": one value edited in a copy.
"$simpy" "$clone/scripts/tamper.py" "$journal" "$own/edited.jsonl" >"$work/logs/own-tamper.log" 2>&1 ||
	fail "scripts/tamper.py: $(tail -n 3 "$work/logs/own-tamper.log")"
cmp -s "$journal" "$own/edited.jsonl" && fail "scripts/tamper.py left the copy unchanged"
run_cli 1 'hash_mismatch' "$ts" verify "$own/edited.jsonl"
stop_all
cd "$clone"

# ---------------------------------------------------------------------------
step "Demo stack with Docker (README, Quick start)"
if [[ -n $RELEASE ]]; then
	cat >"$work/release.yaml" <<EOF
services:
  opcua-adapter:
    image: $IMAGE:$RELEASE
EOF
	export COMPOSE_FILE="docker-compose.yaml:$work/release.yaml"
	export TRAILSOURCE_VERSION="$RELEASE"
fi
demo_up=1
if [[ -n $RELEASE ]]; then
	mkdir -p .demo/state .demo/journal
	docker compose up -d >"$work/logs/demo.log" 2>&1 || fail "docker compose up with $IMAGE:$RELEASE"
else
	make demo >"$work/logs/demo.log" 2>&1 || fail "make demo: $(tail -n 5 "$work/logs/demo.log")"
fi
wait_port 1883 "demo broker"
mosquitto_sub -h localhost -t 'trailsource/#' -v >"$work/logs/demo-topics.log" 2>&1 &
pids+=($!)
wait_for "$work/logs/demo-topics.log" '^trailsource/plant-a/ext-01/extruder-01/values ' "value record from the demo OPC UA adapter"
docker compose stop opcua-adapter modbus-adapter ethernetip-adapter >/dev/null 2>&1 || fail "stopping the demo adapters"
run_cli 0 'opcua\.jsonl: OK, [0-9]+ records' make verify
make demo-down >/dev/null 2>&1 || fail "make demo-down"
demo_up=0
docker compose down -v >/dev/null 2>&1 || true
stop_all
ok "values over MQTT, make verify, make demo-down"

# ---------------------------------------------------------------------------
step "Independent verifier trail-evidence (L3)"
TE_VERSION="${TRAIL_EVIDENCE_VERSION:-v0.2.1}"
te=trail-evidence
if ! command -v trail-evidence >/dev/null 2>&1; then
	if GOBIN="$work/bin" go install "github.com/RainerGewalt/trail-evidence/go/cmd/trail-evidence@$TE_VERSION" 2>"$work/logs/trail-evidence-install.log"; then
		te="$work/bin/trail-evidence"
		ok "trail-evidence $TE_VERSION installed into $work/bin for this run"
	else
		te=""
		echo "    skipped: trail-evidence is not on PATH and go install …@$TE_VERSION failed (see $work/logs/trail-evidence-install.log)"
	fi
fi
if [[ -n $te ]]; then
	for s in inventory-change gap restart server-restart half-open silent-subscription; do
		run_cli 0 '^VALID' "$te" verify "$(j "$s")"
	done
	run_cli 0 '^VALID' "$te" verify "$own/journal/plant-a-ext-01-extruder-01-opcua.jsonl"
	run_cli 1 '^INVALID' "$te" verify "$own/edited.jsonl"
fi

if [[ -n $RELEASE ]]; then
	printf '\nFIRST-USE PASSED: %d steps, published release %s\n' "$steps" "$RELEASE"
else
	printf '\nFIRST-USE PASSED: %d steps, committed HEAD from a fresh clone\n' "$steps"
fi
