//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func InstallSupported() bool { return true }

func runPowerShell(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ProgressPreference='SilentlyContinue'; "+script)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	PrepareCmd(cmd)
	err := cmd.Run()
	if ctx.Err() != nil {
		err = errors.New("timed out after 15 minutes")
	}
	return out.String(), err
}

// openTerminal opens a visible PowerShell window running one command and
// leaves it open so you can finish signing in.
func openTerminal(title, command string) error {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = `C:\Windows\System32\cmd.exe`
	}
	title = strings.NewReplacer(`"`, "", "&", "and", "^", "", "|", "", "<", "", ">", "").Replace(title)
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: `"` + comspec + `" /d /c start "` + title + `" powershell.exe -NoExit -NoProfile -Command ` + command,
	}
	PrepareCmd(cmd)
	return cmd.Start()
}
