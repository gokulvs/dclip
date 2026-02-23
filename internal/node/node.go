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

	"github.com/google/uuid"
	pb "github.com/gokulvs/dclip/gen/clipboard/v1"
	cliputil "github.com/gokulvs/dclip/internal/clipboard"
	"github.com/gokulvs/dclip/internal/discovery"
	"github.com/gokulvs/dclip/internal/server"
	xclip "golang.design/x/clipboard"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Node is a dclip P2P node.
type Node struct {
	id   string
	port int

	srv     *server.Server
	grpcSrv *grpc.Server
	mdnsSvc *discovery.Service

	peersMu sync.RWMutex
	peers   map[string]*peerConn // keyed by peer ID

	// suppress echoing back content we just received from the network
	lastNetMu sync.Mutex
	lastNetTS int64
}

type peerConn struct {
	id     string
	addr   string
	conn   *grpc.ClientConn
	cancel context.CancelFunc
}

// New creates a Node with a random UUID and the given listen port.
// Use port 0 to pick a random available port.
func New(port int) *Node {
	return &Node{
		id:    uuid.New().String(),
		port:  port,
		peers: make(map[string]*peerConn),
	}
}

// ID returns the node's UUID.
func (n *Node) ID() string { return n.id }

// Port returns the actual listen port (useful when port was 0).
func (n *Node) Port() int { return n.port }

// Start launches the gRPC server, mDNS registration, peer discovery loop,
// and local clipboard monitor. It blocks until ctx is cancelled.
func (n *Node) Start(ctx context.Context) error {
	// Initialise the system clipboard (required by golang.design/x/clipboard).
	if err := xclip.Init(); err != nil {
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
			log.Printf("gRPC server stopped: %v", err)
		}
	}()

	// Register via mDNS.
	n.mdnsSvc, err = discovery.Register(n.id, n.port)
	if err != nil {
		return fmt.Errorf("mDNS register: %w", err)
	}

	log.Printf("dclip node %s listening on :%d", n.id, n.port)

	// Browse for peers.
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

	// Monitor local clipboard changes.
	go n.watchLocalClipboard(ctx)

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

// handleDiscoveredPeers dials each newly found peer and subscribes to its events.
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
			go n.connectPeer(ctx, p)
		}
	}
}

func (n *Node) connectPeer(ctx context.Context, p discovery.Peer) {
	addr := p.Addr()
	log.Printf("connecting to peer %s at %s", p.ID[:8], addr)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("dial %s: %v", addr, err)
		return
	}

	pCtx, cancel := context.WithCancel(ctx)
	pc := &peerConn{id: p.ID, addr: addr, conn: conn, cancel: cancel}

	n.peersMu.Lock()
	n.peers[p.ID] = pc
	n.peersMu.Unlock()

	log.Printf("peer %s connected", p.ID[:8])

	// Subscribe to that peer's clipboard stream.
	n.subscribeToPeer(pCtx, pc)
}

func (n *Node) subscribeToPeer(ctx context.Context, pc *peerConn) {
	defer func() {
		n.peersMu.Lock()
		delete(n.peers, pc.id)
		n.peersMu.Unlock()
		pc.conn.Close()
		log.Printf("peer %s disconnected", pc.id[:8])
	}()

	client := pb.NewClipboardServiceClient(pc.conn)

	for {
		if ctx.Err() != nil {
			return
		}

		stream, err := client.Subscribe(ctx, &pb.SubscribeRequest{SubscriberId: n.id})
		if err != nil {
			log.Printf("subscribe %s: %v — retrying in 5s", pc.id[:8], err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		for {
			event, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("stream recv %s: %v", pc.id[:8], err)
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

// applyRemoteContent writes received content to the local system clipboard.
func (n *Node) applyRemoteContent(c *pb.ClipboardContent) {
	log.Printf("received clipboard from %s (type=%s, %d bytes)",
		c.SenderId[:8], c.Type, len(c.Data))

	// Record timestamp to suppress echo in watchLocalClipboard.
	n.lastNetMu.Lock()
	n.lastNetTS = time.Now().UnixNano()
	n.lastNetMu.Unlock()

	switch c.Type {
	case pb.ContentType_CONTENT_TYPE_TEXT:
		xclip.Write(xclip.FmtText, c.Data)
	case pb.ContentType_CONTENT_TYPE_IMAGE:
		xclip.Write(xclip.FmtImage, c.Data)
	case pb.ContentType_CONTENT_TYPE_FILE:
		path, err := cliputil.WriteFile(c.Filename, c.Data)
		if err != nil {
			log.Printf("save file: %v", err)
		} else {
			log.Printf("file saved to %s", path)
		}
	}
}

// watchLocalClipboard polls the system clipboard and pushes changes to peers.
func (n *Node) watchLocalClipboard(ctx context.Context) {
	textCh := xclip.Watch(ctx, xclip.FmtText)
	imgCh := xclip.Watch(ctx, xclip.FmtImage)

	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-textCh:
			if !ok {
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
			log.Printf("local text clipboard changed (%d bytes)", len(data))
			n.pushToPeers(ctx, content)
		case data, ok := <-imgCh:
			if !ok {
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
			log.Printf("local image clipboard changed (%d bytes)", len(data))
			n.pushToPeers(ctx, content)
		}
	}
}

// recentNetWrite returns true if we wrote to the system clipboard within the
// last 500 ms (to avoid re-broadcasting content we just received).
func (n *Node) recentNetWrite() bool {
	n.lastNetMu.Lock()
	ts := n.lastNetTS
	n.lastNetMu.Unlock()
	return time.Now().UnixNano()-ts < int64(500*time.Millisecond)
}

// pushToPeers sends content to all connected peers via gRPC Push.
func (n *Node) pushToPeers(ctx context.Context, content *pb.ClipboardContent) {
	n.peersMu.RLock()
	pcs := make([]*peerConn, 0, len(n.peers))
	for _, pc := range n.peers {
		pcs = append(pcs, pc)
	}
	n.peersMu.RUnlock()

	for _, pc := range pcs {
		go func(pc *peerConn) {
			client := pb.NewClipboardServiceClient(pc.conn)
			ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if _, err := client.Push(ctx2, &pb.PushRequest{Content: content}); err != nil {
				log.Printf("push to %s: %v", pc.id[:8], err)
			}
		}(pc)
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
}
