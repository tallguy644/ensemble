//go:build windows

package main

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
)

func CanPickFolder() bool { return true }

// PickFolder shows the Windows folder picker on top of the Ensemble window.
func PickFolder() (string, error) {
	script := `[Console]::OutputEncoding=[Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
$owner = New-Object System.Windows.Forms.Form -Property @{TopMost=$true; ShowInTaskbar=$false}
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = 'Choose the project folder for Ensemble to work in'
$d.ShowNewFolderButton = $true
if ($d.ShowDialog($owner) -eq 'OK') { [Console]::Out.Write($d.SelectedPath) }`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass", "-Command", script)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	PrepareCmd(cmd)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New("Couldn't open the folder picker: " + msg)
	}
	return strings.TrimSpace(out.String()), nil
}
