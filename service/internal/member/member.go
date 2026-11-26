// Package member is the app's part in a party: it joins through the hub, waits for the host's approval, runs the
// tunnel and says hello to the host every few seconds. It follows the member side of netkey/lan.py (via == "hub").
package member

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"netkey.no/pc/internal/hub"
	"netkey.no/pc/internal/sw"
	"netkey.no/pc/internal/tunnel"
)

const (
	helloEvery = 5 * time.Second // netkey/party.py HELLO_S
	maxMembers = 15
)

// State is what survives a restart (party.json).
type State struct {
	Status    string  `json:"status"` // "" (in no party), "joining", "approved"
	HubParty  string  `json:"hub_party"`
	PartyName string  `json:"party_name"`
	HostPub   string  `json:"host_pub"`
	Priv      string  `json:"priv"`
	Pub       string  `json:"pub"`
	Name      string  `json:"name"`
	HubSeq    int64   `json:"hub_seq"`
	ID        int     `json:"id"`
	RelayPort int     `json:"relay_port"`
	RelayHost string  `json:"relay_host"`
	RelayName string  `json:"relay_name"`
	JoinedAt  float64 `json:"joined_at"`
	Public    bool    `json:"public"`  // joined from the public party list
	Listing   string  `json:"listing"` // its listing ID (chat room l:<listing>)
}

// Room is the party's chat room at the hub ("" outside a party).
func (s State) Room() string {
	switch {
	case s.Status == "":
		return ""
	case s.Public && s.Listing != "":
		return "l:" + s.Listing
	default:
		return "p:" + s.HubParty
	}
}

// Status is a snapshot for the GUI and the command line.
type Status struct {
	State
	Online    bool           // hellos answered
	RTTms     float64        // last hello round trip
	Error     string         // last problem talking to the host
	View      map[string]any // the host's party view (name, members, shared files)
	HubOnline bool
	HubError  string
	Handshake time.Time
	LastOK    time.Time // last answered hello (zero: none yet in this run)
	Notes     []string
}

// Member runs one membership.
type Member struct {
	hub  *hub.Client
	sw   *sw.Switch
	path string
	logf func(string, ...any)

	mu        sync.Mutex
	st        State
	tun       *tunnel.Tunnel
	port      *partyPort
	online    bool
	rtt       float64
	err       string
	view      map[string]any
	hubOnline bool
	hubErr    string
	lastHello time.Time
	lastOK    time.Time
	notes     []string
	resolved  string
}

// New loads the saved state (if any).
func New(h *hub.Client, s *sw.Switch, statePath string, logf func(string, ...any)) *Member {
	m := &Member{hub: h, sw: s, path: statePath, logf: logf}
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, &m.st)
	}
	return m
}

func (m *Member) save() {
	b, _ := json.MarshalIndent(m.st, "", "  ")
	tmp := m.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, m.path)
	}
}

func (m *Member) note(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	m.logf("%s", s)
	m.notes = append(m.notes, s)
	if len(m.notes) > 20 {
		m.notes = m.notes[len(m.notes)-20:]
	}
}

// CleanName is netkey/party.py clean_name: printable, at most 32 characters.
func CleanName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsPrint(r) && n < 32 {
			b.WriteRune(r)
			n++
		}
	}
	return b.String()
}

// Status returns a snapshot.
func (m *Member) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Status{State: m.st, Online: m.online, RTTms: m.rtt, Error: m.err, View: m.view, HubOnline: m.hubOnline,
		HubError: m.hubErr, LastOK: m.lastOK, Notes: append([]string(nil), m.notes...)}
	s.Priv = ""
	if m.tun != nil {
		s.Handshake = m.tun.LastHandshake()
	}
	return s
}

