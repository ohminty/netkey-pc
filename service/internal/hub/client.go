package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Defaults: the same hub a box uses out of the box (netkey/config.py lan.hub_url and lan.hub_fingerprint).
const (
	DefaultURL         = "https://relay.netkey.no:8443"
	DefaultFingerprint = "cc4045ee943d7ccb21cb6bb2601c5a3251096477bc427d482138445ad65d194f"
	// Proto 2: relay_host in approvals and relay_moved; 3: permanent identity. 4 (phone calls) is box-only.
	Proto = 3
	// Kind tells the hub (and the host, later) that this member is the PC app, not a box.
	Kind = "pc"
)

// Error is a hub reply other than 200 (Status 0: the hub couldn't be reached).
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

type creds struct {
	URL   string `json:"url"`
	Box   string `json:"box"`
	Token string `json:"token"`
}

// Client calls the hub over HTTPS pinned to the certificate's SHA-256 fingerprint (no certificate authority).
type Client struct {
	URL       string
	Name      string
	Identity  string // base64 32-byte secret for a permanent ID, or "" for none
	CredsPath string

	host string
	http *http.Client
	mu   sync.Mutex
	c    creds
}

// New returns a client for hubURL pinned to fingerprint (hex SHA-256 of the DER certificate).
func New(hubURL, fingerprint, name, identity, credsPath string) (*Client, error) {
	u, err := url.Parse(hubURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, errors.New("the hub URL must be https://host:port")
	}
	want, err := hex.DecodeString(strings.ReplaceAll(strings.ToLower(fingerprint), ":", ""))
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("bad hub fingerprint")
	}
	tlsConf := &tls.Config{
		InsecureSkipVerify: true, // verified below by fingerprint (pinning), like the box
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the hub sent no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if !bytes.Equal(got[:], want) {
				return errors.New("the hub's certificate does not match the pinned fingerprint")
			}
			return nil
		},
	}
	c := &Client{URL: hubURL, Name: name, Identity: identity, CredsPath: credsPath, host: u.Hostname(),
		http: &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConf, ForceAttemptHTTP2: false,
			DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext}}}
	if b, err := os.ReadFile(credsPath); err == nil {
		_ = json.Unmarshal(b, &c.c)
		if c.c.URL != hubURL {
			c.c = creds{}
		}
	}
	return c, nil
}

// Host is the hub's host name (its own relay, relay 0, runs there).
func (c *Client) Host() string { return c.host }

// Box is the ID the hub knows this app by ("" before registering).
func (c *Client) Box() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.c.Box
}

func (c *Client) saveCreds() {
	b, _ := json.Marshal(c.c)
	_ = writeFileAtomic(c.CredsPath, b)
}

func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	tok := c.c.Token
	c.mu.Unlock()
	if tok != "" {
		return tok, nil
	}
	body := map[string]any{"name": c.Name, "kind": Kind}
	if c.Identity != "" {
		body["identity"] = c.Identity
	}
	var r struct {
		Box   string `json:"box"`
		Token string `json:"token"`
	}
	if err := c.do(ctx, "POST", "/v1/register", body, "", 15*time.Second, &r); err != nil {
		return "", err
	}
	c.mu.Lock()
	c.c = creds{URL: c.URL, Box: r.Box, Token: r.Token}
	c.saveCreds()
	c.mu.Unlock()
	return r.Token, nil
}

// Call sends one authenticated request; out (a pointer) receives the JSON reply.
func (c *Client) Call(ctx context.Context, method, path string, body, out any) error {
	return c.CallTimeout(ctx, method, path, body, out, 15*time.Second)
}

// CallTimeout is Call with its own timeout (the event long-poll waits up to 25 s at the hub).
func (c *Client) CallTimeout(ctx context.Context, method, path string, body, out any, timeout time.Duration) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	err = c.do(ctx, method, path, body, tok, timeout, out)
	var he *Error
	if errors.As(err, &he) && he.Status == 401 {
		c.mu.Lock()
		c.c = creds{} // the hub forgot us (or a new hub): register again next time
		c.saveCreds()
		c.mu.Unlock()
	}
	return err
}

func (c *Client) do(ctx context.Context, method, path string, body any, token string, timeout time.Duration,
	out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NetKey-Proto", fmt.Sprint(Proto))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{0, "cannot reach the hub: " + err.Error()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &Error{0, "cannot reach the hub: " + err.Error()}
	}
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("hub error %d", resp.StatusCode)
		}
		return &Error{resp.StatusCode, e.Error}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return &Error{0, "bad reply from the hub"}
		}
	}
	return nil
}

// Event is one entry of the hub's long-poll channel (fields depend on Type).
type Event struct {
	Seq       int64  `json:"seq"`
	Type      string `json:"type"`
	Party     string `json:"party"`
	MemberID  int    `json:"member_id"`
	RelayPort int    `json:"relay_port"`
	RelayHost string `json:"relay_host"`
	Relay     string `json:"relay"`
	HostPub   string `json:"host_pub"`
	PartyName string `json:"party_name"`
	Reason    string `json:"reason"`
}

// Events waits up to 25 s for events after seq.
func (c *Client) Events(ctx context.Context, after int64) ([]Event, error) {
	var r struct {
		Events []Event `json:"events"`
	}
	err := c.CallTimeout(ctx, "GET", fmt.Sprintf("/v1/events?after=%d&wait=25", after), nil, &r, 40*time.Second)
	return r.Events, err
}

// SkipEvents reads past every event already waiting after seq, without acting on them, and returns the last
// sequence number. Call it before asking to join: the hub keeps old events, and an "approved" from an earlier
// membership of the same party must not count as an answer to a new request.
func (c *Client) SkipEvents(ctx context.Context, after int64) (int64, error) {
	for i := 0; i < 1000; i++ {
		var r struct {
			Events []Event `json:"events"`
			Last   int64   `json:"last"`
		}
		if err := c.CallTimeout(ctx, "GET", fmt.Sprintf("/v1/events?after=%d&wait=0", after), nil, &r,
			15*time.Second); err != nil {
			return after, err
		}
		if len(r.Events) == 0 {
			return after, nil
		}
		for _, e := range r.Events {
			if e.Seq > after {
				after = e.Seq
			}
		}
	}
	return after, nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
