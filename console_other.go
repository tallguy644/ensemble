//go:build !windows

package main

import "os"

func EnableConsoleColors() bool {
	st, err := os.Stdout.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
