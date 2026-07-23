# Canopy Makefile
#
# Targets:
#   build              — Build the canopyd binary
#   build-embed        — Build with ldflags version injection
#   test               — Run tests
#   test-short         — Run tests skipping integration
#   vet                — Run go vet
#   lint               — Run golangci-lint
#   tidy               — Tidy go.mod/go.sum
#   clean              — Remove build artifacts

GO       ?= go
BIN_DIR  ?= bin
BINARY   ?= canopyd
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS   = -ldflags="-X main.version=$(VERSION)"

.PHONY: all build build-embed test test-short vet lint tidy clean

all: build test vet lint

build:
	$(GO) build -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)

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
	$(GO) test ./... -count=1 -timeout=120s

test-short:
	$(GO) test ./... -short -count=1 -timeout=60s

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./... --timeout=3m

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)/
