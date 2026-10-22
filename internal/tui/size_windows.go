//go:build windows

package tui

func terminalSize() (int, int) { return 80, 24 }
