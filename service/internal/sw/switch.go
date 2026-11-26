// Package sw is the small Ethernet switch inside the app: the party, the NetKey adapter (TAP) and the 86Box Local
// Switch each are a port. It learns source MACs, sends known unicast to one port, floods the rest, and never sends
// a frame back out of the port it came in on.
package sw

import (
	"sync"
	"time"
)

// Port is one side of the switch.
type Port interface {
	Name() string
	Send(frame []byte) error
}

const ageOut = 5 * time.Minute

type entry struct {
	port Port
	seen time.Time
}

// Stats counts frames per port name.
type Stats struct {
	In, Out uint64
}

// Switch connects ports.
type Switch struct {
	mu    sync.Mutex
	ports []Port
	macs  map[[6]byte]entry
	stats map[string]*Stats
	now   func() time.Time
	// OnMove is called when a MAC turns up on another port than before (a duplicate MAC, e.g. two DOSBox-X with the
	// default AC:DE:48:88:99:AA), at most once a minute per MAC.
	OnMove   func(mac [6]byte, from, to string)
	lastMove map[[6]byte]time.Time
}

// New returns an empty switch.
func New() *Switch {
	return &Switch{macs: map[[6]byte]entry{}, stats: map[string]*Stats{}, now: time.Now,
		lastMove: map[[6]byte]time.Time{}}
}

// Add plugs in a port.
func (s *Switch) Add(p Port) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ports = append(s.ports, p)
	s.stats[p.Name()] = &Stats{}
}

// Remove unplugs a port and forgets its MACs.
func (s *Switch) Remove(p Port) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, q := range s.ports {
		if q == p {
			s.ports = append(s.ports[:i], s.ports[i+1:]...)
			break
		}
	}
	for m, e := range s.macs {
		if e.port == p {
			delete(s.macs, m)
		}
	}
}

// Stats returns a copy of the frame counters.
func (s *Switch) Stats() map[string]Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Stats{}
	for k, v := range s.stats {
		out[k] = *v
	}
	return out
}

// Input takes a frame that arrived on port from.
func (s *Switch) Input(from Port, frame []byte) {
	if len(frame) < 14 {
		return
	}
	var dst, src [6]byte
	copy(dst[:], frame[0:6])
	copy(src[:], frame[6:12])
	now := s.now()
	s.mu.Lock()
	if st := s.stats[from.Name()]; st != nil {
		st.In++
	}
	if src[0]&1 == 0 { // never learn a multicast source
		if old, ok := s.macs[src]; ok && old.port != from && now.Sub(old.seen) < 30*time.Second && s.OnMove != nil {
			if now.Sub(s.lastMove[src]) > time.Minute {
				s.lastMove[src] = now
				cb, a, b := s.OnMove, old.port.Name(), from.Name()
				defer cb(src, a, b)
			}
		}
		s.macs[src] = entry{from, now}
	}
	var targets []Port
	if e, ok := s.macs[dst]; ok && dst[0]&1 == 0 && now.Sub(e.seen) < ageOut {
		if e.port != from {
			targets = []Port{e.port}
		}
	} else {
		for _, p := range s.ports {
			if p != from {
				targets = append(targets, p)
			}
		}
	}
	for _, p := range targets {
		if st := s.stats[p.Name()]; st != nil {
			st.Out++
		}
	}
	s.mu.Unlock()
	for _, p := range targets {
		_ = p.Send(frame)
	}
}
