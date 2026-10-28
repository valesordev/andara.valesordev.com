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
# kubectl, helm and kind act on the cluster ANDARA_KUBE_CONTEXT names, default kind-andara, read
# from the environment by scripts/kube.py and scripts/kube.sh (AW-INF-053). kind's cluster is the
# context without its kind- prefix.
KIND        ?= kind
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
        proto proto-check adr \
        k8s-dry check-targets clean build build-info goldens \
        values-schema values-schema-check helm-test image image-publish image-check cli-release cli-release-check cli-release-publish kind-load helm-install measure-tick stack-smoke stack-play stack-linkdead stack-recover stack-recover-mismatch \
        platform-up platform-check collectors-check collectors-probe collectors-logs kube-context-check cluster-up cluster-down cluster-rebuild stream-soak content-grammar-check observe-check slo-report dashboard-check observe-unavailable gcx-config gcx-check gcx-only-check scripts-test kafka-install kafka-broker-bounce shared-up shared-down shared-check shared-netcheck kafka-users kafka-users-check kafka-creds kafka-creds-rotate kafka-acl-probe \
        argocd-install promote promote-log argocd-status argocd-ui argocd-recover argocd-uninstall world-reset content-seed env-destroy env-recover \
        objectstore-up objectstore-install objectstore-check projector-stop projector-start projector-rebuild server-stop server-start core-versions-check \
        builder-reference builder-reference-check guide-check \
        stack-boundary-lost cli-offline-check stack-projector-check recovery-timing recovery-timing-previous recovery-timing-summary startup-budget-check alerts-sync alerts-diff update-agents \
        tf-plan tf-apply tf-unlock tf-test-alert tf-test-alert-clean tf-fmt-check tf-validate tf-test tf-lock tf-bootstrap-gcp tf-drift tf-plan-show tf-workflow-check tf-ci-environments tf-plan-key tf-replan tf-credentials-check pipelines-check

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

## topics-apply: create missing Kafka topics and align existing ones to deploy/kafka/topics.yaml, named under ENV's prefix — ENV=<local|dev> (default local), BROKER=<compose|cluster> (local only; compose while compose exists), ALLOW_DATA_LOSS=<topic>[,…] accepts a change that deletes data
topics-apply:
	@$(PY) $(SCRIPTS)/topics.py apply --env $(TOPICS_ENV) $(if $(BROKER),--broker "$(BROKER)") $(if $(ALLOW_DATA_LOSS),--allow-data-loss "$(ALLOW_DATA_LOSS)")

## topics-diff: fail if a live topic has drifted from the declaration — ENV=<local|dev>, BROKER as topics-apply
topics-diff:
	@$(PY) $(SCRIPTS)/topics.py diff --env $(TOPICS_ENV) $(if $(BROKER),--broker "$(BROKER)")

# ENV on the command line, else ANDARA_ENV (default local), as before AW-INF-055.
TOPICS_ENV = $(if $(filter command line,$(origin ENV)),$(ENV),$(ANDARA_ENV))

## schemas-apply: register ENV's prefixed subjects (`dev.andara.commands.v1-value`) in the schema registry; refuses an incompatible change, registering nothing — ENV=<local|dev> [BROKER=cluster] (the shared Karapace in andara-shared; local defaults to compose's while deploy/compose/ exists) (AW-INF-057)
schemas-apply:
	@$(PY) $(SCRIPTS)/schemas.py apply --env "$(TOPICS_ENV)" $(if $(BROKER),--broker "$(BROKER)")

## schemas-diff: fail if ENV's subjects in the registry have drifted from deploy/kafka/schemas.yaml, or any subject has no environment prefix — ENV=<local|dev> [BROKER=cluster] (AW-INF-057)
schemas-diff:
	@$(PY) $(SCRIPTS)/schemas.py diff --env "$(TOPICS_ENV)" $(if $(BROKER),--broker "$(BROKER)")

## schemas-check: verify the subject declaration — offline, no broker needed
schemas-check:
	@$(PY) $(SCRIPTS)/schemas.py check

# The single list. CI enumerates these as named steps for diagnosability, and a parity
# guard in the workflow reads this target to prove the two lists have not drifted —
# they had, silently, before `status-check` existed.
CHECK_TARGETS := fmt-check vet lint test proto-check schemas-check \
                 values-schema-check k8s-dry helm-test \
                 license-check content-grammar-check content-conformance scripts-test \
                 core-versions-check builder-reference-check guide-check \
                 tf-fmt-check tf-validate tf-test tf-workflow-check gcx-only-check platform-check \
                 kube-context-check kafka-users-check pipelines-check

## check: fmt, vet, lint, test, proto, manifests — what CI runs
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
	  $(GO) test -tags integration -race -count=1 -v -timeout 10m ./server/recordlog/ ./server/tickloop/ ./server/ingress/ ./server/store/ ./server/content/ ./server/projector/ ./server/boot/ ./server/recovery/ ./admin/cli/

