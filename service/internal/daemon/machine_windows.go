//go:build windows

package daemon

import "golang.org/x/sys/windows/registry"

// machineID is Windows' MachineGuid (set at installation, changes when Windows is reinstalled or sysprepped).
func machineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("MachineGuid")
	return v
}
