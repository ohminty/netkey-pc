package member

import (
	"path/filepath"
	"strings"
	"testing"

	"netkey.no/pc/internal/hub"
	"netkey.no/pc/internal/sw"
)

func joining(t *testing.T) *Member {
	m := New(nil, sw.New(), filepath.Join(t.TempDir(), "party.json"), t.Logf)
	m.st = State{Status: "joining", HubParty: "p1", HostPub: "HOSTKEY", PartyName: "Host", Pub: "ME", Name: "PC"}
	return m
}

func TestApprovalWithAnotherHostKeyIsRefused(t *testing.T) {
	m := joining(t)
	m.handle(hub.Event{Seq: 1, Type: "approved", Party: "p1", HostPub: "OTHERKEY", MemberID: 2, RelayPort: 20001})
	if m.st.Status != "" {
		t.Fatal("an approval with a host key other than the code's must end the join")
	}
	if !strings.Contains(strings.Join(m.notes, " "), "different host key") {
		t.Fatal(m.notes)
	}
}

func TestApprovalAndRelayMove(t *testing.T) {
	m := joining(t)
	m.handle(hub.Event{Seq: 1, Type: "approved", Party: "other", HostPub: "HOSTKEY", MemberID: 2, RelayPort: 20001})
	if m.st.Status != "joining" {
		t.Fatal("events for another party must be ignored")
	}
	m.handle(hub.Event{Seq: 2, Type: "approved", Party: "p1", HostPub: "HOSTKEY", MemberID: 99, RelayPort: 20001})
	if m.st.Status != "joining" {
		t.Fatal("a member id out of range must be ignored")
	}
	m.handle(hub.Event{Seq: 3, Type: "approved", Party: "p1", HostPub: "HOSTKEY", MemberID: 2, RelayPort: 20001,
		RelayHost: "203.0.113.7", Relay: "eu-no"})
	if m.st.Status != "approved" || m.st.ID != 2 || m.st.RelayHost != "203.0.113.7" || m.st.RelayName != "eu-no" {
		t.Fatalf("%+v", m.st)
	}
	m.handle(hub.Event{Seq: 4, Type: "relay_moved", Party: "p1", RelayPort: 20002, RelayHost: "not-an-ip"})
	if m.st.RelayPort != 20001 {
		t.Fatal("a bad relay address must be ignored")
	}
	m.handle(hub.Event{Seq: 5, Type: "relay_moved", Party: "p1", RelayPort: 20002, RelayHost: "", Relay: ""})
	if m.st.RelayPort != 20002 || m.st.RelayHost != "" {
		t.Fatal("a move to the hub's own relay must be taken")
	}
	if m.st.HubSeq != 5 {
		t.Fatalf("seq %d", m.st.HubSeq)
	}
	m.handle(hub.Event{Seq: 6, Type: "kicked", Party: "p1"})
	if m.st.Status != "" {
		t.Fatal("kicked must end the membership")
	}
	// the state file is gone too: a restart doesn't rejoin
	if New(nil, sw.New(), m.path, t.Logf).st.Status != "" {
		t.Fatal("the saved state must be cleared")
	}
}

func TestRejectedKeepsTheReason(t *testing.T) {
	m := joining(t)
	m.handle(hub.Event{Seq: 1, Type: "rejected", Party: "p1", Reason: "not tonight\x07"})
	if m.st.Status != "" || !strings.Contains(strings.Join(m.notes, " "), "Join refused: not tonight") {
		t.Fatal(m.notes)
	}
}

func TestCleanName(t *testing.T) {
	if got := CleanName("  Ada\x00 PC\n" + strings.Repeat("x", 40)); got != "Ada PCxxxxxxxxxxxxxxxxxxxxxxxxxx" {
		t.Fatalf("%q (%d)", got, len(got))
	}
}

func TestRoom(t *testing.T) {
	if (State{}).Room() != "" {
		t.Fatal("no room outside a party")
	}
	if r := (State{Status: "approved", HubParty: "ab12"}).Room(); r != "p:ab12" {
		t.Fatal(r)
	}
	if r := (State{Status: "joining", HubParty: "ab12", Public: true, Listing: "f00d"}).Room(); r != "l:f00d" {
		t.Fatal(r)
	}
}
