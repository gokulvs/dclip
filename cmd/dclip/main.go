package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gokulvs/dclip/internal/node"
	"github.com/spf13/cobra"
)

var (
	port    int
	connect []string // manual peer addresses
)

func init() {
	// Always log to stdout with a clean format (no date prefix clutter).
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ltime)
}

func main() {
	root := &cobra.Command{
		Use:   "dclip",
		Short: "dclip — distributed network clipboard over gRPC",
		Long: `dclip syncs your clipboard across machines on the same local network.

Run "dclip start" on each machine. Anything you copy is automatically
propagated to all peers discovered via mDNS (Bonjour/Avahi).

If mDNS is blocked by a firewall, specify peers manually:
  dclip start --connect 192.168.1.42:9090`,
	}

	root.PersistentFlags().IntVarP(&port, "port", "p", 9090, "gRPC listen port (0 = random)")
	root.PersistentFlags().StringArrayVarP(&connect, "connect", "c", nil,
		"manually connect to peer at host:port (repeatable, bypasses mDNS)")

	root.AddCommand(
		startCmd(),
		pushCmd(),
		pullCmd(),
		peersCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// startCmd starts the daemon: gRPC server + mDNS + clipboard watcher.
func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the dclip daemon (blocks until interrupted)",
		Example: `  dclip start
  dclip start --port 9191
  dclip start --connect 192.168.1.42:9090`,
		RunE: func(cmd *cobra.Command, args []string) error {
			n := node.New(port, connect...)

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			fmt.Printf("dclip node %s  port=%d\n", n.ID(), port)
			if len(connect) > 0 {
				for _, c := range connect {
					fmt.Printf("  seed peer: %s\n", c)
				}
			} else {
				fmt.Println("  peer discovery: mDNS (auto)")
				fmt.Println("  tip: if peers are not found, use --connect <ip:port>")
			}
			fmt.Println("Press Ctrl-C to stop.")
			fmt.Println()

			return n.Start(ctx)
		},
	}
}

// pushCmd pushes text or a file to the network clipboard.
func pushCmd() *cobra.Command {
	var filePath string

	cmd := &cobra.Command{
		Use:   "push [text]",
		Short: "Push text or a file to the network clipboard",
		Example: `  dclip push "hello world"
  dclip push -f /path/to/image.png`,
		RunE: func(cmd *cobra.Command, args []string) error {
			n := node.New(port, connect...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go func() { _ = n.Start(ctx) }()

			if filePath != "" {
				data, err := os.ReadFile(filePath)
				if err != nil {
					return fmt.Errorf("read file: %w", err)
				}
				n.PushFile(filepath.Base(filePath), data)
				fmt.Printf("pushed file %q (%d bytes)\n", filepath.Base(filePath), len(data))
			} else if len(args) > 0 {
				text := args[0]
				n.PushText(text)
				fmt.Printf("pushed text (%d bytes)\n", len(text))
			} else {
				return fmt.Errorf("provide text or use -f <file>")
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "path to file to push")
	return cmd
}

// pullCmd prints the current clipboard content from any reachable peer.
func pullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Pull the current clipboard from a peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("pull: not yet implemented — use 'start' for automatic sync")
			return nil
		},
	}
}

// peersCmd scans and lists discovered peers.
func peersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "peers",
		Short: "Scan and list peers on the local network",
		RunE: func(cmd *cobra.Command, args []string) error {
			n := node.New(port, connect...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go func() { _ = n.Start(ctx) }()

			fmt.Println("Scanning for 5 s… (Ctrl-C to stop early)")

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			select {
			case <-sigCh:
			case <-time.After(5 * time.Second):
			}

			peers := n.Peers()
			if len(peers) == 0 {
				fmt.Println("no peers found")
				return nil
			}
			fmt.Printf("found %d peer(s):\n", len(peers))
			for _, p := range peers {
				fmt.Printf("  %s\n", p)
			}
			return nil
		},
	}
}
