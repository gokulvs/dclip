// Package discovery handles mDNS-based peer discovery on the local network.
package discovery

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	// ServiceType is the mDNS service type for dclip nodes.
	ServiceType = "_dclip._tcp"
	// Domain is the mDNS domain.
	Domain = "local."
)

// Peer represents a discovered dclip peer.
type Peer struct {
	ID      string
	Host    string
	Port    int
	AddrV4  net.IP
}

func (p Peer) Addr() string {
	if p.AddrV4 != nil {
		return fmt.Sprintf("%s:%d", p.AddrV4.String(), p.Port)
	}
	return fmt.Sprintf("%s:%d", p.Host, p.Port)
}

// Service wraps zeroconf registration and browsing.
type Service struct {
	nodeID string
	port   int
	server *zeroconf.Server
}

// Register announces this node on the local network via mDNS.
func Register(nodeID string, port int) (*Service, error) {
	server, err := zeroconf.Register(
		nodeID,     // instance name (unique per node)
		ServiceType,
		Domain,
		port,
		[]string{"id=" + nodeID},
		nil, // auto-detect interfaces
	)
	if err != nil {
		return nil, fmt.Errorf("mDNS register: %w", err)
	}
	return &Service{nodeID: nodeID, port: port, server: server}, nil
}

// Shutdown deregisters the mDNS service.
func (s *Service) Shutdown() {
	if s.server != nil {
		s.server.Shutdown()
	}
}

// Browse continuously discovers peers and sends them on the returned channel.
// It stops when ctx is cancelled.
func Browse(ctx context.Context, selfID string, found chan<- Peer) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Printf("mDNS resolver error: %v", err)
		return
	}

	entries := make(chan *zeroconf.ServiceEntry)
	go func() {
		if err := resolver.Browse(ctx, ServiceType, Domain, entries); err != nil {
			if ctx.Err() == nil {
				log.Printf("mDNS browse error: %v", err)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-entries:
			if !ok {
				// Channel closed; retry after a short pause.
				time.Sleep(5 * time.Second)
				return
			}
			// Skip ourselves.
			if entry.Instance == selfID {
				continue
			}
			peer := Peer{
				ID:   entry.Instance,
				Host: entry.HostName,
				Port: entry.Port,
			}
			if len(entry.AddrIPv4) > 0 {
				peer.AddrV4 = entry.AddrIPv4[0]
			}
			select {
			case found <- peer:
			case <-ctx.Done():
				return
			}
		}
	}
}
