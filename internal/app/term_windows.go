// SPDX-License-Identifier: 0BSD

//go:build windows

package app

import (
	"os"
	"syscall"
	"unsafe"
)

// enableANSI turns on virtual terminal processing for the console so
// ANSI color codes work on Windows 10 and later.
func enableANSI(f *os.File) bool {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getConsoleMode := kernel32.NewProc("GetConsoleMode")
	setConsoleMode := kernel32.NewProc("SetConsoleMode")

	var mode uint32
	h := syscall.Handle(f.Fd())
	r, _, _ := getConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return false
	}
	const enableVirtualTerminalProcessing = 0x0004
	r, _, _ = setConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}
