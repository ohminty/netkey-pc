package tunnel

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// A fake host on this machine: WireGuard on 127.0.0.1, its own TCP/IP stack at 172.30.98.1 with a VXLAN socket and
// the control port. The member tunnel under test talks to it the way it talks to a host box.
func fakeHost(t *testing.T, hostPriv, memberPub string, id int) (endpoint string, frames chan []byte,
	vxSend func([]byte)) {
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{HostIP}, nil, MTU)
	if err != nil {
		t.Fatal(err)
	}
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "host: "))
	l, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	ph, _ := hexKey(hostPriv)
	mp, _ := hexKey(memberPub)
	if err := dev.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\npublic_key=%s\nallowed_ip=%s/32\n",
		ph, port, mp, MemberIP(id))); err != nil {
		t.Fatal(err)
	}
	if err := dev.Up(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dev.Close)
	vx, err := tnet.ListenUDPAddrPort(netip.AddrPortFrom(HostIP, VXPort))
	if err != nil {
		t.Fatal(err)
	}
	frames = make(chan []byte, 16)
	go func() {
		buf := make([]byte, 9000)
		for {
			n, _, err := vx.ReadFrom(buf)
			if err != nil {
				return
			}
			if binary.BigEndian.Uint32(buf[4:])>>8 != uint32(VNIBase+id) {
				continue
			}
			f := make([]byte, n-8)
			copy(f, buf[8:n])
			frames <- f
		}
	}()
	ln, err := tnet.ListenTCPAddrPort(netip.AddrPortFrom(HostIP, CtrlPort))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			if len(line) > 0 {
				c.Write([]byte(`{"status": "ok", "party": {"name": "Test host"}}` + "\n"))
			}
			c.Close()
		}
	}()
	vxSend = func(f []byte) {
		h := vxHeader(uint32(VNIBase + id))
		vx.WriteTo(append(h[:], f...), &net.UDPAddr{IP: MemberIP(id).AsSlice(), Port: VXPort})
	}
	return fmt.Sprintf("127.0.0.1:%d", port), frames, vxSend
}

func TestMemberTunnelCarriesFullSizeFramesBothWays(t *testing.T) {
	hostPriv, hostPub, _ := GenKey()
	memPriv, memPub, _ := GenKey()
	const id = 3
	ep, atHost, hostSend := fakeHost(t, hostPriv, memPub, id)
	tn, err := Start(id, memPriv, hostPub, ep, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer tn.Close()
	got := make(chan []byte, 16)
	tn.OnFrame = func(f []byte) { got <- f }

	reply, _, err := tn.Request(context.Background(), map[string]any{"op": "hello", "proto": PartyProto})
	if err != nil || reply["status"] != "ok" {
		t.Fatalf("control request: %v %v", reply, err)
	}
	for _, size := range []int{60, 1514} { // 1514 + VXLAN 50 > MTU 1380: has to travel as IP fragments
		f := make([]byte, size)
		for i := range f {
			f[i] = byte(i)
		}
		if err := tn.Send(f); err != nil {
			t.Fatal(err)
		}
		select {
		case h := <-atHost:
			if len(h) != size || h[size-1] != byte(size-1) {
				t.Fatalf("host got %d bytes, want %d", len(h), size)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("a %d-byte frame never reached the host", size)
		}
		hostSend(f)
		select {
		case m := <-got:
			if len(m) != size {
				t.Fatalf("member got %d bytes, want %d", len(m), size)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("a %d-byte frame from the host never arrived", size)
		}
	}
	if tn.LastHandshake().IsZero() {
		t.Error("no handshake time after traffic")
	}
}

func TestKeys(t *testing.T) {
	priv, pub, err := GenKey()
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := PubKey(priv); p != pub {
		t.Fatal("PubKey disagrees with GenKey")
	}
	// RFC 7748 test vector (Alice).
	if p, _ := PubKey("dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo="); p != "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo=" {
		t.Fatalf("X25519 vector: %s", p)
	}
}
