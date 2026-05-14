//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func init() {
	handle := windows.Handle(os.Stdout.Fd())
	var mode uint32
	_ = windows.GetConsoleMode(handle, &mode)
	_ = windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
}