## recovery-timing: AW-SRV-007 AC-7 — recover the sizing fixture with a 600-tick tail, timed per phase, into RECOVERY_TIMING_OUT — needs `make up` (a broker; PROFILE=min is enough); takes minutes
# No -race: the bound under test is 90 s of replay at the 100 ms tick, and the race detector's
# slowdown would measure the detector. The test asserts the bound and the tail itself.
RECOVERY_TIMING_OUT ?= .local/recovery-timing.json
recovery-timing:
	@mkdir -p "$(dir $(RECOVERY_TIMING_OUT))"
	@rm -f "$(RECOVERY_TIMING_OUT)"
	@ANDARA_RECOVERY_TIMING=1 ANDARA_RECOVERY_TIMING_OUT="$(abspath $(RECOVERY_TIMING_OUT))" \
	  ANDARA_KAFKA_BROKERS="$${ANDARA_KAFKA_BROKERS:-localhost:$${ANDARA_KAFKA_PORT:-9092}}" \
	  $(GO) test -tags integration -count=1 -v -timeout 20m -run '^TestRecoveryTimingAtSizingScale$$' ./server/recovery/

## recovery-timing-previous: download the last successful main run's recovery-timing artifact into RECOVERY_TIMING_PREVIOUS (default .local/recovery-timing-previous) — needs gh
RECOVERY_TIMING_PREVIOUS ?= .local/recovery-timing-previous
recovery-timing-previous:
	@mkdir -p "$(RECOVERY_TIMING_PREVIOUS)"
	@python3 $(SCRIPTS)/recovery_timing.py previous "$(RECOVERY_TIMING_PREVIOUS)"

## recovery-timing-summary: render RECOVERY_TIMING_OUT against the previous run's as a table, to $$GITHUB_STEP_SUMMARY when set
recovery-timing-summary:
	@python3 $(SCRIPTS)/recovery_timing.py summary "$(RECOVERY_TIMING_OUT)" "$(RECOVERY_TIMING_PREVIOUS)/recovery-timing.json"

## startup-budget-check: AW-INF-011 AC-7 — the chart's startup budget (every env's values) is at least 3x RECOVERY_TIMING_OUT's measured total
startup-budget-check:
	@for env in local dev prod; do \
	  echo "== $$env"; \
	  python3 $(SCRIPTS)/recovery_timing.py budget "$(RECOVERY_TIMING_OUT)" deploy/helm/andara/values.yaml deploy/helm/values/$$env.yaml || exit 1; \
	done

## proto: regenerate committed protobuf code from docs/specs/protocol/
proto:
	@$(SCRIPTS)/proto.sh gen

## proto-check: fail if committed protobuf code is stale
proto-check:
	@$(SCRIPTS)/proto.sh check

## scripts-test: unit tests for the Python tooling under scripts/ (scripts/tests)
# Against a copy of a fixture kubeconfig whose current-context is some-other, never the developer's
# own, and with ANDARA_KUBE_CONTEXT unset (AW-INF-053).
scripts-test:
	@kubeconfig="$$(mktemp)"
	trap 'rm -f "$$kubeconfig"' EXIT
	cp $(SCRIPTS)/tests/fixtures/kubeconfig "$$kubeconfig"
	KUBECONFIG="$$kubeconfig" env -u ANDARA_KUBE_CONTEXT $(PY) -m unittest discover -s $(SCRIPTS)/tests

## builder-reference: regenerate the Builder's Guide reference tables from andara-cli content reference
builder-reference:
	@$(PY) $(SCRIPTS)/builder_guide.py reference

## builder-reference-check: fail if docs/builders/reference.md differs from what make builder-reference writes
builder-reference-check:
	@$(PY) $(SCRIPTS)/builder_guide.py reference-check

## guide-check: every andara-cli command in the Builder's Guide exists, every diagnostic code is documented, every link resolves
guide-check:
	@$(PY) $(SCRIPTS)/builder_guide.py guide-check

## core-versions-check: fail if content/core/VERSIONS changes other than by appending the next version (AW-INF-029)
core-versions-check:
	@$(SCRIPTS)/core_versions_check.sh

## content-grammar-check: parse the Content Language corpus against grammar.ebnf (AW-CLI-005 AC-1)
content-grammar-check:
	@$(PY) $(SCRIPTS)/content_grammar_check.py

## content-conformance: run the AW-CLI-005 corpus against the compiler (AW-CLI-006 AC-1)
content-conformance:
	@$(GO) run ./content/conformance

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

## image-publish: build the server image for linux/amd64 and push :sha-<12 hex> to ghcr, printing its digest; nothing moves :dev (AW-INF-059) — what CI runs on merge to main; needs `docker login ghcr.io` (AW-INF-013)
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

