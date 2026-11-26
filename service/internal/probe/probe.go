// Package probe is a tiny virtual PC on the switch, for the connection test: it asks the party for an address by
// DHCP, finds the gateway by ARP and pings it with a small and a full-size (1514-byte frame) packet, then gives the
// address back. If all of that works, a real retro PC or emulator on this app will work too.
package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// Result of a connection test.
type Result struct {
	MAC        string
	Address    netip.Addr
	Gateway    netip.Addr
	DHCPServer netip.Addr
	PingSmall  time.Duration // 0: no reply
	PingFull   time.Duration
	Extra      map[netip.Addr]time.Duration
}

// Probe is the virtual PC; plug it into the switch, then call Run.
type Probe struct {
	mac [6]byte
	ip  netip.Addr
	in  chan []byte
	Out func(frame []byte) // to the switch
	mu  sync.Mutex
}

// New returns a probe with a random locally administered MAC.
func New() *Probe {
	p := &Probe{in: make(chan []byte, 64)}
	rand.Read(p.mac[:])
	p.mac[0] = (p.mac[0] | 0x02) &^ 0x01
	return p
}

func (p *Probe) Name() string { return "test" }

// Send takes a frame from the switch.
func (p *Probe) Send(f []byte) error {
	if len(f) < 14 || ([6]byte(f[0:6]) != p.mac && f[0]&1 == 0) {
		return nil
	}
	select {
	case p.in <- f:
	default:
	}
	return nil
}

