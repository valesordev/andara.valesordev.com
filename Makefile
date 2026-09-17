# Andara's World — the entire automation surface.
#
# Every workflow in this repo is a target here (CLAUDE.md §9). A documented
# sequence of shell commands that is not a target is a defect.
#
# Onboarding on a new machine, in full:
#     make bootstrap && make up && make check

SHELL := bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help
.ONESHELL:

# Pinned tooling lives in ./bin (see scripts/bootstrap.sh), ahead of whatever the
# developer happens to have installed globally. This is what makes "CI runs the same
# check the developer does" true for tool versions and not only for target names.
export PATH := $(CURDIR)/bin:$(PATH)

PY          ?= python3
GO          ?= go
SCRIPTS     := scripts
ENV         ?= dev
# The stack targets talk to a broker, and a broker is a place, not an environment name.
# ENV=dev with replication_factor 3 against a single-node local Redpanda would fail in a
# confusing way, so the topic targets default to local and are overridden explicitly.
ANDARA_ENV  ?= local
PROFILE     ?= full
VOLUMES     ?= 0
PKG         ?= ./...
COMPOSE     := deploy/compose/docker-compose.yaml
IMAGE       ?= andara-server
TAG         ?= dev
KIND_CLUSTER ?= $(shell kind get clusters 2>/dev/null | head -1)
DURATION    ?= 300
SOAK        ?= 5m
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILT_AT    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
CLI_PKG     := github.com/valesordev/andara/admin/cli
LDFLAGS_CLI := -X $(CLI_PKG).version=$(VERSION) -X $(CLI_PKG).commit=$(COMMIT) -X $(CLI_PKG).builtAt=$(BUILT_AT)

# Empty when the module has no Go packages yet, which is the state until AW-SRV-001
# lands. The Go steps skip rather than fail so that `make check` is green from day one.
HAS_GO := $(shell find . -name '*.go' -not -path './.git/*' -not -path './bin/*' -print -quit 2>/dev/null)

.PHONY: help bootstrap up down logs ps tls auth-keys topics-apply topics-diff \
        schemas-apply schemas-check schemas-diff check fmt fmt-check vet lint test test-integration test-determinism \
        proto proto-check backlog backlog-check status status-check story adr validate-stories \
        graph k8s-dry check-targets clean build goldens \
        values-schema values-schema-check helm-test image kind-load helm-install measure-tick stack-smoke \
        kind-platform stream-soak

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

## tls: provision the local CA and server certificate; FORCE=1 to reissue
tls:
	@$(SCRIPTS)/tls.sh

## auth-keys: provision the local session-token signing keyring; FORCE=1 to regenerate
auth-keys:
	@$(SCRIPTS)/auth_keys.sh

## topics-apply: create missing Kafka topics from deploy/kafka/topics.yaml
topics-apply:
	@$(PY) $(SCRIPTS)/topics.py apply --env $(ANDARA_ENV)

## topics-diff: fail if a live topic has drifted from the declaration
topics-diff:
	@$(PY) $(SCRIPTS)/topics.py diff --env $(ANDARA_ENV)

## schemas-apply: register protobuf schemas with the schema registry
schemas-apply:
	@$(PY) $(SCRIPTS)/schemas.py apply --env $(ANDARA_ENV)

## schemas-diff: fail if the registry has drifted from deploy/kafka/schemas.yaml
schemas-diff:
	@$(PY) $(SCRIPTS)/schemas.py diff --env $(ANDARA_ENV)

## schemas-check: verify the subject declaration — offline, no broker needed
schemas-check:
	@$(PY) $(SCRIPTS)/schemas.py check

# The single list. CI enumerates these as named steps for diagnosability, and a parity
# guard in the workflow reads this target to prove the two lists have not drifted —
# they had, silently, before `status-check` existed.
CHECK_TARGETS := fmt-check vet lint test proto-check schemas-check validate-stories \
                 backlog-check status-check values-schema-check k8s-dry helm-test \
                 license-check

## check: fmt, vet, lint, test, proto, story validation, manifests — what CI runs
check: $(CHECK_TARGETS)
	@echo "check: all clean"

## check-targets: print what `make check` runs, one per line — the CI parity guard reads this
check-targets:
	@printf '%s\n' $(CHECK_TARGETS)

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

## test-determinism: the replay-equality and golden-hash tests, what CI runs on every architecture
test-determinism:
	@$(GO) test -count=1 -run 'GoldenHash|Replay|RNG|StateHash|Deterministic|ZoneFault' ./server/sim/ ./server/tickloop/

