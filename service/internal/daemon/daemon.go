// Package daemon is the NetKey service: one membership, the switch and its ports, and the control channel the app
// talks to (a named pipe on Windows). Requests and replies are one JSON object per line.
package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"netkey.no/pc/internal/hub"
	"netkey.no/pc/internal/ipx"
	"netkey.no/pc/internal/lswitch"
	"netkey.no/pc/internal/member"
	"netkey.no/pc/internal/probe"
	"netkey.no/pc/internal/sw"
	"netkey.no/pc/internal/tap"
)

// Version of the service (shown in the app).
var Version = "0.1.0"

// Settings are the user's choices (settings.json).
type Settings struct {
	Name   string `json:"name"`
	Secret string `json:"secret"` // 86Box Local Switch shared secret
	// DOSBoxMAC is a MAC for DOSBox-X's macaddr setting, unique to this PC (every DOSBox-X defaults to the same one).
	DOSBoxMAC string `json:"dosbox_mac"`
}

// Daemon is the running service.
type Daemon struct {
	dir  string
	logf func(string, ...any)
	hub  *hub.Client
	sw   *sw.Switch
	m    *member.Member

	mu       sync.Mutex
	set      Settings
	tap      *tap.TAP
	tapErr   string
	ls       *lswitch.LSwitch
	lsErr    string
	ipx      *ipx.Port // DOSBox's IPX tunnel (IPXNET CONNECT 127.0.0.1): always listening
	ipxErr   string
	warnings []string
	testing  bool
	lastTest map[string]any
}

// New loads state from dir (created if missing).
func New(dir string, logf func(string, ...any)) (*Daemon, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	d := &Daemon{dir: dir, logf: logf}
	if b, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil {
		_ = json.Unmarshal(b, &d.set)
	}
	if d.set.Name == "" {
		h, _ := os.Hostname()
		d.set.Name = member.CleanName(h)
	}
	if d.set.Secret == "" {
		b := make([]byte, 9)
		rand.Read(b)
		d.set.Secret = "netkey-" + base64.RawURLEncoding.EncodeToString(b)
	}
	if d.set.DOSBoxMAC == "" {
		b := make([]byte, 3)
		rand.Read(b)
		d.set.DOSBoxMAC = fmt.Sprintf("AC:DE:48:%02X:%02X:%02X", b[0], b[1], b[2]) // DOSBox-X's private OUI
	}
	d.saveSettings()
	identity := loadIdentity(dir, machineID(), logf)
	h, err := hub.New(hub.DefaultURL, hub.DefaultFingerprint, d.set.Name, identity, filepath.Join(dir, "hub.json"))
	if err != nil {
		return nil, err
	}
	d.hub = h
	d.sw = sw.New()
	d.sw.OnMove = func(mac [6]byte, from, to string) {
		w := fmt.Sprintf("Two machines use the MAC address %02X:%02X:%02X:%02X:%02X:%02X (seen on %s and %s). "+
			"Give each its own: DOSBox-X's default is AC:DE:48:88:99:AA (macaddr in dosbox-x.conf).",
			mac[0], mac[1], mac[2], mac[3], mac[4], mac[5], from, to)
		logf("%s", w)
		d.mu.Lock()
		d.warnings = append(d.warnings, w)
		if len(d.warnings) > 5 {
			d.warnings = d.warnings[len(d.warnings)-5:]
		}
		d.mu.Unlock()
	}
	d.m = member.New(h, d.sw, filepath.Join(dir, "party.json"), logf)
	return d, nil
}

func (d *Daemon) saveSettings() {
	b, _ := json.MarshalIndent(d.set, "", "  ")
	_ = os.WriteFile(filepath.Join(d.dir, "settings.json"), b, 0o600)
}

// identityFile is this PC's permanent ID at the hub: a 32-byte secret (the hub derives the ID from it) and the
// machine it was made on. It survives an uninstall, like the Pi's box-identity.json, so nicknames and bans stay with
// the PC; if the file turns up on another machine (a copied disk image), it's set aside and a new one is made.
type identityFile struct {
	Secret  string `json:"secret"`
	Machine string `json:"machine"`
}

