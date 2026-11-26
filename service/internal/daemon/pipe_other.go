//go:build !windows

package daemon

import (
	"net"
	"os"
	"path/filepath"
)

// Listen opens the control socket (a Unix socket in the state folder; used for tests off Windows).
func Listen(dir string) (net.Listener, error) {
	p := filepath.Join(dir, "control.sock")
	_ = os.Remove(p)
	return net.Listen("unix", p)
}

// Dial connects to the running service.
func Dial(dir string) (net.Conn, error) {
	return net.Dial("unix", filepath.Join(dir, "control.sock"))
}
