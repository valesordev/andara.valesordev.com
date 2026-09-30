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
# Where CI publishes the server image and dev/prod pull it from (AW-INF-013).
REGISTRY_IMAGE := ghcr.io/valesordev/andara-server
TAG         ?= dev
KIND_CLUSTER ?= $(shell kind get clusters 2>/dev/null | head -1)
DURATION    ?= 300
SOAK        ?= 5m
# `--match 'v*'`: release tags only. The rolling `cli-dev` tag (AW-INF-020) sits on a recent
# main commit, and without the match every build after it would call itself `cli-dev-N-g…`.
VERSION     ?= $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)
# `-dirty` exactly when a tracked file differs from HEAD, which is what VERSION's
# `git describe --dirty` means; an untracked file doesn't count. It's in COMMIT and REVISION
# themselves, so `make up`, `make image` and `make build` can't stamp one tree two ways, and
# andara_build_info{commit} and the image's revision label agree about what ran (AW-INF-016).
DIRTY       := $(shell git rev-parse --verify -q HEAD >/dev/null 2>&1 && { git diff --quiet HEAD -- 2>/dev/null || echo -dirty; })
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)$(DIRTY)
REVISION    ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)$(DIRTY)
BUILT_AT    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
CLI_PKG     := github.com/valesordev/andara/admin/cli
LDFLAGS_CLI := -X $(CLI_PKG).version=$(VERSION) -X $(CLI_PKG).commit=$(COMMIT) -X $(CLI_PKG).builtAt=$(BUILT_AT)

# Empty when the module has no Go packages yet, which is the state until AW-SRV-001
# lands. The Go steps skip rather than fail so that `make check` is green from day one.
HAS_GO := $(shell find . -name '*.go' -not -path './.git/*' -not -path './bin/*' -print -quit 2>/dev/null)

.PHONY: help bootstrap up down logs ps tls auth-keys topics-apply topics-diff \
        schemas-apply schemas-check schemas-diff check fmt fmt-check vet lint test test-integration test-determinism \
        proto proto-check backlog backlog-check status status-check story adr validate-stories \
        graph k8s-dry check-targets clean build build-info goldens \
        values-schema values-schema-check helm-test image image-publish image-check cli-release cli-release-check cli-release-publish kind-load helm-install measure-tick stack-smoke stack-play stack-linkdead \
        kind-platform stream-soak content-grammar-check observe-check observe-unavailable scripts-test kafka-operator kafka-install kafka-broker-bounce \
        argocd-install argocd-status argocd-ui argocd-recover argocd-uninstall world-reset \
        objectstore-install projector-stop projector-start projector-rebuild

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
	@$(SCRIPTS)/stack.sh up "$(PROFILE)" "$(VERSION)" "$(COMMIT)" "$(REVISION)"

## build-info: print VERSION, COMMIT and REVISION as `make up`, `make image` and `make build` stamp them
build-info:
	@printf 'VERSION=%s\nCOMMIT=%s\nREVISION=%s\n' "$(VERSION)" "$(COMMIT)" "$(REVISION)"

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

## topics-apply: create missing Kafka topics and align existing ones to deploy/kafka/topics.yaml — ALLOW_DATA_LOSS=<topic>[,…] accepts a change that deletes data
topics-apply:
	@$(PY) $(SCRIPTS)/topics.py apply --env $(ANDARA_ENV) $(if $(ALLOW_DATA_LOSS),--allow-data-loss "$(ALLOW_DATA_LOSS)")

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
                 license-check content-grammar-check content-conformance scripts-test

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
# The s3 store's tests skip unless ANDARA_S3_TEST_ENDPOINT names an endpoint, so `make up
# PROFILE=min` — which has no MinIO — still passes. With the full stack, run them with
# `ANDARA_S3_TEST_ENDPOINT=localhost:$${ANDARA_S3_PORT:-19000} make test-integration`; the
# credentials default to the compose service's. CI exports all three (stack workflow).
test-integration:
	@ANDARA_KAFKA_BROKERS="$${ANDARA_KAFKA_BROKERS:-localhost:$${ANDARA_KAFKA_PORT:-9092}}" \
	  ANDARA_S3_TEST_ACCESS_KEY="$${ANDARA_S3_TEST_ACCESS_KEY:-andaratest}" \
	  ANDARA_S3_TEST_SECRET_KEY="$${ANDARA_S3_TEST_SECRET_KEY:-andaratest123}" \
	  $(GO) test -tags integration -race -count=1 -v -timeout 10m ./server/recordlog/ ./server/tickloop/ ./server/ingress/ ./server/store/ ./server/content/ ./server/projector/

## proto: regenerate committed protobuf code from docs/specs/protocol/
proto:
	@$(SCRIPTS)/proto.sh gen

## proto-check: fail if committed protobuf code is stale
proto-check:
	@$(SCRIPTS)/proto.sh check

## backlog: render BACKLOG.md from story frontmatter, write it, and print it — not committed (AW-INF-026)
backlog:
	@$(PY) $(SCRIPTS)/gen_backlog.py