func loadIdentity(dir, machine string, logf func(string, ...any)) string {
	path := filepath.Join(dir, "identity.json")
	var id identityFile
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &id) == nil && len(id.Secret) == 44 {
		if id.Machine == machine {
			return id.Secret
		}
		logf("the identity was made on another machine; making a new one for this PC")
		_ = os.Rename(path, path+".other-machine")
		id = identityFile{}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "identity")); err == nil && len(strings.TrimSpace(string(b))) == 44 {
		id.Secret = strings.TrimSpace(string(b)) // made by an earlier version
	}
	if id.Secret == "" {
		b := make([]byte, 32)
		rand.Read(b)
		id.Secret = base64.StdEncoding.EncodeToString(b)
	}
	id.Machine = machine
	b, _ := json.Marshal(id)
	_ = os.WriteFile(path, b, 0o600)
	_ = os.Remove(filepath.Join(dir, "identity"))
	return id.Secret
}

func loadOrMake(path string, mk func() string) string {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b))
	}
	v := mk()
	_ = os.WriteFile(path, []byte(v+"\n"), 0o600)
	return v
}

// Run keeps the service going until ctx ends; l is the control listener.
func (d *Daemon) Run(ctx context.Context, l net.Listener) {
	d.openIPX()
	go d.m.Run(ctx)
	go d.serve(ctx, l)
	go d.probeRelays(ctx)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		d.ports()
		select {
		case <-ctx.Done():
			d.closePorts()
			if d.ipx != nil {
				d.sw.Remove(d.ipx)
				d.ipx.Close()
			}
			l.Close()
			return
		case <-t.C:
		}
	}
}

// IPXAddr is where DOSBox connects (IPXNET CONNECT 127.0.0.1). Tests change it.
var IPXAddr = fmt.Sprintf("127.0.0.1:%d", ipx.DefaultPort)

// openIPX starts the IPX server for DOSBox. It listens in and out of parties, so DOSBox can connect before the party
// starts; its frames only go anywhere while the party port exists.
func (d *Daemon) openIPX() {
	p, err := ipx.Open(IPXAddr, d.logf)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.ipxErr = err.Error()
		d.logf("DOSBox IPX server: %v", err)
		return
	}
	p.OnFrame = func(f []byte) { d.sw.Input(p, f) }
	d.sw.Add(p)
	d.ipx = p
	d.logf("DOSBox IPX server listening on %s", p.Addr())
}

// probeRelays measures the relays now and every 10 minutes (the box does the same): the hub uses the times to pick
// relays and to estimate the ping to public parties.
func (d *Daemon) probeRelays(ctx context.Context) {
	for {
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		rtt, err := d.hub.ProbeRelays(pctx)
		cancel()
		if err != nil {
			d.logf("relay measurement: %v", err)
		} else {
			d.logf("relay round trips: %v", rtt)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}

// ports opens the NetKey adapter and the Local Switch while in a party (approved), and closes them otherwise, so
// emulators see "cable unplugged" outside a party.
func (d *Daemon) ports() {
	in := d.m.Status().State.Status == "approved"
	d.mu.Lock()
	defer d.mu.Unlock()
	if !in {
		d.closePortsLocked()
		return
	}
	if d.tap != nil && d.tap.Dead() {
		d.sw.Remove(d.tap)
		d.tap.Close()
		d.tap = nil
		d.logf("NetKey adapter went away; reopening it")
	}
	if d.tap == nil {
		t, err := tap.Open(d.logf)
		if err != nil {
			if d.tapErr != err.Error() {
				d.logf("NetKey adapter: %v", err)
			}
			d.tapErr = err.Error()
		} else {
			t.OnFrame = func(f []byte) { d.sw.Input(t, f) }
			d.sw.Add(t)
			d.tap, d.tapErr = t, ""
			d.logf("NetKey adapter connected")
		}
	}
	if d.ls == nil {
		l, err := lswitch.Open(d.set.Secret, d.logf)
		if err != nil {
			if d.lsErr != err.Error() {
				d.logf("86Box Local Switch: %v", err)
			}
			d.lsErr = err.Error()
		} else {
			l.OnFrame = func(f []byte) { d.sw.Input(l, f) }
			d.sw.Add(l)
			d.ls, d.lsErr = l, ""
		}
	}
}

func (d *Daemon) closePorts() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closePortsLocked()
}

func (d *Daemon) closePortsLocked() {
	if d.tap != nil {
		d.sw.Remove(d.tap)
		d.tap.Close()
		d.tap = nil
		d.logf("NetKey adapter disconnected")
	}
	if d.ls != nil {
		d.sw.Remove(d.ls)
		d.ls.Close()
		d.ls = nil
	}
	d.tapErr, d.lsErr = "", ""
}

func (d *Daemon) serve(ctx context.Context, l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() == nil {
				d.logf("control: %v", err)
				time.Sleep(time.Second)
				continue
			}
			return
		}
		go d.conn(ctx, c)
	}
}

