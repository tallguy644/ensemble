package main

// Phone access: lets your phone drive Ensemble running on this PC, from
// anywhere, through Tailscale (a free private network between your own
// devices). Nothing is opened to the public internet.
//
// Tailscale is built into Ensemble (Tailscale's own tsnet library), so the PC
// doesn't need the Tailscale app. Ensemble joins your Tailscale account as its
// own device called "ensemble", and serves the phone app at
// https://ensemble.<your-tailnet>.ts.net with a long-lived pairing key. If
// HTTPS isn't turned on for your account, it uses plain HTTP inside Tailscale
// (still private and encrypted by Tailscale itself).
//
// The phone still needs the Tailscale app: that's what lets it into your
// private network.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const tsHostname = "ensemble"

type phoneSettings struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

// TSState is what Ensemble needs to know about its built-in Tailscale node.
type TSState struct {
	BackendState string // NoState, NeedsLogin, Starting, Running, Stopped...
	AuthURL      string
	DNSName      string // ensemble.tail1234.ts.net
	IPv4         string
	Tailnet      string
	HTTPS        bool // HTTPS certificates are turned on for the tailnet
}

// tsNode is the embedded Tailscale device (see tsnode.go).
type tsNode interface {
	State(ctx context.Context) (TSState, error)
	Login(ctx context.Context) error
	Listen(https bool) (net.Listener, error)
	Logout(ctx context.Context) error
	Close() error
}

// newTSNode is replaced in tests.
var newTSNode = newRealTSNode

// phonePoll is how often the node's state is checked.
var phonePoll = 2 * time.Second

type PhoneStatus struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"` // off, starting, needs-login, running, error
	AuthURL string `json:"authUrl,omitempty"`
	Tailnet string `json:"tailnet,omitempty"`
	Mode    string `json:"mode,omitempty"` // https or http
	URL     string `json:"url,omitempty"`  // includes the pairing key
	QR      string `json:"qr,omitempty"`
	Problem string `json:"problem,omitempty"`
	FixURL  string `json:"fixUrl,omitempty"`
}

type PhoneManager struct {
	mu        sync.Mutex
	node      tsNode
	server    *Server
	stop      context.CancelFunc
	state     string
	authURL   string
	tailnet   string
	mode      string
	url       string
	problem   string
	fixURL    string
	loginSent time.Time
	logURL    string // login link seen in Tailscale's log, as a fallback
}

var Phone = &PhoneManager{state: "off"}

func phoneFile() string { return filepath.Join(HomeDir(), "phone.json") }
func tsDir() string     { return filepath.Join(HomeDir(), "tailscale") }

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

var loginURLRe = regexp.MustCompile(`https://login\.tailscale\.com/a/[A-Za-z0-9]+`)

// tsLog writes Tailscale's (chatty) log to ~/.ensemble/tailscale.log, capped
// in size, and picks out the sign-in link.
func (p *PhoneManager) tsLog() func(format string, args ...any) {
	var mu sync.Mutex
	path := filepath.Join(HomeDir(), "tailscale.log")
	return func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if m := loginURLRe.FindString(line); m != "" {
			p.mu.Lock()
			p.logURL = m
			p.mu.Unlock()
		}
		mu.Lock()
		defer mu.Unlock()
		if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
			_ = os.Truncate(path, 0)
		}
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
			f.Close()
		}
	}
}

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
	if p.stop != nil {
		return // already running
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.stop = cancel
	p.state, p.problem, p.fixURL = "starting", "", ""
	go p.run(ctx)
}

