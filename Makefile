# Andara's World — the entire automation surface.
#
# Every workflow in this repo is a target here (CLAUDE.md §9). A documented
# sequence of shell commands that is not a target is a defect.
#
# Onboarding on a new machine, in full:
#     make bootstrap && make up && make check

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help
.ONESHELL:

PY          ?= python3
GO          ?= go
SCRIPTS     := scripts
ENV         ?= dev
PROFILE     ?= full
VOLUMES     ?= 0
PKG         ?= ./...
COMPOSE     := deploy/compose/docker-compose.yaml

# Empty when the module has no Go packages yet, which is the state until AW-SRV-001
# lands. The Go steps skip rather than fail so that `make check` is green from day one.
HAS_GO := $(shell find . -name '*.go' -not -path './.git/*' -print -quit 2>/dev/null)

.PHONY: help bootstrap up down logs ps check fmt fmt-check vet lint test \
        backlog backlog-check story adr validate-stories graph k8s-dry clean

## help: print this target list
help:
	@echo "Andara's World — make targets"
	@echo
	@grep -hE '^## [a-z0-9_-]+:' $(MAKEFILE_LIST) | sed 's/^## /  /' | \
	  awk -F': ' '{ printf "  \033[1m%-18s\033[0m %s\n", $$1, $$2 }' | sed 's/^  //'
	@echo
	@echo "Onboarding: make bootstrap && make up && make check"

## bootstrap: install and verify the toolchain; idempotent
bootstrap:
	@$(SCRIPTS)/bootstrap.sh

## up: start the local stack (server + datastores + observability)
up:
	@$(SCRIPTS)/stack.sh up "$(PROFILE)"

## down: stop the local stack; VOLUMES=1 also removes volumes
down:
	@$(SCRIPTS)/stack.sh down "$(VOLUMES)"

## logs: tail local stack logs; SVC=<name> for one service
logs:
	@$(SCRIPTS)/stack.sh logs "$(SVC)"

## ps: show local stack service status
ps:
	@$(SCRIPTS)/stack.sh ps

## check: fmt, vet, lint, test, story validation, manifest validation — what CI runs
check: fmt-check vet lint test validate-stories backlog-check k8s-dry
	@echo "check: all clean"

## fmt: format Go sources in place
fmt:
ifeq ($(HAS_GO),)
	@echo "fmt: no Go sources yet; skipping"
else
	@$(GO) fmt $(PKG)
endif

## fmt-check: fail if any Go source is unformatted
fmt-check:
ifeq ($(HAS_GO),)
	@echo "fmt-check: no Go sources yet; skipping"
else
	@out=$$(gofmt -l . | grep -v '^$$' || true); \
	if [[ -n "$$out" ]]; then \
	  echo "make: fmt-check: unformatted files (run \`make fmt\`):" >&2; \
	  echo "$$out" >&2; exit 1; \
	fi; \
	echo "fmt-check: clean"
endif

## vet: run go vet
vet:
ifeq ($(HAS_GO),)
	@echo "vet: no Go sources yet; skipping"
else
	@$(GO) vet $(PKG)
endif

## lint: run golangci-lint, including the sim-core import boundary
lint:
ifeq ($(HAS_GO),)
	@echo "lint: no Go sources yet; skipping"
else
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
	  echo "make: lint: golangci-lint not found (run \`make bootstrap\`)" >&2; exit 1; \
	fi; \
	golangci-lint run
endif

## test: run tests; PKG=./path/... to narrow
test:
ifeq ($(HAS_GO),)
	@echo "test: no Go sources yet; skipping"
else
	@$(GO) test -race -count=1 $(PKG)
endif

## backlog: regenerate BACKLOG.md from story frontmatter
backlog:
	@$(PY) $(SCRIPTS)/gen_backlog.py

## backlog-check: fail if BACKLOG.md is stale
backlog-check:
	@$(PY) $(SCRIPTS)/gen_backlog.py --check

## validate-stories: schema-check frontmatter, resolve IDs, detect cycles
validate-stories:
	@$(PY) $(SCRIPTS)/validate_stories.py

## graph: emit the story dependency DAG as mermaid
graph:
	@$(PY) $(SCRIPTS)/gen_graph.py

## story: scaffold a story — make story COMP=SRV TITLE="..."
story:
	@if [[ -z "$(COMP)" || -z "$(TITLE)" ]]; then \
	  echo 'make: story: usage: make story COMP=<SRV|CLI|INF|CLT> TITLE="<title>"' >&2; exit 2; \
	fi
	@$(PY) $(SCRIPTS)/new_story.py "$(COMP)" "$(TITLE)"

## adr: scaffold an ADR — make adr TITLE="..."
adr:
	@if [[ -z "$(TITLE)" ]]; then \
	  echo 'make: adr: usage: make adr TITLE="<title>"' >&2; exit 2; \
	fi
	@$(PY) $(SCRIPTS)/new_adr.py "$(TITLE)"

## k8s-dry: render and validate manifests for ENV=<env>
k8s-dry:
	@$(SCRIPTS)/k8s_dry.sh "$(ENV)"

## clean: remove build artifacts and local state
clean:
	@rm -rf bin/ .local/ coverage.out
	@echo "clean: removed bin/, .local/, coverage.out"
