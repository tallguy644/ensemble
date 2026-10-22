//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
)

var ErrNoWindow = errors.New("no app window on this platform")

// RunWindow: outside Windows, Ensemble opens in the browser instead.
func RunWindow(string) error { return ErrNoWindow }

func ShowError(title, msg string) { fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg) }
