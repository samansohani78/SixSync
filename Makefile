# SixSync development tasks.
#
# Go is expected on PATH. If it is not, this repository was developed against
# Go 1.27.0 installed to /usr/local/go.
GO ?= go

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

PKG := github.com/samansohani78/SixSync
LDFLAGS := -X $(PKG)/internal/buildinfo.version=$(VERSION) \
           -X $(PKG)/internal/buildinfo.commit=$(COMMIT) \
           -X $(PKG)/internal/buildinfo.date=$(DATE)

.PHONY: all
all: fmt-check vet test build

.PHONY: build
build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/sixsync ./cmd/sixsync

.PHONY: test
test:
	$(GO) test ./...

.PHONY: test-race
test-race:
	$(GO) test -race ./...

.PHONY: cover
cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: fmt
fmt:
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l . | grep -v '^third_party/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: lint
lint:
	golangci-lint run

.PHONY: vulncheck
vulncheck:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

# --- Integration environment -------------------------------------------------
# A disposable APISIX pinned to an exact version. Never point these at anything
# you care about: the stack has a wide-open admin allowlist and published keys.

COMPOSE := docker compose -f deploy/compose/docker-compose.yml
APISIX_VERSION ?= 3.18.0
ADMIN_PORT ?= 9318
PROXY_PORT ?= 9418
ETCD_PORT ?= 2318
ETCD_PEER_PORT ?= 2418
CONTROL_PORT ?= 9018
COMPOSE_ENV := APISIX_VERSION=$(APISIX_VERSION) ADMIN_PORT=$(ADMIN_PORT) \
               PROXY_PORT=$(PROXY_PORT) ETCD_PORT=$(ETCD_PORT) ETCD_PEER_PORT=$(ETCD_PEER_PORT) \
               CONTROL_PORT=$(CONTROL_PORT)

.PHONY: env-up
env-up:
	$(COMPOSE_ENV) $(COMPOSE) up -d
	@echo "Waiting for the Admin API on :$(ADMIN_PORT)..."
	@for i in $$(seq 1 60); do \
		if curl -sf -o /dev/null --max-time 2 \
			-H "X-API-KEY: sixsync-test-admin-key-not-secret" \
			http://127.0.0.1:$(ADMIN_PORT)/apisix/admin/routes; then \
			echo "ready"; exit 0; \
		fi; sleep 2; \
	done; echo "timed out waiting for APISIX"; exit 1

.PHONY: env-down
env-down:
	$(COMPOSE_ENV) $(COMPOSE) down -v

.PHONY: test-integration
test-integration:
	SIXSYNC_TEST_ADMIN_URL=http://127.0.0.1:$(ADMIN_PORT) \
	SIXSYNC_TEST_APISIX_VERSION=$(APISIX_VERSION) \
	SIXSYNC_TEST_ADMIN_KEY=sixsync-test-admin-key-not-secret \
	SIXSYNC_TEST_VIEWER_KEY=sixsync-test-viewer-key-not-secret \
	$(GO) test -tags integration -count=1 ./test/integration/ -v

.PHONY: compat-matrix
compat-matrix:
	./scripts/compat-matrix.sh

.PHONY: test-perf
test-perf:
	$(GO) test -tags perf -count=1 -timeout 20m ./test/perf/ -v -run TestScale

.PHONY: bench
bench:
	$(GO) test -tags perf -bench . -benchmem -run "^$$" ./test/perf/

.PHONY: fuzz
fuzz:
	@for pkg in canonical journal plan redact resource; do \
		for target in $$($(GO) test ./internal/$$pkg/ -list 'Fuzz.*' 2>/dev/null | grep '^Fuzz' || true); do \
			printf '  %-24s ' "$$target"; \
			$(GO) test ./internal/$$pkg/ -run '^$$' -fuzz "^$$target$$" -fuzztime $(FUZZTIME) \
				2>&1 | grep -E '^(ok|FAIL)' | tail -1; \
		done; \
	done

FUZZTIME ?= 30s

.PHONY: clean
clean:
	rm -rf dist coverage.out
