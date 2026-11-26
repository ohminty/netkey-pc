// Package lswitch is a switch port that speaks 86Box's "Local Switch" (src/network/net_switch.c), so 86Box on this
// PC joins the party with no driver. QEMU (dgram backend) speaks the same thing without a secret.
//
// 86Box sends every frame as one UDP datagram to port 8086: the raw Ethernet frame, prefixed with SHA3-256(secret)
// when a shared secret is set. It sends a copy per host interface: multicast (239.255.80.86 with a secret,
// 239.255.86.86 without) on real interfaces and a broadcast to 127.255.255.255 on loopback. Its receive socket
// listens on 0.0.0.0:8086 with SO_REUSEADDR and drops frames carrying its own source MAC.
//
// This port only uses the loopback copy: it never joins the multicast group, accepts datagrams only from
// 127.0.0.0/8, and sends only to the loopback broadcast. So frames from other machines on the home network are never
// taken in, and party frames never leave this PC.
package lswitch

import (
	"bytes"
	"context"
	"crypto/sha3"
	"errors"
	"net"
	"sync"
	"syscall"
)

const Port = 8086

// Dest is where frames for 86Box go (overridable for tests).
var Dest = &net.UDPAddr{IP: net.IPv4(127, 255, 255, 255), Port: Port}

// LSwitch is the Local Switch port.
type LSwitch struct {
	hash    []byte // nil without a secret
	rx, tx  *net.UDPConn
	txPort  int
	mu      sync.Mutex
	closed  bool
	OnFrame func(frame []byte)
	Logf    func(string, ...any)
}

// Hash is the 32-byte prefix 86Box puts before every frame for this secret ("" = none).
func Hash(secret string) []byte {
	if secret == "" {
		return nil
	}
	h := sha3.Sum256([]byte(secret))
	return h[:]
}

// Open starts listening on 0.0.0.0:8086 (shared with 86Box) and opens the loopback send socket.
func Open(secret string, logf func(string, ...any)) (*LSwitch, error) {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = reuse(fd) }); err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", "0.0.0.0:8086")
	if err != nil {
		return nil, err
	}
	rx := pc.(*net.UDPConn)
	bc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = broadcast(fd) }); err != nil {
			return err
		}
		return serr
	}}
	tpc, err := bc.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		rx.Close()
		return nil, err
	}
	tx := tpc.(*net.UDPConn)
	l := &LSwitch{hash: Hash(secret), rx: rx, tx: tx, txPort: tx.LocalAddr().(*net.UDPAddr).Port, Logf: logf}
	go l.recv()
	return l, nil
}

// Name is the switch port name.
func (l *LSwitch) Name() string { return "86box" }

// Send passes one party frame to 86Box.
func (l *LSwitch) Send(frame []byte) error {
	if len(frame) < 12 || len(frame) > 1518 {
		return errors.New("bad frame size")
	}
	pkt := frame
	if l.hash != nil {
		pkt = append(append(make([]byte, 0, 32+len(frame)), l.hash...), frame...)
	}
	_, err := l.tx.WriteToUDP(pkt, Dest)
	return err
}

// Accept decides whether a datagram from src is a frame for the switch and returns the frame.
func (l *LSwitch) Accept(src *net.UDPAddr, pkt []byte) ([]byte, bool) {
	if src == nil || !src.IP.IsLoopback() || src.Port == l.txPort {
		return nil, false // another machine, or our own loopback broadcast coming back
	}
	if l.hash != nil {
		if len(pkt) < 32+14 || !bytes.Equal(pkt[:32], l.hash) {
			return nil, false
		}
		pkt = pkt[32:]
	}
	if len(pkt) < 14 || len(pkt) > 1518 {
		return nil, false
	}
	return pkt, true
}

func (l *LSwitch) recv() {
	buf := make([]byte, 1518+32+64)
	for {
		n, src, err := l.rx.ReadFromUDP(buf)
		if err != nil {
			l.mu.Lock()
			closed := l.closed
			l.mu.Unlock()
			if !closed && l.Logf != nil {
				l.Logf("local switch: receive stopped: %v", err)
			}
			return
		}
		if f, ok := l.Accept(src, buf[:n]); ok && l.OnFrame != nil {
			out := make([]byte, len(f))
			copy(out, f)
			l.OnFrame(out)
		}
	}
}

// Close stops the port.
func (l *LSwitch) Close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.rx.Close()
	l.tx.Close()
}
