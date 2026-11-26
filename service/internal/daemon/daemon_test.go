package daemon

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestControlChannel(t *testing.T) {
	dir, err := os.MkdirTemp("", "nk") // short path: Unix socket paths are limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	d, err := New(dir, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx, l)

	c, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	call := func(req map[string]any) map[string]any {
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		r, err := Client(c, req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	st := call(map[string]any{"op": "status"})
	if st["ok"] != true || st["party"].(map[string]any)["status"] != "" {
		t.Fatalf("status: %v", st)
	}
	if s, _ := st["secret"].(string); !strings.HasPrefix(s, "netkey-") {
		t.Fatalf("no Local Switch secret: %v", st["secret"])
	}
	if r := call(map[string]any{"op": "set_name", "name": "  Ada's PC  "}); r["ok"] != true {
		t.Fatal(r)
	}
	if call(map[string]any{"op": "status"})["name"] != "Ada's PC" {
		t.Fatal("name not saved")
	}
	if r := call(map[string]any{"op": "join", "code": "hello"}); r["ok"] == true ||
		!strings.Contains(r["error"].(string), "KEY-") {
		t.Fatalf("a bad code must be refused before the hub is asked: %v", r)
	}
	if r := call(map[string]any{"op": "test"}); r["ok"] == true {
		t.Fatal("no test outside a party")
	}
	if r := call(map[string]any{"op": "nope"}); r["ok"] == true {
		t.Fatal("unknown requests must fail")
	}
	// settings survive a restart
	d2, err := New(dir, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if d2.set.Name != "Ada's PC" || d2.set.Secret != st["secret"] {
		t.Fatal("settings not kept")
	}
}

func TestIdentityStaysWithTheMachine(t *testing.T) {
	dir := t.TempDir()
	a := loadIdentity(dir, "machine-1", t.Logf)
	if len(a) != 44 || loadIdentity(dir, "machine-1", t.Logf) != a {
		t.Fatal("the identity must be kept on the same machine")
	}
	if b := loadIdentity(dir, "machine-2", t.Logf); b == a || len(b) != 44 {
		t.Fatal("a copied identity must be replaced on another machine")
	}
	old := t.TempDir()
	os.WriteFile(old+"/identity", []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"), 0o600)
	if loadIdentity(old, "m", t.Logf) != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Fatal("an identity from the earlier version must be kept")
	}
}