## backlog-check: fail if the backlog view doesn't render; writes nothing
backlog-check:
	@$(PY) $(SCRIPTS)/gen_backlog.py --check

## status: render docs/status.md — each lane's development state, one screen — write it and print it; not committed (AW-INF-026)
status:
	@$(PY) $(SCRIPTS)/gen_status.py

## status-check: fail if the status view doesn't render within its one-screen budget; writes nothing
status-check:
	@$(PY) $(SCRIPTS)/gen_status.py --check

## validate-stories: schema-check frontmatter, resolve IDs, detect cycles
validate-stories:
	@$(PY) $(SCRIPTS)/validate_stories.py

## scripts-test: unit tests for the Python tooling under scripts/ (scripts/tests)
scripts-test:
	@$(PY) -m unittest discover -s $(SCRIPTS)/tests

## content-grammar-check: parse the Content Language corpus against grammar.ebnf (AW-CLI-005 AC-1)
content-grammar-check:
	@$(PY) $(SCRIPTS)/content_grammar_check.py

## content-conformance: run the AW-CLI-005 corpus against the compiler (AW-CLI-006 AC-1)
content-conformance:
	@$(GO) run ./content/conformance

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
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg REVISION=$(REVISION) \
	  -t $(IMAGE):$(TAG) .
	@echo "image: $(IMAGE):$(TAG)"

## image-publish: build the server image for linux/amd64 and push :sha-<12 hex> then :dev to ghcr — what CI runs on merge to main; needs `docker login ghcr.io` (AW-INF-013)
image-publish:
	@$(SCRIPTS)/image_publish.sh "$(REGISTRY_IMAGE)" "$(VERSION)" "$(COMMIT)" "$(REVISION)"

## image-check: prove a published tag pulls anonymously and from the cluster — ENV=<env> TAG=<tag>, default dev; REGISTRY_ONLY=1 skips the cluster (AW-INF-013)
image-check:
	@$(SCRIPTS)/image_check.sh "$(ENV)" "$(REGISTRY_IMAGE)" "$(TAG)" "$(REGISTRY_ONLY)"

# TAG defaults to `dev` for the image targets; the CLI's rolling release is `cli-dev`, so
# the CLI targets take TAG only when it's given on the command line.
CLI_TAG = $(if $(filter command line,$(origin TAG)),$(TAG),cli-dev)

## cli-release: build andara-cli for every Builder platform into dist/, with SHA256SUMS
cli-release:
	@$(SCRIPTS)/cli_release.sh "$(GO)" "$(VERSION)" "$(LDFLAGS_CLI)"

## cli-release-check: download a published andara-cli anonymously, verify it, and run it — TAG=<tag>, default cli-dev (AW-INF-020)
cli-release-check:
	@$(SCRIPTS)/cli_release_check.sh "$(CLI_TAG)"

## cli-release-publish: attach dist/ to the cli-dev pre-release (only from main's head) or a v* release — TAG=<cli-dev|v*>; what CI runs, needs gh with contents: write (AW-INF-020)
cli-release-publish:
	@$(SCRIPTS)/cli_release_publish.sh "$(CLI_TAG)"

## kind-load: load the built image into the kind cluster — KIND_CLUSTER=<name>
kind-load:
	@if [[ -z "$(KIND_CLUSTER)" ]]; then echo "make: kind-load: no kind cluster found" >&2; exit 1; fi
	@kind load docker-image $(IMAGE):$(TAG) --name $(KIND_CLUSTER)
	@echo "kind-load: $(IMAGE):$(TAG) -> kind/$(KIND_CLUSTER)"

## helm-install: idempotent `helm upgrade --install` of the chart into namespace andara-<env> — ENV=<env> [TAG=<tag>]
# IMAGE/TAG override a registry environment's values only when given explicitly: their
# defaults name the local build (scripts/helm_image_args.sh).
helm-install:
	@PY=$(PY) $(SCRIPTS)/helm_install.sh "$(ENV)" "$(IMAGE)" "$(TAG)" \
	  "$(if $(filter file,$(origin IMAGE)),,1)" "$(if $(filter file,$(origin TAG)),,1)"

## stack-smoke: open a Session on the running stack and verify Prometheus counted it — needs `make up`
stack-smoke:
	@GO=$(GO) PY=$(PY) $(SCRIPTS)/stack_smoke.sh

## stack-play: the M1 gate scripted — `andara-cli play` against the running stack — needs `make up` and `make build`
stack-play: build
	@$(SCRIPTS)/stack_play.sh

## stack-linkdead: the linkdead gate scripted — drop a player's stream, reconnect, and a bystander sees both — needs `make up` and `make build`
stack-linkdead: build
	@$(SCRIPTS)/stack_linkdead.sh

## kind-platform: install Traefik and cert-manager into a fresh kind cluster the way the box has them — KIND_CLUSTER=<name>
kind-platform:
	@$(SCRIPTS)/kind_platform.sh "$(KIND_CLUSTER)"

## kafka-operator: install the Strimzi operator (1.2.0) into namespace strimzi, watching andara-dev and andara-prod — once per cluster (AW-INF-014)
kafka-operator:
	@PY=$(PY) $(SCRIPTS)/kafka.sh operator

