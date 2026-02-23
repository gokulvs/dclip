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

	// subscribers is a map of subscriber ID -> channel of events
	subsMu      sync.Mutex
	subscribers map[string]chan *pb.ClipboardEvent
}

// New creates a new Server.
func New() *Server {
	return &Server{
		subscribers: make(map[string]chan *pb.ClipboardEvent),
	}
}

// Push receives clipboard content from a peer and stores it locally,
// then fans it out to all subscribers.
func (s *Server) Push(ctx context.Context, req *pb.PushRequest) (*pb.PushResponse, error) {
	if req.Content == nil {
		return nil, status.Error(codes.InvalidArgument, "content is required")
	}

	s.mu.Lock()
	s.current = req.Content
	s.mu.Unlock()

	// Notify all subscribers (non-blocking).
	event := &pb.ClipboardEvent{Content: req.Content}
	s.subsMu.Lock()
	for id, ch := range s.subscribers {
		select {
		case ch <- event:
		default:
			// Subscriber is slow — drop the event rather than block.
			_ = id
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

// Broadcast pushes content directly (used by the local node to propagate its
// own clipboard changes without going through gRPC).
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
