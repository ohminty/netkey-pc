//go:build !windows

// Package tap is the NetKey adapter port; it only exists on Windows.
package tap

import "errors"

const AdapterName = "NetKey"

type TAP struct {
	OnFrame func(frame []byte)
}

func Open(logf func(string, ...any)) (*TAP, error) {
	return nil, errors.New("the NetKey adapter is Windows only")
}

func (t *TAP) Name() string            { return "tap" }
func (t *TAP) Send(frame []byte) error { return errors.New("no adapter") }
func (t *TAP) Close()                  {}
func (t *TAP) Dead() bool              { return false }
