BINARY   := dclip
MODULE   := github.com/gokulvs/dclip
PROTO    := proto/clipboard.proto
GEN_DIR  := gen/clipboard/v1
CMD      := ./cmd/dclip
DIST     := dist
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  := -ldflags "-X main.version=$(VERSION) -s -w"

# Cross-compilers used when building Linux targets from macOS.
# Install via: brew install FiloSottile/musl-cross/musl-cross
LINUX_CC_AMD64  ?= x86_64-linux-musl-gcc
LINUX_CC_ARM64  ?= aarch64-linux-musl-gcc

.PHONY: all build dist \
        build-mac-arm64 build-mac-amd64 \
        build-linux-amd64 build-linux-arm64 \
        build-windows-amd64 build-windows-arm64 \
        install proto clean

all: build

## Build native binary into ./dclip  (matches current OS/arch)
build:
	go build $(LDFLAGS) -o $(BINARY) $(CMD)

## Build release binaries for all platforms into ./dist/
dist:
	@mkdir -p $(DIST)
	@$(MAKE) build-mac-arm64 build-mac-amd64 \
	         build-linux-amd64 build-linux-arm64 \
	         build-windows-amd64 build-windows-arm64
	@echo ""
	@echo "Release binaries:"
	@ls -lh $(DIST)/

# ── macOS ─────────────────────────────────────────────────────────────────────
# CGO is required by golang.design/x/clipboard on macOS (uses Objective-C).
# Both arches can be cross-compiled from an Apple Silicon or Intel Mac using
# the system clang that ships with Xcode.

build-mac-arm64:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
	go build $(LDFLAGS) -o $(DIST)/$(BINARY)-darwin-arm64 $(CMD)
	@echo "built $(DIST)/$(BINARY)-darwin-arm64"

build-mac-amd64:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
	go build $(LDFLAGS) -o $(DIST)/$(BINARY)-darwin-amd64 $(CMD)
	@echo "built $(DIST)/$(BINARY)-darwin-amd64"

# ── Linux ─────────────────────────────────────────────────────────────────────
# golang.design/x/clipboard uses CGO + X11 on Linux.
# Native (on a Linux host):  CC is picked up automatically; no override needed.
# Cross (from macOS):        requires musl-cross or gnu-cross toolchain.
#   brew install FiloSottile/musl-cross/musl-cross
# Override the compiler via: make build-linux-amd64 LINUX_CC_AMD64=x86_64-linux-gnu-gcc

build-linux-amd64:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC=$(LINUX_CC_AMD64) \
	go build $(LDFLAGS) -o $(DIST)/$(BINARY)-linux-amd64 $(CMD)
	@echo "built $(DIST)/$(BINARY)-linux-amd64"

build-linux-arm64:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=$(LINUX_CC_ARM64) \
	go build $(LDFLAGS) -o $(DIST)/$(BINARY)-linux-arm64 $(CMD)
	@echo "built $(DIST)/$(BINARY)-linux-arm64"

# ── Windows ───────────────────────────────────────────────────────────────────
# golang.design/x/clipboard uses pure Windows syscalls — no CGO needed.

build-windows-amd64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
	go build $(LDFLAGS) -o $(DIST)/$(BINARY)-windows-amd64.exe $(CMD)
	@echo "built $(DIST)/$(BINARY)-windows-amd64.exe"

build-windows-arm64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 \
	go build $(LDFLAGS) -o $(DIST)/$(BINARY)-windows-arm64.exe $(CMD)
	@echo "built $(DIST)/$(BINARY)-windows-arm64.exe"

# ── Helpers ───────────────────────────────────────────────────────────────────

## Install native binary into $GOPATH/bin
install:
	go install $(CMD)

## Regenerate gRPC code from proto/clipboard.proto
proto:
	protoc \
		--go_out=$(GEN_DIR) \
		--go_opt=paths=source_relative \
		--go-grpc_out=$(GEN_DIR) \
		--go-grpc_opt=paths=source_relative \
		-I proto \
		$(PROTO)

## Remove built binaries
clean:
	rm -f $(BINARY)
	rm -rf $(DIST)
