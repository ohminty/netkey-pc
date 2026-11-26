// Package ipx is a switch port that is an IPX server for DOSBox on this PC (IPXNET CONNECT 127.0.0.1). It turns
// DOSBox's IPX-over-UDP packets into real IPX frames on the party and back, so DOS games play IPX with nothing to
// install. Design: docs/design-dosbox-ipx.md.
//
// DOSBox's protocol (DOSBox-X src/hardware/ipx.cpp, ipxserver.cpp): one UDP datagram per IPX packet, the 30-byte
// IPX header (big-endian) and the data. A client registers with a bare header to socket 2 with a zero dest node and
// takes its node address from our reply. Clients don't filter by dest node, so we route exactly.
package ipx

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sort"
	"sync"
	"time"
)

// FrameType is how IPX is carried on Ethernet.
type FrameType int

const (
	Ethernet8022 FrameType = iota // 802.3 + LLC E0 E0 03 (Novell default since NetWare 4)
	EthernetII                    // EtherType 0x8137
	Ethernet8023                  // "raw" 802.3: IPX right after the length (checksum FFFF)
	EthernetSNAP                  // 802.3 + LLC AA AA 03 + SNAP 00 00 00 81 37
)

func (t FrameType) String() string {
	return [...]string{"Ethernet_802.2", "Ethernet_II", "Ethernet_802.3", "Ethernet_SNAP"}[t]
}

const (
	DefaultPort = 213 // IANA's IPX-over-IP port; DOSBox's default for IPXNET CONNECT
	headerLen   = 30
	controlSock = 2 // DOSBox's registration and ping socket (Novell's echo socket on a real LAN)
	maxClients  = 32
	expireAfter = 30 * time.Minute
	seenFor     = 5 * time.Minute // frame types seen from the party within this get a copy of each broadcast
	maxStations = 4096
	minFrame    = 60
)

