package sw

import (
	"testing"
	"time"
)

type fakePort struct {
	name string
	got  [][]byte
}

func (p *fakePort) Name() string { return p.name }
func (p *fakePort) Send(f []byte) error {
	p.got = append(p.got, f)
	return nil
}

func frame(dst, src byte) []byte {
	f := make([]byte, 60)
	for i := 0; i < 6; i++ {
		f[i] = dst
		f[6+i] = src
	}
	if dst == 0xff {
		return f
	}
	f[0] = dst & 0xfe
	f[6] = src & 0xfe
	return f
}

func TestFloodLearnAndNoEcho(t *testing.T) {
	s := New()
	a, b, c := &fakePort{name: "party"}, &fakePort{name: "tap"}, &fakePort{name: "86box"}
	s.Add(a)
	s.Add(b)
	s.Add(c)
	s.Input(b, frame(0xff, 0x02)) // broadcast from the TAP: everyone but the TAP
	if len(a.got) != 1 || len(c.got) != 1 || len(b.got) != 0 {
		t.Fatalf("broadcast: %d %d %d", len(a.got), len(b.got), len(c.got))
	}
	s.Input(a, frame(0x02, 0x04)) // reply from the party to the learned MAC: only the TAP
	if len(b.got) != 1 || len(c.got) != 1 {
		t.Fatalf("unicast to a learned MAC went to %d/%d", len(b.got), len(c.got))
	}
	s.Input(c, frame(0x08, 0x06)) // unknown unicast: flooded, not back to its port
	if len(a.got) != 2 || len(b.got) != 2 || len(c.got) != 1 {
		t.Fatalf("unknown unicast: %d %d %d", len(a.got), len(b.got), len(c.got))
	}
	s.Input(b, frame(0x02, 0x02)) // to itself on the same port: dropped
	if len(a.got) != 2 || len(c.got) != 1 {
		t.Fatal("a frame for the port it came from must not go anywhere")
	}
}

func TestDuplicateMACIsReported(t *testing.T) {
	s := New()
	a, b := &fakePort{name: "party"}, &fakePort{name: "86box"}
	s.Add(a)
	s.Add(b)
	var moves int
	s.OnMove = func(mac [6]byte, from, to string) { moves++ }
	s.Input(a, frame(0xff, 0x0a))
	s.Input(b, frame(0xff, 0x0a))
	s.Input(a, frame(0xff, 0x0a))
	if moves != 1 {
		t.Fatalf("moves = %d, want 1 (once a minute)", moves)
	}
}

func TestAgedEntriesFlood(t *testing.T) {
	s := New()
	now := time.Now()
	s.now = func() time.Time { return now }
	a, b, c := &fakePort{name: "a"}, &fakePort{name: "b"}, &fakePort{name: "c"}
	s.Add(a)
	s.Add(b)
	s.Add(c)
	s.Input(b, frame(0xff, 0x02))
	now = now.Add(ageOut + time.Second)
	s.Input(a, frame(0x02, 0x04))
	if len(c.got) != 2 {
		t.Fatal("an aged-out MAC must be flooded again")
	}
}