// Join asks the host of the party behind an invite code (KEY-...) to let this app in.
func (m *Member) Join(ctx context.Context, code, name string) (string, error) {
	m.mu.Lock()
	busy := m.st.Status != ""
	m.mu.Unlock()
	if busy {
		return "", errors.New("already in a party; leave it first")
	}
	if !hub.IsCode(code) {
		return "", errors.New("that is not a NetKey party code (it starts with KEY-)")
	}
	_, fp, err := hub.ParseCode(code)
	if err != nil {
		return "", err
	}
	name = CleanName(name)
	if name == "" {
		return "", errors.New("choose a name the host will see")
	}
	priv, pub, err := tunnel.GenKey()
	if err != nil {
		return "", err
	}
	var r struct {
		Party     string `json:"party"`
		PartyName string `json:"party_name"`
		HostPub   string `json:"host_pub"`
		Status    string `json:"status"`
	}
	seq, err := m.skipOldEvents(ctx)
	if err != nil {
		return "", err
	}
	if err := m.hub.Call(ctx, "POST", "/v1/join", map[string]any{"code": code, "pub": pub, "name": name}, &r); err != nil {
		return "", err
	}
	if got, err := hub.KeyFingerprint(r.HostPub, 24); err != nil || got != fp {
		return "", errors.New("the hub returned a host key that doesn't match the code's fingerprint; refused")
	}
	if r.Status != "pending" {
		return "", errors.New("this app is already in that party")
	}
	m.mu.Lock()
	m.st = State{Status: "joining", HubParty: r.Party, PartyName: CleanName(r.PartyName), HostPub: r.HostPub,
		Priv: priv, Pub: pub, Name: name, HubSeq: seq}
	m.save()
	m.notes = nil
	m.note("Asked %s to let you in. The host sees the key check %s", m.st.PartyName, hub.VerifyWords(pub))
	m.mu.Unlock()
	return fmt.Sprintf("Asked %s to let you in; the host sees the key check %s", CleanName(r.PartyName),
		hub.VerifyWords(pub)), nil
}

// JoinListing asks to join a public party from the directory (no code: the hub hands out the host's key, and the
// host sees the key check next to the nickname).
func (m *Member) JoinListing(ctx context.Context, listing, name string) (string, error) {
	m.mu.Lock()
	busy := m.st.Status != ""
	m.mu.Unlock()
	if busy {
		return "", errors.New("already in a party; leave it first")
	}
	listing = strings.TrimSpace(listing)
	if listing == "" || len(listing) > 16 {
		return "", errors.New("pick a party from the list")
	}
	priv, pub, err := tunnel.GenKey()
	if err != nil {
		return "", err
	}
	var r struct {
		Party     string `json:"party"`
		PartyName string `json:"party_name"`
		HostPub   string `json:"host_pub"`
		Status    string `json:"status"`
		Listing   string `json:"listing"`
	}
	seq, err := m.skipOldEvents(ctx)
	if err != nil {
		return "", err
	}
	if err := m.hub.Call(ctx, "POST", "/v1/join", map[string]any{"listing": listing, "pub": pub, "name": CleanName(name)},
		&r); err != nil {
		return "", err
	}
	if _, err := hub.KeyFingerprint(r.HostPub, 24); err != nil {
		return "", errors.New("bad host key from the hub")
	}
	if r.Status != "pending" {
		return "", errors.New("this app is already in that party")
	}
	m.mu.Lock()
	m.st = State{Status: "joining", HubParty: r.Party, PartyName: CleanName(r.PartyName), HostPub: r.HostPub,
		Priv: priv, Pub: pub, Name: CleanName(name), Public: true, Listing: r.Listing, HubSeq: seq}
	if m.st.Listing == "" {
		m.st.Listing = listing
	}
	m.save()
	m.notes = nil
	m.note("Asked to join %s. The host sees the key check %s", m.st.PartyName, hub.VerifyWords(pub))
	m.mu.Unlock()
	return fmt.Sprintf("Asked to join %s", CleanName(r.PartyName)), nil
}

// skipOldEvents moves past the hub events already waiting, so that only answers to the new request count.
func (m *Member) skipOldEvents(ctx context.Context) (int64, error) {
	m.mu.Lock()
	after := m.st.HubSeq
	m.mu.Unlock()
	seq, err := m.hub.SkipEvents(ctx, after)
	if err != nil {
		return after, err
	}
	return seq, nil
}

