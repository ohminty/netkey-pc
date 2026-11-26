package hub

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestSTUNRTTs(t *testing.T) {
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, a, err := srv.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n >= 20 && binary.BigEndian.Uint16(buf) == 1 {
				resp := append([]byte{1, 1, 0, 0}, buf[4:20]...)
				srv.WriteToUDP(resp, a)
			}
		}
	}()
	got := STUNRTTs(map[string]string{"local": srv.LocalAddr().String(), "dead": "127.0.0.1:9"}, 3,
		300*time.Millisecond, 20*time.Millisecond)
	if _, ok := got["local"]; !ok {
		t.Fatalf("no time for the answering relay: %v", got)
	}
	if _, ok := got["dead"]; ok {
		t.Fatal("a relay that doesn't answer has no time")
	}
}