// run starts the built-in Tailscale node and keeps phone access in step with it.
func (p *PhoneManager) run(ctx context.Context) {
	EnsureHome()
	_ = os.MkdirAll(tsDir(), 0o700)
	node, err := newTSNode(tsDir(), tsHostname, p.tsLog())
	if err != nil {
		p.mu.Lock()
		p.state, p.problem = "error", "Couldn't start Tailscale: "+err.Error()
		p.mu.Unlock()
		return
	}
	p.mu.Lock()
	if ctx.Err() != nil {
		p.mu.Unlock()
		node.Close()
		return
	}
	p.node = node
	p.mu.Unlock()

	tick := time.NewTicker(phonePoll)
	defer tick.Stop()
	for {
		p.sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (p *PhoneManager) sync(ctx context.Context) {
	p.mu.Lock()
	node := p.node
	p.mu.Unlock()
	if node == nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := node.State(sctx)
	cancel()
	if ctx.Err() != nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.state, p.problem = "error", "Tailscale isn't responding: "+err.Error()
		return
	}
	p.tailnet = st.Tailnet
	switch st.BackendState {
	case "NeedsLogin", "NeedsMachineAuth":
		p.closeServerLocked()
		p.state = "needs-login"
		p.problem = ""
		if st.BackendState == "NeedsMachineAuth" {
			p.problem = "Approve the new “ensemble” device in your Tailscale admin console."
			p.fixURL = "https://login.tailscale.com/admin/machines"
		}
		p.authURL = st.AuthURL
		if p.authURL == "" {
			p.authURL = p.logURL
		}
		if p.authURL == "" && time.Since(p.loginSent) > 20*time.Second {
			p.loginSent = time.Now()
			go node.Login(context.Background())
		}
		return
	case "Running":
	default:
		if p.server == nil {
			p.state = "starting"
		}
		return
	}

	// Running: serve the phone app over HTTPS when the account allows it.
	p.authURL, p.logURL = "", ""
	want := "http"
	if st.HTTPS {
		want = "https"
	}
	if p.server != nil && p.mode == want {
		p.state = "running"
		return
	}
	p.closeServerLocked()
	ln, err := node.Listen(want == "https")
	if err != nil {
		p.state, p.problem = "error", "Couldn't open phone access on Tailscale: "+err.Error()
		return
	}
	srv := newServerWithListeners(loadPhone().Token, []net.Listener{ln}, true)
	for _, h := range []string{st.DNSName, st.DNSName + ":443", st.DNSName + ":80", st.IPv4, st.IPv4 + ":80", tsHostname, tsHostname + ":80"} {
		if h != "" && h[0] != ':' {
			srv.AllowHost(h)
		}
	}
	go func() { _ = srv.Serve() }()
	p.server, p.mode, p.state = srv, want, "running"
	host := st.DNSName
	if host == "" {
		host = st.IPv4
	}
	p.url = fmt.Sprintf("%s://%s/?token=%s", want, host, srv.Token)
	p.problem, p.fixURL = "", ""
	if want == "http" {
		p.problem = "HTTPS isn't turned on for your Tailscale account, so your phone can only add Ensemble as a browser shortcut instead of a full-screen app. Turn on HTTPS Certificates (button below); Ensemble switches over automatically."
		p.fixURL = "https://login.tailscale.com/admin/dns"
	}
}

func (p *PhoneManager) closeServerLocked() {
	if p.server != nil {
		p.server.Close()
		p.server = nil
	}
	p.mode, p.url = "", ""
}

func (p *PhoneManager) stopLocked() {
	if p.stop != nil {
		p.stop()
		p.stop = nil
	}
	p.closeServerLocked()
	if p.node != nil {
		n := p.node
		p.node = nil
		go n.Close()
	}
	p.state, p.authURL, p.problem, p.fixURL = "off", "", "", ""
}

func (p *PhoneManager) Disable() {
	s := loadPhone()
	s.Enabled = false
	savePhone(s)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
}

// SignOut disconnects Ensemble's built-in Tailscale device from your account.
func (p *PhoneManager) SignOut() {
	p.mu.Lock()
	node := p.node
	p.mu.Unlock()
	if node != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = node.Logout(ctx)
		cancel()
	}
	p.Disable()
	_ = os.RemoveAll(tsDir())
}

// ResetPairing makes a new pairing key, so previously paired phones lose access.
func (p *PhoneManager) ResetPairing() {
	s := loadPhone()
	s.Token = randomToken(24)
	savePhone(s)
	p.mu.Lock()
	p.closeServerLocked() // reopened with the new key on the next check
	p.mu.Unlock()
	p.Refresh()
}

// Refresh checks Tailscale right away instead of waiting for the next poll.
func (p *PhoneManager) Refresh() {
	p.mu.Lock()
	running := p.stop != nil
	p.mu.Unlock()
	if running {
		p.sync(context.Background())
	}
}

// Shutdown stops phone access when the app closes (the setting is kept).
func (p *PhoneManager) Shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
}

func (p *PhoneManager) Status() PhoneStatus {
	enabled := loadPhone().Enabled
	p.mu.Lock()
	defer p.mu.Unlock()
	st := PhoneStatus{Enabled: enabled, State: p.state, AuthURL: p.authURL, Tailnet: p.tailnet, Mode: p.mode, URL: p.url, Problem: p.problem, FixURL: p.fixURL}
	if !enabled {
		st.State = "off"
	}
	if p.url != "" {
		if png, err := qrcode.Encode(p.url, qrcode.Medium, 512); err == nil {
			st.QR = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
	}
	return st
}
