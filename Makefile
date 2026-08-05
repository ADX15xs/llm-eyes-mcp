# llm-eyes-mcp Makefile
#
# The server is built with CGO_ENABLED=0 on purpose: it keeps the binary a
# single self-contained file (no libc, no DLL) and well under the 20 MB budget.
# Cross-compilation is then a one-liner with GOOS/GOARCH.

BINARY  := llm-eyes-mcp
PKG     := ./cmd/llm-eyes-mcp
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build windows linux darwin test clean run check

# Detect the host OS: Windows/mingw -> "windows", everything else -> "unix".
OS := $(if $(findstring Windows,$(OS) $(shell uname -s 2>/dev/null)),windows,unix)

# Native build for the host platform.
build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)$(if $(filter windows,$(OS)),.exe,) $(PKG)

# Cross-compiled release artefacts.
windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-windows-amd64.exe $(PKG)

linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 $(PKG)

darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-darwin-arm64 $(PKG)

BIN := bin/$(BINARY)$(if $(filter windows,$(OS)),.exe,)

# All three release binaries.
all: windows linux darwin

# Run the full test suite. -race is intentionally omitted: it requires a C
# compiler (CGO), which this zero-CGO project does not assume.
test:
	CGO_ENABLED=0 go test ./...

# Validate config + providers without starting the server.
check: build
	$(BIN) --check --config config.yml

# Start the server against config.yml (assumes `make build` ran).
run: build
	$(BIN) --config config.yml

clean:
	rm -rf bin
