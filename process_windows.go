package main

import (
	"os/exec"
	"syscall"
	"unsafe"
)

func hideCommand(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
func showError(message string) {
	title, _ := syscall.UTF16PtrFromString("Epic IPv6 下载助手")
	body, _ := syscall.UTF16PtrFromString(message)
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), 0x10)
}
func acquireInstance() (uintptr, bool, error) {
	name, _ := syscall.UTF16PtrFromString("Local\\EpicIPv6Helper-SingleInstance-v1")
	h, _, e := syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return 0, false, e
	}
	return h, e == syscall.Errno(183), nil
}
func releaseInstance(h uintptr) { syscall.CloseHandle(syscall.Handle(h)) }
