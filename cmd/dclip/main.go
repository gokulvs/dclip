package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/gokulvs/dclip/internal/node"
	"github.com/spf13/cobra"
)

var (
	port    int
	verbose bool
)

func main() {
	root := &cobra.Command{
		Use:   "dclip",
		Short: "dclip — distributed network clipboard over gRPC",
		Long: `dclip syncs your clipboard across machines on the same local network.

Run "dclip start" on each machine. Anything you copy is automatically
propagated to all peers discovered via mDNS.`,
	}

	root.PersistentFlags().IntVarP(&port, "port", "p", 9090, "gRPC listen port (0 = random)")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose logging")

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
		RunE: func(cmd *cobra.Command, args []string) error {
			if !verbose {
				log.SetOutput(os.Stdout)
			}

			n := node.New(port)
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			fmt.Printf("dclip starting — node %s\n", n.ID())
			fmt.Println("Clipboard changes will be synced automatically. Press Ctrl-C to stop.")

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
			n := node.New(port)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Start the node in the background so peer connections are established.
			errCh := make(chan error, 1)
			go func() { errCh <- n.Start(ctx) }()

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
			fmt.Println("pull: not yet implemented (use 'start' for automatic sync)")
			return nil
		},
	}
}

// peersCmd lists currently connected peers (requires a running daemon).
func peersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "peers",
		Short: "List discovered peers on the local network",
		RunE: func(cmd *cobra.Command, args []string) error {
			n := node.New(port)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go func() { _ = n.Start(ctx) }()

			// Give mDNS a moment to discover peers.
			fmt.Println("Scanning for peers (3 s)…")

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			select {
			case <-sigCh:
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
