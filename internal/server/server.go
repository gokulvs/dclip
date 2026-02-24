// Package server implements the gRPC ClipboardService.
package server

import (
	"context"
	"sync"
	"time"

	pb "github.com/gokulvs/dclip/gen/clipboard/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements pb.ClipboardServiceServer.
type Server struct {
	pb.UnimplementedClipboardServiceServer

	mu      sync.RWMutex
	current *pb.ClipboardContent

	// pushNotify delivers content received via the Push RPC to the local node
	// so it can apply the change to the system clipboard.
	pushNotify chan *pb.ClipboardContent

	// subscribers is a map of subscriber ID -> channel of events
	subsMu      sync.Mutex
	subscribers map[string]chan *pb.ClipboardEvent
}

// New creates a new Server.
func New() *Server {
	return &Server{
		pushNotify:  make(chan *pb.ClipboardContent, 32),
		subscribers: make(map[string]chan *pb.ClipboardEvent),
	}
}

// IncomingPushes returns the channel that receives every Push from a remote peer.
// The node reads from this channel to write incoming content to the local clipboard.
func (s *Server) IncomingPushes() <-chan *pb.ClipboardContent {
	return s.pushNotify
}

// Push receives clipboard content from a peer, stores it, notifies the local
// node (via pushNotify), and fans out to any Subscribe stream subscribers.
func (s *Server) Push(ctx context.Context, req *pb.PushRequest) (*pb.PushResponse, error) {
	if req.Content == nil {
		return nil, status.Error(codes.InvalidArgument, "content is required")
	}

	s.mu.Lock()
	s.current = req.Content
	s.mu.Unlock()

	// Deliver to the local node (non-blocking — node may be slow).
	select {
	case s.pushNotify <- req.Content:
	default:
	}

	// Also fan out to any Subscribe-stream subscribers.
	event := &pb.ClipboardEvent{Content: req.Content}
	s.subsMu.Lock()
	for _, ch := range s.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
	s.subsMu.Unlock()

	return &pb.PushResponse{Accepted: true}, nil
}

// Pull returns the most recently stored clipboard content.
func (s *Server) Pull(ctx context.Context, _ *pb.PullRequest) (*pb.PullResponse, error) {
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	return &pb.PullResponse{Content: current}, nil
}

// Subscribe opens a server-streaming RPC that pushes clipboard events to the
// subscriber as they arrive.
// On connect it immediately sends the current clipboard so a rejoining peer
// is brought up to date without waiting for the next copy event.
func (s *Server) Subscribe(req *pb.SubscribeRequest, stream pb.ClipboardService_SubscribeServer) error {
	id := req.SubscriberId
	ch := make(chan *pb.ClipboardEvent, 16)

	s.subsMu.Lock()
	s.subscribers[id] = ch
	s.subsMu.Unlock()

	defer func() {
		s.subsMu.Lock()
		delete(s.subscribers, id)
		s.subsMu.Unlock()
	}()

	// Initial sync: send whatever we currently hold so the subscriber is
	// immediately up-to-date (handles peer restart / late join).
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	if current != nil {
		if err := stream.Send(&pb.ClipboardEvent{Content: current}); err != nil {
			return err
		}
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(event); err != nil {
				return err
			}
		}
	}
}

// Broadcast pushes locally-originated content to all Subscribe-stream subscribers.
func (s *Server) Broadcast(content *pb.ClipboardContent) {
	content.Timestamp = time.Now().UnixNano()

	s.mu.Lock()
	s.current = content
	s.mu.Unlock()

	event := &pb.ClipboardEvent{Content: content}
	s.subsMu.Lock()
	for _, ch := range s.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
	s.subsMu.Unlock()
}
