package clipboard

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
)

// Write puts text on the clipboard.
func Write(text string) error {
	u, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	// Another program can hold the clipboard for a moment: try again.
	opened := false
	for i := 0; i < 10 && !opened; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			opened = true
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !opened {
		return errors.New("the clipboard is busy")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	const gmemMoveable, cfUnicodeText = 0x0002, 13
	size := uintptr(len(u) * 2)
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return errors.New("no memory for the clipboard")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("cannot lock clipboard memory")
	}
	dst := *(*unsafe.Pointer)(unsafe.Pointer(&p)) // memory from GlobalLock, not Go memory
	copy(unsafe.Slice((*uint16)(dst), len(u)), u)
	procGlobalUnlock.Call(h)
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return errors.New("cannot set the clipboard")
	}
	return nil // the clipboard owns the memory now
}