func (d *Daemon) conn(ctx context.Context, c net.Conn) {
	defer c.Close()
	r := bufio.NewReaderSize(c, 64<<10)
	for {
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Minute))
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req map[string]any
		var reply any
		if json.Unmarshal(line, &req) != nil {
			reply = map[string]any{"ok": false, "error": "bad request"}
		} else {
			reply = d.Handle(ctx, req)
		}
		b, _ := json.Marshal(reply)
		_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.Write(append(b, '\n')); err != nil {
			return
		}
	}
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// Handle answers one control request.
func (d *Daemon) Handle(ctx context.Context, req map[string]any) map[string]any {
	fail := func(err error) map[string]any { return map[string]any{"ok": false, "error": err.Error()} }
	switch str(req, "op") {
	case "status":
		return d.status()
	case "join":
		name := member.CleanName(str(req, "name"))
		if name != "" {
			d.mu.Lock()
			d.set.Name = name
			d.saveSettings()
			d.mu.Unlock()
			d.hub.Name = name
		}
		d.mu.Lock()
		name = d.set.Name
		d.warnings = nil
		d.mu.Unlock()
		jctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		msg, err := d.m.Join(jctx, strings.TrimSpace(str(req, "code")), name)
		if err != nil {
			return fail(err)
		}
		return map[string]any{"ok": true, "message": msg}
	case "leave":
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := d.m.Leave(lctx); err != nil {
			return fail(err)
		}
		d.closePorts()
		return map[string]any{"ok": true, "message": "Left the party"}
	case "set_name":
		name := member.CleanName(str(req, "name"))
		if name == "" {
			return fail(fmt.Errorf("choose a name the host will see"))
		}
		d.mu.Lock()
		d.set.Name = name
		d.saveSettings()
		d.mu.Unlock()
		d.hub.Name = name
		return map[string]any{"ok": true}
	case "test":
		return d.test(ctx)
	case "join_listing":
		d.mu.Lock()
		name := d.set.Name
		d.warnings = nil
		d.mu.Unlock()
		jctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		msg, err := d.m.JoinListing(jctx, str(req, "listing"), name)
		if err != nil {
			return fail(err)
		}
		return map[string]any{"ok": true, "message": msg}
	case "me", "directory":
		return d.hubGet(ctx, "/v1/"+str(req, "op"), 15*time.Second)
	case "nick_check", "set_nick":
		path := map[string]string{"nick_check": "/v1/nick/check", "set_nick": "/v1/nick"}[str(req, "op")]
		return d.hubPost(ctx, path, map[string]any{"nick": strings.TrimSpace(str(req, "nick"))})
	case "chat":
		room := d.m.Status().State.Room()
		if room == "" {
			return fail(fmt.Errorf("not in a party"))
		}
		after, _ := req["after"].(float64)
		wait, _ := req["wait"].(float64)
		wait = max(0, min(wait, 20))
		r := d.hubGet(ctx, fmt.Sprintf("/v1/chat?room=%s&after=%d&wait=%d", url.QueryEscape(room), int64(after),
			int(wait)), time.Duration(wait+15)*time.Second)
		r["room"] = room
		return r
	case "chat_send":
		room := d.m.Status().State.Room()
		if room == "" {
			return fail(fmt.Errorf("not in a party"))
		}
		return d.hubPost(ctx, "/v1/chat", map[string]any{"room": room, "text": str(req, "text")})
	case "report":
		room := d.m.Status().State.Room()
		id, _ := req["id"].(float64)
		return d.hubPost(ctx, "/v1/report", map[string]any{"room": room, "id": int64(id), "text": str(req, "text")})
	}
	return fail(fmt.Errorf("unknown request"))
}

// hubGet and hubPost pass a request through to the hub (as this PC) and add "ok".
func (d *Daemon) hubGet(ctx context.Context, path string, timeout time.Duration) map[string]any {
	out := map[string]any{}
	if err := d.hub.CallTimeout(ctx, "GET", path, nil, &out, timeout); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	out["ok"] = true
	return out
}

func (d *Daemon) hubPost(ctx context.Context, path string, body any) map[string]any {
	out := map[string]any{}
	if err := d.hub.Call(ctx, "POST", path, body, &out); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	out["ok"] = true
	return out
}