## cli-offline-check: run the built linux/amd64 andara-cli with no network and no HOME and check `content reference` — needs `make cli-release` (AW-CLI-009 AC-1)
cli-offline-check:
	@$(SCRIPTS)/cli_offline_check.sh

## cli-release-check: download a published andara-cli anonymously, verify it, and run it — TAG=<tag>, default cli-dev (AW-INF-020)
cli-release-check:
	@$(SCRIPTS)/cli_release_check.sh "$(CLI_TAG)"

## cli-release-publish: attach dist/ to the cli-dev pre-release (only from main's head) or a v* release — TAG=<cli-dev|v*>; what CI runs, needs gh with contents: write (AW-INF-020)
cli-release-publish:
	@$(SCRIPTS)/cli_release_publish.sh "$(CLI_TAG)"

## kind-load: load the built image into the kind cluster ANDARA_KUBE_CONTEXT names (default kind-andara, so `andara`)
kind-load:
	@cluster="$$($(PY) $(SCRIPTS)/kube.py kind-name)"
	$(KIND) load docker-image $(IMAGE):$(TAG) --name "$$cluster"
	echo "kind-load: $(IMAGE):$(TAG) -> kind/$$cluster"

## helm-install: idempotent `helm upgrade --install` of the chart into namespace andara-<env> — ENV=<env> [TAG=<tag>]; PROFILE=ci (ENV=local only) adds values/ci.yaml after local's (CI's lifecycle job), ANDARA_EDGE_PORT (default 8443) is the edge port it prints
# IMAGE/TAG override a registry environment's values only when given explicitly: their
# defaults name the local build (scripts/helm_image_args.sh).
helm-install:
	@PY=$(PY) $(SCRIPTS)/helm_install.sh "$(ENV)" "$(IMAGE)" "$(TAG)" \
	  "$(if $(filter file,$(origin IMAGE)),,1)" "$(if $(filter file,$(origin TAG)),,1)" \
	  "$(if $(filter command line,$(origin PROFILE)),$(PROFILE),)"

## stack-smoke: open a Session on the running stack and verify Prometheus counted it — needs `make up`
stack-smoke:
	@GO=$(GO) PY=$(PY) $(SCRIPTS)/stack_smoke.sh

## stack-play: the M1 gate scripted — `andara-cli play` against the running stack — needs `make up` and `make build`
stack-play: build
	@$(SCRIPTS)/stack_play.sh

## stack-boundary-lost: AW-SRV-026 AC-4 — stop Redpanda past the delivery timeout; the server exits 5 once, recovers to the last delivered boundary, gapless — needs make up (about 3–4 minutes)
stack-boundary-lost:
	@$(SCRIPTS)/stack_boundary_lost.sh

## stack-linkdead: the linkdead gate scripted — drop a player's stream, reconnect, and a bystander sees both — needs `make up` and `make build`
stack-linkdead: build
	@$(SCRIPTS)/stack_linkdead.sh

## stack-recover: the M2 gate scripted — kill -9 the server mid-play, recover from a snapshot within 120 s with a matching State Hash, and both players rebind — needs `make up` and `make build`
STACK_RECOVER_RTO ?= 120
stack-recover: build
	@STACK_RECOVER_RTO=$(STACK_RECOVER_RTO) $(SCRIPTS)/stack_recover.sh

## stack-recover-mismatch: RecoveryStateMismatch observed firing — restart the server on another sim.seed so recovery refuses its round (exit 6), then watch the linger, the alert and the exit — needs `make up` and `make build` (about 2 minutes)
stack-recover-mismatch: build
	@$(SCRIPTS)/stack_recover_mismatch.sh

## stack-projector-check: AW-SRV-043 §7 — `andara-projector state --rebuild` on the host against the stack; its restore metric, log line and spans are observed — needs `make up` and `make build`
stack-projector-check: build
	@$(SCRIPTS)/stack_projector_check.sh

## platform-up: install and reconcile the platform at deploy/platform/versions.yaml's pins, the k8s-monitoring collectors last — [PROFILE=box|ci] [ANDARA_KUBE_CONTEXT=kind-andara] [REQUIRE_LETSENCRYPT=1]; the letsencrypt issuers need CLOUDFLARE_API_TOKEN and the collectors ship only with GRAFANA_SOLO7DEV_TELEMETRY_TOKEN, both from .local/box.env (AW-INF-054, AW-INF-065)
# PROFILE defaults to box here, not to the compose profile `full` above.
platform-up:
	@$(PY) $(SCRIPTS)/platform_up.py up "$(if $(filter command line,$(origin PROFILE)),$(PROFILE),box)"

## collectors-check: the k8s-monitoring release on the cluster — at its pin, the Alloy CRD before the collectors, every collector Ready, no node-exporter, the telemetry Secret's keys (token set or empty, never printed), only the release's pods read it, and on the box with the token exactly one active Fleet collector — [PROFILE=box|ci] (AW-INF-065)
collectors-check:
	@$(PY) $(SCRIPTS)/collectors_live.py check "$(if $(filter command line,$(origin PROFILE)),$(PROFILE),box)"

