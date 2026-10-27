# Canopy Makefile
#
# Targets:
#   build              — Build the canopyd binary
#   build-embed        — Build with ldflags version injection
#   build-ui           — Build the SPA + canopyd with the UI embedded (-tags embedui)
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

.PHONY: all build deploy install-deploy-timer build-embed build-ui test test-short test-chaos-disconnect test-proxy validate-release-manifest-publication validate-wails-desktop test-wails-desktop-contract build-wails docs-check docs-check-test vet lint tidy clean run docker

all: build test vet lint

build:
	$(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)

docs-check:
	bash scripts/docs-check.sh

docs-check-test:
	bash scripts/docs-check-test.sh

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

# Single-process UI + API build: one binary serves the built Hermes Canopy SPA
# and the API on the same port. The SPA is compiled in under `-tags embedui`
# (internal/webui/spa_embed.go). go:embed cannot reference a path outside its
# own package directory, so the Vite output is staged at internal/webui/dist
# for the compile and removed again afterwards; a plain `make build` above is
# untouched, API-only and fast.
build-ui:
	npm --prefix frontend ci --no-audit --no-fund
	npm --prefix frontend run build
	rm -rf internal/webui/dist
	mkdir -p $(BIN_DIR)
	cp -R frontend/dist internal/webui/dist
	$(GO) build $(LDFLAGS) -tags embedui -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY); \
		status=$$?; rm -rf internal/webui/dist; exit $$status

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

# Offline release publication contract: checks updater defaults, workflow paths,
# override plumbing, and the stable gh-pages byte-copy contract.
validate-release-manifest-publication:
	python3 scripts/validate-release-manifest-publication.py

# FTR-06 Phase 5: Wails desktop packaging contract. Source-level validation of
# the nested desktop module, wails.json, Makefile wiring, updater feed, and the
# documented host boundary. No Wails download, no native build.
validate-wails-desktop:
	python3 scripts/validate-wails-desktop.py

# Host-independent build+test of the desktop shell: compiles the Wails
# application surface with the pure-Go server platform app (no WebView SDKs,
# no CGO). This is the CI-provable cell; the native build is build-wails.
test-wails-desktop-contract:
	cd cmd/canopy-desktop && CGO_ENABLED=0 go test -tags server,production ./... -count=1

# Native Wails build (FTR-06 Phase 5, mode B). Explicit and fail-loud: every
# prerequisite is checked first and a missing one aborts with the install
# action — never a silent fallback to the embed build. Requires the platform
# WebView SDKs; see docs/DESKTOP-WAILS.md for the per-OS list and for why
# signed .dmg/.msi output is NOT produced here.
WAILS3 ?= wails3
build-wails: validate-wails-desktop
	@command -v $(WAILS3) >/dev/null 2>&1 || { \
		echo "FAIL: $(WAILS3) not found on PATH."; \
		echo "  Install the Wails v3 CLI:  go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.34"; \
		echo "  (with CGO_ENABLED=0 the CLI itself builds on any host)"; \
		exit 1; }
	@command -v go >/dev/null 2>&1 || { echo "FAIL: go toolchain not found."; exit 1; }
	@if [ "$$(go env GOOS)" = "linux" ]; then \
		pkg-config --exists gtk4 webkitgtk-6.0 || { \
			echo "FAIL: Linux WebView SDKs missing (needs gtk4 + webkitgtk-6.0 dev packages)."; \
			echo "  Debian/Ubuntu: sudo apt install libgtk-4-dev libwebkitgtk-6.0-dev"; \
			echo "  The host-independent contract cell is 'make test-wails-desktop-contract'."; \
			exit 1; }; \
		pkg-config --exists libsoup-3.0 || { \
			echo "FAIL: libsoup-3.0 missing (WebKitGTK dependency)."; \
			echo "  Debian/Ubuntu: sudo apt install libsoup-3.0-dev"; \
			exit 1; }; \
	fi
	@cd cmd/canopy-desktop && CGO_ENABLED=0 go build -tags server,production -o ../../bin/canopy-desktop-server-check . \
		&& echo "OK: server-mode desktop binary compiled (host-independent cell)" \
		|| { echo "FAIL: server-mode desktop build failed."; exit 1; }
	@echo "NEXT: native window build needs the WebView SDKs above; on a provisioned host run:"
	@echo "  cd cmd/canopy-desktop && go build -tags production -o ../../bin/canopy-desktop ."
	@echo "  (packaging to .dmg/.msi and code signing are NOT part of this target; see docs/DESKTOP-WAILS.md)"

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./... --timeout=3m

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)/

docker:
	docker build -f deploy/Dockerfile -t hermes-canopy-canopyd \
		--build-arg CANOPY_VERSION=$(VERSION) \
		--build-arg CANOPY_COMMIT=$(COMMIT) \
		--build-arg CANOPY_BUILD_TIME=$(BUILD_TIME) .
