SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

COMPOSE ?= docker compose
PYTHON  ?= python3
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help build test py-test fmt vet ci compose-config demo demo-down logs verify clean \
	scenarios scenario-inventory-change scenario-gap scenario-restart scenario-tamper

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
	  'Scenarios (local processes, no Docker; need mosquitto and Python with asyncua):' \
	  '  make scenario-inventory-change   a node appears at runtime: source.inventory.added with diff' \
	  '  make scenario-gap                the server goes away and comes back: a gap with duration' \
	  '  make scenario-restart            the adapter restarts: chain_restart with anchor, a notice' \
	  '  make scenario-tamper             one value is edited: hash_mismatch, then chain_break' \
	  '  make scenarios                   all four, one after another'

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
	  scripts/tamper.py scripts/tcp_relay.py

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

scenario-inventory-change scenario-gap scenario-restart scenario-tamper: scenario-%: build
	scripts/scenario.sh $*

scenarios: scenario-inventory-change scenario-gap scenario-restart scenario-tamper

clean:
	rm -rf bin