## kafka-install: Apache Kafka `andara-log` (3 brokers, KRaft) in andara-<env>, then the topics from deploy/kafka/topics.yaml — ENV=<dev|prod> (AW-INF-014)
kafka-install:
	@PY=$(PY) $(SCRIPTS)/kafka.sh install "$(ENV)"

## kafka-broker-bounce: delete one broker and prove the server never left service while it was gone — ENV=<dev|prod> (AW-INF-014 AC-5)
kafka-broker-bounce:
	@PY=$(PY) $(SCRIPTS)/kafka.sh bounce "$(ENV)"

## argocd-install: Argo CD and Image Updater into `argocd` at pinned versions; with ENV=dev, dev's Secrets (if absent) and the andara-dev Application — dev follows main (AW-INF-019)
argocd-install:
	@$(PY) $(SCRIPTS)/argocd.py install "$(if $(filter command line,$(origin ENV)),$(ENV),)"

## objectstore-install: versitygw, its PVC, the Secret andara-snapshot-s3 (generated once) and the bucket andara-snapshots-<env> in andara-<env> — idempotent — ENV=<dev|prod> (AW-INF-025)
objectstore-install:
	@$(PY) $(SCRIPTS)/objectstore.py install "$(if $(filter command line,$(origin ENV)),$(ENV))"

## projector-stop: scale the state projector to 0 and wait for its consumer group to empty — ENV=<dev|prod> [PROJECTOR_STOP_TIMEOUT=120s] (AW-INF-025)
projector-stop:
	@$(PY) $(SCRIPTS)/projector.py stop "$(if $(filter command line,$(origin ENV)),$(ENV))"

## projector-start: scale the state projector to 1 and wait for Ready — ENV=<dev|prod> [PROJECTOR_START_TIMEOUT=300s] (AW-INF-025)
projector-start:
	@$(PY) $(SCRIPTS)/projector.py start "$(if $(filter command line,$(origin ENV)),$(ENV))"

## projector-rebuild: stop the state projector, run `andara-projector state --rebuild` as a one-shot Job until it catches up, then start it — ENV=<dev|prod> [PROJECTOR_REBUILD_TIMEOUT=30m] (AW-INF-025)
projector-rebuild:
	@$(PY) $(SCRIPTS)/projector.py rebuild "$(if $(filter command line,$(origin ENV)),$(ENV))"

## world-reset: recreate ENV's World log and Account store, keeping content — destroys every Character and Account — ENV=<env> CONFIRM=andara-<env> (AW-INF-021)
world-reset:
	@$(PY) $(SCRIPTS)/world_reset.py "$(if $(filter command line,$(origin ENV)),$(ENV))" "$(CONFIRM)"

## argocd-status: the andara-dev Application's sync and health, the main revision it synced, and the image it runs with the commit that built it — exit 1 unless Synced/Healthy
argocd-status:
	@$(PY) $(SCRIPTS)/argocd.py status "$(if $(filter command line,$(origin ENV)),$(ENV),dev)"

## argocd-ui: port-forward the Argo CD UI to localhost (ARGOCD_UI_PORT, default 8090), and say where the admin password is
argocd-ui:
	@$(PY) $(SCRIPTS)/argocd.py ui

## argocd-recover: replace an andara pod stuck on a build that never became Ready, once a good build has synced (Kubernetes' forced rollback) — ENV=dev
argocd-recover:
	@$(PY) $(SCRIPTS)/argocd.py recover "$(ENV)"

## argocd-uninstall: remove the andara-dev Application without cascading and hand its resources back to Helm; `make helm-install ENV=dev` then works again — ENV=dev
argocd-uninstall:
	@$(PY) $(SCRIPTS)/argocd.py uninstall "$(ENV)"

## observe-check: ask Grafana Cloud whether andara-<env>'s metrics, logs, and traces arrived (GRAFANA_CLOUD_* from the environment; exits 3 without them) — ENV=<env>
observe-check:
	@$(PY) $(SCRIPTS)/observe_check.py "$(ENV)"

## observe-unavailable: scale andara-dev's server to 0 and back, and assert AndaraServerUnavailable names andara-dev and clears, no other namespace moving (GRAFANA_CLOUD_* from the environment) — ENV=dev
observe-unavailable:
	@$(PY) $(SCRIPTS)/observe_unavailable.py "$(ENV)"

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
	@$(GO) build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/andara-projector ./cmd/andara-projector
	@echo "build: bin/andara-cli bin/andara-server bin/andara-projector"

## goldens: regenerate CLI --help golden files and the play rendering transcript
goldens:
	@$(GO) test ./admin/cli -count=1 -run '^(TestHelpGoldens|TestRender_Golden)$$' -args -update
	@echo "goldens: updated admin/cli/testdata/help and admin/cli/testdata/play/transcript.txt"

## clean: remove build artifacts and local state
clean:
	@rm -rf bin/ .local/ coverage.out
	@echo "clean: removed bin/, .local/, coverage.out"
