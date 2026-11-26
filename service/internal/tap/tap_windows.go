//go:build windows

// Package tap is the switch port for the "NetKey" network adapter (OpenVPN's TAP-Windows6 driver). VMware,
// VirtualBox and DOSBox-X (through Npcap) bridge their machines onto that adapter; each read is one Ethernet frame
// the machines sent, each write puts one party frame in front of them.
//
// The adapter is marked connected only while this port is open, so emulators see "cable unplugged" outside a party.
package tap

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AdapterName is the adapter the installer creates.
const AdapterName = "NetKey"

// CTL_CODE(FILE_DEVICE_UNKNOWN, 6, METHOD_BUFFERED, FILE_ANY_ACCESS): tap-windows.h TAP_WIN_IOCTL_SET_MEDIA_STATUS.
const ioctlSetMediaStatus = 0x22<<16 | 6<<2

// TAP is an open adapter.
type TAP struct {
	h       windows.Handle
	wmu     sync.Mutex
	wev     windows.Handle
	mu      sync.Mutex
	closed  bool
	dead    bool
	OnFrame func(frame []byte)
	Logf    func(string, ...any)
}

// adapterGUID finds the adapter's GUID ("{...}") by its friendly name.
func adapterGUID(name string) (string, error) {
	size := uint32(16 << 10)
	for i := 0; i < 3; i++ {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_ALL_INTERFACES, 0, aa, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return "", err
		}
		for a := aa; a != nil; a = a.Next {
			if windows.UTF16PtrToString(a.FriendlyName) == name {
				return windows.BytePtrToString(a.AdapterName), nil
			}
		}
		return "", fmt.Errorf("no network adapter named %q (is NetKey installed?)", name)
	}
	return "", errors.New("cannot list the network adapters")
}

// Open opens the NetKey adapter and marks it connected.
func Open(logf func(string, ...any)) (*TAP, error) {
	guid, err := adapterGUID(AdapterName)
	if err != nil {
		return nil, err
	}
	path, _ := windows.UTF16PtrFromString(`\\.\Global\` + guid + `.tap`)
	h, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_SYSTEM|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot open the NetKey adapter: %w", err)
	}
	t := &TAP{h: h, Logf: logf}
	if err := t.setMedia(true); err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("cannot connect the NetKey adapter: %w", err)
	}
	if t.wev, err = windows.CreateEvent(nil, 1, 0, nil); err != nil {
		t.Close()
		return nil, err
	}
	go t.recv()
	return t, nil
}

func (t *TAP) setMedia(up bool) error {
	var v uint32
	if up {
		v = 1
	}
	var ret uint32
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ev)
	ov := windows.Overlapped{HEvent: ev}
	err = windows.DeviceIoControl(t.h, ioctlSetMediaStatus, (*byte)(unsafe.Pointer(&v)), 4,
		(*byte)(unsafe.Pointer(&v)), 4, &ret, &ov)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(t.h, &ov, &ret, true)
	}
	return err
}

// Name is the switch port name.
func (t *TAP) Name() string { return "tap" }

// Send writes one party frame to the adapter.
func (t *TAP) Send(frame []byte) error {
	if len(frame) < 14 || len(frame) > 1518 {
		return errors.New("bad frame size")
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	windows.ResetEvent(t.wev)
	ov := windows.Overlapped{HEvent: t.wev}
	var n uint32
	err := windows.WriteFile(t.h, frame, &n, &ov)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(t.h, &ov, &n, true)
	}
	return err
}

func (t *TAP) recv() {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return
	}
	defer windows.CloseHandle(ev)
	buf := make([]byte, 2048)
	for {
		windows.ResetEvent(ev)
		ov := windows.Overlapped{HEvent: ev}
		var n uint32
		err := windows.ReadFile(t.h, buf, &n, &ov)
		if err == windows.ERROR_IO_PENDING {
			err = windows.GetOverlappedResult(t.h, &ov, &n, true)
		}
		if err != nil {
			t.mu.Lock()
			closed := t.closed
			t.dead = true
			t.mu.Unlock()
			if !closed && t.Logf != nil {
				t.Logf("NetKey adapter: read stopped: %v", err)
			}
			return
		}
		if n >= 14 && t.OnFrame != nil {
			f := make([]byte, n)
			copy(f, buf[:n])
			t.OnFrame(f)
		}
	}
}

// Dead reports whether the adapter went away under us (restarted or disabled); the caller reopens it.
func (t *TAP) Dead() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dead && !t.closed
}

// Close marks the adapter disconnected and closes it.
func (t *TAP) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	_ = t.setMedia(false)
	windows.CancelIoEx(t.h, nil)
	windows.CloseHandle(t.h)
	if t.wev != 0 {
		windows.CloseHandle(t.wev)
	}
}
