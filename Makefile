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

.PHONY: env-up
env-up:
	$(COMPOSE) up -d
	@echo "Waiting for the Admin API..."
	@for i in $$(seq 1 60); do \
		if curl -sf -o /dev/null --max-time 2 \
			-H "X-API-KEY: sixsync-test-admin-key-not-secret" \
			http://127.0.0.1:9180/apisix/admin/routes; then \
			echo "ready"; exit 0; \
		fi; sleep 2; \
	done; echo "timed out waiting for APISIX"; exit 1

.PHONY: env-down
env-down:
	$(COMPOSE) down -v

.PHONY: test-integration
test-integration:
	SIXSYNC_TEST_ADMIN_URL=http://127.0.0.1:9180 \
	SIXSYNC_TEST_ADMIN_KEY=sixsync-test-admin-key-not-secret \
	SIXSYNC_TEST_VIEWER_KEY=sixsync-test-viewer-key-not-secret \
	$(GO) test -tags integration ./test/integration/ -v

.PHONY: clean
clean:
	rm -rf dist coverage.out
