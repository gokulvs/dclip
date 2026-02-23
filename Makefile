BINARY   := dclip
MODULE   := github.com/gokulvs/dclip
PROTO    := proto/clipboard.proto
GEN_DIR  := gen/clipboard/v1
CMD      := ./cmd/dclip
DIST     := dist
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  := -ldflags "-X main.version=$(VERSION) -s -w"

.PHONY: all build dist \
        build-mac-arm64 build-mac-amd64 \
        build-windows-amd64 build-windows-arm64 \
        install proto clean

all: build

## Build native binary into ./dclip  (matches current OS/arch)
build:
	go build $(LDFLAGS) -o $(BINARY) $(CMD)

## Build release binaries for all platforms into ./dist/
dist:
	@mkdir -p $(DIST)
	@$(MAKE) build-mac-arm64 build-mac-amd64 build-windows-amd64 build-windows-arm64
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
