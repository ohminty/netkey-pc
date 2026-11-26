package ipx

import (
	"bytes"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeDOSBox is a UDP socket that talks to the server like DOSBox's emulated IPX driver.
type fakeDOSBox struct {
	t    *testing.T
	conn *net.UDPConn
	node [6]byte
}

func newDOSBox(t *testing.T, srv *net.UDPAddr) *fakeDOSBox {
	c, err := net.DialUDP("udp4", nil, srv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &fakeDOSBox{t: t, conn: c}
}

func (d *fakeDOSBox) register(extraMAC bool) {
	b := make([]byte, headerLen)
	binary.BigEndian.PutUint16(b[0:], 0xffff)
	binary.BigEndian.PutUint16(b[2:], headerLen)
	binary.BigEndian.PutUint16(b[16:], controlSock)
	binary.BigEndian.PutUint16(b[28:], controlSock)
	if extraMAC { // DOSBox-X with an NE2000 card appends its MAC and sets transport control to 'M'
		b[4] = 'M'
		b = append(b, 0xac, 0xde, 0x48, 0x11, 0x22, 0x33)
	}
	d.conn.Write(b)
	r := d.read(time.Second)
	if r == nil || len(r) != headerLen || binary.BigEndian.Uint16(r[16:18]) != controlSock {
		d.t.Fatalf("bad registration reply %x", r)
	}
	d.node = dstNode(r)
}

func (d *fakeDOSBox) read(timeout time.Duration) []byte {
	d.conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	n, err := d.conn.Read(buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

func packet(src, dst [6]byte, sock uint16, data string) []byte {
	b := make([]byte, headerLen+len(data))
	binary.BigEndian.PutUint16(b[0:], 0xffff)
	binary.BigEndian.PutUint16(b[2:], uint16(len(b)))
	copy(b[10:16], dst[:])
	binary.BigEndian.PutUint16(b[16:], sock)
	copy(b[22:28], src[:])
	binary.BigEndian.PutUint16(b[28:], sock)
	copy(b[headerLen:], data)
	return b
}

type frames struct {
	mu  sync.Mutex
	got [][]byte
}

func (f *frames) add(b []byte) { f.mu.Lock(); f.got = append(f.got, b); f.mu.Unlock() }
func (f *frames) list() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.got...)
}

func start(t *testing.T) (*Port, *frames) {
	p, err := Open("127.0.0.1:0", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	fr := &frames{}
	p.OnFrame = fr.add
	return p, fr
}

const doomSock = 0x869c

func TestRegistrationGivesEachDOSBoxItsOwnNode(t *testing.T) {
	p, _ := start(t)
	a, b := newDOSBox(t, p.Addr()), newDOSBox(t, p.Addr())
	a.register(false)
	b.register(true) // DOSBox-X's NE2000 MAC is not reused (it may also be on the NetKey Adapter)
	if a.node == b.node || a.node[0] != 0x02 || a.node[1] != 0x4e || a.node[2] != 0x4b || b.node[3] == 0x11 && b.node[4] == 0x22 {
		t.Fatalf("nodes %x %x", a.node, b.node)
	}
	old := a.node
	a.register(false)
	if a.node != old {
		t.Fatal("re-registering must keep the node")
	}
	if len(p.Clients()) != 2 {
		t.Fatal(p.Clients())
	}
}

func TestBroadcastReachesOtherDOSBoxAndTheParty(t *testing.T) {
	p, fr := start(t)
	a, b := newDOSBox(t, p.Addr()), newDOSBox(t, p.Addr())
	a.register(false)
	b.register(false)
	a.conn.Write(packet(a.node, broadcast, doomSock, "who's there"))
	got := b.read(time.Second)
	if !bytes.Contains(got, []byte("who's there")) {
		t.Fatalf("b got %q", got)
	}
	if a.read(100*time.Millisecond) != nil {
		t.Fatal("a broadcast must not come back to its sender")
	}
	time.Sleep(50 * time.Millisecond)
	fs := fr.list()
	if len(fs) != 1 {
		t.Fatalf("%d frames to the party, want 1 (802.2 only, nothing else seen yet)", len(fs))
	}
	ft, pkt, ok := Decode(fs[0])
	if !ok || ft != Ethernet8022 || !bytes.Equal(fs[0][0:6], broadcast[:]) || !bytes.Equal(fs[0][6:12], a.node[:]) ||
		!bytes.Contains(pkt, []byte("who's there")) {
		t.Fatalf("frame %x", fs[0])
	}
}

func TestUnicastBetweenLocalDOSBoxStaysLocal(t *testing.T) {
	p, fr := start(t)
	a, b, c := newDOSBox(t, p.Addr()), newDOSBox(t, p.Addr()), newDOSBox(t, p.Addr())
	a.register(false)
	b.register(false)
	c.register(false)
	a.conn.Write(packet(a.node, b.node, doomSock, "for b"))
	if got := b.read(time.Second); !bytes.Contains(got, []byte("for b")) {
		t.Fatalf("b got %q", got)
	}
	if c.read(100*time.Millisecond) != nil {
		t.Fatal("DOSBox doesn't filter by node: a unicast must reach only its destination")
	}
	if len(fr.list()) != 0 {
		t.Fatal("a unicast between two DOSBox on this PC must not go to the party")
	}
}

func TestPartyTrafficAndFrameTypes(t *testing.T) {
	p, fr := start(t)
	a := newDOSBox(t, p.Addr())
	a.register(false)
	retro := [6]byte{0x00, 0xa0, 0x24, 0x01, 0x02, 0x03}
	// a retro PC on the party, Ethernet II: its broadcast reaches DOSBox
	f, _ := Encode(EthernetII, broadcast, retro, packet(retro, broadcast, doomSock, "hello from DOS"))
	p.Send(f)
	if got := a.read(time.Second); !bytes.Contains(got, []byte("hello from DOS")) {
		t.Fatalf("a got %q", got)
	}
	// unicast reply goes out in the retro PC's own frame type
	a.conn.Write(packet(a.node, retro, doomSock, "reply"))
	time.Sleep(50 * time.Millisecond)
	fs := fr.list()
	if len(fs) != 1 {
		t.Fatalf("%d frames", len(fs))
	}
	if ft, _, _ := Decode(fs[0]); ft != EthernetII || !bytes.Equal(fs[0][0:6], retro[:]) {
		t.Fatalf("unicast went out as %v to %x", ft, fs[0][0:6])
	}
	// a broadcast now goes out in 802.2 and Ethernet II (seen), one copy each
	a.conn.Write(packet(a.node, broadcast, doomSock, "all"))
	time.Sleep(50 * time.Millisecond)
	fs = fr.list()[1:]
	if len(fs) != 2 {
		t.Fatalf("%d broadcast copies, want 2", len(fs))
	}
	t1, _, _ := Decode(fs[0])
	t2, _, _ := Decode(fs[1])
	if t1 != Ethernet8022 || t2 != EthernetII {
		t.Fatalf("copies %v %v", t1, t2)
	}
	// a unicast for someone else on the party isn't delivered to DOSBox
	other := [6]byte{0x00, 0xa0, 0x24, 0x09, 0x09, 0x09}
	f, _ = Encode(Ethernet8022, other, retro, packet(retro, other, doomSock, "not for you"))
	p.Send(f)
	if a.read(100*time.Millisecond) != nil {
		t.Fatal("a unicast for another node must not reach DOSBox")
	}
}

func TestControlSocketNeverLeavesAndSpoofingIsDropped(t *testing.T) {
	p, fr := start(t)
	a, b := newDOSBox(t, p.Addr()), newDOSBox(t, p.Addr())
	a.register(false)
	b.register(false)
	a.conn.Write(packet(a.node, broadcast, controlSock, "ping")) // IPXNET PING
	if got := b.read(time.Second); !bytes.Contains(got, []byte("ping")) {
		t.Fatal("pings must reach the other DOSBox on this PC")
	}
	retro := [6]byte{0x00, 0xa0, 0x24, 0x01, 0x02, 0x03}
	f, _ := Encode(Ethernet8022, broadcast, retro, packet(retro, broadcast, controlSock, "echo"))
	p.Send(f)
	a.conn.Write(packet(b.node, broadcast, doomSock, "spoofed")) // a's socket claims to be b
	if b.read(150*time.Millisecond) != nil || a.read(50*time.Millisecond) != nil {
		t.Fatal("socket 2 from the party and spoofed packets must be dropped")
	}
	time.Sleep(50 * time.Millisecond)
	if len(fr.list()) != 0 {
		t.Fatalf("%d frames to the party, want none", len(fr.list()))
	}
}

func TestAdoptionAndExpiry(t *testing.T) {
	p, fr := start(t)
	a := newDOSBox(t, p.Addr())
	node := [6]byte{0x02, 0x4e, 0x4b, 0xaa, 0xbb, 0xcc} // registered with a NetKey service that has since restarted
	a.conn.Write(packet(node, broadcast, doomSock, "still here"))
	time.Sleep(50 * time.Millisecond)
	if len(p.Clients()) != 1 || len(fr.list()) != 1 {
		t.Fatalf("clients %v frames %d: a DOSBox sending with its node must be picked up", p.Clients(), len(fr.list()))
	}
	now := time.Now()
	p.mu.Lock()
	p.now = func() time.Time { return now.Add(expireAfter + time.Minute) }
	p.expireLocked()
	p.mu.Unlock()
	if len(p.Clients()) != 0 {
		t.Fatal("silent clients must be forgotten")
	}
}

func TestEncodeDecodeAllFrameTypes(t *testing.T) {
	src, dst := [6]byte{2, 0, 0, 0, 0, 1}, [6]byte{2, 0, 0, 0, 0, 2}
	small := packet(src, dst, doomSock, "x")
	for _, ft := range []FrameType{Ethernet8022, EthernetII, Ethernet8023, EthernetSNAP} {
		f, ok := Encode(ft, dst, src, small)
		if !ok || len(f) < minFrame {
			t.Fatalf("%v: encode", ft)
		}
		gt, pkt, ok := Decode(f)
		if !ok || gt != ft || !bytes.Equal(pkt, small) {
			t.Fatalf("%v: decoded %v %x", ft, gt, pkt)
		}
	}
	big := packet(src, dst, doomSock, string(make([]byte, 1497-headerLen)))
	if _, ok := Encode(Ethernet8022, dst, src, big); !ok {
		t.Fatal("1497 bytes fit 802.2")
	}
	if _, ok := Encode(EthernetSNAP, dst, src, big); ok {
		t.Fatal("1497 bytes don't fit SNAP (1492)")
	}
	if _, _, ok := Decode(make([]byte, 60)); ok {
		t.Fatal("not IPX")
	}
}
