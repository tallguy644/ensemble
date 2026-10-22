package main

// Phone access: lets your phone drive Ensemble running on this PC, from
// anywhere, through Tailscale (a free private network between your own
// devices). Nothing is opened to the public internet.
//
// When enabled, Ensemble runs a second server on 127.0.0.1:4318 with a
// long-lived pairing token, and asks Tailscale to share it with your other
// devices over HTTPS at https://<this-pc>.<tailnet>.ts.net:8443. If HTTPS isn't
// turned on for your tailnet yet, it falls back to plain HTTP on this PC's
// Tailscale address (still private and encrypted by Tailscale itself).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	phonePort      = 4318
	phoneHTTPSPort = 8443
)

type phoneSettings struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

type TailscaleInfo struct {
	Installed bool     `json:"installed"`
	Running   bool     `json:"running"`
	State     string   `json:"state,omitempty"`
	DNSName   string   `json:"dnsName,omitempty"`
	IPs       []string `json:"ips,omitempty"`
	HTTPS     bool     `json:"https"` // HTTPS certificates enabled for the tailnet
}

type PhoneStatus struct {
	Enabled   bool          `json:"enabled"`
	Serving   bool          `json:"serving"`
	Mode      string        `json:"mode,omitempty"` // "https" or "http"
	URL       string        `json:"url,omitempty"`  // includes the pairing token
	QR        string        `json:"qr,omitempty"`   // data: URL of the QR code
	Tailscale TailscaleInfo `json:"tailscale"`
	Problem   string        `json:"problem,omitempty"`
	FixURL    string        `json:"fixUrl,omitempty"`
}

type PhoneManager struct {
	mu       sync.Mutex
	server   *Server
	mode     string
	url      string
	problem  string
	fixURL   string
	tsCache  *TailscaleInfo
	tsCached time.Time
}

var Phone = &PhoneManager{}

func phoneFile() string { return filepath.Join(HomeDir(), "phone.json") }