var bcast = [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

func eth(dst, src [6]byte, typ uint16, payload []byte) []byte {
	f := make([]byte, 14, 14+len(payload))
	copy(f[0:6], dst[:])
	copy(f[6:12], src[:])
	binary.BigEndian.PutUint16(f[12:], typ)
	f = append(f, payload...)
	for len(f) < 60 {
		f = append(f, 0)
	}
	return f
}

func csum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

func ipv4(src, dst netip.Addr, proto byte, df bool, payload []byte) []byte {
	h := make([]byte, 20, 20+len(payload))
	h[0] = 0x45
	binary.BigEndian.PutUint16(h[2:], uint16(20+len(payload)))
	var id [2]byte
	rand.Read(id[:])
	copy(h[4:6], id[:])
	if df {
		h[6] = 0x40
	}
	h[8] = 64
	h[9] = proto
	s, d := src.As4(), dst.As4()
	copy(h[12:16], s[:])
	copy(h[16:20], d[:])
	binary.BigEndian.PutUint16(h[10:], csum(h))
	return append(h, payload...)
}

func udp(sport, dport uint16, payload []byte) []byte {
	u := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint16(u[0:], sport)
	binary.BigEndian.PutUint16(u[2:], dport)
	binary.BigEndian.PutUint16(u[4:], uint16(8+len(payload)))
	return append(u, payload...) // checksum 0: allowed for UDP over IPv4
}

// DHCP
const (
	dhcpDiscover = 1
	dhcpOffer    = 2
	dhcpRequest  = 3
	dhcpAck      = 5
	dhcpRelease  = 7
)

func (p *Probe) dhcp(xid uint32, typ byte, ciaddr, reqIP, server netip.Addr) []byte {
	b := make([]byte, 240)
	b[0], b[1], b[2] = 1, 1, 6
	binary.BigEndian.PutUint32(b[4:], xid)
	if typ != dhcpRelease {
		b[10] = 0x80 // broadcast reply: we have no address yet
	}
	if ciaddr.IsValid() {
		a := ciaddr.As4()
		copy(b[12:16], a[:])
	}
	copy(b[28:34], p.mac[:])
	binary.BigEndian.PutUint32(b[236:], 0x63825363)
	b = append(b, 53, 1, typ)
	if reqIP.IsValid() {
		a := reqIP.As4()
		b = append(b, 50, 4, a[0], a[1], a[2], a[3])
	}
	if server.IsValid() {
		a := server.As4()
		b = append(b, 54, 4, a[0], a[1], a[2], a[3])
	}
	b = append(b, 12, 9, 'n', 'e', 't', 'k', 'e', 'y', '-', 'p', 'c', 55, 2, 1, 3, 255)
	src, dst := netip.IPv4Unspecified(), netip.AddrFrom4([4]byte{255, 255, 255, 255})
	dmac := bcast
	if typ == dhcpRelease {
		src, dst = ciaddr, server
	}
	return eth(dmac, p.mac, 0x0800, ipv4(src, dst, 17, false, udp(68, 67, b)))
}

type dhcpReply struct {
	typ            byte
	yiaddr, server netip.Addr
	router         netip.Addr
}

func parseDHCP(f []byte, xid uint32) (dhcpReply, bool) {
	var r dhcpReply
	if len(f) < 14+20+8+240 || binary.BigEndian.Uint16(f[12:]) != 0x0800 {
		return r, false
	}
	ip := f[14:]
	ihl := int(ip[0]&0x0f) * 4
	if ip[9] != 17 || len(ip) < ihl+8+240 {
		return r, false
	}
	u := ip[ihl:]
	if binary.BigEndian.Uint16(u[0:]) != 67 || binary.BigEndian.Uint16(u[2:]) != 68 {
		return r, false
	}
	d := u[8:]
	if d[0] != 2 || binary.BigEndian.Uint32(d[4:]) != xid {
		return r, false
	}
	r.yiaddr = netip.AddrFrom4([4]byte(d[16:20]))
	opts := d[240:]
	for i := 0; i < len(opts); {
		code := opts[i]
		if code == 255 {
			break
		}
		if code == 0 {
			i++
			continue
		}
		if i+1 >= len(opts) || i+2+int(opts[i+1]) > len(opts) {
			break
		}
		v := opts[i+2 : i+2+int(opts[i+1])]
		switch {
		case code == 53 && len(v) == 1:
			r.typ = v[0]
		case code == 54 && len(v) == 4:
			r.server = netip.AddrFrom4([4]byte(v))
		case code == 3 && len(v) >= 4:
			r.router = netip.AddrFrom4([4]byte(v[:4]))
		}
		i += 2 + len(v)
	}
	return r, r.typ != 0
}

func (p *Probe) arp(op uint16, dmac [6]byte, tha [6]byte, spa, tpa netip.Addr) []byte {
	a := make([]byte, 28)
	binary.BigEndian.PutUint16(a[0:], 1)
	binary.BigEndian.PutUint16(a[2:], 0x0800)
	a[4], a[5] = 6, 4
	binary.BigEndian.PutUint16(a[6:], op)
	copy(a[8:14], p.mac[:])
	s, t := spa.As4(), tpa.As4()
	copy(a[14:18], s[:])
	copy(a[18:24], tha[:])
	copy(a[24:28], t[:])
	return eth(dmac, p.mac, 0x0806, a)
}

// answerARP replies to "who has <our ip>" so the gateway can send pings back. Returns true if f was ARP.
func (p *Probe) answerARP(f []byte) (sender netip.Addr, senderMAC [6]byte, isReply bool, ok bool) {
	if len(f) < 42 || binary.BigEndian.Uint16(f[12:]) != 0x0806 {
		return
	}
	a := f[14:]
	op := binary.BigEndian.Uint16(a[6:])
	copy(senderMAC[:], a[8:14])
	sender = netip.AddrFrom4([4]byte(a[14:18]))
	target := netip.AddrFrom4([4]byte(a[24:28]))
	p.mu.Lock()
	mine := p.ip
	p.mu.Unlock()
	if op == 1 && mine.IsValid() && target == mine {
		p.Out(p.arp(2, senderMAC, senderMAC, mine, sender))
	}
	return sender, senderMAC, op == 2, true
}

func (p *Probe) wait(ctx context.Context, d time.Duration, match func(f []byte) bool) ([]byte, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case f := <-p.in:
			if _, _, _, isARP := p.answerARP(f); isARP && !match(f) {
				continue
			}
			if match(f) {
				return f, nil
			}
		case <-t.C:
			return nil, errors.New("timed out")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (p *Probe) resolve(ctx context.Context, ip netip.Addr) ([6]byte, error) {
	var mac [6]byte
	for try := 0; try < 3; try++ {
		p.Out(p.arp(1, bcast, [6]byte{}, p.ip, ip))
		_, err := p.wait(ctx, time.Second, func(f []byte) bool {
			s, m, isReply, ok := p.answerARP(f)
			if ok && isReply && s == ip {
				mac = m
				return true
			}
			return false
		})
		if err == nil {
			return mac, nil
		}
	}
	return mac, fmt.Errorf("no ARP reply from %s", ip)
}

func (p *Probe) ping(ctx context.Context, mac [6]byte, ip netip.Addr, size int, seq uint16) time.Duration {
	icmp := make([]byte, 8+size)
	icmp[0] = 8
	binary.BigEndian.PutUint16(icmp[4:], 0x4e4b) // "NK"
	binary.BigEndian.PutUint16(icmp[6:], seq)
	for i := 8; i < len(icmp); i++ {
		icmp[i] = byte(i)
	}
	binary.BigEndian.PutUint16(icmp[2:], csum(icmp))
	for try := 0; try < 3; try++ {
		t0 := time.Now()
		p.Out(eth(mac, p.mac, 0x0800, ipv4(p.ip, ip, 1, true, icmp)))
		_, err := p.wait(ctx, 2*time.Second, func(f []byte) bool {
			if len(f) < 14+20+8 || binary.BigEndian.Uint16(f[12:]) != 0x0800 || f[14+9] != 1 {
				return false
			}
			ihl := int(f[14]&0x0f) * 4
			r := f[14+ihl:]
			return netip.AddrFrom4([4]byte(f[14+12:14+16])) == ip && r[0] == 0 &&
				binary.BigEndian.Uint16(r[4:]) == 0x4e4b && binary.BigEndian.Uint16(r[6:]) == seq &&
				len(r) == len(icmp)
		})
		if err == nil {
			return time.Since(t0)
		}
	}
	return 0
}

// Run does the whole test; extra are more addresses on the party to ping (small packets).
func (p *Probe) Run(ctx context.Context, extra []netip.Addr) (Result, error) {
	res := Result{MAC: fmt.Sprintf("%x:%x:%x:%x:%x:%x", p.mac[0], p.mac[1], p.mac[2], p.mac[3], p.mac[4], p.mac[5]),
		Extra: map[netip.Addr]time.Duration{}}
	var xb [4]byte
	rand.Read(xb[:])
	xid := binary.BigEndian.Uint32(xb[:])
	var offer dhcpReply
	var err error
	for try := 0; try < 4; try++ {
		p.Out(p.dhcp(xid, dhcpDiscover, netip.Addr{}, netip.Addr{}, netip.Addr{}))
		_, err = p.wait(ctx, 3*time.Second, func(f []byte) bool {
			r, ok := parseDHCP(f, xid)
			if ok && r.typ == dhcpOffer {
				offer = r
			}
			return ok && r.typ == dhcpOffer
		})
		if err == nil {
			break
		}
	}
	if err != nil {
		return res, errors.New("no DHCP offer from the party (is the host's network up?)")
	}
	var ack dhcpReply
	p.Out(p.dhcp(xid, dhcpRequest, netip.Addr{}, offer.yiaddr, offer.server))
	if _, err := p.wait(ctx, 4*time.Second, func(f []byte) bool {
		r, ok := parseDHCP(f, xid)
		if ok && r.typ == dhcpAck {
			ack = r
		}
		return ok && r.typ == dhcpAck
	}); err != nil {
		return res, errors.New("the DHCP server offered an address but never confirmed it")
	}
	p.mu.Lock()
	p.ip = ack.yiaddr
	p.mu.Unlock()
	res.Address, res.DHCPServer, res.Gateway = ack.yiaddr, ack.server, ack.router
	defer p.Out(p.dhcp(xid+1, dhcpRelease, ack.yiaddr, netip.Addr{}, ack.server))
	gw := ack.router
	if !gw.IsValid() {
		gw = ack.server
	}
	mac, err := p.resolve(ctx, gw)
	if err != nil {
		return res, err
	}
	res.PingSmall = p.ping(ctx, mac, gw, 56, 1)
	res.PingFull = p.ping(ctx, mac, gw, 1472, 2) // 1472 + 8 ICMP + 20 IP = 1500: a 1514-byte frame
	for i, ip := range extra {
		m, err := p.resolve(ctx, ip)
		if err != nil {
			res.Extra[ip] = 0
			continue
		}
		res.Extra[ip] = p.ping(ctx, m, ip, 56, uint16(10+i))
	}
	return res, nil
}