var broadcast = [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

// Encode wraps an IPX packet in an Ethernet frame of type t. ok is false if it doesn't fit.
func Encode(t FrameType, dst, src [6]byte, pkt []byte) ([]byte, bool) {
	var f []byte
	head := func(typeOrLen int) {
		f = make([]byte, 14, 14+8+len(pkt))
		copy(f[0:6], dst[:])
		copy(f[6:12], src[:])
		binary.BigEndian.PutUint16(f[12:], uint16(typeOrLen))
	}
	switch t {
	case EthernetII:
		if len(pkt) > 1500 {
			return nil, false
		}
		head(0x8137)
	case Ethernet8023:
		if len(pkt) > 1500 {
			return nil, false
		}
		head(len(pkt))
	case Ethernet8022:
		if len(pkt) > 1497 {
			return nil, false
		}
		head(3 + len(pkt))
		f = append(f, 0xe0, 0xe0, 0x03)
	case EthernetSNAP:
		if len(pkt) > 1492 {
			return nil, false
		}
		head(8 + len(pkt))
		f = append(f, 0xaa, 0xaa, 0x03, 0x00, 0x00, 0x00, 0x81, 0x37)
	default:
		return nil, false
	}
	f = append(f, pkt...)
	for len(f) < minFrame {
		f = append(f, 0)
	}
	return f, true
}

// Decode finds an IPX packet in an Ethernet frame (any of the four framings), trimmed to its length field.
func Decode(f []byte) (FrameType, []byte, bool) {
	if len(f) < 14+headerLen {
		return 0, nil, false
	}
	tl := int(binary.BigEndian.Uint16(f[12:14]))
	var t FrameType
	var pkt []byte
	switch {
	case tl == 0x8137:
		t, pkt = EthernetII, f[14:]
	case tl <= 1500:
		if 14+tl > len(f) {
			return 0, nil, false
		}
		p := f[14 : 14+tl]
		switch {
		case len(p) >= 2 && p[0] == 0xff && p[1] == 0xff:
			t, pkt = Ethernet8023, p
		case len(p) >= 3 && p[0] == 0xe0 && p[1] == 0xe0 && p[2] == 0x03:
			t, pkt = Ethernet8022, p[3:]
		case len(p) >= 8 && p[0] == 0xaa && p[1] == 0xaa && p[2] == 0x03 && p[3] == 0 && p[4] == 0 && p[5] == 0 &&
			p[6] == 0x81 && p[7] == 0x37:
			t, pkt = EthernetSNAP, p[8:]
		default:
			return 0, nil, false
		}
	default:
		return 0, nil, false
	}
	return t, trim(pkt), trim(pkt) != nil
}

// trim cuts an IPX packet to its length field; nil if it's not a plausible IPX packet.
func trim(p []byte) []byte {
	if len(p) < headerLen {
		return nil
	}
	n := int(binary.BigEndian.Uint16(p[2:4]))
	if n < headerLen || n > len(p) {
		return nil
	}
	return p[:n]
}

func dstNode(p []byte) (n [6]byte) { copy(n[:], p[10:16]); return }
func srcNode(p []byte) (n [6]byte) { copy(n[:], p[22:28]); return }
func dstSock(p []byte) uint16      { return binary.BigEndian.Uint16(p[16:18]) }

// isBroadcast: DOSBox's own server only looks at the first four bytes of the dest node.
func isBroadcast(n [6]byte) bool { return n[0] == 0xff && n[1] == 0xff && n[2] == 0xff && n[3] == 0xff }

func plausibleNode(n [6]byte) bool {
	return n != [6]byte{} && !isBroadcast(n) && n[0]&1 == 0
}

type client struct {
	addr        *net.UDPAddr
	node        [6]byte
	since, last time.Time
}

// ClientInfo is a registered DOSBox, for the status.
type ClientInfo struct {
	Node  string    `json:"node"`
	Since time.Time `json:"since"`
}

// Port is the IPX server and switch port.
type Port struct {
	conn *net.UDPConn
	now  func() time.Time
	Logf func(string, ...any)
	// OnFrame hands a frame to the switch (set before traffic starts).
	OnFrame func(frame []byte)

	mu       sync.Mutex
	byAddr   map[string]*client
	byNode   map[[6]byte]*client
	seen     map[FrameType]time.Time
	stations map[[6]byte]FrameType
	closed   bool
}

// Open listens on addr (normally "127.0.0.1:213").
func Open(addr string, logf func(string, ...any)) (*Port, error) {
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	if !ua.IP.IsLoopback() {
		return nil, errors.New("the DOSBox IPX server only listens on this PC (127.0.0.1)")
	}
	conn, err := net.ListenUDP("udp4", ua)
	if err != nil {
		return nil, err
	}
	p := newPort(logf)
	p.conn = conn
	go p.recv()
	go p.janitor()
	return p, nil
}

func newPort(logf func(string, ...any)) *Port {
	return &Port{now: time.Now, Logf: logf, byAddr: map[string]*client{}, byNode: map[[6]byte]*client{},
		seen: map[FrameType]time.Time{}, stations: map[[6]byte]FrameType{}}
}

// Name is the switch port name.
func (p *Port) Name() string { return "ipx" }

// Addr is where the server listens.
func (p *Port) Addr() *net.UDPAddr { return p.conn.LocalAddr().(*net.UDPAddr) }

// Clients lists the registered DOSBox clients.
func (p *Port) Clients() []ClientInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ClientInfo, 0, len(p.byAddr))
	for _, c := range p.byAddr {
		out = append(out, ClientInfo{Node: net.HardwareAddr(c.node[:]).String(), Since: c.since})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

func (p *Port) logf(f string, a ...any) {
	if p.Logf != nil {
		p.Logf(f, a...)
	}
}

func (p *Port) recv() {
	buf := make([]byte, 2048)
	for {
		n, from, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if !closed {
				p.logf("DOSBox IPX: receive stopped: %v", err)
			}
			return
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		p.fromDOSBox(from, pkt)
	}
}

func (p *Port) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		p.expireLocked()
		p.mu.Unlock()
	}
}

func (p *Port) expireLocked() {
	now := p.now()
	for k, c := range p.byAddr {
		if now.Sub(c.last) > expireAfter {
			delete(p.byAddr, k)
			delete(p.byNode, c.node)
			p.logf("DOSBox IPX: forgot %s (silent for %v)", net.HardwareAddr(c.node[:]), expireAfter)
		}
	}
	for t, at := range p.seen {
		if now.Sub(at) > seenFor {
			delete(p.seen, t)
		}
	}
}

// newNode picks a free locally administered address 02:4E:4B:xx:xx:xx ("NK").
func (p *Port) newNodeLocked() [6]byte {
	for {
		n := [6]byte{0x02, 0x4e, 0x4b}
		rand.Read(n[3:])
		if _, taken := p.byNode[n]; !taken && plausibleNode(n) {
			return n
		}
	}
}

// registration reply: dest node = the client's node, socket 2; src network 1 and our address (unused by DOSBox).
func reply(node [6]byte) []byte {
	b := make([]byte, headerLen)
	binary.BigEndian.PutUint16(b[0:], 0xffff)
	binary.BigEndian.PutUint16(b[2:], headerLen)
	copy(b[10:16], node[:])
	binary.BigEndian.PutUint16(b[16:], controlSock)
	binary.BigEndian.PutUint32(b[18:], 1)
	copy(b[22:28], []byte{127, 0, 0, 1, 0, DefaultPort})
	binary.BigEndian.PutUint16(b[28:], controlSock)
	return b
}

