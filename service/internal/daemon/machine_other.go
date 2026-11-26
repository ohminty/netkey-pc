//go:build !windows

package daemon

import "os"

func machineID() string {
	h, _ := os.Hostname()
	return h
}
