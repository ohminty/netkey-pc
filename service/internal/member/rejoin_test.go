package member

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"netkey.no/pc/internal/hub"
	"netkey.no/pc/internal/sw"
	"netkey.no/pc/internal/tunnel"
)

// fakeHub serves the few hub calls a join needs, with an event list per test.
type fakeHub struct {
	mu      sync.Mutex
	events  []map[string]any
	hostPub string
}

func (f *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]any{}
	switch r.URL.Path {
	case "/v1/register":
		out = map[string]any{"box": "nk-test", "token": "t"}
	case "/v1/events":
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		evs := []map[string]any{}
		for _, e := range f.events {
			if int64(e["seq"].(int)) > after {
				evs = append(evs, e)
			}
		}
		out = map[string]any{"events": evs, "last": after}
	case "/v1/join":
		out = map[string]any{"party": "p1", "party_name": "Host", "host_pub": f.hostPub, "status": "pending"}
	}
	json.NewEncoder(w).Encode(out)
}

func TestRejoiningIgnoresTheOldApproval(t *testing.T) {
	_, hostPub, _ := tunnel.GenKey()
	fh := &fakeHub{hostPub: hostPub, events: []map[string]any{
		// from an earlier membership of the same party: must not approve the new request
		{"seq": 4, "type": "approved", "party": "p1", "member_id": 1, "relay_port": 20001, "host_pub": hostPub},
		{"seq": 5, "type": "left", "party": "p1"},
	}}
	srv := httptest.NewTLSServer(fh)
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	dir := t.TempDir()
	h, err := hub.New(srv.URL, hex.EncodeToString(sum[:]), "PC", "", filepath.Join(dir, "hub.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(h, sw.New(), filepath.Join(dir, "party.json"), t.Logf)

	fp, _ := hub.KeyFingerprint(hostPub, 24)
	if _, err := m.Join(context.Background(), "KEY-0011ABCD"+fp, "PC"); err != nil {
		t.Fatal(err)
	}
	if m.st.Status != "joining" || m.st.HubSeq != 5 {
		t.Fatalf("status %q seq %d: the old events must be skipped before joining", m.st.Status, m.st.HubSeq)
	}
	// what the event loop would get next: nothing old
	evs, err := h.Events(context.Background(), m.st.HubSeq)
	if err != nil || len(evs) != 0 {
		t.Fatalf("events after the join: %v %v", evs, err)
	}
	// the real approval arrives
	m.handle(hub.Event{Seq: 6, Type: "approved", Party: "p1", HostPub: hostPub, MemberID: 2, RelayPort: 20002})
	if m.st.Status != "approved" || m.st.ID != 2 {
		t.Fatalf("%+v", m.st)
	}
	// leaving keeps the sequence number for the next party
	m.drop("left")
	if m.st.HubSeq != 6 || m.st.Status != "" {
		t.Fatalf("after leaving: %+v", m.st)
	}
}
