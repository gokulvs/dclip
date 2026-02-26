// Package node ties together the gRPC server, mDNS discovery, system clipboard
// monitoring, and outbound peer connections into a single P2P clipboard node.
package node

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"os"
	"path/filepath"

	"github.com/google/uuid"
	pb "github.com/gokulvs/dclip/gen/clipboard/v1"
	cliputil "github.com/gokulvs/dclip/internal/clipboard"
	"github.com/gokulvs/dclip/internal/discovery"
	"github.com/gokulvs/dclip/internal/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Node is a dclip P2P node.
type Node struct {
	id        string
	port      int
	seedPeers []string // manually specified peer addresses

	srv     *server.Server
	grpcSrv *grpc.Server
	mdnsSvc *discovery.Service

	peersMu sync.RWMutex
	peers   map[string]*peerConn // keyed by peer ID

	// clipMu protects both echo-suppression and stale-content tracking fields.
	clipMu       sync.Mutex
	lastNetTS    int64 // when we last wrote to clipboard from network (echo suppression)
	currentClipTS int64 // timestamp of most recently applied clipboard content
}

type peerConn struct {
	id     string
	addr   string
	conn   *grpc.ClientConn
	cancel context.CancelFunc
}

// New creates a Node with a random UUID, the given listen port, and optional
// seed peer addresses (host:port) that bypass mDNS discovery.
func New(port int, seeds ...string) *Node {
	return &Node{
		id:        uuid.New().String(),
		port:      port,
		seedPeers: seeds,
		peers:     make(map[string]*peerConn),
	}
}

// ID returns the node's UUID.
func (n *Node) ID() string { return n.id }

// Port returns the actual listen port (useful when port was 0).
func (n *Node) Port() int { return n.port }

// Start launches the gRPC server, mDNS registration, peer discovery loop,
// and local clipboard monitor. It blocks until ctx is cancelled.
func (n *Node) Start(ctx context.Context) error {
	// Initialise the system clipboard.
	if err := cliputil.Init(); err != nil {
		return fmt.Errorf("clipboard init: %w", err)
	}

	// Start gRPC server.
	n.srv = server.New()
	n.grpcSrv = grpc.NewServer()
	pb.RegisterClipboardServiceServer(n.grpcSrv, n.srv)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", n.port))
	if err != nil {
		return fmt.Errorf("listen :%d: %w", n.port, err)
	}
	// If port was 0, learn the actual port.
	n.port = lis.Addr().(*net.TCPAddr).Port

	go func() {
		if err := n.grpcSrv.Serve(lis); err != nil {
			log.Printf("[node] gRPC server stopped: %v", err)
		}
	}()

	// Register via mDNS.
	n.mdnsSvc, err = discovery.Register(n.id, n.port)
	if err != nil {
		// Non-fatal: log and continue — manual seeds still work.
		log.Printf("[node] mDNS register failed (continuing): %v", err)
	}

	// Print startup summary.
	log.Printf("[node] id=%s port=%d", n.id, n.port)
	for _, ip := range localIPs() {
		log.Printf("[node] listening on %s:%d", ip, n.port)
	}

	// Dial seed peers (manual --connect addresses).
	for _, addr := range n.seedPeers {
		a := addr // capture
		go n.dialAddr(ctx, "seed-"+uuid.New().String(), a)
	}

	// Browse for mDNS peers.
	foundPeers := make(chan discovery.Peer, 16)
	go func() {
		for {
			discovery.Browse(ctx, n.id, foundPeers)
			if ctx.Err() != nil {
				return
			}
			time.Sleep(3 * time.Second)
		}
	}()

	// Connect to discovered peers.
	go n.handleDiscoveredPeers(ctx, foundPeers)

	// Apply clipboard content pushed to us by remote peers.
	go n.watchIncomingPushes(ctx)

	// Monitor local clipboard changes.
	go n.watchLocalClipboard(ctx)

	// Monitor local file copies (Finder/Explorer Cmd+C / Ctrl+C on files).
	go n.watchLocalFiles(ctx)

	// Periodic heartbeat so the user can see the node is alive.
	go n.heartbeat(ctx)

	<-ctx.Done()
	n.shutdown()
	return nil
}

// PushText pushes text to all known peers and broadcasts it locally.
func (n *Node) PushText(text string) {
	content := &pb.ClipboardContent{
		Type:      pb.ContentType_CONTENT_TYPE_TEXT,
		Data:      []byte(text),
		SenderId:  n.id,
		Timestamp: time.Now().UnixNano(),
	}
	n.pushToPeers(context.Background(), content)
	n.srv.Broadcast(content)
}

