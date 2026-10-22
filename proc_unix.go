//go:build !windows

package main

import (
	"os/exec"
	"syscall"
	"time"
)

func ResolveCommand(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

func IsBatchFile(string) bool { return false }

func NewCommand(resolved string, args []string) *exec.Cmd { return exec.Command(resolved, args...) }

func ShellCommand(line string) *exec.Cmd { return exec.Command("/bin/sh", "-c", line) }

// PrepareCmd puts the child in its own process group so the whole tree can be stopped.
func PrepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func KillTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	go func() {
		time.Sleep(3 * time.Second)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}()
}

func RefreshPath() {}