## collectors-probe: push an unkept and a kept series to the receiver and prove solo7dev keeps only the kept one — the destination's keep rule applies to forwarded series; box only, needs the token (AW-INF-065 AC-7)
collectors-probe:
	@$(PY) $(SCRIPTS)/collectors_live.py probe

## collectors-logs: a collector's warnings and errors over the last hour, read on the cluster because observability ships no logs — COLLECTOR=<metrics|logs|receiver|fleet> (AW-INF-065)
collectors-logs:
	@$(PY) $(SCRIPTS)/collectors_live.py logs "$(COLLECTOR)"

## platform-check: versions.yaml is valid, and no script or workflow pins a platform item outside it (AW-INF-054)
platform-check:
	@$(PY) $(SCRIPTS)/platform_up.py check

## kube-context-check: every kubectl, helm and kind call in scripts/, the Makefile and the workflows names ANDARA_KUBE_CONTEXT's cluster, and none moves current-context (AW-INF-053)
kube-context-check:
	@$(PY) $(SCRIPTS)/kube_check.py

## cluster-up: create the kind cluster `andara` (context kind-andara) from deploy/kind/cluster.yaml, edge on 127.0.0.1:8080 and :8443; idempotent (AW-INF-052)
cluster-up:
	@$(PY) $(SCRIPTS)/cluster.py up

## cluster-down: delete the kind cluster `andara` and no other — CONFIRM=andara
cluster-down:
	@$(PY) $(SCRIPTS)/cluster.py down "$(CONFIRM)"

## cluster-rebuild: export the certificate Secrets to .local/tls/, delete `andara`, create it again, then platform-up (which restores the CA root and installs the collectors with the telemetry Secret), then shared-up — CONFIRM=andara
cluster-rebuild:
	@$(PY) $(SCRIPTS)/cluster.py rebuild "$(CONFIRM)"

## shared-up: the shared services in andara-shared — Kafka `andara-log` (3 brokers, KRaft, SASL/SCRAM with deny-by-default ACLs) and its KafkaUsers from topics.yaml, both environments' prefixed topics, the platform topics and the schema registry, then the object store and each environment's bucket and pair; labels andara-local and andara-dev; idempotent (AW-INF-055, AW-INF-056, AW-INF-057, AW-INF-058)
shared-up:
	@PY=$(PY) $(SCRIPTS)/kafka.sh up

## shared-down: delete the shared services and their data (every environment's Command history and snapshots) — CONFIRM=andara-shared (AW-INF-055, AW-INF-058)
shared-down:
	@$(PY) $(SCRIPTS)/shared.py down "$(CONFIRM)"

## shared-netcheck: prove the shared Kafka's NetworkPolicy with throwaway pods — andara pods in andara-local and andara-dev connect, one in an unlabelled namespace and an unlabelled one in andara-dev don't; cleans up (AW-INF-055 AC-5)
shared-netcheck:
	@$(PY) $(SCRIPTS)/shared.py netcheck

## shared-check: each andara-shared PVC's use (exit 1 over 80%) and each broker's bytes per environment prefix (AW-INF-055)
shared-check:
	@$(PY) $(SCRIPTS)/shared.py check

## kafka-install: the shared Kafka `andara-log` in andara-shared (as shared-up), then ENV's prefixed topics from deploy/kafka/topics.yaml — ENV=<local|dev> (AW-INF-014, AW-INF-055)
kafka-install:
	@PY=$(PY) $(SCRIPTS)/kafka.sh install "$(ENV)"

## kafka-broker-bounce: delete one shared broker and prove ENV's server never left service while it was gone — ENV=dev (local once AW-INF-060 puts its server on the broker) (AW-INF-014 AC-5)
kafka-broker-bounce:
	@PY=$(PY) $(SCRIPTS)/kafka.sh bounce "$(ENV)"

## kafka-users-check: the KafkaUsers rendered from deploy/kafka/topics.yaml grant no environment principal a topic Create, Alter or AlterConfigs, and no literal `*`; kafka.yaml has no unauthenticated listener and exactly the declared superuser, andara-operator, whose Secret no chart value names; every consumer group in a *_integration_test.go starts with `it-`, the only group prefix andara-it may use (AW-INF-055 AC-9, AW-INF-056 AC-8, AC-14, AW-INF-060 AC-12)
kafka-users-check:
	@$(PY) $(SCRIPTS)/kafka_users.py check
	@$(PY) $(SCRIPTS)/it_groups_check.py

## kafka-users: print the KafkaUser manifests rendered from deploy/kafka/topics.yaml (`make shared-up` applies them, AW-INF-056)
kafka-users:
	@$(PY) $(SCRIPTS)/kafka_users.py render

