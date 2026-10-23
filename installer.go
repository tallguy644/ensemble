package main

// One-click setup: runs the vendors' official installers and opens their
// sign-in in a terminal window, so you don't have to paste commands.

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// gitInstallScript installs Git for Windows through winget (built into Windows 10/11).
const gitInstallScript = "winget install --id Git.Git -e --source winget --accept-package-agreements --accept-source-agreements --silent"

var installMu sync.Mutex

func installScriptFor(id string) (label, script string, err error) {
	if id == "git" {
		return "Git", gitInstallScript, nil
	}
	cfg, err := LoadConfig()
	if err != nil {
		return "", "", err
	}
	a := cfg.Agents[id]
	if a == nil || a.InstallScript == "" {
		return "", "", fmt.Errorf("there's no one-click install for %q", id)
	}
	return a.Label, a.InstallScript, nil
}

// Install runs the official installer for an AI (or "git") and reports what it printed.
func Install(id string) (string, error) {
	if !InstallSupported() {
		return "", errors.New("One-click install is only available on Windows.")
	}
	label, script, err := installScriptFor(id)
	if err != nil {
		return "", err
	}
	if !installMu.TryLock() {
		return "", errors.New("Another install is still running. Try again when it finishes.")
	}
	defer installMu.Unlock()
	out, err := runPowerShell(script)
	RefreshPath()
	ClearStatusCache()
	out = strings.TrimSpace(out)
	if len(out) > 4000 {
		out = "..." + out[len(out)-4000:]
	}
	if err != nil {
		return out, fmt.Errorf("Installing %s didn't finish: %v", label, err)
	}
	return out, nil
}

// SignIn opens a terminal window running the AI's sign-in command.
func SignIn(id string) error {
	if !InstallSupported() {
		return errors.New("Sign-in from Ensemble is only available on Windows.")
	}
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	a := cfg.Agents[id]
	if a == nil || a.LoginCmd == "" {
		return fmt.Errorf("there's no sign-in command for %q", id)
	}
	RefreshPath()
	return openTerminal("Sign in to "+a.Label, a.LoginCmd)
}
