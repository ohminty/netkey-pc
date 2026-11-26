//go:build windows

// Package setup installs and removes what NetKey needs on Windows: the "NetKey" network adapter (OpenVPN's signed
// TAP-Windows6 driver), with Windows' own protocols turned off on it, the private state folder, and the service.
// The installer runs `netkeyd install` and `netkeyd uninstall`.
package setup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"netkey.no/pc/internal/tap"
)

const ServiceName = "NetKey"

// Bindings turned off on the adapter: IPv4/IPv6, file and printer sharing, the Microsoft network client, network
// discovery (LLTD), LLDP and QoS. Windows itself must never join a party; emulators still see every frame through
// the bridge and capture drivers (VMware Bridge Protocol, VirtualBox, Npcap), which stay on.
var offBindings = []string{"ms_tcpip", "ms_tcpip6", "ms_server", "ms_msclient", "ms_lltdio", "ms_rspndr", "ms_lldp",
	"ms_implat", "ms_pacer"}

func powershell(script string) (string, error) {
	out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", "$ProgressPreference='SilentlyContinue'; "+script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// AdapterExists reports whether the NetKey adapter is installed.
func AdapterExists() bool {
	out, err := powershell(fmt.Sprintf("(Get-NetAdapter -Name '%s' -IncludeHidden -EA SilentlyContinue).Name",
		tap.AdapterName))
	return err == nil && out == tap.AdapterName
}

// InstallAdapter creates the NetKey adapter from the driver folder (OemVista.inf, tap0901.sys/.cat, devcon.exe)
// unless it exists, and turns off Windows' protocols on it.
func InstallAdapter(driverDir string, logf func(string, ...any)) error {
	if !AdapterExists() {
		before, _ := powershell("(Get-NetAdapter -IncludeHidden | ForEach-Object { $_.InterfaceGuid }) -join ','")
		out, err := exec.Command(filepath.Join(driverDir, "devcon.exe"), "install",
			filepath.Join(driverDir, "OemVista.inf"), "tap0901").CombinedOutput()
		logf("devcon: %s", strings.TrimSpace(string(out)))
		if err != nil {
			return fmt.Errorf("installing the TAP driver failed: %v", err)
		}
		var name string
		for i := 0; i < 20 && name == ""; i++ {
			time.Sleep(500 * time.Millisecond)
			name, _ = powershell(fmt.Sprintf("$b = '%s' -split ','; (Get-NetAdapter -IncludeHidden | Where-Object { "+
				"$b -notcontains $_.InterfaceGuid -and $_.InterfaceDescription -like 'TAP-Windows*' } | "+
				"Select-Object -First 1).Name", before))
		}
		if name == "" {
			return errors.New("the TAP driver installed, but no new adapter turned up")
		}
		if out, err := powershell(fmt.Sprintf("Rename-NetAdapter -Name '%s' -NewName '%s'", name,
			tap.AdapterName)); err != nil {
			return fmt.Errorf("renaming the adapter: %s", out)
		}
		logf("created the %s network adapter", tap.AdapterName)
	}
	if err := NameDevice(logf); err != nil {
		logf("%v", err)
	}
	return LockAdapter(logf)
}

// DeviceName is what Device Manager, VMware's network editor and Npcap (DOSBox-X's realnic list) show for the
// adapter, instead of the driver's "TAP-Windows Adapter V9". It's the device's FriendlyName, so the signed driver
// files stay untouched.
const DeviceName = "NetKey Adapter"

var netClass = windows.GUID{Data1: 0x4d36e972, Data2: 0xe325, Data3: 0x11ce,
	Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}

// NameDevice sets the adapter's device name to DeviceName and restarts it so Windows shows the new name.
func NameDevice(logf func(string, ...any)) error {
	id, err := powershell(fmt.Sprintf("(Get-NetAdapter -Name '%s' -IncludeHidden -EA SilentlyContinue).PnPDeviceID",
		tap.AdapterName))
	if err != nil || id == "" {
		return errors.New("the NetKey adapter is missing")
	}
	set, err := windows.SetupDiGetClassDevsEx(&netClass, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return err
	}
	defer set.Close()
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if err != nil {
			return fmt.Errorf("device %s not found", id)
		}
		if got, err := set.DeviceInstanceID(data); err != nil || !strings.EqualFold(got, id) {
			continue
		}
		if cur, err := set.DeviceRegistryProperty(data, windows.SPDRP_FRIENDLYNAME); err == nil && cur == DeviceName {
			return nil
		}
		if err := set.SetDeviceRegistryPropertyString(data, windows.SPDRP_FRIENDLYNAME, DeviceName); err != nil {
			return fmt.Errorf("naming the adapter: %w", err)
		}
		if out, err := powershell(fmt.Sprintf("Restart-NetAdapter -Name '%s' -Confirm:$false", tap.AdapterName)); err != nil {
			logf("restarting the adapter: %s", out)
		}
		logf("the adapter now shows as %q", DeviceName)
		return nil
	}
}