func loadPhone() phoneSettings {
	var s phoneSettings
	if b, err := os.ReadFile(phoneFile()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func savePhone(s phoneSettings) {
	EnsureHome()
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(phoneFile(), b, 0o600)
}

func tailscaleExe() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("ProgramFiles"), `C:\Program Files`} {
			p := filepath.Join(base, "Tailscale", "tailscale.exe")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func runTailscale(timeout time.Duration, args ...string) (string, error) {
	exe := tailscaleExe()
	if exe == "" {
		return "", errors.New("Tailscale isn't installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	PrepareCmd(cmd)
	err := cmd.Run()
	if ctx.Err() != nil {
		err = errors.New("timed out")
	}
	return strings.TrimSpace(out.String()), err
}

// TailscaleStatus reads `tailscale status --json`.
func TailscaleStatus(refresh bool) TailscaleInfo {
	p := Phone
	p.mu.Lock()
	if !refresh && p.tsCache != nil && time.Since(p.tsCached) < 15*time.Second {
		info := *p.tsCache
		p.mu.Unlock()
		return info
	}
	p.mu.Unlock()

	info := TailscaleInfo{Installed: tailscaleExe() != ""}
	if info.Installed {
		out, err := runTailscale(10*time.Second, "status", "--json", "--peers=false")
		if err == nil || strings.HasPrefix(out, "{") {
			var st struct {
				BackendState string   `json:"BackendState"`
				CertDomains  []string `json:"CertDomains"`
				Self         struct {
					DNSName      string   `json:"DNSName"`
					TailscaleIPs []string `json:"TailscaleIPs"`
				} `json:"Self"`
			}
			if json.Unmarshal([]byte(out), &st) == nil {
				info.State = st.BackendState
				info.Running = st.BackendState == "Running"
				info.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
				info.IPs = st.Self.TailscaleIPs
				info.HTTPS = len(st.CertDomains) > 0
			}
		}
	}
	p.mu.Lock()
	p.tsCache, p.tsCached = &info, time.Now()
	p.mu.Unlock()
	return info
}

var urlRe = regexp.MustCompile(`https://login\.tailscale\.com/\S+`)

// Start is called when the app launches: it resumes phone access if it was on.
func (p *PhoneManager) Start() {
	if loadPhone().Enabled {
		p.Enable()
	}
}

func (p *PhoneManager) Enable() {
	s := loadPhone()
	s.Enabled = true
	if s.Token == "" {
		s.Token = randomToken(24)
	}
	savePhone(s)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.problem, p.fixURL = "", ""
	ts := p.tsInfoLocked()
	if !ts.Installed {
		p.problem = "Install Tailscale on this PC and on your phone, and sign in to the same account on both."
		p.fixURL = "https://tailscale.com/download"
		p.stopLocked()
		return
	}
	if !ts.Running {
		p.problem = "Tailscale is installed but not connected. Open Tailscale on this PC and sign in."
		p.stopLocked()
		return
	}

	if p.server == nil {
		// Plain-HTTP fallback listens on this PC's Tailscale IPv4 address only.
		extra := []string{}
		for _, ip := range ts.IPs {
			if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil {
				extra = append(extra, ip)
			}
		}
		srv, err := newServerOn(s.Token, phonePort, extra, true)
		if err != nil {
			p.problem = fmt.Sprintf("Couldn't start phone access on port %d: %v", phonePort, err)
			return
		}
		if ts.DNSName != "" {
			srv.AllowHost(ts.DNSName)
			srv.AllowHost(fmt.Sprintf("%s:%d", ts.DNSName, phoneHTTPSPort))
		}
		p.server = srv
		go func() { _ = srv.Serve() }()
	}

	// Prefer HTTPS through Tailscale Serve: installable as a real app on the phone.
	p.mode, p.url = "", ""
	if ts.DNSName != "" {
		out, err := runTailscale(20*time.Second, "serve", "--bg", fmt.Sprintf("--https=%d", phoneHTTPSPort), fmt.Sprintf("http://127.0.0.1:%d", phonePort))
		if err == nil {
			p.mode = "https"
			p.url = fmt.Sprintf("https://%s:%d/?token=%s", ts.DNSName, phoneHTTPSPort, s.Token)
			return
		}
		if m := urlRe.FindString(out); m != "" {
			p.fixURL = m
		}
		p.problem = "Tailscale HTTPS isn't turned on for your account yet, so phone access is using plain HTTP over Tailscale. It works, but your phone can only add it as a shortcut instead of a full-screen app. Turn on HTTPS (button below), then press Re-check."
		if p.fixURL == "" {
			p.fixURL = "https://login.tailscale.com/admin/dns"
		}
	}
	for _, ip := range ts.IPs {
		if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil {
			p.mode = "http"
			p.url = fmt.Sprintf("http://%s:%d/?token=%s", ip, phonePort, s.Token)
			return
		}
	}
	if p.url == "" {
		p.problem = "This PC has no Tailscale address yet. Open Tailscale and make sure it's connected."
	}
}

func (p *PhoneManager) tsInfoLocked() TailscaleInfo {
	p.mu.Unlock()
	defer p.mu.Lock()
	return TailscaleStatus(true)
}

func (p *PhoneManager) stopLocked() {
	if p.server != nil {
		p.server.Close()
		p.server = nil
	}
	if p.mode == "https" {
		go runTailscale(15*time.Second, "serve", fmt.Sprintf("--https=%d", phoneHTTPSPort), "off")
	}
	p.mode, p.url = "", ""
}

func (p *PhoneManager) Disable() {
	s := loadPhone()
	s.Enabled = false
	savePhone(s)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.problem, p.fixURL = "", ""
	p.stopLocked()
}

// ResetPairing makes a new token, so previously paired phones lose access.
func (p *PhoneManager) ResetPairing() {
	s := loadPhone()
	s.Token = randomToken(24)
	savePhone(s)
	if s.Enabled {
		p.mu.Lock()
		p.stopLocked()
		p.mu.Unlock()
		p.Enable()
	}
}

// Shutdown stops serving when the app closes (the Tailscale share is left
// configured so it works again next launch; it just has nothing to reach).
func (p *PhoneManager) Shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		p.server.Close()
		p.server = nil
	}
}

func (p *PhoneManager) Status(refresh bool) PhoneStatus {
	s := loadPhone()
	if refresh && s.Enabled {
		p.Enable()
	}
	ts := TailscaleStatus(refresh)
	p.mu.Lock()
	defer p.mu.Unlock()
	st := PhoneStatus{Enabled: s.Enabled, Serving: p.server != nil, Mode: p.mode, URL: p.url, Tailscale: ts, Problem: p.problem, FixURL: p.fixURL}
	if p.url != "" {
		if png, err := qrcode.Encode(p.url, qrcode.Medium, 512); err == nil {
			st.QR = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
	}
	return st
}