// Leave leaves the party (or cancels a join request).
func (m *Member) Leave(ctx context.Context) error {
	m.mu.Lock()
	st, tun := m.st, m.tun
	m.mu.Unlock()
	if st.Status == "" {
		return nil
	}
	if err := m.hub.Call(ctx, "POST", "/v1/leave", map[string]any{}, nil); err != nil {
		m.logf("hub leave: %v", err)
	}
	if tun != nil && st.Status == "approved" {
		_, _, _ = tun.Request(ctx, map[string]any{"op": "leave", "proto": tunnel.PartyProto, "pub": st.Pub})
	}
	m.drop("Left the party")
	return nil
}

// drop forgets the party and stops the tunnel.
func (m *Member) drop(msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tun != nil {
		m.tun.Close()
		m.tun = nil
	}
	if m.port != nil {
		m.sw.Remove(m.port)
		m.port = nil
	}
	m.st = State{HubSeq: m.st.HubSeq} // the event sequence carries over to the next party, like the box's
	m.online, m.view, m.err, m.rtt, m.lastOK = false, nil, "", 0, time.Time{}
	m.save()
	m.note("%s", msg)
}

// Run keeps the membership going until ctx ends: the hub's event channel, the tunnel and the hellos.
func (m *Member) Run(ctx context.Context) {
	go m.events(ctx)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		m.step(ctx)
		select {
		case <-ctx.Done():
			m.mu.Lock()
			if m.tun != nil {
				m.tun.Close()
				m.tun = nil
			}
			m.mu.Unlock()
			return
		case <-t.C:
		}
	}
}

func (m *Member) endpoint(st State) (string, error) {
	host := st.RelayHost
	if host == "" { // the hub's own relay (relay 0)
		if m.resolved == "" {
			ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", m.hub.Host())
			if err != nil || len(ips) == 0 {
				return "", fmt.Errorf("cannot resolve the hub %s", m.hub.Host())
			}
			m.resolved = ips[0].String()
		}
		host = m.resolved
	}
	return net.JoinHostPort(host, fmt.Sprint(st.RelayPort)), nil
}

func (m *Member) step(ctx context.Context) {
	m.mu.Lock()
	st := m.st
	if st.Status != "approved" {
		m.mu.Unlock()
		return
	}
	if m.tun == nil {
		ep, err := m.endpoint(st)
		if err != nil {
			m.err = err.Error()
			m.mu.Unlock()
			return
		}
		tun, err := tunnel.Start(st.ID, st.Priv, st.HostPub, ep, m.logf)
		if err != nil {
			m.err = "cannot start the tunnel: " + err.Error()
			m.mu.Unlock()
			return
		}
		port := &partyPort{tun: tun}
		m.tun, m.port = tun, port
		tun.OnFrame = func(f []byte) { m.sw.Input(port, f) }
		m.sw.Add(port)
		m.logf("tunnel up as member %d via %s", st.ID, ep)
	}
	due := time.Since(m.lastHello) >= helloEvery
	tun := m.tun
	m.mu.Unlock()
	if due {
		m.hello(ctx, tun, st)
	}
}

func (m *Member) hello(ctx context.Context, tun *tunnel.Tunnel, st State) {
	m.mu.Lock()
	m.lastHello = time.Now()
	rtt := m.rtt
	m.mu.Unlock()
	reply, ms, err := tun.Request(ctx, map[string]any{"op": "hello", "proto": tunnel.PartyProto, "pub": st.Pub,
		"rtt": rtt, "name": st.Name})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tun != tun {
		return
	}
	if err != nil {
		if m.online {
			m.note("Lost the connection to the host; retrying")
		}
		m.online, m.err = false, "Cannot reach the host"
		return
	}
	if !m.online {
		m.note("Connected to %s", st.PartyName)
	}
	m.online, m.err, m.rtt, m.lastOK = true, "", ms, time.Now()
	switch reply["status"] {
	case "ok":
		if v, ok := reply["party"].(map[string]any); ok {
			m.view = v
			if n, ok := v["name"].(string); ok && CleanName(n) != "" && CleanName(n) != m.st.PartyName {
				m.st.PartyName = CleanName(n)
				m.save()
			}
		}
	case "kicked":
		go m.drop("You were removed from the party by the host")
	case "closed":
		go m.drop("The host closed the party")
	case "unknown":
		go m.drop("The host no longer knows this app")
	}
}

