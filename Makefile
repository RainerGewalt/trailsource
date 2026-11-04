SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

COMPOSE ?= docker compose
PYTHON  ?= python3
# Without the tag's leading v, like the release archives and images: 0.1.0.
VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev))
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help build test py-test fmt vet ci compose-config demo demo-down logs verify clean \
	scenarios scenario-inventory-change scenario-gap scenario-restart scenario-tamper \
	scenario-server-restart scenario-half-open scenario-silent-subscription sim-venv

help:
	@printf '%s\n' 'TrailSource:' \
	  '  make build           build ./bin/trailsource' \
	  '  make test            go test ./... and Python unit tests' \
	  '  make ci              fmt check, vet, tests, python compile, compose config' \
	  '  make demo            start simulators, Mosquitto and adapters (docker compose)' \
	  '  make verify          verify the journals written by the demo adapters' \
	  '  make demo-down       stop the demo stack' \
	  '  make logs            follow demo logs' \
	  '' \
	  'Scenarios (local processes, no Docker; need Go 1.24+, mosquitto and Python 3.9+ with asyncua):' \
	  '  make sim-venv                    install the simulator requirements into .demo/sim-venv' \
	  '  make scenario-inventory-change   a node appears at runtime: source.node_added with diff' \
	  '  make scenario-gap                the server goes away and comes back: a gap with duration' \
	  '  make scenario-restart            the adapter restarts: chain_restart with anchor, a notice' \
	  '  make scenario-tamper             one value is edited: hash_mismatch, then chain_break' \
	  '  make scenario-server-restart     the server restarts reordered and renumbered: same bindings' \
	  '  make scenario-half-open          the connection stays open but goes silent: lost, then restored' \
	  '  make scenario-silent-subscription  reads answered, publish responses gone: stalled, gap declared' \
	  '  make scenarios                   all seven, one after another'

build:
	mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/trailsource ./cmd/trailsource

test: py-test
	go test ./...

py-test:
	cd adapters/pylib && $(PYTHON) -m unittest discover -s tests -t . -v

fmt:
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; echo 'gofmt: files need formatting'; exit 1; }

vet:
	go vet ./...

py-compile:
	$(PYTHON) -m py_compile adapters/modbus/main.py adapters/ethernetip/main.py \
	  adapters/pylib/trailsource_adapter/runtime.py sim/opcua/main.py sim/modbus/main.py sim/ethernetip/main.py \
	  scripts/tamper.py scripts/tcp_relay.py scripts/check_bindings.py

compose-config:
	$(COMPOSE) config >/dev/null

ci: fmt vet test py-compile compose-config

demo:
	mkdir -p .demo/state .demo/journal
	$(COMPOSE) up -d --build
	@echo 'subscribe with: mosquitto_sub -h localhost -t "trailsource/#" -v'

demo-down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100

verify: build
	@ls .demo/journal/*.jsonl >/dev/null 2>&1 || { echo 'no journals in .demo/journal (run make demo first)'; exit 1; }
	./bin/trailsource verify .demo/journal/*.jsonl

scenario-inventory-change scenario-gap scenario-restart scenario-tamper scenario-server-restart scenario-half-open scenario-silent-subscription: scenario-%: build
	scripts/scenario.sh $*

# Newest Python 3.9 or newer on PATH for the simulator venv: asyncua 1.1.8
# (sim/opcua/requirements.txt) needs 3.9. Override with SIM_VENV_PYTHON=...
PY39_CHECK := import sys; sys.exit(sys.version_info < (3, 9))
SIM_VENV_PYTHON ?= $(shell for p in python3.13 python3.12 python3.11 python3.10 python3.9 python3; do command -v $$p >/dev/null && $$p -c '$(PY39_CHECK)' 2>/dev/null && { echo $$p; break; }; done)

sim-venv:
	@test -n "$(SIM_VENV_PYTHON)" || { echo 'sim-venv: no Python 3.9 or newer on PATH (asyncua 1.1.8 needs it); install one or set SIM_VENV_PYTHON=/path/to/python3.x' >&2; exit 1; }
	@$(SIM_VENV_PYTHON) -c '$(PY39_CHECK)' 2>/dev/null || { echo 'sim-venv: $(SIM_VENV_PYTHON) is not a Python 3.9 or newer (asyncua 1.1.8 needs it); set SIM_VENV_PYTHON=/path/to/python3.x' >&2; exit 1; }
	$(SIM_VENV_PYTHON) -m venv .demo/sim-venv
	.demo/sim-venv/bin/pip install -q -r sim/opcua/requirements.txt

scenarios: scenario-inventory-change scenario-gap scenario-restart scenario-tamper scenario-server-restart scenario-half-open scenario-silent-subscription

clean:
	rm -rf bin