func (d *Daemon) status() map[string]any {
	st := d.m.Status()
	d.mu.Lock()
	defer d.mu.Unlock()
	members := []map[string]any{}
	if v, ok := st.View["members"].([]any); ok {
		for _, x := range v {
			if mm, ok := x.(map[string]any); ok {
				members = append(members, map[string]any{"id": mm["id"], "name": mm["name"]})
			}
		}
	}
	files := map[string]any{}
	if st.View != nil && st.View["share_files"] == true {
		files = map[string]any{"name": st.View["files_name"], "share": st.View["files_share"],
			"address": st.View["files_address"]}
	}
	hs := ""
	if !st.Handshake.IsZero() {
		hs = st.Handshake.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"ok": true, "version": Version, "name": d.set.Name, "secret": d.set.Secret,
		"party": map[string]any{
			"status": st.State.Status, "party_name": st.PartyName, "member_id": st.ID, "online": st.Online,
			"rtt_ms": st.RTTms, "error": st.Error, "relay": st.RelayName, "members": members, "files": files,
			"joined_at": st.JoinedAt, "handshake": hs, "key_check": hub.VerifyWords(st.Pub),
			"public": st.Public, "listing": st.Listing, "last_ok": lastOK(st),
		},
		"dosbox_mac": d.set.DOSBoxMAC,
		"hub":        map[string]any{"online": st.HubOnline, "error": st.HubError},
		"adapter":    map[string]any{"connected": d.tap != nil, "error": d.tapErr, "name": tap.AdapterName},
		"lswitch":    map[string]any{"open": d.ls != nil, "error": d.lsErr},
		"ipx":        d.ipxStatus(),
		"notes":      st.Notes,
		"warnings":   append([]string(nil), d.warnings...),
		"frames":     d.sw.Stats(),
		"test":       d.lastTest,
		"testing":    d.testing,
	}
}

func (d *Daemon) ipxStatus() map[string]any {
	if d.ipx == nil {
		return map[string]any{"listening": false, "error": d.ipxErr, "clients": []ipx.ClientInfo{}}
	}
	return map[string]any{"listening": true, "port": d.ipx.Addr().Port, "error": "", "clients": d.ipx.Clients()}
}

func lastOK(st member.Status) string {
	if st.LastOK.IsZero() {
		return ""
	}
	return st.LastOK.UTC().Format(time.RFC3339)
}

// test runs the connection test: a virtual PC asks the party for an address and pings the gateway.
func (d *Daemon) test(ctx context.Context) map[string]any {
	if !d.m.Status().Online {
		return map[string]any{"ok": false, "error": "not connected to a party"}
	}
	d.mu.Lock()
	if d.testing {
		d.mu.Unlock()
		return map[string]any{"ok": false, "error": "a test is already running"}
	}
	d.testing = true
	d.mu.Unlock()
	p := probe.New()
	p.Out = func(f []byte) { d.sw.Input(p, f) }
	d.sw.Add(p)
	tctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	r, err := p.Run(tctx, []netip.Addr{})
	cancel()
	d.sw.Remove(p)
	out := map[string]any{"ok": err == nil, "at": time.Now().UTC().Format(time.RFC3339)}
	if err != nil {
		out["error"] = err.Error()
	} else {
		ms := func(x time.Duration) float64 { return float64(x.Microseconds()) / 1000 }
		out["address"], out["gateway"] = r.Address.String(), r.Gateway.String()
		out["ping_ms"], out["ping_full_ms"] = ms(r.PingSmall), ms(r.PingFull)
		out["ok"] = r.PingSmall > 0 && r.PingFull > 0
		if r.PingSmall == 0 {
			out["error"] = "got an address, but the gateway didn't answer pings"
		} else if r.PingFull == 0 {
			out["error"] = "small packets work, full-size ones don't"
		}
	}
	d.mu.Lock()
	d.testing, d.lastTest = false, out
	d.mu.Unlock()
	return out
}

// Client sends one request over c and reads the reply.
func Client(c io.ReadWriter, req map[string]any) (map[string]any, error) {
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, json.Unmarshal(line, &out)
}

// NewLogger writes to dir/netkeyd.log (cut back to the last half when it passes 1 MB) and to stderr.
func NewLogger(dir string) func(string, ...any) {
	path := filepath.Join(dir, "netkeyd.log")
	var mu sync.Mutex
	return func(f string, a ...any) {
		line := time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(f, a...) + "\n"
		log.Print(strings.TrimRight(fmt.Sprintf(f, a...), "\n"))
		mu.Lock()
		defer mu.Unlock()
		if fi, err := os.Stat(path); err == nil && fi.Size() > 1<<20 {
			if b, err := os.ReadFile(path); err == nil {
				_ = os.WriteFile(path, b[len(b)/2:], 0o600)
			}
		}
		if fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fh.WriteString(line)
			fh.Close()
		}
	}
}