// PushFile pushes a named file to all peers.
func (n *Node) PushFile(filename string, data []byte) {
	content := &pb.ClipboardContent{
		Type:      pb.ContentType_CONTENT_TYPE_FILE,
		Data:      data,
		Filename:  filename,
		SenderId:  n.id,
		Timestamp: time.Now().UnixNano(),
	}
	n.pushToPeers(context.Background(), content)
	n.srv.Broadcast(content)
}

// Peers returns a snapshot of currently connected peer addresses.
func (n *Node) Peers() []string {
	n.peersMu.RLock()
	defer n.peersMu.RUnlock()
	addrs := make([]string, 0, len(n.peers))
	for _, p := range n.peers {
		addrs = append(addrs, fmt.Sprintf("%s (%s)", p.id[:8], p.addr))
	}
	return addrs
}

// handleDiscoveredPeers dials each newly found mDNS peer.
func (n *Node) handleDiscoveredPeers(ctx context.Context, peers <-chan discovery.Peer) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-peers:
			n.peersMu.RLock()
			_, exists := n.peers[p.ID]
			n.peersMu.RUnlock()
			if exists {
				continue
			}
			go n.dialPeer(ctx, p)
		}
	}
}

// dialPeer tries each address advertised by a peer (in order) and connects to
// the first one that succeeds. This handles Windows machines that advertise
// multiple IPs including non-routable virtual adapters (Hyper-V, WSL2, VPN).
func (n *Node) dialPeer(ctx context.Context, p discovery.Peer) {
	addrs := p.Addrs()
	for i, addr := range addrs {
		log.Printf("[node] trying peer %s address %d/%d: %s", p.ID[:8], i+1, len(addrs), addr)

		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Printf("[node] address %s: NewClient error: %v", addr, err)
			continue
		}

		// Probe connectivity with a real RPC.
		probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = pb.NewClipboardServiceClient(conn).Pull(probeCtx, &pb.PullRequest{})
		probeCancel()
		if err != nil {
			conn.Close()
			log.Printf("[node] address %s unreachable: %v", addr, err)
			continue
		}

		// Connected — hand off to the subscription loop.
		pCtx, cancel := context.WithCancel(ctx)
		pc := &peerConn{id: p.ID, addr: addr, conn: conn, cancel: cancel}

		n.peersMu.Lock()
		n.peers[p.ID] = pc
		n.peersMu.Unlock()

		log.Printf("[node] connected to peer %s via %s", p.ID[:8], addr)
		n.subscribeToPeer(pCtx, pc)
		return
	}
	log.Printf("[node] peer %s unreachable on all %d address(es)", p.ID[:8], len(addrs))
}

