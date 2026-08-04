//go:build windows

package tui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// copyTextToClipboard writes text via the Win32 clipboard API. OSC 52 is not
// an option here: legacy conhost ignores it, and the API works in every
// Windows terminal (Windows Terminal, conhost, VSCode) without configuration.
var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	openClipboard    = user32.NewProc("OpenClipboard")
	closeClipboard   = user32.NewProc("CloseClipboard")
	emptyClipboard   = user32.NewProc("EmptyClipboard")
	setClipboardData = user32.NewProc("SetClipboardData")

	kernel32      = windows.NewLazySystemDLL("kernel32.dll")
	globalAlloc   = kernel32.NewProc("GlobalAlloc")
	globalLock    = kernel32.NewProc("GlobalLock")
	globalUnlock  = kernel32.NewProc("GlobalUnlock")
	rtlMoveMemory = kernel32.NewProc("RtlMoveMemory")
)

const (
	gmemMoveable  = 0x0002
	cfUnicodeText = 13
)

func copyTextToClipboard(text string) error {
	u16, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	size := uintptr(len(u16)) * 2

	r, _, err := openClipboard.Call(0)
	if r == 0 {
		return err
	}
	defer closeClipboard.Call()
	if r, _, err = emptyClipboard.Call(); r == 0 {
		return err
	}

	h, _, err := globalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return err
	}
	p, _, err := globalLock.Call(h)
	if p == 0 {
		return err
	}
	// Copy through RtlMoveMemory so the locked address only ever travels as a
	// syscall argument (uintptr); converting it to unsafe.Pointer would trip
	// vet's unsafeptr check and is unsound under a moving GC.
	rtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u16[0])), size)
	globalUnlock.Call(h)

	// On success the system owns the handle; it must not be freed.
	if r, _, err := setClipboardData.Call(cfUnicodeText, h); r == 0 {
		return err
	}
	return nil
}