## kafka-creds: copy ENV's broker credentials (each principal's password, from the User Operator's Secret in andara-shared) into andara-ENV only, and remove a copy topics.yaml no longer declares; the only writer of those Secrets; exit 1 a source Secret missing, 2 usage, 3 no cluster — ENV=<local|dev> (AW-INF-056)
kafka-creds:
	@$(PY) $(SCRIPTS)/kafka_creds.py copy "$(ENV)"

## kafka-acl-probe: probe the shared broker's ACLs as every principal topics.yaml declares — own prefix allowed, the other environment's denied, no topic Create/Delete/AlterConfigs, only the projector writes the state topic, the projector never writes content or audit nor reads audit — leaving nothing behind; exit 1 on any answer other than declared (AW-INF-056 AC-2, AC-3, AC-12, AC-13)
kafka-acl-probe:
	@$(PY) $(SCRIPTS)/kafka_acl_probe.py

## kafka-creds-rotate: a new password for one principal (the User Operator writes it to the broker; the old one is refused), then kafka-creds; running workloads pick it up without a restart — ENV=<local|dev> PRINCIPAL=<andara-server|andara-projector-state>, or PRINCIPAL=<andara-operator|andara-schema-registry> with no ENV (the registry is restarted) (AW-INF-056)
kafka-creds-rotate:
	@$(PY) $(SCRIPTS)/kafka_creds.py rotate "$(if $(filter command line,$(origin ENV)),$(ENV),)" "$(PRINCIPAL)"

## argocd-install: Argo CD into `argocd` at its pinned version; with ENV=dev, dev's Secrets (if absent) and the andara-dev Application, which follows the Git tag `dev` and needs it to exist (AW-INF-019, AW-INF-059)
argocd-install:
	@$(PY) $(SCRIPTS)/argocd.py install "$(if $(filter command line,$(origin ENV)),$(ENV),)"

## promote: move dev to a main commit's image, or back to an earlier promotion — ENV=dev SHA=<main sha> | ENV=dev TAG=dev-<n> [PIN_ROUND=<round>]; one signed commit on release/dev, signed tags dev and dev-<n>, then waits for argocd-status to agree; Brian's shell only, refused under CLAUDECODE (AW-INF-059)
promote:
	@$(PY) $(SCRIPTS)/promote.py "$(if $(filter command line,$(origin ENV)),$(ENV))" "$(SHA)" "$(if $(filter command line,$(origin TAG)),$(TAG))" "$(PIN_ROUND)"

## promote-log: the ten newest promotions of dev, newest first, each dev-<n> tag's event record (time, main SHA, image digest, who, rollback markers) — read first when an SLI moved (AW-INF-059)
promote-log:
	@$(PY) $(SCRIPTS)/promote.py log

## objectstore-up: the shared snapshot object store in andara-shared — versitygw with its IAM, the operator-only root Secret, the andara-it user; idempotent; `make shared-up` runs it (AW-INF-058)
objectstore-up:
	@$(PY) $(SCRIPTS)/objectstore.py up

## objectstore-install: local|dev — the user andara-<env>, its bucket andara-snapshots-<env> on the shared gateway, and its pair in andara-<env>/andara-snapshot-s3; prod — its own versitygw in andara-prod (AW-INF-025); idempotent — ENV=<local|dev|prod> (AW-INF-058)
objectstore-install:
	@$(PY) $(SCRIPTS)/objectstore.py install "$(if $(filter command line,$(origin ENV)),$(ENV))"

## objectstore-check: prove the shared object store separates the environments — each pair does all four verbs on its own bucket and none on the other's, andara-it only on its andara-test-* bucket (AW-INF-058 AC-2, AC-3, AC-6)
objectstore-check:
	@$(PY) $(SCRIPTS)/objectstore.py check

## projector-stop: scale the state projector to 0 and wait for its consumer group to empty — ENV=<local|dev|prod> [PROJECTOR_STOP_TIMEOUT=120s] (AW-INF-025)
projector-stop:
	@$(PY) $(SCRIPTS)/projector.py stop "$(if $(filter command line,$(origin ENV)),$(ENV))"

## projector-start: scale the state projector to 1 and wait for Ready — ENV=<local|dev|prod> [PROJECTOR_START_TIMEOUT=300s] (AW-INF-025)
projector-start:
	@$(PY) $(SCRIPTS)/projector.py start "$(if $(filter command line,$(origin ENV)),$(ENV))"

## projector-rebuild: stop the state projector, run `andara-projector state --rebuild` as a one-shot Job until it catches up, then start it — ENV=<local|dev|prod> [PROJECTOR_REBUILD_TIMEOUT=30m] (AW-INF-025)
projector-rebuild:
	@$(PY) $(SCRIPTS)/projector.py rebuild "$(if $(filter command line,$(origin ENV)),$(ENV))"