func (m *Member) events(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		m.mu.Lock()
		after, inParty := m.st.HubSeq, m.st.Status != ""
		m.mu.Unlock()
		if !inParty {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		evs, err := m.hub.Events(ctx, after)
		m.mu.Lock()
		if err != nil {
			m.hubOnline, m.hubErr = false, err.Error()
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		m.hubOnline, m.hubErr, backoff = true, "", 2*time.Second
		m.mu.Unlock()
		for _, e := range evs {
			m.handle(e)
		}
	}
}

func (m *Member) handle(e hub.Event) {
	m.mu.Lock()
	if e.Seq > m.st.HubSeq {
		m.st.HubSeq = e.Seq
	}
	if m.st.Status == "" || e.Party != m.st.HubParty {
		m.save()
		m.mu.Unlock()
		return // an old event for a previous party
	}
	switch e.Type {
	case "approved":
		if e.HostPub != m.st.HostPub {
			m.mu.Unlock()
			m.drop("The hub sent a different host key than the code promised; refused")
			return
		}
		if e.MemberID < 1 || e.MemberID > maxMembers || e.RelayPort < 1 || e.RelayPort > 65535 {
			m.note("Bad approval from the hub; ignored")
			break
		}
		if e.RelayHost != "" && net.ParseIP(e.RelayHost).To4() == nil {
			m.note("Bad relay address from the hub; ignored")
			break
		}
		m.st.Status, m.st.ID, m.st.RelayPort, m.st.RelayHost = "approved", e.MemberID, e.RelayPort, e.RelayHost
		m.st.RelayName, m.st.JoinedAt = cleanRelay(e.Relay), float64(time.Now().Unix())
		if n := CleanName(e.PartyName); n != "" {
			m.st.PartyName = n
		}
		m.note("Joined %s's party as member %d", m.st.PartyName, m.st.ID)
	case "rejected":
		m.mu.Unlock()
		reason := CleanName(e.Reason)
		if reason == "" {
			m.drop("Join refused by the host")
		} else {
			m.drop("Join refused: " + reason)
		}
		return
	case "relay_moved":
		if e.RelayPort < 1 || e.RelayPort > 65535 || (e.RelayHost != "" && net.ParseIP(e.RelayHost).To4() == nil) {
			m.note("Bad relay move from the hub; ignored")
			break
		}
		m.st.RelayPort, m.st.RelayHost, m.st.RelayName = e.RelayPort, e.RelayHost, cleanRelay(e.Relay)
		if m.tun != nil {
			if ep, err := m.endpoint(m.st); err == nil {
				if err := m.tun.SetEndpoint(ep); err != nil {
					m.note("Could not move to the new relay: %v", err)
				}
			}
		}
		relay := m.st.RelayName
		if relay == "" {
			relay = "the hub"
		}
		m.note("The party is now relayed via %s", relay)
	case "kicked":
		m.mu.Unlock()
		m.drop("You were removed from the party by the host")
		return
	case "closed":
		m.mu.Unlock()
		m.drop("The host closed the party")
		return
	}
	m.save()
	m.mu.Unlock()
}

func cleanRelay(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() < 32 && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-._", r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// partyPort is the party's side of the switch.
type partyPort struct{ tun *tunnel.Tunnel }

func (p *partyPort) Name() string            { return "party" }
func (p *partyPort) Send(frame []byte) error { return p.tun.Send(frame) }