## test-integration: run the broker-backed tests against the running stack — needs `make up`
test-integration:
	@ANDARA_KAFKA_BROKERS="$${ANDARA_KAFKA_BROKERS:-localhost:$${ANDARA_KAFKA_PORT:-9092}}" \
	  $(GO) test -tags integration -race -count=1 -v -timeout 10m ./server/recordlog/ ./server/tickloop/

## proto: regenerate committed protobuf code from docs/specs/protocol/
proto:
	@$(SCRIPTS)/proto.sh gen

## proto-check: fail if committed protobuf code is stale
proto-check:
	@$(SCRIPTS)/proto.sh check

## backlog: regenerate BACKLOG.md from story frontmatter
backlog:
	@$(PY) $(SCRIPTS)/gen_backlog.py

## backlog-check: fail if BACKLOG.md is stale
backlog-check:
	@$(PY) $(SCRIPTS)/gen_backlog.py --check

## status: regenerate docs/status.md — the two-lane development state, one screen
status:
	@$(PY) $(SCRIPTS)/gen_status.py

## status-check: fail if docs/status.md is stale
status-check:
	@$(PY) $(SCRIPTS)/gen_status.py --check

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

## k8s-dry: render and validate the chart against Kubernetes 1.36.1 — ENV=<env>, or every environment when ENV is not given
k8s-dry:
	@$(SCRIPTS)/k8s_dry.sh "$(if $(filter command line,$(origin ENV)),$(ENV),all)"

## values-schema: regenerate the chart's values.schema.json and templates/_env.tpl from keys.yaml
values-schema:
	@$(PY) $(SCRIPTS)/values_schema.py

## values-schema-check: fail if an ANDARA_* the server reads is missing from keys.yaml, or the generated files are stale
values-schema-check:
	@$(PY) $(SCRIPTS)/values_schema.py --check

## helm-test: render-level assertions over the chart for every environment (AW-INF-003 test plan)
helm-test:
	@$(PY) $(SCRIPTS)/helm_test.py

## license-check: REUSE compliance and SPDX headers — the declaration in LICENSING.md, verified
license-check:
	@PY=$(PY) $(SCRIPTS)/license_check.sh

## image: build the andara-server image from deploy/compose/Dockerfile.server — TAG=<tag>, default dev
image:
	@docker build -f deploy/compose/Dockerfile.server \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
	  -t $(IMAGE):$(TAG) .
	@echo "image: $(IMAGE):$(TAG)"

## kind-load: load the built image into the kind cluster — KIND_CLUSTER=<name>
kind-load:
	@if [[ -z "$(KIND_CLUSTER)" ]]; then echo "make: kind-load: no kind cluster found" >&2; exit 1; fi
	@kind load docker-image $(IMAGE):$(TAG) --name $(KIND_CLUSTER)
	@echo "kind-load: $(IMAGE):$(TAG) -> kind/$(KIND_CLUSTER)"

## helm-install: idempotent `helm upgrade --install` of the chart into namespace andara-<env> — ENV=<env>
helm-install:
	@$(SCRIPTS)/helm_install.sh "$(ENV)" "$(IMAGE)" "$(TAG)"

## stack-smoke: open a Session on the running stack and verify Prometheus counted it — needs `make up`
stack-smoke:
	@GO=$(GO) PY=$(PY) $(SCRIPTS)/stack_smoke.sh

## kind-platform: install Traefik and cert-manager into a fresh kind cluster the way the box has them — KIND_CLUSTER=<name>
kind-platform:
	@$(SCRIPTS)/kind_platform.sh "$(KIND_CLUSTER)"

## stream-soak: hold a Subscribe through the edge for SOAK (default 5m), renewing the edge certificate mid-stream — ENV=<env> SOAK=<duration>
stream-soak:
	@GO=$(GO) PY=$(PY) $(SCRIPTS)/stream_soak.sh "$(ENV)" "$(SOAK)"

## measure-tick: run the server against the sizing fixture and record p99 tick CPU and RSS into measurements.yaml — DURATION=<seconds>
measure-tick:
	@$(SCRIPTS)/measure_tick.sh "$(DURATION)"

## build: compile andara-cli and andara-server into ./bin
build:
	@mkdir -p bin
	@$(GO) build -ldflags "$(LDFLAGS_CLI)" -o bin/andara-cli ./cmd/andara-cli
	@$(GO) build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/andara-server ./cmd/andara-server
	@echo "build: bin/andara-cli bin/andara-server"

## goldens: regenerate CLI --help golden files
goldens:
	@$(GO) test ./admin/cli -count=1 -run '^TestHelpGoldens$$' -args -update
	@echo "goldens: updated admin/cli/testdata/help"

## clean: remove build artifacts and local state
clean:
	@rm -rf bin/ .local/ coverage.out
	@echo "clean: removed bin/, .local/, coverage.out"