## server-stop: suspend Argo CD's automated sync (none on local, which has no Application), scale the server to 0 and wait for the pod to go — ENV=<local|dev|prod> [SERVER_STOP_TIMEOUT=120s] (AW-INF-037)
server-stop:
	@$(PY) $(SCRIPTS)/server_ctl.py stop "$(if $(filter command line,$(origin ENV)),$(ENV))"

## server-start: scale the server to the chart's replica count, restore the sync policy server-stop suspended (none on local), wait for Ready — ENV=<local|dev|prod> [SERVER_START_TIMEOUT=300s] (AW-INF-037)
server-start:
	@$(PY) $(SCRIPTS)/server_ctl.py start "$(if $(filter command line,$(origin ENV)),$(ENV))"

## world-reset: recreate ENV's World log and Account store, keeping content — destroys every Character and Account — ENV=<env> CONFIRM=andara-<env> [BROKER=cluster, local only] (AW-INF-021, AW-INF-055)
world-reset:
	@$(PY) $(SCRIPTS)/world_reset.py "$(if $(filter command line,$(origin ENV)),$(ENV))" "$(CONFIRM)" $(if $(BROKER),--broker "$(BROKER)")

## env-destroy: delete ENV's namespace and wait until it's gone — Argo CD's Application (non-cascading), the object store, every Secret and PVC — then ENV's prefixed topics and consumer groups on the shared Kafka; the cluster-wide operators and the shared Kafka stay — ENV=<env> CONFIRM=andara-<env> (AW-INF-021 AC-7)
env-destroy:
	@$(PY) $(SCRIPTS)/env_destroy.py "$(if $(filter command line,$(origin ENV)),$(ENV))" "$(CONFIRM)"

## content-seed: publish and activate the dev fixture as pack town in ENV's content store — idempotent — needs the operator credential (ANDARA_BOOTSTRAP_OPERATOR) — ENV=<env> (AW-INF-021)
content-seed:
	@$(SCRIPTS)/content_seed.sh "$(if $(filter command line,$(origin ENV)),$(ENV))"

## env-recover: the M2 gate on ENV — SIGKILL the server container mid-play, recover from a snapshot within the RTO with a matching State Hash, both players rebind — kills ENV's live sessions; runs on the box — ENV=<env> CONFIRM=andara-<env> (AW-INF-034)
env-recover:
	@ENV_RECOVER_RTO=$(or $(ENV_RECOVER_RTO),120) $(PY) $(SCRIPTS)/env_recover.py "$(if $(filter command line,$(origin ENV)),$(ENV))" "$(CONFIRM)"

## argocd-status: the andara-dev Application's sync and health, and the three answers to "which image is dev" — the commit tag `dev` points at, Argo's synced revision, the digest andara-0 runs — exit 1 unless Synced/Healthy and all three agree (AW-INF-059)
argocd-status:
	@$(PY) $(SCRIPTS)/argocd.py status "$(if $(filter command line,$(origin ENV)),$(ENV),dev)"

## argocd-ui: port-forward the Argo CD UI to localhost (ARGOCD_UI_PORT, default 8090), and say where the admin password is
argocd-ui:
	@$(PY) $(SCRIPTS)/argocd.py ui

## argocd-recover: replace an andara pod stuck on a build that never became Ready, once a good build has synced (Kubernetes' forced rollback) — ENV=dev
argocd-recover:
	@$(PY) $(SCRIPTS)/argocd.py recover "$(ENV)"

## argocd-uninstall: remove the andara-dev Application without cascading and hand its resources back to Helm; `make helm-install ENV=dev TAG=<sha-…@sha256:…>` then works again — ENV=dev
argocd-uninstall:
	@$(PY) $(SCRIPTS)/argocd.py uninstall "$(ENV)"

## alerts-sync: the legacy ruler is frozen (AW-INF-047): refuses files/alerts.yaml, whose `# env:` markers make it the Grafana-managed rule set, with exit 1 before any key is read; an unmarked file is delivered to the Grafana Cloud ruler, namespace andara (MIMIR_API_KEY, the read key, and MIMIR_API_KEY_WRITE, used only when there is drift, from the environment, and MIMIR_ADDRESS/MIMIR_TENANT_ID or GRAFANA_CLOUD_PROM_URL/_USER; exits 3 without them) — ENV is accepted and ignored (AW-INF-009)
alerts-sync:
	@$(PY) $(SCRIPTS)/alerts_sync.py sync

## alerts-diff: show how the ruler differs from files/alerts.yaml; exits 1 on drift, 3 without the key, address or tenant; ALERTS_FILE=<path> diffs another rule file (AW-INF-009)
alerts-diff:
	@$(PY) $(SCRIPTS)/alerts_sync.py diff

## tf-plan: plan a Grafana Cloud Terraform root — STACK=solo7dev (solo7 and solo7-irm are CI's); GRAFANA_<STACK>_READ_TOKEN, SLACK_WEBHOOK_<DEV|PROD> and, for a root with a Fleet provider, GRAFANA_<STACK>_FLEET_READ_TOKEN from the environment or .local/box.env, exit 3 without them (AW-INF-046, AW-INF-051)
tf-plan:
	@$(PY) $(SCRIPTS)/tf.py plan --stack "$(STACK)"