// LockAdapter turns off Windows' own protocols on the adapter (run at install and at every service start, since
// updates or new software can turn bindings back on).
func LockAdapter(logf func(string, ...any)) error {
	var parts []string
	for _, b := range offBindings {
		parts = append(parts, fmt.Sprintf("Disable-NetAdapterBinding -Name '%s' -ComponentID %s -EA SilentlyContinue",
			tap.AdapterName, b))
	}
	parts = append(parts, fmt.Sprintf("(Get-NetAdapterBinding -Name '%s' | Where-Object { $_.Enabled -and "+
		"@('%s') -contains $_.ComponentID }).ComponentID -join ','", tap.AdapterName, strings.Join(offBindings, "','")))
	out, err := powershell(strings.Join(parts, "; "))
	if err != nil {
		return fmt.Errorf("turning off Windows' protocols on the adapter: %s", out)
	}
	if out != "" {
		return fmt.Errorf("these stayed on on the %s adapter: %s", tap.AdapterName, out)
	}
	return nil
}

// RemoveAdapter removes the NetKey adapter (only ours; other TAP adapters, e.g. OpenVPN's, are left alone).
func RemoveAdapter(driverDir string, logf func(string, ...any)) error {
	id, err := powershell(fmt.Sprintf("(Get-NetAdapter -Name '%s' -IncludeHidden -EA SilentlyContinue).PnPDeviceID",
		tap.AdapterName))
	if err != nil || id == "" {
		return nil
	}
	out, err := exec.Command(filepath.Join(driverDir, "devcon.exe"), "remove", "@"+id).CombinedOutput()
	logf("devcon: %s", strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("removing the adapter failed: %v", err)
	}
	return nil
}

// PrepareState creates the state folder readable only by SYSTEM and administrators (it holds the keys).
func PrepareState(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	out, err := exec.Command("icacls", dir, "/inheritance:r", "/grant:r", "*S-1-5-18:(OI)(CI)F",
		"/grant:r", "*S-1-5-32-544:(OI)(CI)F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("securing %s: %s", dir, strings.TrimSpace(string(out)))
	}
	return nil
}

// InstallService registers and starts the service (exe runs with the "service" argument).
func InstallService(exe string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err == nil {
		s.Close()
		if err := RemoveService(); err != nil {
			return err
		}
	}
	s, err = m.CreateService(ServiceName, exe, mgr.Config{DisplayName: "NetKey", StartType: mgr.StartAutomatic,
		Description: "Joins LAN parties for your emulators and virtual machines (the NetKey network adapter)."},
		"service")
	if err != nil {
		return err
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second}, {Type: mgr.ServiceRestart, Delay: time.Minute}}, 86400)
	_ = eventlog.InstallAsEventCreate(ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info)
	return s.Start()
}

// RemoveService stops and deletes the service.
func RemoveService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return nil
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != 1 { // 1 = SERVICE_STOPPED
		_, _ = s.Control(windows.SERVICE_CONTROL_STOP)
		for i := 0; i < 40; i++ {
			if st, err := s.Query(); err != nil || st.State == 1 {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	_ = eventlog.Remove(ServiceName)
	return s.Delete()
}
