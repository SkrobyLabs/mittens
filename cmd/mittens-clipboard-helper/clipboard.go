//go:build windows
// +build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")

	procGlobalLock    = kernel32.NewProc("GlobalLock")
	procGlobalUnlock  = kernel32.NewProc("GlobalUnlock")
	procGlobalSize    = kernel32.NewProc("GlobalSize")
	procRtlMoveMemory = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfDIB   = 8
	cfDIBV5 = 17
)

// hasClipboardImage checks if the clipboard has image data without opening it.
func hasClipboardImage() bool {
	ret, _, _ := procIsClipboardFormatAvailable.Call(uintptr(cfDIBV5))
	if ret != 0 {
		return true
	}
	ret, _, _ = procIsClipboardFormatAvailable.Call(uintptr(cfDIB))
	return ret != 0
}

// readClipboardImage reads an image from the Windows clipboard and returns
// it as PNG-encoded bytes. Returns an error if no image is available.
//
// The clipboard is held open only long enough to copy the raw DIB bytes out,
// then closed immediately. The PNG conversion happens after the clipboard is
// released to minimize lock duration.
func readClipboardImage() ([]byte, error) {
	// Open the clipboard (NULL window handle — associate with current thread).
	ret, _, _ := procOpenClipboard.Call(0)
	if ret == 0 {
		return nil, fmt.Errorf("OpenClipboard failed")
	}

	// Determine format and copy raw data out, then close ASAP.
	rawData, format, err := copyClipboardData()
	procCloseClipboard.Call() // close immediately, before PNG encoding
	if err != nil {
		return nil, err
	}

	return dibToPNG(rawData, format == cfDIBV5)
}

// copyClipboardData reads raw DIB data from the already-opened clipboard.
// Must be called between OpenClipboard and CloseClipboard.
func copyClipboardData() ([]byte, uint32, error) {
	// Try CF_DIBV5 first (supports alpha), fall back to CF_DIB.
	format := uint32(cfDIBV5)
	hMem, _, _ := procGetClipboardData.Call(uintptr(format))
	if hMem == 0 {
		format = cfDIB
		hMem, _, _ = procGetClipboardData.Call(uintptr(format))
		if hMem == 0 {
			return nil, 0, fmt.Errorf("no image on clipboard")
		}
	}

	size, _, _ := procGlobalSize.Call(hMem)
	if size == 0 {
		return nil, 0, fmt.Errorf("GlobalSize returned 0")
	}

	ptr, _, _ := procGlobalLock.Call(hMem)
	if ptr == 0 {
		return nil, 0, fmt.Errorf("GlobalLock failed")
	}

	data := make([]byte, size)
	procRtlMoveMemory.Call(
		uintptr(unsafe.Pointer(&data[0])),
		ptr,
		size,
	)

	procGlobalUnlock.Call(hMem)

	return data, format, nil
}
