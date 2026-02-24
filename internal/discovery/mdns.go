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
	ID   string
	Host string
	Port int
	// AddrIPv4s holds ALL IPv4 addresses advertised by this peer (e.g. LAN +
	// Hyper-V virtual adapters on Windows). We try each one in order.
	AddrIPv4s []net.IP
	// RawAddr overrides everything when set (used for manually specified peers).
	RawAddr string
}

// Addrs returns all candidate dial addresses for this peer in preference order.
// For mDNS peers this can be several IPs (real LAN + virtual adapters).
func (p Peer) Addrs() []string {
	if p.RawAddr != "" {
		return []string{p.RawAddr}
	}
	var out []string
	for _, ip := range p.AddrIPv4s {
		out = append(out, fmt.Sprintf("%s:%d", ip.String(), p.Port))
	}
	if len(out) == 0 {
		out = append(out, fmt.Sprintf("%s:%d", p.Host, p.Port))
	}
	return out
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
		nodeID,      // instance name (unique per node)
		ServiceType,
		Domain,
		port,
		[]string{"id=" + nodeID},
		nil, // auto-detect interfaces
	)
	if err != nil {
		return nil, fmt.Errorf("mDNS register: %w", err)
	}
	log.Printf("[mdns] registered as %s on port %d", nodeID[:8], port)
	return &Service{nodeID: nodeID, port: port, server: server}, nil
}

// Shutdown deregisters the mDNS service.
func (s *Service) Shutdown() {
	if s.server != nil {
		s.server.Shutdown()
	}
}

// Browse continuously discovers peers and sends them on found.
// It returns when ctx is cancelled.
func Browse(ctx context.Context, selfID string, found chan<- Peer) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Printf("[mdns] resolver error: %v", err)
		return
	}

	log.Printf("[mdns] browsing for %s peers…", ServiceType)

	entries := make(chan *zeroconf.ServiceEntry)
	go func() {
		if err := resolver.Browse(ctx, ServiceType, Domain, entries); err != nil {
			if ctx.Err() == nil {
				log.Printf("[mdns] browse error: %v", err)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-entries:
			if !ok {
				time.Sleep(5 * time.Second)
				return
			}
			// Skip ourselves.
			if entry.Instance == selfID {
				continue
			}
			peer := Peer{
				ID:        entry.Instance,
				Host:      entry.HostName,
				Port:      entry.Port,
				AddrIPv4s: entry.AddrIPv4, // all IPs, including virtual adapters
			}
			log.Printf("[mdns] found peer %s with %d addr(s): %v", peer.ID[:8], len(peer.AddrIPv4s), peer.Addrs())
			select {
			case found <- peer:
			case <-ctx.Done():
				return
			}
		}
	}
}