type delivery struct {
	addr *net.UDPAddr
	pkt  []byte
}

// fromDOSBox handles one datagram from a DOSBox on this PC.
func (p *Port) fromDOSBox(from *net.UDPAddr, raw []byte) {
	if from == nil || !from.IP.IsLoopback() {
		return // only DOSBox on this PC: the tunnel has no authentication
	}
	if len(raw) < headerLen {
		return
	}
	dst := dstNode(raw)
	key := from.String()
	now := p.now()

	if dstSock(raw) == controlSock && dst[0] == 0 && dst[1] == 0 && dst[2] == 0 && dst[3] == 0 {
		p.mu.Lock()
		c := p.byAddr[key]
		if c == nil {
			if len(p.byAddr) >= maxClients {
				p.mu.Unlock()
				p.logf("DOSBox IPX: refused a registration, %d DOSBox already connected", maxClients)
				return
			}
			c = &client{addr: from, node: p.newNodeLocked(), since: now}
			p.byAddr[key], p.byNode[c.node] = c, c
			p.logf("DOSBox IPX: DOSBox at %s connected as %s", from, net.HardwareAddr(c.node[:]))
		}
		c.last = now
		node := c.node
		p.mu.Unlock()
		_, _ = p.conn.WriteToUDP(reply(node), from)
		return
	}

	pkt := trim(raw)
	if pkt == nil {
		return
	}
	src := srcNode(pkt)
	p.mu.Lock()
	c := p.byAddr[key]
	if c == nil {
		// adoption: a DOSBox that registered before the service restarted keeps sending with its node
		if !plausibleNode(src) || p.byNode[src] != nil || len(p.byAddr) >= maxClients {
			p.mu.Unlock()
			return
		}
		c = &client{addr: from, node: src, since: now}
		p.byAddr[key], p.byNode[src] = c, c
		p.logf("DOSBox IPX: picked up DOSBox at %s as %s", from, net.HardwareAddr(src[:]))
	}
	if src != c.node {
		p.mu.Unlock()
		return // a DOSBox can't speak for another station
	}
	c.last = now
	bcast := isBroadcast(dst)
	var out []delivery
	local := false
	for _, o := range p.byAddr {
		if o == c {
			continue
		}
		if bcast || o.node == dst {
			out = append(out, delivery{o.addr, pkt})
			local = local || o.node == dst
		}
	}
	var frames [][]byte
	if dstSock(pkt) != controlSock && (bcast || !local) {
		frames = p.framesLocked(dst, c.node, pkt, bcast)
	}
	p.mu.Unlock()

	for _, d := range out {
		_, _ = p.conn.WriteToUDP(d.pkt, d.addr)
	}
	if f := p.OnFrame; f != nil {
		for _, fr := range frames {
			f(fr)
		}
	}
}

// framesLocked: broadcasts in 802.2 and every frame type seen from the party lately; unicasts in the destination's.
func (p *Port) framesLocked(dst, src [6]byte, pkt []byte, bcast bool) [][]byte {
	types := []FrameType{Ethernet8022}
	if bcast {
		now := p.now()
		for t, at := range p.seen {
			if t != Ethernet8022 && now.Sub(at) <= seenFor {
				types = append(types, t)
			}
		}
		sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	} else if t, ok := p.stations[dst]; ok {
		types = []FrameType{t}
	}
	mac := dst
	if bcast {
		mac = broadcast
	}
	var out [][]byte
	for _, t := range types {
		if f, ok := Encode(t, mac, src, pkt); ok {
			out = append(out, f)
		}
	}
	return out
}

// Send takes a frame from the switch (the party, the NetKey Adapter, the Local Switch).
func (p *Port) Send(frame []byte) error {
	t, pkt, ok := Decode(frame)
	if !ok {
		return nil
	}
	var from [6]byte
	copy(from[:], frame[6:12])
	dst := dstNode(pkt)
	p.mu.Lock()
	p.seen[t] = p.now()
	if len(p.stations) >= maxStations {
		p.stations = map[[6]byte]FrameType{}
	}
	p.stations[from] = t
	var out []*net.UDPAddr
	if dstSock(pkt) != controlSock {
		if isBroadcast(dst) {
			for _, c := range p.byAddr {
				out = append(out, c.addr)
			}
		} else if c := p.byNode[dst]; c != nil {
			out = append(out, c.addr)
		}
	}
	p.mu.Unlock()
	for _, a := range out {
		_, _ = p.conn.WriteToUDP(pkt, a)
	}
	return nil
}

// Close stops the server.
func (p *Port) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.conn.Close()
}
