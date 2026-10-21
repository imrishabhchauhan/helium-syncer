package power

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

func read() (State, error) {
	var st systemPowerStatus
	if r, _, err := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return State{}, err
	}
	return parseWindows(st), nil
}
