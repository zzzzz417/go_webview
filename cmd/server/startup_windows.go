//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

const (
	mbIconError = 0x00000010
	mbOK        = 0x00000000
)

func showStartupError(title, message string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	messageBoxW := user32.NewProc("MessageBoxW")
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	messagePtr, _ := syscall.UTF16PtrFromString(message)
	_, _, _ = messageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(messagePtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(mbOK|mbIconError),
	)
}
