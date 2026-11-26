// Package tunnel is the member end of a party: WireGuard to the host (in-process, wireguard-go with its own
// TCP/IP stack, so no driver), VXLAN for the retro frames and the host's JSON control port.
//
// It matches what a member box sets up (netkey/lan.py member_reconcile): one WireGuard peer, the host at
// 172.30.98.1/32 behind the relay endpoint, keepalive 15 s, MTU 1380; a VXLAN port with VNI 1000+id from the member's
// tunnel address to the host's port 4789. Frames up to 1514 bytes don't fit the MTU after VXLAN's 50 bytes; the
// TCP/IP stack splits those packets into IP fragments inside the tunnel, as the box's kernel does (VXLAN df unset).
package tunnel

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

const (
	MTU        = 1380 // netkey/party.py WG_MTU
	VXPort     = 4789
	CtrlPort   = 7999
	VNIBase    = 1000
	Keepalive  = 15
	PartyProto = 1 // netkey/party.py PROTO (the control messages)
)

// Tunnel addresses: netkey/config.py lan.tunnel 172.30.98.0/24, host .1, members .10+id.
var (
	HostIP = netip.MustParseAddr("172.30.98.1")
)

// MemberIP is the tunnel address of member id.
func MemberIP(id int) netip.Addr {
	return netip.AddrFrom4([4]byte{172, 30, 98, byte(10 + id)})
}

// GenKey returns a new WireGuard key pair (base64, like `wg genkey` / `wg pubkey`).
func GenKey() (priv, pub string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return
	}
	raw[0] &= 248
	raw[31] = (raw[31] & 127) | 64
	return base64.StdEncoding.EncodeToString(raw), pubOf(raw), nil
}

// PubKey returns the public key for a base64 private key.
func PubKey(priv string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(priv)
	if err != nil || len(raw) != 32 {
		return "", errors.New("bad private key")
	}
	return pubOf(raw), nil
}

func pubOf(raw []byte) string {
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

func hexKey(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != 32 {
		return "", errors.New("bad WireGuard key")
	}
	return hex.EncodeToString(raw), nil
}

// Tunnel is one running member tunnel.
type Tunnel struct {
	ID      int
	hostPub string
	dev     *device.Device
	tnet    *netstack.Net
	vx      *gonet.UDPConn
	vni     uint32
	hostVX  *net.UDPAddr

	mu      sync.Mutex
	closed  bool
	OnFrame func(frame []byte) // frames from the party (set before Start's receive loop gets any)
}

// Start brings up WireGuard to the host at endpoint ("ip:port") as member id.
func Start(id int, priv, hostPub, endpoint string, logf func(string, ...any)) (*Tunnel, error) {
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{MemberIP(id)}, nil, MTU)
	if err != nil {
		return nil, err
	}
	lg := &device.Logger{Verbosef: device.DiscardLogf, Errorf: func(f string, a ...any) { logf("wireguard: "+f, a...) }}
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), lg)
	privHex, err := hexKey(priv)
	if err != nil {
		dev.Close()
		return nil, err
	}
	pubHex, err := hexKey(hostPub)
	if err != nil {
		dev.Close()
		return nil, err
	}
	conf := fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=%s/32\npersistent_keepalive_interval=%d\n",
		privHex, pubHex, endpoint, HostIP, Keepalive)
	if err := dev.IpcSet(conf); err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	vx, err := tnet.ListenUDPAddrPort(netip.AddrPortFrom(MemberIP(id), VXPort))
	if err != nil {
		dev.Close()
		return nil, err
	}
	t := &Tunnel{ID: id, hostPub: hostPub, dev: dev, tnet: tnet, vx: vx, vni: uint32(VNIBase + id),
		hostVX: &net.UDPAddr{IP: HostIP.AsSlice(), Port: VXPort}}
	go t.recv(logf)
	return t, nil
}

// SetEndpoint moves the host peer to a new relay endpoint (the hub's relay_moved), without a re-join.
func (t *Tunnel) SetEndpoint(endpoint string) error {
	pubHex, _ := hexKey(t.hostPub)
	return t.dev.IpcSet(fmt.Sprintf("public_key=%s\nupdate_only=true\nendpoint=%s\n", pubHex, endpoint))
}

// LastHandshake is the time of the last WireGuard handshake with the host (zero before the first).
func (t *Tunnel) LastHandshake() time.Time {
	s, err := t.dev.IpcGet()
	if err != nil {
		return time.Time{}
	}
	var sec, nsec int64
	for _, line := range splitLines(s) {
		fmt.Sscanf(line, "last_handshake_time_sec=%d", &sec)
		fmt.Sscanf(line, "last_handshake_time_nsec=%d", &nsec)
	}
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, nsec)
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// VXLAN header (RFC 7348): flags 0x08 (VNI valid), 3 reserved bytes, VNI (24 bits), 1 reserved byte.
func vxHeader(vni uint32) [8]byte {
	var h [8]byte
	h[0] = 0x08
	binary.BigEndian.PutUint32(h[4:], vni<<8)
	return h
}

// Send puts one Ethernet frame (no FCS) on the party.
func (t *Tunnel) Send(frame []byte) error {
	if len(frame) < 14 || len(frame) > 1518 {
		return errors.New("bad frame size")
	}
	h := vxHeader(t.vni)
	pkt := make([]byte, 0, 8+len(frame))
	pkt = append(pkt, h[:]...)
	pkt = append(pkt, frame...)
	_, err := t.vx.WriteTo(pkt, t.hostVX)
	return err
}

func (t *Tunnel) recv(logf func(string, ...any)) {
	buf := make([]byte, 9000)
	for {
		n, addr, err := t.vx.ReadFrom(buf)
		if err != nil {
			t.mu.Lock()
			closed := t.closed
			t.mu.Unlock()
			if !closed {
				logf("tunnel: VXLAN receive stopped: %v", err)
			}
			return
		}
		ua, ok := addr.(*net.UDPAddr)
		if !ok || !ua.IP.Equal(t.hostVX.IP) || n < 8+14 || buf[0]&0x08 == 0 {
			continue
		}
		if binary.BigEndian.Uint32(buf[4:])>>8 != t.vni {
			continue
		}
		if f := t.OnFrame; f != nil {
			frame := make([]byte, n-8)
			copy(frame, buf[8:n])
			f(frame)
		}
	}
}

// Request sends one JSON line to the host's control port and returns its reply plus the TCP connect time
// (the round trip through the tunnel, like the box's hello rtt).
func (t *Tunnel) Request(ctx context.Context, msg any) (map[string]any, float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	t0 := time.Now()
	c, err := t.tnet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(HostIP, CtrlPort))
	if err != nil {
		return nil, 0, err
	}
	defer c.Close()
	rtt := float64(time.Since(t0).Microseconds()) / 1000
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	b, _ := json.Marshal(msg)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, rtt, err
	}
	line, err := bufio.NewReaderSize(c, 65536).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, rtt, err
	}
	var reply map[string]any
	if err := json.Unmarshal(line, &reply); err != nil {
		return nil, rtt, errors.New("bad reply from the host")
	}
	return reply, rtt, nil
}

// Close tears the tunnel down.
func (t *Tunnel) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	t.vx.Close()
	t.dev.Close()
}
