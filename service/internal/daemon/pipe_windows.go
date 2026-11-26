//go:build windows

package daemon

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// PipeName is the control channel the app connects to.
const PipeName = `\\.\pipe\netkey`

// Only SYSTEM, administrators and the person logged in at this PC (interactive users) may talk to the service.
const pipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

// Listen opens the control pipe.
func Listen(dir string) (net.Listener, error) {
	return winio.ListenPipe(PipeName, &winio.PipeConfig{SecurityDescriptor: pipeSDDL, InputBufferSize: 64 << 10,
		OutputBufferSize: 64 << 10})
}

// Dial connects to the running service.
func Dial(dir string) (net.Conn, error) {
	t := 3 * time.Second
	return winio.DialPipe(PipeName, &t)
}