// dialAddr dials a seed peer by address and keeps reconnecting indefinitely
// until ctx is cancelled. Used for manually specified --connect peers.
func (n *Node) dialAddr(ctx context.Context, id, addr string) {
	for {
		if ctx.Err() != nil {
			return
		}

		log.Printf("[node] dialing seed peer %s at %s", id[:8], addr)

		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Printf("[node] seed peer %s NewClient error: %v — retry in 10s", addr, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}

		// Probe connectivity with a real RPC.
		probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
		_, err = pb.NewClipboardServiceClient(conn).Pull(probeCtx, &pb.PullRequest{})
		probeCancel()
		if err != nil {
			conn.Close()
			log.Printf("[node] seed peer %s unreachable: %v — retry in 10s", addr, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}

		pCtx, cancel := context.WithCancel(ctx)
		pc := &peerConn{id: id, addr: addr, conn: conn, cancel: cancel}

		n.peersMu.Lock()
		n.peers[id] = pc
		n.peersMu.Unlock()

		log.Printf("[node] seed peer connected: %s (%s)", id[:8], addr)
		n.subscribeToPeer(pCtx, pc) // blocks until disconnected

		// subscribeToPeer returned — peer went away. Loop to reconnect.
		log.Printf("[node] seed peer %s gone — reconnecting in 5s", addr)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (n *Node) subscribeToPeer(ctx context.Context, pc *peerConn) {
	defer func() {
		n.peersMu.Lock()
		delete(n.peers, pc.id)
		n.peersMu.Unlock()
		pc.conn.Close()
		log.Printf("[node] peer %s (%s) disconnected", pc.id[:8], pc.addr)
	}()

	client := pb.NewClipboardServiceClient(pc.conn)

	for {
		if ctx.Err() != nil {
			return
		}

		stream, err := client.Subscribe(ctx, &pb.SubscribeRequest{SubscriberId: n.id})
		if err != nil {
			log.Printf("[node] subscribe to %s failed: %v — retry in 5s", pc.id[:8], err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		log.Printf("[node] subscribed to peer %s", pc.id[:8])

		for {
			event, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("[node] stream from %s closed: %v — retry in 3s", pc.id[:8], err)
				break
			}
			if event.Content != nil {
				n.applyRemoteContent(event.Content)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// watchIncomingPushes reads content that remote peers sent via the Push RPC
// and writes it to the local system clipboard.
// This is the primary sync path: peer A changes clipboard → calls Push on our
// gRPC server → server enqueues on pushNotify → we apply it here.
func (n *Node) watchIncomingPushes(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case content, ok := <-n.srv.IncomingPushes():
			if !ok {
				return
			}
			n.applyRemoteContent(content)
		}
	}
}

// applyRemoteContent writes received content to the local system clipboard.
func (n *Node) applyRemoteContent(c *pb.ClipboardContent) {
	// Drop content that originated from this node (echo from Subscribe fanout).
	if c.SenderId == n.id {
		return
	}

	// Drop stale content — keeps the newest copy if two paths deliver the same
	// event (e.g. both Push and Subscribe-initial-sync on reconnect).
	n.clipMu.Lock()
	if c.Timestamp > 0 && c.Timestamp <= n.currentClipTS {
		n.clipMu.Unlock()
		return
	}
	n.currentClipTS = c.Timestamp
	n.lastNetTS = time.Now().UnixNano()
	n.clipMu.Unlock()

	senderShort := c.SenderId
	if len(senderShort) > 8 {
		senderShort = senderShort[:8]
	}
	log.Printf("[node] received %s from %s (%d bytes)", c.Type, senderShort, len(c.Data))

	switch c.Type {
	case pb.ContentType_CONTENT_TYPE_TEXT:
		cliputil.WriteText(c.Data)
	case pb.ContentType_CONTENT_TYPE_IMAGE:
		cliputil.WriteImage(c.Data)
	case pb.ContentType_CONTENT_TYPE_FILE:
		path, err := cliputil.WriteFile(c.Filename, c.Data)
		if err != nil {
			log.Printf("[node] save file: %v", err)
		} else {
			log.Printf("[node] file saved to %s", path)
			// Put the saved path in the text clipboard so the user can paste it.
			cliputil.WriteText([]byte(path))
		}
	}
}

// watchLocalClipboard monitors the system clipboard and pushes changes to peers.
func (n *Node) watchLocalClipboard(ctx context.Context) {
	textCh := cliputil.WatchText(ctx)
	imgCh := cliputil.WatchImage(ctx)

	if textCh == nil && imgCh == nil {
		log.Printf("[node] WARNING: clipboard Watch returned nil channels — local changes will not be detected")
		return
	}

	log.Printf("[node] watching local clipboard for changes")

	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-textCh:
			if !ok {
				log.Printf("[node] text clipboard watch channel closed")
				return
			}
			if n.recentNetWrite() {
				continue
			}
			content := &pb.ClipboardContent{
				Type:      pb.ContentType_CONTENT_TYPE_TEXT,
				Data:      data,
				SenderId:  n.id,
				Timestamp: time.Now().UnixNano(),
			}
			n.updateLocalClipTS(content.Timestamp)
			log.Printf("[node] local text changed (%d bytes) — pushing to %d peer(s)", len(data), n.peerCount())
			// Broadcast updates s.current so reconnecting peers get this via
			// Subscribe initial-sync. pushToPeers delivers to active peers now.
			n.srv.Broadcast(content)
			n.pushToPeers(ctx, content)
		case data, ok := <-imgCh:
			if !ok {
				log.Printf("[node] image clipboard watch channel closed")
				return
			}
			if n.recentNetWrite() {
				continue
			}
			content := &pb.ClipboardContent{
				Type:      pb.ContentType_CONTENT_TYPE_IMAGE,
				Data:      data,
				MimeType:  "image/png",
				SenderId:  n.id,
				Timestamp: time.Now().UnixNano(),
			}
			n.updateLocalClipTS(content.Timestamp)
			log.Printf("[node] local image changed (%d bytes) — pushing to %d peer(s)", len(data), n.peerCount())
			n.srv.Broadcast(content)
			n.pushToPeers(ctx, content)
		}
	}
}

// watchLocalFiles monitors the system clipboard for file copies (e.g. Cmd+C in
// Finder) and pushes each file to all peers.
func (n *Node) watchLocalFiles(ctx context.Context) {
	ch := cliputil.WatchFiles(ctx)
	if ch == nil {
		log.Printf("[node] file clipboard watch not supported on this platform")
		return
	}
	log.Printf("[node] watching local clipboard for file changes")
	for {
		select {
		case <-ctx.Done():
			return
		case paths, ok := <-ch:
			if !ok {
				return
			}
			if n.recentNetWrite() {
				continue
			}
			n.pushLocalFiles(ctx, paths)
		}
	}
}

const maxFileSize = 50 * 1024 * 1024 // 50 MB

// pushLocalFiles reads each file from disk and pushes it to all peers.
func (n *Node) pushLocalFiles(ctx context.Context, paths []string) {
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			log.Printf("[node] file read error %s: %v", p, err)
			continue
		}
		if len(data) > maxFileSize {
			log.Printf("[node] file too large to sync: %s (%d MB)", p, len(data)/(1024*1024))
			continue
		}
		filename := filepath.Base(p)
		content := &pb.ClipboardContent{
			Type:      pb.ContentType_CONTENT_TYPE_FILE,
			Data:      data,
			Filename:  filename,
			SenderId:  n.id,
			Timestamp: time.Now().UnixNano(),
		}
		n.updateLocalClipTS(content.Timestamp)
		log.Printf("[node] local file copied: %s (%d bytes) — pushing to %d peer(s)", filename, len(data), n.peerCount())
		n.srv.Broadcast(content)
		n.pushToPeers(ctx, content)
	}
}

// recentNetWrite returns true if we wrote to the system clipboard within the
// last 500 ms (to avoid re-broadcasting content we just received).
func (n *Node) recentNetWrite() bool {
	n.clipMu.Lock()
	ts := n.lastNetTS
	n.clipMu.Unlock()
	return time.Now().UnixNano()-ts < int64(500*time.Millisecond)
}

// updateLocalClipTS records that the local user (not the network) just changed
// the clipboard at time ts, so applyRemoteContent won't overwrite it with older
// network content.
func (n *Node) updateLocalClipTS(ts int64) {
	n.clipMu.Lock()
	if ts > n.currentClipTS {
		n.currentClipTS = ts
	}
	n.clipMu.Unlock()
}

// pushToPeers sends content to all connected peers via gRPC Push.
func (n *Node) pushToPeers(ctx context.Context, content *pb.ClipboardContent) {
	n.peersMu.RLock()
	pcs := make([]*peerConn, 0, len(n.peers))
	for _, pc := range n.peers {
		pcs = append(pcs, pc)
	}
	n.peersMu.RUnlock()

	if len(pcs) == 0 {
		return
	}

	for _, pc := range pcs {
		go func(pc *peerConn) {
			client := pb.NewClipboardServiceClient(pc.conn)
			ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if _, err := client.Push(ctx2, &pb.PushRequest{Content: content}); err != nil {
				log.Printf("[node] push to %s failed: %v", pc.id[:8], err)
			} else {
				log.Printf("[node] pushed to %s ok", pc.id[:8])
			}
		}(pc)
	}
}

func (n *Node) peerCount() int {
	n.peersMu.RLock()
	defer n.peersMu.RUnlock()
	return len(n.peers)
}

// heartbeat logs a periodic status line so the user knows the node is alive.
func (n *Node) heartbeat(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			log.Printf("[node] alive — %d peer(s) connected", n.peerCount())
		}
	}
}

func (n *Node) shutdown() {
	if n.mdnsSvc != nil {
		n.mdnsSvc.Shutdown()
	}
	n.grpcSrv.GracefulStop()

	n.peersMu.Lock()
	for _, pc := range n.peers {
		pc.cancel()
		pc.conn.Close()
	}
	n.peersMu.Unlock()

	log.Printf("[node] shutdown complete")
}

// localIPs returns all non-loopback IPv4 addresses on this machine.
func localIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() == nil {
				continue
			}
			ips = append(ips, ip.String())
		}
	}
	return ips
}
