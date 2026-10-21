package power

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procSetProcessInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetProcessInformation")

// Efficient asks Windows to run this program in efficiency mode (EcoQoS):
// low clock speed and efficient cores. It is safe on Windows versions
// without it: the call then does nothing.
func Efficient() {
	if procSetProcessInformation.Find() != nil {
		return
	}
	const processPowerThrottling = 4
	state := struct{ Version, ControlMask, StateMask uint32 }{1, 1, 1} // execution speed: throttled
	procSetProcessInformation.Call(uintptr(windows.CurrentProcess()), processPowerThrottling, uintptr(unsafe.Pointer(&state)), unsafe.Sizeof(state))
}

// Background lowers CPU, disk and memory priority until restore is called.
func Background() (restore func()) {
	const begin, end = 0x00100000, 0x00200000 // PROCESS_MODE_BACKGROUND_BEGIN/END
	h := windows.CurrentProcess()
	if windows.SetPriorityClass(h, begin) != nil {
		return func() {}
	}
	return func() { windows.SetPriorityClass(h, end) }
}