## tf-apply: plan, apply that plan, then require an empty second plan — STACK=solo7dev; GRAFANA_<STACK>_APPLY_TOKEN, the webhook and GRAFANA_<STACK>_FLEET_APPLY_TOKEN, exit 3 without them; a held state lock exits 1 naming it (AW-INF-046)
tf-apply:
	@$(PY) $(SCRIPTS)/tf.py apply --stack "$(STACK)"

## tf-unlock: release a stale state lock — STACK=solo7dev LOCK_ID=<id> (the id tf-apply printed); see docs/runbooks/terraform-state.md
tf-unlock:
	@$(PY) $(SCRIPTS)/tf.py unlock --stack "$(STACK)" --lock-id "$(LOCK_ID)"

## tf-test-alert: create one always-firing alert labelled environment=ENV in STACK, to check its notification policy end to end (AC-7) — STACK=solo7dev ENV=<local|dev|staging>; GRAFANA_<STACK>_APPLY_TOKEN, exit 3 without it
tf-test-alert:
	@$(PY) $(SCRIPTS)/tf.py test-alert --stack "$(STACK)" --env "$(if $(filter command line,$(origin ENV)),$(ENV))"

## tf-test-alert-clean: remove the alert tf-test-alert created — STACK=solo7dev ENV=<local|dev|staging>
tf-test-alert-clean:
	@$(PY) $(SCRIPTS)/tf.py test-alert-clean --stack "$(STACK)" --env "$(if $(filter command line,$(origin ENV)),$(ENV))"

## tf-fmt-check: fail if terraform fmt would change anything under deploy/terraform
tf-fmt-check:
	@$(PY) $(SCRIPTS)/tf.py fmt-check

## tf-validate: terraform validate every Grafana Cloud root, no backend and no credential
tf-validate:
	@$(PY) $(SCRIPTS)/tf.py validate

## tf-test: terraform test every root against its committed terraform.tfvars, mock provider: policy routes, endpoints (AW-INF-046 AC-6, AC-10, AC-11); then a copy with tick-health.json broken each way the module's preconditions refuse (AW-INF-049 AC-2)
tf-test:
	@$(PY) $(SCRIPTS)/tf.py test

## tf-drift: compare a Grafana Cloud root with main and print any drift; exits 1 on drift — STACK=solo7dev (solo7 and solo7-irm are CI's) (AW-INF-068)
tf-drift:
	@$(PY) $(SCRIPTS)/tf_ci.py drift --stack "$(STACK)"

## tf-plan-show: print a plan-prod run's encrypted plan, decrypted in memory, with its sha256 — RUN=<run id> STACK=<solo7|solo7-irm>; TF_PLAN_KEY_PROD from .local/box.env (AW-INF-068)
tf-plan-show:
	@$(PY) $(SCRIPTS)/tf_ci.py plan-show --run "$(RUN)" --stack "$(STACK)"

## tf-ci-environments: report what the terraform workflow needs on GitHub (environments, reviewer, secret and variable names); APPLY=1 creates the environments, never a secret; with TF_PROD_ENABLED not 'true', the dev items only, and APPLY=1 refuses andara-prod-* (AW-INF-068)
tf-ci-environments:
	@$(PY) $(SCRIPTS)/tf_ci.py environments $(if $(filter 1,$(APPLY)),--apply)

## tf-replan: start the terraform workflow on main by hand: apply solo7dev, then (behind TF_PROD_ENABLED) plan-prod and apply-prod for main's head, replacing a superseded or cancelled prod plan; needs gh (AW-INF-068)
tf-replan:
	@gh workflow run terraform.yaml --ref main
	@echo "tf-replan: started the terraform workflow on main; gh run list --workflow terraform.yaml shows it"

## tf-plan-key: make TF_PLAN_KEY_PROD and set it in andara-prod-plan, andara-prod-apply and .local/box.env; ROTATE=1 replaces an existing one; refused while TF_PROD_ENABLED is not 'true' (AW-INF-068)
tf-plan-key:
	@$(PY) $(SCRIPTS)/tf_ci.py plan-key $(if $(filter 1,$(ROTATE)),--rotate)

## tf-credentials-check: list each hand-made Grafana Cloud credential's expiry from deploy/terraform/grafana/credentials.yaml; exits 1 when one is within its warn_days (default 14) or expired, or the file is unreadable; no network (AW-INF-076)
tf-credentials-check:
	@$(PY) $(SCRIPTS)/tf_credentials.py

## tf-workflow-check: actionlint the terraform and credentials workflows, then check terraform's credential, sink and concurrency rules (AW-INF-068 AC-9)
tf-workflow-check:
	@if ! command -v actionlint >/dev/null 2>&1; then echo "make: tf-workflow-check: actionlint not found (run \`make bootstrap\`)" >&2; exit 1; fi
	actionlint .github/workflows/terraform.yaml .github/workflows/credentials.yaml
	$(PY) $(SCRIPTS)/tf_workflow_check.py

