//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const createNoWindow = 0x08000000

// RefreshPath adds the user's and system's current PATH from the registry, so
// tools installed after Windows started (claude, codex, agy) are still found,
// plus the folders their installers usually use.
func RefreshPath() {
	parts := strings.Split(os.Getenv("PATH"), ";")
	seen := map[string]bool{}
	for _, p := range parts {
		seen[strings.ToLower(strings.TrimRight(p, `\`))] = true
	}
	add := func(list string) {
		for _, p := range strings.Split(list, ";") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if exp, err := registry.ExpandString(p); err == nil {
				p = exp
			}
			k := strings.ToLower(strings.TrimRight(p, `\`))
			if !seen[k] {
				seen[k] = true
				parts = append(parts, p)
			}
		}
	}
	for _, loc := range []struct {
		root registry.Key
		path string
	}{
		{registry.CURRENT_USER, `Environment`},
		{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
	} {
		if k, err := registry.OpenKey(loc.root, loc.path, registry.QUERY_VALUE); err == nil {
			if v, _, err := k.GetStringValue("Path"); err == nil {
				add(v)
			}
			k.Close()
		}
	}
	home, _ := os.UserHomeDir()
	extra := []string{filepath.Join(home, ".local", "bin")}
	if ad := os.Getenv("APPDATA"); ad != "" {
		extra = append(extra, filepath.Join(ad, "npm"))
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		extra = append(extra, filepath.Join(la, "Microsoft", "WinGet", "Links"))
	}
	add(strings.Join(extra, ";"))
	os.Setenv("PATH", strings.Join(parts, ";"))
}

func ResolveCommand(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

func IsBatchFile(resolved string) bool {
	l := strings.ToLower(resolved)
	return strings.HasSuffix(l, ".cmd") || strings.HasSuffix(l, ".bat")
}

var cmdMeta = regexp.MustCompile(`([()\][%!^"` + "`" + `<>&|;, *?])`)

func escapeCmdArg(arg string, double bool) string {
	a := regexp.MustCompile(`(\\*)"`).ReplaceAllString(arg, `$1$1\"`)
	a = regexp.MustCompile(`(\\*)$`).ReplaceAllString(a, `$1$1`)
	a = cmdMeta.ReplaceAllString(`"`+a+`"`, `^$1`)
	if double {
		a = cmdMeta.ReplaceAllString(a, `^$1`)
	}
	return a
}

// NewCommand runs .exe files directly. npm-style .cmd launchers have to go
// through cmd.exe with careful escaping.
func NewCommand(resolved string, args []string) *exec.Cmd {
	if !IsBatchFile(resolved) {
		return exec.Command(resolved, args...)
	}
	double := regexp.MustCompile(`(?i)node_modules[\\/]\.bin[\\/][^\\/]+\.cmd$`).MatchString(resolved)
	parts := []string{cmdMeta.ReplaceAllString(resolved, `^$1`)}
	for _, a := range args {
		parts = append(parts, escapeCmdArg(a, double))
	}
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = `C:\Windows\System32\cmd.exe`
	}
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + comspec + `" /d /s /c "` + strings.Join(parts, " ") + `"`}
	return cmd
}

func ShellCommand(line string) *exec.Cmd {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = `C:\Windows\System32\cmd.exe`
	}
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + comspec + `" /d /s /c "` + line + `"`}
	return cmd
}

// PrepareCmd stops child programs from flashing console windows.
func PrepareCmd(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

func KillTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	k := exec.Command("taskkill", "/pid", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	PrepareCmd(k)
	if err := k.Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
