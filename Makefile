# Canopy Makefile
#
# Targets:
#   build              — Build the canopyd binary
#   build-embed        — Build with ldflags version injection
#   test               — Run tests
#   test-short         — Run tests skipping integration
#   test-chaos-disconnect — QA chaos-disconnect probe (runs test-short)
#   test-proxy         — Run the deploy/reference-proxy.py end-to-end suite
#   vet                — Run go vet
#   lint               — Run golangci-lint
#   tidy               — Tidy go.mod/go.sum
#   clean              — Remove build artifacts

GO       ?= go
BIN_DIR  ?= bin
BINARY   ?= canopyd
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT    ?= $(shell git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# R14-03 / GAP-100: every stamped build carries version + commit + build
# timestamp; the same three values are what `canopyd -version` prints and
# GET /version and GET /health serve.
LDFLAGS   = -ldflags="-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)"

# Dev defaults — match the Vite dev proxy target (frontend/vite.config.ts → :8091)
# and the docker-compose PostgreSQL host port (5437). Export your own values to override.
HTTP_ADDR ?= :8091
DB_PORT   ?= 5437

.PHONY: all build deploy install-deploy-timer build-embed test test-short test-chaos-disconnect test-proxy vet lint tidy clean run docker

all: build test vet lint

build:
	$(GO) build -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)

# Deploy to the live systemd user service (GAP-052): build from HEAD, install
# atomically to the unit's exec path, restart, health-poll, gateway smoke.
deploy:
	bash scripts/deploy-canopyd.sh

# Install + enable the hourly staleness check timer (GAP-067, GAP-070): renders
# this checkout's repo path into the user unit, daemon-reload, enable --now. The
# timer runs scripts/check-deploy-staleness.sh --deploy once per hour (GAP-070
# moved it off 24h); it refuses to auto-deploy from a dirty worktree
# (STALE_BLOCKED) and alerts the board when that refusal persists. Manual
# `make deploy` behavior is unchanged. Does not restart the live service.
install-deploy-timer:
	bash scripts/install-deploy-timer.sh

run: build
	HTTP_ADDR=$(HTTP_ADDR) DB_PORT=$(DB_PORT) $(BIN_DIR)/$(BINARY)

build-embed:
	$(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)

build-embed-linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY)_linux_amd64 ./cmd/$(BINARY)

build-embed-darwin-amd64:
	GOOS=darwin GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY)_darwin_amd64 ./cmd/$(BINARY)

build-embed-darwin-arm64:
	GOOS=darwin GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY)_darwin_arm64 ./cmd/$(BINARY)

build-embed-windows-amd64:
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY)_windows_amd64.exe ./cmd/$(BINARY)

build-embed-linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY)_linux_arm64 ./cmd/$(BINARY)

test:
	$(GO) test ./... -count=1 -timeout=600s

test-short:
	$(GO) test ./... -short -count=1 -timeout=480s

# QA chaos-disconnect probe (QA-CAN-004): explicit, stable target name for the
# disconnect-safety cell. That cell runs under a 120s disconnect window; the
# full non-short suite exceeds it (internal/db alone measured 135.3s), so the
# harness must drive the short-mode suite. Delegates to test-short as a pure
# prerequisite — single source of truth for the flags, no drift. The full
# suite remains `make test`.
test-chaos-disconnect: test-short

# Reference proxy end-to-end tests (DF-HERMES-CANOPY-7): starts the real
# deploy/reference-proxy.py as a subprocess in front of a throwaway stdlib
# upstream (no canopyd, no PostgreSQL, no host services) and asserts static
# assets, the SPA fallback, unauthenticated rejection, bearer injection/
# pass-through, upstream error preservation, incremental SSE and the startup
# refusals. python3 stdlib only — no pytest, no pip.
test-proxy:
	python3 -m unittest discover -s deploy/tests -v

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./... --timeout=3m

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)/

docker:
	docker build -f deploy/Dockerfile -t hermes-canopy-canopyd .