## pipelines-check: every Fleet pipeline under deploy/terraform/grafana/pipelines validates (bin/alloy) and writes only to the chart's receiver: no secret, templatefile, other remote_write URL, otelcol exporter, loki.write, or sys.env outside env-allow.txt (AW-INF-051)
pipelines-check:
	@$(PY) $(SCRIPTS)/pipelines_check.py

## tf-lock: re-pin the grafana provider hashes in every root's .terraform.lock.hcl, for linux and darwin on amd64 and arm64
tf-lock:
	@$(PY) $(SCRIPTS)/tf.py lock

## tf-bootstrap-gcp: create or verify the andara-tfstate bucket, service accounts, IAM conditions and workload identity, seed the three states; idempotent, run once by the GCP project owner (GCP_STATE_PROJECT, GCP_DEVBOX_IDENTITY; docs/runbooks/terraform-state.md)
tf-bootstrap-gcp:
	@$(PY) $(SCRIPTS)/tf_bootstrap_gcp.py

## slo-report: render the 28-day SLIs of world-write-availability.md and kafka-availability.md into their measurement tables, reading solo7dev through gcx; no drill exclusion yet (AW-INF-081) (GRAFANA_SOLO7DEV_READ_TOKEN from the environment or .local/box.env; exits 3 without it) (AW-INF-039)
slo-report:
	@$(PY) $(SCRIPTS)/slo_report.py

## observe-check: ask ENV's Grafana Cloud stack, through gcx, whether andara-<env>'s metrics, logs and traces arrived; KEEP_LIST=1 checks every series the rules and the dashboard name instead (GRAFANA_<STACK>_READ_TOKEN from the environment or .local/box.env; exits 3 without it) — ENV=<local|dev|staging|prod> (AW-INF-075)
observe-check:
	@$(PY) $(SCRIPTS)/observe_check.py "$(ENV)" $(if $(filter 1,$(KEEP_LIST)),--keep-list)

## dashboard-check: evaluate every tick-health panel query for environment=ENV, namespace=andara-<env> in ENV's stack through gcx, polled to DEADLINE (default 300 s), and check the environment variable's options; exits 1 naming a panel with no series, 3 without GRAFANA_<STACK>_READ_TOKEN — ENV=<local|dev|staging|prod> (AW-INF-049)
dashboard-check:
	@$(PY) $(SCRIPTS)/dashboard_check.py "$(ENV)"

## gcx-config: write $GCX_CONFIG (default .local/gcx.yaml, or $RUNNER_TEMP/gcx.yaml in CI; mode 0600) with one gcx context per stack whose GRAFANA_<STACK>_READ_TOKEN is set; exits 3 with none (AW-INF-075)
gcx-config:
	@$(PY) $(SCRIPTS)/gcx.py config

## gcx-check: read STACK's rule groups with its read token, the check after rotating GRAFANA_<STACK>_READ_TOKEN; exits 1 when gcx is refused, 3 when the token is unset — STACK=<solo7dev|solo7> (AW-INF-076)
gcx-check:
	@$(PY) $(SCRIPTS)/gcx.py check "$(STACK)"

## gcx-only-check: fail if a script, the Makefile or a workflow calls a Grafana query or alerting API other than through gcx (AW-INF-075)
gcx-only-check:
	@$(PY) $(SCRIPTS)/gcx_only_check.py

## observe-unavailable: scale andara-dev's server to 0 and back, and assert through gcx that AndaraServerUnavailable fires for dev and clears, nothing else moving; refuses before cutover step 4(v) (GRAFANA_SOLO7DEV/SOLO7_READ_TOKEN, the MIMIR_* ruler read; AW-INF-069) — ENV=dev
observe-unavailable:
	@$(PY) $(SCRIPTS)/observe_unavailable.py "$(ENV)"

## stream-soak: hold a Subscribe through the edge for SOAK (default 5m), renewing the edge certificate mid-stream — ENV=<env> SOAK=<duration>; dials ANDARA_EDGE_PORT (default 8443, the box cluster's edge; CI sets its own)
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

## clean: remove build artifacts and local state, keeping .local/tls/ (the certificate exports a cluster rebuild restores)
clean:
	@rm -rf bin/ coverage.out
	if [[ -d .local ]]; then find .local -mindepth 1 -maxdepth 1 ! -name tls -exec rm -rf {} +; fi
	echo "clean: removed bin/, coverage.out and .local/ except .local/tls/"

## update-agents: update the vendored agents from a signed tag (make update-agents REF=<tag> [PR=0])
update-agents:
	@.claude/bin/update-agents "$(REF)" $(if $(filter 0,$(PR)),--no-pr)
