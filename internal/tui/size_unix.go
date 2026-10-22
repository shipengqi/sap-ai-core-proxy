//go:build !windows

package tui

import (
	"os"
	"syscall"
	"unsafe"
)

type winsize struct {
	Row, Col       uint16
	Xpixel, Ypixel uint16
}

func terminalSize() (int, int) {
	ws := &winsize{}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(ws)))
	if errno != 0 || ws.Col == 0 || ws.Row == 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}
