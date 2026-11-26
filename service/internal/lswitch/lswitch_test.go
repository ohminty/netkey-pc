package lswitch

import (
	"encoding/hex"
	"net"
	"testing"
)

func TestHashIsSHA3OfTheSecret(t *testing.T) {
	// SHA3-256("abc"), FIPS 202 test vector: what 86Box's net_switch_secret_hash computes for the secret "abc".
	if got := hex.EncodeToString(Hash("abc")); got != "3a985da74fe225b2045c172d6bd390bd855f086e3e9d525b46bfe24511431532" {
		t.Fatal(got)
	}
	if Hash("") != nil {
		t.Fatal("no secret, no prefix")
	}
}

func TestAccept(t *testing.T) {
	l := &LSwitch{hash: Hash("s3cret"), txPort: 50000}
	frame := make([]byte, 60)
	pkt := append(append([]byte{}, l.hash...), frame...)
	lo := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 61000}
	if f, ok := l.Accept(lo, pkt); !ok || len(f) != 60 {
		t.Fatal("a frame from 86Box on this PC with the right secret must be taken")
	}
	if _, ok := l.Accept(&net.UDPAddr{IP: net.IPv4(192, 168, 1, 9), Port: 61000}, pkt); ok {
		t.Fatal("frames from other machines must be dropped")
	}
	if _, ok := l.Accept(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50000}, pkt); ok {
		t.Fatal("our own broadcast coming back must be dropped")
	}
	if _, ok := l.Accept(lo, append(Hash("other"), frame...)); ok {
		t.Fatal("another secret must be dropped")
	}
	if _, ok := l.Accept(lo, frame); ok {
		t.Fatal("no prefix while a secret is set must be dropped")
	}
	plain := &LSwitch{txPort: 50000}
	if _, ok := plain.Accept(lo, frame); !ok {
		t.Fatal("without a secret a bare frame is taken (QEMU)")
	}
}
