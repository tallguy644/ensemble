//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// EnableConsoleColors turns on ANSI colors in the Windows console.
func EnableConsoleColors() bool {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false // output is redirected
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
