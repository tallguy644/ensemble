//go:build !windows

package main

import "errors"

func InstallSupported() bool { return false }

func runPowerShell(string) (string, error) { return "", errors.New("Windows only") }

func openTerminal(string, string) error { return errors.New("Windows only") }
