# dclip — distributed network clipboard

`dclip` syncs your clipboard across machines on the **same local network** using
peer-to-peer gRPC and mDNS auto-discovery. No configuration required — just run
it on each machine and start copying.

## Features

- **Zero-config**: peers are discovered automatically via mDNS (Bonjour / Avahi)
- **gRPC**: efficient binary transport with server-streaming subscriptions
- **Content types**: plain text, images, and arbitrary files
- **macOS / Linux / Windows**: backed by [`golang.design/x/clipboard`](https://pkg.go.dev/golang.design/x/clipboard)

## Installation

```bash
# From source
git clone <repo>
cd dclip
make install       # installs to $GOPATH/bin
```

## Usage

### Start the daemon (automatic sync)

```bash
dclip start               # listens on :9090
dclip start --port 9191   # custom port
```

Run this on every machine you want in the clipboard group. Anything you copy
will be pushed to all peers instantly.

### Push manually

```bash
# Push text
dclip push "hello from machine A"

# Push a file
dclip push -f ~/Documents/notes.md

# Push an image
dclip push -f ~/Desktop/screenshot.png
```

### List peers

```bash
dclip peers
```

## Architecture

```
Machine A                              Machine B
┌─────────────────────────┐           ┌─────────────────────────┐
│  dclip node             │           │  dclip node             │
│  ┌──────────────────┐   │  gRPC     │  ┌──────────────────┐   │
│  │ gRPC server      │◄──┼───────────┼──│ gRPC client      │   │
│  │ ClipboardService │   │           │  └──────────────────┘   │
│  └────────┬─────────┘   │           │  ┌──────────────────┐   │
│           │ subscribe   │  gRPC     │  │ gRPC server      │──►│
│  ┌────────▼─────────┐   │◄──────────┼──│ ClipboardService │   │
│  │ mDNS discovery   │   │           │  └──────────────────┘   │
│  └──────────────────┘   │           │  ┌──────────────────┐   │
│  ┌──────────────────┐   │           │  │ mDNS discovery   │   │
│  │ clipboard watcher│   │           │  └──────────────────┘   │
│  └──────────────────┘   │           │  ┌──────────────────┐   │
└─────────────────────────┘           │  │ clipboard watcher│   │
                                      │  └──────────────────┘   │
                                      └─────────────────────────┘
```

Each node:
1. **Registers** itself on the LAN via `_dclip._tcp` mDNS
2. **Browses** for other `_dclip._tcp` nodes and dials them
3. **Subscribes** to their clipboard event streams
4. **Watches** the local system clipboard for changes
5. On local change → **Push** to all peers
6. On remote push → **Write** to the local system clipboard

## Project layout

```
dclip/
├── cmd/dclip/          # CLI entry point (cobra)
├── proto/              # Protobuf definitions
├── gen/clipboard/v1/   # Generated gRPC Go code
├── internal/
│   ├── server/         # gRPC ClipboardService implementation
│   ├── discovery/      # mDNS registration and browsing
│   ├── node/           # P2P node (ties everything together)
│   └── clipboard/      # File-save helper for received files
└── Makefile
```

## Regenerating gRPC code

```bash
make proto
```

Requires `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` on `$PATH`.
